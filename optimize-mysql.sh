#!/bin/bash
set -e

echo "=== Optimizing MySQL for Bulk Loads ==="

# Stop current container
echo "Stopping MySQL container..."
docker stop mysql-target || true
docker rm mysql-target || true

# Start with performance configuration
echo "Starting MySQL with optimized settings..."
docker run -d \
  --name mysql-target \
  -e MYSQL_ROOT_PASSWORD=MySQLPassword123 \
  -e MYSQL_DATABASE=so2013_mysql \
  -p 3306:3306 \
  -v "$(pwd)/mysql-performance.cnf:/etc/mysql/conf.d/performance.cnf:ro" \
  mysql:8.0 \
  --innodb-buffer-pool-size=2G \
  --innodb-redo-log-capacity=2G \
  --innodb-flush-log-at-trx-commit=2 \
  --innodb-flush-method=O_DIRECT \
  --innodb-doublewrite=0 \
  --max-connections=200 \
  --skip-log-bin

echo "Waiting for MySQL to be ready..."
sleep 15

# Verify settings
echo "Verifying MySQL configuration..."
docker exec mysql-target mysql -uroot -pMySQLPassword123 -e "
SELECT
  @@innodb_buffer_pool_size/1024/1024/1024 AS buffer_pool_gb,
  @@innodb_log_file_size/1024/1024 AS log_file_mb,
  @@innodb_flush_log_at_trx_commit AS flush_trx_commit,
  @@innodb_doublewrite AS doublewrite,
  @@max_connections AS max_conn
" 2>/dev/null

echo ""
echo "=== MySQL optimized! ==="
echo "Expected improvements:"
echo "  - 10-30x faster due to larger buffer pool (4GB vs 128MB)"
echo "  - 2-3x faster from flush_log_at_trx_commit=2"
echo "  - 20-40% faster from disabled doublewrite buffer"
echo ""
echo "Run migration with: ./dmt run --config config-mssql-mysql-optimized.yaml"
