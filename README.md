# hostd

`hostd` is a local-first deployment manager with a durable authenticated control plane, independent local diagnostics, an explicitly enabled development fake runtime, an opt-in controller-local Docker Compose runtime, and an opt-in generated runtime for inferred JavaScript/TypeScript applications. Runtime execution is disabled by default. Generated applications use controller-managed Caddy ingress and blue/green replacement.

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
$dataRoot = [System.IO.Path]::GetFullPath((Join-Path (Get-Location) ".hostd-dev"))
go run ./cmd/hostd serve --data-root $dataRoot --fake-runtime
```

```sh
sh scripts/embed-web.sh
data_root="$PWD/.hostd-dev"
go run ./cmd/hostd serve --data-root "$data_root" --fake-runtime
```

Open `http://127.0.0.1:7345`. The daemon prints only the path to an owner-protected bootstrap file; it never writes the token to process output or logs. Read it explicitly with the printed path, then paste the returned token into the dashboard:

```powershell
go run ./cmd/hostctl bootstrap-token --file (Join-Path $dataRoot "bootstrap-token.secret")
```

```sh
go run ./cmd/hostctl bootstrap-token --file "$data_root/bootstrap-token.secret"
```

The file is atomic and `0600` on POSIX, current-user DPAPI-encrypted on Windows, and removed after successful bootstrap, expiry, or a clean daemon shutdown. Only the token hash is stored in SQLite.

Phase A accepts only an explicit loopback IP literal for `--listen`, such as `127.0.0.1:7345` or `[::1]:7345`. Wildcard addresses, LAN addresses, empty hosts, and hostnames are rejected before the data root is created. This enforced local-only HTTP boundary is why the session cookie is intentionally not marked `Secure` in Phase A.

Fake runtime is fail-closed. It must be explicitly enabled and its resolved data root must either be named `.hostd-dev` or be an isolated `hostd-*` directory under the system temporary directory. It persists job progress but never executes a workload.

The real Docker Compose runtime requires `--compose-runtime`, is mutually exclusive with the fake runtime, and accepts only a controller-local Docker endpoint. The authenticated API and embedded dashboard expose deployment history, releases, exact runtime approvals, waiting-job resume, and explicit prior-release recovery. See [Docker Compose runtime operations](docs/compose-runtime.md) for security boundaries, timeout flags, dashboard and API verification, crash recovery, and disable/rollback guidance.

The generated runtime requires `--generated-runtime` and supports inferred npm, pnpm, and Yarn applications on Node.js 20, 22, or 24. Rig reviews Build and Run commands before executing them, builds immutable non-root images in a bounded controller-owned BuildKit environment, and replaces healthy applications through private blue/green slots. Compose and generated runtimes may be enabled together; neither may be combined with the fake runtime. See [Generated JavaScript runtime operations](docs/generated-runtime.md) for the supported project shapes, review workflow, resource model, security boundaries, recovery states, and verification checklist.

Applications can continue to use a local source path or connect to a selected GitHub.com repository without a user-managed checkout or installed Git CLI. GitHub credentials remain in controller-protected files, releases are immutable commit snapshots, and automatic deployment is disabled per application by default. See [GitHub-connected deployments](docs/github-connected-deployments.md) for the controller workflow, supported source model, relay enrollment, failure behavior, and acceptance checklist.

The separately deployable GitHub webhook relay has an immutable, non-root OCI build, PostgreSQL Compose examples, a strict HTTPS health probe, and production operations guidance. The dashboard exposes controller enrollment, binding removal, key rotation, and relay recovery; cloud account, DNS, region, and live provisioning remain operations work. See [Official webhook relay operations](docs/relay-operations.md) for TLS/proxy modes, protected configuration, backup/recovery, monitoring, rollback, and staging verification.

## Use the operator terminal

With the daemon running, start the interactive terminal application in another terminal:

```sh
go run ./cmd/hostd
```

Use `hostd ui --endpoint URL --session-file PATH` to override the controller or protected session file. A non-interactive no-argument invocation exits with guidance instead of waiting for input. `hostd serve` is the daemon command; flag-led invocations remain compatible for one release and print a deprecation warning.

After masked first-administrator bootstrap or login, the terminal opens the application-first Switchboard. Use Up/Down or `j`/`k` to select an application, Enter to open its contextual actions, and Escape to go back. Page Up/Page Down and Home/End navigate longer application lists. Deploy, Start, Stop, Restart, server-job cancellation, logout, and quit are explicit typed flows with visible confirmation; there is no command prompt, transcript, mouse interaction, or command-history file.

Rig follows the single job created by a confirmed mutation and shows its controller-reported status, numeric progress, observed phases, and latest bounded event. Escape returns to the Switchboard while the operation and local live follow continue. Press `c` only on the progress screen to open a separate server-job cancellation confirmation. Approval-required and needs-attention jobs hand off to the web dashboard rather than exposing a context-free Resume action. Press `o` to open the validated local controller origin directly in the system browser.

Add `--accessible` for a monochrome, keyboard-only primary-buffer view with persistent authentication labels, textual selection and state, and numeric progress. It remains an interactive terminal program, so screen-reader behavior depends on the terminal emulator. Use `hostctl` for line-oriented scripting and JSON-oriented controller operations; it is not a complete interactive substitute for every Switchboard screen.

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
