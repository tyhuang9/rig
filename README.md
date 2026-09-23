# Rig

Rig is a local-first deployment manager for Docker Compose applications and supported generated JavaScript or TypeScript applications. It gives one administrator a browser dashboard, an operator terminal, and a scriptable CLI while keeping the controller and its credentials on the machine that runs the workloads. Runtime execution is disabled by default; Compose and generated application runtimes are separate, opt-in controller-local capabilities.

Rig is being built to make small, self-hosted deployments understandable and recoverable:

- register an application from a local folder or a GitHub.com repository;
- keep visible configuration and write-only secrets in versioned revisions;
- inspect Compose capabilities or review an inferred generated-app plan before execution;
- retain immutable releases and deployment history;
- explicitly redeploy a prior release with its original or current configuration; and
- optionally receive GitHub push events through the separately deployed relay.

## Current boundaries

Rig is not a general-purpose cloud platform. The controller accepts local loopback connections only, and real workload execution is disabled unless it is explicitly started with `--compose-runtime` for Compose applications or `--generated-runtime` for supported JavaScript and TypeScript applications. Docker access is a high-trust boundary: a Docker administrator can inspect or alter managed workloads.

The included `--fake-runtime` is for development and testing. It records realistic job progress but never runs a workload, and cannot be combined with either real runtime. The generated runtime manages a local Caddy ingress gateway for its own blue/green application slots; it does not configure ingress for Compose applications. Rig does not target remote agents, support GitHub Enterprise Server, expand Git submodules or Git LFS, or automatically roll back a failed deployment. Full live GitHub-to-Docker execution remains a promotion check for your own environment.

## Quick start

Prerequisites: Go 1.26.x, Node.js 24 LTS, and pnpm 11.x.

Build the dashboard, embed it in the controller, and start a development-only controller:

```powershell
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
powershell -ExecutionPolicy Bypass -File scripts/embed-web.ps1
$dataRoot = [System.IO.Path]::GetFullPath((Join-Path (Get-Location) ".hostd-dev"))
go run ./cmd/hostd serve --data-root $dataRoot --fake-runtime
```

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
sh scripts/embed-web.sh
data_root="$PWD/.hostd-dev"
go run ./cmd/hostd serve --data-root "$data_root" --fake-runtime
```

Open `http://127.0.0.1:7345`. The controller prints the path to a protected bootstrap file, not the token itself. Read the token explicitly and paste it into the dashboard:

```powershell
go run ./cmd/hostctl bootstrap-token --file (Join-Path $dataRoot "bootstrap-token.secret")
```

```sh
go run ./cmd/hostctl bootstrap-token --file "$data_root/bootstrap-token.secret"
```

The fake runtime requires an isolated development data root and cannot execute the application. Follow the [Docker Compose runtime guide](docs/compose-runtime.md) before enabling real execution.

## Choose an application source

### Local folder

In **Add application**, keep **Local folder** selected, enter an absolute path on the controller machine, and run the source check. Rig snapshots local source into its managed workspace before a deployment so later edits cannot change an existing release.

### GitHub repository

GitHub sources use GitHub's device authorization flow and do not require an installed Git CLI or a user-managed checkout. Standard Rig builds enable GitHub connections through the official `rig-deployment-connector`; no GitHub flags are needed. In **Connections**, select **Connect GitHub**, complete authorization, and use **Manage repository access** to grant access. In **Add application**, choose **GitHub repository**, use the one aggregate repository picker, then select the tracked branch. For a Compose application, choose a discovered Compose file; for a generated application, continue with its build and run setup instead.

Forks and custom deployments can replace the official identity by supplying `--github-client-id` and `--github-app-slug` together. The override is atomic: do not supply only one flag. Administrators can explicitly disable the feature with `--github-connections=false`.

GitHub credentials stay in purpose-bound protected files on the controller. Do not put a client secret, private key, access token, or webhook secret in command-line flags or in the repository. See [Connect GitHub](docs/connect-github.md) for the user flow and [GitHub-connected deployments](docs/github-connected-deployments.md) for administrator setup and operational behavior.

## Choose a runtime

The Docker Compose runtime requires `--compose-runtime` and reviews effective Compose configuration before execution. The generated runtime requires `--generated-runtime` and supports inferred npm, pnpm, and Yarn applications on Node.js 20, 22, or 24. Rig reviews editable build and run settings, builds immutable non-root images in a bounded controller-owned BuildKit environment, and replaces healthy generated applications through private blue/green slots behind controller-managed Caddy ingress. Both real runtimes can be enabled together; neither can be combined with the fake runtime.

Read [Generated JavaScript runtime operations](docs/generated-runtime.md) for project support, plan review, resources, trust boundaries, recovery, and verification. [Editable deployment setup verification](docs/deployment-setup-verification.md) records its setup-specific failure behavior and verification scope.

## Safety model

- The controller serves only an explicit loopback IP address.
- Bootstrap tokens, sessions, GitHub credentials, and configuration secrets are stored through platform-specific protected-file handling; plaintext secret values are not returned after submission.
- Compose input is normalized and inspected before execution. Elevated capabilities require an exact, persisted approval; unsafe or unverifiable shapes are rejected.
- Deployment failure does not trigger an automatic `docker compose down` or rollback. Inspect the retained diagnostics before taking recovery action.
- GitHub automatic deployment is off per application by default and requires the optional relay.

## Documentation

- [Getting started](docs/getting-started.md)
- [Connect GitHub](docs/connect-github.md)
- [Docker Compose runtime operations](docs/compose-runtime.md)
- [Generated JavaScript runtime operations](docs/generated-runtime.md)
- [Editable deployment setup verification](docs/deployment-setup-verification.md)
- [GitHub-connected deployments](docs/github-connected-deployments.md)
- [Official webhook relay operations](docs/relay-operations.md)

## Operator and contributor commands

Run the keyboard-driven operator terminal against a started controller:

```sh
go run ./cmd/hostd
```

Use `hostd ui --accessible` for the monochrome primary-buffer view. Use `hostctl` for line-oriented diagnostics and automation; credentials can be supplied through standard input, and operational failures return a nonzero exit code.

Run the main verification set before submitting a change:

```sh
go test ./...
go vet ./...
go build ./cmd/hostd ./cmd/hostctl
pnpm --dir web typecheck
pnpm --dir web test
sh scripts/check-generation.sh
sh scripts/check-embedded.sh
```

PowerShell equivalents for repository scripts are available under `scripts/`.
