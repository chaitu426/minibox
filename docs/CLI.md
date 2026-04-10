# minibox — CLI and daemon reference

This document describes the **CLI** (`minibox`) and **daemon** (`miniboxd`), environment variables, and example commands.

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
go build -o bin/minibox ./cmd/cli
go build -o bin/miniboxd ./cmd/daemon
```

Add `bin/` to your `PATH`, or invoke with `./bin/minibox` and `./bin/miniboxd`.

---

## Daemon (`miniboxd`)

Runs the HTTP API and container runtime backend.

### Start

```bash
./bin/miniboxd
```

Default listen address: **`127.0.0.1:8080`** (localhost only).

### Version

```bash
miniboxd --version
```

### Daemon environment variables

| Variable | Description |
|----------|-------------|
| `MINIBOX_DATA_ROOT` | Data directory for images, containers, blobs (default: `/var/lib/minibox`). |
| `MINIBOX_HTTP_ADDR` | Listen address (default: `127.0.0.1:8080`). Use `:8080` only if you intend to expose the API on all interfaces. |
| `MINIBOX_API_TOKEN` | If set, all API requests must send `Authorization: Bearer <token>` or header `X-API-Token`. |
| `MINIBOX_BUILD_PREFIXES` | Comma-separated list of allowed **build context** directory roots. |
| `MINIBOX_SUBUID_BASE` | First host UID/GID for user-namespace mapping (default `100000`). |
| `MINIBOX_SUBUID_COUNT` | Size of ID map (default `65536`). |

---

## CLI (`minibox`)

### CLI environment variables

| Variable | Description |
|----------|-------------|
| `MINIBOX_API` | Base URL of the daemon (default: `http://127.0.0.1:8080`). |
| `MINIBOX_API_TOKEN` | Bearer token if the daemon was started with `MINIBOX_API_TOKEN`. |

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
| `compose` | Multi-container orchestration (up/down/ps) |
| `system prune` | Garbage-collect unused blobs and lazy mounts |

---

## Commands and examples

Replace `./bin/minibox` with `minibox` if it is on your `PATH`.

### `ping`

Check connectivity to the daemon.

```bash
./bin/minibox ping
```

Example output: `Daemon is running`

---

### `build`

Build an image from a directory containing **`MiniBox`**.

**Usage:** `minibox build -t <image-name> [context-directory]`

Default context is the current directory (`.`).

```bash
# Build from current directory, tag image as "myapp"
./bin/minibox build -t myapp .

# Build from a specific path
./bin/minibox build -t webserver /home/user/my-project
```

**Note:** The daemon validates that the build context path is under allowed prefixes (`MINIBOX_BUILD_PREFIXES`).

MiniBox healthcheck (basic support):

```text
HEALTHCHECK --interval=30 /bin/sh -c "wget -qO- http://127.0.0.1:3000/health || exit 1"
```

The container `HEALTH` column in `ps` updates as `starting`, `healthy`, or `unhealthy` while running.

---

### `run`

Run a command in a new container.

**Usage:**  
`minibox run [-d] [-m <memoryMB>] [-c <cpuMax>] [-p <host:container>] <image> [command...]`

| Flag | Description |
|------|-------------|
| `-d` | Detached mode (container ID printed, runs in background) |
| `-m` | Memory limit in megabytes |
| `-c` | CPU limit (cgroup v2 `cpu.max` format, e.g. `50000` for quota) |
| `-p` | Port map: host port → container port (e.g. `-p 8080:80`). Repeat `-p` for multiple mappings. |

```bash
# Interactive foreground run with default image command
./bin/minibox run alpine /bin/sh

# Detached
./bin/minibox run -d myapp

# Memory limit 512 MB, map host 9000 to container 80
./bin/minibox run -m 512 -p 9000:80 myapp

# Custom command
./bin/minibox run alpine ls -la /
```

If you omit `command`, the image’s default `CMD` from metadata is used (when available).

---

### `db run`

Run a **database container** with production-friendly defaults: detached, named persistent volume, larger `/dev/shm`, high IO priority, OOM-protected.

**Usage:**
```
minibox db run [flags] <image> [command...]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--name <name>` | derived from image | Named volume ID. Stored in `DataRoot/volumes/<name>-data`. |
| `--data <path>` | `/var/lib/minibox-data` | Container path where the volume is mounted |
| `--shm-size <MB>` | `256` | `/dev/shm` size in MB (Postgres needs ≥128, Mongo benefits from ≥256) |
| `-p <host:container>` | — | Port map; repeat for multiple |
| `-e KEY=VAL` | — | Environment variable; repeat for multiple |
| `--cmd <string>` | — | Shell command string (`/bin/sh -c <cmd>`) when no positional args |
| `-d` | implicit | Always detached; accepted for symmetry |

**What `db run` does differently from `run`:**
- `db_mode=true` — skips capability drop so the DB entrypoint can `chown` data dirs on first boot
- `/dev/shm` mounted at `--shm-size` MB (Postgres `shared_buffers`, MongoDB wiredTiger)
- `/dev/pts` devpts mounted — required by `initdb` and DB shell tools
- `/tmp` mounted as `tmpfs` (mode 1777) — used broadly by all DB engines
- `/dev/ptmx`, `/dev/console`, fd symlinks created for full POSIX device coverage
- `io_weight=800` — high disk scheduling priority
- `oom_score_adj=-900` — last process to be OOM-killed

**Examples:**

```bash
# Postgres 15
minibox build -t minibox-postgres ./db-test/postgres
minibox db run \
  --name pg-data --data /var/lib/postgresql/data \
  --shm-size 256 -p 5432:5432 \
  -e POSTGRES_PASSWORD=secret -e POSTGRES_USER=app -e POSTGRES_DB=mydb \
  minibox-postgres
# Connect: psql -h 127.0.0.1 -p 5432 -U app -d mydb

# MongoDB 7
minibox build -t minibox-mongo ./db-test/mongo
minibox db run \
  --name mongo-data --data /data/db \
  -p 27017:27017 \
  -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=secret \
  minibox-mongo
# Connect: mongosh mongodb://root:secret@127.0.0.1:27017

# Redis 7
minibox build -t minibox-redis ./db-test/redis
minibox db run \
  --name redis-data --data /data \
  --shm-size 64 -p 6379:6379 \
  minibox-redis
# Connect: redis-cli -h 127.0.0.1 -p 6379 ping
```

Ready-to-run helper scripts: `db-test/postgres/run.sh`, `db-test/mongo/run.sh`, `db-test/redis/run.sh`.

---

### `ps`

List containers.

```bash
# Running containers only
./bin/minibox ps

# Include stopped / exited
./bin/minibox ps -a
```

`ps` shows `STATUS`, `HEALTH`, `EXIT`, and `PORTS`.

---

### `stats`

Live stats for one container (refreshes every second; **Ctrl+C** to stop).

**Usage:** `minibox stats <containerID>`

```bash
./bin/minibox stats a1b2c3d4
```

---

### `images`

List local images (from the daemon’s index).

```bash
./bin/minibox images
```

---

### `save`

Export a local image into a tar archive.

**Usage:** `minibox save <image> <output.tar>`

```bash
./bin/minibox save demo ./demo-image.tar
```

---

### `load`

Import an image tar archive created by `save`.

**Usage:** `minibox load <input.tar>`

```bash
./bin/minibox load ./demo-image.tar
```

---

### `logs`

Fetch logs for a container.

**Usage:** `minibox logs <containerID>`

```bash
./bin/minibox logs a1b2c3d4
```

---

### `exec`

Run a command inside a running container by entering its namespaces.

**Usage:** `minibox exec [-it] <containerID> <cmd...>`

- `-it` attaches stdin/stdout/stderr to your terminal (requires `nsenter` from `util-linux`).

Examples:

```bash
# Non-interactive
./bin/minibox exec a1b2c3d4 ls -la /

# Interactive shell
./bin/minibox exec -it a1b2c3d4 /bin/sh
```

---

### `stop`

Stop a running container (SIGTERM to container process).

**Usage:** `minibox stop [-t seconds] <containerID>`

```bash
./bin/minibox stop a1b2c3d4
```

---

### `kill`

Force-kill a running container (SIGKILL).

**Usage:** `minibox kill <containerID>`

```bash
./bin/minibox kill a1b2c3d4
```

---

### `rm`

Remove a container record and its data directory on the daemon.

**Usage:** `minibox rm <containerID>`

```bash
./bin/minibox rm a1b2c3d4
```

---

### `rmi`

Remove an image from the local index (by repository name as registered at build time).

**Usage:** `minibox rmi <image>`

```bash
./bin/minibox rmi myapp
```

---

### `system prune`

Remove orphaned blobs, teardown stale lazy FUSE mounts, clean extracted layers, and tmp under the data root.

```bash
./bin/minibox system prune
```

---

### `compose`

Manage multi-container projects using a `minibox-compose.yaml` file.

**Usage:** `minibox compose [up|down|ps] [-f <file>]`

| Subcommand | Description |
|------------|-------------|
| `up` | Builds images and starts all services in the project. |
| `ps` | Lists running services associated with the project. |
| `logs` | Streams multiplexed logs for all services in the project. |
| `build` | Builds service images without starting containers. |
| `start` | Starts services in the project. |
| `stop` | Stops services in the project. |
| `restart` | Restarts services in the project. |
| `down` | Stops and removes all containers in the project. |

**Example:**
```bash
# Start current project
minibox compose up

# Stop and cleanup
minibox compose down
```

For a full guide on the Compose YAML schema and features, see `docs/COMPOSE.md`.

---

## Typical session

Terminal 1 — start daemon:

```bash
./bin/miniboxd
```

Terminal 2 — use CLI:

```bash
export PATH="$PWD/bin:$PATH"   # optional

minibox ping
minibox build -t demo .
minibox run demo
minibox ps
minibox stop <container-id>
minibox rm <container-id>
```

With API token (daemon and CLI):

```bash
export MINIBOX_API_TOKEN="$(openssl rand -hex 16)"
MINIBOX_API_TOKEN="$MINIBOX_API_TOKEN" ./bin/miniboxd &
export MINIBOX_API_TOKEN="$MINIBOX_API_TOKEN"
minibox ping
```

---

## Container ID format

Container IDs are **8 hexadecimal characters** (e.g. `a1b2c3d4`). Use the ID returned by `run` / `ps` for `logs`, `stats`, `stop`, and `rm`.

---

## Troubleshooting

- **Connection refused:** Start `miniboxd` or set `MINIBOX_API` to the correct URL.
- **401 Unauthorized:** Set `MINIBOX_API_TOKEN` on the client to match the daemon’s `MINIBOX_API_TOKEN`.
- **Build context rejected:** Ensure the context directory is under a path allowed by `MINIBOX_BUILD_PREFIXES` on the daemon.
- **Wipe local state (`./data`):** If the daemon created root-owned files, run from the repo root: `sudo ./scripts/clean-data.sh` (or set `MINIBOX_DATA_ROOT` to point at another directory to clean). This deletes images, layers, containers, and blobs under that path and recreates an empty directory.
- **`could not unshare mount namespace: operation not permitted`:** The child skips a second `unshare` when the daemon sets `MINIBOX_CHILD_NEWNS=1`. Rebuild and restart the daemon.
- **`Error remounting root private: operation not permitted` / `no such process` (network):** Rootless kernels often deny `MS_PRIVATE` on `/`. The runtime now bind-mounts `rootfs` first and marks that mount private, with fallbacks; the “no such process” message was the container exiting early—rebuild after this fix.
- **DB containers (Postgres example) exits early:** Known issue on some kernels because Postgres requires a more complete `/dev` setup (real device nodes like `/dev/null`). This will be improved in a future release; for now, treat DB images as experimental.

### Startup performance knobs

The daemon can do expensive work on startup (blob indexing and bridge setup). You can disable these to make startup near-instant:

- `MINIBOX_INDEX_ON_STARTUP=0`: skip indexing blobs at daemon start (lazy indexing still possible later)
- `MINIBOX_BRIDGE_ON_STARTUP=0`: skip bridge setup at daemon start (bridge is created lazily on first container network setup)

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
