# Hosting notes fixture (F1)

This is the canonical F1 fixture for a supported static Vite/React frontend
and Node API deployment. It is an application source tree: Rig is expected to
build and run it, but it never provisions its database, creates a provider
account, or manages its schema.

The browser uses relative `/api/...` requests so one ingress address can serve
the static frontend and API. The frontend displays only `VITE_BUILD_LABEL`,
which is intentionally a public build input. It does not receive database or
test secrets.

## Commands

Use Node 24 or later and pnpm 11:

```sh
pnpm install --frozen-lockfile
VITE_BUILD_LABEL=fixture-A pnpm build
pnpm test
```

For a pre-approved disposable database, configure server-only values and let
the application-owned setup script create the isolated schema:

```sh
export DATABASE_URL='postgresql://fixture_user:fixture_password@postgres.fixture.test:55432/fixture_notes'
export DATABASE_TLS_CA_PEM_BASE64="$(base64 -w0 harness/certs/test-ca.crt)"
export API_RUNTIME_MARKER='runtime-A'
export TEST_SENTINEL_SECRET='fixture-sentinel-only'
export FIXTURE_SCHEMA='rig_fixture_notes'
pnpm schema:prepare
pnpm start:api
```

`pnpm start:api` binds to `0.0.0.0`; the static frontend can be inspected with
`pnpm start:web`, which also binds to `0.0.0.0`. A generated static deployment
serves `frontend/dist` through its ingress instead of using Vite preview.

The database URL, CA, and sentinel are runtime-only values. Do not set them
when running `pnpm build`. `schema:prepare` is deliberately a separate
application script; use it only against a disposable application-owned
database/schema.

## Runtime contract

The API implements:

- `GET /healthz` for process liveness;
- `GET /readyz` for a bounded database readiness query;
- `GET /api/version` for nonsecret source and runtime markers;
- `GET` and `POST /api/notes` for parameterized note reads/writes;
- `GET /api/events` for a finite SSE response; and
- `/api/ws` for a bounded WebSocket echo/version check.

With `TEST_FIXTURE_MODE=1`, test-only endpoints are available under
`/api/test/`: a bounded response delay, synthetic log producer, and a
presence-only runtime check. `/api/test/dependency` makes a bounded HTTPS
request using server-only URL, token, and CA inputs. None returns a secret
value.

See [external application databases](../../docs/external-application-databases.md)
for scoped configuration, TLS verification, the F7 harness topology, and the
separate pre-created Neon qualification.
