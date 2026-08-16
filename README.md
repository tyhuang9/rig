# hostd - Milestone 1

`hostd` is a local-first deployment manager foundation. Milestone 1 provides a durable, authenticated control plane, an embedded dashboard, independent local diagnostics, and an explicitly enabled fake runtime. It does not execute Docker Compose or configure Caddy.

## Prerequisites

- Go 1.26.x
- Node.js 24 LTS
- pnpm 11.x
- Chromium installed for Playwright (`pnpm --dir web exec playwright install chromium`)

The commands below assume these tools are already on `PATH`; no machine-specific toolchain locations are required.

## Run locally

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
```

Embed the production dashboard with the script for your shell:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/embed-web.ps1
go run ./cmd/hostd --data-root .hostd-dev --fake-runtime
```

```sh
sh scripts/embed-web.sh
go run ./cmd/hostd --data-root .hostd-dev --fake-runtime
```

Open `http://127.0.0.1:7345`. The daemon prints a single-use bootstrap token through a dedicated protected-console path outside structured, request, and audit logs. Treat that console as sensitive; only the token hash is stored.

Fake runtime is fail-closed. It must be explicitly enabled and its resolved data root must either be named `.hostd-dev` or be an isolated `hostd-*` directory under the system temporary directory. It persists job progress but never executes a workload.

## Use hostctl safely

After creating the administrator in the dashboard, create a CLI session without putting credentials in process arguments. PowerShell can supply a credential object through standard input:

```powershell
$credential = Get-Credential
@{ username = $credential.UserName; passphrase = $credential.GetNetworkCredential().Password } |
  ConvertTo-Json -Compress |
  go run ./cmd/hostctl login --credentials-stdin
Remove-Variable credential

go run ./cmd/hostctl status
go run ./cmd/hostctl doctor
go run ./cmd/hostctl jobs cancel JOB_ID
```

In a POSIX shell, keep the passphrase out of shell history and pipe the JSON from shell built-ins:

```sh
printf 'Username: '
read -r username
printf 'Passphrase: '
stty -echo
read -r passphrase
stty echo
printf '\n'
printf '{"username":"%s","passphrase":"%s"}\n' "$username" "$passphrase" |
  go run ./cmd/hostctl login --credentials-stdin
unset passphrase

go run ./cmd/hostctl status
go run ./cmd/hostctl doctor
go run ./cmd/hostctl jobs cancel JOB_ID
```

`hostctl` stores the opaque session and CSRF values in the current user's config directory. Files are written atomically with mode `0600` on POSIX systems; Windows relies on the user-profile directory ACL. Use `--session-file` to choose another protected path or `--session-stdin` to avoid persistent storage. Operational failures return a nonzero exit code.

## Generate contracts

`api/openapi.yaml` is the Phase A API source of truth. A pinned repository generator produces the Go operation map consumed by the HTTP router and the TypeScript operation map consumed by the browser client:

```sh
go run ./cmd/openapi-gen
go run ./cmd/openapi-gen -check
```

Generated files are `internal/apicontract/generated.go` and `web/src/generated/api-contract.ts`. `scripts/check-generation.*` also checks route, schema, migration-mirror, and generated-artifact drift.

## Verify

```sh
go test ./...
go vet ./...
go build -o artifacts/hostd ./cmd/hostd
go build -o artifacts/hostctl ./cmd/hostctl
pnpm --dir web typecheck
pnpm --dir web test
sh scripts/check-generation.sh
sh scripts/check-embedded.sh
sh scripts/scan-log-leaks.sh
sh scripts/capture-visuals.sh
```

PowerShell equivalents are available for generation, embedding, secret scanning, and visual capture under `scripts/`.

The default `make build`, `make check-embedded`, and `make test-e2e` targets use the POSIX scripts. On Windows, use the explicit `build-windows`, `check-embedded-windows`, and `test-e2e-windows` targets when GNU Make is available, or invoke the corresponding `.ps1` scripts directly.
