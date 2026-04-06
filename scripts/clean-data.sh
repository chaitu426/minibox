#!/usr/bin/env bash
# Remove mini-docker local state under ./data (or MINI_DOCKER_DATA_ROOT).
# Must be run with sudo if files are root-owned:  sudo ./scripts/clean-data.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="${MINI_DOCKER_DATA_ROOT:-$REPO_ROOT/data}"

# SECURITY: Guardrail against deleting system critical directories
FORBIDDEN_PATHS=("/" "/home" "/root" "/usr" "/etc" "/var" "/boot" "/bin" "/sbin" "/dev" "/proc" "/sys" "/run")

# Resolve absolute path (readlink -f does not require path to exist to return absolute path in standard bash, but fallback to absolute dir if target doesn't exist)
abs_target=$(readlink -m "$TARGET" 2>/dev/null || echo "$TARGET")

for p in "${FORBIDDEN_PATHS[@]}"; do
  if [[ "$abs_target" == "$p" || "$abs_target" == "$p/" ]]; then
    echo "ERROR: Refusing to delete system critical path: $abs_target" >&2
    exit 1
  fi
done

# Additional safeguard: don't allow target to be a top-level directory (e.g. /home, /opt)
# It should be at least two levels deep or explicitly within the repo root.
slashes=$(echo "$abs_target" | tr -cd '/' | wc -c)
if [[ "$slashes" -lt 2 && "$abs_target" != "$REPO_ROOT/data" && "$abs_target" != "$REPO_ROOT"/* ]]; then
    echo "ERROR: Target path $abs_target seems too broad. Refusing to delete." >&2
    exit 1
fi

echo "Target: $abs_target"
if [[ ! -e "$abs_target" ]]; then
  mkdir -p "$abs_target"
  echo "Created empty $abs_target"
  exit 0
fi

rm -rf "$abs_target"
mkdir -p "$abs_target"

# Restore ownership to the invoking user when run via sudo
if [[ -n "${SUDO_USER:-}" ]]; then
  chown -R "$SUDO_USER:$SUDO_USER" "$abs_target"
elif [[ -n "${USER:-}" ]]; then
  chown -R "$USER:$USER" "$abs_target" 2>/dev/null || true
fi

echo "Done. $abs_target is now empty."
