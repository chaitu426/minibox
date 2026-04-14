#!/bin/bash

# This script pulls images from Docker and imports them into Minibox
# to be used by the Code Executor Playground.

IMAGES=("python:3.9-alpine" "node:16-alpine" "alpine:latest")

# Check if docker is present
if ! command -v docker &> /dev/null; then
    echo "Error: Docker is not installed or not in PATH."
    exit 1
fi

# Determine if sudo is needed for docker
DOCKER_CMD="docker"
if ! docker ps &> /dev/null; then
    echo "Warning: Permission denied for 'docker'. Switching to 'sudo docker'..."
    DOCKER_CMD="sudo docker"
fi

# Get the path to minibox-cli
MINIBOX_CLI="../../bin/minibox-cli"
if [ ! -f "$MINIBOX_CLI" ]; then
    echo "Warning: $MINIBOX_CLI not found. Attempting to use global 'minibox' command."
    MINIBOX_CLI="minibox"
fi

# Determine if sudo is needed for minibox
MINIBOX_CMD="$MINIBOX_CLI"
if ! $MINIBOX_CLI images &> /dev/null; then
    echo "Warning: Permission denied for minibox. Switching to 'sudo $MINIBOX_CLI'..."
    MINIBOX_CMD="sudo $MINIBOX_CLI"
fi

for image in "${IMAGES[@]}"; do
    echo "----------------------------------------------------"
    echo "Processing image: $image"
    
    # Create a temporary build context
    BUILD_DIR=$(mktemp -d)
    echo "BASE $image" > "$BUILD_DIR/MiniBox"
    
    echo "Pulling/Building $image via Minibox build..."
    if ! $MINIBOX_CMD build -t "$image" "$BUILD_DIR"; then
        echo "Error: Failed to fetch image $image"
        rm -rf "$BUILD_DIR"
        continue
    fi
    
    # Cleanup
    rm -rf "$BUILD_DIR"
    echo "Successfully imported $image!"
done

echo "----------------------------------------------------"
echo "All images are now available in Minibox!"
echo "You can refresh the Playground and start executing code."
