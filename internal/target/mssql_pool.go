package target

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/johndauphine/mssql-pg-migrate/internal/config"
	"github.com/johndauphine/mssql-pg-migrate/internal/source"
	"github.com/johndauphine/mssql-pg-migrate/internal/typemap"
	mssql "github.com/microsoft/go-mssqldb"
)

// MSSQLPool manages a pool of SQL Server target connections
type MSSQLPool struct {
	db           *sql.DB
	config       *config.TargetConfig
	maxConns     int
	rowsPerBatch int // Hint for bulk copy optimizer
	compatLevel  int // Database compatibility level (e.g., 130 for SQL Server 2016)
}

// NewMSSQLPool creates a new SQL Server target connection pool
func NewMSSQLPool(cfg *config.TargetConfig, maxConns int, rowsPerBatch int) (*MSSQLPool, error) {
	trustCert := "false"
	if cfg.TrustServerCert {
		trustCert = "true"
	}
	dsn := fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s&encrypt=%s&TrustServerCertificate=%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database, cfg.Encrypt, trustCert)

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening connection: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns / 4)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	// Query database compatibility level (used for MERGE/EXCEPT support check)
	var compatLevel int
	err = db.QueryRow(`
		SELECT compatibility_level
		FROM sys.databases
		WHERE name = DB_NAME()
	`).Scan(&compatLevel)
	if err != nil {
		// Non-fatal - just log and continue with level 0
		compatLevel = 0
	}

	return &MSSQLPool{
		db:           db,
		config:       cfg,
		maxConns:     maxConns,
		rowsPerBatch: rowsPerBatch,
		compatLevel:  compatLevel,
	}, nil
}

// Close closes all connections in the pool
func (p *MSSQLPool) Close() {
	p.db.Close()
}

// DB returns the underlying database connection
func (p *MSSQLPool) DB() *sql.DB {
	return p.db
}

// MaxConns returns the configured maximum connections
func (p *MSSQLPool) MaxConns() int {
	return p.maxConns
}

// DBType returns the database type
func (p *MSSQLPool) DBType() string {
	return "mssql"
}

// CompatLevel returns the database compatibility level (e.g., 130 for SQL Server 2016)
func (p *MSSQLPool) CompatLevel() int {
	return p.compatLevel
}

// CreateSchema creates the target schema if it doesn't exist
func (p *MSSQLPool) CreateSchema(ctx context.Context, schema string) error {
	// Check if schema exists
	var exists int
	err := p.db.QueryRowContext(ctx,
		"SELECT 1 FROM sys.schemas WHERE name = @schema",
		sql.Named("schema", schema)).Scan(&exists)
	if err == sql.ErrNoRows {
		// Create schema
		_, err = p.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", quoteMSSQLIdent(schema)))
		return err
	}
	return err
}

// CreateTable creates a table from source metadata
func (p *MSSQLPool) CreateTable(ctx context.Context, t *source.Table, targetSchema string) error {
	return p.CreateTableWithOptions(ctx, t, targetSchema, false)
}

// CreateTableWithOptions creates a table (unlogged option ignored for MSSQL)
func (p *MSSQLPool) CreateTableWithOptions(ctx context.Context, t *source.Table, targetSchema string, unlogged bool) error {
	ddl := GenerateMSSQLDDL(t, targetSchema)

	_, err := p.db.ExecContext(ctx, ddl)
	if err != nil {
		return fmt.Errorf("creating table %s: %w", t.FullName(), err)
	}

	return nil
}

// SetTableLogged is a no-op for SQL Server (all tables are logged)
func (p *MSSQLPool) SetTableLogged(ctx context.Context, schema, table string) error {
	return nil
}

// TruncateTable truncates a table
func (p *MSSQLPool) TruncateTable(ctx context.Context, schema, table string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE %s", qualifyMSSQLTable(schema, table)))
	return err
}

// DropTable drops a table if it exists
func (p *MSSQLPool) DropTable(ctx context.Context, schema, table string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", qualifyMSSQLTable(schema, table)))
	return err
}

// TableExists checks if a table exists in the schema
func (p *MSSQLPool) TableExists(ctx context.Context, schema, table string) (bool, error) {
	var exists int
	err := p.db.QueryRowContext(ctx, `
		SELECT 1 FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = @schema AND TABLE_NAME = @table
	`, sql.Named("schema", schema), sql.Named("table", table)).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// CreatePrimaryKey creates a primary key on the table
func (p *MSSQLPool) CreatePrimaryKey(ctx context.Context, t *source.Table, targetSchema string) error {
	if len(t.PrimaryKey) == 0 {
		return nil
	}

	pkCols := ""
	for i, col := range t.PrimaryKey {
		if i > 0 {
			pkCols += ", "
		}
		pkCols += quoteMSSQLIdent(col)
	}

	sql := fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s)",
		qualifyMSSQLTable(targetSchema, t.Name), pkCols)

	_, err := p.db.ExecContext(ctx, sql)
	return err
}

// GetRowCount returns the row count for a table
func (p *MSSQLPool) GetRowCount(ctx context.Context, schema, table string) (int64, error) {
	var count int64
	err := p.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", qualifyMSSQLTable(schema, table))).Scan(&count)
	return count, err
}

// HasPrimaryKey checks if a table has a primary key constraint
func (p *MSSQLPool) HasPrimaryKey(ctx context.Context, schema, table string) (bool, error) {
	var exists int
	err := p.db.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS (
			SELECT 1 FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
			WHERE CONSTRAINT_TYPE = 'PRIMARY KEY'
			AND TABLE_SCHEMA = @schema
			AND TABLE_NAME = @table
		) THEN 1 ELSE 0 END
	`, sql.Named("schema", schema), sql.Named("table", table)).Scan(&exists)
	return exists == 1, err
}

// ResetSequence resets identity sequence to max value
func (p *MSSQLPool) ResetSequence(ctx context.Context, schema string, t *source.Table) error {
	// Find identity column
	var identityCol string
	for _, c := range t.Columns {
		if c.IsIdentity {
			identityCol = c.Name
			break
		}
	}

	if identityCol == "" {
		return nil
	}

	// Get max value from the table
	var maxVal int64
	err := p.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COALESCE(MAX(%s), 0) FROM %s", quoteMSSQLIdent(identityCol), qualifyMSSQLTable(schema, t.Name))).Scan(&maxVal)
	if err != nil {
		return fmt.Errorf("getting max value for %s.%s: %w", t.Name, identityCol, err)
	}

	if maxVal == 0 {
		return nil // Empty table, no need to reset
	}

	// Reseed identity
	_, err = p.db.ExecContext(ctx, fmt.Sprintf("DBCC CHECKIDENT ('%s', RESEED, %d)", qualifyMSSQLTable(schema, t.Name), maxVal))
	return err
}

// CreateIndex creates an index on the target table
func (p *MSSQLPool) CreateIndex(ctx context.Context, t *source.Table, idx *source.Index, targetSchema string) error {
	// Build column list
	cols := make([]string, len(idx.Columns))
	for i, col := range idx.Columns {
		cols[i] = quoteMSSQLIdent(col)
	}

	unique := ""
	if idx.IsUnique {
		unique = "UNIQUE "
	}

	// Generate index name (SQL Server has length limits)
	idxName := fmt.Sprintf("idx_%s_%s", t.Name, idx.Name)
	if len(idxName) > 128 {
		idxName = idxName[:128]
	}

	sqlStmt := fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
		unique, quoteMSSQLIdent(idxName), qualifyMSSQLTable(targetSchema, t.Name), strings.Join(cols, ", "))

	// Add included columns if any
	if len(idx.IncludeCols) > 0 {
		includeCols := make([]string, len(idx.IncludeCols))
		for i, col := range idx.IncludeCols {
			includeCols[i] = quoteMSSQLIdent(col)
		}
		sqlStmt += fmt.Sprintf(" INCLUDE (%s)", strings.Join(includeCols, ", "))
	}

	_, err := p.db.ExecContext(ctx, sqlStmt)
	return err
}

// CreateForeignKey creates a foreign key constraint on the target table
func (p *MSSQLPool) CreateForeignKey(ctx context.Context, t *source.Table, fk *source.ForeignKey, targetSchema string) error {
	// Build column lists
	cols := make([]string, len(fk.Columns))
	for i, col := range fk.Columns {
		cols[i] = quoteMSSQLIdent(col)
	}

	refCols := make([]string, len(fk.RefColumns))
	for i, col := range fk.RefColumns {
		refCols[i] = quoteMSSQLIdent(col)
	}

	// Map referential actions
	onDelete := mapReferentialActionMSSQL(fk.OnDelete)
	onUpdate := mapReferentialActionMSSQL(fk.OnUpdate)

	// Generate FK name
	fkName := fmt.Sprintf("fk_%s_%s", t.Name, fk.Name)
	if len(fkName) > 128 {
		fkName = fkName[:128]
	}

	sqlStmt := fmt.Sprintf(`
		ALTER TABLE %s
		ADD CONSTRAINT %s
		FOREIGN KEY (%s)
		REFERENCES %s (%s)
		ON DELETE %s
		ON UPDATE %s
	`, qualifyMSSQLTable(targetSchema, t.Name), quoteMSSQLIdent(fkName),
		strings.Join(cols, ", "),
		qualifyMSSQLTable(targetSchema, fk.RefTable), strings.Join(refCols, ", "),
		onDelete, onUpdate)

	_, err := p.db.ExecContext(ctx, sqlStmt)
	return err
}

// CreateCheckConstraint creates a check constraint on the target table
func (p *MSSQLPool) CreateCheckConstraint(ctx context.Context, t *source.Table, chk *source.CheckConstraint, targetSchema string) error {
	// Convert PostgreSQL CHECK syntax to SQL Server
	definition := convertCheckDefinitionMSSQL(chk.Definition)

	// Generate constraint name
	chkName := fmt.Sprintf("chk_%s_%s", t.Name, chk.Name)
	if len(chkName) > 128 {
		chkName = chkName[:128]
	}

	sqlStmt := fmt.Sprintf(`
		ALTER TABLE %s
		ADD CONSTRAINT %s
		CHECK %s
	`, qualifyMSSQLTable(targetSchema, t.Name), quoteMSSQLIdent(chkName), definition)

	_, err := p.db.ExecContext(ctx, sqlStmt)
	return err
}

// WriteChunk writes a chunk of data to the target table using TDS bulk copy protocol
func (p *MSSQLPool) WriteChunk(ctx context.Context, schema, table string, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}

	// Start a transaction for bulk copy
	txn, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	// Create fully qualified table name
	fullTableName := fmt.Sprintf("[%s].[%s]", schema, table)

	// Prepare bulk copy statement with performance hints
	// - Tablock: acquire table lock to reduce lock overhead
	// - RowsPerBatch: hint to optimizer for memory allocation
	rowsPerBatch := p.rowsPerBatch
	if rowsPerBatch <= 0 || rowsPerBatch > len(rows) {
		rowsPerBatch = len(rows) // Use actual row count if not set or too large
	}
	bulkOpts := mssql.BulkOptions{
		Tablock:      true,
		RowsPerBatch: rowsPerBatch,
	}
	stmt, err := txn.PrepareContext(ctx, mssql.CopyIn(fullTableName, bulkOpts, cols...))
	if err != nil {
		txn.Rollback()
		return fmt.Errorf("preparing bulk copy: %w", err)
	}

	// Execute for each row
	for _, row := range rows {
		_, err = stmt.ExecContext(ctx, row...)
		if err != nil {
			stmt.Close()
			txn.Rollback()
			return fmt.Errorf("bulk copy row: %w", err)
		}
	}

	// Final exec with no args to flush all rows
	_, err = stmt.ExecContext(ctx)
	if err != nil {
		stmt.Close()
		txn.Rollback()
		return fmt.Errorf("flushing bulk copy: %w", err)
	}

	if err = stmt.Close(); err != nil {
		txn.Rollback()
		return fmt.Errorf("closing bulk copy: %w", err)
	}

	if err = txn.Commit(); err != nil {
		return fmt.Errorf("committing bulk copy: %w", err)
	}

	return nil
}

// UpsertChunk for MSSQL uses whole-table staging approach:
// - Bulk loads data into staging table (fast, using existing WriteChunk logic)
// - MERGE runs once at the end via ExecuteUpsertMerge
func (p *MSSQLPool) UpsertChunk(ctx context.Context, schema, table string, cols []string, pkCols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}

	// Write to staging table instead of target (bulk copy is fast)
	stagingTable := fmt.Sprintf("staging_%s", table)
	return p.WriteChunk(ctx, schema, stagingTable, cols, rows)
}

// PrepareUpsertStaging creates the staging table before transfer
func (p *MSSQLPool) PrepareUpsertStaging(ctx context.Context, schema, table string) error {
	stagingTable := fmt.Sprintf("%s.staging_%s", schema, table)
	targetTable := qualifyMSSQLTable(schema, table)

	// Drop if exists and create fresh staging table with same structure
	sql := fmt.Sprintf(`
		IF OBJECT_ID('%s', 'U') IS NOT NULL DROP TABLE %s;
		SELECT * INTO %s FROM %s WHERE 1=0`,
		stagingTable, stagingTable, stagingTable, targetTable)

	_, err := p.db.ExecContext(ctx, sql)
	return err
}

// ExecuteUpsertMerge runs chunked MERGE operations after all data is staged
// This scales better than a single MERGE on large tables
func (p *MSSQLPool) ExecuteUpsertMerge(ctx context.Context, schema, table string, cols []string, pkCols []string) error {
	stagingTable := fmt.Sprintf("%s.staging_%s", schema, table)

	// Get row count and min/max PK from staging table
	var rowCount int64
	var minPK, maxPK int64
	pkCol := pkCols[0] // Use first PK column for chunking

	countSQL := fmt.Sprintf("SELECT COUNT(*), ISNULL(MIN(%s), 0), ISNULL(MAX(%s), 0) FROM %s",
		quoteMSSQLIdent(pkCol), quoteMSSQLIdent(pkCol), stagingTable)
	if err := p.db.QueryRowContext(ctx, countSQL).Scan(&rowCount, &minPK, &maxPK); err != nil {
		return fmt.Errorf("getting staging table stats: %w", err)
	}

	if rowCount == 0 {
		// Nothing to merge, just cleanup
		p.db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", stagingTable))
		return nil
	}

	// Create index on staging table PK for efficient chunked MERGE
	// This dramatically speeds up the BETWEEN queries
	indexName := fmt.Sprintf("IX_staging_%s_pk", table)
	var pkColList []string
	for _, pk := range pkCols {
		pkColList = append(pkColList, quoteMSSQLIdent(pk))
	}
	indexSQL := fmt.Sprintf("CREATE CLUSTERED INDEX %s ON %s (%s)",
		quoteMSSQLIdent(indexName), stagingTable, strings.Join(pkColList, ", "))
	if _, err := p.db.ExecContext(ctx, indexSQL); err != nil {
		// Non-fatal - continue without index (just slower)
		// Log would be nice here but we don't have logger access
	}

	// For small tables or non-integer PKs, use single MERGE
	if rowCount <= 100000 || maxPK == 0 {
		mergeSQL := buildMSSQLMergeSQL(schema, table, stagingTable, cols, pkCols, false)
		if _, err := p.db.ExecContext(ctx, mergeSQL); err != nil {
			return fmt.Errorf("executing merge: %w", err)
		}
	} else {
		// Chunked MERGE for large tables
		chunkSize := int64(50000) // 50k rows per MERGE

		for start := minPK; start <= maxPK; start += chunkSize {
			end := start + chunkSize - 1
			if end > maxPK {
				end = maxPK
			}

			mergeSQL := buildMSSQLChunkedMergeSQL(schema, table, stagingTable, cols, pkCols, pkCol, start, end)
			if _, err := p.db.ExecContext(ctx, mergeSQL); err != nil {
				return fmt.Errorf("executing chunked merge (pk %d-%d): %w", start, end, err)
			}
		}
	}

	// Drop staging table
	dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s", stagingTable)
	p.db.ExecContext(ctx, dropSQL)

	return nil
}

// buildMSSQLChunkedMergeSQL generates a MERGE statement for a PK range
func buildMSSQLChunkedMergeSQL(schema, table, stagingTable string, cols []string, pkCols []string, pkCol string, startPK, endPK int64) string {
	var sb strings.Builder

	// Build ON clause for PK matching
	onClauses := make([]string, len(pkCols))
	for i, pk := range pkCols {
		onClauses[i] = fmt.Sprintf("target.%s = src.%s", quoteMSSQLIdent(pk), quoteMSSQLIdent(pk))
	}

	// Build SET clause (exclude PK columns)
	pkSet := make(map[string]bool)
	for _, pk := range pkCols {
		pkSet[pk] = true
	}
	var setClauses []string
	for _, col := range cols {
		if !pkSet[col] {
			setClauses = append(setClauses, fmt.Sprintf("target.%s = src.%s", quoteMSSQLIdent(col), quoteMSSQLIdent(col)))
		}
	}

	// Build INSERT columns and values
	quotedCols := make([]string, len(cols))
	srcCols := make([]string, len(cols))
	for i, col := range cols {
		quotedCols[i] = quoteMSSQLIdent(col)
		srcCols[i] = fmt.Sprintf("src.%s", quoteMSSQLIdent(col))
	}

	// MERGE with subquery that filters by PK range
	sb.WriteString(fmt.Sprintf("MERGE INTO %s AS target\n", qualifyMSSQLTable(schema, table)))
	sb.WriteString(fmt.Sprintf("USING (SELECT * FROM %s WHERE %s BETWEEN %d AND %d) AS src\n",
		stagingTable, quoteMSSQLIdent(pkCol), startPK, endPK))
	sb.WriteString(fmt.Sprintf("ON (%s)\n", strings.Join(onClauses, " AND ")))

	if len(setClauses) > 0 {
		sb.WriteString("WHEN MATCHED THEN\n")
		sb.WriteString(fmt.Sprintf("  UPDATE SET %s\n", strings.Join(setClauses, ", ")))
	}

	sb.WriteString("WHEN NOT MATCHED BY TARGET THEN\n")
	sb.WriteString(fmt.Sprintf("  INSERT (%s) VALUES (%s);",
		strings.Join(quotedCols, ", "),
		strings.Join(srcCols, ", ")))

	return sb.String()
}

// buildMSSQLMergeSQL generates SQL Server MERGE statement
// MERGE INTO target AS t
// USING staging AS s ON t.pk = s.pk
// WHEN MATCHED AND EXISTS(SELECT s.* EXCEPT SELECT t.*) THEN UPDATE SET ...
// WHEN NOT MATCHED THEN INSERT (cols) VALUES (s.cols);
//
// useExcept: If true, use EXCEPT for change detection (requires compat level >= 130).
// If false, always update matched rows (less efficient but compatible with older SQL Server).
func buildMSSQLMergeSQL(schema, table, stagingTable string, cols []string, pkCols []string, useExcept bool) string {
	var sb strings.Builder

	// Build ON clause for PK matching
	onClauses := make([]string, len(pkCols))
	for i, pk := range pkCols {
		onClauses[i] = fmt.Sprintf("target.%s = src.%s", quoteMSSQLIdent(pk), quoteMSSQLIdent(pk))
	}

	// Build SET clause (exclude PK columns)
	pkSet := make(map[string]bool)
	for _, pk := range pkCols {
		pkSet[pk] = true
	}
	var setClauses []string
	for _, col := range cols {
		if !pkSet[col] {
			setClauses = append(setClauses, fmt.Sprintf("target.%s = src.%s", quoteMSSQLIdent(col), quoteMSSQLIdent(col)))
		}
	}

	// Build INSERT columns and values
	quotedCols := make([]string, len(cols))
	srcCols := make([]string, len(cols))
	for i, col := range cols {
		quotedCols[i] = quoteMSSQLIdent(col)
		srcCols[i] = fmt.Sprintf("src.%s", quoteMSSQLIdent(col))
	}

	sb.WriteString(fmt.Sprintf("MERGE INTO %s AS target\n", qualifyMSSQLTable(schema, table)))
	sb.WriteString(fmt.Sprintf("USING %s AS src\n", stagingTable))
	sb.WriteString(fmt.Sprintf("ON (%s)\n", strings.Join(onClauses, " AND ")))

	if len(setClauses) > 0 {
		if useExcept {
			// WHEN MATCHED AND (change detection using EXCEPT) - SQL Server 2016+
			srcColsForCompare := make([]string, 0, len(cols))
			targetColsForCompare := make([]string, 0, len(cols))
			for _, col := range cols {
				if !pkSet[col] {
					srcColsForCompare = append(srcColsForCompare, fmt.Sprintf("src.%s", quoteMSSQLIdent(col)))
					targetColsForCompare = append(targetColsForCompare, fmt.Sprintf("target.%s", quoteMSSQLIdent(col)))
				}
			}
			sb.WriteString("WHEN MATCHED AND EXISTS(\n")
			sb.WriteString(fmt.Sprintf("  SELECT %s\n  EXCEPT\n  SELECT %s\n) THEN\n",
				strings.Join(srcColsForCompare, ", "),
				strings.Join(targetColsForCompare, ", ")))
			sb.WriteString(fmt.Sprintf("  UPDATE SET %s\n", strings.Join(setClauses, ", ")))
		} else {
			// WHEN MATCHED - always update (less efficient, but compatible with older SQL Server)
			sb.WriteString("WHEN MATCHED THEN\n")
			sb.WriteString(fmt.Sprintf("  UPDATE SET %s\n", strings.Join(setClauses, ", ")))
		}
	}

	sb.WriteString("WHEN NOT MATCHED BY TARGET THEN\n")
	sb.WriteString(fmt.Sprintf("  INSERT (%s) VALUES (%s);",
		strings.Join(quotedCols, ", "),
		strings.Join(srcCols, ", ")))

	return sb.String()
}

// GenerateMSSQLDDL generates SQL Server DDL from source table metadata
func GenerateMSSQLDDL(t *source.Table, targetSchema string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", qualifyMSSQLTable(targetSchema, t.Name)))

	for i, col := range t.Columns {
		if i > 0 {
			sb.WriteString(",\n")
		}

		// Map data type
		mssqlType := typemap.PostgresToMSSQL(col.DataType, col.MaxLength, col.Precision, col.Scale)

		sb.WriteString(fmt.Sprintf("    %s %s", quoteMSSQLIdent(col.Name), mssqlType))

		// Add IDENTITY for serial columns
		if col.IsIdentity {
			sb.WriteString(" IDENTITY(1,1)")
		}

		// Nullability
		if !col.IsNullable {
			sb.WriteString(" NOT NULL")
		} else {
			sb.WriteString(" NULL")
		}
	}

	sb.WriteString("\n)")

	return sb.String()
}

// mapReferentialActionMSSQL converts referential action to SQL Server syntax
func mapReferentialActionMSSQL(action string) string {
	switch strings.ToUpper(action) {
	case "CASCADE":
		return "CASCADE"
	case "SET_NULL", "SET NULL":
		return "SET NULL"
	case "SET_DEFAULT", "SET DEFAULT":
		return "SET DEFAULT"
	case "RESTRICT", "NO_ACTION", "NO ACTION":
		return "NO ACTION"
	default:
		return "NO ACTION"
	}
}

// convertCheckDefinitionMSSQL converts PostgreSQL CHECK definition to SQL Server
func convertCheckDefinitionMSSQL(def string) string {
	result := def

	// Replace "column" with [column]
	// This is a simplified conversion
	for {
		start := strings.Index(result, `"`)
		if start == -1 {
			break
		}
		end := strings.Index(result[start+1:], `"`)
		if end == -1 {
			break
		}
		colName := result[start+1 : start+1+end]
		result = result[:start] + "[" + colName + "]" + result[start+end+2:]
	}

	// Replace CURRENT_TIMESTAMP with GETDATE()
	result = strings.ReplaceAll(result, "CURRENT_TIMESTAMP", "GETDATE()")
	result = strings.ReplaceAll(result, "current_timestamp", "GETDATE()")

	// Replace true/false with 1/0
	result = strings.ReplaceAll(result, " true", " 1")
	result = strings.ReplaceAll(result, " false", " 0")
	result = strings.ReplaceAll(result, "(true)", "(1)")
	result = strings.ReplaceAll(result, "(false)", "(0)")

	return result
}
