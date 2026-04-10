#!/bin/bash
# Run a Redis 7 (alpine) container with minibox.
#
# Usage:
#   sudo -E bash run_redis.sh
#
# After it starts, connect with:
#   redis-cli -h 127.0.0.1 -p 6379
#
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[redis] Building image..."
minibox build -t minibox-redis1 "$SCRIPT_DIR"

echo "[redis] Starting container (detached)..."
minibox db run \
  --name    redis-data \
  --data    /data \
  --shm-size 64 \
  -p        6379:6379 \
  --cmd redis-server \
  minibox-redis1

echo ""
echo "✅ Redis started."
echo "   Connect: redis-cli -h 127.0.0.1 -p 6379 ping"
