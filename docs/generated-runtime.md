# Generated JavaScript runtime operations

The generated runtime is an opt-in, controller-local Docker execution capability for supported JavaScript and TypeScript repositories. Rig analyzes repository metadata without executing repository code, asks an administrator to accept an immutable deployment plan, then builds and runs the accepted commands only inside Linux containers.

Docker Compose remains a separate strategy. Existing Compose applications continue through the Compose executor, while generated applications are routed only to the generated executor. Rig never falls back from one strategy to the other.

## Enable the runtime

Start `hostd` with an absolute data root and the explicit flag:

```powershell
go run ./cmd/hostd serve --data-root C:\hostd\state --generated-runtime
```

```sh
go run ./cmd/hostd serve --data-root /var/lib/hostd --generated-runtime
```

Use both `--generated-runtime` and `--compose-runtime` when one controller must host both strategies. `--fake-runtime` is mutually exclusive with either real runtime. The Docker endpoint must be the local default, a local `unix:///...` socket, or a local `npipe:////./pipe/...` endpoint; TCP, HTTP(S), SSH, and `fd://` endpoints are rejected.

Generated startup requires a Linux Docker engine with memory, swap, CPU quota, PID-limit, Buildx, and BuildKit support. Startup resolves the Docker executable once, prepares controller-owned Docker and Buildx configuration directories, recovers generated artifacts and ingress, and then starts the job worker. If those boundaries cannot be established, startup fails closed.

## Supported source model

Initial inference supports:

- npm, pnpm, and Yarn lockfiles;
- Node.js 20, 22, and 24, with Node.js 24 as the pinned default when repository metadata does not select a version;
- one Node or Next.js server;
- one static Vite frontend served by Rig's managed static server;
- one static frontend plus one Node API; and
- monorepos containing those combinations.

Next.js, Vite, Express, Fastify, NestJS, Prisma, Drizzle, and Knex metadata contribute evidence. Multiple lockfiles, ambiguous roots, malformed or duplicate-key JSON, unsafe paths, unsupported workers/queues/multiple APIs, and unresolved ports or health checks return review findings instead of guesses.

Analysis excludes Git metadata, environment files, credentials, private keys, dependency directories, build output, controller state, links/reparse points, and oversized inputs. It reads bounded normalized metadata and never runs package scripts.

## Dashboard workflow

1. Add an application from a local folder or connected GitHub repository.
2. Choose **Analyze project**. For GitHub, a generated application does not need a Compose path.
3. Review **How Rig will run this app**. Normal review exposes the Build and Run commands. Package installation is inferred from the lockfile.
4. Correct a Build or Run command when inference is incomplete. Advanced settings include dependency installation, Node.js version, internal port, health path, and a detected migration.
5. Accept the setup. Acceptance uses the inspected source fingerprint, candidate digest, and expected revision so stale browser state cannot replace a newer decision.
6. If a migration is detected, review its persistent-data warning and approve it separately.
7. Open the application and choose **Deploy latest**.

Commands are non-secret configuration. Put credentials and application values in **Configuration**, not in Build, Run, or migration commands. Authenticated plan responses use `Cache-Control: no-store`.

## Immutable plans and source drift

An accepted deployment-plan revision records the strategy, detector version, structural source fingerprint, component roots and roles, package manager, Node.js version, Build and Run commands, ports, health probes, migration evidence, field provenance, canonical digest, actor, and time. In Rig-controlled durable storage, command text lives only in an application-purpose-bound protected bundle; SQLite retains metadata and digests without the commands. Authenticated plan-review responses return the commands with `Cache-Control: no-store`; execution surfaces handle them only as described below.

Bundle protection uses DPAPI and restrictive ACLs on Windows. On POSIX it uses purpose binding and `0600` files in `0700` directories, not encryption at rest; it does not protect against root or another process running as the controller user.

Every release pins both its configuration revision and deployment-plan revision. Redeploying an older release uses those original pins even after a newer plan is accepted.

Rig reanalyzes every new release:

- Ordinary source edits continue automatically when the canonical setup remains compatible.
- Changes to roots, package manager, runtime, inferred commands, ports, health checks, or migration evidence pause with `deployment_plan_review_required` before a deployment job is created.
- User overrides remain authoritative until reset, but their roots and component structure must still exist.
- Auto-deploy resumes only after the updated setup is accepted and Rig revalidates the exact GitHub SHA again.

## Build boundary

For each component Rig:

1. materializes and digest-checks an immutable release snapshot;
2. copies a sanitized context of at most 256 MiB and 20,000 entries into controller-owned protected temporary storage;
3. generates a multi-stage Containerfile outside the repository;
4. selects a Node.js base image pinned by digest;
5. runs the accepted dependency installation and Build command in a dedicated BuildKit builder; and
6. records the immutable image content ID before exporting runtime configuration.

The build context cannot be broadened by a repository `.dockerignore`. GitHub credentials, runtime secrets, application configuration, host directories, SSH agents, the Docker socket, and controller files are not supplied to the build. One build runs at a time, with a 30-minute default timeout and bounded output. Temporary material is removed on success, failure, cancellation, timeout, and restart recovery.

Build and Run commands are bounded UTF-8 strings. NUL, newlines, control characters, and excessive length are rejected. Shell operators remain supported. Dependency-install and Build commands are written to controller-owned `0600` temporary files, supplied to BuildKit as secret mounts, and removed with the rest of the build operation; their text is not placed in the Containerfile, image, or image history. Each Run or migration command is passed whole as one fixed argument to `/bin/sh -lc`, so it is present in the live Docker container configuration until that container is removed. Rig never expands a command through a Windows shell or concatenates it into a tag, path, project name, YAML document, log, job, audit, metric, or problem response.

## Runtime and blue/green replacement

Each inferred component receives its own generated image and container. A static frontend plus API therefore runs as two component containers, not one combined container. Components share the application's private network, while other applications use different networks.

Each component has stable blue and green slots. A deployment:

1. builds the new images;
2. reserves capacity for every inactive component slot;
3. runs an approved migration once, when present;
4. starts the inactive slots;
5. waits for Docker health checks;
6. atomically reloads the controller-managed Caddy route;
7. drains existing connections for 30 seconds; and
8. stops and removes the previous slots.

Neither slot publishes a host port. Caddy joins each application network separately and does not create a shared lateral-access network. If build, migration, startup, health, or route validation fails, the old slots remain active. If temporary capacity cannot be reserved, the job pauses with `insufficient_replacement_capacity`; Rig never silently chooses a downtime-producing stop/start replacement.

Runtime containers run as the non-root `node` user, drop all Linux capabilities, set `no-new-privileges`, use a read-only root filesystem plus bounded tmpfs, have no host binds or Docker socket, and use bounded CPU, memory, PIDs, file descriptors, and local logs.

## Migrations

Prisma, Drizzle, and Knex can produce a proposed deployment migration under Advanced. A migration requires approval before its first execution and whenever its command or migration evidence changes.

The migration runs as a one-off container from the new image before the new slots start. It receives only database configuration, does not receive unrelated secrets, and never triggers an automatic schema rollback. The old application remains online while the migration runs, so the old and new releases briefly share the migrated database. Use backward-compatible expand/contract migrations.

A job paused with `migration_approval_required` already exists. Approve the exact pinned plan migration, then resume that waiting job. Auto-deploy itself must not create a second job for this condition.

## Resource model

Defaults are admission limits, not guaranteed steady-state consumption:

| Workload | Default limit or reservation |
| --- | --- |
| Controller-managed BuildKit | 3 GiB memory limit, 2 GiB tmpfs state quota, 1 CPU, 512 PIDs, one parallel build |
| Controller-managed Caddy | 256 MiB memory, 1 CPU, 128 PIDs |
| Each active component | 512 MiB memory, 1 CPU, 256 PIDs, 64 MiB tmpfs, up to three 10 MiB local log files |
| Each temporary replacement component | an additional 512 MiB memory and 256 MiB disk admission reservation |
| Retained source snapshots | 1 GiB per application and 8 GiB globally by default |

A one-component application may temporarily require limits for Caddy, BuildKit, and two 512 MiB application slots during deployment. A two-component frontend/API application can require two active plus two candidate component limits. Docker, the operating system, source snapshots, image layers, and build context also consume resources. For development, treat 8 GiB system RAM with several GiB free and multiple GiB of free Docker storage as a practical floor; production sizing must be based on measured application and build use.

The current alpha uses fixed generated-runtime limits rather than per-application tuning. Free resources and resume the same waiting job when capacity admission pauses a deployment.

## Durable recovery and safe diagnostics

Generated deployment phases, active slot state, image artifacts, routes, plan pins, and release provenance are durable. On daemon restart, Rig recovers ingress before job execution, revalidates owned containers and networks, removes controller-owned temporary files, and resumes only phases that are safe to replay. A migration recorded as running is never rerun automatically after an uncertain interruption.

Deployment history, jobs, events, audits, metrics, problem responses, and ordinary logs contain stable diagnostic codes, not commands or raw Docker/build output. The protected plan bundle, authenticated no-store plan-review response, and live Docker runtime configuration contain command text only as described above. Controller-captured Docker and build output is cleared and is not persisted. Relevant recovery states include:

- `deployment_plan_review_required` — reanalyze and accept the current setup, then resume auto-deploy;
- `migration_approval_required` — approve the exact migration and resume the waiting job;
- `insufficient_replacement_capacity` — free capacity and resume the waiting job;
- `route_reconciliation_required` — Rig could not prove and persist one safe routing, drain, or finalization outcome. Retry the same waiting job so Rig can re-attest the candidate route and finish cleanup; do not create a replacement deployment; and
- stable build, runtime, health, ingress, and recovery failure codes in deployment history.

A route-reconciliation pause is deliberately resumable but not cancellable. The candidate may already be serving traffic, so cancellation cannot safely assume that deleting it is harmless. Rig preserves the pinned deployment and both slots until retry proves the route state and completes the remaining drain or finalization work.

## Verification

Before promotion, run:

```sh
go test ./...
go vet ./...
go run ./cmd/openapi-gen -check
pnpm --dir web typecheck
pnpm --dir web test
```

On a disposable Linux Docker host, also verify:

1. npm, pnpm, Yarn, Next.js, Vite, Express, Fastify, NestJS, and supported monorepo fixtures produce the expected review plan.
2. Quotes, `&&`, `$()`, `${VAR}`, backslashes, and Unicode remain one exact in-container command and never execute on the host.
3. A healthy candidate switches traffic and drains the old slot; build/start/health failure leaves the old slot serving.
4. Capacity admission pauses without stopping the old slot.
5. Migration approval is bound to the exact revision and executes once per deployment attempt.
6. Source drift pauses auto-deploy before job creation, while an unchanged plan continues without repeated approval.
7. Existing local and GitHub Compose deployments remain unchanged when both runtimes are enabled.
8. Synthetic credentials and commands are absent from SQLite, generated images and image history, logs, events, audits, metrics, problem responses, and retained temporary files after cleanup. Accepted command text remains in the purpose-bound plan bundle and, during execution, the bounded temporary BuildKit-secret and live Docker runtime surfaces described above.

Pull requests run `.github/workflows/generated-runtime-lifecycle-ci.yml` on a disposable Ubuntu Docker host. That gate starts hardened blue and green application slots through the production runtime engine, proves that traffic stays on blue until the Caddy route commit, switches traffic to green, removes the drained slot, checks cleanup, and runs the generated-runtime packages with Go's race detector. A checkout without a Docker daemon can compile and skip the opt-in live test, but cannot substitute for a passing hosted lifecycle job.
