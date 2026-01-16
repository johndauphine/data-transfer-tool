# MySQL Source Database Tuning Guide

## Overview

Testing revealed that MySQL as a source database performs significantly slower than MSSQL or PostgreSQL (90K vs 308K/255K rows/sec). This guide documents optimizations to improve MySQL source read performance by 2-3x.

## Baseline Performance

### Before Tuning
- **Throughput**: 90,228 rows/sec
- **Dataset**: 29.5M rows (partial, StackOverflow 2013)
- **Bottleneck**: MySQL sequential scan performance
- **Connection waits**: 132 waits, 97.9ms average
- **Comparison**: 3.4x slower than MSSQL (308K), 2.8x slower than PostgreSQL (255K)

### Identified Issues
1. **Small read buffers**: 128KB default inadequate for bulk scans
2. **Limited table cache**: 200 table handles insufficient for concurrent access
3. **Small temp tables**: 16MB limiting partition calculations
4. **Connection overhead**: High connection wait times

## MySQL Source Optimizations

### Applied Configuration

```sql
-- Sequential read buffer (table scans)
SET GLOBAL read_buffer_size = 8388608;        -- 8MB (was 128KB, 64x improvement)

-- Random read buffer (sorted reads, ORDER BY)
SET GLOBAL read_rnd_buffer_size = 16777216;   -- 16MB (was 256KB, 64x improvement)

-- Sort buffer (partition calculations)
SET GLOBAL sort_buffer_size = 8388608;        -- 8MB (was 256KB, 32x improvement)

-- Join buffer (if queries use joins)
SET GLOBAL join_buffer_size = 8388608;        -- 8MB (was 256KB, 32x improvement)

-- Table cache for concurrent access
SET GLOBAL table_open_cache = 4000;           -- 4000 (was 200, 20x improvement)
SET GLOBAL table_definition_cache = 2000;     -- 2000 (was 400, 5x improvement)

-- Thread reuse for connection efficiency
SET GLOBAL thread_cache_size = 100;           -- 100 (was 9, 11x improvement)

-- Temporary tables for partition boundary calculations
SET GLOBAL tmp_table_size = 268435456;        -- 256MB (was 16MB, 16x improvement)
SET GLOBAL max_heap_table_size = 268435456;   -- 256MB (was 16MB, 16x improvement)

-- Disable metadata stats updates on queries
SET GLOBAL innodb_stats_on_metadata = 0;      -- Reduce overhead
```

### Verification

```sql
SHOW VARIABLES WHERE Variable_name IN (
    'read_buffer_size',
    'read_rnd_buffer_size',
    'sort_buffer_size',
    'join_buffer_size',
    'table_open_cache',
    'thread_cache_size',
    'tmp_table_size',
    'max_heap_table_size'
);
```

## Expected Performance Improvement

### Conservative Estimate (2x)
Based on buffer size improvements alone:
- **Baseline**: 90K rows/sec
- **With tuning**: 180K rows/sec
- **Improvement**: 2.0x

### Optimistic Estimate (3x)
Including table cache and connection optimization benefits:
- **Baseline**: 90K rows/sec
- **With tuning**: 270K rows/sec
- **Improvement**: 3.0x

### Realistic Target
- **Expected**: 200-250K rows/sec
- **Still slower than**: MSSQL (308K)
- **Competitive with**: PostgreSQL (255K)

## Why These Settings Work

### read_buffer_size (8MB)
- Used for sequential table scans
- Migration reads entire tables sequentially
- Larger buffer = fewer I/O operations
- **Impact**: 2-3x faster sequential scans

### read_rnd_buffer_size (16MB)
- Used for sorted reads (ORDER BY operations)
- Partition calculations require sorted data
- **Impact**: Faster partition boundary detection

### sort_buffer_size (8MB)
- Used for ORDER BY, GROUP BY operations
- Critical for partition boundary calculations on large tables
- **Impact**: Reduces disk-based sorting

### table_open_cache (4000)
- Caches table file descriptors
- Parallel workers accessing multiple tables
- **Impact**: Eliminates table open/close overhead

### tmp_table_size (256MB)
- In-memory temporary tables
- Used during partition boundary calculations
- **Impact**: Keeps calculations in RAM vs disk

## Comparison: MySQL vs Other Sources

| Optimization | MySQL | MSSQL | PostgreSQL |
|-------------|-------|-------|------------|
| **Default performance** | 90K/s | 308K/s | 255K/s |
| **Main bottleneck** | Small buffers | Already optimal | Already good |
| **Tuning effort** | High | None | Minimal |
| **After tuning** | ~200K/s* | 308K/s | 255K/s |
| **Best for** | Target DB | Source DB | Balanced |

*Projected based on buffer improvements

## When MySQL is Slow as Source

### Root Causes

1. **InnoDB optimized for writes**
   - Buffer pool prioritizes write caching
   - Read-ahead logic optimized for OLTP, not bulk scans

2. **Small default buffers**
   - Optimized for many small queries
   - Not optimized for large sequential scans

3. **Connection overhead**
   - Thread creation/destruction overhead
   - Connection pooling less efficient than PostgreSQL

4. **Partition calculation cost**
   - MIN/MAX queries on large tables slow
   - Temporary table limitations

### Architecture Differences

**MSSQL Advantages:**
- Page compression reduces I/O
- Read-ahead optimized for sequential scans
- Better query optimizer for range queries

**PostgreSQL Advantages:**
- MVCC reduces lock contention
- Better concurrent query performance
- Efficient connection pooling

**MySQL Limitations:**
- InnoDB designed for write-heavy workloads
- Buffer pool optimized for caching, not scanning
- Limited parallel query capabilities

## Production Recommendations

### For MySQL as Source

**Do:**
- ✅ Apply all buffer size optimizations
- ✅ Increase table cache significantly
- ✅ Use larger chunk sizes (100K+ rows)
- ✅ Increase parallel readers (6-8)
- ✅ Monitor connection wait times
- ✅ Consider using MyISAM for read-only source data (faster scans)

**Don't:**
- ❌ Use default buffer sizes for production
- ❌ Expect MSSQL-level performance
- ❌ Use tiny chunk sizes (<10K rows)
- ❌ Skip connection pool tuning

### Alternative: MySQL → MySQL Migration

For MySQL → MySQL migrations, consider:
- Same optimizations apply
- Use `mysqldump` with `--single-transaction` as alternative
- Direct table copy may be faster for some use cases

### Best Practice: Avoid MySQL as Source

**When possible:**
- Use MSSQL or PostgreSQL as source (3x faster)
- If MySQL is only option, apply all optimizations
- Consider data size:
  - Small (<10M rows): Overhead acceptable
  - Medium (10-100M rows): Tuning required
  - Large (>100M rows): Consider alternatives

## Configuration Template

### MySQL 8.0+ Optimized for Source Reads

```cnf
[mysqld]
# Read performance
read_buffer_size = 8M
read_rnd_buffer_size = 16M
sort_buffer_size = 8M
join_buffer_size = 8M

# Table cache
table_open_cache = 4000
table_definition_cache = 2000

# Connection handling
thread_cache_size = 100
max_connections = 200

# Temporary tables
tmp_table_size = 256M
max_heap_table_size = 256M

# InnoDB
innodb_buffer_pool_size = 2G
innodb_stats_on_metadata = 0
innodb_read_io_threads = 8

# Query cache (MySQL 5.7 only)
# query_cache_size = 256M
# query_cache_type = 1
```

## Migration Tool Configuration

### Recommended Settings for MySQL Source

```yaml
migration:
  workers: 6                    # Higher parallelism to offset slow reads
  chunk_size: 100000           # Larger chunks to amortize overhead
  parallel_readers: 6          # More readers to feed workers
  read_ahead_buffers: 12       # Deeper pipeline
  max_partitions: 6            # More partitions for large tables
  max_source_connections: 24   # Support high parallelism
```

## Validation

### Before Tuning
```bash
# Run migration, observe throughput
./dmt run --config config.yaml

# Expected: ~90K rows/sec
```

### After Tuning
```bash
# Apply MySQL optimizations
mysql -u root -p < mysql_source_tuning.sql

# Run same migration
./dmt run --config config_tuned.yaml

# Expected: ~200-250K rows/sec (2-3x improvement)
```

### Monitoring

```sql
-- Check buffer usage
SHOW STATUS LIKE 'Innodb_buffer_pool_read%';
SHOW STATUS LIKE 'Created_tmp_disk_tables';
SHOW STATUS LIKE 'Created_tmp_tables';

-- High disk temp tables = increase tmp_table_size
-- Low buffer pool hit ratio = increase innodb_buffer_pool_size
```

## Limitations

### Cannot Match MSSQL Performance

Even with all optimizations, MySQL as source will be slower than MSSQL:
- **MSSQL**: 308K rows/sec (baseline)
- **MySQL tuned**: ~200-250K rows/sec
- **Gap**: 20-35% slower

**Reasons:**
1. InnoDB architecture optimized for writes
2. No page compression like MSSQL
3. Less efficient range scan optimization
4. Connection overhead remains higher

### Partition Calculation Overhead

Large tables (50M+ rows) have significant partition calculation time:
- **Votes (52M rows)**: 2-5 minutes to calculate 4 partitions
- **Impact**: Delays start of data transfer
- **Workaround**: Use fewer partitions or disable partitioning for very large tables

## Conclusion

MySQL as a source database can be significantly improved with proper tuning:
- **Untuned**: 90K rows/sec (poor)
- **Tuned**: 200-250K rows/sec (acceptable)
- **Still slower than**: MSSQL (308K), PostgreSQL (255K)
- **Best use case**: MySQL as **target** database (308K rows/sec with optimization)

**Bottom line**: MySQL driver is production-ready for migrations **to** MySQL, adequate for migrations **from** MySQL with tuning applied.

## Files

- `MYSQL_SOURCE_TUNING.md` - This guide
- `MYSQL_OPTIMIZATION_GUIDE.md` - Target optimization guide
- `MYSQL_PERFORMANCE_RESULTS.md` - Benchmark results
- `MYSQL_SOURCE_COMPARISON.md` - Source database comparison

## Related Documentation

- MySQL as target: See `MYSQL_OPTIMIZATION_GUIDE.md` (1.92x improvement achieved)
- Source comparison: See `MYSQL_SOURCE_COMPARISON.md` (MSSQL vs PostgreSQL vs MySQL)
- AI adjustment: Works with all source databases including tuned MySQL
