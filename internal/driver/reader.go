package driver

import (
	"context"
	"database/sql"
	"time"

	"github.com/johndauphine/mssql-pg-migrate/internal/stats"
)

// Reader represents a database reader that can stream data from source tables.
// This is the "Producer" in the Reader -> Queue -> Writer pipeline.
type Reader interface {
	// Connection management
	Close() error
	DB() *sql.DB

	// Schema operations
	ExtractSchema(ctx context.Context, schema string) ([]Table, error)
	LoadIndexes(ctx context.Context, t *Table) error
	LoadForeignKeys(ctx context.Context, t *Table) error
	LoadCheckConstraints(ctx context.Context, t *Table) error

	// Data reading - returns channel for streaming batches
	ReadTable(ctx context.Context, opts ReadOptions) (<-chan Batch, error)

	// Metadata
	GetRowCount(ctx context.Context, schema, table string) (int64, error)
	GetPartitionBoundaries(ctx context.Context, t *Table, numPartitions int) ([]Partition, error)
	GetDateColumnInfo(ctx context.Context, schema, table string, candidates []string) (columnName, dataType string, found bool)

	// Data sampling for AI type mapping
	SampleColumnValues(ctx context.Context, schema, table, column string, limit int) ([]string, error)

	// Pool info
	MaxConns() int
	DBType() string
	PoolStats() stats.PoolStats
}

// ReadOptions configures how to read data from a table.
type ReadOptions struct {
	// Table is the source table to read from.
	Table Table

	// Columns is the list of columns to read.
	Columns []string

	// ColumnTypes contains the data types for each column.
	ColumnTypes []string

	// Partition specifies a partition to read (nil for whole table).
	Partition *Partition

	// ChunkSize is the number of rows per batch.
	ChunkSize int

	// DateFilter filters rows by a date column (for incremental sync).
	DateFilter *DateFilter

	// TargetDBType is the target database type (for spatial column conversion).
	TargetDBType string

	// StrictConsistency uses table hints for consistent reads (e.g., NOLOCK).
	StrictConsistency bool
}

// DateFilter specifies a filter on a date/timestamp column for incremental sync.
type DateFilter struct {
	// Column is the name of the date column to filter on.
	Column string

	// Timestamp is the minimum value (rows where column > timestamp are included).
	Timestamp time.Time
}

// Batch represents a batch of rows read from the source.
type Batch struct {
	// Rows contains the data, where each row is a slice of column values.
	Rows [][]any

	// Stats contains timing information for this batch.
	Stats BatchStats

	// LastKey is the last primary key value (for keyset pagination).
	LastKey any

	// RowNum is the current row number (for row number pagination).
	RowNum int64

	// Done indicates this is the final batch.
	Done bool

	// Error contains any error that occurred reading this batch.
	Error error
}

// BatchStats contains timing information for a batch read operation.
type BatchStats struct {
	// QueryTime is the time spent executing the query.
	QueryTime time.Duration

	// ScanTime is the time spent scanning rows.
	ScanTime time.Duration

	// ReadEnd is when the batch read completed.
	ReadEnd time.Time
}
