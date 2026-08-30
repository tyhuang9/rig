# Getting started

This guide starts Rig with the development-only fake runtime. You can learn the dashboard and create an application without allowing Rig to execute Docker Compose.

## Prerequisites

- Go 1.26.x
- Node.js 24 LTS
- pnpm 11.x

Run commands from the repository root. Chromium is needed only for the Playwright browser checks.

## Build the dashboard

Install the locked frontend dependencies, create a production build, and embed the resulting static files in the Go controller.

::: code-group

```powershell [PowerShell]
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
powershell -ExecutionPolicy Bypass -File scripts/embed-web.ps1
```

```sh [POSIX shell]
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
sh scripts/embed-web.sh
```

:::

## Start a safe development controller

The fake runtime accepts only an isolated data root and never runs a workload.

::: code-group

```powershell [PowerShell]
$dataRoot = [System.IO.Path]::GetFullPath((Join-Path (Get-Location) ".hostd-dev"))
go run ./cmd/hostd serve --data-root $dataRoot --fake-runtime
```

```sh [POSIX shell]
data_root="$PWD/.hostd-dev"
go run ./cmd/hostd serve --data-root "$data_root" --fake-runtime
```

:::

Open `http://127.0.0.1:7345`. Rig intentionally rejects wildcard, LAN, hostname, and non-loopback listen addresses.

## Create the administrator

On first start, the controller prints a protected bootstrap-file path. It does not print the bootstrap token. In another terminal, read the token explicitly:

::: code-group

```powershell [PowerShell]
go run ./cmd/hostctl bootstrap-token --file (Join-Path $dataRoot "bootstrap-token.secret")
```

```sh [POSIX shell]
go run ./cmd/hostctl bootstrap-token --file "$data_root/bootstrap-token.secret"
```

:::

Paste the token into the dashboard and create the first administrator. The bootstrap file is removed after successful use, expiry, or a clean controller shutdown.

## Add a local application

1. Select **Add application**.
2. Enter a name and optional description.
3. Keep **Local folder** selected.
4. Enter an absolute folder path on the controller machine.
5. Select **Check source**, review the findings, and save the application.
6. Add visible variables and write-only secrets under **Configuration**.

Fake-runtime jobs demonstrate durable progress and history, but they do not build, start, stop, or change a workload.

## Choose the next step

- To use a repository instead of a local folder, continue to [Connect GitHub](./connect-github.md).
- Before allowing real execution, read [Docker Compose runtime operations](./compose-runtime.md) in full.
- For the terminal interface and automation commands, see the [project README](https://github.com/tyhuang9/rig#operator-and-contributor-commands).

::: danger Docker is a trust boundary
`--compose-runtime` authorizes Rig to use a controller-local Docker endpoint. Anyone who controls that endpoint can inspect or alter containers and their environment. Enable it only on a machine whose Docker administrators are trusted.
:::
