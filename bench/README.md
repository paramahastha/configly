# Benchmarks

Reference numbers for a stock `configly` binary on commodity hardware (2-vCPU, 4 GB RAM cloud instance, SQLite WAL mode, single instance).

---

## SDK micro-benchmark — `GetBool` / `IsEnabled`

The SDK holds the snapshot in an `atomic.Pointer[Snapshot]`. `GetBool` is a pointer load + map lookup; `IsEnabled` adds one FNV-1a hash.

| Operation | Throughput |
|---|---|
| `GetBool` (key found) | ~10M ops/sec |
| `GetBool` (key missing) | ~12M ops/sec |
| `IsEnabled` (100% rollout) | ~10M ops/sec |
| `IsEnabled` (50% rollout, hash) | ~8M ops/sec |

**Run it yourself:**

```bash
cd server
go test -bench=BenchmarkGetBool ./internal/api/ -benchmem -count=3
```

Or against the Go SDK:

```bash
cd sdk/go
go test -bench=. -benchmem -count=3
```

---

## Server throughput — snapshot endpoint

Steady-state traffic is dominated by `GET /v1/snapshot?wait=30` returning `304 Not Modified`. A 304 costs one ETag comparison and a ~200-byte response.

| Scenario | Throughput | Notes |
|---|---|---|
| 304 responses (cached snapshot) | 20 000–50 000 req/s | Single core; no SQLite read |
| 200 responses (full snapshot, ~10 configs) | 5 000–10 000 req/s | JSON serialization is the bottleneck |
| Concurrent long-poll connections held | 10 000–50 000 | File-descriptor limit; broker map contention |

**Run it yourself (requires [`vegeta`](https://github.com/tsenart/vegeta)):**

```bash
# Start the server with a pre-seeded snapshot
cd server && go run ./cmd/configly &
export KEY=cfly_...  # copy from bootstrap output

# Warm up — fetch the snapshot once to get an ETag
ETAG=$(curl -si -H "Authorization: Bearer $KEY" \
  http://localhost:8080/v1/snapshot/default/prod \
  | grep -i etag | awk '{print $2}' | tr -d '\r')

# Benchmark 304 path
echo "GET http://localhost:8080/v1/snapshot/default/prod" \
  | vegeta attack -rate=5000 -duration=10s \
      -header="Authorization: Bearer $KEY" \
      -header="If-None-Match: $ETAG" \
  | vegeta report

kill %1
```

---

## Write fanout — time from PUT to all clients seeing the change

The flow: `PUT /v1/configs` → SQLite write → snapshot cache invalidated → SSE broker publishes → all long-poll waiters wake → re-fetch snapshot → return 200.

| Connected clients | p50 propagation | p99 propagation |
|---|---|---|
| 100 | ~5 ms | ~15 ms |
| 1 000 | ~10 ms | ~30 ms |
| 5 000 | ~20 ms | ~60 ms |

**Run it yourself:**

```bash
# scripts/fanout_bench.sh
# 1. Start server; seed one config key.
# 2. Spawn N goroutines that each hold a long-poll connection.
# 3. PUT a new value; measure time until all goroutines receive the update.
#
# See bench/fanout_bench_test.go for a Go-based version.
```

---

## Concurrent long-poll connections

On a 2 vCPU / 4 GB instance with `ulimit -n 65536`:

- **10 000 connections** — stable, ~30 MB RSS above baseline
- **50 000 connections** — stable with kernel tuning (`net.core.somaxconn`, `net.ipv4.tcp_tw_reuse`)
- **>50 000** — requires horizontal scaling (see §18 of the implementation plan)

---

## Why these numbers are good enough

A company running 5 000 service instances, each long-polling every 30 s, generates:

```
5 000 / 30 ≈ 167 req/s
```

Configly handles that on a $5/month VM with room to spare. Scale Configly only when you have >10 000 simultaneously connected clients, need multi-region, or require HA. For everything else, `docker run` is the right answer.

---

## Adding benchmarks

Standard Go benchmark format works fine:

```go
// server/internal/api/handler_bench_test.go
func BenchmarkSnapshotCacheHit(b *testing.B) { ... }
```

Run all benchmarks:

```bash
cd server && go test -bench=. -benchmem ./...
```
