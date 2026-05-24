# Configly

> Runtime config & feature flags — self-hosted, single binary, no vendor lock-in.

**MIT licensed · free forever · no telemetry · no SaaS dependency**

---

## Architecture

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

The SDK holds the entire config snapshot **in process memory**. `GetBool` / `IsEnabled` are lock-free map lookups (~50 ns). The server only sees traffic on client startup and when a config actually changes.

---

## Why Configly?

- **Zero infrastructure overhead.** One `docker run` command, one SQLite file, nothing else.
- **Change propagates in under a second.** ETag long-polling wakes all connected SDKs the moment a config is saved.
- **Any language, 30 lines.** The HTTP API is simple enough to integrate without an SDK. Python, Ruby, Java — just copy the recipe from the docs.

---

## Quick start

### Docker (30 seconds)

```bash
docker run -d -p 8080:8080 -v configly-data:/data \
  ghcr.io/paramahastha/configly:latest

# Get the bootstrap API key from the logs:
docker logs <container-id> | grep "API key"

# Open the dashboard:
open http://localhost:8080
```

### From source

```bash
git clone https://github.com/paramahastha/configly && cd configly
docker compose up -d
open http://localhost:8080
```

### Go SDK

```go
import configly "github.com/paramahastha/configly/sdk/go"

cfg, _ := configly.New(configly.Options{
    URL:         "http://localhost:8080",
    APIKey:      os.Getenv("CONFIGLY_API_KEY"),
    Project:     "default",
    Environment: "prod",
})
cfg.Start(context.Background())
defer cfg.Close()

if cfg.IsEnabled("new_checkout", userID, false) {
    // show new checkout flow
}
```

### JS/TS SDK

```ts
import { Client } from '@configly/sdk';

const cfg = new Client({
  url: 'http://localhost:8080',
  apiKey: process.env.CONFIGLY_API_KEY!,
  project: 'default',
  environment: 'prod',
});
await cfg.start();

if (cfg.isEnabled('new_checkout', userId, false)) {
  // show new checkout flow
}
```

---

## Features

| Feature | Status |
|---|---|
| Runtime config (string, int, float, bool, JSON) | ✅ |
| Feature flags with % rollout | ✅ |
| ETag long-poll (sub-second propagation) | ✅ |
| SSE live updates | ✅ |
| Version history + one-click rollback | ✅ |
| Admin / Editor / Viewer RBAC | ✅ |
| Audit trail | ✅ |
| Embedded web dashboard | ✅ |
| Go SDK (zero deps) | ✅ |
| JS/TS SDK (browser + Node) | ✅ |
| Single binary, SQLite by default | ✅ |
| Docker image < 30 MB | ✅ |
| Postgres adapter | 🔜 v0.2 |
| Python / Ruby SDKs | 🔜 community |
| Targeting rules (attribute-based) | 🔜 v0.3 |

---

## API reference

| Method | Path | Min role | Notes |
|---|---|---|---|
| GET | `/healthz` | public | Liveness check |
| GET | `/v1/snapshot/{project}/{env}` | viewer | `If-None-Match` → 304; `?wait=N` long-poll |
| GET | `/v1/stream/{project}/{env}` | viewer | SSE stream |
| GET | `/v1/configs/{project}/{env}` | editor | List configs |
| PUT | `/v1/configs/{project}/{env}` | editor | Upsert |
| DELETE | `/v1/configs/{project}/{env}/{key}` | editor | Delete |
| GET | `/v1/configs/{id}/versions` | editor | Version history |
| POST | `/v1/configs/{id}/rollback/{version}` | editor | Rollback |
| POST | `/v1/environments` | admin | Create environment |
| GET | `/v1/projects` | admin | List projects |
| GET | `/v1/projects/{project}/environments` | admin | List environments |
| POST | `/v1/users` | admin | Create user |
| GET | `/v1/users` | admin | List users |
| GET | `/v1/audit` | admin | Audit log |

Auth: `Authorization: Bearer <api-key>`, `X-API-Key: <key>`, or `?key=<key>` (SSE).

---

## Configuration

| Env var | Default | Description |
|---|---|---|
| `CONFIGLY_ADDR` | `:8080` | Listen address |
| `CONFIGLY_DB` | `configly.db` | SQLite database path |
| `CONFIGLY_ADMIN_EMAIL` | `admin@configly.local` | Bootstrap admin email |
| `CONFIGLY_ADMIN_PASSWORD` | `changeme` | Bootstrap admin password |

No environment variables are required — all have sensible defaults.

---

## Roadmap

- **v0.2** — Postgres store adapter + Redis pub/sub for multi-instance HA
- **v0.3** — Attribute-based targeting rules (`country == "US" && plan == "pro"`)
- **v0.4** — Webhook on change, Python SDK, Helm chart
- **v1.0** — CDN-backed snapshot mode for mobile/edge scale

---

## License

MIT. Free forever. Self-hosted. No telemetry, ever.
