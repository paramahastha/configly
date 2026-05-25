# Changelog

All notable changes to Configly are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.1.0] — 2026-05-25

First public release.

### Added

**Server**
- SQLite store with WAL mode, in-memory snapshot cache, and ETag-based change detection
- Full CRUD for configs: string, int, float, bool, JSON, and feature flag types
- Version history with transactional upsert (each write appends an immutable snapshot)
- One-click rollback creates a new version with the previous value — history is never deleted
- Admin / Editor / Viewer RBAC with bcrypt password hashing and `cfly_`-prefixed API keys
- ETag long-poll endpoint (`GET /v1/snapshot?wait=30`) — changes propagate in < 1 s
- SSE stream endpoint (`GET /v1/stream`) for dashboard live updates
- Value validation on upsert (type-safe: int parses, bool must be "true"/"false", JSON must parse)
- Audit trail for every config write/delete/rollback
- First-run admin bootstrap: prints API key once, creates `default` project with `dev`/`staging`/`prod` envs
- Graceful shutdown on SIGINT/SIGTERM with 10 s drain window
- Embedded single-file dashboard served at `/`

**Dashboard**
- Dark-mode UI — no build step, no framework, no separate deploy
- Project / environment switcher
- Config table with type badges, rollout bar, version, and relative timestamps
- New / edit / delete modals with type-aware form fields
- Real-time long-poll loop (visible as mostly-304 requests in DevTools network tab)
- Connection status indicator (green pulse when live, "reconnecting…" on error)
- Toast notifications for save / delete / errors
- API key stored in `localStorage` — survives tab reloads

**Go SDK** (`sdk/go`)
- Zero non-stdlib dependencies
- Lock-free snapshot reads via `atomic.Pointer[Snapshot]`
- `GetString`, `GetInt`, `GetFloat`, `GetBool`, `GetJSON`, `IsEnabled` — never panic, never block
- `IsEnabled` uses FNV-1a → mod 100 for stable % rollout bucketing
- Background long-poll loop with exponential backoff (1 s → 30 s cap)
- `If-None-Match` ETag short-circuit — unchanged snapshot costs one 304 round-trip

**JS/TS SDK** (`sdk/js`, `@paramahastha/configly`)
- Browser + Node compatible, dual CJS/ESM output, < 5 KB gzipped
- Same `IsEnabled` FNV-1a algorithm as the Go SDK (cross-language test locks this)
- SSE mode for dashboards; falls back to polling when `EventSource` is unavailable
- Strict TypeScript, zero runtime dependencies

**Packaging**
- Multi-stage Dockerfile: < 30 MB compressed image
- `docker-compose.yml` for one-command local deploy
- GitHub Actions CI: server tests, Go SDK tests, JS SDK typecheck + tests

---

## Unreleased

### Planned for v0.2
- Postgres store adapter + Redis pub/sub (multi-instance HA)
- Python SDK

### Planned for v0.3
- Attribute-based targeting rules (`country == "US" && plan == "pro"`)

[0.1.0]: https://github.com/paramahastha/configly/releases/tag/v0.1.0
