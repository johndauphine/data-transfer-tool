package target

import (
	"strings"
	"testing"

	"github.com/johndauphine/mssql-pg-migrate/internal/source"
)

func TestGenerateDDLWithOptions_SourceTypes(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		column     source.Column
		wantType   string
	}{
		// MSSQL -> PostgreSQL (cross-engine)
		{
			name:       "mssql int to pg",
			sourceType: "mssql",
			column:     source.Column{Name: "id", DataType: "int", IsNullable: false},
			wantType:   "integer",
		},
		{
			name:       "mssql nvarchar to pg",
			sourceType: "mssql",
			column:     source.Column{Name: "name", DataType: "nvarchar", MaxLength: 100, IsNullable: true},
			wantType:   "varchar(100)",
		},
		{
			name:       "mssql datetime to pg",
			sourceType: "mssql",
			column:     source.Column{Name: "created", DataType: "datetime", IsNullable: true},
			wantType:   "timestamp",
		},

		// PostgreSQL -> PostgreSQL (same-engine normalization)
		{
			name:       "pg int4 normalized",
			sourceType: "postgres",
			column:     source.Column{Name: "id", DataType: "int4", IsNullable: false},
			wantType:   "integer",
		},
		{
			name:       "pg int8 normalized",
			sourceType: "postgres",
			column:     source.Column{Name: "id", DataType: "int8", IsNullable: false},
			wantType:   "bigint",
		},
		{
			name:       "pg bool normalized",
			sourceType: "postgres",
			column:     source.Column{Name: "flag", DataType: "bool", IsNullable: true},
			wantType:   "boolean",
		},
		{
			name:       "pg varchar passthrough",
			sourceType: "postgres",
			column:     source.Column{Name: "name", DataType: "varchar", MaxLength: 255, IsNullable: true},
			wantType:   "varchar(255)",
		},
		{
			name:       "pg text passthrough",
			sourceType: "postgres",
			column:     source.Column{Name: "desc", DataType: "text", IsNullable: true},
			wantType:   "text",
		},
		{
			name:       "pg numeric with precision",
			sourceType: "postgres",
			column:     source.Column{Name: "amount", DataType: "numeric", Precision: 18, Scale: 2, IsNullable: true},
			wantType:   "numeric(18,2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := &source.Table{
				Name:    "test_table",
				Columns: []source.Column{tt.column},
			}

			ddl := GenerateDDLWithOptions(table, "public", false, tt.sourceType)

			if !strings.Contains(ddl, tt.wantType) {
				t.Errorf("GenerateDDLWithOptions() with sourceType=%q\nDDL = %s\nwant type %q", tt.sourceType, ddl, tt.wantType)
			}
		})
	}
}

func TestGenerateMSSQLDDL_SourceTypes(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		column     source.Column
		wantType   string
	}{
		// PostgreSQL -> MSSQL (cross-engine)
		{
			name:       "pg integer to mssql",
			sourceType: "postgres",
			column:     source.Column{Name: "id", DataType: "integer", IsNullable: false},
			wantType:   "int",
		},
		{
			name:       "pg text to mssql",
			sourceType: "postgres",
			column:     source.Column{Name: "desc", DataType: "text", IsNullable: true},
			wantType:   "nvarchar(max)",
		},
		{
			name:       "pg timestamptz to mssql",
			sourceType: "postgres",
			column:     source.Column{Name: "created", DataType: "timestamptz", IsNullable: true},
			wantType:   "datetimeoffset",
		},

		// MSSQL -> MSSQL (same-engine normalization)
		{
			name:       "mssql varchar with len",
			sourceType: "mssql",
			column:     source.Column{Name: "name", DataType: "varchar", MaxLength: 100, IsNullable: true},
			wantType:   "varchar(100)",
		},
		{
			name:       "mssql varchar max",
			sourceType: "mssql",
			column:     source.Column{Name: "name", DataType: "varchar", MaxLength: -1, IsNullable: true},
			wantType:   "varchar(max)",
		},
		{
			name:       "mssql nvarchar with len",
			sourceType: "mssql",
			column:     source.Column{Name: "name", DataType: "nvarchar", MaxLength: 50, IsNullable: true},
			wantType:   "nvarchar(50)",
		},
		{
			name:       "mssql decimal with precision",
			sourceType: "mssql",
			column:     source.Column{Name: "amount", DataType: "decimal", Precision: 10, Scale: 2, IsNullable: true},
			wantType:   "decimal(10,2)",
		},
		{
			name:       "mssql int passthrough",
			sourceType: "mssql",
			column:     source.Column{Name: "id", DataType: "int", IsNullable: false},
			wantType:   "int",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := &source.Table{
				Name:    "test_table",
				Columns: []source.Column{tt.column},
			}

			ddl := GenerateMSSQLDDL(table, "dbo", tt.sourceType)

			if !strings.Contains(ddl, tt.wantType) {
				t.Errorf("GenerateMSSQLDDL() with sourceType=%q\nDDL = %s\nwant type %q", tt.sourceType, ddl, tt.wantType)
			}
		})
	}
}
