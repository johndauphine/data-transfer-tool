package driver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/johndauphine/dmt/internal/driver/dbtuning"
	"github.com/johndauphine/dmt/internal/logging"
	"github.com/shirou/gopsutil/v3/mem"
)

// SmartConfigSuggestions contains AI-detected configuration suggestions.
type SmartConfigSuggestions struct {
	// DateColumns maps table names to suggested date_updated_columns
	DateColumns map[string][]string

	// ExcludeTables lists tables that should probably be excluded
	ExcludeTables []string

	// ChunkSizeRecommendation is the suggested chunk size based on table analysis
	ChunkSizeRecommendation int

	// Auto-tuned performance parameters
	Workers             int   // Recommended worker count (based on CPU cores)
	ReadAheadBuffers    int   // Recommended read-ahead buffers
	WriteAheadWriters   int   // Recommended write-ahead writers per job
	ParallelReaders     int   // Recommended parallel readers per job
	MaxPartitions       int   // Recommended max partitions for large tables
	LargeTableThreshold int64 // Row count threshold for partitioning

	// Connection pool tuning
	MaxSourceConnections int // Recommended max source database connections
	MaxTargetConnections int // Recommended max target database connections

	// Additional tuning parameters
	UpsertMergeChunkSize int // Recommended chunk size for upsert operations
	CheckpointFrequency  int // Recommended checkpoint frequency (chunks)
	MaxRetries           int // Recommended max retries for failed operations

	// Database statistics
	TotalTables     int   // Number of tables analyzed
	TotalRows       int64 // Total rows across all tables
	AvgRowSizeBytes int64 // Average row size in bytes
	EstimatedMemMB  int64 // Estimated memory usage with these settings

	// Warnings contains any issues detected during analysis
	Warnings []string

	// AISuggestions contains AI-recommended values (if AI was used)
	AISuggestions *AutoTuneOutput

	// Database tuning recommendations (NEW)
	SourceTuning *dbtuning.DatabaseTuning
	TargetTuning *dbtuning.DatabaseTuning
}

// AutoTuneInput contains system and database info for AI auto-tuning.
type AutoTuneInput struct {
	// System info
	CPUCores int `json:"cpu_cores"`
	MemoryGB int `json:"memory_gb"`

	// Database info
	DatabaseType string `json:"database_type"` // "mssql" or "postgres"
	TotalTables  int    `json:"total_tables"`
	TotalRows    int64  `json:"total_rows"`
	AvgRowBytes  int64  `json:"avg_row_bytes"`

	// Largest tables (top 5)
	LargestTables []TableStats `json:"largest_tables"`
}

// TableStats contains stats for a single table.
type TableStats struct {
	Name        string `json:"name"`
	RowCount    int64  `json:"row_count"`
	AvgRowBytes int64  `json:"avg_row_bytes"`
}

// AutoTuneOutput contains AI-recommended configuration values.
type AutoTuneOutput struct {
	Workers             int    `json:"workers"`
	ChunkSize           int    `json:"chunk_size"`
	ReadAheadBuffers    int    `json:"read_ahead_buffers"`
	WriteAheadWriters   int    `json:"write_ahead_writers"`
	ParallelReaders     int    `json:"parallel_readers"`
	MaxPartitions       int    `json:"max_partitions"`
	LargeTableThreshold int64  `json:"large_table_threshold"`
	EstimatedMemoryMB   int64  `json:"estimated_memory_mb"`

	// Connection pool tuning
	MaxSourceConnections int `json:"max_source_connections"`
	MaxTargetConnections int `json:"max_target_connections"`

	// Additional tuning
	UpsertMergeChunkSize int    `json:"upsert_merge_chunk_size"`
	CheckpointFrequency  int    `json:"checkpoint_frequency"`
	MaxRetries           int    `json:"max_retries"`
	Reasoning            string `json:"reasoning,omitempty"`
}

// TuningHistoryProvider provides access to historical tuning data.
// This allows the analyzer to learn from past analyses and migrations.
type TuningHistoryProvider interface {
	// GetAIAdjustments returns recent runtime AI adjustments from migrations
	GetAIAdjustments(limit int) ([]AIAdjustmentRecord, error)
	// GetAITuningHistory returns recent tuning recommendations from analyze
	GetAITuningHistory(limit int) ([]AITuningRecord, error)
	// SaveAITuning saves a tuning recommendation for future reference
	SaveAITuning(record AITuningRecord) error
}

// AIAdjustmentRecord represents a historical AI adjustment from runtime migration.
type AIAdjustmentRecord struct {
	Action           string         `json:"action"`
	Adjustments      map[string]int `json:"adjustments"`
	ThroughputBefore float64        `json:"throughput_before"`
	ThroughputAfter  float64        `json:"throughput_after"`
	EffectPercent    float64        `json:"effect_percent"`
	Reasoning        string         `json:"reasoning"`
}

// AITuningRecord represents a historical AI tuning recommendation.
type AITuningRecord struct {
	Timestamp        time.Time `json:"timestamp"`
	SourceDBType     string    `json:"source_db_type"`
	TargetDBType     string    `json:"target_db_type"`
	TotalTables      int       `json:"total_tables"`
	TotalRows        int64     `json:"total_rows"`
	AvgRowSizeBytes  int64     `json:"avg_row_size_bytes"`
	CPUCores         int       `json:"cpu_cores"`
	MemoryGB         int       `json:"memory_gb"`
	Workers          int       `json:"workers"`
	ChunkSize        int       `json:"chunk_size"`
	ReadAheadBuffers int       `json:"read_ahead_buffers"`
	WriteAheadWriters int      `json:"write_ahead_writers"`
	ParallelReaders  int       `json:"parallel_readers"`
	MaxPartitions    int       `json:"max_partitions"`
	EstimatedMemoryMB int64    `json:"estimated_memory_mb"`
	AIReasoning      string    `json:"ai_reasoning"`
	WasAIUsed        bool      `json:"was_ai_used"`
}

// SmartConfigAnalyzer analyzes source database metadata to suggest optimal configuration.
type SmartConfigAnalyzer struct {
	db              *sql.DB
	dbType          string // "mssql" or "postgres"
	targetDBType    string // target database type (if known)
	aiMapper        *AITypeMapper
	useAI           bool
	suggestions     *SmartConfigSuggestions
	historyProvider TuningHistoryProvider
}

// NewSmartConfigAnalyzer creates a new smart config analyzer.
func NewSmartConfigAnalyzer(db *sql.DB, dbType string, aiMapper *AITypeMapper) *SmartConfigAnalyzer {
	return &SmartConfigAnalyzer{
		db:       db,
		dbType:   dbType,
		aiMapper: aiMapper,
		useAI:    aiMapper != nil,
		suggestions: &SmartConfigSuggestions{
			DateColumns:   make(map[string][]string),
			ExcludeTables: []string{},
			Warnings:      []string{},
		},
	}
}

// SetHistoryProvider sets the history provider for learning from past analyses.
func (s *SmartConfigAnalyzer) SetHistoryProvider(provider TuningHistoryProvider) {
	s.historyProvider = provider
}

// SetTargetDBType sets the target database type for more accurate recommendations.
func (s *SmartConfigAnalyzer) SetTargetDBType(targetType string) {
	s.targetDBType = targetType
}

// Analyze performs smart configuration detection on the source database.
func (s *SmartConfigAnalyzer) Analyze(ctx context.Context, schema string) (*SmartConfigSuggestions, error) {
	logging.Debug("Analyzing database schema for configuration suggestions...")

	// Get all tables with their metadata
	tables, err := s.getTables(ctx, schema)
	if err != nil {
		return nil, fmt.Errorf("getting tables: %w", err)
	}

	// Calculate database statistics
	s.suggestions.TotalTables = len(tables)
	var totalRows int64
	for _, t := range tables {
		totalRows += t.RowCount
	}
	s.suggestions.TotalRows = totalRows

	// Analyze each table for date columns and exclude candidates
	for _, table := range tables {
		// Detect date columns
		dateColumns, err := s.detectDateColumns(ctx, schema, table.Name)
		if err != nil {
			logging.Warn("Warning: analyzing date columns for %s: %v", table.Name, err)
			continue
		}
		if len(dateColumns) > 0 {
			s.suggestions.DateColumns[table.Name] = dateColumns
		}

		// Detect exclude candidates
		if s.shouldExcludeTable(table.Name) {
			s.suggestions.ExcludeTables = append(s.suggestions.ExcludeTables, table.Name)
		}
	}

	// Calculate auto-tuned parameters
	s.calculateAutoTuneParams(ctx, tables)

	// Log summary
	logging.Debug("Smart config analysis complete:")
	logging.Debug("  - Tables: %d (%s rows)", s.suggestions.TotalTables, formatRowCount(s.suggestions.TotalRows))
	logging.Debug("  - Tables with date columns: %d", len(s.suggestions.DateColumns))
	logging.Debug("  - Suggested exclude tables: %d", len(s.suggestions.ExcludeTables))
	logging.Debug("  - Recommended: workers=%d, chunk_size=%d, read_ahead=%d",
		s.suggestions.Workers, s.suggestions.ChunkSizeRecommendation, s.suggestions.ReadAheadBuffers)
	logging.Debug("  - Estimated memory: %dMB", s.suggestions.EstimatedMemMB)

	return s.suggestions, nil
}

// calculateAutoTuneParams calculates all auto-tuned performance parameters.
// Always uses formula-based calculation, optionally gets AI suggestions too.
func (s *SmartConfigAnalyzer) calculateAutoTuneParams(ctx context.Context, tables []tableInfo) {
	// First calculate avg row size
	avgRowSize := s.calculateAvgRowSize(tables)
	s.suggestions.AvgRowSizeBytes = avgRowSize

	// Always calculate formula-based params first (as fallback)
	s.calculateFormulaBasedParams(tables, avgRowSize)

	// Try AI tuning as primary source if available
	wasAIUsed := false
	var aiReasoning string
	input := s.buildAutoTuneInput(tables, avgRowSize)

	if s.useAI && s.aiMapper != nil {
		output, err := s.getAIAutoTune(ctx, input)
		if err == nil && output != nil {
			// AI succeeded - use AI values as primary, keep formula as reference
			s.suggestions.AISuggestions = output
			s.applyAISuggestions(output)
			wasAIUsed = true
			aiReasoning = output.Reasoning
			logging.Debug("AI tuning applied as primary source")
		} else {
			logging.Debug("AI auto-tune unavailable, using formula-based: %v", err)
		}
	}

	// Save tuning result for future reference
	s.saveTuningResult(input, wasAIUsed, aiReasoning)
}

// saveTuningResult saves the tuning recommendation to history for future analyses.
func (s *SmartConfigAnalyzer) saveTuningResult(input AutoTuneInput, wasAIUsed bool, aiReasoning string) {
	if s.historyProvider == nil {
		return
	}

	record := AITuningRecord{
		Timestamp:         time.Now(),
		SourceDBType:      s.dbType,
		TargetDBType:      s.targetDBType,
		TotalTables:       s.suggestions.TotalTables,
		TotalRows:         s.suggestions.TotalRows,
		AvgRowSizeBytes:   s.suggestions.AvgRowSizeBytes,
		CPUCores:          input.CPUCores,
		MemoryGB:          input.MemoryGB,
		Workers:           s.suggestions.Workers,
		ChunkSize:         s.suggestions.ChunkSizeRecommendation,
		ReadAheadBuffers:  s.suggestions.ReadAheadBuffers,
		WriteAheadWriters: s.suggestions.WriteAheadWriters,
		ParallelReaders:   s.suggestions.ParallelReaders,
		MaxPartitions:     s.suggestions.MaxPartitions,
		EstimatedMemoryMB: s.suggestions.EstimatedMemMB,
		AIReasoning:       aiReasoning,
		WasAIUsed:         wasAIUsed,
	}

	if err := s.historyProvider.SaveAITuning(record); err != nil {
		logging.Debug("Failed to save tuning history: %v", err)
	}
}

// applyAISuggestions applies AI-recommended values to the suggestions.
func (s *SmartConfigAnalyzer) applyAISuggestions(ai *AutoTuneOutput) {
	s.suggestions.Workers = ai.Workers
	s.suggestions.ChunkSizeRecommendation = ai.ChunkSize
	s.suggestions.ReadAheadBuffers = ai.ReadAheadBuffers
	s.suggestions.WriteAheadWriters = ai.WriteAheadWriters
	s.suggestions.ParallelReaders = ai.ParallelReaders
	s.suggestions.MaxPartitions = ai.MaxPartitions
	s.suggestions.LargeTableThreshold = ai.LargeTableThreshold
	s.suggestions.MaxSourceConnections = ai.MaxSourceConnections
	s.suggestions.MaxTargetConnections = ai.MaxTargetConnections
	s.suggestions.UpsertMergeChunkSize = ai.UpsertMergeChunkSize
	s.suggestions.CheckpointFrequency = ai.CheckpointFrequency
	s.suggestions.MaxRetries = ai.MaxRetries
	s.suggestions.EstimatedMemMB = ai.EstimatedMemoryMB
}

// calculateAvgRowSize calculates average row size from top 5 largest tables.
func (s *SmartConfigAnalyzer) calculateAvgRowSize(tables []tableInfo) int64 {
	var totalSize int64
	var count int
	for i, t := range tables {
		if i >= 5 || t.RowCount == 0 {
			break
		}
		if t.AvgRowSizeBytes > 0 {
			totalSize += t.AvgRowSizeBytes
			count++
		}
	}
	avgRowSize := int64(500) // Default estimate
	if count > 0 {
		avgRowSize = totalSize / int64(count)
	}
	// Cap at reasonable max (very wide tables skew estimates)
	if avgRowSize > 2000 {
		avgRowSize = 2000
	}
	return avgRowSize
}

// buildAutoTuneInput constructs input for AI auto-tuning.
func (s *SmartConfigAnalyzer) buildAutoTuneInput(tables []tableInfo, avgRowSize int64) AutoTuneInput {
	// Get system info
	cores := runtime.NumCPU()
	memoryGB := 8 // Default
	if v, err := mem.VirtualMemory(); err == nil {
		memoryGB = int(v.Total / (1024 * 1024 * 1024))
	}

	// Build largest tables list
	var largestTables []TableStats
	for i, t := range tables {
		if i >= 5 {
			break
		}
		largestTables = append(largestTables, TableStats{
			Name:        t.Name,
			RowCount:    t.RowCount,
			AvgRowBytes: t.AvgRowSizeBytes,
		})
	}

	return AutoTuneInput{
		CPUCores:      cores,
		MemoryGB:      memoryGB,
		DatabaseType:  s.dbType,
		TotalTables:   s.suggestions.TotalTables,
		TotalRows:     s.suggestions.TotalRows,
		AvgRowBytes:   avgRowSize,
		LargestTables: largestTables,
	}
}

// formatHistoricalContext builds a historical context string from past tuning data.
func (s *SmartConfigAnalyzer) formatHistoricalContext() string {
	if s.historyProvider == nil {
		return ""
	}

	var sb strings.Builder

	// Get recent tuning recommendations
	tuningHistory, err := s.historyProvider.GetAITuningHistory(5)
	if err == nil && len(tuningHistory) > 0 {
		sb.WriteString("\nHISTORICAL TUNING RECOMMENDATIONS (from similar analyses):\n")
		for i, h := range tuningHistory {
			sb.WriteString(fmt.Sprintf("  %d. %s (%s, %d tables, %s rows):\n",
				i+1, h.SourceDBType, h.Timestamp.Format("2006-01-02"),
				h.TotalTables, formatRowCount(h.TotalRows)))
			sb.WriteString(fmt.Sprintf("     workers=%d, chunk=%d, read_ahead=%d, write_ahead=%d\n",
				h.Workers, h.ChunkSize, h.ReadAheadBuffers, h.WriteAheadWriters))
			if h.AIReasoning != "" {
				// Truncate long reasoning
				reasoning := h.AIReasoning
				if len(reasoning) > 100 {
					reasoning = reasoning[:100] + "..."
				}
				sb.WriteString(fmt.Sprintf("     reason: %s\n", reasoning))
			}
		}
	}

	// Get recent runtime adjustments (what worked during actual migrations)
	adjustments, err := s.historyProvider.GetAIAdjustments(10)
	if err == nil && len(adjustments) > 0 {
		// Summarize adjustments by action type
		actionStats := make(map[string]struct {
			count      int
			avgEffect  float64
			totalRows  float64
		})
		for _, adj := range adjustments {
			stat := actionStats[adj.Action]
			stat.count++
			stat.avgEffect += adj.EffectPercent
			actionStats[adj.Action] = stat
		}

		sb.WriteString("\nRUNTIME ADJUSTMENT HISTORY (what worked during migrations):\n")
		for action, stat := range actionStats {
			avgEffect := stat.avgEffect / float64(stat.count)
			sb.WriteString(fmt.Sprintf("  - %s: used %d times, avg effect: %.1f%% improvement\n",
				action, stat.count, avgEffect))
		}

		// Show some specific examples with high positive effects
		sb.WriteString("  Best performing adjustments:\n")
		shown := 0
		for _, adj := range adjustments {
			if adj.EffectPercent > 10 && shown < 3 {
				sb.WriteString(fmt.Sprintf("    - %s: %.1f%% improvement (before: %.0f rows/s, after: %.0f rows/s)\n",
					adj.Action, adj.EffectPercent, adj.ThroughputBefore, adj.ThroughputAfter))
				shown++
			}
		}
	}

	return sb.String()
}

// getAIAutoTune calls the AI to get auto-tuned parameters.
func (s *SmartConfigAnalyzer) getAIAutoTune(ctx context.Context, input AutoTuneInput) (*AutoTuneOutput, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshaling input: %w", err)
	}

	// Get historical context from past analyses and migrations
	historicalContext := s.formatHistoricalContext()

	// Calculate environment-based guardrails (all derived from actual hardware)
	// Max workers: CPU cores - 2 for OS overhead, minimum 2 for any parallelism
	maxWorkers := input.CPUCores - 2
	if maxWorkers < 2 {
		maxWorkers = 2
	}
	// No arbitrary upper cap - let the hardware dictate (32-core machines can use more workers)

	// Max memory: 50% of available RAM for migration buffers
	maxMemoryMB := input.MemoryGB * 1024 / 2
	if maxMemoryMB < 256 {
		maxMemoryMB = 256 // Bare minimum to function
	}

	// Max connections: scale with workers, but also consider typical DB limits
	// Formula: workers * 4 (for read/write parallelism) + overhead
	maxConnections := maxWorkers * 4
	if maxConnections < 10 {
		maxConnections = 10
	}
	// Only cap at very high numbers to avoid overwhelming DBs
	if input.CPUCores > 32 && maxConnections > 200 {
		maxConnections = 200 // Even large systems rarely need more
	}

	prompt := fmt.Sprintf(`You are a database migration performance expert. Recommend optimal configuration parameters based on the system constraints and historical data.

System and Database Info:
%s
%s
HARD CONSTRAINTS (must not exceed):
- max_workers: %d (based on %d CPU cores)
- max_memory_mb: %d (50%% of %dGB RAM)
- max_connections: %d per database
- Memory formula: workers * (read_ahead_buffers + write_ahead_writers) * chunk_size * avg_row_bytes / 1024 / 1024
  This MUST be <= max_memory_mb

Parameters to tune:
- workers: Parallel workers (1-%d)
- chunk_size: Rows per batch (10000-500000)
- read_ahead_buffers: Read buffers per worker (2-16)
- write_ahead_writers: Write threads per worker (2-4)
- parallel_readers: Parallel readers (2-4)
- max_partitions: Large table partitions (matches workers)
- large_table_threshold: Rows before partitioning (500000-10000000)
- max_source_connections: Source pool size (workers + parallel_readers + 2)
- max_target_connections: Target pool size (workers * write_ahead_writers + 2)
- upsert_merge_chunk_size: Upsert batch size (1000-10000)
- checkpoint_frequency: Checkpoint interval (10-100)
- max_retries: Retry count (2-5)

Guidelines:
1. CHUNK SIZE is most important - larger = higher throughput, but must fit in memory
2. Calculate memory usage and ensure it stays under max_memory_mb
3. Balance parallelism with available CPU cores
4. More workers = more connections needed
5. Learn from historical data: if certain adjustments improved performance, consider applying them proactively

Respond with ONLY a JSON object:
{
  "workers": <int>,
  "chunk_size": <int>,
  "read_ahead_buffers": <int>,
  "write_ahead_writers": <int>,
  "parallel_readers": <int>,
  "max_partitions": <int>,
  "large_table_threshold": <int>,
  "max_source_connections": <int>,
  "max_target_connections": <int>,
  "upsert_merge_chunk_size": <int>,
  "checkpoint_frequency": <int>,
  "max_retries": <int>,
  "estimated_memory_mb": <int>,
  "reasoning": "<brief explanation of choices, referencing historical data if relevant>"
}`, string(inputJSON), historicalContext, maxWorkers, input.CPUCores, maxMemoryMB, input.MemoryGB, maxConnections, maxWorkers)

	response, err := s.aiMapper.CallAI(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("calling AI: %w", err)
	}

	// Parse JSON response
	response = strings.TrimSpace(response)
	// Remove markdown code blocks if present
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var output AutoTuneOutput
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		return nil, fmt.Errorf("parsing AI response: %w (response: %s)", err, response)
	}

	// Enforce guardrails - clamp values to valid ranges based on environment
	// Workers: min 1, max based on CPU cores
	if output.Workers < 1 {
		output.Workers = 1
	}
	if output.Workers > maxWorkers {
		output.Workers = maxWorkers
	}

	// Chunk size: 10K-500K range
	if output.ChunkSize < 10000 {
		output.ChunkSize = 10000
	}
	if output.ChunkSize > 500000 {
		output.ChunkSize = 500000
	}

	// Read-ahead buffers: 2-16 range
	if output.ReadAheadBuffers < 2 {
		output.ReadAheadBuffers = 2
	}
	if output.ReadAheadBuffers > 16 {
		output.ReadAheadBuffers = 16
	}

	// Write-ahead writers: 2-4 range
	if output.WriteAheadWriters < 2 {
		output.WriteAheadWriters = 2
	}
	if output.WriteAheadWriters > 4 {
		output.WriteAheadWriters = 4
	}

	// Parallel readers: 2-4 range
	if output.ParallelReaders < 2 {
		output.ParallelReaders = 2
	}
	if output.ParallelReaders > 4 {
		output.ParallelReaders = 4
	}

	// Max partitions: match workers
	if output.MaxPartitions < 1 {
		output.MaxPartitions = output.Workers
	}
	if output.MaxPartitions > output.Workers {
		output.MaxPartitions = output.Workers
	}

	// Connection limits
	if output.MaxSourceConnections < output.Workers+2 {
		output.MaxSourceConnections = output.Workers + output.ParallelReaders + 2
	}
	if output.MaxSourceConnections > maxConnections {
		output.MaxSourceConnections = maxConnections
	}
	if output.MaxTargetConnections < output.Workers+2 {
		output.MaxTargetConnections = output.Workers*output.WriteAheadWriters + 2
	}
	if output.MaxTargetConnections > maxConnections {
		output.MaxTargetConnections = maxConnections
	}

	// Upsert chunk size: 1K-10K range
	if output.UpsertMergeChunkSize < 1000 {
		output.UpsertMergeChunkSize = 1000
	}
	if output.UpsertMergeChunkSize > 10000 {
		output.UpsertMergeChunkSize = 10000
	}

	// Checkpoint frequency: 10-100 range
	if output.CheckpointFrequency < 10 {
		output.CheckpointFrequency = 10
	}
	if output.CheckpointFrequency > 100 {
		output.CheckpointFrequency = 100
	}

	// Max retries: 2-5 range
	if output.MaxRetries < 2 {
		output.MaxRetries = 2
	}
	if output.MaxRetries > 5 {
		output.MaxRetries = 5
	}

	// Validate memory usage - reduce chunk size if needed
	estimatedMemMB := int64(output.Workers) * int64(output.ReadAheadBuffers+output.WriteAheadWriters) *
		int64(output.ChunkSize) * input.AvgRowBytes / 1024 / 1024
	if estimatedMemMB > int64(maxMemoryMB) {
		// Reduce chunk size to fit in memory
		safeChunkSize := int64(maxMemoryMB) * 1024 * 1024 /
			(int64(output.Workers) * int64(output.ReadAheadBuffers+output.WriteAheadWriters) * input.AvgRowBytes)
		if safeChunkSize < 10000 {
			safeChunkSize = 10000
		}
		output.ChunkSize = int(safeChunkSize)
		// Recalculate memory
		estimatedMemMB = int64(output.Workers) * int64(output.ReadAheadBuffers+output.WriteAheadWriters) *
			int64(output.ChunkSize) * input.AvgRowBytes / 1024 / 1024
	}
	output.EstimatedMemoryMB = estimatedMemMB

	return &output, nil
}

// calculateFormulaBasedParams calculates parameters using formulas (fallback).
func (s *SmartConfigAnalyzer) calculateFormulaBasedParams(tables []tableInfo, avgRowSize int64) {
	cores := runtime.NumCPU()

	// Workers: based on CPU cores (cores - 2, clamped to 4-12)
	s.suggestions.Workers = cores - 2
	if s.suggestions.Workers < 4 {
		s.suggestions.Workers = 4
	}
	if s.suggestions.Workers > 12 {
		s.suggestions.Workers = 12
	}

	// Chunk size: based on average row size (target ~50MB per chunk)
	s.suggestions.ChunkSizeRecommendation = s.calculateChunkSize(tables)

	// ReadAheadBuffers: based on memory budget and workers
	// Target ~200MB total memory, divided by workers and chunk size
	targetMemoryMB := int64(200)
	bytesPerChunk := int64(s.suggestions.ChunkSizeRecommendation) * avgRowSize
	buffersPerWorker := (targetMemoryMB * 1024 * 1024) / int64(s.suggestions.Workers) / bytesPerChunk
	s.suggestions.ReadAheadBuffers = int(buffersPerWorker)
	if s.suggestions.ReadAheadBuffers < 2 {
		s.suggestions.ReadAheadBuffers = 2
	}
	if s.suggestions.ReadAheadBuffers > 16 {
		s.suggestions.ReadAheadBuffers = 16
	}

	// WriteAheadWriters: 2-4 parallel writers per worker for I/O pipelining
	// More writers help when target database can handle concurrent inserts
	s.suggestions.WriteAheadWriters = 2
	if cores >= 8 {
		s.suggestions.WriteAheadWriters = 3
	}
	if cores >= 16 {
		s.suggestions.WriteAheadWriters = 4
	}

	// ParallelReaders: for reading partitioned tables concurrently
	// Usually matches write-ahead writers to balance I/O pipeline
	s.suggestions.ParallelReaders = s.suggestions.WriteAheadWriters

	// MaxPartitions: match workers for parallel processing
	s.suggestions.MaxPartitions = s.suggestions.Workers

	// LargeTableThreshold: tables above this get partitioned
	// Default 1M rows - tables larger than this benefit from parallel partitioning
	s.suggestions.LargeTableThreshold = 1000000

	// Connection pool tuning: based on workers and parallel I/O
	// Source needs connections for workers + parallel readers
	s.suggestions.MaxSourceConnections = s.suggestions.Workers + s.suggestions.ParallelReaders + 2
	// Target needs connections for workers + write-ahead writers
	s.suggestions.MaxTargetConnections = s.suggestions.Workers + s.suggestions.WriteAheadWriters*s.suggestions.Workers + 2
	// Cap at reasonable maximums
	if s.suggestions.MaxSourceConnections > 50 {
		s.suggestions.MaxSourceConnections = 50
	}
	if s.suggestions.MaxTargetConnections > 100 {
		s.suggestions.MaxTargetConnections = 100
	}

	// UpsertMergeChunkSize: smaller chunks for upsert to avoid lock contention
	// Typically 20-50% of regular chunk size
	s.suggestions.UpsertMergeChunkSize = s.suggestions.ChunkSizeRecommendation / 3
	if s.suggestions.UpsertMergeChunkSize < 1000 {
		s.suggestions.UpsertMergeChunkSize = 1000
	}
	if s.suggestions.UpsertMergeChunkSize > 10000 {
		s.suggestions.UpsertMergeChunkSize = 10000
	}

	// CheckpointFrequency: save state every N chunks for resumability
	// More frequent checkpoints = better resumability but more I/O
	// Base on total data size - larger migrations need less frequent checkpoints
	if s.suggestions.TotalRows > 100000000 { // > 100M rows
		s.suggestions.CheckpointFrequency = 50
	} else if s.suggestions.TotalRows > 10000000 { // > 10M rows
		s.suggestions.CheckpointFrequency = 20
	} else {
		s.suggestions.CheckpointFrequency = 10
	}

	// MaxRetries: retry failed chunks before giving up
	// More retries for larger/longer migrations
	s.suggestions.MaxRetries = 3

	// EstimatedMemoryMB: calculate expected memory usage
	// Formula: workers * (read_ahead + write_ahead) * chunk_size * avg_row_size
	totalBuffers := int64(s.suggestions.ReadAheadBuffers + s.suggestions.WriteAheadWriters)
	s.suggestions.EstimatedMemMB = (int64(s.suggestions.Workers) * totalBuffers *
		int64(s.suggestions.ChunkSizeRecommendation) * avgRowSize) / (1024 * 1024)
}

// formatRowCount formats large row counts with K/M/B suffixes.
func formatRowCount(count int64) string {
	if count >= 1000000000 {
		return fmt.Sprintf("%.1fB", float64(count)/1000000000)
	}
	if count >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(count)/1000000)
	}
	if count >= 1000 {
		return fmt.Sprintf("%.1fK", float64(count)/1000)
	}
	return fmt.Sprintf("%d", count)
}

// tableInfo holds basic table metadata.
type tableInfo struct {
	Name            string
	RowCount        int64
	AvgRowSizeBytes int64
}

// getTables retrieves table metadata from the source database.
func (s *SmartConfigAnalyzer) getTables(ctx context.Context, schema string) ([]tableInfo, error) {
	var query string
	switch s.dbType {
	case "mssql":
		query = `
			SELECT
				t.name AS table_name,
				p.rows AS row_count,
				ISNULL(SUM(a.total_pages) * 8 * 1024 / NULLIF(p.rows, 0), 0) AS avg_row_size
			FROM sys.tables t
			INNER JOIN sys.indexes i ON t.object_id = i.object_id
			INNER JOIN sys.partitions p ON i.object_id = p.object_id AND i.index_id = p.index_id
			INNER JOIN sys.allocation_units a ON p.partition_id = a.container_id
			INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
			WHERE s.name = @p1 AND i.index_id <= 1
			GROUP BY t.name, p.rows
			ORDER BY p.rows DESC`
	case "postgres":
		query = `
			SELECT
				relname AS table_name,
				COALESCE(n_live_tup, 0) AS row_count,
				CASE WHEN n_live_tup > 0
					THEN pg_relation_size(quote_ident(schemaname) || '.' || quote_ident(relname)) / n_live_tup
					ELSE 0
				END AS avg_row_size
			FROM pg_stat_user_tables
			WHERE schemaname = $1
			ORDER BY n_live_tup DESC`
	default:
		return nil, fmt.Errorf("unsupported database type: %s", s.dbType)
	}

	rows, err := s.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []tableInfo
	for rows.Next() {
		var t tableInfo
		if err := rows.Scan(&t.Name, &t.RowCount, &t.AvgRowSizeBytes); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}

	return tables, rows.Err()
}

// detectDateColumns finds columns that could be used for incremental sync.
func (s *SmartConfigAnalyzer) detectDateColumns(ctx context.Context, schema, table string) ([]string, error) {
	var query string
	switch s.dbType {
	case "mssql":
		query = `
			SELECT c.name
			FROM sys.columns c
			INNER JOIN sys.types t ON c.user_type_id = t.user_type_id
			INNER JOIN sys.tables tbl ON c.object_id = tbl.object_id
			INNER JOIN sys.schemas s ON tbl.schema_id = s.schema_id
			WHERE s.name = @p1 AND tbl.name = @p2
			  AND t.name IN ('datetime', 'datetime2', 'datetimeoffset', 'date', 'timestamp')
			ORDER BY c.column_id`
	case "postgres":
		query = `
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2
			  AND data_type IN ('timestamp without time zone', 'timestamp with time zone', 'date')
			ORDER BY ordinal_position`
	default:
		return nil, fmt.Errorf("unsupported database type: %s", s.dbType)
	}

	rows, err := s.db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dateColumns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		dateColumns = append(dateColumns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Rank columns by likelihood of being "updated at" columns
	return s.rankDateColumns(dateColumns), nil
}

// rankDateColumns sorts date columns by likelihood of being update timestamps.
func (s *SmartConfigAnalyzer) rankDateColumns(columns []string) []string {
	// Common patterns for update timestamp columns (in priority order)
	patterns := []string{
		`(?i)^updated_?at$`,
		`(?i)^modified_?(at|date|time)?$`,
		`(?i)^last_?modified`,
		`(?i)^changed_?(at|date)?$`,
		`(?i)update`,
		`(?i)modif`,
		`(?i)^created_?at$`,
		`(?i)^creation_?date$`,
		`(?i)create`,
	}

	type rankedCol struct {
		name  string
		score int
	}

	ranked := make([]rankedCol, 0, len(columns))
	for _, col := range columns {
		score := len(patterns) + 1 // Default low priority
		for i, pattern := range patterns {
			if matched, _ := regexp.MatchString(pattern, col); matched {
				score = i
				break
			}
		}
		ranked = append(ranked, rankedCol{name: col, score: score})
	}

	// Sort by score (lower is better)
	for i := 0; i < len(ranked)-1; i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].score < ranked[i].score {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}

	result := make([]string, len(ranked))
	for i, r := range ranked {
		result[i] = r.name
	}
	return result
}

// shouldExcludeTable determines if a table should be excluded from migration.
func (s *SmartConfigAnalyzer) shouldExcludeTable(tableName string) bool {
	lower := strings.ToLower(tableName)

	// Common patterns for tables that should be excluded
	excludePatterns := []string{
		`^temp_`,
		`_temp$`,
		`^tmp_`,
		`_tmp$`,
		`^log_`,
		`_log$`,
		`_logs$`,
		`^audit_`,
		`_audit$`,
		`^archive_`,
		`_archive$`,
		`_archived$`,
		`^backup_`,
		`_backup$`,
		`_bak$`,
		`^staging_`,
		`_staging$`,
		`^test_`,
		`_test$`,
		`^__`,           // Double underscore prefix (internal/system)
		`_history$`,     // History tables
		`^sysdiagrams$`, // SQL Server diagram table
		`^aspnet_`,      // ASP.NET membership tables
		`^elmah`,        // ELMAH error logging
	}

	for _, pattern := range excludePatterns {
		if matched, _ := regexp.MatchString(pattern, lower); matched {
			return true
		}
	}

	return false
}

// calculateChunkSize recommends an optimal chunk size based on table characteristics.
func (s *SmartConfigAnalyzer) calculateChunkSize(tables []tableInfo) int {
	if len(tables) == 0 {
		return 100000 // Default
	}

	// Find average row size of largest tables
	var totalSize int64
	var count int
	for i, t := range tables {
		if i >= 5 || t.RowCount == 0 { // Only consider top 5 largest tables
			break
		}
		totalSize += t.AvgRowSizeBytes
		count++
	}

	if count == 0 {
		return 100000 // Default
	}

	avgRowSize := totalSize / int64(count)

	// Target ~50MB per chunk for good throughput
	targetChunkBytes := int64(50 * 1024 * 1024)

	chunkSize := int(targetChunkBytes / avgRowSize)

	// Clamp to reasonable range
	if chunkSize < 10000 {
		chunkSize = 10000
	}
	if chunkSize > 500000 {
		chunkSize = 500000
	}

	// Round to nice number
	if chunkSize >= 100000 {
		chunkSize = (chunkSize / 50000) * 50000
	} else {
		chunkSize = (chunkSize / 10000) * 10000
	}

	return chunkSize
}

// FormatYAML returns the suggestions formatted as YAML config.
func (s *SmartConfigSuggestions) FormatYAML() string {
	var sb strings.Builder

	sb.WriteString("# Smart Configuration Suggestions\n")
	sb.WriteString(fmt.Sprintf("# Database: %d tables, %s rows, ~%d bytes/row avg\n\n",
		s.TotalTables, formatRowCount(s.TotalRows), s.AvgRowSizeBytes))

	sb.WriteString("migration:\n")

	// Indicate source of tuning
	if s.AISuggestions != nil {
		sb.WriteString("  # AI-tuned parameters (powered by AI analysis)\n")
	} else {
		sb.WriteString("  # Formula-based parameters (configure AI for smarter tuning)\n")
	}

	// Performance parameters
	sb.WriteString(fmt.Sprintf("  workers: %d\n", s.Workers))
	sb.WriteString(fmt.Sprintf("  chunk_size: %d\n", s.ChunkSizeRecommendation))
	sb.WriteString(fmt.Sprintf("  read_ahead_buffers: %d\n", s.ReadAheadBuffers))
	sb.WriteString(fmt.Sprintf("  write_ahead_writers: %d\n", s.WriteAheadWriters))
	sb.WriteString(fmt.Sprintf("  parallel_readers: %d\n", s.ParallelReaders))
	sb.WriteString(fmt.Sprintf("  max_partitions: %d\n", s.MaxPartitions))
	sb.WriteString(fmt.Sprintf("  large_table_threshold: %d\n", s.LargeTableThreshold))
	sb.WriteString(fmt.Sprintf("  max_source_connections: %d\n", s.MaxSourceConnections))
	sb.WriteString(fmt.Sprintf("  max_target_connections: %d\n", s.MaxTargetConnections))
	sb.WriteString(fmt.Sprintf("  upsert_merge_chunk_size: %d\n", s.UpsertMergeChunkSize))
	sb.WriteString(fmt.Sprintf("  checkpoint_frequency: %d\n", s.CheckpointFrequency))
	sb.WriteString(fmt.Sprintf("  max_retries: %d\n", s.MaxRetries))
	sb.WriteString(fmt.Sprintf("  # Estimated memory: ~%dMB\n", s.EstimatedMemMB))

	// Show AI reasoning if available
	if s.AISuggestions != nil && s.AISuggestions.Reasoning != "" {
		sb.WriteString(fmt.Sprintf("  # AI reasoning: %s\n", s.AISuggestions.Reasoning))
	}
	sb.WriteString("\n")

	// Date columns
	if len(s.DateColumns) > 0 {
		sb.WriteString("  # Date columns for incremental sync (priority order)\n")
		sb.WriteString("  date_updated_columns:\n")

		// Collect unique column names in priority order
		seen := make(map[string]bool)
		var columns []string
		for _, cols := range s.DateColumns {
			for _, col := range cols {
				if !seen[col] {
					seen[col] = true
					columns = append(columns, col)
				}
			}
		}

		for _, col := range columns {
			sb.WriteString(fmt.Sprintf("    - %s\n", col))
		}
		sb.WriteString("\n")
	}

	// Exclude tables
	if len(s.ExcludeTables) > 0 {
		sb.WriteString("  # Tables to exclude (temp/log/archive patterns)\n")
		sb.WriteString("  exclude_tables:\n")
		for _, table := range s.ExcludeTables {
			sb.WriteString(fmt.Sprintf("    - %s\n", table))
		}
		sb.WriteString("\n")
	}

	// Database tuning recommendations
	if s.SourceTuning != nil {
		sb.WriteString(s.formatDatabaseTuning(s.SourceTuning))
	}

	if s.TargetTuning != nil {
		sb.WriteString(s.formatDatabaseTuning(s.TargetTuning))
	}

	// Warnings
	if len(s.Warnings) > 0 {
		sb.WriteString("# Warnings:\n")
		for _, w := range s.Warnings {
			sb.WriteString(fmt.Sprintf("# - %s\n", w))
		}
	}

	return sb.String()
}

// formatDatabaseTuning formats database tuning recommendations in a human-readable format.
func (s *SmartConfigSuggestions) formatDatabaseTuning(tuning *dbtuning.DatabaseTuning) string {
	var sb strings.Builder

	// Header with visual separator
	sb.WriteString("\n")
	sb.WriteString("#" + strings.Repeat("=", 78) + "\n")
	sb.WriteString(fmt.Sprintf("# %s DATABASE TUNING (%s)\n", strings.ToUpper(tuning.Role), strings.ToUpper(tuning.DatabaseType)))
	sb.WriteString("#" + strings.Repeat("=", 78) + "\n")
	sb.WriteString(fmt.Sprintf("# Tuning Potential: %s\n", strings.ToUpper(tuning.TuningPotential)))
	sb.WriteString(fmt.Sprintf("# Impact: %s\n", tuning.EstimatedImpact))
	sb.WriteString("#" + strings.Repeat("-", 78) + "\n\n")

	if len(tuning.Recommendations) == 0 {
		if tuning.TuningPotential == "unknown" {
			// Analysis failed or wasn't performed
			sb.WriteString(fmt.Sprintf("# ⚠ Unable to analyze %s database tuning\n", tuning.Role))
			sb.WriteString(fmt.Sprintf("# Reason: %s\n\n", tuning.EstimatedImpact))
		} else {
			sb.WriteString(fmt.Sprintf("# ✓ No tuning needed - %s database is already well-configured!\n\n", tuning.Role))
		}
		return sb.String()
	}

	// Group by priority
	priority1 := []dbtuning.TuningRecommendation{}
	priority2 := []dbtuning.TuningRecommendation{}
	priority3 := []dbtuning.TuningRecommendation{}

	for _, rec := range tuning.Recommendations {
		switch rec.Priority {
		case 1:
			priority1 = append(priority1, rec)
		case 2:
			priority2 = append(priority2, rec)
		case 3:
			priority3 = append(priority3, rec)
		}
	}

	// Format recommendations by priority
	if len(priority1) > 0 {
		sb.WriteString("# 🔴 CRITICAL (Priority 1) - High Impact Changes\n")
		sb.WriteString("#" + strings.Repeat("-", 78) + "\n")
		for i, rec := range priority1 {
			sb.WriteString(s.formatRecommendation(i+1, rec))
		}
		sb.WriteString("\n")
	}

	if len(priority2) > 0 {
		sb.WriteString("# 🟡 IMPORTANT (Priority 2) - Medium Impact Changes\n")
		sb.WriteString("#" + strings.Repeat("-", 78) + "\n")
		for i, rec := range priority2 {
			sb.WriteString(s.formatRecommendation(i+1, rec))
		}
		sb.WriteString("\n")
	}

	if len(priority3) > 0 {
		sb.WriteString("# 🟢 OPTIONAL (Priority 3) - Nice to Have\n")
		sb.WriteString("#" + strings.Repeat("-", 78) + "\n")
		for i, rec := range priority3 {
			sb.WriteString(s.formatRecommendation(i+1, rec))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatRecommendation formats a single tuning recommendation in a human-readable format.
func (s *SmartConfigSuggestions) formatRecommendation(num int, rec dbtuning.TuningRecommendation) string {
	var sb strings.Builder

	// Parameter name and number
	sb.WriteString(fmt.Sprintf("#\n# %d. %s\n", num, rec.Parameter))

	// Current vs Recommended (side by side for easy comparison)
	sb.WriteString(fmt.Sprintf("#    Current:     %v\n", rec.CurrentValue))
	sb.WriteString(fmt.Sprintf("#    Recommended: %v\n", rec.RecommendedValue))
	sb.WriteString(fmt.Sprintf("#    Impact:      %s\n", strings.ToUpper(rec.Impact)))

	// Wrap reason text to 75 characters for readability
	sb.WriteString("#\n")
	sb.WriteString("#    Why: " + s.wrapText(rec.Reason, 75, "#         ") + "\n")

	// Show how to apply the change
	if rec.CanApplyRuntime && rec.SQLCommand != "" {
		sb.WriteString("#\n")
		sb.WriteString("#    ✓ Can apply at runtime (no restart needed):\n")
		// Wrap long SQL commands
		sqlLines := strings.Split(rec.SQLCommand, ";")
		for _, line := range sqlLines {
			line = strings.TrimSpace(line)
			if line != "" {
				sb.WriteString("#      " + line + ";\n")
			}
		}
	} else if rec.RequiresRestart {
		sb.WriteString("#\n")
		sb.WriteString("#    ⚠ Requires database restart\n")
		if rec.ConfigFile != "" {
			sb.WriteString("#    Add to config file:\n")
			lines := strings.Split(rec.ConfigFile, "\n")
			for _, line := range lines {
				if line != "" {
					sb.WriteString("#      " + line + "\n")
				}
			}
		}
	}

	return sb.String()
}

// wrapText wraps text to maxWidth characters with the given prefix for continuation lines
func (s *SmartConfigSuggestions) wrapText(text string, maxWidth int, contPrefix string) string {
	if len(text) <= maxWidth {
		return text
	}

	var result strings.Builder
	words := strings.Fields(text)
	lineLen := 0

	for i, word := range words {
		wordLen := len(word)

		if i == 0 {
			// First word always goes on first line
			result.WriteString(word)
			lineLen = wordLen
		} else if lineLen+1+wordLen > maxWidth {
			// Start new line
			result.WriteString("\n" + contPrefix + word)
			lineLen = len(contPrefix) + wordLen
		} else {
			// Add to current line with space
			result.WriteString(" " + word)
			lineLen += 1 + wordLen
		}
	}

	return result.String()
}
