# GitHub-connected deployments

Rig can deploy a selected GitHub.com repository without a user-managed checkout or installed Git CLI. The controller resolves a tracked branch to one commit, downloads a bounded snapshot, installs it as an immutable managed release, and applies the selected Compose file on the controller machine. Existing local-path applications remain supported.

## Boundaries and defaults

- GitHub.com is the only provider/host in v1.
- A GitHub App device connection and its rotating user credential stay on the controller in purpose-bound protected files. SQLite contains safe identity, status, and expiry metadata only.
- Sources are either `local` or `github`. A GitHub source binds a connection, installation ID, immutable repository ID, display owner/name, tracked branch, and normalized repository-relative Compose path.
- Automatic deployment is disabled per application by default. Manual deployment remains available when the relay is unavailable.
- Docker Compose execution is disabled unless `hostd` starts with `--compose-runtime`; `--fake-runtime` remains isolated development behavior.
- Elevated Compose capabilities and LAN/public port bindings require an exact administrator approval. Workspace escapes and unsupported remote resources are rejected.
- A failed deployment preserves diagnostics and release state. Rig never rolls back automatically.

V1 does not expand submodules or Git LFS, fetch Git history, deploy pull-request refs, support GitHub Enterprise Server, or target remote agents.

## Controller setup

1. Register/configure the official GitHub App with device flow, repository Contents read/metadata access, and the `push`, `installation`, and `installation_repositories` events described in [the relay runbook](relay-operations.md#github-app-prerequisites).
2. Start `hostd` with an absolute data root. Add `--compose-runtime` only on a controller whose local Docker endpoint and administrator trust boundary are ready.
3. Bootstrap and sign in as the local administrator.
4. In **Add application**, choose **GitHub**, start device authorization, complete the GitHub page, then select an accessible installation, repository, branch, and discovered Compose file.
5. Create the application, add visible/secret configuration as needed, and use **Deploy latest**. The resulting release records the resolved commit SHA, archive hash, Compose path, managed workspace state, and configuration revision without credentials.

If authorization expires or repository access is removed, reconnect before inspection/deployment. Rig returns a stable local problem code and never forwards the provider response body.

## Compose review and recovery

Before `docker compose up`, Rig renders bounded effective configuration and evaluates paths, ports, namespaces, devices, capabilities, security settings, Docker-socket access, and external binds. New approval-gated findings place the job in a waiting/attention state. Review the exact capability and scope in the dashboard, grant its persisted fingerprint, and explicitly resume.

After a failure, inspect the deployment and retained release. To recover, select a prior ready release and explicitly choose:

- **Current configuration** (default): deploy the prior source snapshot with the application's current configuration revision.
- **Original configuration**: deploy the prior source snapshot with the revision originally pinned to it.

Neither choice removes or rolls back the failed workload automatically. See [Docker Compose runtime operations](compose-runtime.md) for API automation, crash checks, and rollback guidance.

## Relay enrollment and automatic deployment

Deploy the official relay from [the relay operations runbook](relay-operations.md). The relay uses PostgreSQL-authoritative state; cloud account, DNS, region, certificates, backups, and live provisioning are operator responsibilities.

In the dashboard **Relay management** panel:

1. Start enrollment for the selected GitHub installation/repository and complete the canonical GitHub OAuth/PKCE authorization.
2. Poll or resume enrollment until the controller binding becomes ready.
3. Enable automatic deployment on an eligible GitHub-source application.

Controllers connect outbound over authenticated WSS. A relay desired-source envelope is acknowledged only after it is durable in controller SQLite. The controller then resolves the branch's current head using its own GitHub credential. Several offline/intermediate pushes therefore converge to the newest head instead of deploying every commit. A push during active deployment schedules at most one follow-up when the head changed.

Automatic deployment pauses after failure, missing configuration, lost source access, or a new approval requirement. Resume through the administrator action or a later head that satisfies the approved policy. Manual deployment remains usable during a relay outage.

Use Relay management to remove a binding or perform two-phase controller-key rotation. Do not delete the old private key until the new key has authenticated and finalization is durable.

## Acceptance checklist

Run these checks in a disposable staging environment with synthetic credentials and secrets:

1. Deploy a private repository with no Git executable and no checkout outside Rig's data root.
2. Disconnect the controller, push several commits, reconnect, and verify one deployment converges to the current branch head.
3. Add a privileged capability or public port and verify the job waits before Compose mutation.
4. Remove GitHub access and verify a sanitized access-lost state with no deployment attempt.
5. Fail health/application startup, verify there is no automatic rollback, then explicitly deploy a prior release with current and original configuration.
6. Open and deploy an existing local-source application to verify backfill compatibility.
7. Scan controller/relay databases, protected-file metadata, jobs, events, audits, logs, metrics, problem responses, image layers, and PostgreSQL backups for synthetic GitHub token prefixes and submitted secrets. Relay storage must also exclude source archives, Compose/configuration documents, application names, and raw webhook bodies.

Repository tests cover the deterministic validation, persistence, protocol, UI, and failure paths. Live GitHub, Docker, PostgreSQL, TLS/proxy, load, Linux permission/race, multi-architecture image, browser/device, and assistive-technology checks remain promotion gates and are listed in [TASKS.md](../TASKS.md#external-promotion-gates).
