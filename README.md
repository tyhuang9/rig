# hostd

`hostd` is a local-first deployment manager with a durable authenticated control plane, independent local diagnostics, an explicitly enabled development fake runtime, and an opt-in controller-local Docker Compose runtime. Runtime execution is disabled by default. Caddy configuration is not implemented.

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
go run ./cmd/hostd serve --data-root .hostd-dev --fake-runtime
```

```sh
sh scripts/embed-web.sh
go run ./cmd/hostd serve --data-root .hostd-dev --fake-runtime
```

Open `http://127.0.0.1:7345`. The daemon prints only the path to an owner-protected bootstrap file; it never writes the token to process output or logs. Read it explicitly with the printed path, then paste the returned token into the dashboard:

```powershell
go run ./cmd/hostctl bootstrap-token --file .\.hostd-dev\bootstrap-token.secret
```

```sh
go run ./cmd/hostctl bootstrap-token --file ./.hostd-dev/bootstrap-token.secret
```

The file is atomic and `0600` on POSIX, current-user DPAPI-encrypted on Windows, and removed after successful bootstrap, expiry, or a clean daemon shutdown. Only the token hash is stored in SQLite.

Phase A accepts only an explicit loopback IP literal for `--listen`, such as `127.0.0.1:7345` or `[::1]:7345`. Wildcard addresses, LAN addresses, empty hosts, and hostnames are rejected before the data root is created. This enforced local-only HTTP boundary is why the session cookie is intentionally not marked `Secure` in Phase A.

Fake runtime is fail-closed. It must be explicitly enabled and its resolved data root must either be named `.hostd-dev` or be an isolated `hostd-*` directory under the system temporary directory. It persists job progress but never executes a workload.

The real Docker Compose runtime requires `--compose-runtime`, is mutually exclusive with the fake runtime, and accepts only a controller-local Docker endpoint. The authenticated API and embedded dashboard expose deployment history, releases, exact runtime approvals, waiting-job resume, and explicit prior-release recovery. See [Docker Compose runtime operations](docs/compose-runtime.md) for security boundaries, timeout flags, dashboard and API verification, crash recovery, and disable/rollback guidance.

Applications can continue to use a local source path or connect to a selected GitHub.com repository without a user-managed checkout or installed Git CLI. GitHub credentials remain in controller-protected files, releases are immutable commit snapshots, and automatic deployment is disabled per application by default. See [GitHub-connected deployments](docs/github-connected-deployments.md) for the controller workflow, supported source model, relay enrollment, failure behavior, and acceptance checklist.

The separately deployable GitHub webhook relay has an immutable, non-root OCI build, PostgreSQL Compose examples, a strict HTTPS health probe, and production operations guidance. The dashboard exposes controller enrollment, binding removal, key rotation, and relay recovery; cloud account, DNS, region, and live provisioning remain operations work. See [Official webhook relay operations](docs/relay-operations.md) for TLS/proxy modes, protected configuration, backup/recovery, monitoring, rollback, and staging verification.

## Use the operator terminal

With the daemon running, start the interactive terminal application in another terminal:

```sh
go run ./cmd/hostd
```

Use `hostd ui --endpoint URL --session-file PATH` to override the controller or protected session file. Add `--accessible` for a monochrome, linear primary-buffer view: it disables the alternate screen and mouse reporting, gives every auth field a persistent label, and prints command, result, and job-event entries in chronological text. It remains an interactive terminal program, so screen-reader behavior depends on the terminal emulator; for fully line-oriented scripting or an assistive setup that cannot reliably follow terminal redraws, use the equivalent `hostctl` commands instead. A non-interactive no-argument invocation exits with guidance instead of waiting for input. `hostd serve` is the daemon command; flag-led invocations remain compatible for one release and print a deprecation warning.

The terminal application handles first-administrator bootstrap and login with masked fields. Its command bar accepts `/help`, `/status`, `/doctor`, application selection and lifecycle commands, machine and job inspection, job following, cancellation, and resume. Mutations require an inline confirmation. Escape cancels a confirmation or stops following locally without cancelling the server job. Tab completes commands; arrow keys recall protected command history; Page Up/Page Down and the mouse wheel scroll the bounded transcript. Only the latest 100 accepted command strings are persisted in the current user's protected config storage—credentials, confirmation input, and transcript output are never saved.

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

`hostctl` stores the opaque session and CSRF values in the current user's config directory. Files are written atomically with mode `0600` on POSIX systems and protected with current-user Windows DPAPI on Windows; Windows never falls back to plaintext session files. Use `--session-file` to choose another protected path or `--session-stdin` to avoid persistent storage. Operational failures return a nonzero exit code.

## Generate contracts

`api/openapi.yaml` is the API source of truth. A pinned repository generator produces the Go operation map consumed by the HTTP router and the TypeScript operation map consumed by the browser client:

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
