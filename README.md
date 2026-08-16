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

Open `http://127.0.0.1:7345`. The daemon writes a one-time bootstrap token through a dedicated protected-console path, outside structured/request/audit logging. Treat that console output as sensitive; it is never returned through the API and only its hash is stored in the database. On a persistent installation, restrict access to the daemon console.

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
powershell -ExecutionPolicy Bypass -File scripts/capture-visuals.ps1
```

Equivalent shell commands are in `scripts/*.sh`; `make build` runs the production asset embedding workflow.
