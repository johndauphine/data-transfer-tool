package source

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEstimateRowSizeFromStats(t *testing.T) {
	tests := []struct {
		name      string
		mockRows  *sqlmock.Rows
		mockErr   error
		wantSize  int64
		wantQuery string
	}{
		{
			name:     "valid row size",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(1000)),
			wantSize: 1500, // 1000 * 3 / 2 (with Go runtime overhead)
		},
		{
			name:     "zero row size returns default",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(0)),
			wantSize: 500,
		},
		{
			name:     "null row size returns default",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(nil),
			wantSize: 500,
		},
		{
			name:     "query error returns default",
			mockErr:  sql.ErrNoRows,
			wantSize: 500,
		},
		{
			name:     "small row size",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(100)),
			wantSize: 150, // 100 * 3 / 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			expectedQuery := "SELECT.*avg_row_size.*FROM sys.dm_db_partition_stats"
			if tt.mockErr != nil {
				mock.ExpectQuery(expectedQuery).WillReturnError(tt.mockErr)
			} else {
				mock.ExpectQuery(expectedQuery).WillReturnRows(tt.mockRows)
			}

			got := EstimateRowSizeFromStats(context.Background(), db, "dbo", "test_table")

			if got != tt.wantSize {
				t.Errorf("EstimateRowSizeFromStats() = %v, want %v", got, tt.wantSize)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}

func TestEstimateRowSizeFromStatsPostgres(t *testing.T) {
	tests := []struct {
		name     string
		mockRows *sqlmock.Rows
		mockErr  error
		wantSize int64
	}{
		{
			name:     "valid row size",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(1000)),
			wantSize: 1500, // 1000 * 3 / 2 (with Go runtime overhead)
		},
		{
			name:     "zero row size returns default",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(0)),
			wantSize: 500,
		},
		{
			name:     "null row size returns default",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(nil),
			wantSize: 500,
		},
		{
			name:     "query error returns default",
			mockErr:  sql.ErrNoRows,
			wantSize: 500,
		},
		{
			name:     "small row size",
			mockRows: sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(200)),
			wantSize: 300, // 200 * 3 / 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			// Match query using relpages (the fixed version)
			expectedQuery := "SELECT.*avg_row_size.*FROM pg_class"
			if tt.mockErr != nil {
				mock.ExpectQuery(expectedQuery).WillReturnError(tt.mockErr)
			} else {
				mock.ExpectQuery(expectedQuery).WillReturnRows(tt.mockRows)
			}

			got := EstimateRowSizeFromStatsPostgres(context.Background(), db, "public", "test_table")

			if got != tt.wantSize {
				t.Errorf("EstimateRowSizeFromStatsPostgres() = %v, want %v", got, tt.wantSize)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unfulfilled expectations: %v", err)
			}
		})
	}
}

func TestEstimateRowSizeFromStatsPostgres_Timeout(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	// Simulate a slow query that exceeds the 5-second timeout
	expectedQuery := "SELECT.*avg_row_size.*FROM pg_class"
	mock.ExpectQuery(expectedQuery).WillDelayFor(6 * time.Second).WillReturnRows(
		sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(1000)),
	)

	// Use a context that will be cancelled
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got := EstimateRowSizeFromStatsPostgres(ctx, db, "public", "test_table")

	// Should return default due to timeout
	if got != 500 {
		t.Errorf("EstimateRowSizeFromStatsPostgres() with timeout = %v, want 500", got)
	}
}

func TestEstimateRowSizeFromStatsPostgres_UsesRelpages(t *testing.T) {
	// This test verifies the query uses relpages instead of pg_total_relation_size
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	// The query should contain relpages, NOT pg_total_relation_size
	mock.ExpectQuery("relpages").WillReturnRows(
		sqlmock.NewRows([]string{"avg_row_size"}).AddRow(int64(500)),
	)

	EstimateRowSizeFromStatsPostgres(context.Background(), db, "public", "test_table")

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("query should use relpages: %v", err)
	}
}
