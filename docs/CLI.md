# mini-docker — CLI and daemon reference

This document describes the **CLI** (`mini-docker`) and **daemon** (`mini-dockerd`), environment variables, and example commands.

For a full implementation deep-dive (parser → DAG build → OCI storage → runtime → API/CLI), see `docs/ARCHITECTURE.md`.

The CLI talks to the daemon over **HTTP** (default `http://127.0.0.1:8080`). Start the daemon before using CLI commands (except you can run `ping` to check connectivity).

## Recommended packaged commands (no `./bin/...`)

Install command aliases:

```bash
make install-user
```

This installs:

- `minibox` (CLI)
- `miniboxd` (daemon)

Then run:

```bash
sudo -E miniboxd
minibox ping
```

---

## Build binaries

From the repository root:

```bash
go build -o bin/mini-docker ./cmd/cli
go build -o bin/mini-dockerd ./cmd/daemon
```

Add `bin/` to your `PATH`, or invoke with `./bin/mini-docker` and `./bin/mini-dockerd`.

---

## Daemon (`mini-dockerd`)

Runs the HTTP API and container runtime backend.

### Start

```bash
./bin/mini-dockerd
```

Default listen address: **`127.0.0.1:8080`** (localhost only).

### Daemon environment variables

| Variable | Description |
|----------|-------------|
| `MINI_DOCKER_DATA_ROOT` | Data directory for images, containers, blobs (default: `/var/lib/mini-docker`). |
| `MINI_DOCKER_HTTP_ADDR` | Listen address (default: `127.0.0.1:8080`). Use `:8080` only if you intend to expose the API on all interfaces. |
| `MINI_DOCKER_API_TOKEN` | If set, all API requests must send `Authorization: Bearer <token>` or header `X-API-Token`. |
| `MINI_DOCKER_BUILD_PREFIXES` | Comma-separated list of allowed **build context** directory roots. |
| `MINI_DOCKER_SUBUID_BASE` | First host UID/GID for user-namespace mapping (default `100000`). |
| `MINI_DOCKER_SUBUID_COUNT` | Size of ID map (default `65536`). |

---

## CLI (`mini-docker`)

### CLI environment variables

| Variable | Description |
|----------|-------------|
| `MINI_DOCKER_API` | Base URL of the daemon (default: `http://127.0.0.1:8080`). |
| `MINI_DOCKER_API_TOKEN` | Bearer token if the daemon was started with `MINI_DOCKER_API_TOKEN`. |

---

## Command overview

| Command | Description |
|---------|-------------|
| `ping` | Check if the daemon is reachable |
| `build` | Build an image from a MiniBox |
| `run` | Run a container from an image |
| `ps` | List containers |
| `stats` | Live container resource stats |
| `images` | List images |
| `save` | Export an image to a tar archive |
| `load` | Import an image from a tar archive |
| `logs` | Show container logs |
| `exec` | Run a command in a running container |
| `stop` | Stop a running container |
| `kill` | Force-kill a running container |
| `rm` | Remove a container |
| `rmi` | Remove an image |
| `system prune` | Garbage-collect unused blobs and lazy mounts |

---

## Commands and examples

Replace `./bin/mini-docker` with `mini-docker` if it is on your `PATH`.

### `ping`

Check connectivity to the daemon.

```bash
./bin/mini-docker ping
```

Example output: `Daemon is running`

---

### `build`

Build an image from a directory containing **`MiniBox`**.

**Usage:** `mini-docker build -t <image-name> [context-directory]`

Default context is the current directory (`.`).

```bash
# Build from current directory, tag image as "myapp"
./bin/mini-docker build -t myapp .

# Build from a specific path
./bin/mini-docker build -t webserver /home/user/my-project
```

**Note:** The daemon validates that the build context path is under allowed prefixes (`MINI_DOCKER_BUILD_PREFIXES`).

MiniBox healthcheck (basic support):

```text
HEALTHCHECK --interval=30 /bin/sh -c "wget -qO- http://127.0.0.1:3000/health || exit 1"
```

The container `HEALTH` column in `ps` updates as `starting`, `healthy`, or `unhealthy` while running.

---

### `run`

Run a command in a new container.

**Usage:**  
`mini-docker run [-d] [-m <memoryMB>] [-c <cpuMax>] [-p <host:container>] <image> [command...]`

| Flag | Description |
|------|-------------|
| `-d` | Detached mode (container ID printed, runs in background) |
| `-m` | Memory limit in megabytes |
| `-c` | CPU limit (cgroup v2 `cpu.max` format, e.g. `50000` for quota) |
| `-p` | Port map: host port → container port (e.g. `-p 8080:80`). Repeat `-p` for multiple mappings. |

```bash
# Interactive foreground run with default image command
./bin/mini-docker run alpine /bin/sh

# Detached
./bin/mini-docker run -d myapp

# Memory limit 512 MB, map host 9000 to container 80
./bin/mini-docker run -m 512 -p 9000:80 myapp

# Custom command
./bin/mini-docker run alpine ls -la /
```

If you omit `command`, the image’s default `CMD` from metadata is used (when available).

---

### `ps`

List containers.

```bash
# Running containers only
./bin/mini-docker ps

# Include stopped / exited
./bin/mini-docker ps -a
```

`ps` shows `STATUS`, `HEALTH`, `EXIT`, and `PORTS`.

---

### `stats`

Live stats for one container (refreshes every second; **Ctrl+C** to stop).

**Usage:** `mini-docker stats <containerID>`

```bash
./bin/mini-docker stats a1b2c3d4
```

---

### `images`

List local images (from the daemon’s index).

```bash
./bin/mini-docker images
```

---

### `save`

Export a local image into a tar archive.

**Usage:** `mini-docker save <image> <output.tar>`

```bash
./bin/mini-docker save demo ./demo-image.tar
```

---

### `load`

Import an image tar archive created by `save`.

**Usage:** `mini-docker load <input.tar>`

```bash
./bin/mini-docker load ./demo-image.tar
```

---

### `logs`

Fetch logs for a container.

**Usage:** `mini-docker logs <containerID>`

```bash
./bin/mini-docker logs a1b2c3d4
```

---

### `exec`

Run a command inside a running container by entering its namespaces.

**Usage:** `mini-docker exec [-it] <containerID> <cmd...>`

- `-it` attaches stdin/stdout/stderr to your terminal (requires `nsenter` from `util-linux`).

Examples:

```bash
# Non-interactive
./bin/mini-docker exec a1b2c3d4 ls -la /

# Interactive shell
./bin/mini-docker exec -it a1b2c3d4 /bin/sh
```

---

### `stop`

Stop a running container (SIGTERM to container process).

**Usage:** `mini-docker stop [-t seconds] <containerID>`

```bash
./bin/mini-docker stop a1b2c3d4
```

---

### `kill`

Force-kill a running container (SIGKILL).

**Usage:** `mini-docker kill <containerID>`

```bash
./bin/mini-docker kill a1b2c3d4
```

---

### `rm`

Remove a container record and its data directory on the daemon.

**Usage:** `mini-docker rm <containerID>`

```bash
./bin/mini-docker rm a1b2c3d4
```

---

### `rmi`

Remove an image from the local index (by repository name as registered at build time).

**Usage:** `mini-docker rmi <image>`

```bash
./bin/mini-docker rmi myapp
```

---

### `system prune`

Remove orphaned blobs, teardown stale lazy FUSE mounts, clean extracted layers, and tmp under the data root.

```bash
./bin/mini-docker system prune
```

---

## Typical session

Terminal 1 — start daemon:

```bash
./bin/mini-dockerd
```

Terminal 2 — use CLI:

```bash
export PATH="$PWD/bin:$PATH"   # optional

mini-docker ping
mini-docker build -t demo .
mini-docker run demo
mini-docker ps
mini-docker stop <container-id>
mini-docker rm <container-id>
```

With API token (daemon and CLI):

```bash
export MINI_DOCKER_API_TOKEN="$(openssl rand -hex 16)"
MINI_DOCKER_API_TOKEN="$MINI_DOCKER_API_TOKEN" ./bin/mini-dockerd &
export MINI_DOCKER_API_TOKEN="$MINI_DOCKER_API_TOKEN"
mini-docker ping
```

---

## Container ID format

Container IDs are **8 hexadecimal characters** (e.g. `a1b2c3d4`). Use the ID returned by `run` / `ps` for `logs`, `stats`, `stop`, and `rm`.

---

## Troubleshooting

- **Connection refused:** Start `mini-dockerd` or set `MINI_DOCKER_API` to the correct URL.
- **401 Unauthorized:** Set `MINI_DOCKER_API_TOKEN` on the client to match the daemon’s `MINI_DOCKER_API_TOKEN`.
- **Build context rejected:** Ensure the context directory is under a path allowed by `MINI_DOCKER_BUILD_PREFIXES` on the daemon.
- **Wipe local state (`./data`):** If the daemon created root-owned files, run from the repo root: `sudo ./scripts/clean-data.sh` (or set `MINI_DOCKER_DATA_ROOT` to point at another directory to clean). This deletes images, layers, containers, and blobs under that path and recreates an empty directory.
- **`could not unshare mount namespace: operation not permitted`:** The child skips a second `unshare` when the daemon sets `MINI_DOCKER_CHILD_NEWNS=1`. Rebuild and restart the daemon.
- **`Error remounting root private: operation not permitted` / `no such process` (network):** Rootless kernels often deny `MS_PRIVATE` on `/`. The runtime now bind-mounts `rootfs` first and marks that mount private, with fallbacks; the “no such process” message was the container exiting early—rebuild after this fix.

---

## API (reference)

The CLI is a thin client over HTTP. Default routes include:

- `GET /ping`
- `POST /containers/run`, `POST /containers/build`
- `GET /containers`, `GET /containers/logs`, `GET /containers/stats`
- `POST /containers/stop`, `POST /containers/remove`
- `GET /images`, `POST /images/remove`
- `POST /system/prune`

See `internal/api/router.go` for the authoritative list.
