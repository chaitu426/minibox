# minibox Full Architecture Deep Dive

This document explains the current system end-to-end in implementation-level depth:

- how a MiniBox file is parsed
- how DAG blocks are built and cached
- how OCI artifacts are laid out and indexed
- how `run` executes containers (namespaces, rootfs, networking, init/reaper, seccomp)
- how state/health/logging works
- how CLI maps to daemon APIs
- where each feature is implemented and how to extend it safely

For visual references, see `docs/ARCHITECTURE_DIAGRAMS.md`.

---

## 1) System Overview

minibox has two binaries:

- **Daemon**: `cmd/daemon/main.go` → starts HTTP API and runtime backend
- **CLI**: `cmd/cli/main.go` → user-facing command tool that calls daemon APIs

Primary domains:

- **Build system**: MiniBox parser + DAG scheduler + layer creation
- **Image storage**: OCI-like blobs and `index.json`
- **Runtime**: container process, namespace/chroot isolation, cgroups, seccomp, health checks
- **Network**: bridge + veth + iptables NAT/port-forwarding
- **Control plane**: API handlers + CLI commands + persisted state

---

## 2) Core Data Model

### 2.1 Build model (MiniBox)

Defined in `internal/models/cfile.go`:

- `Cfile`
  - `BaseImage`
  - `Blocks` (DAG blocks)
  - `Cmd`, `Env`, `Workdir`
  - `HealthcheckCmd`, `HealthcheckIntervalSec`
  - `Instructions` (legacy linear mode fallback)

### 2.2 OCI model

Defined in `internal/models/oci.go`:

- `OCIImageIndex` (`index.json`)
- `OCIManifest` (points to config + layers)
- `OCIConfig` + `ContainerConfig` (cmd/env/workdir/labels)
- `OCIDescriptor` (digest, media type, size)

### 2.3 Runtime state model

Defined in `internal/runtime/state.go`:

- `ContainerInfo`
  - `ID`, `Image`, `Command`, `PID`
  - `Status`
  - `Health` (`starting|healthy|unhealthy|none`)
  - `CreatedAt`, `ExitCode`
  - `Ports` (`hostPort -> containerPort`)

Persisted in `state.json` under `DataRoot`.

---

## 3) Build Pipeline (MiniBox → OCI blobs)

Entry point:

- API: `POST /containers/build` (`internal/api/handler/build.go`)
- Calls: `builder.BuildImage(...)` (`internal/builder/builder.go`)

### 3.1 Parse phase

Parser in `internal/parser/parser.go` supports:

- **DAG format**:
  - `BASE`
  - `BLOCK <name>`
  - block-level `NEED`, `RUN`, `COPY`, `WORKDIR`, `ENV`, `AUTO-DEPS`, etc.
  - top-level `START` (final command)
  - top-level `HEALTHCHECK [--interval=N] <cmd...>`
- **Legacy format** aliases (`BOX`, `START-WITH`, etc.)

### 3.2 Base image prep

Current supported base: Alpine (`alpine` / `alpine:latest`).

- Downloads/extracts Alpine minirootfs if needed.
- Caches in `DataRoot/base_layers/alpine`.

### 3.3 DAG execution model

`buildFromBlocks(...)`:

- Computes executable wavefronts of blocks whose dependencies are satisfied.
- Runs each wave in parallel goroutines.
- Uses per-line prefixed writer (`[block] ...`) to avoid interleaved partial lines.

Block-level execution (`buildBlock(...)`):

- Creates temp overlay root (`tmp/<hash>/{upper,work,root}`)
- Runs instructions:
  - `RUN`: mounts overlay and executes through `chroot ... /bin/sh -c ...`
  - `COPY`: copy from context to upper layer
  - `WORKDIR`: updates effective workdir and ensures path exists
- Optional `AUTO-DEPS` pass (detects package manifests and runs installers)
- Finalizes by moving upper dir into `layers/<hash>`

### 3.4 Hashing and cache keys

Key points:

- Layer hash includes parent hash + deps + workdir + instruction content.
- Cached block if `layers/<hash>` already exists.
- Logs show `CACHED` vs `DONE` and wave-level summary.

### 3.5 OCI finalization

After block/layer generation:

- Tar+gzip each layer to `blobs/sha256/<digest>`
- Generate `OCIConfig` blob and `OCIManifest` blob
- Update `index.json` annotations:
  - `org.opencontainers.image.ref.name = <imageName>`

Healthcheck metadata currently encoded via labels:

- `mini.healthcheck.cmd`
- `mini.healthcheck.interval`

### 3.6 Build logging format (current)

Structured stream in builder:

- `[build] START ...`
- `[base] ...`
- `[build] mode=dag ...`
- `[dag] wave=...`
- `[block] START/CACHED/DONE ...`
- `[dag] wave done ...`
- `[dag-summary] ...`
- `[finalize] ...`
- `[build] DONE ...`

---

## 4) OCI Storage Layout

Under `DataRoot` (default `/var/lib/minibox` unless overridden):

- `index.json` (image references)
- `blobs/sha256/<digest>` (manifest/config/layer blobs)
- `layers/` (builder upperdir cache)
- `base_layers/` (alpine base rootfs)
- `containers/<id>/` (`upper`, `work`, `rootfs`, logs)
- `lazy/`, `cache/`, `extracted/` (lazy/full layer mechanisms)
- `state.json` (runtime state)

---

## 5) Runtime Execution Path

## 5.1 API to runtime

`POST /containers/run` (`internal/api/handler/container.go`) does:

1. validate request + image + port mappings
2. resolve default command from image config when omitted
3. generate container ID
4. call:
   - `runtime.RunCommand(...)` (detached)
   - `runtime.RunCommandStream(...)` (foreground stream)

### 5.2 Parent launcher (`process.go`)

`RunCommand` / `RunCommandStream`:

- Resolve image config + layers from OCI
- Prepare container dirs and mount overlay rootfs (`MountRootfs`)
- Spawn child process as `/proc/self/exe child ...`
- Set namespace clone flags:
  - `CLONE_NEWPID`, `CLONE_NEWUTS`, `CLONE_NEWNS`, `CLONE_NEWNET`
- Setup container networking (`SetupContainerNetwork`)
- Register runtime state (`RegisterContainer`)
- Start health monitor goroutine (if image has healthcheck labels)
- Wait and mark exit status

### 5.3 Child runtime (`container.go`)

`RunContainer()`:

- Validates args/container ID
- Ensures mount namespace is isolated (via env guard + optional unshare fallback)
- Applies cgroup writes (`memory.max`, `cpu.max`, `cgroup.procs`)
- Sets hostname
- Enters container rootfs via `chroot` (rootless-compatible path)
- Mounts `/proc`, `/dev` minimal environment
- Applies security:
  - capability drop
  - rlimits
  - seccomp filter
- Execs tiny init:
  - `/proc/self/exe init -- <cmd...>`

### 5.4 PID 1 tiny init (`init.go`)

`RunInit()`:

- Starts target command
- Forwards incoming signals to process group
- Reaps zombies using `wait4(-1, ...)`
- Exits with child status/signal semantics

This prevents zombie accumulation when workload forks subprocesses.

---

## 6) Security Controls

### 6.1 API hardening

- Method-based routes (`GET`/`POST`) in `internal/api/router.go`
- request body limits on high-risk endpoints
- optional bearer token auth (`MINIBOX_API_TOKEN`) via middleware
- strict input validation (`internal/security/security.go`)

### 6.2 Runtime hardening

- Namespace isolation (PID/UTS/MOUNT/NET)
- seccomp deny-list (`internal/runtime/seccomp_linux.go`)
- capability dropping (`internal/runtime/drop_linux.go`)
- `PR_SET_NO_NEW_PRIVS` before seccomp
- container ID/path validation to avoid traversal

### 6.3 Build context restrictions

Build contexts constrained to configured path prefixes:

- `MINIBOX_BUILD_PREFIXES`
- validated in build handler

---

## 7) Networking Architecture

Implemented in `internal/network/network.go`.

### 7.1 Host setup

`SetupBridge()`:

- creates `minibox0` bridge
- assigns `172.19.0.1/24`
- enables forwarding sysctls
- adds NAT masquerade rules

### 7.2 Per-container networking

`SetupContainerNetwork(pid, id, ip, portMap)`:

- create veth pair (`veth-<id>`, `vetp-<id>`)
- attach host veth to bridge
- move peer into container netns
- configure inside netns:
  - loopback up
  - rename to `eth0`
  - assign IP
  - add default route
- program DNAT/FORWARD rules for `-p` mappings

### 7.3 Teardown

- `TeardownContainerNetwork(...)` removes veth + per-container rules
- `TeardownBridge()` removes base bridge/NAT setup (daemon shutdown)

---

## 8) Health Checks

Current implementation:

1. Parse `HEALTHCHECK` in MiniBox.
2. Persist as config labels in OCI config.
3. On container run, start monitor goroutine:
   - periodic `nsenter ... /bin/sh -c <healthcheck cmd>`
4. Update state `Health` to `starting|healthy|unhealthy`.
5. `ps` shows health column.

Notes:

- Current health execution uses `nsenter` from host.
- It is “basic support”; retries/start-period thresholds are not yet full Docker parity.

---

## 9) Image Export / Import

Implemented in `internal/storage/image_archive.go`.

### 9.1 Save (`minibox save`)

- Find manifest digest by image name in `index.json`
- Read manifest + config + layer blobs
- Pack into tar:
  - `meta.json` (`image`, `manifest_digest`)
  - `blobs/sha256/*` files (+ optional `*.index.json`)

### 9.2 Load (`minibox load`)

- Unpack tar under `DataRoot`
- Read `meta.json`
- Upsert image reference into `index.json`

This is local archive portability for images already in OCI blob store.

---

## 10) Runtime State and Observability

### 10.1 State persistence

`state.json` contains `ContainerInfo` map.
Updated by:

- register on start
- status/health transitions
- exit code writeback
- remove on `rm`

### 10.2 Logs

- Per-container logs: `containers/<id>/container.log`
- Build logs streamed directly from build pipeline

### 10.3 Stats

`runtime/stats.go` reads:

- cgroup v2 metrics (memory/cpu/pids/io)
- host veth counters for network

CLI renders live TUI-like stream.

---

## 11) CLI Architecture

CLI in `cmd/cli/main.go`:

- communicates over HTTP only (daemon API)
- supports token auth by env passthrough
- stable timeouts and consistent exit codes
- table output and optional JSON (`ps --json`, `images --json`)
- plain mode via `NO_COLOR` / `MINIBOX_PLAIN`

Major commands:

- Build/run lifecycle: `build`, `run`, `logs`, `ps`, `stats`
- Control: `stop`, `kill`, `rm`, `exec`
- Images: `images`, `rmi`, `save`, `load`
- Maintenance: `system prune`

---

## 12) End-to-End Flows

## 12.1 Build flow (MiniBox to local image)

1. CLI `build -t X <ctx>`
2. API `/containers/build`
3. Parse MiniBox
4. Build DAG blocks into layer dirs
5. Tar+gzip layers into OCI blobs
6. Write config+manifest blobs
7. Update `index.json` with `X -> manifest digest`
8. Stream structured logs back to CLI

## 12.2 Run flow (image to process)

1. CLI `run ... X`
2. API `/containers/run`
3. Runtime resolves `X` in `index.json`
4. Mount overlay rootfs
5. Spawn child in namespaces
6. Child chroot + proc/dev + security setup
7. Child execs tiny init → workload
8. Parent wires networking + updates state
9. Wait; record exit code; teardown network

## 12.3 Save/load flow

1. CLI `save X out.tar` → API `/images/save`
2. storage packs blobs + metadata tar
3. CLI `load out.tar` → API `/images/load`
4. storage extracts and updates index

---

## 13) Configuration Surface

From `internal/config/config.go` + CLI env usage:

- `MINIBOX_DATA_ROOT`
- `MINIBOX_HTTP_ADDR`
- `MINIBOX_BUILD_PREFIXES`
- `MINIBOX_API_TOKEN`
- `MINIBOX_API` (CLI target)
- `NO_COLOR`, `MINIBOX_PLAIN` (CLI output mode)

---

## 14) Known Gaps / Future Hardening

This section describes current behavior honestly for production planning:

- User namespace remap is currently not active in launcher flags (rootful execution path).
- Healthcheck model is basic (interval + command) and not full OCI HealthConfig semantics.
- `exec` currently uses local `nsenter` from CLI side (not daemon-side streaming API).
- Build system currently supports Alpine base only.
- Save/load format is local minibox archive, not full Docker save format compatibility.

These are good future milestones but do not block current operation.

---

## 15) File Map (Where to Read Next)

- Build core: `internal/builder/builder.go`
- Parser: `internal/parser/parser.go`
- Runtime parent/child/init:
  - `internal/runtime/process.go`
  - `internal/runtime/container.go`
  - `internal/runtime/init.go`
- Security:
  - `internal/runtime/seccomp_linux.go`
  - `internal/runtime/drop_linux.go`
  - `internal/security/security.go`
- Network: `internal/network/network.go`
- API routing/handlers:
  - `internal/api/router.go`
  - `internal/api/handler/*.go`
- OCI models: `internal/models/oci.go`
- Runtime state: `internal/runtime/state.go`
- Storage:
  - `internal/storage/image_archive.go`
  - `internal/storage/indexer.go`
  - `internal/storage/prune.go`
- CLI: `cmd/cli/main.go`
- Daemon entry: `cmd/daemon/main.go`

---

## 16) Practical “How You Built It” Summary

In concise terms, your architecture is:

1. **MiniBox compiler** (parser + DAG scheduler) builds deterministic layer graph.
2. **OCI packager** writes config/manifest/layers as digest-addressed blobs.
3. **Runtime launcher** resolves image references to rootfs layers and process config.
4. **Container executor** applies namespace isolation + chroot + hardening, then runs payload under tiny init.
5. **Network plane** allocates per-container veth/IP and optional host port mapping.
6. **Control plane** (API + CLI) orchestrates lifecycle and exposes observability.
7. **State + archive system** makes runs traceable and images portable.

That is already a solid “modern minimal container engine” architecture with clear boundaries and extension points.

