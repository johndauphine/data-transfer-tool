# AI Context File - mssql-pg-migrate

This file provides context for AI assistants working on this project. Read this before making changes.

## Project Overview

**mssql-pg-migrate** is a high-performance CLI tool for bidirectional database migration between Microsoft SQL Server and PostgreSQL. Written in Go, it achieves 575K+ rows/sec for MSSQL→PG and 419K+ rows/sec for PG→MSSQL.

**Repository**: https://github.com/johndauphine/mssql-pg-migrate

## Architecture

```
cmd/migrate/main.go          # CLI entry point (urfave/cli)
internal/
├── config/                  # Configuration loading and validation
│   ├── config.go           # Config structs, YAML parsing, DSN building
│   ├── permissions_unix.go # File permission check (Linux/macOS)
│   └── permissions_windows.go # File permission check (Windows)
├── orchestrator/           # Migration coordinator
│   └── orchestrator.go     # Main workflow: extract → create → transfer → validate
├── checkpoint/             # State persistence (SQLite)
│   └── state.go           # Run/task tracking, progress saving, resume support
├── source/                 # Source database abstraction
│   ├── pool.go            # MSSQL source pool
│   ├── postgres_pool.go   # PostgreSQL source pool
│   └── types.go           # Table, Column, Index, FK structs
├── target/                 # Target database abstraction
│   ├── pool.go            # PostgreSQL target pool (COPY protocol)
│   └── mssql_pool.go      # MSSQL target pool (TDS bulk copy)
├── transfer/              # Data transfer engine
│   └── transfer.go        # Chunked transfer with read-ahead/write-ahead pipelining
├── pool/                  # Connection pool factory
│   └── factory.go         # Creates source/target pools based on config
├── progress/              # Progress bar display
│   └── tracker.go
└── notify/                # Slack notifications
    └── notifier.go
examples/                   # Example configuration files
```

## Key Concepts

### Transfer Pipeline
1. **Read-ahead**: Async goroutines pre-fetch chunks into buffered channel
2. **Write-ahead**: Multiple parallel writers consume from channel
3. **Chunk-level checkpointing**: Progress saved every 10 chunks for resume

### Pagination Strategies
- **Keyset pagination**: For single-column integer PKs (fastest)
- **ROW_NUMBER pagination**: For composite/varchar PKs
- Tables without PKs are rejected

### Authentication
- **Password**: Traditional user/password (default)
- **Kerberos**: Enterprise SSO via krb5 (MSSQL) or GSSAPI (PostgreSQL)

## Current State (v1.10.0 - December 2025)

### Recent Security Fixes
- Credentials are now sanitized before storing in SQLite state database
- Automatic migration cleans up any previously stored passwords
- Config file permission warnings added (chmod 600 recommended)

### Recent Features
- Kerberos authentication for both MSSQL and PostgreSQL
- SSL/TLS configuration options (ssl_mode, encrypt, trust_server_cert)
- `history --run <id>` command to view past run configurations
- Comprehensive documentation with example configs

### Performance Optimizations
- Auto-tuning based on CPU cores and available RAM
- Parallel readers (parallel_readers config)
- Parallel writers (write_ahead_writers config, use 8 for PG→MSSQL)
- RowsPerBatch hint for MSSQL bulk copy

## Configuration

Config files use YAML with environment variable support (`${VAR_NAME}`).

Key sections:
- `source`: Database to migrate FROM
- `target`: Database to migrate TO
- `migration`: Transfer settings (workers, chunk_size, etc.)
- `slack`: Optional notifications

See `examples/` directory for complete configurations.

## Building

```bash
# Linux
CGO_ENABLED=0 go build -o mssql-pg-migrate ./cmd/migrate

# Windows
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o mssql-pg-migrate.exe ./cmd/migrate

# macOS
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o mssql-pg-migrate-darwin ./cmd/migrate
```

## Testing Locally

Docker databases for local testing:
```bash
# SQL Server
docker run -e 'ACCEPT_EULA=Y' -e 'SA_PASSWORD=YourStrong@Passw0rd' -p 1433:1433 -d mcr.microsoft.com/mssql/server:2022-latest

# PostgreSQL
docker run -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:15
```

Test config at `/tmp/test-ssl-config.yaml` uses local Docker instances.

## Code Patterns

### Error Handling
- Wrap errors with context: `fmt.Errorf("doing X: %w", err)`
- Log warnings but continue for non-fatal issues

### Database Abstraction
- `pool.SourcePool` interface for source databases
- `pool.TargetPool` interface for target databases
- Factory functions in `pool/factory.go`

### Config Defaults
- Applied in `config.applyDefaults()`
- Auto-sizing based on system resources (CPU, RAM)

## Known Issues / TODOs

1. **Kerberos not tested in production** - Implementation complete but needs real Kerberos environment testing
2. **Windows ACL check is heuristic** - Uses icacls output parsing, may have edge cases
3. **No support for same-to-same migrations** - By design (use native tools instead)

## Commit Style

```
Short summary of change

Optional longer description.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
```

## Release Process

1. Update version in code if needed
2. Update README download links
3. Create annotated tag: `git tag -a v1.x.x -m "description"`
4. Push tag: `git push origin v1.x.x`
5. CI automatically builds and creates GitHub release
6. Update release notes via: `gh release edit v1.x.x --notes "..."`

## Session History (December 20, 2025)

### What Was Done Today

1. **Performance Optimization**
   - Added read-ahead pipelining for async I/O
   - Added write-ahead pipelining with parallel writers
   - Auto-tuning based on CPU/RAM
   - Optimized PG→MSSQL to 419K rows/sec (was ~200K)

2. **Security Improvements**
   - Fixed credential storage in SQLite (now sanitized)
   - Added config file permission checks
   - Added Kerberos authentication support

3. **SSL/TLS Support**
   - Added ssl_mode, encrypt, trust_server_cert options
   - Secure defaults for production

4. **Documentation**
   - Comprehensive parameter reference tables
   - 6 example configuration files
   - Kerberos setup guide

5. **CLI Enhancements**
   - Added `history --run <id>` to view run configs

### Files Modified Today
- `internal/config/config.go` - Kerberos, SSL, Sanitized()
- `internal/config/permissions_*.go` - NEW: permission checks
- `internal/checkpoint/state.go` - Config storage, sanitization migration
- `internal/orchestrator/orchestrator.go` - ShowRunDetails
- `cmd/migrate/main.go` - history --run flag
- `README.md` - Comprehensive rewrite
- `examples/*.yaml` - NEW: example configs

## Contact

Project maintainer: John Dauphine (jdauphine@gmail.com)
