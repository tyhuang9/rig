# Docker Compose runtime operations

The Docker Compose runtime is an opt-in, controller-local execution capability. It is disabled by default. The authenticated API is the Unit 7 administrative surface; the embedded dashboard does not expose real deployment, release, or approval controls yet.

## Enable the runtime

Start `hostd` with an absolute data root and the explicit flag:

```powershell
go run ./cmd/hostd --data-root C:\hostd\state --compose-runtime
```

```sh
go run ./cmd/hostd --data-root /var/lib/hostd --compose-runtime
```

`--compose-runtime` and `--fake-runtime` are mutually exclusive. Omitting both starts no job worker. The real runtime accepts only the local Docker default, a local `unix:///...` socket, or a local `npipe:////./pipe/...` endpoint. TCP, HTTP(S), SSH, and `fd://` endpoints are rejected.

The bounded runtime flags and defaults are:

| Flag | Default | Accepted range |
| --- | ---: | ---: |
| `--compose-config-timeout` | `30s` | `1s` to `5m` |
| `--release-workspace-per-app-bytes` | `1073741824` (1 GiB) | 1 MiB to 1 TiB; must not exceed the global quota |
| `--release-workspace-global-bytes` | `8589934592` (8 GiB) | 1 MiB to 16 TiB |
| `--compose-apply-timeout` | `15m` | `1s` to `2h` |
| `--compose-wait-timeout` | `2m` | `1s` to `1h` |

The apply timeout must be greater than the health-wait timeout.

## Security and failure model

- `hostd` validates the managed workspace and referenced local paths before normalization. Docker Compose then normalizes the source with bounded output; policy evaluates that effective JSON before `up`.
- The inspected effective JSON is written to current-user-protected temporary storage and is the only Compose file used by `up`.
- Runtime environment and normalized Compose files are deleted on success, rejection, failure, cancellation, and restart recovery. Secret-bearing in-memory buffers are cleared after use.
- Protected temporary deletion does not make container environment values inaccessible to Docker administrators. Anyone who controls the Docker daemon can inspect container configuration and environment. Grant Docker administration only to trusted administrators.
- Named volumes, workspace-local builds and binds, and loopback-only published ports are allowed. Host-level namespaces, privileged services, devices and GPUs, added capabilities, unconfined security, Docker socket access, external binds, and non-loopback or unspecified ports require an exact stored approval when supported. Escapes, remote resources, and unsafe or unknown shapes are rejected.
- Approval records bind the application, policy version, capability, canonical scope, and SHA-256 fingerprint. Grant requests accept only a fingerprint from a persisted `approval_required` finding. An approval cannot be revoked while a matching deployment is applying or waiting for health.
- Values originating from protected configuration secrets are tracked through policy evaluation. If one appears in a policy-bearing scope, Rig stores only a revision-and-key-derived placeholder and rejects the deployment; it never creates an approval for that finding. This check is intentionally conservative, so short or common secret values can reject an otherwise safe scope. Rotate or replace that secret value rather than approving the finding.
- `docker compose up -d --wait` never triggers an automatic `down` or rollback. Failed or unhealthy deployments may leave partial containers, volumes, and networks for diagnosis. Releases and deployment history are preserved.
- Cancellation and timeout first stop the entire Compose process tree, then hard-kill it if necessary. Each termination wait is bounded; if the operating system does not reap the process, Rig returns a stable process-termination failure without reading buffers that the process may still own.
- Durable job events, deployment history, and operator-facing problems contain only bounded phases and stable codes. Configuration failures distinguish `compose_config_timeout`, `compose_config_output_truncated`, and `compose_config_invalid`; mutation failures distinguish `compose_apply_timeout`, `compose_apply_output_truncated`, and `compose_apply_failed`. Provider bodies and Compose stdout/stderr are never persisted or returned.
- On restart, exact owned temporary operation directories are removed. Deployments interrupted in preparing, applying, or waiting-for-health become failed with `daemon_restarted`; intentionally paused deployments remain `needs_attention`. Running jobs become interrupted; jobs waiting for user approval remain paused.
- GitHub deployments do not require Git or a user-managed checkout. Rig downloads a pinned archive through the provider API and records its immutable release provenance.
- A prior release can deploy its `original` configuration revision or the application's `current` revision. The actual revision used is stored on the deployment independently from the release's original revision. Local sources are copied into bounded retained managed snapshots before inspection and deployment, so later edits cannot change the release being applied.

## Authenticated PowerShell verification

These steps assume an administrator has already completed bootstrap and the daemon is listening on the default loopback address. They keep the passphrase out of command arguments.

```powershell
$base = 'http://127.0.0.1:7345/api/v1'
$web = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$credential = Get-Credential
$loginBody = @{
  username = $credential.UserName
  passphrase = $credential.GetNetworkCredential().Password
} | ConvertTo-Json -Compress
$login = Invoke-RestMethod -Method Post -Uri "$base/auth/sessions" -WebSession $web -ContentType 'application/json' -Body $loginBody
Remove-Variable credential, loginBody
$csrf = $login.csrfToken
$mutationHeaders = @{ 'X-CSRF-Token' = $csrf; 'Idempotency-Key' = [guid]::NewGuid().ToString() }
$status = Invoke-RestMethod -Uri "$base/system/status" -WebSession $web
if (-not $status.capabilities.composeRuntime) { throw 'Compose runtime is not enabled' }
$apps = Invoke-RestMethod -Uri "$base/apps" -WebSession $web
$appId = $apps.items[0].id
```

Queue the latest source with the current application configuration, then inspect its job and deployment history:

```powershell
$latest = Invoke-RestMethod -Method Post -Uri "$base/apps/$appId/deployments" -WebSession $web -Headers $mutationHeaders
$job = Invoke-RestMethod -Uri "$base/jobs/$($latest.job.id)" -WebSession $web
$deployments = Invoke-RestMethod -Uri "$base/apps/$appId/deployments" -WebSession $web
$deployments.items | Format-Table id,status,releaseId,configurationMode,actualConfigurationRevisionNumber,diagnosticCode
```

Cancel a queued or active job with its authenticated mutation endpoint:

```powershell
$cancelled = Invoke-RestMethod -Method Post -Uri "$base/jobs/$($latest.job.id)/cancel" -WebSession $web -Headers @{ 'X-CSRF-Token' = $csrf }
```

Cancellation records the job, and any deployment already linked to it, as cancelled and cleans owned temporary files. It does not run `docker compose down` or remove partial containers. A job interrupted by daemon shutdown is not resumable through the resume endpoint: submit a new deployment request instead. Reusing the same idempotency key returns the original job, so use a new key when an interrupted deployment must be retried. `POST /jobs/{jobId}/resume` is only for a job in `waiting_user`.

List releases and redeploy one ready release using either its original configuration or the current application configuration:

```powershell
$releases = Invoke-RestMethod -Uri "$base/apps/$appId/releases" -WebSession $web
$releaseId = ($releases.items | Where-Object workspaceState -eq 'ready' | Select-Object -First 1).id
$priorHeaders = @{ 'X-CSRF-Token' = $csrf; 'Idempotency-Key' = [guid]::NewGuid().ToString() }
$prior = Invoke-RestMethod -Method Post -Uri "$base/apps/$appId/releases/$releaseId/deployments" -WebSession $web -Headers $priorHeaders -ContentType 'application/json' -Body '{"configurationMode":"original"}'
# Repeat with a new Idempotency-Key and {"configurationMode":"current"} after the first job is terminal.
```

If a job pauses with `status=waiting_user`, obtain the exact finding from deployment history, grant it, and explicitly resume the same job:

```powershell
$deployments = Invoke-RestMethod -Uri "$base/apps/$appId/deployments" -WebSession $web
$paused = $deployments.items | Where-Object jobId -eq $prior.job.id | Select-Object -First 1
$finding = $paused.findings | Where-Object disposition -eq 'approval_required' | Select-Object -First 1
$approvalBody = @{ fingerprint = $finding.fingerprint } | ConvertTo-Json -Compress
$approval = Invoke-RestMethod -Method Post -Uri "$base/apps/$appId/runtime-approvals" -WebSession $web -Headers @{ 'X-CSRF-Token' = $csrf } -ContentType 'application/json' -Body $approvalBody
$resumed = Invoke-RestMethod -Method Post -Uri "$base/jobs/$($prior.job.id)/resume" -WebSession $web -Headers @{ 'X-CSRF-Token' = $csrf }
$approvalHistory = Invoke-RestMethod -Uri "$base/apps/$appId/runtime-approvals" -WebSession $web
```

After no matching deployment is applying or waiting for health, revoke the approval:

```powershell
Invoke-RestMethod -Method Delete -Uri "$base/apps/$appId/runtime-approvals/$($approval.approval.id)" -WebSession $web -Headers @{ 'X-CSRF-Token' = $csrf }
```

## Authenticated curl verification

This equivalent flow requires `curl` and `jq` and assumes bootstrap is complete:

```sh
base=http://127.0.0.1:7345/api/v1
printf 'Username: '; read -r username
printf 'Passphrase: '; stty -echo; read -r passphrase; stty echo; printf '\n'
login=$(jq -nc --arg u "$username" --arg p "$passphrase" '{username:$u,passphrase:$p}' | curl -fsS -c hostd.cookies -H 'Content-Type: application/json' --data-binary @- "$base/auth/sessions")
unset passphrase
csrf=$(printf '%s' "$login" | jq -r .csrfToken)
app_id=$(curl -fsS -b hostd.cookies "$base/apps" | jq -r '.items[0].id')
latest=$(curl -fsS -b hostd.cookies -H "X-CSRF-Token: $csrf" -H "Idempotency-Key: manual-latest-$(date +%s)" -X POST "$base/apps/$app_id/deployments")
job_id=$(printf '%s' "$latest" | jq -r .job.id)
curl -fsS -b hostd.cookies "$base/jobs/$job_id" | jq .
curl -fsS -b hostd.cookies -H "X-CSRF-Token: $csrf" -X POST "$base/jobs/$job_id/cancel" | jq .
curl -fsS -b hostd.cookies "$base/apps/$app_id/deployments" | jq .
curl -fsS -b hostd.cookies "$base/apps/$app_id/releases" | jq .
```

Remove `hostd.cookies` after verification. Use the PowerShell examples or the OpenAPI document for prior-release, approval, and resume request bodies.

## Crash and partial-failure checks

On a disposable host, queue a deployment, terminate `hostd` while Compose is normalizing or applying, and restart it with the same data root. Verify:

1. The job is `interrupted` with `daemon_restarted`, unless it was intentionally `waiting_user`.
2. The deployment is `failed` with `daemon_restarted`, unless it was intentionally `needs_attention`.
3. Only exact owned runtime temp directories were removed from `<data-root>/runtime/compose`.
4. Existing containers and releases were not automatically removed or rolled back.
5. A retry creates or resumes only the intended job/deployment linkage and does not duplicate history for the same job.

## Disable and rollback guidance

Stop `hostd` cleanly and restart without `--compose-runtime` to disable further execution. No worker starts, and existing containers are left untouched. Disabling the flag is not a container rollback; inspect and remove workloads manually with Docker only after confirming their ownership and desired state.

The database migrations are additive and retained. Back up the data root before first enabling the runtime. Do not downgrade to an older binary against the migrated database unless that version's schema compatibility has been verified. To roll back the application binary safely, stop the daemon, preserve the data root and release history, install the previously approved binary, and keep the Compose runtime disabled until compatibility is confirmed.
