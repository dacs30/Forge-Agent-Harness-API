# HaaS — Architecture Overview

**HaaS** (Hosted Application as a Service) is a containerized environment management service built in Go. It exposes a REST API to create, manage, and destroy Docker containers on-demand, execute commands inside them, and manage files — with automatic lifecycle cleanup.

---

## Table of Contents

- [High-Level Architecture](#high-level-architecture)
- [Project Structure](#project-structure)
- [Layer Diagram](#layer-diagram)
- [Request Flow](#request-flow)
- [Domain Model](#domain-model)
- [API Endpoints](#api-endpoints)
- [Container Lifecycle](#container-lifecycle)
- [Reaper — Automatic Cleanup](#reaper--automatic-cleanup)
- [Engine — Docker Abstraction](#engine--docker-abstraction)
- [Security Model](#security-model)
- [Network Policies](#network-policies)
- [Store — State Management](#store--state-management)
- [Configuration](#configuration)
- [Streaming Exec Protocol](#streaming-exec-protocol)
- [Middleware Stack](#middleware-stack)
- [Dependency Graph](#dependency-graph)

---

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      HTTP Clients                       │
└──────────────────────────┬──────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│                    HaaS REST API                        │
│                  (chi router, :8080)                     │
│                                                         │
│  ┌─────────────┐  ┌──────────┐  ┌───────────────────┐  │
│  │ Environments│  │   Exec   │  │      Files        │  │
│  │   Handler   │  │  Handler │  │     Handler       │  │
│  └──────┬──────┘  └────┬─────┘  └────────┬──────────┘  │
└─────────┼──────────────┼─────────────────┼──────────────┘
          │              │                 │
          ▼              ▼                 ▼
┌─────────────────────────────────────────────────────────┐
│                     Engine (Docker)                      │
│                                                         │
│  Container Lifecycle │ Exec Streams │ File Ops (tar)    │
│  Security Policies   │ Multiplexing │ Read/Write/List   │
└──────────────────────────┬──────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────┐
│                    Docker Daemon                         │
│               (via Docker SDK for Go)                    │
└─────────────────────────────────────────────────────────┘

┌──────────────────┐          ┌────────────────────────┐
│   Memory Store   │◄────────►│   Reaper (goroutine)   │
│  (environments)  │          │  cleanup every 30s     │
└──────────────────┘          └────────────────────────┘
```

---

## Project Structure

```
haas/
├── cmd/haas/main.go           # Entry point — wires all components
├── internal/
│   ├── api/                   # HTTP handlers & middleware
│   │   ├── router.go          # Route definitions & middleware stack
│   │   ├── environments.go    # CRUD handlers for environments
│   │   ├── exec.go            # Command execution streaming handler
│   │   ├── files.go           # File list/read/write handlers
│   │   ├── middleware.go       # Request ID, logging, panic recovery
│   │   ├── request.go         # JSON decoding & body size limits
│   │   └── errors.go          # Structured error responses
│   ├── config/config.go       # Environment-variable configuration
│   ├── domain/environment.go  # Core types: Environment, ExecRequest, etc.
│   ├── engine/                # Container runtime abstraction
│   │   ├── engine.go          # Engine interface
│   │   ├── docker.go          # Docker SDK implementation
│   │   ├── security.go        # Container security hardening
│   │   ├── network.go         # Network policy mapping
│   │   └── mock.go            # Mock engine for testing
│   ├── lifecycle/reaper.go    # Background expired-container cleanup
│   └── store/                 # Environment state persistence
│       ├── store.go           # Store interface
│       └── memory.go          # In-memory map implementation
├── pkg/apitypes/types.go      # Public request/response types (SDK)
└── test/                      # Integration & test utilities
    ├── integration/api_test.go
    └── testutil/docker.go
```

---

## Layer Diagram

```
┌──────────────────────────────────────────────────────┐
│                   cmd/haas/main.go                   │
│          Bootstrap ◦ Config ◦ Graceful Shutdown       │
└────────────┬────────────┬────────────┬───────────────┘
             │            │            │
             ▼            ▼            ▼
┌──────────────┐  ┌─────────────┐  ┌───────────────┐
│   API Layer  │  │   Lifecycle │  │    Config      │
│  (internal/  │  │   (Reaper)  │  │  (env vars)   │
│    api/)     │  │             │  │               │
└──────┬───────┘  └──────┬──────┘  └───────────────┘
       │                 │
       │     ┌───────────┤
       ▼     ▼           ▼
┌──────────────┐  ┌─────────────┐
│    Engine    │  │    Store    │
│  (Docker)    │  │  (Memory)   │
└──────┬───────┘  └─────────────┘
       │
       ▼
┌──────────────┐
│ Docker Daemon│
└──────────────┘
```

**Dependency direction:** `main` → `API`, `Lifecycle` → `Engine`, `Store` → `Docker`

All layers depend on `domain` types. The `API` layer depends on both `Engine` and `Store`.

---

## Request Flow

```
Client Request
      │
      ▼
┌─────────────────────────────────────────────────────┐
│                  Middleware Pipeline                  │
│                                                     │
│  1. RequestIDMiddleware  (assign/forward X-Request-ID)  │
│  2. RecoveryMiddleware   (catch panics → 500)        │
│  3. LoggingMiddleware    (log method, path, status)  │
│  4. RealIP Middleware    (extract client IP)          │
└──────────────────────────┬──────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────┐
│                    Route Handler                     │
│                                                     │
│  ┌─────────────┐ ┌────────────┐ ┌────────────────┐ │
│  │ Environments│ │    Exec    │ │     Files      │ │
│  │   CRUD      │ │  Streaming │ │  List/Read/    │ │
│  │             │ │  (NDJSON)  │ │  Write         │ │
│  └──────┬──────┘ └─────┬──────┘ └───────┬────────┘ │
└─────────┼──────────────┼────────────────┼───────────┘
          │              │                │
     ┌────┘         ┌────┘           ┌────┘
     ▼              ▼                ▼
 ┌────────┐    ┌────────┐      ┌────────┐
 │ Store  │    │ Engine │      │ Engine │
 └────────┘    └────────┘      └────────┘
```

---

## Domain Model

```
┌──────────────────────────────────────────┐
│              Environment                 │
├──────────────────────────────────────────┤
│  ID            string  ("env_xxxxxxxx")  │
│  Status        EnvironmentStatus         │
│  ContainerID   string                    │
│  CreatedAt     time.Time                 │
│  LastUsedAt    time.Time                 │
│  ExpiresAt     time.Time                 │
│  Spec          EnvironmentSpec           │
└──────────────┬───────────────────────────┘
               │
               ▼
┌──────────────────────────────────────────┐
│          EnvironmentSpec                 │
├──────────────────────────────────────────┤
│  Image          string                   │
│  CPU            float64   (0.1 – 4.0)    │
│  MemoryMB       int64     (128 – 8192)   │
│  DiskMB         int64                    │
│  NetworkPolicy  NetworkPolicy            │
│  EnvVars        map[string]string        │
└──────────────────────────────────────────┘

┌───────────────────────────┐
│    EnvironmentStatus      │
├───────────────────────────┤
│  "creating"               │
│  "running"                │
│  "stopping"               │
│  "stopped"                │
│  "destroyed"              │
└───────────────────────────┘

┌───────────────────────────┐
│     NetworkPolicy         │
├───────────────────────────┤
│  "none"                   │
│  "egress-limited"         │
│  "full"                   │
└───────────────────────────┘
```

### Supporting Types

| Type | Fields | Purpose |
|------|--------|---------|
| `ExecRequest` | `Command []string`, `WorkingDir string`, `TimeoutSeconds int`, `CaptureOutput bool` | Describes a command to execute inside a container |
| `ExecEvent` | `Stream string`, `Data string` | A single output event (stdout/stderr/exit) |
| `FileInfo` | `Name`, `Path`, `Size`, `IsDir`, `ModTime` | File metadata for directory listings |

---

## API Endpoints

All API routes are versioned under `/v1/`.

| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| `GET` | `/healthz` | inline | Health check → `{"status": "ok"}` |
| `POST` | `/v1/environments` | `Create` | Create and start a new container |
| `GET` | `/v1/environments` | `List` | List all environments |
| `GET` | `/v1/environments/{id}` | `Get` | Get a single environment by ID |
| `DELETE` | `/v1/environments/{id}` | `Destroy` | Stop and destroy an environment |
| `POST` | `/v1/environments/{id}/exec` | `Exec` | Execute a command (streamed NDJSON) |
| `GET` | `/v1/environments/{id}/files` | `ListFiles` | List files at a path (query: `?path=`) |
| `GET` | `/v1/environments/{id}/files/content` | `ReadFile` | Download file content (query: `?path=`) |
| `PUT` | `/v1/environments/{id}/files/content` | `WriteFile` | Upload file content (query: `?path=`) |

### Create Environment — Request/Response

**Request** (`POST /v1/environments`):
```json
{
  "image": "ubuntu:22.04",
  "cpu": 1.0,
  "memory_mb": 2048,
  "disk_mb": 4096,
  "network_policy": "none",
  "env_vars": {"KEY": "value"}
}
```

**Response** (201 Created):
```json
{
  "id": "env_a1b2c3d4",
  "status": "running",
  "image": "ubuntu:22.04"
}
```

### Exec — Request/Response

**Request** (`POST /v1/environments/{id}/exec`):
```json
{
  "command": ["bash", "-c", "echo hello"],
  "working_dir": "/app",
  "timeout_seconds": 30
}
```

**Response** (200 OK, `application/x-ndjson` stream):
```
{"stream":"stdout","data":"hello\n"}
{"stream":"exit","data":"0"}
```

---

## Container Lifecycle

```
  POST /v1/environments
          │
          ▼
   ┌──────────────┐
   │   creating    │──── engine.CreateContainer()
   └──────┬───────┘      engine.StartContainer()
          │
          ▼
   ┌──────────────┐
   │   running     │◄─── exec, file ops update LastUsedAt
   └──────┬───────┘
          │
          │  DELETE /v1/environments/{id}
          │  OR Reaper (idle/expired)
          ▼
   ┌──────────────┐
   │  stopping     │──── engine.StopContainer() (10s timeout)
   └──────┬───────┘      removes container + volumes
          │
          ▼
   ┌──────────────┐
   │  destroyed    │──── store.Delete()
   └──────────────┘
```

Each container:
- Runs `sleep infinity` as its main process to stay alive
- Is labeled with `haas.environment.id` and `haas.managed=true`
- Has an **idle timeout** (default 10 min) and **max lifetime** (default 60 min)

---

## Reaper — Automatic Cleanup

The **Reaper** is a background goroutine that runs every **30 seconds** to garbage-collect expired environments.

```
            ┌─────────────────────────────┐
            │       Reaper Loop           │
            │     (every 30 seconds)      │
            └─────────────┬───────────────┘
                          │
                          ▼
              ┌───────────────────────┐
              │  store.ListExpired()  │
              │                       │
              │  Returns envs where:  │
              │  • now > LastUsedAt   │
              │       + IdleTimeout   │
              │  OR                   │
              │  • now > ExpiresAt    │
              └───────────┬───────────┘
                          │
                ┌─────────┴─────────┐
                ▼                   ▼
         ┌────────────┐      ┌────────────┐
         │  env-001   │      │  env-002   │ ...
         └─────┬──────┘      └─────┬──────┘
               │                   │
               ▼                   ▼
        engine.StopContainer  engine.StopContainer
               │                   │
               ▼                   ▼
         store.Delete         store.Delete
```

---

## Engine — Docker Abstraction

The `Engine` interface decouples business logic from the Docker SDK:

```
┌──────────────────────────────────────────────────┐
│                Engine Interface                   │
├──────────────────────────────────────────────────┤
│  CreateContainer(ctx, env) → (containerID, err)  │
│  StartContainer(ctx, containerID) → err          │
│  StopContainer(ctx, containerID) → err           │
│  Exec(ctx, containerID, req) → (io.ReadCloser)   │
│  ExecExitCode(ctx, execID) → (int, err)          │
│  ListFiles(ctx, containerID, path) → []FileInfo  │
│  ReadFile(ctx, containerID, path) → ReadCloser   │
│  WriteFile(ctx, containerID, path, content) → err│
└────────────┬──────────────────────┬──────────────┘
             │                      │
             ▼                      ▼
    ┌─────────────────┐    ┌────────────────┐
    │  DockerEngine   │    │  MockEngine    │
    │  (production)   │    │  (testing)     │
    └─────────────────┘    └────────────────┘
```

### Docker Implementation Details

| Operation | Implementation |
|-----------|----------------|
| **Create** | Pulls image → creates container with security config, labels, `sleep infinity` |
| **Start/Stop** | Docker API start/stop (10s kill timeout) + remove container + volumes |
| **Exec** | Creates exec instance → attaches → returns multiplexed stream |
| **ListFiles** | Runs `find` inside container (falls back to `ls -la`) |
| **ReadFile** | `CopyFromContainer` → extracts from tar archive |
| **WriteFile** | Builds tar archive → `CopyToContainer` |

---

## Security Model

Every container is hardened with the following constraints:

```
┌─────────────────────────────────────────────────┐
│              Security Configuration              │
├─────────────────────────────────────────────────┤
│                                                 │
│  Privileged:          false                     │
│  Capabilities:        DROP ALL                  │
│  SecurityOpt:         no-new-privileges         │
│  PID Limit:           256                       │
│  Memory Swap:         disabled                  │
│                                                 │
│  Resource Limits:                               │
│  ┌────────────────────────────────────────────┐ │
│  │  CPU:        0.1 – 4.0 cores (NanoCPUs)   │ │
│  │  Memory:     128 – 8192 MB (hard limit)    │ │
│  │  Disk:       configurable (StorageOpt)     │ │
│  │  PIDs:       max 256 processes             │ │
│  └────────────────────────────────────────────┘ │
│                                                 │
│  Conditional:                                   │
│  • NET_BIND_SERVICE added if network ≠ none     │
│  • StorageOpt["size"] set if disk specified     │
│                                                 │
└─────────────────────────────────────────────────┘
```

---

## Network Policies

```
┌─────────────────┐     ┌─────────────────────────────────┐
│  NetworkPolicy  │────▶│   Docker Network Mode            │
├─────────────────┤     ├─────────────────────────────────┤
│  "none"         │────▶│  "none" (complete isolation)     │
│  "egress-limited"│───▶│  "bridge" (MVP, needs iptables) │
│  "full"         │────▶│  "bridge" (full access)          │
└─────────────────┘     └─────────────────────────────────┘
```

> **Note:** `egress-limited` currently maps to `bridge` as an MVP. A production implementation would use a custom Docker network with `iptables` rules to restrict egress to specific CIDRs.

---

## Store — State Management

```
┌────────────────────────────────────────────────┐
│                Store Interface                  │
├────────────────────────────────────────────────┤
│  Create(ctx, env) → err                        │
│  Get(ctx, id) → (*Environment, err)            │
│  Update(ctx, env) → err                        │
│  Delete(ctx, id) → err                         │
│  List(ctx) → ([]*Environment, err)             │
│  ListExpired(ctx) → ([]*Environment, err)      │
└────────────┬───────────────────────────────────┘
             │
             ▼
┌────────────────────────────────────────────────┐
│            MemoryStore                          │
├────────────────────────────────────────────────┤
│  map[string]*Environment (sync.RWMutex)        │
│                                                │
│  Expiration logic (ListExpired):               │
│  • Skips stopped/destroyed environments        │
│  • Expired if: now > LastUsedAt + IdleTimeout  │
│  • Expired if: now > ExpiresAt (MaxLifetime)   │
└────────────────────────────────────────────────┘
```

The store is currently **in-memory only** — all state is lost on restart. The `Store` interface allows for future database-backed implementations.

---

## Configuration

All configuration is loaded from environment variables with sensible defaults:

| Env Variable | Default | Description |
|---|---|---|
| `HAAS_LISTEN_ADDR` | `:8080` | HTTP server bind address |
| `DOCKER_HOST` | (auto) | Docker daemon socket |
| `HAAS_DEFAULT_CPU` | `1.0` | Default CPU cores per container |
| `HAAS_DEFAULT_MEMORY_MB` | `2048` | Default memory per container (MB) |
| `HAAS_DEFAULT_DISK_MB` | `4096` | Default disk per container (MB) |
| `HAAS_IDLE_TIMEOUT` | `10m` | Idle timeout before reaping |
| `HAAS_MAX_LIFETIME` | `60m` | Maximum container lifetime |
| `HAAS_DEFAULT_NETWORK_POLICY` | `none` | Default network policy |

---

## Streaming Exec Protocol

Command execution uses **NDJSON** (Newline-Delimited JSON) streaming over HTTP:

```
Client                                    Server
  │                                         │
  │  POST /v1/environments/{id}/exec        │
  │  {"command": ["ls", "-la"]}             │
  │────────────────────────────────────────▶│
  │                                         │
  │  200 OK                                 │
  │  Content-Type: application/x-ndjson     │
  │◀────────────────────────────────────────│
  │                                         │
  │  {"stream":"stdout","data":"total 4\n"} │
  │◀────────────────────────────────────────│
  │  {"stream":"stdout","data":"file.txt"}  │
  │◀────────────────────────────────────────│
  │  {"stream":"exit","data":"0"}           │
  │◀────────────────────────────────────────│
  │                                         │
```

Internally, Docker's multiplexed stream (8-byte header per frame) is demuxed into `stdout`/`stderr` events and flushed to the client in real-time.

---

## Middleware Stack

Requests pass through middleware in the following order:

```
Request ──▶ RequestID ──▶ Recovery ──▶ Logging ──▶ RealIP ──▶ Handler
              │              │            │           │
              │              │            │           └─ Extracts client IP
              │              │            └─ Logs method, path, status,
              │              │               duration_ms, request_id
              │              └─ Catches panics → 500 Internal Server Error
              └─ Generates UUID or reads X-Request-ID header
```

Additional protections:
- **Request body limit**: 1 MB max (`request.go`)
- **Unknown JSON fields rejected** in request decoding
- **Structured error responses**: `{"error": "...", "code": 400, "detail": "..."}`

---

## Dependency Graph

```
                    ┌───────────┐
                    │   main    │
                    └─────┬─────┘
          ┌───────────┬───┴────┬──────────────┐
          ▼           ▼        ▼              ▼
     ┌────────┐  ┌────────┐ ┌──────────┐ ┌────────┐
     │  api   │  │lifecycle│ │  config  │ │ engine │
     │(router,│  │(reaper) │ │          │ │(docker)│
     │handlers│  │         │ │          │ │        │
     └───┬──┬─┘  └──┬───┬──┘ └──────────┘ └────┬───┘
         │  │       │   │                       │
         │  └───────┼───┼───────────────────────┘
         │          │   │
         ▼          ▼   ▼
     ┌────────┐  ┌────────┐
     │ store  │  │ engine │
     └───┬────┘  └───┬────┘
         │           │
         ▼           ▼
     ┌────────┐  ┌────────────┐
     │ domain │  │ Docker SDK │
     └────────┘  └────────────┘
```

All layers depend on **`domain`** for shared types. The **`api`** and **`lifecycle`** layers consume both **`store`** and **`engine`** through interfaces, enabling testability via the **`MockEngine`**.
