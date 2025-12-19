package transfer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/johndauphine/mssql-pg-migrate/internal/config"
	"github.com/johndauphine/mssql-pg-migrate/internal/progress"
	"github.com/johndauphine/mssql-pg-migrate/internal/source"
	"github.com/johndauphine/mssql-pg-migrate/internal/target"
)

// Job represents a data transfer job
type Job struct {
	Table     source.Table
	Partition *source.Partition
}

// TransferStats tracks timing statistics for profiling
type TransferStats struct {
	QueryTime time.Duration
	ScanTime  time.Duration
	WriteTime time.Duration
	Rows      int64
}

func (s *TransferStats) String() string {
	total := s.QueryTime + s.ScanTime + s.WriteTime
	if total == 0 {
		return "no data"
	}
	return fmt.Sprintf("query=%.1fs (%.0f%%), scan=%.1fs (%.0f%%), write=%.1fs (%.0f%%), rows=%d",
		s.QueryTime.Seconds(), float64(s.QueryTime)/float64(total)*100,
		s.ScanTime.Seconds(), float64(s.ScanTime)/float64(total)*100,
		s.WriteTime.Seconds(), float64(s.WriteTime)/float64(total)*100,
		s.Rows)
}

// Execute runs a transfer job using the optimal pagination strategy
func Execute(
	ctx context.Context,
	srcPool *source.Pool,
	tgtPool *target.Pool,
	cfg *config.Config,
	job Job,
	prog *progress.Tracker,
) (*TransferStats, error) {
	// Handle truncation based on job type
	if job.Partition == nil {
		// Non-partitioned table: truncate here (no race possible)
		if err := tgtPool.TruncateTable(ctx, cfg.Target.Schema, job.Table.Name); err != nil {
			// Ignore truncate errors (table might not exist)
		}
	} else {
		// Partitioned table: already truncated in orchestrator, just cleanup for idempotent retry
		if job.Table.SupportsKeysetPagination() {
			if err := cleanupPartitionData(ctx, tgtPool.Pool(), cfg.Target.Schema, &job); err != nil {
				fmt.Printf("Warning: cleanup partition data for %s: %v\n", job.Table.Name, err)
			}
		}
	}

	// Build column list
	cols := make([]string, len(job.Table.Columns))
	colTypes := make([]string, len(job.Table.Columns))
	for i, c := range job.Table.Columns {
		cols[i] = c.Name
		colTypes[i] = strings.ToLower(c.DataType)
	}

	// Choose pagination strategy
	if job.Table.SupportsKeysetPagination() {
		return executeKeysetPagination(ctx, srcPool.DB(), tgtPool.Pool(), cfg, job, cols, colTypes, prog)
	}

	// Fall back to ROW_NUMBER pagination for composite/varchar PKs or no PK
	return executeRowNumberPagination(ctx, srcPool.DB(), tgtPool.Pool(), cfg, job, cols, colTypes, prog)
}

// cleanupPartitionData removes any existing data for a partition's PK range (idempotent retry)
func cleanupPartitionData(ctx context.Context, pool *pgxpool.Pool, schema string, job *Job) error {
	if job.Partition == nil || job.Partition.MinPK == nil {
		return nil
	}

	pkCol := job.Table.PrimaryKey[0]
	query := fmt.Sprintf(
		`DELETE FROM %s.%q WHERE %q >= $1 AND %q <= $2`,
		schema, job.Table.Name, pkCol, pkCol,
	)

	_, err := pool.Exec(ctx, query, job.Partition.MinPK, job.Partition.MaxPK)
	return err
}

// executeKeysetPagination uses WHERE pk > last_pk for efficient pagination
func executeKeysetPagination(
	ctx context.Context,
	db *sql.DB,
	pool *pgxpool.Pool,
	cfg *config.Config,
	job Job,
	cols, colTypes []string,
	prog *progress.Tracker,
) (*TransferStats, error) {
	stats := &TransferStats{}
	pkCol := job.Table.PrimaryKey[0]
	colList := "[" + strings.Join(cols, "], [") + "]"

	var lastPK any
	var minPK, maxPK any

	if job.Partition != nil {
		minPK = job.Partition.MinPK
		maxPK = job.Partition.MaxPK
		// Start from one before minPK to include minPK in first chunk
		lastPK = decrementPK(minPK)
	}

	chunkSize := cfg.Migration.ChunkSize
	totalTransferred := int64(0)

	for {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		default:
		}

		var query string
		var args []any

		// Use NOLOCK unless strict consistency is enabled
		tableHint := "WITH (NOLOCK)"
		if cfg.Migration.StrictConsistency {
			tableHint = ""
		}

		if job.Partition != nil {
			// Keyset pagination within partition bounds
			query = fmt.Sprintf(`
				SELECT TOP (@limit) %s
				FROM [%s].[%s] %s
				WHERE [%s] > @lastPK AND [%s] <= @maxPK
				ORDER BY [%s]
			`, colList, job.Table.Schema, job.Table.Name, tableHint, pkCol, pkCol, pkCol)
			args = []any{
				sql.Named("limit", chunkSize),
				sql.Named("lastPK", lastPK),
				sql.Named("maxPK", maxPK),
			}
		} else {
			// Keyset pagination for entire table
			if lastPK == nil {
				// First chunk - get minimum PK value first
				var firstPK any
				err := db.QueryRowContext(ctx,
					fmt.Sprintf("SELECT MIN([%s]) FROM [%s].[%s] %s", pkCol, job.Table.Schema, job.Table.Name, tableHint)).
					Scan(&firstPK)
				if err != nil || firstPK == nil {
					return stats, nil // Empty table
				}
				lastPK = decrementPK(firstPK)
			}

			query = fmt.Sprintf(`
				SELECT TOP (@limit) %s
				FROM [%s].[%s] %s
				WHERE [%s] > @lastPK
				ORDER BY [%s]
			`, colList, job.Table.Schema, job.Table.Name, tableHint, pkCol, pkCol)
			args = []any{
				sql.Named("limit", chunkSize),
				sql.Named("lastPK", lastPK),
			}
		}

		// Time the query
		queryStart := time.Now()
		rows, err := db.QueryContext(ctx, query, args...)
		stats.QueryTime += time.Since(queryStart)
		if err != nil {
			return stats, fmt.Errorf("keyset query: %w", err)
		}

		// Time the scan
		scanStart := time.Now()
		chunk, newLastPK, err := scanRows(rows, cols, colTypes)
		rows.Close()
		stats.ScanTime += time.Since(scanStart)
		if err != nil {
			return stats, fmt.Errorf("scanning rows: %w", err)
		}

		if len(chunk) == 0 {
			break
		}

		// Find PK column index for updating lastPK
		pkIdx := 0
		for i, c := range cols {
			if c == pkCol {
				pkIdx = i
				break
			}
		}
		lastPK = chunk[len(chunk)-1][pkIdx]

		// Time the write
		writeStart := time.Now()
		if err := writeChunk(ctx, pool, cfg.Target.Schema, job.Table.Name, cols, chunk); err != nil {
			return stats, fmt.Errorf("writing chunk: %w", err)
		}
		stats.WriteTime += time.Since(writeStart)

		prog.Add(int64(len(chunk)))
		totalTransferred += int64(len(chunk))
		stats.Rows = totalTransferred

		// Update lastPK for next iteration
		if newLastPK != nil {
			lastPK = newLastPK
		}

		if len(chunk) < chunkSize {
			break
		}
	}

	return stats, nil
}

// executeRowNumberPagination uses ROW_NUMBER for composite/varchar PKs
func executeRowNumberPagination(
	ctx context.Context,
	db *sql.DB,
	pool *pgxpool.Pool,
	cfg *config.Config,
	job Job,
	cols, colTypes []string,
	prog *progress.Tracker,
) (*TransferStats, error) {
	stats := &TransferStats{}
	colList := "[" + strings.Join(cols, "], [") + "]"

	// Build ORDER BY clause from PK columns
	// Tables without PK cannot be migrated safely - fail fast
	if len(job.Table.PrimaryKey) == 0 {
		return nil, fmt.Errorf("table %s has no primary key - cannot guarantee data correctness with ROW_NUMBER pagination. "+
			"Add a primary key to the table or exclude it from migration", job.Table.FullName())
	}

	pkCols := make([]string, len(job.Table.PrimaryKey))
	for i, pk := range job.Table.PrimaryKey {
		pkCols[i] = fmt.Sprintf("[%s]", pk)
	}
	orderBy := strings.Join(pkCols, ", ")

	// Use NOLOCK unless strict consistency is enabled
	tableHint := "WITH (NOLOCK)"
	if cfg.Migration.StrictConsistency {
		tableHint = ""
	}

	chunkSize := cfg.Migration.ChunkSize
	rowNum := int64(0)

	for {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		default:
		}

		var query string
		var args []any

		// ROW_NUMBER pagination
		query = fmt.Sprintf(`
			WITH numbered AS (
				SELECT %s, ROW_NUMBER() OVER (ORDER BY %s) as __rn
				FROM [%s].[%s] %s
			)
			SELECT %s FROM numbered
			WHERE __rn > @rowNum AND __rn <= @rowNumEnd
			ORDER BY __rn
		`, colList, orderBy, job.Table.Schema, job.Table.Name, tableHint, colList)

		args = []any{
			sql.Named("rowNum", rowNum),
			sql.Named("rowNumEnd", rowNum+int64(chunkSize)),
		}

		// Time the query
		queryStart := time.Now()
		rows, err := db.QueryContext(ctx, query, args...)
		stats.QueryTime += time.Since(queryStart)
		if err != nil {
			return stats, fmt.Errorf("row_number query: %w", err)
		}

		// Time the scan
		scanStart := time.Now()
		chunk, _, err := scanRows(rows, cols, colTypes)
		rows.Close()
		stats.ScanTime += time.Since(scanStart)
		if err != nil {
			return stats, fmt.Errorf("scanning rows: %w", err)
		}

		if len(chunk) == 0 {
			break
		}

		// Time the write
		writeStart := time.Now()
		if err := writeChunk(ctx, pool, cfg.Target.Schema, job.Table.Name, cols, chunk); err != nil {
			return stats, fmt.Errorf("writing chunk at row %d: %w", rowNum, err)
		}
		stats.WriteTime += time.Since(writeStart)

		prog.Add(int64(len(chunk)))
		rowNum += int64(len(chunk))
		stats.Rows = rowNum

		if len(chunk) < chunkSize {
			break
		}
	}

	return stats, nil
}

// scanRows scans database rows into a slice of values with proper type handling
func scanRows(rows *sql.Rows, cols, colTypes []string) ([][]any, any, error) {
	numCols := len(cols)
	var result [][]any
	var lastPK any

	for rows.Next() {
		row := make([]any, numCols)
		ptrs := make([]any, numCols)
		for i := range row {
			ptrs[i] = &row[i]
		}

		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}

		// Process values for PostgreSQL compatibility
		for i, val := range row {
			row[i] = processValue(val, colTypes[i])
		}

		result = append(result, row)
		if len(result) > 0 {
			lastPK = result[len(result)-1][0] // Assume first column is PK for keyset
		}
	}

	return result, lastPK, rows.Err()
}

// processValue handles type conversions for PostgreSQL compatibility
func processValue(val any, colType string) any {
	if val == nil {
		return nil
	}

	switch colType {
	case "binary", "varbinary", "image":
		// Convert binary data to hex format for bytea
		switch v := val.(type) {
		case []byte:
			if len(v) == 0 {
				return nil
			}
			return v // pgx handles []byte directly
		}
	case "uniqueidentifier":
		// Handle UUID conversion
		switch v := val.(type) {
		case []byte:
			if len(v) == 16 {
				// SQL Server GUID to PostgreSQL UUID
				return formatUUID(v)
			}
			return string(v)
		case string:
			return v
		}
	case "bit":
		// Convert bit to boolean
		switch v := val.(type) {
		case bool:
			return v
		case int64:
			return v != 0
		case int:
			return v != 0
		}
	case "datetime", "datetime2", "smalldatetime":
		// Ensure proper timestamp format
		switch v := val.(type) {
		case time.Time:
			// Handle SQL Server minimum datetime (1753-01-01)
			if v.Year() < 1 {
				return nil
			}
			return v
		}
	case "datetimeoffset":
		// Handle datetimeoffset with timezone
		switch v := val.(type) {
		case time.Time:
			if v.Year() < 1 {
				return nil
			}
			return v
		}
	}

	return val
}

// formatUUID converts SQL Server GUID bytes to UUID string
func formatUUID(b []byte) string {
	if len(b) != 16 {
		return hex.EncodeToString(b)
	}
	// SQL Server stores GUIDs in mixed-endian format
	// Convert to standard UUID format
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[3], b[2], b[1], b[0], // time_low (reversed)
		b[5], b[4], // time_mid (reversed)
		b[7], b[6], // time_hi_and_version (reversed)
		b[8], b[9], // clock_seq
		b[10], b[11], b[12], b[13], b[14], b[15]) // node
}

// decrementPK returns a value that is less than the given PK value
func decrementPK(pk any) any {
	switch v := pk.(type) {
	case int64:
		return v - 1
	case int32:
		return v - 1
	case int:
		return v - 1
	default:
		return pk
	}
}

func writeChunk(ctx context.Context, pool *pgxpool.Pool, schema, table string, cols []string, rows [][]any) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Disable statement timeout for this operation
	_, err = conn.Exec(ctx, "SET statement_timeout = 0")
	if err != nil {
		return fmt.Errorf("setting statement timeout: %w", err)
	}

	// Use COPY for bulk insert
	_, err = conn.Conn().CopyFrom(
		ctx,
		pgx.Identifier{schema, table},
		cols,
		pgx.CopyFromRows(rows),
	)

	return err
}

// ValidateBinaryData ensures binary data is properly formatted
func ValidateBinaryData(data []byte) []byte {
	if data == nil || len(data) == 0 {
		return nil
	}
	return data
}

// FormatBytea formats binary data for PostgreSQL bytea column
func FormatBytea(data []byte) string {
	if data == nil || len(data) == 0 {
		return ""
	}
	var buf bytes.Buffer
	buf.WriteString("\\x")
	buf.WriteString(hex.EncodeToString(data))
	return buf.String()
}
