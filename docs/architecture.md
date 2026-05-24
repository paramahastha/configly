# Configly — Architecture

## Overview

Configly is a config distribution system, not a request-time evaluation service. The distinction matters for scale: your application code evaluates flags in process memory at nanosecond speed; the Configly server only participates when a config actually changes.

```
┌──────────────┐    long-poll / SSE     ┌─────────────────────┐
│ your app(s)  │ ◄────────────────────► │  configly server    │
│  + SDK       │     HTTP + JSON        │  (single binary)    │
└──────────────┘                        │  SQLite (WAL mode)  │
                                        └─────────────────────┘
                                                ▲
                                                │ REST + web UI
                                                ▼
                                        ┌─────────────────────┐
                                        │  dashboard / CI     │
                                        └─────────────────────┘
```

## Server internals

```
server/
├── cmd/configly/main.go          — binary entry: flags, bootstrap, graceful shutdown
└── internal/
    ├── model/model.go            — shared types (Config, User, Snapshot, …)
    ├── store/
    │   ├── store.go              — Store interface (11 method groups)
    │   └── sqlite.go             — SQLite impl + in-memory snapshot cache
    ├── auth/auth.go              — bcrypt, API key gen, HTTP middleware, RBAC
    ├── sse/broker.go             — in-process pub/sub (topic = "project:env")
    └── api/
        ├── handler.go            — chi router, all HTTP handlers
        └── ui/index.html         — embedded single-file dashboard
```

### Data flow: config read (hot path)

```
SDK.GetBool("flag", false)
  └─ atomic.Load(snap)           // lock-free, ~50 ns
       └─ map lookup             // returns immediately
```

No network. No lock contention. The snapshot lives in an `atomic.Pointer[Snapshot]`.

### Data flow: config change

```
PUT /v1/configs/default/prod
  └─ store.UpsertConfig()        // SQLite transaction: UPDATE + INSERT history row
       └─ store.invalidate()     // delete snapshot cache entry
            └─ broker.Publish()  // notify all subscribers on "default:prod"
                 └─ long-pollers wake, re-fetch snapshot, return 200
                 └─ SSE clients receive "change" event
```

### Snapshot cache

The store holds one `map[string]*Snapshot` guarded by a `sync.RWMutex`. On a cache hit (reads), only the RLock is taken — no allocation. On a miss (first read after write), the snapshot is rebuilt from SQLite and stored. Writes are O(1) invalidations.

### ETag computation

```
sha256("key=value/version|key=value/version|...")[:16]
```

Keys are sorted before hashing so the result is deterministic regardless of Go map iteration order. The truncated hex prefix is short enough to fit in a header but unique enough for correctness.

### Long-poll protocol

```
1. Compute current snapshot.
2. If client ETag matches AND wait > 0:
   - Subscribe to broker topic "project:env".
   - select { case <-ch: re-fetch; return 200 | case <-ctx.Done(): return 304 }
3. Else if ETag matches: return 304 immediately.
4. Else: return 200 with snapshot + ETag header.
```

`wait` is clamped to 60 seconds server-side. The SDK adds 10 seconds to its HTTP client timeout so the long-poll has room to breathe before the client times out.

### SSE broker

Each subscriber is a `struct { ch chan Event; topic string }`. Publish holds the RLock and does a non-blocking send to each matching subscriber. Slow subscribers are silently dropped — they catch up on the next poll. Cancel closes the channel and removes the subscriber.

## SDK design

Both SDKs (Go and JS) share these invariants:

- **Never throw / never panic on reads.** Missing key → return default.
- **Snapshot in atomic storage.** Go: `atomic.Pointer[Snapshot]`. JS: module-level variable updated under a flag.
- **FNV-1a rollout hash.** `hashBucket(key + ":" + userID) % 100`. Identical algorithm in both SDKs so a user in bucket 42 lands there regardless of which SDK is evaluating.
- **Exponential backoff on error.** 1s → 2s → 4s → … cap 30s. Reset to 1s on success.
- **`If-None-Match` on every poll.** 304 responses consume negligible bandwidth — ~200 bytes of headers.

### IsEnabled logic

```
if entry missing          → return default
if type != "flag"         → return value == "true"
if rollout >= 100         → return true
if rollout <= 0           → return value == "true"   // honor raw value
if userID == ""           → return value == "true"
return hashBucket(key + ":" + userID) < rollout
```

## Storage schema

```sql
environments(id, project, name, created_at)
configs(id, project, environment, key, type, value, rollout, rules,
        description, version, updated_at, updated_by)
config_versions(id, config_id, version, type, value, rollout, rules,
                updated_at, updated_by)
users(id, email, password_hash, role, created_at)
api_keys(id, user_id, name, prefix, key_hash, created_at, last_used_at)
audit(id, actor, action, resource, project, environment, before, after,
      created_at)
```

Versions are append-only. Rollback inserts a new version row with old values — history is never deleted.

SQLite is opened in WAL mode (`_journal_mode=WAL`) so reads never block writes.

## Performance

| Operation | Latency |
|---|---|
| `GetBool` / `GetString` | ~50 ns (in-process map lookup) |
| `IsEnabled` | ~200 ns (map lookup + FNV-1a hash) |
| Snapshot read (cache hit) | ~50 µs |
| Snapshot read (cold) | ~5 ms |
| Long-poll wake on change | ~10 ms |

A single instance handles ~10,000–50,000 concurrent long-polling clients and ~1,000 config writes/second before SQLite WAL becomes the bottleneck. For most teams, `docker run` is all you'll ever need.

See `§18 Performance & Scale` in the implementation plan for the full scaling story (Postgres + Redis adapter, CDN-backed snapshots).

## Scaling path

**Single instance (default):** SQLite + one binary. Handles ~99% of real use cases.

**HA cluster (v0.2):** Postgres store adapter + Redis pub/sub broker. Stateless server instances behind a load balancer.

**CDN-backed (v0.4):** Write snapshots to S3/R2 on every change; SDKs read from edge with `If-None-Match`. Configly server only handles write traffic + thin SSE wake-up signals.
