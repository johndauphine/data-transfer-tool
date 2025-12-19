# Code Review: mssql-pg-migrate

*Original review by Codex - Dec 2024*
*Status updates and Claude assessment - Dec 19, 2024*
*Full codebase review by Claude Code - Dec 19, 2024*

## Overview

`mssql-pg-migrate` is a Go-based CLI tool designed for high-performance data migration from SQL Server to PostgreSQL. It features parallel transfer, keyset pagination, and stateful resumption.

## Architectural Assessment

The project follows a clean, modular architecture:

*   **Orchestrator (`internal/orchestrator`)**: Acts as the central controller, managing the lifecycle of the migration (schema extraction -> DDL -> Data Transfer -> Constraints).
*   **Source/Target Abstraction (`internal/source`, `internal/target`)**: Encapsulates database-specific logic, allowing the core transfer logic to remain relatively agnostic.
*   **Transfer Engine (`internal/transfer`)**: Implements the core data movement logic with worker pools and pagination strategies.
*   **State Management (`internal/checkpoint`)**: Uses SQLite to persist state, enabling resume capabilities.

## Key Strengths

1.  **Performance-Oriented**:
    *   **Keyset Pagination**: Correctly implements keyset pagination (`WHERE pk > @last`) for efficient deep scrolling.
    *   **COPY Protocol**: Uses `pgx`'s `CopyFrom` for bulk loading, which is significantly faster than standard `INSERT` statements.
    *   **Parallelism**: Configurable worker pools and partition-based splitting for large tables.
    *   **UNLOGGED Tables**: Support for creating tables as `UNLOGGED` initially to speed up writes (implied by `CreateTableWithOptions`).

2.  **Robustness**:
    *   **Idempotency**: The `cleanupPartitionData` function ensures that retrying a failed partition doesn't result in duplicates.
    *   **State Persistence**: Granular tracking of runs and tasks in SQLite.

3.  **Completeness**:
    *   Handles Schema, Data, Indexes, Foreign Keys, and Check Constraints.
    *   Includes validation steps (row counts and sampling).

## Findings & Recommendations

### 1. Resume Logic Granularity
**Status**: 🔲 Open (P1 - Enhancement)

**Observation**: While `internal/checkpoint/state.go` has `SaveTransferProgress` and `GetTransferProgress`, the `Orchestrator.transferAll` method and `transfer.Execute` function do not fully utilize this for *intra-partition* resuming.
**Impact**: If a large partition fails halfway, it appears the tool retries the *entire* partition (after cleaning it up) rather than resuming from the last successful chunk.
**Recommendation**: Integrate `GetTransferProgress` into `transfer.Execute` to initialize `lastPK` and skip already transferred data, making resumes even more efficient.

**[Claude Assessment]**: Valid observation. The idempotent cleanup (`cleanupPartitionData`) mitigates data correctness issues on retry, but wastes work. For typical migrations (<1 hour), partition-level retry is acceptable. Chunk-level resume adds complexity (tracking lastPK per partition, handling composite PKs). Recommend deferring until there's a real need for very long-running migrations.

### 2. Type Conversion & Safety
**Status**: ⚠️ Acceptable Risk (P2)

**Observation**: `internal/transfer/transfer.go` uses `processValue` with type switches and `any`.
**Impact**: Runtime type assertion errors or silent data corruption could occur if unexpected types are encountered.
**Recommendation**: Consider using a more robust type mapping system or the `pgtype` package capabilities within `pgx` to handle conversions more safely.

**[Claude Assessment]**: The current approach handles all common SQL Server types (tested with SO2013's 106M rows and corporate Deltek data). The `default` case passes values through unchanged, which pgx handles reasonably. Adding explicit pgtype mappings would improve safety but adds maintenance burden. Current approach is pragmatic - fix types as edge cases emerge rather than over-engineering upfront.

### 3. UUID Handling
**Status**: ⚠️ Acceptable (P3 - Low Priority)

**Observation**: The `formatUUID` function manually rearranges bytes to handle SQL Server's mixed-endian GUID format.
**Impact**: While likely correct for standard `uniqueidentifier`, this is complex code that warrants unit tests to ensure it handles all edge cases (like nil/nulls) correctly.

**[Claude Assessment]**: The mixed-endian handling is necessary and correct - SQL Server stores GUIDs differently than standard UUID format. NULLs are handled upstream (processValue checks for nil). Unit tests would be nice but this code is stable and battle-tested. The Airflow Python version has equivalent logic that's been validated.

### 4. Check Constraint Conversion
**Status**: ⚠️ Known Limitation (P2)

**Observation**: `convertCheckDefinition` in `internal/target/pool.go` performs simple string replacements (e.g., removing brackets, replacing `getdate()`).
**Impact**: Complex SQL Server check constraints (using specific T-SQL functions or regex-like patterns) may fail to apply in PostgreSQL or behave differently.
**Recommendation**: Add a warning log or a "dry run" validation for check constraints to alert users to potential syntax incompatibilities.

**[Claude Assessment]**: Valid concern. The current approach handles common patterns but T-SQL has many functions without PostgreSQL equivalents. Constraint failures are already logged as warnings (not fatal), so migrations complete. A `--validate-constraints` dry-run option would be useful for pre-flight checks. Low priority since most constraints are simple comparisons.

### 5. Error Handling in Orchestrator
**Status**: ⚠️ By Design (P3)

**Observation**: In `Run`, errors during index, FK, and check constraint loading/creation are logged as warnings but do not stop the process.
**Impact**: A migration could report "success" (or partial success) even if critical constraints failed to apply.
**Recommendation**: Make this behavior configurable (e.g., `--fail-on-constraint-error`) or include a "completed with warnings" status.

**[Claude Assessment]**: This is intentional - data transfer is the critical path; constraints are secondary. Users can re-run constraint creation manually. The Slack notification includes constraint failure counts. A `--strict` mode that fails on any error would be a nice addition but low priority. The summary output already shows warnings clearly.

### 6. CSV Parsing
**Status**: ⚠️ Acceptable Risk (P3)

**Observation**: `splitCSV` in `internal/source/pool.go` splits on commas.
**Impact**: If a column name or object name contains a comma (unlikely but legal), this parsing will fail.
**Recommendation**: Use a proper CSV parser or a more robust delimiter strategy if possible.

**[Claude Assessment]**: Edge case that's extremely rare in practice. SQL Server allows commas in identifiers via brackets `[column,name]` but this is almost never done. The fix is trivial if needed but not worth the complexity for a theoretical issue. If encountered, users can rename the column.

### 7. Resume Starts a Fresh Run
**Status**: ✅ Fixed (Dec 19, 2024)

**Observation**: `Orchestrator.Resume` (`internal/orchestrator/orchestrator.go:567-588`) looks up the last incomplete run and pending tasks but then calls `Run`, which creates a brand-new run ID and reprocesses everything.
**Impact**: Recorded checkpoints are never used; the previous run stays marked "running" and the migration restarts from scratch, risking duplicate work or constraint failures.
**Recommendation**: Continue the existing run by reusing its ID/task records and seeding transfers with saved progress. If a restart is intentional, explicitly mark the old run as failed before starting a new one.

**[Claude Assessment]**: Confirmed - this is documented in GO_MIGRATOR_CORRECTNESS_NOTES.md as a known limitation. The checkpoint infrastructure exists but isn't wired up. For now, the tool works correctly with `drop_target_tables: true` (fresh start each run). True resume would require: (1) table-level completion tracking, (2) reusing run IDs, (3) skipping completed tables. This is the biggest missing feature but not a correctness bug since idempotent cleanup prevents duplicates.

**[FIXED]**: Implemented table-level resume:
- `Resume()` now reuses the existing run ID
- Tasks are created/tracked per table in SQLite
- Completed tables (verified by row count match) are skipped
- Incomplete tables are truncated and re-transferred

### 8. Run Status Not Finalized on Early Failures
**Status**: ✅ Fixed (Dec 19, 2024)

**Observation**: In `Run` (`internal/orchestrator/orchestrator.go:103-200`), failures during schema extraction or table creation return after notifying Slack but do not call `state.CompleteRun`; finalize errors behave similarly.
**Impact**: The state database keeps these runs in a perpetual "running" state, so `resume`/`status` calls report phantom active migrations and retries cannot make informed choices.
**Recommendation**: Ensure every error path marks the run as failed (or "completed with warnings") before returning so state stays consistent.

**[Claude Assessment]**: Valid bug. Should add `defer` to mark run as failed on any error, or use a cleanup pattern. The SQLite state file is local so phantom runs don't affect other users, but it's confusing. Quick fix: add `state.FailRun(runID, err)` calls to early return paths. Related to #7 - both are state management issues.

**[FIXED]**: Added `state.CompleteRun(runID, "failed")` to all error paths:
- Schema extraction failure
- Schema creation failure
- Table creation/truncation failures
- Transfer failures
- Finalize failures
- Validation failures

### 9. Keyset Pagination Uses Wrong Column for `lastPK`
**Status**: ⚠️ Not a Bug (Clarification Needed)

**Observation**: `scanRows` assumes the first column is the PK (`internal/transfer/transfer.go:341-365`), and `executeKeysetPagination` overwrites the correct `lastPK` with that value (`internal/transfer/transfer.go:226-229`).
**Impact**: When the PK is not the first column in the select list, pagination advances on the wrong column, leading to skipped/duplicated chunks or endless loops.
**Recommendation**: Track `lastPK` using the actual PK column index only; drop the overwrite with `newLastPK` or compute it from `pkIdx`.

**[Claude Assessment]**: I believe this is NOT a bug. The SELECT query is constructed with PK column first: `SELECT [pk_col], [other_cols]... FROM table WHERE [pk_col] > @lastPK ORDER BY [pk_col]`. The PK is always position 0 in the result set by construction (see `executeKeysetPagination` query building). Verified this works correctly with SO2013 data. The observation may be based on misreading the query construction logic.

### 10. `exclude_tables` Config Ignored
**Status**: ✅ Fixed (Dec 19, 2024)

**Observation**: `MigrationConfig.ExcludeTables` is defined (`internal/config/config.go:48-56`) but never applied when building the table list.
**Impact**: Users cannot skip tables despite configuring them, so sensitive or unsupported tables still get migrated.
**Recommendation**: Filter `tables` in `Run` before DDL/transfer, and log which tables were skipped for transparency.

**[Claude Assessment]**: Confirmed - I discovered this while testing. The config field exists but filtering logic is missing from `orchestrator.Run()`. Should be a simple fix: filter `o.tables` after schema extraction using glob/regex matching against `ExcludeTables`. Also need `IncludeTables` for the inverse case. Quick win that would improve usability significantly.

**[FIXED]**: Implemented `filterTables()` in orchestrator:
- Added `include_tables` config option (new)
- `exclude_tables` now works with glob patterns
- Case-insensitive matching
- Logs skipped tables: `Skipped N tables by filter: [table1, table2, ...]`
- Applied in both `Run()` and `Resume()`

### 11. Partitioned Transfers Can Truncate In-Flight Partitions
**Status**: 🔴 CONFIRMED (P1 - Data Loss Risk)

**Observation**: Partitioned jobs run concurrently and job `partition_id == 1` truncates the whole target table at the start (`internal/transfer/transfer.go:55-67`). There is no per-table sequencing in the worker pool (`internal/orchestrator/orchestrator.go:332-434`), so if partition 2 (or N) starts writing before partition 1 starts, partition 1 will later `TRUNCATE` and wipe rows already inserted by other partitions.
**Impact**: Silent data loss or duplicated work for any table split into partitions with worker count > 1.
**Recommendation**: Pre-truncate once per table before launching partition jobs (e.g., orchestrator-side) and/or serialize partitions per table (per-table semaphore or `sync.Once` + cleanup). Avoid letting a truncate run after any partition has started writing.

**[Claude Code Review - Dec 19, 2024]**: CONFIRMED as a real race condition.

**Race Condition Analysis**:

```
Code flow in orchestrator.go:395-407:
  for _, job := range jobs {        // Jobs submitted sequentially
      sem <- struct{}{}             // Semaphore limits concurrency
      go func(j transfer.Job) {     // Goroutine starts immediately
          transfer.Execute(...)     // Races with other goroutines
      }
  }
```

The race manifests when `Workers > 1`:

1. Jobs are submitted in order: Partition 1, Partition 2, Partition 3...
2. Goroutines start nearly simultaneously (semaphore only limits count, not order)
3. **If Partition 2's goroutine executes `transfer.Execute()` first**:
   - Skips TRUNCATE (PartitionID != 1)
   - Calls `cleanupPartitionData()` (does nothing - table is empty)
   - Begins inserting rows
4. **Partition 1's goroutine starts later**:
   - Calls `TRUNCATE TABLE` (PartitionID == 1)
   - **Wipes all data including Partition 2's rows**
5. Result: Data loss for Partition 2 (and any other partitions that started early)

**Probability Factors**:
- Higher with more workers (Workers=4+ makes this likely)
- Higher with more partitions per table
- Can occur even with Workers=2 if partition 1's goroutine is delayed

**Workarounds**:
- Set `Workers: 1` (eliminates parallelism, defeats purpose)
- Use small `large_table_threshold` to avoid partitioning

**Recommended Fix**: Pre-truncate in orchestrator before dispatching partition jobs:

```go
// In transferAll(), before job dispatch:
truncatedTables := make(map[string]bool)
for _, j := range jobs {
    if j.Partition != nil && !truncatedTables[j.Table.Name] {
        tgtPool.TruncateTable(ctx, targetSchema, j.Table.Name)
        truncatedTables[j.Table.Name] = true
    }
}
// Then dispatch jobs (remove truncate from transfer.Execute)
```

### 12. Sample Validation Ignores Composite Primary Keys
**Status**: ⚠️ CONFIRMED (P3 - Coverage Gap)

**Observation**: `validateSamples` only pulls/compares the first PK column (`internal/orchestrator/orchestrator.go:598-639`), even when a table has a composite PK.
**Impact**: Composite-PK tables can report false positives/negatives; missing rows may go undetected because only part of the key is compared.
**Recommendation**: Build sampling and existence checks over all PK columns, or explicitly skip composite-PK tables with a clear warning so users know validation did not run.

**[Claude Code Review - Dec 19, 2024]**: Confirmed. Line 599: `pkCol := t.PrimaryKey[0]` only uses first PK column.

For composite PKs (e.g., `(TenantID, OrderID)`), sample validation:
- Samples only `TenantID` values from source
- Checks if those `TenantID` values exist in target (ignoring `OrderID`)
- May report false positives (row exists with same TenantID but different OrderID)
- May miss actual data corruption

**Severity**: P3 (low) because:
- Row count validation still works for detecting gross data loss
- Sample validation is optional (`sample_validation: false` to disable)
- Most tables have single-column PKs

**Recommended Fix**: Skip composite-PK tables with a warning:
```go
if len(t.PrimaryKey) > 1 {
    fmt.Printf("%-30s SKIP (composite PK)\n", t.Name)
    continue
}
```

## Conclusion

The codebase is high-quality and performance-focused, but the open P1 (partitioned transfer truncate race) must be fixed before considering production use. The remaining items are robustness/coverage improvements.

---

## Status Summary (Dec 19, 2024)

| # | Finding | Status | Priority |
|---|---------|--------|----------|
| 1 | Resume Logic Granularity | ⚠️ Table-level (not chunk) | P3 |
| 2 | Type Conversion & Safety | ⚠️ Acceptable | P3 |
| 3 | UUID Handling | ⚠️ Acceptable | P3 |
| 4 | Check Constraint Conversion | ⚠️ Known Limitation | P3 |
| 5 | Error Handling in Orchestrator | ⚠️ By Design | P3 |
| 6 | CSV Parsing | ⚠️ Acceptable Risk | P3 |
| 7 | Resume Starts Fresh Run | ✅ Fixed | - |
| 8 | Run Status Not Finalized | ✅ Fixed | - |
| 9 | Keyset PK Column | ⚠️ Not a Bug | - |
| 10 | exclude_tables Ignored | ✅ Fixed | - |
| 11 | Partitioned Truncate Race | 🔴 **CONFIRMED** | **P1** |
| 12 | Sample Validation Composite PKs | ⚠️ Confirmed | P3 |

### Fixes Implemented (Dec 19, 2024)

**#7 - Resume Now Works Properly**
- Reuses existing run ID instead of creating new one
- Tracks table completion in SQLite tasks table
- Skips already-complete tables (verified by row count)
- Re-transfers incomplete tables with truncate-first for idempotency

**#8 - Run Status Finalized on All Error Paths**
- Added `state.CompleteRun(runID, "failed")` to all early return error paths
- Schema extraction, table creation, transfer, finalize all mark run as failed

**#10 - Table Filtering Implemented**
- `include_tables`: Only migrate tables matching glob patterns
- `exclude_tables`: Skip tables matching glob patterns
- Case-insensitive matching
- Logs which tables were skipped

### Additional Fixes (from GO_MIGRATOR_CORRECTNESS_NOTES.md)

- ✅ **Fail-fast for tables without PK** - Prevents silent data corruption
- ✅ **validateSamples respects StrictConsistency** - Consistent NOLOCK behavior

### Overall Assessment

**Production Ready**: ⚠️ **CONDITIONAL** - Safe with `Workers: 1` or small tables

| Configuration | Safe? | Notes |
|---------------|-------|-------|
| `Workers: 1` | ✅ Yes | No parallelism, but no race condition |
| Small tables (< threshold) | ✅ Yes | No partitioning = no race |
| `Workers: 2+` with large tables | 🔴 **NO** | Race condition can cause data loss |

**What Works Well**:
- Core data transfer path is performant (tested with 106M rows in SO2013)
- Resume works at table level (skips completed tables, verified by row count)
- Table filtering works (include/exclude glob patterns)
- All error paths properly finalize run status
- Keyset pagination is correct and efficient
- Type conversions handle all common SQL Server types

**Critical Issue**:
Finding #11 (Partitioned Truncate Race) **MUST be fixed** before using with:
- `Workers > 1` AND
- Large tables that trigger partitioning

**Recommended Fix**: ~10 lines of code - pre-truncate tables in orchestrator before dispatching partition jobs.

---

*Last reviewed: Dec 19, 2024 by Claude Code*