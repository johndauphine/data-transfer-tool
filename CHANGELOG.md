# Changelog

All notable changes to this project will be documented in this file.

## [1.21.0] - 2025-12-30

### Performance
- **Direct Bulk API for MSSQL Inserts** - 73% throughput improvement using `CreateBulkContext` directly (#20)
  - Bypasses `database/sql` prepared statement overhead by using `conn.Raw()` to access driver directly
  - Previous: ~15,500 rows/sec → Now: ~27,000 rows/sec
  - Explicit transaction wrapper for atomicity with proper `defer tx.Rollback()` pattern

## [1.20.0] - 2025-12-30

### Performance
- **Worker Cap at 12** - Capped max workers at 12 for optimal performance (#17)
- **Removed Row Size Adjustment** - Removed chunk_size reduction based on row size that was hurting throughput (#16)

## [1.19.0] - 2025-12-30

### Performance
- **Removed Memory Safety Loop** - Removed conservative memory reduction loop that was limiting throughput (#15)

## [1.18.0] - 2025-12-30

### Features
- **Auto-tune Parallel Readers and Writers** - Automatic tuning of `parallel_readers` and `write_ahead_writers` based on system resources (#14)

## [1.17.0] - 2025-12-30

### Features
- Auto-tuning improvements for migration settings

## [1.16.0] - 2025-12-24

### Features
- Performance improvements and bug fixes

## [1.15.0] - 2025-12-24

### Features
- Additional migration optimizations

## [1.14.0] - 2025-12-23

### Features
- Migration enhancements

## [1.13.0] - 2025-12-21

### Features
- Core migration functionality improvements

## [1.12.0] - 2025-12-21

### Features
- Initial stable release with bidirectional migration support
