# Minibox Playground

A standalone web application for executing code in isolated containers using the Minibox runtime.

## Features
- **Multi-language Support**: Python 3.9, Node.js 16, and Shell.
- **Modern UI**: Dark-themed, split-screen layout with an interactive output terminal.
- **Isolated Execution**: Every code snippet runs in a temporary Minibox container.
- **Streaming Output**: See code results in real-time.

## Prerequisites
- **Minibox Daemon**: Ensure `miniboxd` is running on `127.0.0.1:8080`.
- **Images**: The following images should be pulled/cached in Minibox:
  - `python:3.9-alpine`
  - `node:16-alpine`
  - `alpine:latest`

## Setup & Running

1. **Import Required Images**:
   Since Minibox does not have a `pull` command, you need to import the base images from Docker once. I've provided a helper script for this:
   ```bash
   cd tests/code-executor
   chmod +x setup_images.sh
   ./setup_images.sh
   ```

2. **Install Dependencies**:
   ```bash
   npm install
   ```

3. **Start the Server**:
   ```bash
   node server.js
   ```

3. **Access the Playground**:
   Open [http://localhost:3000](http://localhost:3000) in your browser.

## How it works
The backend (`server.js`) receives code from the frontend and sends a `POST` request to the Minibox API `/containers/run` with the code embedded in the command. The output from the container is then piped back to the browser in real-time.
