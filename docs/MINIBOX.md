# MiniBox File Specification

The `MiniBox` file (formerly `Boxfile`) defines the blueprint for building OCI-compliant container images with **minibox**. It supports a modern, DAG-based multi-stage build system designed for high concurrency and small final image sizes.

## File Format Overview

A `MiniBox` file consists of top-level directives and indented block instructions.

```minibox
BASE node:18-slim

BLOCK builder
    copy ./package.json /build/package.json
    auto-deps
    copy ./src /build/src

BLOCK runtime
    BNEED builder
    run mkdir -p /app
    copy FROM=builder /build/node_modules /app/node_modules
    copy FROM=builder /build/src /app/src
    workdir /app
    env NODE_ENV=production

START node index.js
```

---

## Top-Level Directives

### `BASE <image>`
**(Required)** Specifies the base image source for the build. MiniBox supports multiple resolver types:
- **OCI Registry**: Pulls from a Docker V2 compatible registry.
  - Defaults to Docker Hub: `BASE alpine:latest`, `BASE library/ubuntu:22.04`
  - Custom registries: `BASE quay.io/coreos/etcd`, `BASE ghcr.io/user/repo:tag`
- **Scratch**: `BASE scratch` starts with a completely empty root filesystem. Ideal for statically linked Go/Rust binaries.
- **Local Image**: Reference a previously built image. MiniBox will reuse its layers.
  - Example: `BASE my-custom-base:latest`
- **Local Archive**: Use a local `.tar` or `.tar.gz` file as a base.
  - Example: `BASE ./rootfs.tar.gz`, `BASE /tmp/base.tar`

### `BLOCK <name>`
Defines a logical stage in the build graph. Blocks can run in parallel if they don't depend on each other.
- Block names are used for dependency resolution (`NEED`, `BNEED`) and copying artifacts (`COPY FROM=`).

### `START <command>`
Defines the final execution command (entrypoint) for the container.
- Example: `START node index.js`, `START python main.py`.

### `HEALTHCHECK [--interval=N] <command>`
Defines a health check for the container.
- Default interval is 30 seconds.
- Example: `HEALTHCHECK curl -f http://localhost:3000/health`.

---

## Block Instructions (Indented)

Instructions inside a `BLOCK` must be indented by at least 4 spaces or a tab.

### `NEED <block_name>`
Declares a **runtime dependency**. 
- The target block's layers will be included in the final image's filesystem stack.
- Useful for base layers or shared libraries.

### `BNEED <block_name>`
Declares a **build-time dependency** (Build Need).
- The target block must be completed before this block starts.
- **Optimization**: The target block's layers are NOT included in the final image unless explicitly copied. This is the primary way to achieve "lean" images.

### `RUN <command>`
Executes a command inside the container chroot during the build.
- MiniBox automatically mounts `/proc`, `/dev`, and `/tmp` with correct permissions (1777) for tools like `apt` or `npm`.

### `COPY <src> <dest>` or `COPY FROM=<block> <src> <dest>`
Copies files/directories into the current block layer.
- **Local Copy**: `COPY ./src /app` (Copies from host context).
- **Stage Copy**: `COPY FROM=builder /build/bin /usr/local/bin` (Copies from another block's final layer).

### `AUTO-DEPS`
Automatically detects the project type and runs the standard dependency manager.
- **Node.js**: Runs `npm install` if `package.json` is found.
- **Python**: Runs `pip install -r requirements.txt` if found.
- **Go**: Runs `go mod download` if `go.mod` is found.
- **Rust**: Runs `cargo build` if `Cargo.toml` is found.

### `WORKDIR <path>`
Sets the working directory for subsequent `RUN` and `COPY` instructions and for the final container.

### `ENV <KEY>=<VALUE>` or `ENV <KEY> <VALUE>`
Sets environment variables in the build environment and the final image.

### `PKG <name>[@version]`
Shortcut for installing system packages.
- Currently specialized for Alpine (`apk add`). For Ubuntu/Debian, use `RUN apt-get install`.

### `PORT <number>`
Metadata declaring that the container listens on the specified port.

---

## Advanced: Size Optimization (Multi-Stage)

To keep images small, use the `BNEED` and `COPY FROM=` pattern:

1.  Use a `builder` block to compile code or install heavy dev dependencies.
2.  Use a `runtime` block as the final stage.
3.  `BNEED builder` in the runtime block so it waits for the build to finish.
4.  `COPY FROM=builder` only the necessary artifacts (binaries, `node_modules`).
5.  Minibox will automatically prune the `builder` layers from the final image export.

---

## Legacy Format Support
For backwards compatibility with older versions of Minibox, the following aliases are supported in a flat file structure (no `BLOCK` or `BASE`):
- `BOX` / `FROM` / `START-WITH` -> `BASE`
- `ADD-PACKAGE` -> `apk add`
- `INSTALL-DEPS` -> `npm install`
- `CLONE-REPO` -> `git clone`
- `GOTO-FOLDER` -> `WORKDIR`
- `SET-ENVIRONMENT` -> `ENV`
- `IMPORT-FILE` / `GRAB-ALL` -> `COPY`
- `LAUNCH` -> `START`



BASE alpine:latest

# 1. Base Setup
BLOCK setup
    pkg bash
    pkg util-linux
    pkg coreutils

# 2. Parallel IO Generator 1
BLOCK ioload1
    NEED setup
    run mkdir -p /scratch/io1
    run bash -c 'for i in {1..20000}; do touch /scratch/io1/file_$i; done'

# 3. Parallel IO Generator 2
BLOCK ioload2
    NEED setup
    run mkdir -p /scratch/io2
    run bash -c 'for i in {1..20000}; do echo "stress" > /scratch/io2/data_$i; done'

# 4. Massive Layer Deepening
BLOCK deep_hierarchy
    NEED setup
# Creates a 150-level deep directory string
    run bash -c 'p=""; for i in {1..150}; do p=$p/depth$i; done; mkdir -p /scratch/deep/$p'
    run bash -c 'p=""; for i in {1..150}; do p=$p/depth$i; done; touch /scratch/deep/$p/bottom_file'

# 5. Cyclic Symlinks and Hardlinks
BLOCK nasty_links
    NEED ioload1
    NEED ioload2
    run mkdir -p /scratch/links
    run ln -s /scratch/links/b /scratch/links/a
    run ln -s /scratch/links/c /scratch/links/b
    run ln -s /scratch/links/a /scratch/links/c
    run bash -c 'for i in {1..1000}; do ln /scratch/io1/file_1 /scratch/links/hardlink_$i || true; done'

# 6. Tarball bomb creation
BLOCK tar_bomb
    NEED deep_hierarchy
    run tar -czf /bomb.tar.gz /scratch/deep
    run mkdir /explosion
# Re-extract
    run tar -xzf /bomb.tar.gz -C /explosion

# 7. The Ultimate Killer: Cross-Layer Chown
# In Docker, a huge `chown -R` across directories built in prior layers
# triggers a massive copy-up in OverlayFS, completely destroying build performance and heavily taxing the storage driver.
BLOCK chown_apocalypse
    NEED ioload1
    NEED ioload2
    NEED deep_hierarchy
    NEED nasty_links
    NEED tar_bomb
    run adduser -D stressuser
    run chown -hR stressuser:stressuser /scratch

# 8. Many instructions (Testing parser and instruction array limits)
BLOCK instruction_spam
    NEED setup
    env A1=VAL1
    env A2=VAL2
    env A3=VAL3
    env A4=VAL4
    env A5=VAL5
    env A6=VAL6
    env A7=VAL7
    env A8=VAL8
    env A9=VAL9
    env A10=VAL10
    env LONG_VAR="thisisaverylongstringthatdoesntstopthisisaverylongstringthatdoesntstopthisisaverylongstringthatdoesntstopthisisaverylongstringthatdoesntstop"
    run echo $A1
    run echo $A2
    run echo $A3
    run echo $A4
    run echo $A5

# 9. Huge File Generation
BLOCK big_file
    NEED setup
    run dd if=/dev/urandom of=/huge.bin bs=1M count=150

START /bin/bash


BASE alpine

BLOCK runtime
    pkg nodejs
    pkg npm

BLOCK source 
    workdir /app
    copy . /app

BLOCK deps
    NEED runtime
    NEED source
    auto-deps

BLOCK config
    NEED deps
    env PORT=3000
    env NODE_ENV=production
    port 3000

START node index.js


BASE node:18-slim

# Stage 1: builder - installs dev dependencies and transpiles/builds src
BLOCK builder
    run mkdir -p /build
    workdir /build
    copy ./package.json /build/package.json
    auto-deps
    copy ./src /build/src
    copy ./index.js /build/index.js

# Stage 2: final runtime - only picks the pre-built artifacts, no dev deps
BLOCK runtime
    BNEED builder
    run mkdir -p /app
    copy FROM=builder /build/src /app/src
    copy FROM=builder /build/node_modules /app/node_modules
    copy FROM=builder /build/index.js /app/index.js
    copy ./package.json /app/package.json
    workdir /app
    env PORT=3000
    env NODE_ENV=production
    port 3001

START node index.js

---

### Advanced Example: Go Static Binary from Scratch

```minibox
BASE golang:1.21-alpine

BLOCK builder
    run apk add --no-cache git
    workdir /src
    copy . .
    # Build a statically linked binary
    run CGO_ENABLED=0 go build -o /app/server .

BLOCK runtime
    BASE scratch
    BNEED builder
    copy FROM=builder /app/server /server
    # Minimal environment setup
    env PORT=8080

START /server
```
