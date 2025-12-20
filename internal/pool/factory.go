package pool

import (
	"fmt"

	"github.com/johndauphine/mssql-pg-migrate/internal/config"
	"github.com/johndauphine/mssql-pg-migrate/internal/source"
	"github.com/johndauphine/mssql-pg-migrate/internal/target"
)

// NewSourcePool creates a source pool based on the configuration type
func NewSourcePool(cfg *config.SourceConfig, maxConns int) (SourcePool, error) {
	switch cfg.Type {
	case "mssql", "":
		return source.NewPool(cfg, maxConns)
	case "postgres":
		return source.NewPostgresPool(cfg, maxConns)
	default:
		return nil, fmt.Errorf("unsupported source type: %s", cfg.Type)
	}
}

// NewTargetPool creates a target pool based on the configuration type
// mssqlRowsPerBatch is only used for MSSQL targets (ignored for PostgreSQL)
func NewTargetPool(cfg *config.TargetConfig, maxConns int, mssqlRowsPerBatch int) (TargetPool, error) {
	switch cfg.Type {
	case "postgres", "":
		return target.NewPool(cfg, maxConns)
	case "mssql":
		return target.NewMSSQLPool(cfg, maxConns, mssqlRowsPerBatch)
	default:
		return nil, fmt.Errorf("unsupported target type: %s", cfg.Type)
	}
}
