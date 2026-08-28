# GitHub-connected deployments

Rig can deploy a selected GitHub.com repository without a user-managed checkout or installed Git CLI. The controller resolves a tracked branch to one commit, downloads a bounded snapshot, installs it as an immutable managed release, and applies the selected Compose file on the controller machine. Existing local-path applications remain supported.

## Boundaries and defaults

- GitHub.com is the only provider/host in v1.
- A GitHub App device connection and its rotating user credential stay on the controller in purpose-bound protected files. SQLite contains safe identity, status, and expiry metadata only.
- Sources are either `local` or `github`. A GitHub source binds a connection, installation ID, immutable repository ID, display owner/name, tracked branch, and normalized repository-relative Compose path.
- Automatic deployment is disabled per application by default. The relay is optional: a relay outage leaves GitHub-connected manual deployment operable.
- Docker Compose execution is disabled unless `hostd` starts with `--compose-runtime`; `--fake-runtime` remains isolated development behavior. This flag enables execution only; it does not configure GitHub or the relay.
- Elevated Compose capabilities and LAN/public port bindings require an exact administrator approval. Workspace escapes and unsupported remote resources are rejected.
- A failed deployment preserves diagnostics and release state. Rig never rolls back automatically.

V1 does not expand submodules or Git LFS, fetch Git history, deploy pull-request refs, support GitHub Enterprise Server, or target remote agents.

## Controller setup

1. Register/configure the official GitHub App with device flow, repository Contents read/metadata access, and the `push`, `installation`, and `installation_repositories` events described in [the relay runbook](relay-operations.md#github-app-prerequisites).
2. Start `hostd` with an absolute data root. GitHub connection and optional manual GitHub deployment require the public pair `--github-client-id` and `--github-app-slug`. They must be supplied together; supplying either one alone, or an invalid client ID or slug, fails startup before the controller begins serving.
3. Add `--compose-runtime` only when this controller is authorized to execute its selected application's Docker Compose workload through its local Docker endpoint. It is required to execute a manual deployment, but it neither enables GitHub connection nor automatic deployment.
4. Bootstrap and sign in as the local administrator.
5. In **Add application**, choose **GitHub**, start device authorization, complete the GitHub page, then select an accessible installation, repository, branch, and discovered Compose file.
6. Create the application, add visible/secret configuration as needed, and use **Deploy latest**. The resulting release records the resolved commit SHA, archive hash, Compose path, managed workspace state, and configuration revision without credentials.

For a GitHub-connected controller that can execute manual deployments, use the public GitHub pair with the execution flag:

```powershell
hostd serve --data-root <absolute-data-root> --github-client-id <public-client-id> --github-app-slug <github-app-slug> --compose-runtime
```

The client ID and slug are public identifiers; do not place any secret in this command.

If authorization expires or repository access is removed, reconnect before inspection/deployment. Rig returns a stable local problem code and never forwards the provider response body.

## Compose review and recovery

Before `docker compose up`, Rig renders bounded effective configuration and evaluates paths, ports, namespaces, devices, capabilities, security settings, Docker-socket access, and external binds. New approval-gated findings place the job in a waiting/attention state. Review the exact capability and scope in the dashboard, grant its persisted fingerprint, and explicitly resume.

After a failure, inspect the deployment and retained release. To recover, select a prior ready release and explicitly choose:

- **Current configuration** (default): deploy the prior source snapshot with the application's current configuration revision.
- **Original configuration**: deploy the prior source snapshot with the revision originally pinned to it.

Neither choice removes or rolls back the failed workload automatically. See [Docker Compose runtime operations](compose-runtime.md) for API automation, crash checks, and rollback guidance.

## Relay enrollment and automatic deployment

Deploy the official relay from [the relay operations runbook](relay-operations.md). The relay uses PostgreSQL-authoritative state; cloud account, DNS, region, certificates, backups, and live provisioning are operator responsibilities.

Relay-driven event delivery and controller pairing require both `--controller-relay` and `--relay-origin`. The pair is all-or-nothing: either flag alone fails startup. `--relay-origin` must be a canonical absolute HTTPS origin (host only, with no user information, path, query, fragment, noncanonical host representation, or explicit default port); an invalid origin also fails startup. Omitting both relay flags disables relay connectivity and relay event delivery, but is not a kill switch for already enabled durable auto-deploy or reconciliation; use the per-application auto-deploy control to turn off future automatic deployments, and handle any already active deployment job separately. These relay flags do not replace the GitHub pair or `--compose-runtime`: GitHub source access still needs `--github-client-id` with `--github-app-slug`, and workload execution still needs `--compose-runtime`.

To enable relay-driven event delivery and controller pairing, add the relay pair to the same controller invocation:

```powershell
hostd serve --data-root <absolute-data-root> --github-client-id <public-client-id> --github-app-slug <github-app-slug> --compose-runtime --controller-relay --relay-origin https://relay.example.invalid
```

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

Repository and hosted workflows provide deterministic validation, persistence, protocol, UI, and failure-path evidence. Hosted coverage includes Chromium execution of the embedded-hostd and GitHub source-wizard flows, Windows controller coverage, a real Linux relay Docker Compose lifecycle, Linux race tests, real-filesystem permission/no-follow tests, a native linux/amd64 relay image, and PostgreSQL integration tests; the result of each hosted run remains visible in CI. Live controller-application Docker Compose execution and live GitHub remain external promotion gates, alongside PostgreSQL restore, TLS/proxy, relay recovery/load, container hardening, multi-architecture images, SBOM/signature/provenance, additional browsers and physical devices, and assistive-technology checks listed in [TASKS.md](../TASKS.md#external-promotion-gates).
