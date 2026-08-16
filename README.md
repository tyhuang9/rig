# hostd — Milestone 1

`hostd` is a local-first deployment manager foundation. This branch deliberately implements a durable, authenticated control plane with an explicitly enabled fake runtime. It does **not** execute Docker Compose projects yet.

## Run locally (PowerShell)

```powershell
$env:Path = 'C:\Users\huang\.codex\toolchains\go1.26.6\go\bin;C:\Users\huang\.codex\toolchains\node-v24.19.0\node-v24.19.0-win-x64;C:\Users\huang\.codex\toolchains\pnpm;' + $env:Path
pnpm --dir web install
pnpm --dir web build
pwsh -File scripts/embed-web.ps1
go run ./cmd/hostd --data-root .hostd-dev --fake-runtime
```

Open `http://127.0.0.1:7345`. The daemon prints a one-time bootstrap token only to its local stderr. Treat that local console output as sensitive; it is intentionally never returned through the API or stored in the database. On a persistent installation, restrict access to daemon logs.

## Verify

```powershell
go test ./...
go vet ./...
\# Windows PowerShell is used if pwsh is not installed.
powershell -ExecutionPolicy Bypass -File scripts/check-generation.ps1
powershell -ExecutionPolicy Bypass -File scripts/scan-log-leaks.ps1
pnpm --dir web typecheck
pnpm --dir web test
go run ./cmd/hostctl doctor
```

Equivalent shell commands are in `scripts/*.sh`; `make build` runs the production asset embedding workflow.
