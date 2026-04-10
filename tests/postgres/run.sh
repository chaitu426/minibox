#!/bin/bash
# Run a Postgres 15 container with minibox.
#
# Usage:
#   sudo -E bash run_postgres.sh
#
# After it starts, connect with:
#   psql -h 127.0.0.1 -p 5432 -U postgres
#
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[postgres] Building image..."
minibox build -t minibox-postgres "$SCRIPT_DIR"

echo "[postgres] Starting container (detached)..."
minibox db run \
  --name    pg-data \
  --data    /var/lib/postgresql/data \
  -e PGDATA=/var/lib/postgresql/data \
  --shm-size 256 \
  -p        5432:5432 \
  -e        POSTGRES_PASSWORD=minibox \
  -e        POSTGRES_USER=postgres \
  -e        POSTGRES_DB=app \
  --cmd    "docker-entrypoint.sh postgres" \
  minibox-postgres

echo ""
echo "✅ Postgres started."
echo "   Connect: psql -h 127.0.0.1 -p 5432 -U postgres -d app"
echo "   Password: minibox"
echo "   Logs:     minibox logs \$(minibox ps --json | python3 -c \"import sys,json;cs=json.load(sys.stdin);[print(c['id']) for c in cs if c['image']=='minibox-postgres' and c['status']=='running']\" | head -1)"
