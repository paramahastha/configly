# Configly — Implementation Plan

> Runtime config & feature flag platform. Single Go binary, embedded UI, SDKs for any language.
> This document is a complete, executable build plan. Hand it to Claude in your local IDE and work through it in phases.

---

## How to use this document

1. Read **§1 Vision & Decisions** first so you (and Claude) share the same mental model.
2. Execute phases **2 → 10** in order. Each phase is a self-contained chunk you can paste into a fresh Claude conversation if needed.
3. Each phase has: **Goal**, **Files to create**, **Acceptance criteria**, **How to test**.
4. Before starting, set up your environment as described in §2.

**Prompt template for each phase** (paste into Claude):

> I'm working on Configly, a runtime config platform. Read CONFIGLY_IMPLEMENTATION_PLAN.md. Execute **Phase N**. Follow the acceptance criteria. After implementing, run the test commands listed and show me the output. Don't skip ahead to later phases.

---

## §1 Vision & Decisions

### What it is

A self-hostable service that lets backend and frontend apps read configuration values and feature flags at runtime, with changes propagating in under a second — no redeploy required.

### Target user

Engineers who want LaunchDarkly-style capabilities without LaunchDarkly's price, vendor lock-in, or complexity. Solo devs, small teams, side projects, internal tools.

### Non-goals

- Not a secrets manager (no Vault-style encryption-at-rest features).
- Not an experimentation platform (no statistical analysis of A/B tests).
- Not a service discovery tool.

### Locked-in decisions

| Decision | Choice | Reason |
|---|---|---|
| **License** | **MIT — fully open source, free forever** | Anyone can use, fork, or self-host without restriction. No "open core" tricks, no paid tier. |
| **Hosting model** | **Self-hosted, single binary** | No SaaS dependency. Users own their data. No phone-home telemetry. |
| Server language | **Go 1.22+** | Single binary, fast, low memory, great stdlib HTTP. |
| HTTP framework | **chi** | Minimal, idiomatic, ~zero magic. |
| Default storage | **SQLite (WAL mode)** | Zero-config; ships in the binary. Postgres adapter later. |
| Wire protocol | **HTTP/JSON** | Universal; works through every proxy and firewall. Any language integrates without an SDK. |
| Update mechanism | **ETag long-polling + SSE option** | Long-poll covers 99% of cases. SSE for dashboards. |
| Client distribution | **Per-language SDKs (MIT-licensed)** | Go + JS shipped; community SDKs encouraged. |
| Dashboard | **Single embedded HTML file** | No build step, no SPA framework, no separate deploy. |
| Auth | **API keys (bcrypt + bearer tokens)** | Simplest thing that works; OIDC is a v2 feature. |
| RBAC | **admin / editor / viewer** | Three levels covers nearly all real use cases. |
| **Install promise** | **One command (`docker run`) or one binary** | If setup takes more than 2 minutes on a fresh machine, it's a bug. |
| **Integration promise** | **30 lines of code in any language** | The HTTP API must be simple enough to integrate without reading docs for hours. |

### Architectural shape

```
┌──────────────┐    long-poll / SSE     ┌─────────────────────┐
│ your app(s)  │ ◄────────────────────► │  configly server    │
│  + SDK       │     HTTP + JSON        │  (single binary)    │
└──────────────┘                        │  SQLite or Postgres │
                                        └─────────────────────┘
                                                ▲
                                                │ REST + web UI
                                                ▼
                                        ┌─────────────────────┐
                                        │  dashboard / CI     │
                                        └─────────────────────┘
```

### Final repo layout

```
configly/
├── README.md
├── LICENSE (MIT)
├── CONTRIBUTING.md
├── Dockerfile
├── docker-compose.yml
├── .gitignore
├── .github/workflows/ci.yml
├── server/
│   ├── go.mod
│   ├── cmd/configly/main.go
│   └── internal/
│       ├── model/model.go
│       ├── store/store.go         # interface
│       ├── store/sqlite.go        # default impl
│       ├── auth/auth.go
│       ├── sse/broker.go
│       └── api/
│           ├── handler.go
│           └── ui/index.html       # embedded dashboard
├── sdk/
│   ├── go/
│   │   ├── go.mod
│   │   ├── client.go
│   │   └── client_test.go
│   └── js/
│       ├── package.json
│       ├── tsconfig.json
│       └── src/index.ts
├── examples/
│   ├── go-gin/main.go
│   └── node-express/server.js
└── docs/
    └── architecture.md
```

### Module path convention

Throughout this plan I use `github.com/yourname/configly`. **Replace `yourname` with your actual GitHub username before you start coding** so all imports resolve correctly. A repo-wide find-and-replace at the end is fine too.

---

## §2 Phase 0 — Environment Setup

### Goal
Confirm your dev environment can build and run everything.

### Prerequisites

| Tool | Version | Check |
|---|---|---|
| Go | 1.22+ | `go version` |
| Node.js | 18+ | `node -v` |
| Docker (optional but recommended) | any recent | `docker --version` |
| C compiler (for CGO/sqlite3) | gcc or clang | `gcc --version` or `cc --version` |

### Steps

1. Create the repo:
   ```bash
   mkdir configly && cd configly
   git init
   git branch -M main
   ```
2. Decide your GitHub username/org path. Throughout this plan, replace `yourname` with it.
3. Create the top-level directory skeleton:
   ```bash
   mkdir -p server/cmd/configly \
            server/internal/{model,store,auth,sse,api/ui} \
            sdk/go sdk/js/src \
            examples/{go-gin,node-express} \
            docs \
            .github/workflows
   ```

### Acceptance criteria
- `go version` returns 1.22 or later.
- `gcc --version` works (required for go-sqlite3's CGO build).
- Directory tree above exists.

---

## §3 Phase 1 — Core Data Model

### Goal
Define the Go types that every other component references. This is the contract.

### Files to create
- `server/go.mod`
- `server/internal/model/model.go`

### `server/go.mod`

```go
module github.com/yourname/configly/server

go 1.22

require (
    github.com/go-chi/chi/v5 v5.0.12
    github.com/google/uuid v1.6.0
    github.com/mattn/go-sqlite3 v1.14.22
    golang.org/x/crypto v0.21.0
)
```

### `server/internal/model/model.go`

Define these types (full implementations in §11 reference appendix):

- `ConfigType` (string enum: `string`, `int`, `float`, `bool`, `json`, `flag`)
- `Environment{ID, Project, Name, CreatedAt}`
- `Config{ID, Project, Environment, Key, Type, Value, Rollout, Rules, Description, Version, UpdatedAt, UpdatedBy}`
- `ConfigVersion` — immutable history snapshot
- `User{ID, Email, PasswordHash, Role, APIKey, CreatedAt}`
- `Role` (string enum: `admin`, `editor`, `viewer`)
- `AuditEvent{ID, Actor, Action, Resource, Project, Env, Before, After, CreatedAt}`
- `Snapshot{Project, Environment, ETag, UpdatedAt, Configs map[string]Config}` — the bundle clients fetch

### Acceptance criteria
- `cd server && gofmt -e ./internal/model/model.go` produces no errors.
- `go vet ./internal/model/` is clean.
- Every exported field has a JSON tag.
- `PasswordHash` has JSON tag `-` (never serialized).

### How to test

```bash
cd server && go vet ./internal/model/
```

---

## §4 Phase 2 — Storage Layer

### Goal
Implement the `Store` interface and a SQLite backend with an in-memory snapshot cache.

### Files to create
- `server/internal/store/store.go` (interface)
- `server/internal/store/sqlite.go` (implementation)
- `server/internal/store/sqlite_test.go` (round-trip tests — write these now)

### Design rules

1. **`Store` is a small interface.** New backends (Postgres, etc.) implement the same methods.
2. **Snapshot cache lives in the store**, not in the API layer. Reads must be sub-millisecond on cache hits.
3. **Every write invalidates the relevant `(project, env)` cache entry.**
4. **Versions are append-only.** Rollback creates a *new* version with the old values — it doesn't delete history.
5. **Use WAL mode** for SQLite (`_journal=WAL`) so reads don't block writes.

### Required methods on `Store`

```go
// Environments / projects
CreateEnvironment(ctx, env) error
ListEnvironments(ctx, project) ([]Environment, error)
ListProjects(ctx) ([]string, error)

// Configs
UpsertConfig(ctx, *Config, actor) error
GetConfig(ctx, project, env, key) (*Config, error)
ListConfigs(ctx, project, env) ([]Config, error)
DeleteConfig(ctx, project, env, key, actor) error

// Versions / rollback
ListVersions(ctx, configID, limit) ([]ConfigVersion, error)
Rollback(ctx, configID, version, actor) error

// Snapshot (hot path)
Snapshot(ctx, project, env) (*Snapshot, error)

// Users / auth
CreateUser(ctx, *User) error
GetUserByEmail(ctx, email) (*User, error)
GetUserByAPIKey(ctx, apiKey) (*User, error)
ListUsers(ctx) ([]User, error)

// Audit
AppendAudit(ctx, *AuditEvent) error
ListAudit(ctx, project, limit) ([]AuditEvent, error)

Close() error
```

### SQLite schema

Embed in a `const schema = \`...\`` and execute on `NewSQLite()`:

- `environments(id PK, project, name, created_at, UNIQUE(project, name))`
- `configs(id PK, project, environment, key, type, value, rollout, rules, description, version, updated_at, updated_by, UNIQUE(project, environment, key))` + index on `(project, environment)`
- `config_versions(id PK, config_id, version, type, value, rollout, rules, updated_at, updated_by)` + index on `(config_id, version DESC)`
- `users(id PK, email UNIQUE, password_hash, role, api_key UNIQUE, created_at)`
- `audit(id PK, actor, action, resource, project, environment, before, after, created_at)` + index on `(project, created_at DESC)`

### Snapshot ETag

```
sha256("key=value/version|key=value/version|...")[:16]
```

Sort keys before hashing so the result is deterministic regardless of map iteration order.

### Tests to write now

In `sqlite_test.go`:

- `TestUpsertAndGet` — upsert a config, get it back, version is 1.
- `TestUpsertBumpsVersion` — upsert twice, version is 2, history has 2 rows.
- `TestRollback` — write v1, write v2, rollback to v1, current value matches v1 and version is 3.
- `TestSnapshotCache` — snapshot returns same pointer on second call (cached); after upsert it returns new data with different ETag.
- `TestListAudit` — append 3 events, list them ordered newest first.

### Acceptance criteria
- `go test ./internal/store/ -race` passes.
- ETag is stable across calls when data is unchanged.
- Concurrent reads during a write don't return partial state (covered by `-race`).

### How to test
```bash
cd server && go test -race ./internal/store/
```

---

## §5 Phase 3 — Auth & RBAC ✅ DONE

### Goal
Bcrypt password hashing, API key generation, and role-gated HTTP middleware.

### Files to create
- `server/internal/auth/auth.go`
- `server/internal/auth/auth_test.go`

### Requirements

1. **`HashPassword(pw)`** — uses `bcrypt.DefaultCost`.
2. **`CheckPassword(hash, pw)`** — returns bool.
3. **`NewAPIKey()`** — 24 random bytes hex-encoded, prefixed `cfly_`. The prefix makes leaked keys greppable in logs.
4. **`Middleware(store)`** — extracts the bearer token from `Authorization`, `X-API-Key` header, or `?key=` query param. Loads the user, attaches to context, or returns 401.
5. **`RequireRole(min Role)`** — gates handlers by minimum role using a rank map (`viewer=1`, `editor=2`, `admin=3`).
6. **`UserFrom(ctx)`** — typed helper to recover the user inside handlers.

### Why `?key=` query param

EventSource (browser SSE) cannot set custom headers. Letting clients pass the key as `?key=` is the standard workaround. Document this clearly: SSE keys *will* appear in server access logs.

### Tests to write
- Hash round-trip works.
- Wrong password returns false.
- `NewAPIKey()` produces unique values across 1000 calls.
- Middleware returns 401 with no key.
- Middleware returns 401 with unknown key.
- `RequireRole(editor)` lets editor+admin through, blocks viewer.

### Acceptance criteria
- `go test -race ./internal/auth/` passes.
- API keys start with `cfly_`.

---

## §6 Phase 4 — SSE Broker

### Goal
In-process pub/sub so writes wake up long-poll waiters and SSE streams.

### Files to create
- `server/internal/sse/broker.go`
- `server/internal/sse/broker_test.go`

### Design rules

1. **Topic = `"project:env"`.** Use `sse.Topic(project, env)` helper to build it.
2. **Each subscriber has a buffered channel** (size 8 is enough).
3. **Publish is non-blocking.** If a subscriber's buffer is full, drop the event — they'll catch up via the next poll.
4. **Subscribe returns `(chan, cancel)`.** Callers `defer cancel()`.

### Tests to write
- Subscribe → publish → receive.
- Two subscribers on same topic both get the event.
- Subscriber on different topic gets nothing.
- Slow subscriber doesn't block fast subscriber (publish 20 events to a buffer-8 subscriber, verify publish doesn't hang).
- After cancel, channel is closed and no more events arrive.

### Acceptance criteria
- `go test -race ./internal/sse/` passes including the slow-subscriber test.
- Zero allocations on the non-blocking-drop path (verify with `go test -benchmem` if curious).

---

## §7 Phase 5 — HTTP API

### Goal
REST endpoints, long-poll snapshot, SSE stream, embedded UI.

### Files to create
- `server/internal/api/handler.go`
- `server/internal/api/handler_test.go`
- `server/internal/api/ui/index.html` (placeholder for now; real UI in Phase 8)

### Endpoint table

| Method | Path | Min role | Notes |
|---|---|---|---|
| GET | `/healthz` | public | Liveness check |
| GET | `/v1/snapshot/{project}/{env}` | viewer | `If-None-Match` → 304; `?wait=N` → long-poll up to 60s |
| GET | `/v1/stream/{project}/{env}` | viewer | SSE; keep-alive ping every 20s |
| GET | `/v1/configs/{project}/{env}` | editor | List |
| PUT | `/v1/configs/{project}/{env}` | editor | Upsert (key in body) |
| DELETE | `/v1/configs/{project}/{env}/{key}` | editor | Delete |
| GET | `/v1/configs/{id}/versions` | editor | History |
| POST | `/v1/configs/{id}/rollback/{version}` | editor | Rollback |
| POST | `/v1/environments` | admin | Create env |
| GET | `/v1/projects` | admin | List projects |
| GET | `/v1/projects/{project}/environments` | admin | List envs in project |
| POST | `/v1/users` | admin | Create user (returns API key once) |
| GET | `/v1/users` | admin | List (strips API keys) |
| GET | `/v1/audit` | admin | `?project=X&limit=100` |
| GET | `/*` | public | Embedded UI |

### Long-poll logic for `/v1/snapshot/...`

```
1. Compute current snapshot.
2. If client ETag matches AND wait>0:
   - Subscribe to broker topic.
   - Wait up to min(wait, 60) seconds for an event OR ctx.Done().
   - On event: re-fetch snapshot, return 200 with new data.
   - On timeout: return 304.
3. Else if client ETag matches:
   - Return 304 immediately.
4. Else:
   - Return 200 with current snapshot + ETag header.
```

### SSE handler must

- Set headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no` (disables nginx buffering).
- Send `event: init` with the current ETag on connect.
- Forward broker events as `event: change` with JSON payload.
- Send `: ping\n\n` every 20s to keep proxies from killing the connection.
- Exit cleanly on `r.Context().Done()`.

### CORS middleware

Allow all origins for now (`Access-Control-Allow-Origin: *`). Headers must include `Authorization, Content-Type, X-API-Key, If-None-Match`. Expose `ETag` so browser clients can read it. Production tightening goes in v2.

### Value validation on upsert

```go
switch type:
case string, flag: ok
case int: strconv.ParseInt
case float: strconv.ParseFloat
case bool: value must be "true" or "false"
case json: json.Unmarshal must succeed
```

Reject with 400 if validation fails.

### Embedded UI

```go
//go:embed ui/*
var uiFS embed.FS

// In Routes():
sub, _ := fs.Sub(uiFS, "ui")
r.Handle("/*", http.FileServer(http.FS(sub)))
```

For Phase 5, drop a one-line placeholder `<h1>Configly</h1>` in `ui/index.html`. Real dashboard in Phase 8.

### Tests to write
- Snapshot returns 200 then 304 with the same ETag.
- Upsert with bad type returns 400.
- Upsert without auth returns 401.
- Upsert by viewer returns 403.
- Long-poll: PUT a change in a goroutine while another goroutine holds the poll; poll returns within 2s.
- Delete then GET returns 404 (configurable — currently we just return empty snapshot).
- Rollback restores prior value and bumps version.

### Acceptance criteria
- `go test -race ./internal/api/` passes.
- All endpoint tests use `httptest.Server`.

---

## §8 Phase 6 — Main Entry Point

### Goal
The actual binary with graceful shutdown and first-run admin bootstrap.

### Files to create
- `server/cmd/configly/main.go`

### Required behavior

1. **Flags / env**: `--addr` (default `:8080`, env `CONFIGLY_ADDR`), `--db` (default `configly.db`, env `CONFIGLY_DB`).
2. **First-run bootstrap**: if `users` table is empty, create an admin from `CONFIGLY_ADMIN_EMAIL` / `CONFIGLY_ADMIN_PASSWORD` (defaults `admin@configly.local` / `changeme`). Print the generated API key to stdout — **this is the only time it's shown.**
3. **Seed default project**: create `default` project with `dev`, `staging`, `prod` environments.
4. **HTTP server config**:
   - `ReadHeaderTimeout: 10s`
   - `WriteTimeout: 120s` (long-poll & SSE need this)
   - `IdleTimeout: 120s`
5. **Graceful shutdown** on SIGINT/SIGTERM with 10s timeout.

### Bootstrap output format

Print a clearly-bordered block to stdout. Make the API key impossible to miss:

```
============================================================
 Configly bootstrap
------------------------------------------------------------
 Admin email:    admin@configly.local
 Admin password: changeme
 Admin API key:  cfly_<hex>

 SAVE THIS API KEY. It is shown only once.
============================================================
```

### Acceptance criteria
- `go build -o /tmp/configly ./cmd/configly` produces a binary.
- First run prints the bootstrap block and creates `configly.db`.
- Second run starts silently (admin already exists).
- `curl http://localhost:8080/healthz` returns `{"status":"ok"}`.
- `curl -H "Authorization: Bearer $KEY" http://localhost:8080/v1/projects` returns `["default"]`.

### How to test
```bash
cd server
rm -f configly.db
go run ./cmd/configly &
sleep 1
curl http://localhost:8080/healthz
# Save the API key from the bootstrap output, then:
curl -H "Authorization: Bearer cfly_..." http://localhost:8080/v1/projects
kill %1
```

---

## §9 Phase 7 — Go SDK

### Goal
Zero-dependency Go client. Synchronous typed accessors, background watcher, never panics.

### Files to create
- `sdk/go/go.mod`
- `sdk/go/client.go`
- `sdk/go/client_test.go`

### Design rules

1. **`Get*(key, default)` must never block, never error, never panic.** If anything is wrong, return `default`.
2. **Snapshot is held in `atomic.Pointer[Snapshot]`** so reads are lock-free.
3. **`Start(ctx)` fetches once synchronously**, then spawns the loop.
4. **`Close()` stops the loop** — and is idempotent (`sync.Once`).
5. **Rollout uses FNV-1a → mod 100** for stable bucket assignment. Same algorithm in both SDKs so server-side targeting predictions match client behavior.
6. **HTTP client timeout = pollWaitSeconds + 10** so long-polls don't fight the timeout.
7. **On error: exponential backoff** (1s → 2s → 4s → ... cap 30s). Reset to 1s on success.

### API surface

```go
configly.New(Options{...}) (*Client, error)
c.Start(ctx) error
c.Close()
c.GetString(key, default) string
c.GetInt(key, default) int64
c.GetFloat(key, default) float64
c.GetBool(key, default) bool
c.GetJSON(key, &out) bool        // returns true on success
c.IsEnabled(key, userID, default) bool
```

### `IsEnabled` logic

```
if entry missing → return default
if type != "flag" → return value == "true"
if rollout >= 100 → return true
if rollout <= 0 → return value == "true"  // honor the raw value
if userID == "" → return value == "true"
return hashBucket(key + ":" + userID) < rollout
```

### Tests to write
- All typed accessors return correct values.
- Missing key returns default (no panic).
- `GetInt` with non-numeric value returns default.
- Rollout 30% over 10k userIDs lands between 2700-3300 enabled (uniformity check).
- Rollout 100% always enabled. Rollout 0% with value=false always disabled.
- `If-None-Match` short-circuits to one round-trip.
- `New()` with missing options returns error.

### Acceptance criteria
- `go test -race ./...` in `sdk/go/` passes.
- Zero non-stdlib imports.

---

## §10 Phase 8 — JS/TS SDK

### Goal
Browser + Node SDK with the same shape as the Go SDK.

### Files to create
- `sdk/js/package.json`
- `sdk/js/tsconfig.json`
- `sdk/js/src/index.ts`
- `sdk/js/test/client.test.js` (Node native test runner)

### Mirror the Go SDK semantics exactly

- Same `IsEnabled` algorithm (FNV-1a → mod 100 over `key:userID`). Cross-language consistency is critical here — if the Go and JS SDKs bucket users differently, rollouts become unreliable.
- Same "never throw" contract for `get*()`.
- Same options shape: `url`, `apiKey`, `project`, `environment`, optional `mode: "poll" | "sse"`, `pollWaitSeconds`, `onUpdate`, `logger`.

### Build setup

- `tsup` for dual CJS/ESM output.
- `tsconfig.json`: `target: es2020`, `module: esnext`, `moduleResolution: bundler`, `strict: true`, `lib: [es2020, dom]`.
- Bundle target: <5KB gzipped. No runtime deps.

### Polling loop

```ts
while (!stopped) {
  await fetchOnce(useWait=true);
  await sleep(retryMs + random() * 250);  // jitter
  retryMs = min(retryMs * 1.2, 5000);     // mild backoff on errors
                                          // (reset to 1s on success)
}
```

### SSE mode

If `mode === "sse"` and `EventSource` exists, use it. On `change` event, call `fetchOnce(false)` to pull the new snapshot. On error, EventSource auto-reconnects — nothing to do. If `EventSource` is undefined (older Node), log a warning and fall back to polling.

### Tests to write (Node `node:test`)
- Mocked fetch returns snapshot; `getString` returns correct value.
- Missing key returns default.
- 304 path: second fetchOnce sends `If-None-Match`; verify request header.
- Rollout uniformity check (same 30% → ~3000 of 10000 test).
- Constructor throws when `url` / `apiKey` / `project` / `environment` missing.

### Acceptance criteria
- `tsc --noEmit` clean under strict mode.
- `node --test test/*.test.js` all pass.
- `npx tsup src/index.ts --format cjs,esm --dts --minify` produces `dist/` files.

---

## §11 Phase 9 — Embedded Dashboard

### Goal
Single HTML file dashboard, served by the binary at `/`.

### File to create
- `server/internal/api/ui/index.html`

### Design choices

| Aspect | Choice |
|---|---|
| Build step | **None.** Vanilla JS in a single file. |
| Framework | None. ~600 lines of JS total. |
| Aesthetic | Refined developer-tool: dark mode, monospace, dense tables. Think Linear / Sourcegraph, not Material. |
| Fonts | JetBrains Mono (body) + Fraunces (display headings). Imported from Google Fonts. |
| State | All in `localStorage`: `configly_key`, `configly_url`, `configly_project`, `configly_env`. |
| Auth | User pastes API key once; stored in localStorage. Sent as Bearer header on every request. |
| Real-time | Same long-poll loop the SDKs use. |

### Required features (MVP)

1. **API key + server URL modal** — opens automatically if no key is saved.
2. **Project / environment dropdowns** in the header.
3. **Config table** — key, type (colored badge), value (truncated), rollout bar, version, last-updated relative time, edit + delete buttons.
4. **New config modal** — key, type select, value, rollout (only shown for `flag` type), description.
5. **Edit modal** — same form, key field disabled.
6. **Live updates** — long-poll loop with `?wait=30` keeps the table in sync.
7. **Connection status indicator** — green pulsing dot when connected; switches to "reconnecting..." on error.
8. **Toast notifications** for save / delete / errors.

### v2 features (skip for MVP, leave space in layout)
- Audit log panel
- Version history viewer with one-click rollback
- User management screen

### Color palette (dark)

```
--bg:       #0e0f12
--panel:    #15171c
--panel-2:  #1c1f26
--border:   #262a33
--text:     #e6e8ec
--muted:    #8a8f99
--accent:   #7cf0a8  (green)
--warn:     #f0c47c  (amber)
--danger:   #f07c7c  (red)
```

### Acceptance criteria
- File loads in browser without a build step.
- All UI interactions only depend on the documented REST endpoints.
- localStorage holds the API key; closing/reopening the tab doesn't require re-entry.
- Long-poll loop is visible in the network tab — requests every ~30s, mostly 304s.

### How to test
1. Run the server (Phase 6 binary).
2. Open `http://localhost:8080`.
3. Paste the bootstrap API key.
4. Create a config. Open the same page in a second browser. Confirm it appears within ~1s.

---

## §12 Phase 10 — Packaging & Distribution

### Goal
Make it trivial for someone else to run Configly.

### Files to create
- `Dockerfile`
- `docker-compose.yml`
- `.github/workflows/ci.yml`
- `.gitignore`
- `LICENSE` (MIT)
- `README.md`
- `CONTRIBUTING.md`
- `docs/architecture.md`
- `examples/go-gin/main.go`
- `examples/node-express/server.js`
- `examples/node-express/package.json`

### Dockerfile (multi-stage)

```dockerfile
FROM golang:1.22-alpine AS build
RUN apk add --no-cache build-base sqlite-dev
WORKDIR /src
COPY server/ ./server/
WORKDIR /src/server
RUN go mod download
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /out/configly ./cmd/configly

FROM alpine:3.19
RUN apk add --no-cache ca-certificates sqlite-libs
WORKDIR /data
COPY --from=build /out/configly /usr/local/bin/configly
ENV CONFIGLY_DB=/data/configly.db
ENV CONFIGLY_ADDR=:8080
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/usr/local/bin/configly"]
```

### docker-compose.yml

```yaml
services:
  configly:
    build: .
    ports: ["8080:8080"]
    volumes: ["configly-data:/data"]
    environment:
      CONFIGLY_ADMIN_EMAIL: admin@configly.local
      CONFIGLY_ADMIN_PASSWORD: ${CONFIGLY_ADMIN_PASSWORD:-changeme}
    restart: unless-stopped
volumes:
  configly-data:
```

### CI workflow

`.github/workflows/ci.yml` runs three jobs in parallel:
- **server**: `go vet ./...` + `go test -race ./...` + `go build`
- **sdk-go**: `go vet` + `go test -race` in `sdk/go/`
- **sdk-js**: `npm install` + `npx tsc --noEmit` in `sdk/js/`

### README structure

The README is the front page of your portfolio project. Aim for these sections in this order:

1. **One-line description** + tagline.
2. **Architecture diagram** (ASCII art is fine).
3. **Why** — three bullets max on what makes this different.
4. **Quick start** — Docker one-liner, then dashboard screenshot, then SDK example.
5. **Features table** — checkmark grid.
6. **API reference table** — endpoint, method, min role.
7. **Configuration** — env var table.
8. **Roadmap** — what's missing, what's planned.
9. **License** — MIT.

### Examples

Each example is a runnable mini-app:
- **`examples/go-gin/main.go`** — HTTP server that toggles its homepage based on a flag.
- **`examples/node-express/server.js`** — Express server reading typed configs.

Both should be < 80 lines and obviously demonstrate the value: change a value in the dashboard, the example responds within a second.

### Acceptance criteria
- `docker build -t configly .` succeeds.
- `docker compose up` brings up a working server.
- README renders correctly on GitHub.
- CI workflow runs on push.

---

## §13 Phase 11 — Polish & Portfolio Touches

These take an extra hour total but elevate the project from "code dump" to "this person ships":

1. **A real screenshot** in the README. Run the dashboard, populate ~6 configs of different types, take a clean screenshot.
2. **Demo GIF** (optional but high-impact) — record yourself flipping a flag in the dashboard and the terminal output of an SDK client changing behavior immediately.
3. **GitHub repo settings**:
   - Description + topics (`feature-flags`, `configuration-management`, `go`, `self-hosted`)
   - Pinned to your profile
   - About → website link if you have a demo deployment
4. **Release v0.1.0** with cross-compiled binaries:
   - `goreleaser` config to build Linux/macOS/Windows × amd64/arm64
   - Attach to a GitHub Release
5. **Publish the JS SDK** to npm (or just leave it as a GitHub package — either works for portfolio purposes).
6. **CHANGELOG.md** — even one entry signals you understand release discipline.

### Acceptance criteria for "portfolio-ready"
- The repo passes a 30-second test: a recruiter can open it, read the README, and understand what the project does without scrolling.
- The "Quick start" actually works end-to-end on a fresh machine.
- The code looks like production Go — small interfaces, clear errors, tested.

---

## §14 Future Enhancements (v2+)

In rough priority order. Each is a clean, separable PR.

1. **Postgres store adapter** — implement the `Store` interface against `pgx`. Needed for multi-instance HA. Couple it with a Redis or NATS pub/sub layer to replace the in-process SSE broker.
2. **Python SDK** — mirror the Go SDK shape. ~300 lines. Add to CI matrix.
3. **Targeting rules** — extend `Config.Rules` from "rollout %" to full predicates like `country == "US" && plan == "pro"`. Define a tiny expression language; evaluate in the SDK so the server stays user-attribute-blind.
4. **Webhook on change** — admin can register a URL; server POSTs the new snapshot when configs change.
5. **OIDC / SAML for dashboard login** — currently API-key only.
6. **Audit log UI** — table view in the dashboard.
7. **Version history UI** — diff view + rollback button per config.
8. **Helm chart** — Kubernetes-native deploy.
9. **Snapshot CDN integration** — for huge fanout, dump snapshots to S3/R2 and have SDKs read from there with periodic ETag checks against the server.

---

## §15 Reference Appendix

This appendix has the full code for the trickier pieces so you don't have to redesign them. Where line counts are large I've kept the *interesting* parts and noted what's omitted.

### A. SQLite store — snapshot cache & ETag

```go
type SQLite struct {
    db       *sql.DB
    mu       sync.RWMutex
    snapshot map[string]*model.Snapshot  // key = "project:env"
}

func (s *SQLite) Snapshot(ctx context.Context, project, env string) (*model.Snapshot, error) {
    cacheKey := project + ":" + env
    s.mu.RLock()
    if snap, ok := s.snapshot[cacheKey]; ok {
        s.mu.RUnlock()
        return snap, nil
    }
    s.mu.RUnlock()

    configs, err := s.ListConfigs(ctx, project, env)
    if err != nil { return nil, err }

    snap := &model.Snapshot{
        Project: project, Environment: env,
        Configs: make(map[string]model.Config, len(configs)),
        UpdatedAt: time.Now().UTC(),
    }
    for _, c := range configs { snap.Configs[c.Key] = c }
    snap.ETag = computeETag(snap)

    s.mu.Lock()
    s.snapshot[cacheKey] = snap
    s.mu.Unlock()
    return snap, nil
}

func (s *SQLite) invalidate(project, env string) {
    s.mu.Lock()
    delete(s.snapshot, project+":"+env)
    s.mu.Unlock()
}

func computeETag(snap *model.Snapshot) string {
    h := sha256.New()
    keys := make([]string, 0, len(snap.Configs))
    for k := range snap.Configs { keys = append(keys, k) }
    sort.Strings(keys)
    for _, k := range keys {
        c := snap.Configs[k]
        fmt.Fprintf(h, "%s=%s/%d|", k, c.Value, c.Version)
    }
    return hex.EncodeToString(h.Sum(nil))[:16]
}
```

### B. Upsert with version history (transactional)

```go
func (s *SQLite) UpsertConfig(ctx context.Context, c *model.Config, actor string) error {
    now := time.Now().UTC()
    c.UpdatedAt = now
    c.UpdatedBy = actor

    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()

    var existingID string
    var existingVersion int
    err = tx.QueryRowContext(ctx,
        `SELECT id, version FROM configs WHERE project=? AND environment=? AND key=?`,
        c.Project, c.Environment, c.Key).Scan(&existingID, &existingVersion)

    switch {
    case errors.Is(err, sql.ErrNoRows):
        c.ID = uuid.NewString()
        c.Version = 1
        _, err = tx.ExecContext(ctx,
            `INSERT INTO configs(id,project,environment,key,type,value,rollout,rules,description,version,updated_at,updated_by)
             VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
            c.ID, c.Project, c.Environment, c.Key, c.Type, c.Value,
            c.Rollout, c.Rules, c.Description, c.Version, c.UpdatedAt, actor)
    case err == nil:
        c.ID = existingID
        c.Version = existingVersion + 1
        _, err = tx.ExecContext(ctx,
            `UPDATE configs SET type=?, value=?, rollout=?, rules=?, description=?,
             version=?, updated_at=?, updated_by=? WHERE id=?`,
            c.Type, c.Value, c.Rollout, c.Rules, c.Description,
            c.Version, c.UpdatedAt, actor, c.ID)
    default:
        return err
    }
    if err != nil { return err }

    // Append history row.
    _, err = tx.ExecContext(ctx,
        `INSERT INTO config_versions(id, config_id, version, type, value, rollout, rules, updated_at, updated_by)
         VALUES (?,?,?,?,?,?,?,?,?)`,
        uuid.NewString(), c.ID, c.Version, c.Type, c.Value, c.Rollout, c.Rules, c.UpdatedAt, actor)
    if err != nil { return err }

    if err := tx.Commit(); err != nil { return err }
    s.invalidate(c.Project, c.Environment)
    return nil
}
```

### C. Long-poll handler core logic

```go
func (h *Handler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
    project := chi.URLParam(r, "project")
    env := chi.URLParam(r, "env")

    snap, err := h.Store.Snapshot(r.Context(), project, env)
    if err != nil { writeErr(w, 500, err.Error()); return }

    clientETag := r.Header.Get("If-None-Match")
    wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))

    if wait > 0 && clientETag != "" && clientETag == snap.ETag {
        if wait > 60 { wait = 60 }
        ch, cancel := h.Broker.Subscribe(sse.Topic(project, env))
        defer cancel()

        ctx, cleanup := context.WithTimeout(r.Context(), time.Duration(wait)*time.Second)
        defer cleanup()

        select {
        case <-ch:
            snap, err = h.Store.Snapshot(r.Context(), project, env)
            if err != nil { writeErr(w, 500, err.Error()); return }
        case <-ctx.Done():
            w.Header().Set("ETag", snap.ETag)
            w.WriteHeader(304)
            return
        }
    } else if clientETag == snap.ETag {
        w.Header().Set("ETag", snap.ETag)
        w.WriteHeader(304)
        return
    }

    w.Header().Set("ETag", snap.ETag)
    w.Header().Set("Cache-Control", "no-cache")
    writeJSON(w, 200, snap)
}
```

### D. SSE broker (full)

```go
type Event struct {
    Project     string `json:"project"`
    Environment string `json:"environment"`
    ETag        string `json:"etag"`
}

type subscriber struct {
    ch    chan Event
    topic string
}

type Broker struct {
    mu   sync.RWMutex
    subs map[*subscriber]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[*subscriber]struct{}{}} }

func (b *Broker) Subscribe(topic string) (<-chan Event, func()) {
    s := &subscriber{ch: make(chan Event, 8), topic: topic}
    b.mu.Lock(); b.subs[s] = struct{}{}; b.mu.Unlock()
    return s.ch, func() {
        b.mu.Lock(); delete(b.subs, s); b.mu.Unlock()
        close(s.ch)
    }
}

func (b *Broker) Publish(topic string, ev Event) {
    b.mu.RLock(); defer b.mu.RUnlock()
    for s := range b.subs {
        if s.topic != topic { continue }
        select {
        case s.ch <- ev:
        default:
            // Slow subscriber — drop.
        }
    }
}

func Topic(project, env string) string { return project + ":" + env }
```

### E. Go SDK — atomic snapshot + accessor

```go
type Client struct {
    opts   Options
    snap   atomic.Pointer[Snapshot]
    etag   atomic.Pointer[string]
    cancel context.CancelFunc
    done   chan struct{}
    once   sync.Once
}

func (c *Client) GetString(key, def string) string {
    if e, ok := c.lookup(key); ok { return e.Value }
    return def
}

func (c *Client) lookup(key string) (ConfigEntry, bool) {
    s := c.snap.Load()
    if s == nil { return ConfigEntry{}, false }
    e, ok := s.Configs[key]
    return e, ok
}

func (c *Client) IsEnabled(key, userID string, def bool) bool {
    e, ok := c.lookup(key)
    if !ok { return def }
    if e.Type != "flag" { return e.Value == "true" }
    if e.Rollout >= 100 { return true }
    if e.Rollout <= 0 { return e.Value == "true" }
    if userID == "" { return e.Value == "true" }
    return hashBucket(key+":"+userID) < e.Rollout
}

// FNV-1a → 0..99. Same algorithm in JS SDK for cross-language consistency.
func hashBucket(s string) int {
    var h uint32 = 0x811c9dc5
    for i := 0; i < len(s); i++ {
        h ^= uint32(s[i])
        h *= 0x01000193
    }
    return int(h % 100)
}
```

### F. JS SDK — matching hashBucket

```ts
function hashBucket(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0;
  }
  return h % 100;
}
```

**Cross-check:** for input `"feature.x:user-42"`, both implementations must return the same integer. Add this as a test in both SDKs to prevent drift.

---

## §16 Definition of Done

*Superseded by §22 — see the updated checklist there.*

---

## §17 Suggested execution order in your IDE

A realistic timeline assuming a few focused evenings:

| Evening | Phases | Output |
|---|---|---|
| 1 | 0, 1, 2 | Storage layer works, tests pass |
| 2 | 3, 4 | Auth + SSE working in isolation |
| 3 | 5, 6 | Server runs end-to-end, curl works |
| 4 | 7 | Go SDK + integration test against running server |
| 5 | 8 | JS SDK + browser test |
| 6 | 9 | Dashboard is live in a browser |
| 7 | 10 | Docker, README, examples |
| 8 | 11 | Polish, screenshot, release |

If you only have 1-2 evenings, ship phases 0-6 with the placeholder UI — you'll have a working, testable backend that anyone can run with `docker run`. That's already a credible portfolio piece. The dashboard and SDKs can land in v0.2.

---

## §18 Performance & Scale

This section explains *why* Configly is fast, what the real ceilings are, and how to scale beyond them. Anyone evaluating the project for portfolio purposes will ask "does it scale?" — this is the documented answer.

### The core insight: the SDK does the work, not the server

When your application code does this:

```go
if cfg.IsEnabled("whitelist_user", userID, false) { ... }
```

…it does **not** call Configly. The SDK holds the entire config snapshot in process memory. The call is:

```
your code → SDK.IsEnabled() → map lookup → return
            (no network, ~50 nanoseconds)
```

So if your service handles a billion requests/day and checks 5 flags per request, that's **5 billion flag evaluations/day with zero Configly traffic**. The server only sees traffic when:

1. A client starts up (one fetch to seed the snapshot).
2. A config actually changes (rare — humans edit configs occasionally, not constantly).
3. A 30-second long-poll times out (one request per client every 30s, returning 304 Not Modified, ~200 bytes).

**Configly is a config *distribution* system, not a request-time *evaluation* service.** Get this distinction right and you can scale to billions of evaluations without scaling Configly itself.

### Latency budget per layer

| Operation | Latency | Where |
|---|---|---|
| `cfg.GetBool("x", false)` | **~50 ns** | In-process map lookup |
| `cfg.IsEnabled("flag", userID, false)` | **~200 ns** | Map lookup + FNV-1a hash |
| Server cache hit (snapshot) | **~50 µs** | Read from in-memory map, write JSON |
| Server cache miss (cold) | **~5 ms** | SQLite query + ETag compute + cache populate |
| Long-poll wake on change | **~10 ms** | Broker fanout + JSON serialize |
| SSE notification | **~5 ms** | Broker fanout + write |

### Single-instance throughput ceilings

With the default single-instance SQLite design:

| Workload | Realistic ceiling | Bottleneck |
|---|---|---|
| Connected long-polling clients | **10,000–50,000** | File descriptors, broker map contention |
| Config writes per second | **~1,000** | SQLite WAL throughput |
| 304 responses per second | **20,000–50,000** | Header writes |
| Full snapshot fetches per second | **5,000–10,000** | JSON serialization |

**Sizing example:** a company running 5,000 service instances, each long-polling Configly, generates roughly `5000 / 30s = ~167 requests/sec` to the server at steady state. That's nothing — one Configly instance handles it.

### When you need to scale Configly itself

You hit the single-instance ceiling when:

- You have **>10,000 client instances** simultaneously connected.
- You need **multi-region** deployments (latency from APAC to a single US instance).
- You can't tolerate the **single point of failure** that one server represents.
- You write configs faster than ~1,000/sec (almost never the case for human edits).

If none of these apply, **stop optimizing — the default design is fine**. Most users will never need anything beyond `docker run`.

### Scaling path: when you need it

This is a v2 roadmap, but documenting it shows you've thought about it.

**Tier 1 — single instance** *(default; handles ~99% of use cases)*

```
┌──────────┐    ┌──────────┐
│ configly │ ←→ │ SQLite   │
│ instance │    │  + WAL   │
└──────────┘    └──────────┘
```

**Tier 2 — HA cluster** *(v2; uses Postgres adapter)*

```
       ┌──────────────┐
       │ Load Balancer│
       └──────┬───────┘
              ↓
   ┌──────────┼──────────┐
   ↓          ↓          ↓
┌─────┐   ┌─────┐    ┌─────┐
│cfgly│   │cfgly│    │cfgly│  ← stateless, horizontally scaled
└──┬──┘   └──┬──┘    └──┬──┘
   └────┬────┴────┬─────┘
        ↓         ↓
   ┌────────┐ ┌────────┐
   │Postgres│ │ Redis  │  ← Redis pub/sub replaces in-process SSE broker
   │(state) │ │(events)│     so writes wake clients on any instance
   └────────┘ └────────┘
```

Capacity: 100,000+ concurrent clients across 10 instances.

**Tier 3 — CDN-backed snapshots** *(for massive read fanout, e.g., mobile apps)*

```
   ┌──────────┐    write snapshot
   │ configly │ ──────────────────→  ┌─────────────────┐
   │ instance │                       │ S3 / R2 / GCS   │
   └────┬─────┘                       └────────┬────────┘
        │ publish change event                  │
        ↓                                       ↓
   ┌─────────┐                          ┌──────────────┐
   │  Redis  │                          │ CloudFront / │
   │ pub/sub │                          │ Cloudflare   │
   └────┬────┘                          └──────┬───────┘
        │                                       │
        │ SSE wake-up signal                    │ snapshot reads
        └───────────────┬───────────────────────┘
                        ↓
                  ┌──────────┐
                  │ SDK on   │  ← polls CDN with If-None-Match;
                  │ client   │     only hits Configly for SSE wake-ups
                  └──────────┘
```

Capacity: billions of edge requests/day. The Configly server only sees write traffic + thin wake-up signals.

### Anti-patterns (don't do this)

Documenting these in the project README helps users avoid foot-guns and shows architectural maturity.

| Anti-pattern | Why it's bad | Do this instead |
|---|---|---|
| One config key per user (e.g., `user.123.is_whitelisted = true`) | Snapshot becomes huge; defeats in-memory caching | Store the whitelist as one `json` config: `{"users": ["u1", "u2", ...]}` |
| Calling Configly's API directly on every user request | Defeats the whole architecture | Use the SDK — it caches the snapshot locally |
| Using Configly as an authorization service | Wrong tool; auth needs sub-ms latency and per-request decisions Configly can't make | Use a proper authz service (OPA, Casbin); use Configly to deploy authz *rules* |
| Polling without `If-None-Match` | Every poll downloads the full snapshot | The SDK does this automatically; if you write raw HTTP, send the ETag |
| Storing large blobs (>1MB) in a single config | Snapshot becomes slow to fetch and parse | Configs should be small. Big blobs go in S3 with a config pointing to the URL. |

### Performance benchmarks to add (Phase 11)

Add these to your `bench/` directory before tagging v0.1:

1. **SDK micro-bench**: how many `GetBool` calls per second from a single goroutine. Expected: 10M+/sec.
2. **Server throughput**: `wrk` or `vegeta` hitting `/v1/snapshot` with valid ETag. Expected: 20k+ req/sec returning 304.
3. **Concurrent long-pollers**: how many clients can hold long-poll connections simultaneously. Expected: 10k+ on a 2 vCPU box.
4. **Write fanout**: time from `PUT /v1/configs` to all 1000 connected clients seeing the change. Expected: <100ms.

These numbers in the README turn "I think it's fast" into "here's the data."

---

## §19 Open Source Principles

The project is MIT-licensed, free, and self-hosted forever. These principles guide every decision.

### What "open source" means here

- **MIT License** — anyone can use, modify, redistribute, including commercially.
- **No "open core"** — there's no paid tier with the good features locked behind it. Everything you build is free.
- **No telemetry, ever** — Configly does not phone home. It does not collect usage data. It does not check for updates against a remote server. Run it air-gapped if you want; it works.
- **No SaaS dependency** — there is no "Configly Cloud" you must use. The project is the binary you run.
- **Community contributions welcomed** — clear `CONTRIBUTING.md`, responsive issue triage, no CLA required.

### What this means for users

1. **You own your data.** Configs live in *your* SQLite file or *your* Postgres database. There's no vendor.
2. **You own your destiny.** If the project becomes unmaintained, you have the source, you can fork it, your service keeps running.
3. **No surprise charges.** No per-seat pricing, no per-flag pricing, no per-evaluation pricing.
4. **No vendor lock-in.** The HTTP API is documented; the snapshot format is plain JSON; export is a `curl` command.

### What contributors should know

The `CONTRIBUTING.md` will state:

- All contributions are MIT-licensed by submission.
- No CLA is required — Developer Certificate of Origin (sign-off in commits) is sufficient.
- Issues are triaged within a week.
- Breaking changes require a major version bump and a migration note.
- Backward compatibility for the HTTP API is preserved across minor versions.

### Sustainability

Open source projects die when maintainers burn out. Some safeguards:

- **Scope is locked.** §1 of this plan declares the non-goals. Feature requests outside scope are politely declined.
- **No premature complexity.** SQLite + single binary is the default forever, because most users only need that.
- **Documented architecture.** §18 means a new maintainer can ramp up by reading docs, not by archaeology.
- **Boring tech.** Go stdlib, chi, SQLite, plain HTML. Nothing exotic to maintain.

### What this changes in the implementation

Mostly nothing — the plan was already aligned. The few things to verify:

- [ ] `LICENSE` file is MIT, copyright "Configly contributors".
- [ ] No analytics SDK, no error reporter, no auto-update check in any binary or SDK.
- [ ] Dockerfile uses only public base images (`golang:alpine`, `alpine`).
- [ ] No proprietary dependencies in `go.mod` or `package.json` — all current deps are permissive (MIT/BSD/Apache-2).
- [ ] README clearly states "MIT licensed, free forever, self-hosted" near the top.

---

## §20 Integration Guide (every language, every stack)

This is the "any language, any architecture" promise made concrete. Add a version of this to the project's `docs/integrations.md`.

### The universal contract

To integrate Configly with *any* language or platform, you only need to implement this loop:

```
1. Authenticate:        Authorization: Bearer cfly_<your_key>
2. Fetch snapshot:      GET /v1/snapshot/{project}/{env}
                        → JSON body with {configs: {...}, etag: "..."}
3. Cache it in memory.
4. Loop forever:
   GET /v1/snapshot/{project}/{env}?wait=30
   If-None-Match: <last_etag>
   → 304: do nothing, loop again
   → 200: replace cache, loop again
5. Reads: lookup key in cached map. Never call the server on read.
```

That's the whole protocol. The Go and JS SDKs do this for you; in any other language, it's ~30 lines.

### Integration recipes

#### Python (no SDK yet — direct HTTP)

```python
import requests, threading, time

class Configly:
    def __init__(self, url, api_key, project, env):
        self.url = url.rstrip("/")
        self.headers = {"Authorization": f"Bearer {api_key}"}
        self.project, self.env = project, env
        self.snapshot, self.etag = {}, ""
        self._fetch()
        threading.Thread(target=self._loop, daemon=True).start()

    def _fetch(self, wait=0):
        h = dict(self.headers)
        if self.etag: h["If-None-Match"] = self.etag
        url = f"{self.url}/v1/snapshot/{self.project}/{self.env}"
        if wait: url += f"?wait={wait}"
        r = requests.get(url, headers=h, timeout=wait+10 if wait else 10)
        if r.status_code == 200:
            data = r.json()
            self.snapshot = data["configs"]
            self.etag = r.headers.get("ETag") or data["etag"]

    def _loop(self):
        while True:
            try: self._fetch(wait=30)
            except Exception: time.sleep(2)

    def get_bool(self, key, default=False):
        e = self.snapshot.get(key)
        return e["value"] == "true" if e else default

    def get_string(self, key, default=""):
        e = self.snapshot.get(key)
        return e["value"] if e else default
```

Usage:

```python
cfg = Configly("http://localhost:8080", "cfly_...", "default", "prod")
if cfg.get_bool("new_checkout"): ...
```

#### Java / Kotlin

```kotlin
class Configly(private val url: String, private val apiKey: String,
               private val project: String, private val env: String) {
    @Volatile private var snapshot: Map<String, JsonObject> = emptyMap()
    @Volatile private var etag = ""
    private val client = HttpClient.newHttpClient()

    fun start() {
        fetch(0); Thread { while(true) try { fetch(30) } catch(_: Exception) { Thread.sleep(2000) } }.start()
    }

    private fun fetch(wait: Int) {
        val u = "$url/v1/snapshot/$project/$env" + if (wait > 0) "?wait=$wait" else ""
        val req = HttpRequest.newBuilder(URI(u))
            .header("Authorization", "Bearer $apiKey")
            .apply { if (etag.isNotEmpty()) header("If-None-Match", etag) }
            .build()
        val res = client.send(req, HttpResponse.BodyHandlers.ofString())
        if (res.statusCode() == 200) {
            // parse JSON, update snapshot + etag
        }
    }

    fun getBool(key: String, default: Boolean) =
        snapshot[key]?.get("value")?.asString == "true"
}
```

#### Ruby

```ruby
require 'net/http'; require 'json'

class Configly
  def initialize(url, key, project, env)
    @url, @key, @project, @env = url, key, project, env
    @snapshot, @etag = {}, ''
    fetch(0)
    Thread.new { loop { begin; fetch(30); rescue; sleep 2; end } }
  end

  def fetch(wait)
    uri = URI("#{@url}/v1/snapshot/#{@project}/#{@env}#{wait > 0 ? "?wait=#{wait}" : ''}")
    req = Net::HTTP::Get.new(uri)
    req['Authorization'] = "Bearer #{@key}"
    req['If-None-Match'] = @etag unless @etag.empty?
    res = Net::HTTP.start(uri.hostname, uri.port) { |h| h.request(req) }
    if res.code == '200'
      data = JSON.parse(res.body)
      @snapshot = data['configs']
      @etag = res['ETag'] || data['etag']
    end
  end

  def get_bool(key, default=false)
    e = @snapshot[key]; e ? e['value'] == 'true' : default
  end
end
```

#### Rust, PHP, C#, Elixir, Swift, etc.

Same shape. The HTTP contract is the entire interface. Mention in the README that community ports are welcome, list any that exist.

### Integration with common architectures

| Architecture | How Configly fits |
|---|---|
| Monolith (one process) | One SDK instance at boot; reads from in-memory map forever. |
| Microservices | Each service has its own SDK instance, all reading from one Configly server. |
| Kubernetes | Run Configly as a `Deployment` with a `PersistentVolumeClaim` for SQLite (or use the Postgres adapter for HA). Expose via `Service`. |
| Serverless (Lambda, Cloud Run, Workers) | SDK fetches snapshot on cold start; in-memory cache reused for warm invocations. For very high cold-start rates, point the SDK at a CDN-backed snapshot URL (§18 Tier 3). |
| Mobile (iOS, Android, React Native, Flutter) | Direct HTTP calls to the API. Cache snapshot in app storage so the app works offline with the last-known config. |
| Browser SPA | JS SDK works directly. CORS is enabled by default. **Be aware**: any API key shipped to a browser is public. Use a viewer-scoped key with no write permissions. |
| Edge workers (Cloudflare, Vercel) | Fetch snapshot at startup; cache in worker memory. For multi-region edge, use the CDN-backed snapshot pattern. |
| Batch jobs / cron | Create a Configly client at job start, read configs, run the job, exit. No long-running connection needed. |
| CLI tools | Same as batch — one-shot fetch, done. |

### What does *not* work out of the box

Be honest about limits:

- **Sub-100ms config propagation** across continents — physics. Use SSE mode for ~10ms within a region.
- **Per-request server-side decisions** — Configly isn't an authz service. Use the SDK; do decisions client-side.
- **gRPC-only environments** — no gRPC support. Use the REST API or write a thin gateway.
- **Config values >1MB** — possible but not designed for it. Use a config to point at a URL where the blob lives.

---

## §21 Easy Setup — the 2-minute test

Anyone who lands on the repo should be able to go from "git clone" to "working dashboard" in under 2 minutes. Test this before tagging any release.

### The 30-second path (Docker)

```bash
docker run -d -p 8080:8080 -v configly-data:/data ghcr.io/yourname/configly:latest
# Wait 2 seconds for boot
docker logs $(docker ps -q --filter ancestor=ghcr.io/yourname/configly:latest) | grep "API key"
# Open http://localhost:8080
```

### The 2-minute path (from source)

```bash
git clone https://github.com/yourname/configly && cd configly
docker compose up -d
# Open http://localhost:8080
```

### The "I want a binary, not Docker" path

The release page on GitHub has pre-built binaries for Linux/macOS/Windows × amd64/arm64. Download, `chmod +x`, run:

```bash
curl -L https://github.com/yourname/configly/releases/latest/download/configly-linux-amd64 -o configly
chmod +x configly
./configly
```

### What must be true for setup to feel "easy"

These are project-quality bars. Verify each before release.

- [ ] Docker image is <30MB compressed.
- [ ] Server boots in <1 second on commodity hardware.
- [ ] Bootstrap API key is printed in an unmissable, copy-pasteable block.
- [ ] Dashboard works without any backend round-trip beyond the API.
- [ ] First-time user can create a flag and see it via `curl` within 30 seconds of installation.
- [ ] No environment variables are *required* (sensible defaults for everything).
- [ ] Error messages name the fix, not just the problem ("Set CONFIGLY_DB to a writable path" — not "open: permission denied").
- [ ] README has a screenshot of the dashboard above the fold.

### Integration "easy" bar

For someone adding Configly to their app:

- [ ] Go SDK: `go get` + 5 lines of code in `main.go` → working flag check.
- [ ] JS SDK: `npm install @configly/sdk` + 5 lines in app entry → working flag check.
- [ ] Any other language: copy the recipe from §20, paste, replace URL/key → working in 10 minutes.

If any of these take longer in practice, that's a bug in either the SDK or the docs.

---

## §22 Updated Definition of Done

The project is portfolio-ready when:

**Core functionality:**
- [ ] `docker run` works from a fresh machine.
- [ ] Dashboard renders at `localhost:8080`.
- [ ] Bootstrap API key flow works.
- [ ] Creating a flag in the dashboard updates the Go example within 2 seconds.
- [ ] Same for the Node example.

**Quality:**
- [ ] `go test -race ./...` passes in `server/` and `sdk/go/`.
- [ ] `npm test` passes in `sdk/js/`.
- [ ] CI is green on `main`.
- [ ] Benchmarks documented in `bench/README.md` (§18).

**Open source / discoverability:**
- [ ] `LICENSE` is MIT.
- [ ] `README.md` clearly states "MIT, free forever, self-hosted, no telemetry" in the first paragraph.
- [ ] No analytics, telemetry, or auto-update calls anywhere in the code.
- [ ] `CONTRIBUTING.md` invites contributors and explains the DCO sign-off.
- [ ] `docs/architecture.md` exists and is current.
- [ ] `docs/integrations.md` has recipes for at least 3 non-SDK languages.
- [ ] Screenshot in README.

**Polish:**
- [ ] v0.1.0 tag with cross-compiled binaries attached.
- [ ] GitHub repo has description, topics, pinned to your profile.
- [ ] Issue templates exist (`bug_report.md`, `feature_request.md`).

---

*End of plan.*