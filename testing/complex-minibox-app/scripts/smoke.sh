#!/bin/sh
set -eu

base="${1:-http://127.0.0.1:3000}"

echo "[smoke] health"
wget -qO- "${base}/health" | grep -q '"ok":true'

echo "[smoke] write cache"
wget -qO- \
  --header="Content-Type: application/json" \
  --post-data='{"value":"mini"}' \
  "${base}/api/cache/sample" | grep -q '"ok":true'

echo "[smoke] read cache"
wget -qO- "${base}/api/cache/sample" | grep -q '"value":"mini"'

echo "[smoke] done"
