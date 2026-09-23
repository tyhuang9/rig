# Hosting continuation: M1 scoped configuration candidate

**Recorded:** 2026-09-23. **Base:** M0 local commit
`1f217adc8bf52f715c9efbda0d65342c7331c1ec`.
**Candidate branch:** `feature/hosting-m1-scoped-config`. This work has not been
published, merged, or deployed. M1's live exit gate remains open.

## Implemented boundary

- New configuration revisions use a version-2 protected bundle and additive
  SQLite metadata with exact accepted-plan, component, phase, and sensitivity
  identity. Historical version-1 protected files and revision pins remain
  readable and unchanged. An existing masked version-1 secret can move into
  one explicitly reviewed scope without returning its value to the browser.
- Generated server candidates receive only their component's runtime entries.
  Migration containers receive only names in the approved plan allowlist,
  selected from that server's runtime or migration phase. Missing or
  ambiguous requested keys fail closed. Compose retains its legacy exporter.
- Build values are explicitly public. The selected configuration revision's
  public values are part of the generated image definition digest and enter
  BuildKit via a protected, temporary mount outside the source context. Server
  secrets are never passed to the compiler. An app with public build values
  and no reviewed build command receives an actionable error.
- The dashboard edits generated configuration against an accepted plan,
  requires explicit review of legacy masked secret targets, masks stored
  secrets, and blocks writes when the plan cannot be loaded. A v2 configuration
  shows plan drift and requires explicit rebind review; stored secrets must be
  re-entered for the new plan, while an empty v2 revision can rebind without
  an unrelated edit. Migration key
  names are editable during plan review; migration approval remains separate.
  New legacy writes are rejected for an accepted generated plan.
- The checked-in hosting-notes fixture has a static frontend, API, test-only
  key-presence probe, TLS PostgreSQL client, a bounded HTTPS dependency probe
  and stub, and
  provider-neutral setup guide. Its database and schema are application-owned.
  There is no managed database or Neon provisioning path.

## Automated evidence

| Check | Observed result on M1 worktree |
| --- | --- |
| `go test -p 2 -count=1 -timeout=30m ./...` | Passed on the final candidate after the security fixes. |
| `go vet ./...` | Passed on the final candidate after the security fixes. |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed on the integrated candidate. |
| `pnpm --dir web test` | Passed on the final candidate: 359 tests in 13 files. |
| `pnpm --dir web typecheck` | Passed on the final candidate. |
| `pnpm --dir web build` and `scripts/check-embedded.ps1` | Passed after the final UI fixes; embedded assets match the deterministic build. Existing Vite chunk advisory remains. |
| `scripts/check-generation.ps1` | Passed; OpenAPI routes and migration mirrors agree. |
| `pnpm --dir web e2e` | Passed on this worktree: all 3 Chromium journeys, including the new real-controller scoped-secret check. |
| `pnpm --dir web e2e --grep "bootstraps, restores"` | Passed after adding a real-controller browser check for a synthetic scoped server secret. Its save and reload responses were `no-store` and omitted the value; the reloaded replacement field, page text, URL, and browser storage did not contain it. The controller ran with the fake runtime, so no container was started. The first run failed on a test selector that changed when the row was named; the selector was corrected and the rerun passed. |
| `examples/hosting-notes: pnpm test` and public-label `pnpm build` | Passed: 9 API tests, including no-network rejection of TLS-disable URL parameters, bounded HTTPS probe, and secret-safe responses; frontend fixture build passed. |
| `examples/hosting-notes: pnpm test:https-local` | Passed after generating a disposable test CA with OpenSSL. The API test endpoint called the actual HTTPS stub through the backend client over loopback TLS; wrong token, missing CA, and wrong hostname returned generic 503 responses without credential text. The generated private keys were then removed. Test-only DNS mapping did not exercise container bridge egress. |
| `go test -count=1 -run '^TestHostingNotesFixtureAcceptsReviewedFrontendAndBackendSetup$' ./internal/sourceinspection` | Passed. Rig selected the generated API and frontend candidate instead of the external harness Compose file; an explicit two-component plan with database-backed `/readyz` health was accepted. |
| `go test -count=1 -run '^TestCompilerStagesHostingNotesFixtureWithoutDocker$' ./internal/generatedimage` | Passed with clean source and again after a local frontend build. The actual fixture's checked-in setup staged both component contexts with a fake Docker runner; source lockfile and app files were present, local `node_modules`, built assets, and test certificates were absent, and only the frontend received its public build value. No image was built. |
| `go test -tags live_docker -run '^$' ./internal/generatedimage` | Passed compile-only. The existing direct Docker recipe test now supplies the required empty public-build-values mount; it was not executed without Docker. |
| Opt-in `TestLiveHostingNotesFixtureImages` and generated-runtime CI step | Added and compiled, but not run. On a local Linux Docker host it will build the fixture's API and frontend images from the reviewed setup, inspect image environment, verify the public frontend label in static assets, and remove the exact images. A local Vite build confirmed that exact label appears in the emitted asset. It does not prove external database connectivity or Rig ingress. |
| Fresh copied fixture: `pnpm install --frozen-lockfile --offline` from `api`, then `VITE_BUILD_LABEL=workspace-install-check pnpm build` from `frontend` | Passed with an initially absent `node_modules`. pnpm resolved all three workspace packages and produced the frontend assets. The disposable copy was removed after the check. |

The security review identified a PostgreSQL URL TLS-parameter override, an
empty legacy migration export with a nonempty allowlist, implicit legacy
secret targeting in the editor, and a plan-load error that opened the legacy
editor. Each was fixed and the reviewer rechecked those paths. Focused tests
cover all four. A separate CodeRabbit review was unavailable because its WSL
CLI was not authenticated. An independent source review found three further
issues: omitted configuration plan pins in the API/editor, an HTTPS stub with
no application-side call, and a 64-character API target limit inconsistent
with accepted 256-character component IDs. All were corrected, with focused
regression tests. A subsequent review also caught obsolete-scope removal
payloads during plan rebinding; that path now sends a full replacement against
the new plan and has storage/editor regression coverage.

## Acceptance status

| Cases | Evidence | Remaining gate |
| --- | --- | --- |
| CFG-01, CFG-06, CFG-08 | Scoped export, compiler identity, API/UI validation, and build-input tests pass. | Inspect real built image, layers, static assets, browser traffic, and component environments. |
| CFG-07 | A focused executor test holds the source release constant, advances current configuration from revision 3 to 4, and verifies both the deployment pin and compiler input use revision 4. | Run same-source configuration-only redeploy against real Docker. |
| CFG-09, CFG-10 | Literal-value, invalid-input, immutable storage, masking, admin/CSRF, and editor tests pass. | Run byte-exact deployed environment checks and inspect temporary files. |
| CFG-11 | A real-controller Chromium run saved a synthetic server secret and confirmed `no-store`, omission from save/read responses, and absence from the reloaded field, page, URL, and browser storage. Unit/controller tests cover authorization and CSRF. | Inspect safe diagnostics and viewer behavior in a real controller session; repeat on an enabled generated runtime. |
| CFG-13 | Approved migration allowlist, custom key names, missing/ambiguous key rejection, and short-lived runner tests pass. | Inspect a real migration container and approval journey. |
| CFG-02 through CFG-05, CFG-12, CFG-14, CFG-15 | Fixture and supporting implementation exist; unit tests prove the HTTPS client requests verified TLS with server-only credentials and a total deadline. A real loopback HTTPS smoke with the actual stub passed positive and negative CA, token, and hostname cases. | Container bridge DNS/egress, TLS PostgreSQL, replacement/rotation/failure runs, private-build negative run, and crash cleanup are not verified. |

Neither this Windows host nor its available WSL environment has a Docker or
Podman CLI, so it cannot run BuildKit, a container,
the disposable TLS PostgreSQL/HTTPS harness, or an actual backend database
roundtrip. No disposable external credential was provided; no Neon account,
schema, or data was modified. A Go race run was unavailable because this host
has CGO disabled. Hosted candidate CI would require branch publication, which
the user has not authorized. M1 is a tested local implementation candidate,
not an accepted live hosting milestone.
