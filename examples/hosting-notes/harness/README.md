# External-dependency harness (F7)

This harness creates disposable TLS PostgreSQL and HTTPS services. Compose
owns its network, volumes, certificates, and database; Rig must never label,
create, remove, or attach application workloads to `fixture-external`.

Generate a short-lived test CA first, then start it only in an isolated test
environment:

```sh
./generate-test-ca.sh
export FIXTURE_POSTGRES_DB=fixture_notes
export FIXTURE_POSTGRES_USER=fixture_user
export FIXTURE_POSTGRES_PASSWORD=replace-with-a-disposable-value
export FIXTURE_HTTPS_TOKEN=replace-with-a-second-disposable-value
docker compose up -d
```

The default published ports bind to loopback for host-only checks. Rig's
generated containers do not currently receive custom DNS or host entries, so
the `*.fixture.test` names in the certificates will not resolve inside them
without separately controlled DNS. `host.docker.internal` alone is a different
certificate name and does not solve that mismatch. Do not count a host-only
connection or an untested hostname mapping as backend bridge acceptance.

For a disposable Linux Docker trial, first let Rig create its private app
bridge and inspect that bridge's gateway IPv4. Set
`FIXTURE_HOST_GATEWAY_IP` to that exact address before running
`generate-test-ca.sh` (or pass `-HostGatewayIp` to the PowerShell script).
The generated PostgreSQL and HTTPS certificates then contain the gateway as an
additional IP SAN. Bind both published fixture ports to that same address via
`FIXTURE_POSTGRES_BIND_ADDRESS` and `FIXTURE_HTTPS_BIND_ADDRESS`; supply
numeric-gateway URLs and the test CA only to the API's scoped runtime
configuration. Keep the Rig app containers off `fixture-external` and keep the
database under this harness's ownership. Gateway port reachability and driver
IP-certificate validation remain unverified until a real Docker run. This
trial does not prove external hostname DNS resolution.

The harness's private CA is test-only. Supply the encoded CA to the API using
the server-only `DATABASE_TLS_CA_PEM_BASE64` configuration value. A correct
test proves that the client accepts this CA and URL host identity; a bad CA or host
must fail `/readyz` without an error response containing credentials. Never
turn off certificate verification.

For the HTTPS dependency probe, set `HTTPS_DEPENDENCY_URL` to
`https://<gateway-ip>:55443/` for the proposed bridge trial. The
`https://https.fixture.test:55443/` form is only for a host or test environment
that explicitly resolves that name to the controlled fixture endpoint. Set
`HTTPS_DEPENDENCY_TOKEN` with the same disposable token as
`FIXTURE_HTTPS_TOKEN`, and `HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64` with the test
CA. The API sends the token only in the HTTPS authorization header and returns
only a generic result. The stub rejects a missing or mismatched token.

For an HTTPS-only local smoke without Docker, generate the test CA and run
`pnpm test:https-local` from the fixture root. This starts the actual HTTPS
stub on a temporary loopback port and calls the backend's test API endpoint,
which uses its HTTPS client with the test CA. It verifies a successful call and
generic failure responses for a wrong token, missing CA, and wrong certificate
hostname. The test maps the fixture hostname to
loopback inside its request; it does not establish container bridge DNS or
egress. For an additional local IP-SAN check, generate the certificates with
`FIXTURE_HOST_GATEWAY_IP=127.0.0.1` and run `pnpm test:https-local` with that
same variable set. The generated private keys stay in ignored `harness/certs`
and should be removed after the smoke.

Stop and remove only the known harness project after evidence is captured:

```sh
docker compose down --volumes
```

Do not run this cleanup against a non-fixture Compose project.
