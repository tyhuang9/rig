# External application databases and services

Rig hosts application containers. It does not provide a managed database,
provision Neon projects or accounts, create application schemas, or delete
application data. Each application owns its database driver, migrations,
connection pooling, and external-service lifecycle.

## Scoped configuration

The canonical `examples/hosting-notes` fixture uses the following inputs.

| Key | Scope | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | API server runtime secret | PostgreSQL connection string. |
| `DATABASE_TLS_CA_PEM_BASE64` | API server runtime secret | Optional private test CA for an isolated harness. |
| `API_RUNTIME_MARKER` | API server runtime variable | Nonsecret deployment marker returned by `/api/version`. |
| `TEST_SENTINEL_SECRET` | API server runtime secret | Synthetic canary checked only for presence in fixture test mode. |
| `HTTPS_DEPENDENCY_URL` | API server runtime secret | HTTPS fixture endpoint; `HTTPS_DEPENDENCY_URL_ENV` can select another key. |
| `HTTPS_DEPENDENCY_TOKEN` | API server runtime secret | Bearer token for the HTTPS fixture; `HTTPS_DEPENDENCY_TOKEN_ENV` can select another key. |
| `HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64` | API server runtime secret | Encoded test CA for the HTTPS fixture. |
| `VITE_BUILD_LABEL` | Public frontend build variable | Intentional browser-visible build label. |

Only `VITE_BUILD_LABEL` belongs in the frontend build environment. A browser
bundle, static assets, image history, build context, logs, and API responses
must not contain `DATABASE_URL`, either server secret, or their values. A
runtime configuration change requires a new deployment configuration revision;
a Vite public build value also requires a rebuild because it is compiled into
the assets.

The fixture's `schema:prepare` command is application code. Run it under the
application/test-harness owner's credentials against a designated disposable
schema before deployment. Rig does not run it implicitly or treat a successful
schema command as permission to manage the database later.

## TLS and database ownership

The fixture's Node PostgreSQL client always sets `rejectUnauthorized: true`.
With a public provider, the hostname in `DATABASE_URL` must match the server
certificate and the platform trust store verifies the public CA. With the F7
test harness, the runner encodes its short-lived test CA as
`DATABASE_TLS_CA_PEM_BASE64`; the hostname remains
`postgres.fixture.test`, matching the certificate SAN. A wrong CA or hostname
is an expected readiness failure and must not be worked around by turning
verification off.

The fixture rejects URL TLS modes that would disable certificate verification,
including `sslmode=disable` and `sslmode=no-verify`. It removes permitted TLS
URL parameters before constructing the driver pool so its verified TLS options
remain authoritative.

In test fixture mode, `GET /api/test/dependency` performs one bounded HTTPS
request with the selected server-only URL, bearer token, and optional test CA.
It allows HTTPS only, sets hostname verification and certificate rejection,
does not follow redirects, has an absolute two-second deadline even if the
peer trickles a response, limits the response to 8 KiB, and returns only a
generic reachable/unavailable result. The endpoint and the F7 HTTPS stub make
it possible to verify arbitrary application-owned service key names without
adding a provider integration to Rig.

This is intentionally provider-neutral. A separately pre-created, disposable
Neon database can be used for a manual qualification by supplying its normal
server-only connection string and making a note round trip through the
deployed API. That is distinct from the local synthetic fixture: the local
test CA does not prove public DNS, the provider's certificate chain, or Neon
connectivity. Rig makes no Neon API calls and does not create its database,
role, schema, branch, or account.

Keep driver-specific pooling and retry limits in the application. Avoid
printing connection strings, passwords, CA material, or provider error bodies
in application logs or acceptance evidence. Rotate a runtime secret by adding
the new application-owned credential at a new configuration revision and then
redeploying; a code rollback does not roll back an external schema or data.

## F7 test topology

`examples/hosting-notes/harness` is an application/test-harness-owned Compose
project. It starts a disposable TLS PostgreSQL service and TLS HTTPS stub on a
network named `rig-hosting-notes-fixture-external`, then publishes controlled
host ports. A Rig-generated app remains on its private application network and
must reach the services through a tested host-gateway/DNS route; it must not
join the harness network just to make the request succeed.

The exact route differs by Docker platform. Docker Desktop generally exposes a
host gateway as `host.docker.internal`; qualified Linux automation must create
and verify its own host-gateway mapping. In both cases map the certificate
hostname to that route in the controlled application test environment and
verify name resolution separately before the database test. The harness README
contains the commands and cleanup boundary. Starting it, populating its
schema, or using a live provider are acceptance actions that need a
pre-approved disposable environment; their absence is `blocked_external`, not
a passing deployment claim.
