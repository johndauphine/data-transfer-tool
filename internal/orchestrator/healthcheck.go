package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/johndauphine/dmt/internal/checkpoint"
	"github.com/johndauphine/dmt/internal/driver"
	"github.com/johndauphine/dmt/internal/driver/dbtuning"
	"github.com/johndauphine/dmt/internal/logging"
)

// HealthCheck tests connectivity to source and target databases.
// Runs source and target checks in parallel with independent timeouts to prevent
// one slow connection from causing the other to fail with "context deadline exceeded".
func (o *Orchestrator) HealthCheck(ctx context.Context) (*HealthCheckResult, error) {
	result := &HealthCheckResult{
		Timestamp:    time.Now().Format(time.RFC3339),
		SourceDBType: o.sourcePool.DBType(),
		TargetDBType: o.targetPool.DBType(),
	}

	// Use a per-check timeout of 30 seconds.
	const checkTimeout = 30 * time.Second

	// Run source and target checks in parallel to:
	// 1. Give each check its own 30-second budget
	// 2. Preserve context cancellation (SIGINT, etc.)
	// 3. Complete in max(source, target) time instead of source + target
	var wg sync.WaitGroup
	wg.Add(2)

	// Source check goroutine
	go func() {
		defer wg.Done()
		sourceStart := time.Now()
		sourceCtx, sourceCancel := context.WithTimeout(ctx, checkTimeout)
		defer sourceCancel()

		if db := o.sourcePool.DB(); db != nil {
			if err := db.PingContext(sourceCtx); err != nil {
				result.SourceError = err.Error()
			} else {
				result.SourceConnected = true
				// Get table count
				tables, err := o.sourcePool.ExtractSchema(sourceCtx, o.config.Source.Schema)
				if err == nil {
					result.SourceTableCount = len(tables)
				}
			}
		}
		result.SourceLatencyMs = time.Since(sourceStart).Milliseconds()
	}()

	// Target check goroutine
	go func() {
		defer wg.Done()
		targetStart := time.Now()
		targetCtx, targetCancel := context.WithTimeout(ctx, checkTimeout)
		defer targetCancel()

		if err := o.targetPool.Ping(targetCtx); err != nil {
			result.TargetError = err.Error()
		} else {
			result.TargetConnected = true
		}
		result.TargetLatencyMs = time.Since(targetStart).Milliseconds()
	}()

	wg.Wait()

	result.Healthy = result.SourceConnected && result.TargetConnected
	return result, nil
}

// DryRun performs a migration preview without transferring data.
func (o *Orchestrator) DryRun(ctx context.Context) (*DryRunResult, error) {
	logging.Info("Performing dry run (no data will be transferred)...")

	// Extract schema
	tables, err := o.sourcePool.ExtractSchema(ctx, o.config.Source.Schema)
	if err != nil {
		return nil, fmt.Errorf("extracting schema: %w", err)
	}

	// Apply table filters
	tables = o.filterTables(tables)

	result := &DryRunResult{
		SourceType:   o.config.Source.Type,
		TargetType:   o.config.Target.Type,
		SourceSchema: o.config.Source.Schema,
		TargetSchema: o.config.Target.Schema,
		Workers:      o.config.Migration.Workers,
		ChunkSize:    o.config.Migration.ChunkSize,
		TargetMode:   o.config.Migration.TargetMode,
		TotalTables:  len(tables),
	}

	// Calculate estimated memory
	bufferMem := int64(o.config.Migration.Workers) *
		int64(o.config.Migration.ReadAheadBuffers) *
		int64(o.config.Migration.ChunkSize) *
		500 // bytes per row estimate
	result.EstimatedMemMB = bufferMem / (1024 * 1024)

	// Analyze each table
	for _, t := range tables {
		rowCount, err := o.sourcePool.GetRowCount(ctx, o.config.Source.Schema, t.Name)
		if err != nil {
			logging.Warn("Failed to get row count for %s.%s: %v (assuming 0)", o.config.Source.Schema, t.Name, err)
			rowCount = 0
		}
		result.TotalRows += rowCount

		// Determine pagination method
		paginationMethod := "full_table"
		partitions := 1
		hasPK := len(t.PKColumns) > 0

		if hasPK {
			if len(t.PKColumns) == 1 && isIntegerType(t.PKColumns[0].DataType) {
				paginationMethod = "keyset"
			} else {
				paginationMethod = "row_number"
			}

			// Estimate partitions for large tables
			if rowCount > int64(o.config.Migration.LargeTableThreshold) {
				partitions = o.config.Migration.MaxPartitions
			}
		}

		result.Tables = append(result.Tables, DryRunTable{
			Name:             t.Name,
			RowCount:         rowCount,
			PaginationMethod: paginationMethod,
			Partitions:       partitions,
			HasPK:            hasPK,
			Columns:          len(t.Columns),
		})
	}

	return result, nil
}

// isIntegerType checks if a data type is an integer type.
func isIntegerType(dataType string) bool {
	dataType = strings.ToLower(dataType)
	intTypes := []string{"int", "bigint", "smallint", "tinyint", "integer", "int4", "int8", "int2"}
	for _, t := range intTypes {
		if strings.Contains(dataType, t) {
			return true
		}
	}
	return false
}

// AnalyzeConfig uses AI to analyze the source database and suggest optimal configuration.
func (o *Orchestrator) AnalyzeConfig(ctx context.Context, schema string) (*driver.SmartConfigSuggestions, error) {
	logging.Debug("Analyzing source database for configuration suggestions...")

	// Get AI mapper from secrets
	aiMapper, err := driver.NewAITypeMapperFromSecrets()
	if err != nil {
		return nil, fmt.Errorf("loading AI type mapper: %w", err)
	}

	// Create the smart config analyzer
	analyzer := driver.NewSmartConfigAnalyzer(o.sourcePool.DB(), o.sourcePool.DBType(), aiMapper)

	// Set up history provider using the state backend for learning from past analyses
	if o.state != nil {
		analyzer.SetHistoryProvider(&stateHistoryAdapter{state: o.state})
	}

	// Set target database type for more accurate recommendations
	if o.targetPool != nil {
		analyzer.SetTargetDBType(o.targetPool.DBType())
	}

	// Run analysis
	suggestions, err := analyzer.Analyze(ctx, schema)
	if err != nil {
		return nil, fmt.Errorf("analyzing config: %w", err)
	}

	// Add database tuning recommendations using the same AI mapper
	o.addDatabaseTuningRecommendations(ctx, suggestions, aiMapper)

	return suggestions, nil
}

// stateHistoryAdapter adapts checkpoint.StateBackend to driver.TuningHistoryProvider.
type stateHistoryAdapter struct {
	state checkpoint.StateBackend
}

// GetAIAdjustments returns recent runtime AI adjustments from migrations.
func (a *stateHistoryAdapter) GetAIAdjustments(limit int) ([]driver.AIAdjustmentRecord, error) {
	records, err := a.state.GetAIAdjustments(limit)
	if err != nil {
		return nil, err
	}

	// Convert checkpoint records to driver records
	result := make([]driver.AIAdjustmentRecord, len(records))
	for i, r := range records {
		result[i] = driver.AIAdjustmentRecord{
			Action:           r.Action,
			Adjustments:      r.Adjustments,
			ThroughputBefore: r.ThroughputBefore,
			ThroughputAfter:  r.ThroughputAfter,
			EffectPercent:    r.EffectPercent,
			Reasoning:        r.Reasoning,
		}
	}
	return result, nil
}

// GetAITuningHistory returns recent tuning recommendations from analyze.
func (a *stateHistoryAdapter) GetAITuningHistory(limit int) ([]driver.AITuningRecord, error) {
	records, err := a.state.GetAITuningHistory(limit)
	if err != nil {
		return nil, err
	}

	// Convert checkpoint records to driver records
	result := make([]driver.AITuningRecord, len(records))
	for i, r := range records {
		result[i] = driver.AITuningRecord{
			Timestamp:         r.Timestamp,
			SourceDBType:      r.SourceDBType,
			TargetDBType:      r.TargetDBType,
			TotalTables:       r.TotalTables,
			TotalRows:         r.TotalRows,
			AvgRowSizeBytes:   r.AvgRowSizeBytes,
			CPUCores:          r.CPUCores,
			MemoryGB:          r.MemoryGB,
			Workers:           r.Workers,
			ChunkSize:         r.ChunkSize,
			ReadAheadBuffers:  r.ReadAheadBuffers,
			WriteAheadWriters: r.WriteAheadWriters,
			ParallelReaders:   r.ParallelReaders,
			MaxPartitions:     r.MaxPartitions,
			EstimatedMemoryMB: r.EstimatedMemoryMB,
			AIReasoning:       r.AIReasoning,
			WasAIUsed:         r.WasAIUsed,
		}
	}
	return result, nil
}

// SaveAITuning saves a tuning recommendation for future reference.
func (a *stateHistoryAdapter) SaveAITuning(record driver.AITuningRecord) error {
	return a.state.SaveAITuning(checkpoint.AITuningRecord{
		Timestamp:         record.Timestamp,
		SourceDBType:      record.SourceDBType,
		TargetDBType:      record.TargetDBType,
		TotalTables:       record.TotalTables,
		TotalRows:         record.TotalRows,
		AvgRowSizeBytes:   record.AvgRowSizeBytes,
		CPUCores:          record.CPUCores,
		MemoryGB:          record.MemoryGB,
		Workers:           record.Workers,
		ChunkSize:         record.ChunkSize,
		ReadAheadBuffers:  record.ReadAheadBuffers,
		WriteAheadWriters: record.WriteAheadWriters,
		ParallelReaders:   record.ParallelReaders,
		MaxPartitions:     record.MaxPartitions,
		EstimatedMemoryMB: record.EstimatedMemoryMB,
		AIReasoning:       record.AIReasoning,
		WasAIUsed:         record.WasAIUsed,
	})
}

// addDatabaseTuningRecommendations adds source and target database tuning recommendations.
func (o *Orchestrator) addDatabaseTuningRecommendations(ctx context.Context, suggestions *driver.SmartConfigSuggestions, aiMapper *driver.AITypeMapper) {
	// AI mapper is passed from AnalyzeConfig to avoid refetching
	if aiMapper == nil {
		return
	}

	// Schema statistics for recommendations
	stats := dbtuning.SchemaStatistics{
		TotalTables:     suggestions.TotalTables,
		TotalRows:       suggestions.TotalRows,
		AvgRowSizeBytes: suggestions.AvgRowSizeBytes,
		EstimatedMemMB:  suggestions.EstimatedMemMB,
	}

	// Run source and target analysis concurrently for better performance
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Analyze source database tuning using AI-driven approach (concurrent)
	if o.sourcePool != nil && o.sourcePool.DB() != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Independent timeout for source analysis
			sourceCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			logging.Debug("Analyzing source database configuration...")
			sourceTuning, err := dbtuning.Analyze(
				sourceCtx,
				o.sourcePool.DB(),
				o.sourcePool.DBType(),
				"source",
				stats,
				aiMapper,
			)

			mu.Lock()
			if err != nil {
				logging.Warn("Failed to analyze source database tuning: %v", err)
				// Set fallback tuning so output is consistent
				suggestions.SourceTuning = &dbtuning.DatabaseTuning{
					DatabaseType:    o.sourcePool.DBType(),
					Role:            "source",
					TuningPotential: "unknown",
					EstimatedImpact: fmt.Sprintf("Analysis failed: %v", err),
				}
			} else {
				suggestions.SourceTuning = sourceTuning
				if sourceTuning.TuningPotential != "unknown" {
					logging.Info("Source tuning: %s potential (%s)", sourceTuning.TuningPotential, sourceTuning.EstimatedImpact)
				}
			}
			mu.Unlock()
		}()
	} else {
		// No source pool available
		suggestions.SourceTuning = &dbtuning.DatabaseTuning{
			DatabaseType:    "unknown",
			Role:            "source",
			TuningPotential: "unknown",
			EstimatedImpact: "Source database not available for analysis",
		}
	}

	// Analyze target database tuning using AI-driven approach (concurrent)
	if o.targetPool != nil && o.targetPool.DB() != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Independent timeout for target analysis
			targetCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()

			logging.Debug("Analyzing target database configuration...")
			targetTuning, err := dbtuning.Analyze(
				targetCtx,
				o.targetPool.DB(),
				o.targetPool.DBType(),
				"target",
				stats,
				aiMapper,
			)

			mu.Lock()
			if err != nil {
				logging.Warn("Failed to analyze target database tuning: %v", err)
				// Set fallback tuning so output is consistent
				suggestions.TargetTuning = &dbtuning.DatabaseTuning{
					DatabaseType:    o.targetPool.DBType(),
					Role:            "target",
					TuningPotential: "unknown",
					EstimatedImpact: fmt.Sprintf("Analysis failed: %v", err),
				}
			} else {
				suggestions.TargetTuning = targetTuning
				if targetTuning.TuningPotential != "unknown" {
					logging.Info("Target tuning: %s potential (%s)", targetTuning.TuningPotential, targetTuning.EstimatedImpact)
				}
			}
			mu.Unlock()
		}()
	} else {
		// No target pool available
		suggestions.TargetTuning = &dbtuning.DatabaseTuning{
			DatabaseType:    "unknown",
			Role:            "target",
			TuningPotential: "unknown",
			EstimatedImpact: "Target database not available for analysis",
		}
	}

	// Wait for all analyses to complete before returning
	wg.Wait()
}
