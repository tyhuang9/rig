# hostd architecture

Milestone 1 is a Go modular monolith. `hostd` owns a local SQLite control-plane database and an HTTP API bound to loopback by default. The daemon creates the local machine record, serves embedded dashboard assets, and uses durable database jobs for all application mutations. A fake runtime is available only when explicitly configured for development or tests; it never invokes Docker or a shell.

HTTP handlers are deliberately thin. They validate/authenticate requests, delegate to the `apps`, `auth`, `jobs`, and `machines` services, then serialize stable JSON or RFC 9457 problem responses. Browser mutation routes require a session cookie and matching CSRF header. Opaque session and bootstrap tokens are stored only as SHA-256 hashes; passphrases use Argon2id.

`jobs` serializes mutating work per application through a database uniqueness constraint. Jobs and their ordered append-only events survive daemon restarts. On startup, jobs that were assigned/running/waiting are explicitly interrupted and retain their checkpoint for later recovery. SSE reads persisted events first and then waits for new events; it can resume after `Last-Event-ID`.

The dashboard is a Vite React SPA compiled into `web/dist` and embedded via `go:embed`. A server fallback serves `index.html` for non-API deep links. The API source of truth is `api/openapi.yaml`; the repository-owned `cmd/openapi-gen` tool deterministically derives Go and TypeScript operation/schema-name artifacts. The HTTP router and browser client consume those artifacts, and generation checks fail on source drift.

Docker and Compose diagnostics execute only the resolved Docker client with fixed argument arrays, scoped environment values, and bounded contexts. Host memory/disk values and discovered runtime versions are persisted into the local machine record. CLI sessions are read from protected JSON files or standard input; login credentials are accepted only over standard input and never through process arguments.
