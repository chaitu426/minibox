# mini-docker Architecture Diagrams

This file contains visual architecture diagrams for major features, plus one full end-to-end system diagram.

Use any Markdown viewer with Mermaid support to render these.

---

## 1) End-to-End System Architecture

```mermaid
flowchart LR
    U[User] --> CLI[mini-docker CLI]
    CLI -->|HTTP| API[Daemon API Router]
    API --> H[Handlers]

    H --> B[Builder Engine]
    H --> R[Runtime Engine]
    H --> S[Storage Engine]
    H --> N[Network Engine]
    H --> ST[State Engine]

    B --> P[MiniBox Parser]
    B --> DAG[DAG Scheduler + Cache]
    B --> OCIW[OCI Writer]

    OCIW --> BLOBS[(blobs/sha256)]
    OCIW --> INDEX[(index.json)]

    R --> ROOTFS[Overlay Rootfs Mount]
    R --> CHILD[Child Container Process]
    CHILD --> INIT[Tiny Init / Reaper]
    CHILD --> SEC[Caps + Rlimits + Seccomp]
    CHILD --> CG[Cgroups v2]

    N --> BR[Bridge mini-docker0]
    N --> VETH[Veth Pair]
    N --> IPT[iptables NAT/DNAT]

    ST --> STATE[(state.json)]
    R --> LOGS[(containers/<id>/container.log)]

    S --> ARCHIVE[Image Save/Load Tar]
    S --> PRUNE[GC / Prune]
```

---

## 2) Build Feature (MiniBox -> OCI Image)

```mermaid
sequenceDiagram
    participant U as User
    participant C as CLI build
    participant A as API /containers/build
    participant P as ParseBoxfile
    participant D as DAG Builder
    participant O as OCI Finalizer
    participant I as index.json
    participant B as blobs/sha256

    U->>C: mini-docker build -t app .
    C->>A: POST image + minibox + context
    A->>P: Parse MiniBox
    P-->>A: Build spec
    A->>D: BuildImage(...)
    D->>D: Resolve waves, execute blocks parallel
    D->>D: Cache check by content hash
    D-->>O: Ordered layer dirs
    O->>B: Write layer tar.gz blobs
    O->>B: Write config + manifest blobs
    O->>I: Upsert image ref -> manifest digest
    O-->>A: Build complete
    A-->>C: Stream logs + success
```

---

## 3) Runtime Run Feature (Foreground/Detached)

```mermaid
sequenceDiagram
    participant U as User
    participant C as CLI run
    participant A as API /containers/run
    participant R as runtime.RunCommand*
    participant N as Network
    participant CH as Child RunContainer
    participant IN as Tiny Init
    participant ST as state.json

    U->>C: mini-docker run [-d] image cmd
    C->>A: POST run request
    A->>R: RunCommand / RunCommandStream
    R->>R: Resolve image config + layers
    R->>R: Mount overlay rootfs
    R->>CH: Spawn /proc/self/exe child (namespaces)
    R->>N: SetupContainerNetwork(pid, ports)
    R->>ST: RegisterContainer(status=running)
    CH->>CH: chroot + mount /proc,/dev
    CH->>CH: cgroups + seccomp + cap drop
    CH->>IN: runInitCmd(workload)
    IN->>IN: forward signals + reap zombies
    IN-->>R: exit status
    R->>ST: MarkContainerExited(exit code)
    R->>N: TeardownContainerNetwork
    A-->>C: output / container id
```

---

## 4) Networking Feature

```mermaid
flowchart TD
    subgraph Host
      BR[Bridge mini-docker0 172.19.0.1/24]
      HV[veth-<id>]
      IPT1[iptables nat POSTROUTING MASQUERADE]
      IPT2[iptables nat PREROUTING/OUTPUT DNAT]
      IPT3[iptables filter FORWARD ACCEPT]
    end

    subgraph Container NetNS
      PV[eth0 (from vetp-<id>)]
      LO[lo]
      IP[172.19.0.x/24]
      GW[default via 172.19.0.1]
    end

    HV --- PV
    HV --> BR
    PV --> IP
    LO --> PV
    IP --> GW
    IPT2 --> PV
    PV --> IPT1
    IPT3 --> PV
```

---

## 5) Healthcheck Feature

```mermaid
sequenceDiagram
    participant B as Builder
    participant OCI as OCI Config Labels
    participant R as Runtime
    participant NS as nsenter command
    participant ST as state.json
    participant PS as mini-docker ps

    B->>OCI: Write mini.healthcheck.cmd
    B->>OCI: Write mini.healthcheck.interval
    R->>R: startHealthMonitor(...)
    R->>ST: health=starting
    loop every N seconds
      R->>NS: nsenter -t <pid> ... /bin/sh -c "<cmd>"
      alt success
        R->>ST: health=healthy
      else fail
        R->>ST: health=unhealthy
      end
    end
    PS->>ST: read container states
    ST-->>PS: HEALTH column values
```

---

## 6) Image Save/Load Feature

```mermaid
flowchart LR
    IDX[(index.json)] --> M[manifest digest]
    M --> MBLOB[manifest blob]
    MBLOB --> CFG[config digest]
    MBLOB --> LAYERS[layer digests]
    CFG --> BLOBS[(blobs/sha256)]
    LAYERS --> BLOBS

    BLOBS --> SAVE[save: tar writer]
    SAVE --> TAR[archive.tar]

    TAR --> LOAD[load: tar reader]
    LOAD --> BLOBS
    LOAD --> IDX
```

---

## 7) Graceful Daemon Shutdown Feature

```mermaid
sequenceDiagram
    participant SIG as SIGINT/SIGTERM
    participant D as Daemon main
    participant HS as HTTP Server
    participant NET as Network Teardown
    participant END as Process Exit

    SIG->>D: shutdown requested
    D->>HS: Shutdown(context)
    HS-->>D: stop accepting + drain inflight
    D->>NET: TeardownBridge()
    NET-->>D: iptables + bridge cleanup
    D-->>END: clean exit
```

---

## 8) Security Feature Stack

```mermaid
flowchart TD
    APIREQ[Incoming API Request]
    APIREQ --> AUTH[Token middleware]
    AUTH --> LIMIT[max body size]
    LIMIT --> VALID[input validation]
    VALID --> RUN[Run container]

    RUN --> NS[Namespaces: pid/uts/mnt/net]
    NS --> ROOT[chroot rootfs]
    ROOT --> RL[rlimits]
    RL --> CAP[Drop ambient + bounding caps]
    CAP --> NNP[PR_SET_NO_NEW_PRIVS]
    NNP --> SC[Seccomp-BPF deny list]
    SC --> APP[Workload process]
```

---

## 9) State + Observability Feature

```mermaid
flowchart LR
    RUN[Container start] --> REG[RegisterContainer]
    REG --> STATE[(state.json)]
    RUN --> LOG[(container.log)]
    HC[Health monitor] --> UH[UpdateContainerHealth]
    UH --> STATE
    EXIT[Process exit] --> MARK[MarkContainerExited]
    MARK --> STATE
    CLI1[mini-docker ps] --> STATE
    CLI2[mini-docker logs] --> LOG
    CLI3[mini-docker stats] --> CGFS[/sys/fs/cgroup + net metrics]
```

---

## 10) MiniBox DAG Wave Execution Feature

```mermaid
flowchart TD
    START[Parsed MiniBox Blocks] --> GRAPH[Dependency Graph]
    GRAPH --> READY[Find blocks with all NEED deps satisfied]
    READY --> WAVE[Execute wave in parallel goroutines]
    WAVE --> HASH[Compute per-block cache hash]
    HASH --> CACHE{Layer cache exists?}
    CACHE -->|Yes| CACHED[Mark cached]
    CACHE -->|No| BUILD[Run block instructions]
    BUILD --> SAVE[Save layer dir + blob]
    CACHED --> DONE[Wave done]
    SAVE --> DONE
    DONE --> MORE{All blocks done?}
    MORE -->|No| READY
    MORE -->|Yes| SUM[DAG summary]
```

---

## 11) CLI <-> API Command Mapping Diagram

```mermaid
flowchart LR
    CLI[mini-docker CLI] --> PING[/GET /ping/]
    CLI --> BUILD[/POST /containers/build/]
    CLI --> RUN[/POST /containers/run/]
    CLI --> LISTC[/GET /containers/]
    CLI --> LOGS[/GET /containers/logs/]
    CLI --> STATS[/GET /containers/stats/]
    CLI --> STOP[/POST /containers/stop/]
    CLI --> KILL[/POST /containers/kill/]
    CLI --> RM[/POST /containers/remove/]
    CLI --> IMGS[/GET /images/]
    CLI --> RMI[/POST /images/remove/]
    CLI --> SAVE[/POST /images/save/]
    CLI --> LOAD[/POST /images/load/]
    CLI --> PRUNE[/POST /system/prune/]
```

