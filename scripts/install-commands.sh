#!/usr/bin/env bash
set -euo pipefail

# Installs:
#   minibox   -> CLI
#   miniboxd  -> daemon
#
# Usage:
#   bash scripts/install-commands.sh --user
#   bash scripts/install-commands.sh --system

MODE="${1:-}"
if [[ "$MODE" != "--user" && "$MODE" != "--system" ]]; then
  echo "Usage: $0 --user|--system"
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "[1/3] Building binaries"
mkdir -p bin
go build -o bin/minibox-cli ./cmd/cli
go build -o bin/minibox-daemon ./cmd/daemon

if [[ "$MODE" == "--user" ]]; then
  TARGET_DIR="${HOME}/.local/bin"
  mkdir -p "$TARGET_DIR"
  echo "[2/3] Installing to $TARGET_DIR (no sudo)"
  install -m 0755 "bin/minibox-cli" "${TARGET_DIR}/minibox"
  install -m 0755 "bin/minibox-daemon" "${TARGET_DIR}/miniboxd"
  echo "[3/3] Done"
  echo
  echo "Add to PATH if needed:"
  echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
  echo
  echo "Run commands:"
  echo "  sudo -E miniboxd"
  echo "  minibox ping"
else
  TARGET_DIR="/usr/local/bin"
  echo "[2/3] Installing to $TARGET_DIR (sudo)"
  sudo install -m 0755 "bin/minibox-cli" "${TARGET_DIR}/minibox"
  sudo install -m 0755 "bin/minibox-daemon" "${TARGET_DIR}/miniboxd"
  echo "[3/3] Done"
  echo
  echo "Run commands:"
  echo "  sudo -E miniboxd"
  echo "  minibox ping"
fi
