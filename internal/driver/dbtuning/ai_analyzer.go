package dbtuning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// AIQuerier represents anything that can query AI.
type AIQuerier interface {
	Query(ctx context.Context, prompt string) (string, error)
}

// AITuningAnalyzer uses AI to analyze database configuration and generate recommendations.
type AITuningAnalyzer struct {
	aiMapper AIQuerier
}

// NewAITuningAnalyzer creates a new AI-driven tuning analyzer.
func NewAITuningAnalyzer(aiMapper AIQuerier) *AITuningAnalyzer {
	return &AITuningAnalyzer{
		aiMapper: aiMapper,
	}
}

// AnalyzeDatabase analyzes database configuration and generates tuning recommendations.
func (a *AITuningAnalyzer) AnalyzeDatabase(
	ctx context.Context,
	db *sql.DB,
	dbType string,
	role string,
	stats SchemaStatistics,
) (*DatabaseTuning, error) {
	if a.aiMapper == nil {
		return &DatabaseTuning{
			DatabaseType:    dbType,
			Role:            role,
			TuningPotential: "unknown",
			EstimatedImpact: "AI not configured - set ai.api_key to enable database tuning recommendations",
		}, nil
	}

	// Step 1: Use AI to generate SQL queries to interrogate database configuration
	sqlQueries, err := a.generateInterrogationSQL(ctx, dbType, role)
	if err != nil {
		return nil, fmt.Errorf("generating interrogation SQL: %w", err)
	}

	// Step 2: Execute the AI-generated SQL queries
	configData, err := a.executeConfigQueries(ctx, db, sqlQueries)
	if err != nil {
		return nil, fmt.Errorf("executing config queries: %w", err)
	}

	// Step 3: Use AI to analyze configuration and generate recommendations
	recommendations, potential, impact, err := a.generateRecommendations(ctx, dbType, role, stats, configData)
	if err != nil {
		return nil, fmt.Errorf("generating recommendations: %w", err)
	}

	return &DatabaseTuning{
		DatabaseType:    dbType,
		Role:            role,
		CurrentSettings: configData,
		Recommendations: recommendations,
		TuningPotential: potential,
		EstimatedImpact: impact,
	}, nil
}

// generateInterrogationSQL uses AI to generate SQL queries for database configuration.
func (a *AITuningAnalyzer) generateInterrogationSQL(ctx context.Context, dbType string, role string) ([]string, error) {
	prompt := fmt.Sprintf(`Generate SQL queries to retrieve database configuration settings for %s optimization.

Database Type: %s
Role: %s (source or target in a database migration tool)

Requirements:
1. Generate SQL queries that retrieve ALL relevant configuration parameters
2. For MySQL: Use SHOW VARIABLES, SHOW STATUS, SHOW GLOBAL VARIABLES
3. For PostgreSQL: Query pg_settings, pg_stat_database
4. For MSSQL: Use sp_configure, sys.dm_os_sys_info DMVs
5. For Oracle: Query V$PARAMETER, V$SYSTEM_PARAMETER

Return ONLY valid SQL queries, one per line, no explanations.
Focus on settings relevant for %s database in migration workloads:
- Buffer pools and caches
- I/O settings
- Connection limits
- Write/flush behavior
- Parallel processing
- Memory settings

Output format (JSON array of strings):
["SQL query 1", "SQL query 2", ...]`, role, dbType, role)

	response, err := a.aiMapper.Query(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("AI query failed: %w", err)
	}

	// Parse JSON array of SQL queries
	var queries []string
	// Try to extract JSON from response
	jsonStart := strings.Index(response, "[")
	jsonEnd := strings.LastIndex(response, "]")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		jsonStr := response[jsonStart : jsonEnd+1]
		if err := json.Unmarshal([]byte(jsonStr), &queries); err != nil {
			return nil, fmt.Errorf("parsing AI response: %w", err)
		}
	} else {
		return nil, fmt.Errorf("AI response did not contain JSON array: %s", response)
	}

	return queries, nil
}

// executeConfigQueries executes SQL queries and collects configuration data.
func (a *AITuningAnalyzer) executeConfigQueries(ctx context.Context, db *sql.DB, queries []string) (map[string]interface{}, error) {
	config := make(map[string]interface{})

	for i, query := range queries {
		queryKey := fmt.Sprintf("query_%d", i+1)

		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			// Log error but continue with other queries
			config[queryKey+"_error"] = err.Error()
			continue
		}

		// Get column names
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			continue
		}

		// Collect all rows
		var results []map[string]interface{}
		for rows.Next() {
			// Create a slice of interface{} to hold each column value
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range columns {
				valuePtrs[i] = &values[i]
			}

			if err := rows.Scan(valuePtrs...); err != nil {
				continue
			}

			// Convert to map
			row := make(map[string]interface{})
			for i, col := range columns {
				val := values[i]
				// Convert []byte to string
				if b, ok := val.([]byte); ok {
					row[col] = string(b)
				} else {
					row[col] = val
				}
			}
			results = append(results, row)
		}
		rows.Close()

		config[queryKey] = results
	}

	return config, nil
}

// generateRecommendations uses AI to analyze configuration and generate recommendations.
func (a *AITuningAnalyzer) generateRecommendations(
	ctx context.Context,
	dbType string,
	role string,
	stats SchemaStatistics,
	configData map[string]interface{},
) ([]TuningRecommendation, string, string, error) {
	// Serialize config data to JSON for AI
	configJSON, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return nil, "", "", fmt.Errorf("marshaling config: %w", err)
	}

	prompt := fmt.Sprintf(`Analyze database configuration and provide tuning recommendations for migration workload.

Database Type: %s
Role: %s database
Schema Statistics:
- Total Tables: %d
- Total Rows: %d
- Average Row Size: %d bytes
- Estimated Memory: %d MB

Current Configuration:
%s

Task: Analyze this configuration for a %s database in a migration tool (dmt).

Migration Workload Characteristics:
- %s database performs sequential full table scans
- Target database performs high-volume bulk inserts
- No user queries during migration
- Can tolerate 1-second data loss on crash
- Optimize for throughput over ACID guarantees

Generate tuning recommendations in the following JSON format:
{
  "recommendations": [
    {
      "parameter": "parameter_name",
      "current_value": "current value",
      "recommended_value": "recommended value",
      "impact": "high|medium|low",
      "reason": "why this change helps migration performance",
      "priority": 1|2|3,
      "can_apply_runtime": true|false,
      "sql_command": "SET GLOBAL param = value;" or "",
      "requires_restart": true|false,
      "config_file": "config file snippet" or ""
    }
  ],
  "tuning_potential": "high|medium|low|none",
  "estimated_impact": "description of expected performance improvement"
}

Focus on:
1. Buffer pools (allocate 50-70%% RAM for dedicated servers)
2. Write optimization (disable sync commits, increase buffer sizes)
3. I/O capacity (optimize for SSDs)
4. Connection limits (ensure sufficient for parallel workers)
5. Checkpoint/WAL settings (reduce frequency during bulk loads)

Return ONLY the JSON object, no other text.`, dbType, role, stats.TotalTables, stats.TotalRows, stats.AvgRowSizeBytes, stats.EstimatedMemMB, string(configJSON), role, role)

	response, err := a.aiMapper.Query(ctx, prompt)
	if err != nil {
		return nil, "", "", fmt.Errorf("AI query failed: %w", err)
	}

	// Parse JSON response
	type AIRecommendation struct {
		Parameter        string      `json:"parameter"`
		CurrentValue     interface{} `json:"current_value"`
		RecommendedValue interface{} `json:"recommended_value"`
		Impact           string      `json:"impact"`
		Reason           string      `json:"reason"`
		Priority         int         `json:"priority"`
		CanApplyRuntime  bool        `json:"can_apply_runtime"`
		SQLCommand       string      `json:"sql_command"`
		RequiresRestart  bool        `json:"requires_restart"`
		ConfigFile       string      `json:"config_file"`
	}

	type AIResponse struct {
		Recommendations  []AIRecommendation `json:"recommendations"`
		TuningPotential  string             `json:"tuning_potential"`
		EstimatedImpact  string             `json:"estimated_impact"`
	}

	// Extract JSON from response
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart < 0 || jsonEnd <= jsonStart {
		return nil, "", "", fmt.Errorf("AI response did not contain JSON object: %s", response)
	}

	jsonStr := response[jsonStart : jsonEnd+1]
	var aiResp AIResponse
	if err := json.Unmarshal([]byte(jsonStr), &aiResp); err != nil {
		return nil, "", "", fmt.Errorf("parsing AI response: %w\nResponse: %s", err, jsonStr)
	}

	// Convert AI recommendations to TuningRecommendation
	recommendations := make([]TuningRecommendation, len(aiResp.Recommendations))
	for i, rec := range aiResp.Recommendations {
		recommendations[i] = TuningRecommendation{
			Parameter:        rec.Parameter,
			CurrentValue:     rec.CurrentValue,
			RecommendedValue: rec.RecommendedValue,
			Impact:           rec.Impact,
			Reason:           rec.Reason,
			Priority:         rec.Priority,
			CanApplyRuntime:  rec.CanApplyRuntime,
			SQLCommand:       rec.SQLCommand,
			RequiresRestart:  rec.RequiresRestart,
			ConfigFile:       rec.ConfigFile,
		}
	}

	return recommendations, aiResp.TuningPotential, aiResp.EstimatedImpact, nil
}
