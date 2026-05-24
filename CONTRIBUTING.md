# Contributing to Configly

Thank you for your interest in contributing! This is a small, focused project — contributions that stay within the stated scope are very welcome.

## Before you start

Read `§1 Vision & Decisions` in `docs/CONFIGLY_IMPLEMENTATION_PLAN.md` (or the architecture doc). Scope is intentionally locked. Feature requests outside the non-goals will be politely declined — not because they're bad ideas, but because keeping the scope tight is how the project stays maintainable.

## How to contribute

1. **Open an issue first** for anything non-trivial. Discuss the approach before writing code.
2. **Fork → branch → PR.** Branch names: `fix/short-description` or `feat/short-description`.
3. **Tests required.** New behavior without tests won't be merged.
4. **Keep it boring.** No new runtime dependencies without a very good reason. The value of zero-dep SDKs is real.

## Developer Certificate of Origin

All contributions are MIT-licensed by submission. No CLA required — a DCO sign-off in commits is sufficient:

```
git commit -s -m "feat: add thing"
```

The `-s` flag appends:

```
Signed-off-by: Your Name <you@example.com>
```

## Running the project locally

```bash
# Server (requires gcc for CGO/SQLite)
cd server
go test -race ./...
go run ./cmd/configly

# Go SDK
cd sdk/go
go test -race ./...

# JS/TS SDK
cd sdk/js
npm install
npm test
npx tsc --noEmit
```

## Code style

- Go: `gofmt` + `go vet`. No linter config to fight with.
- JS/TS: `tsc --strict --noEmit` must pass.
- Comments only when the **why** is non-obvious.

## Breaking changes

- HTTP API: backward-compatible across minor versions. Breaking changes require a major version bump and a migration note in the PR.
- SDK API: same policy.

## Issue triage

Issues are triaged within a week. If you don't hear back, a friendly ping is welcome.
