#!/bin/bash
# Run a MongoDB 7 container with minibox.
#
# Usage:
#   sudo -E bash run_mongo.sh
#
# After it starts, connect with:
#   mongosh mongodb://127.0.0.1:27017
#
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[mongo] Building image..."
minibox build -t minibox-mongo1 "$SCRIPT_DIR"

echo "[mongo] Starting container (detached)..."
minibox db run \
  --name    mongo \
  --data    /data/db \
  --shm-size 256 \
  -p        27017:27017 \
  -e        MONGO_INITDB_ROOT_USERNAME=root \
  -e        MONGO_INITDB_ROOT_PASSWORD=chaitu \
  --cmd "/usr/local/bin/docker-entrypoint.sh mongod --bind_ip_all" \
  minibox-mongo1

echo ""
echo "✅ MongoDB started."
echo "   Connect: mongosh mongodb://root:chaitu@127.0.0.1:27017"
