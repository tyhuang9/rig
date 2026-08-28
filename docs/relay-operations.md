# Official webhook relay operations

This runbook covers the independently deployable Rig GitHub webhook relay and its authoritative PostgreSQL state. The supplied production Compose contract always uses TLS at the relay listener, including when a reverse proxy is in front of it. Plain HTTP is permitted only for explicit loopback development outside these production Compose files.

Cloud account, DNS, region, and live provisioning are outside this repository delivery. The controller and dashboard implement enrollment, subscription synchronization, the durable local source-event inbox, latest-head automatic deployment, binding removal, and key rotation; their end-to-end workflow is documented in [GitHub-connected deployments](github-connected-deployments.md).

## Security and data boundaries

- PostgreSQL stores relay topology, enrollment status, delivery generations, acknowledgements, and recovery state. It must be backed up and access-controlled as production data.
- The relay must not receive or retain source archives, Compose documents, application names, variables, deployment configuration, or GitHub user tokens. Its logs and metrics use closed labels and codes; do not add request bodies, URLs, identifiers, network addresses, or provider response bodies to logging.
- GitHub user OAuth material used during enrollment is discarded after authorization. Controller Ed25519 identity is separate from the relay's GitHub App credentials.
- All credential material is supplied through protected files. Do not place passwords, private keys, webhook secrets, enrollment keys, or DSNs in Compose environment values, image arguments, commands, labels, or logs.
- PostgreSQL has no published host port. The `relay-database` network is internal. The relay is non-root, has a read-only root filesystem, drops all capabilities, forbids privilege gain, and has bounded process, memory, CPU, restart, and log settings.

## GitHub App prerequisites

Create a GitHub.com App using GitHub's [registration guidance](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app). V1 does not support GitHub Enterprise Server.

Configure all of the following before starting the relay:

1. Set the public homepage/origin to the exact HTTPS origin in `HOSTD_RELAY_PUBLIC_BASE_URL`. It must have no path, query, fragment, or user information.
2. Set the OAuth callback URL to `https://<public-origin>/v1/github/callback`.
3. Set the webhook URL to `https://<public-origin>/v1/github/webhook` and create a high-entropy webhook secret. GitHub documents the exact raw-body HMAC requirement in [Validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries).
4. Grant repository `Contents: read` and metadata access. Subscribe to `push`, `installation`, and `installation_repositories` events. Follow GitHub's [least-permission guidance](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app).
5. Record the App ID, client ID, client secret, and one active RSA App private key. Never copy these values into a ticket, terminal transcript, Compose file, or environment file.
6. Install the App only on the intended organizations/repositories. Enrollment re-verifies user, installation, and immutable repository access; it does not trust an installation ID from a callback by itself.

The public proxy should route only required relay application paths. Keep `/metrics`, `/healthz`, and `/readyz` on an operator/monitoring path that is not internet-accessible. Preserve the raw webhook request body and required GitHub headers exactly. Preserve WebSocket upgrade headers for `/v1/controllers/connect`.

## Immutable image inputs and build

The Dockerfile currently pins these multi-architecture indexes, resolved from the official registries on 2026-08-25:

- `docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e` (Dockerfile frontend)
- `docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514`
- `gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7`
- Compose uses `docker.io/library/postgres:18.6-bookworm@sha256:1c59e2c3c818eaa0f0628f695b36e7c9e362d6b219b36a54a32df645cbd7e1af`.

The Go and PostgreSQL references are [Docker Official Images](https://hub.docker.com/search?image_filter=official). Distroless documents its supported multi-architecture `nonroot` tag, shell-free runtime, and [keyless signature verification](https://github.com/GoogleContainerTools/distroless#how-do-i-verify-distroless-images).

Before a build, run the deterministic package checks:

```powershell
pwsh -NoProfile -File scripts/check-relay-packaging.ps1 -SelfTest
pwsh -NoProfile -File scripts/check-relay-packaging.ps1 -BehaviorTest
```

`-BehaviorTest` uses injected metadata and Compose-renderer fakes so the no-follow, path-coupling, identity-drift, environment-validation, and effective-model rejection paths execute deterministically on Windows and Linux. Those fakes do not prove Linux `lstat`, GNU `stat`, ownership, or mode behavior. Before promotion, run the separate real-filesystem integration gate as root on an isolated Linux test host; it creates and removes only a randomized `/var/lib/rig-relay-preflight-test-*` fixture:

```sh
sudo env HOSTD_RELAY_RUN_LINUX_PREFLIGHT_TESTS=1 \
  pwsh -NoProfile -File scripts/check-relay-packaging.ps1 -LinuxIntegrationTest
```

Build through BuildKit from the repository root. Substitute controlled release metadata and a registry tag. `--push` is intentional because the deployment must consume the resulting immutable registry digest, not a mutable local tag.

```sh
docker buildx build --file deploy/relay/Dockerfile \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=RELEASE_VERSION \
  --build-arg REVISION=FULL_GIT_COMMIT \
  --build-arg CREATED=RFC3339_UTC_TIME \
  --provenance=mode=max --sbom=true \
  --tag REGISTRY/rig-relay:RELEASE_VERSION --push .

docker buildx imagetools inspect REGISTRY/rig-relay:RELEASE_VERSION
```

Record the returned `sha256` index digest in the protected deployment environment file as `HOSTD_RELAY_IMAGE=REGISTRY/rig-relay@sha256:<64 lowercase hex>`. Scan the image and attached SBOM with the organization's approved tooling before staging.

### Updating pinned upstream images

Never replace a digest from an unauthenticated message, issue, or copied third-party page. For each update:

1. Select a supported patch tag from the official [Dockerfile frontend](https://hub.docker.com/r/docker/dockerfile), [Go image](https://hub.docker.com/_/golang), [PostgreSQL image](https://hub.docker.com/_/postgres), or [Distroless repository](https://github.com/GoogleContainerTools/distroless).
2. Resolve the multi-platform index with `docker buildx imagetools inspect IMAGE:TAG` and copy the reported index digest, not an architecture-specific child digest.
3. Verify every digest with the organization's approved Cosign trust policy before use. A successful registry digest lookup alone is not a signature. For Distroless, use its documented keyless identity:

   ```sh
   cosign verify gcr.io/distroless/static-debian13@sha256:NEW_DIGEST \
     --certificate-oidc-issuer https://accounts.google.com \
     --certificate-identity keyless@distroless.iam.gserviceaccount.com
   ```

   For `docker/dockerfile`, resolve `docker/dockerfile:TAG` from `registry-1.docker.io`, verify `docker.io/docker/dockerfile@sha256:DIGEST` with the organization-approved Docker Official Images Cosign key or certificate identity, and retain the verification bundle. If that trust policy cannot verify the digest, stop; do not update the pin based only on a tag or registry response.
4. Update the Dockerfile syntax directive, Dockerfile/Compose reference, and matching verifier constant together. Every digest must be exactly 64 lowercase hexadecimal characters. Run the verifier self-test, all Go tests, vulnerability scanning, a two-architecture build, and the staging procedure below. Review release notes and CVEs before promotion.
5. Retain the prior relay image digest and database backup until rollback evidence is complete.

## Protected configuration files

Production deployment is Linux-only and requires PowerShell 7 (`pwsh`), GNU `stat` and `id` at `/usr/bin/stat` and `/usr/bin/id`, Docker Engine, and Docker Compose v2.30.0 or newer. Windows deployment is unsupported. The one trusted deployment anchor is `/etc/rig-relay`; the only accepted environment and secret paths are `/etc/rig-relay/relay.env` and `/etc/rig-relay/secrets`.

Create the anchor and secret directory without symlinks. Keep every ancestor root-owned (or owned by the numeric deployment administrator where the preflight permits it) and not writable by group or other. The secret directory itself is always root-owned mode `0700`:

```sh
sudo install -d -o root -g root -m 0755 /etc/rig-relay
sudo install -d -o root -g root -m 0700 /etc/rig-relay/secrets
sudo install -o root -g root -m 0600 deploy/relay/.env.example /etc/rig-relay/relay.env
```

Replace the relay image placeholder with a verified immutable digest. Keep `HOSTD_RELAY_SECRET_DIRECTORY=/etc/rig-relay/secrets` unchanged; it couples the exact preflighted directory to every Compose secret source. The environment file contains non-secret settings only, is limited to 64 KiB, must be UTF-8 with LF (not CRLF), rejects unknown or duplicate keys, and must remain mode `0400` or `0600`, owned by root or the current numeric deployment administrator. Its public URL must be the canonical absolute HTTPS origin with a host and no user information, alternate representation, non-root path, query, or fragment. The GitHub client ID uses only letters, digits, dot, underscore, and hyphen; the App ID is a positive signed 64-bit decimal integer. TLS SNI is an exact DNS name, the direct publish address is exactly `127.0.0.1`, the publish port is `1..65535`, and the edge-network name is bounded to the documented safe identifier syntax.

Populate `/etc/rig-relay/secrets/` from an approved secret manager. The repository's `deploy/relay/secrets/` directory is only a deny-listed placeholder and is never a production source. Docker's official [Compose secrets documentation](https://docs.docker.com/compose/how-tos/use-secrets/) states that standalone Compose delivers a secret by bind-mounting its single source file into a Linux container. Do not rely on long-syntax `uid`, `gid`, or `mode` remapping for a file source. Source metadata is therefore part of this deployment contract.

Required files are:

| File | Required content |
| --- | --- |
| `postgres-password.txt` | PostgreSQL role password only; no CR/LF |
| `relay-postgres-dsn.txt` | `postgresql://rig_relay:<URL-encoded password>@postgres:5432/rig_relay?sslmode=disable`; no CR/LF |
| `github-client-secret.txt` | GitHub App client secret; no CR/LF |
| `github-app-private-key.pem` | active GitHub App RSA private key |
| `github-webhook-secret.txt` | configured GitHub webhook HMAC secret; no CR/LF |
| `enrollment-key.bin` | exactly 32 raw random bytes, not hex or base64; also keys privacy-preserving admission digests |
| `relay-tls-certificate.pem` | served certificate followed by intermediate chain |
| `relay-tls-private-key.pem` | matching private key |
| `relay-tls-ca.pem` | CA bundle that validates the served backend certificate |

Every single-line file in the table must contain exact secret bytes and must contain no trailing CR or LF byte. Use a secret manager's binary file-write operation or `printf %s`, never `echo`. PEM files retain their required PEM line endings. Generate `enrollment-key.bin` with a cryptographic RNG writing exactly 32 raw bytes, for example `openssl rand 32 > /etc/rig-relay/secrets/enrollment-key.bin`; do not encode it.

On Linux, create and populate the directory under `umask 077`. Make every source a regular, non-symlink file with mode `0400` or `0600`. Set `postgres-password.txt` to numeric UID/GID `999:999`, matching the PostgreSQL container. Set the other eight relay/probe-consumed sources to numeric UID/GID `65532:65532`. The relay rejects protected inputs with group/other permission bits, and both containers must be able to read their own bind-mounted sources. A root/deployment administrator should apply ownership only after writing the files:

```sh
chown 999:999 /etc/rig-relay/secrets/postgres-password.txt
chown 65532:65532 \
  /etc/rig-relay/secrets/relay-postgres-dsn.txt \
  /etc/rig-relay/secrets/github-client-secret.txt \
  /etc/rig-relay/secrets/github-app-private-key.pem \
  /etc/rig-relay/secrets/github-webhook-secret.txt \
  /etc/rig-relay/secrets/enrollment-key.bin \
  /etc/rig-relay/secrets/relay-tls-certificate.pem \
  /etc/rig-relay/secrets/relay-tls-private-key.pem \
  /etc/rig-relay/secrets/relay-tls-ca.pem
chmod 0400 /etc/rig-relay/secrets/*.txt /etc/rig-relay/secrets/*.pem /etc/rig-relay/secrets/*.bin
```

Confirm metadata without printing content:

```sh
stat -c '%u:%g %a %F %n' /etc/rig-relay/secrets/*
```

Expected: `postgres-password.txt` reports UID/GID `999:999`; every other file reports UID/GID `65532:65532`; all report mode `400` (or deliberately selected `600`) and `regular file`. Run the preflight as root or the dedicated deployment administrator after ownership is finalized. It checks exact size bounds and rejects CR/LF in single-line files:

```sh
pwsh -NoProfile -File scripts/check-relay-packaging.ps1 \
  -TrustedDeploymentAnchor /etc/rig-relay \
  -EnvironmentFile /etc/rig-relay/relay.env \
  -SecretDirectory /etc/rig-relay/secrets
```

Then test the actual Linux bind-mount metadata/readability as each container UID without printing file contents. Use only the pinned official builder image and disable networking and privileges:

```sh
docker run --rm --network none --read-only --pids-limit 32 --memory 64m --cpus 0.25 \
  --cap-drop ALL --security-opt no-new-privileges --user 999:999 \
  --mount type=bind,src="/etc/rig-relay/secrets/postgres-password.txt",dst=/run/secrets/postgres_password,readonly \
  --entrypoint /bin/sh \
  docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 \
  -ceu 'test -f /run/secrets/postgres_password; test -r /run/secrets/postgres_password; stat -c "%u:%g %a %F %n" /run/secrets/postgres_password'

docker run --rm --network none --read-only --pids-limit 32 --memory 64m --cpus 0.25 \
  --cap-drop ALL --security-opt no-new-privileges --user 65532:65532 \
  --mount type=bind,src="/etc/rig-relay/secrets/relay-postgres-dsn.txt",dst=/run/secrets/relay_postgres_dsn,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/github-client-secret.txt",dst=/run/secrets/github_client_secret,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/github-app-private-key.pem",dst=/run/secrets/github_app_private_key,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/github-webhook-secret.txt",dst=/run/secrets/github_webhook_secret,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/enrollment-key.bin",dst=/run/secrets/enrollment_key,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/relay-tls-certificate.pem",dst=/run/secrets/relay_tls_certificate,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/relay-tls-private-key.pem",dst=/run/secrets/relay_tls_private_key,readonly \
  --mount type=bind,src="/etc/rig-relay/secrets/relay-tls-ca.pem",dst=/run/secrets/relay_tls_ca,readonly \
  --entrypoint /bin/sh \
  docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 \
  -ceu 'for file in /run/secrets/*; do test -f "$file"; test -r "$file"; stat -c "%u:%g %a %F %n" "$file"; done'
```

The commands emit only numeric metadata and mount names. They must report the consuming UID/GID and mode `400`/`600` for every source.

Compose mounts each source through its read-only `secrets` interface. Do not replace these with environment interpolation or general host directory mounts. The build-context allowlist excludes `deploy/`, `.git`, documentation, local environment files, and secret files from image layers.

Windows Docker Desktop deployment with these file-backed secrets is unsupported until Linux-container single-file ownership/mode behavior is independently validated and automated. Do not weaken relay permission checks, broaden ACLs, or substitute environment secrets to make it start. The PowerShell static `-SelfTest` and deterministic `-BehaviorTest` remain supported on Windows; deployment mode and the real-filesystem `-LinuxIntegrationTest` gate require PowerShell 7 on Linux. The preflight uses no-dereference metadata checks for the anchor, environment file, secret directory, every ancestor, and every secret file. Deployment mode renders `docker compose config --format json`, rejects every effective service/top-level key or nested value outside the exact allowlist, then repeats protected-path validation and compares file identities before accepting the model. Run the matching `docker compose up` command immediately after a successful deployment-mode preflight. A privileged root or deployment administrator can still mutate a checked path after validation or after the verifier returns; this residual root/admin TOCTOU is an operational trust boundary, so restrict concurrent administrative access.

## TLS and exposure modes

The certificate must be currently valid, include the configured `HOSTD_RELAY_TLS_SERVER_NAME` as a DNS SAN, and chain to `relay-tls-ca.pem` (or the relevant system trust store for external checks). The relay requires the certificate/key pair whenever `HOSTD_RELAY_LOOPBACK_DEVELOPMENT=false`.

### Reverse proxy with TLS re-encryption

The baseline `compose.yaml` publishes no host port. Create the external edge network once, attach the separately managed reverse proxy to it, and start the stack:

```sh
docker network inspect rig-relay-edge >/dev/null 2>&1 || docker network create rig-relay-edge
pwsh -NoProfile -File scripts/check-relay-packaging.ps1 \
  -TrustedDeploymentAnchor /etc/rig-relay \
  -EnvironmentFile /etc/rig-relay/relay.env \
  -SecretDirectory /etc/rig-relay/secrets \
  -DeploymentMode baseline
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml up -d
```

The proxy must connect to `https://relay:7346`, send `HOSTD_RELAY_TLS_SERVER_NAME` as SNI, validate the complete served chain against `relay-tls-ca.pem`, and verify the DNS name. Configure an explicit backend CA/trust bundle and expected name; never use an insecure-skip-verify or certificate-verification-off setting. The proxy may terminate public TLS, but proxy-to-relay traffic remains independently authenticated TLS. Restrict the Docker edge network to the proxy and relay.

### Direct TLS

The optional overlay publishes the relay's own TLS listener. Its default binding is loopback-only:

```sh
docker network inspect rig-relay-edge >/dev/null 2>&1 || docker network create rig-relay-edge
pwsh -NoProfile -File scripts/check-relay-packaging.ps1 \
  -TrustedDeploymentAnchor /etc/rig-relay \
  -EnvironmentFile /etc/rig-relay/relay.env \
  -SecretDirectory /etc/rig-relay/secrets \
  -DeploymentMode direct-tls
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml -f deploy/relay/compose.direct-tls.yaml up -d
```

The supplied effective-Compose preflight rejects changing `HOSTD_RELAY_PUBLISH_ADDRESS` from `127.0.0.1`. LAN/public publication requires a separately reviewed deployment contract with the exact interface, firewall allowlist, DDoS/rate-limiting boundary, certificate/SNI, and monitoring path. Never use an empty host or `0.0.0.0` as a convenience default. LAN/public publication is not authorized by this example.

There is no production plaintext-behind-proxy mode. `HOSTD_RELAY_LOOPBACK_DEVELOPMENT=true` is only for an isolated loopback developer process outside these Compose files.

## Startup, migrations, and probes

PostgreSQL 18 persists its versioned data directory below the named `/var/lib/postgresql` volume. No host database port is published. On startup, the relay waits for the PostgreSQL health check, acquires its migration lock, and applies embedded ordered migrations before it accepts traffic. Migration failure leaves readiness down and exits startup; do not bypass it.

Pull requests run a hosted Linux lifecycle check against the unmodified baseline `compose.yaml`. The check builds the relay image, deploys it by the immutable digest returned from an isolated local registry, uses protected synthetic credentials and an in-container validated TLS certificate, waits for PostgreSQL and relay readiness, and verifies the complete embedded migration ledger. It then force-recreates only the relay and proves that the PostgreSQL container, named volume, database system identity, migration checksums/timestamps, and a CI-only marker remain unchanged. Captured logs are checked for the synthetic secrets before the fixtures and volume are removed.

This automated check is the hosted relay lifecycle evidence for Unit 9 and proves the baseline topology on a GitHub-hosted Ubuntu runner; it does not publish a host port. The direct-TLS overlay remains covered by effective-Compose and static contract checks. Public exposure, reverse-proxy behavior, firewall and denial-of-service controls, live GitHub integration, backup/restore, load characteristics, and multi-architecture image promotion remain separate staging or operations gates.

Inspect only closed process codes and lifecycle phases in logs:

```sh
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml ps
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml logs --no-log-prefix --tail=100 relay
```

The image's `rig-relay-probe` command accepts only an HTTPS origin plus the fixed `health` or `ready` endpoint. It validates the certificate chain and explicit DNS SNI, refuses redirects, bounds handshake/header/body/time, and emits only `relay probe ok` or `relay probe failed`. Compose uses `/readyz`; `/healthz` proves only process liveness. Never replace this with `curl -k`.

## Backup and restore

Use encrypted, access-controlled backup storage. A PostgreSQL dump contains relay identity and delivery state even though it contains no source archives or GitHub user tokens. Test restoration on an isolated environment on a schedule.

Before every schema/image update, create a consistent custom-format dump through the local container socket so no database password is passed in an argument or environment variable:

```sh
umask 077
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml exec -T postgres \
  pg_dump --username=rig_relay --dbname=rig_relay --format=custom > rig-relay-backup.dump
```

Restore only during an approved outage, into the expected PostgreSQL major version, after verifying the backup checksum and preserving the current volume snapshot:

```sh
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml stop relay
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml exec -T postgres \
  pg_restore --username=rig_relay --dbname=rig_relay --clean --if-exists --exit-on-error < rig-relay-backup.dump
docker compose --env-file /etc/rig-relay/relay.env -f deploy/relay/compose.yaml up -d relay
```

Follow PostgreSQL's [SQL dump](https://www.postgresql.org/docs/current/backup-dump.html) and [continuous archiving/PITR](https://www.postgresql.org/docs/current/continuous-archiving.html) guidance for the required recovery-point objective. A named Docker volume is not a backup.

## Key and secret rotation

- **Controller Ed25519 keys:** use the versioned WSS two-phase rotation: propose the new key, sign the relay challenge with the new private key, confirm, and finalize only after the controller has durably stored the new key and proven reconnection. Keep the old private key until finalization. If either side cannot confirm, abandon/expire the proposal rather than deleting the only working key. The dashboard Relay management panel exposes the administrator action and recovery state.
- **GitHub App private key:** create a second App private key in GitHub, place it in the protected file, recreate the relay, verify GitHub API/recovery success, then delete the old App key in GitHub. Never delete the old key first.
- **GitHub client secret:** stage the replacement in GitHub and the protected file, recreate the relay, verify enrollment in staging, then revoke the previous secret if GitHub permits overlap. Otherwise use a planned enrollment maintenance window.
- **Webhook secret:** GitHub uses the configured current secret for new deliveries. Coordinate GitHub and file replacement in a short maintenance window, recreate the relay, validate a test delivery, and recover any missed delivery afterward.
- **Enrollment key:** this 32-byte AEAD/rate-limit key protects in-flight enrollment verifiers. Rotate only after the ten-minute enrollment window is empty, then recreate the relay. Existing controller keys and subscriptions are unaffected.
- **TLS key/certificate/CA:** stage a certificate whose validity overlaps the current certificate, update certificate/key/CA together, recreate the relay, and validate both the actual served chain and proxy backend verification before removing prior trust.

Never print a key to compare it. Compare public fingerprints or certificate serials through approved tooling.

## Recovery and GitHub redelivery

GitHub does not automatically redeliver failed webhook deliveries. See GitHub's [failed delivery guidance](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries). The relay periodically lists GitHub App webhook deliveries within its bounded recovery window (maximum 72 hours), records recovery attempts durably, and requests redelivery through the GitHub App API. Delivery IDs are deduplicated and desired state remains monotonically versioned per subscription.

After an outage:

1. Restore PostgreSQL and relay readiness first; do not accept webhook success before durable state is available.
2. Confirm `recovery_scan` and `redelivery` background jobs succeed and their last-success age returns within the configured interval.
3. Compare GitHub's App delivery view with aggregate persisted/duplicate/failure outcomes. Do not paste provider response bodies into logs or tickets.
4. Keep controllers reconnecting outbound. At-least-once WSS delivery and acknowledgements converge; do not manually delete unacknowledged durable rows.
5. If the GitHub App lost access, restore installation/repository authorization or terminally reject the affected route. Never retry with a GitHub user token at the relay.

## Metrics and alerts

Scrape `/metrics` only over validated HTTPS on the private monitoring path. Labels are intentionally closed and low-cardinality.

Minimum alerts:

- **Served-chain certificate expiry:** an external TLS probe must validate the certificate actually served through each production route, including hostname/SNI and full chain. Warn at 30 days remaining and page/critically alert at 7 days. Do not monitor only the mounted certificate file.
- **Readiness:** alert when HTTPS `/readyz` is non-200 for more than the deployment grace period or `rig_relay_accepting` is `0` outside planned shutdown.
- **Listener/HTTP/WSS saturation:** alert on increases in `rig_relay_listener_saturation_total`, `rig_relay_http_service_saturation_total`, or `rig_relay_wss_capacity_rejections_total`; also alert when active/capacity ratios remain high.
- **Webhook/store failures:** alert on `rig_relay_webhook_outcomes_total{outcome="store_failure"}`, sustained `auth_invalid`, or abnormal `invalid` growth. A store failure means the webhook was not durably accepted.
- **Delivery versus ACK activity:** compare increases in `rig_relay_wss_deliveries_total` with `rig_relay_wss_decisions_total{decision="ack"}` and `decision="reject"`. Page when delivery continues but neither decision advances beyond the expected offline window.
- **WSS failures:** alert on abnormal failure lifecycle categories, authenticated-session loss for expected controllers, and a persistent gap between authenticated sessions and active leases.
- **Scheduler staleness:** alert when `rig_relay_background_last_success_timestamp_seconds` is zero after startup grace or `rig_relay_background_last_success_age_seconds` exceeds job-specific thresholds. At minimum cover `recovery_scan`, `redelivery`, `expiry`, and `maintenance`; alert on failure/contended trends.
- **Readiness/store signal:** alert on sustained `rig_relay_readiness_total{state="probe_failure"}` or `cached_failure` increases.
- **PostgreSQL and resources:** monitor database availability, connections, locks, transaction latency, storage space, WAL/backup freshness, volume I/O, container restarts/OOM kills, CPU, memory, PIDs, and log-disk pressure.

The relay metrics do not include application names, repository identifiers, refs, URLs, or controller IDs. Do not add those as scrape-time labels.

## Shutdown and rollback

Use `docker compose stop relay` and allow at least the configured 30-second Compose grace period. The process stops new HTTP/WSS admissions, drains the HTTP server, scheduler, and WSS sessions, then closes service/store resources. A nonzero exit or `shutdown_failed` code means drain safety was not proven; investigate before starting another instance against the same identity.

For a binary rollback, restore the prior verified `HOSTD_RELAY_IMAGE` digest and recreate only the relay. Embedded migrations are additive, but an older binary is not automatically guaranteed to understand a newer schema. Review the migration delta before downgrade. If incompatible, stop the relay and restore the pre-upgrade backup/volume snapshot under an approved data rollback. Never claim automatic rollback, and never delete current durable delivery state merely to make an old image start.

## Staging manual testing instructions

Prerequisites: a private test repository, a staging-only GitHub App installation, a staging PostgreSQL volume, valid public and backend certificates, an outbound controller test client, and external TLS/HTTP monitoring. Use synthetic values that are not production credentials.

1. Run the packaging self-test and environment-file validation. **Expected:** both exit zero and print only the fixed success lines.
2. Build both `linux/amd64` and `linux/arm64`, inspect the pushed manifest, verify the Distroless signature, scan the image/SBOM, and pin the resulting relay digest. **Expected:** both platforms exist, all configured images resolve by digest, and the runtime contains no shell or package manager.
3. Run `docker compose ... config --quiet` for the baseline and direct overlay. Inspect the rendered configuration without printing secret contents. **Expected:** no PostgreSQL port, no Docker socket/host bind/privileged mode, relay TLS files and CA/SNI health check present, and all security/resource/log controls retained.
4. Start the reverse-proxy mode. **Expected:** PostgreSQL becomes healthy, migrations complete once, relay `/healthz` and `/readyz` pass over validated backend HTTPS, and the proxy rejects an incorrect backend CA or SNI.
5. Inspect the relay container identity, capabilities, mounts, and filesystem. **Expected:** UID/GID `65532`, zero added capabilities, no-new-privileges, read-only root, only declared read-only secrets, no source tree, and no general host mount.
6. Deliver signed `push`, `installation`, and `installation_repositories` test webhooks. Repeat a GitHub delivery ID. **Expected:** raw-body HMAC is enforced, success is returned only after durable persistence, and the repeated ID is deduplicated.
7. Send invalid HMAC, oversized/invalid body, slow request, and unavailable-database cases. **Expected:** bounded failure, no provider/body/secret leak, readiness falls for database loss, and no false durable success.
8. Connect the controller through WSS, synchronize subscriptions, receive a desired-state envelope, and durably ACK it. Disconnect before another push and reconnect. **Expected:** authenticated TLS/WSS, at-least-once replay, one outstanding generation per subscription, and newest desired state supersedes older unacknowledged state.
9. Exercise two-phase controller Ed25519 rotation, old-key revocation, and reconnect. **Expected:** the new key works before the old key is retired; replay/incorrect signatures fail without losing the valid identity.
10. Simulate a failed GitHub webhook attempt and run/wait for recovery and redelivery. **Expected:** the attempt is durably tracked, redelivery is bounded/deduplicated, and scheduler success metrics advance.
11. Stop during active HTTP and WSS traffic. **Expected:** admissions close, bounded drain completes, durable unacknowledged work remains, and restart converges without duplicate state mutation.
12. Back up the database, restore into an isolated staging volume, and start the same image. **Expected:** migrations are idempotent, readiness returns, controller topology/durable state remains coherent, and no credentials appear in database/log scans.
13. Exercise the prior image digest rollback, then restore the pre-upgrade backup if schema compatibility requires it. **Expected:** rollback is explicit, no automatic destructive migration occurs, and both recovery paths are documented with timestamps/checksums.
14. Validate alerts by presenting certificates inside the 30 days and 7 days thresholds, failing readiness, saturating each bounded admission layer, causing a store failure, withholding ACKs, aging scheduler success, and applying PostgreSQL/resource pressure. **Expected:** every required warning/page routes to the staging responder with no identifier or secret labels.
15. Scan image layers, Compose render, PostgreSQL dump, relay/controller logs, metrics, health output, and problem responses for known synthetic GitHub token prefixes, submitted secrets, raw webhook bodies, repository/app names, source/config content, and raw IDs/URLs. **Expected:** none are present except the deliberately access-controlled opaque database fields required by the relay model.

Cleanup: revoke staging App keys/client secrets and controller keys, remove staging webhook/install access, securely delete local secret/backup files, and remove staging containers, networks, and volumes only after confirming their exact names and that no evidence must be retained.

## Verification boundaries

This Windows checkout has no Docker daemon, `psql`, live PostgreSQL, Linux namespace inspection, or CGO race toolchain, so those checks cannot be reproduced locally here. Pull-request CI separately builds the native Linux relay and exercises the baseline Compose topology, TLS probes, live embedded migrations, relay recreation, and PostgreSQL persistence as described above. Multi-architecture execution, backup/restore, reverse-proxy CA/SNI behavior, served-chain expiry monitoring, public exposure controls, load behavior, and end-to-end live GitHub/controller staging remain mandatory external gates. Do not promote solely from the local Windows or single-runner hosted checks.
