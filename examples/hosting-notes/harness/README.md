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

The default published ports bind to loopback. An authorized Docker integration
runner can choose a routable host interface with
`FIXTURE_POSTGRES_BIND_ADDRESS` and `FIXTURE_HTTPS_BIND_ADDRESS`. It must map
`postgres.fixture.test` and `https.fixture.test` to that controlled host
gateway in the **application test container**, while keeping those containers
off `fixture-external`. Docker Desktop normally supplies `host.docker.internal`;
Linux runners need an explicit, tested host-gateway mapping. Resolve and test
the controlled DNS path before the TLS test. The certificate SAN must stay the
fixture hostname, so hostname validation remains meaningful.

The harness's private CA is test-only. Supply the encoded CA to the API using
the server-only `DATABASE_TLS_CA_PEM_BASE64` configuration value. A correct
test proves that the client accepts this CA and hostname; a bad CA or hostname
must fail `/readyz` without an error response containing credentials. Never
turn off certificate verification.

For the HTTPS dependency probe, provide `HTTPS_DEPENDENCY_URL` with the
controlled `https://https.fixture.test:55443/` endpoint,
`HTTPS_DEPENDENCY_TOKEN` with the same disposable token as
`FIXTURE_HTTPS_TOKEN`, and `HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64` with the test
CA. The API sends the token only in the HTTPS authorization header and returns
only a generic result. The stub rejects a missing or mismatched token.

For an HTTPS-only local smoke without Docker, generate the test CA and run
`pnpm test:https-local` from the fixture root. This starts the actual HTTPS
stub on a temporary loopback port and calls the backend's HTTPS client with
the test CA. It verifies a successful call and rejects a wrong token, missing
CA, and wrong certificate hostname. The test maps the fixture hostname to
loopback inside its request; it does not establish container bridge DNS or
egress. The generated private keys stay in ignored `harness/certs` and should
be removed after the smoke.

Stop and remove only the known harness project after evidence is captured:

```sh
docker compose down --volumes
```

Do not run this cleanup against a non-fixture Compose project.
