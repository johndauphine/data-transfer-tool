# Running Test Migrations with AI Real-Time Adjustment

## Prerequisites

Both databases must be running and accessible:
```bash
./dmt status -c config-ai-test-no-ai.yaml
```

Should show:
```
Connected to MSSQL source: localhost:1433/StackOverflow2010
Connected to PostgreSQL target: localhost:5433/stackoverflow
```

## Test 1: Baseline Migration (Without AI)

Run a baseline migration to verify infrastructure works:

```bash
./dmt run -c config-ai-test-no-ai.yaml --verbosity info 2>&1 | tee baseline-test.log
```

**What to observe:**
- Migration completes successfully
- Final throughput (rows/sec)
- Total time elapsed
- Number of tables migrated

**Expected output pattern:**
```
[INFO] Starting migration run
[INFO] Created X jobs, calculating total rows
[INFO] Starting worker pool with 2 workers
[INFO] Table: Users - Transferred 1,000,000 rows in 45s (22,222 rows/sec)
[INFO] Table: Posts - Transferred 5,000,000 rows in 120s (41,666 rows/sec)
[INFO] Migration completed successfully
```

## Test 2: With AI Adjustment (If API Key Available)

### Step 1: Set AI API Key

```bash
export DMT_AI_PROVIDER=claude
export DMT_AI_API_KEY=sk-ant-...  # Your Anthropic API key
```

### Step 2: Run Migration with AI Enabled

```bash
./dmt run -c config-ai-test.yaml --verbosity debug 2>&1 | tee ai-test.log
```

### Step 3: Monitor AI Decisions

Watch for these log patterns:

**Initial Setup (first 30 seconds):**
```
[INFO] AI-driven parameter adjustment enabled (interval: 30s)
[INFO] AI monitoring started (evaluation interval: 30s)
```

**Metrics Collection (every 30 seconds):**
```
[DEBUG] Metrics snapshot: 45000 rows/sec, memory=256MB (42%), CPU=58%, throughput_trend=-2.5%
[DEBUG] Metrics snapshot: 48000 rows/sec, memory=268MB (44%), CPU=62%, throughput_trend=6.7%
[DEBUG] Metrics snapshot: 52000 rows/sec, memory=280MB (46%), CPU=65%, throughput_trend=8.3%
```

**AI Analysis & Decisions:**
```
[INFO] AI adjustment applied: scale_up - Throughput increasing and resources available - scaling up workers (confidence: high)
[DEBUG] Scaled writers from 1 to 2

[INFO] Using cached AI decision (age 28.5s)

[INFO] AI adjustment applied: continue - Performance stable, no changes needed (confidence: high)
```

## Test 3: Compare Performance

### Setup

Run both tests and capture metrics:

```bash
# Test 1: Baseline (2 workers, static config)
time ./dmt run -c config-ai-test-no-ai.yaml > baseline.txt 2>&1

# Test 2: With AI (2 initial workers, AI-adjusted)
export DMT_AI_API_KEY=sk-ant-...
time ./dmt run -c config-ai-test.yaml --verbosity info > ai-test.txt 2>&1
```

### Analysis

Extract key metrics:

```bash
# Baseline throughput
grep "Transferred" baseline.txt | awk '{print $NF}'

# AI test throughput
grep "Transferred" ai-test.txt | awk '{print $NF}'

# Time comparison
grep "real" baseline.txt
grep "real" ai-test.txt
```

## Test 4: Stress Test (More Tables)

To see more significant AI adjustments, include more tables:

**Edit config-ai-test.yaml:**
```yaml
migration:
  # Remove include_tables to test all tables
  # include_tables:
  #   - Users
  #   - Posts
  #   - Comments
```

Or add more tables:
```yaml
migration:
  include_tables:
    - Users
    - Posts
    - Comments
    - PostHistory
    - Votes
    - BadgesAwards
```

**Run with AI:**
```bash
export DMT_AI_API_KEY=sk-ant-...
./dmt run -c config-ai-test.yaml --verbosity debug 2>&1 | tee stress-test.log

# Extract AI decisions
grep "AI adjustment applied" stress-test.log
```

## Test 5: Monitor Memory & Resource Usage

In another terminal, monitor system resources during migration:

```bash
# Linux
watch -n 2 'ps aux | grep dmt'
top -p $(pgrep dmt)

# Or use gopsutil (what DMT uses for metrics)
./dmt run ... &
sleep 5
ps aux | grep "[d]mt"
```

## Interpreting Results

### Expected AI Adjustments

1. **Initial Phase (first 1-2 minutes)**
   - Collecting baseline metrics
   - May recommend conservative scale-up

2. **Optimization Phase (2-5 minutes)**
   - More aggressive adjustments as AI learns
   - Scale workers: 1→2→3 (or 2→3→4)
   - May reduce chunk size if memory pressure

3. **Stable Phase (after 5 minutes)**
   - Mostly "continue" decisions
   - Occasional fine-tuning
   - Cached decisions to save API calls

### Performance Indicators

**Good signs:**
- Throughput increasing with adjustments
- Memory stable (not continuously growing)
- CPU 60-80% utilization (healthy workload)
- Fewer adjustments over time

**Potential issues:**
- Memory continuously increasing → chunk_size too large
- Throughput declining despite scale-up → source bottleneck
- CPU 95%+ → too many workers
- API call failures → circuit breaker enabled

## Troubleshooting

### Databases unreachable
```
Error: failed to connect to MSSQL
```
**Fix**: Verify SQL Server and PostgreSQL are running
```bash
docker ps  # If using containers
netstat -an | grep 1433  # Check MSSQL port
netstat -an | grep 5433  # Check PostgreSQL port
```

### API key not set
```
Error: ai.api_key is required when AI features are enabled
```
**Fix**: Set environment variable
```bash
export DMT_AI_API_KEY=sk-ant-...
```

### Circuit breaker triggered
```
[WARN] AI adjustment circuit breaker OPEN after 3 failures
```
**Note**: Automatic recovery after 5 minutes. Check:
- API rate limits
- Network connectivity
- API key validity

### Memory usage too high
```
[WARN] Memory saturated: true
```
**Fix**: Reduce chunk_size in config
```yaml
migration:
  chunk_size: 5000  # Reduce from 10000
```

## Expected Costs

- **Baseline run**: $0.00 (no API calls)
- **AI run (30 min)**: ~$0.003 (60 evaluations, mostly cached)
- **AI run (2 hours)**: ~$0.01 (360 evaluations)
- **AI run (8 hours)**: ~$0.05 (1440 evaluations)

## Next Steps

1. **Run Test 1** to verify infrastructure
2. **Get Anthropic API key** from https://console.anthropic.com
3. **Run Test 2** to see AI adjustments in action
4. **Compare results** from Test 3
5. **Run stress test** (Test 4) with more tables
6. **Integrate feedback** for production migrations

## Additional Resources

- `AI_ADJUSTMENT_FEATURE.md` - Architecture and design
- `TEST_AI_ADJUSTMENT.md` - Detailed testing guide
- `config-ai-test.yaml` - Configuration with AI enabled
- `config-ai-test-no-ai.yaml` - Baseline configuration

## Questions?

Check logs with:
```bash
./dmt run -c config.yaml --verbosity debug 2>&1 | grep -E "AI|adjustment|Metrics"
```

Review feature docs:
```bash
cat AI_ADJUSTMENT_FEATURE.md  # Architecture
cat TEST_AI_ADJUSTMENT.md      # Detailed guide
```
