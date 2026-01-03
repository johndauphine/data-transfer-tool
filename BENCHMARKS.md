# Performance Benchmarks

Comprehensive benchmark results comparing Go and Rust implementations.

## Test Environment

- **Hardware**: Apple M3 Pro, 36GB RAM, 14 CPU cores
- **OS**: macOS (Darwin 25.2.0)
- **Databases**: SQL Server 2022, PostgreSQL 15 (Docker containers)
- **Dataset**: StackOverflow2010 (~19.3M rows, 9 tables)
- **Date**: January 2026

> **Note**: SQL Server runs under Rosetta 2 emulation on Apple Silicon, adding overhead. Production Linux deployments will be faster.

## Dataset Details

| Table | Rows | Description |
|-------|------|-------------|
| Votes | 10,143,364 | Largest table |
| Comments | 3,875,183 | Text-heavy |
| Posts | 3,729,195 | Mixed content types |
| Badges | 1,102,020 | Datetime columns |
| Users | 299,398 | Various types |
| PostLinks | 161,519 | Foreign keys |
| VoteTypes | 15 | Small lookup |
| PostTypes | 10 | Small lookup |
| LinkTypes | 2 | Small lookup |
| **Total** | **19,310,706** | |

## Go vs Rust Comparison

### MSSQL → PostgreSQL

| Mode | Go | Rust | Notes |
|------|-----|------|-------|
| drop_recreate | **287,000 rows/s** (75s) | 289,372 rows/s (74s) | Comparable |
| upsert | 72,628 rows/s (266s) | **181,450 rows/s** (106s) | Rust 2.5x faster |

### PostgreSQL → MSSQL

| Mode | Go | Rust | Notes |
|------|-----|------|-------|
| drop_recreate | **140,387 rows/s** (137s) | 145,175 rows/s (133s) | Comparable |
| upsert | 68,825 rows/s (280s) | **80,075 rows/s** (241s) | Rust 16% faster |

### Memory Usage

| Direction | Mode | Go Peak | Rust Peak |
|-----------|------|---------|-----------|
| MSSQL→PG | drop_recreate | 5.3 GB | 4.9 GB |
| MSSQL→PG | upsert | 10.9 GB | 7.4 GB |
| PG→MSSQL | drop_recreate | **1.8 GB** | 5.4 GB |
| PG→MSSQL | upsert | **0.5 GB** | 3.7 GB |

## Key Findings

### 1. Bulk Load Performance is Comparable
Both implementations achieve similar throughput for bulk loads (~290K rows/sec MSSQL→PG). The bottleneck is database I/O, not the application.

### 2. Go Has Lower Memory Usage for PG→MSSQL
Go uses significantly less memory for PostgreSQL to MSSQL migrations due to different buffering strategies and Go's garbage collector reclaiming memory more aggressively.

### 3. Upsert Mode Favors Rust
Rust is faster for upsert operations due to:
- Zero-cost abstractions vs GC overhead during high-allocation workloads
- More efficient async scheduling under sustained load

### 4. PG→MSSQL is ~50% Slower Than MSSQL→PG
Both implementations show this pattern because:
- PostgreSQL COPY protocol is extremely optimized for bulk writes
- MSSQL bulk insert has more TDS protocol overhead
- MSSQL uses UTF-16 (NVARCHAR) vs PostgreSQL's UTF-8

## Implemented Optimizations

- [x] Parallel table processing with configurable workers
- [x] Connection pooling for concurrent operations
- [x] Batch processing with configurable chunk sizes
- [x] Staging table approach for upsert mode
- [x] Intra-table partitioning for large tables
- [x] Progress reporting with throughput metrics

## Reproduction

```bash
# Build
go build -o mssql-pg-migrate .

# MSSQL → PostgreSQL (drop_recreate)
./mssql-pg-migrate -config benchmark-config.yaml run

# MSSQL → PostgreSQL (upsert)
./mssql-pg-migrate -config benchmark-upsert-clean.yaml run

# PostgreSQL → MSSQL (drop_recreate)
./mssql-pg-migrate -config benchmark-pg-to-mssql-clean.yaml run
```

## Configuration

```yaml
migration:
  workers: 6
  chunk_size: 100000  # 50000 for upsert
  target_mode: drop_recreate  # or upsert
  create_indexes: false
  create_foreign_keys: false
```

## When to Use Go vs Rust

| Use Case | Recommendation |
|----------|----------------|
| Bulk migrations (drop_recreate) | Either - performance is comparable |
| Memory-constrained environments | **Go** - lower memory usage for PG→MSSQL |
| Upsert-heavy workloads | **Rust** - 2.5x faster for MSSQL→PG upsert |
| Maximum throughput | **Rust** - slightly better across the board |
