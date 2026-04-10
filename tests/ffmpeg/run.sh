#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Building Minibox image for FFmpeg..."
minibox build -t minibox-ffmpeg "$SCRIPT_DIR"

echo "Running container..."
minibox run minibox-ffmpeg
