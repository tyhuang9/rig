# M3 gateway rebind operator flow

Status: local backend, operator-flow and documentation checks passed. Hosted
acceptance remains pending. M3 acceptance is incomplete.
Base: supported-controller draft PR #154, `8e450e06c97bf3ed6effb9b0db97608af8e384af`.

## Scope

Add administrator review of a replacement LAN gateway and its complete application
roster, plus discovery and recovery of one recorded gateway operation. The initial
review requires separate approval of gateway settings and the application move.
Recovery uses a fresh operation-specific review and confirmation.

The new read-only `GET /api/v1/system/recovery` returns a version, process mode,
and optional recovery kind and operation UUID. It grants no authority. Normal
mode distinguishes validated absence from failed observation and checks active
presence before and after exact operation inspection. Recovery mode retains the
startup operation pin even after a terminal response. Administrator sessions are
checked before and after evidence reads. Queries, request bodies and encoded
bodies are rejected; responses are not cached.

The browser retains a pending operation identifier across refresh. It does not
automatically repeat commit or recovery mutations. Recorded terminal history,
physical gateway proof, and process recovery mode are separate facts. Neither
history nor a URL identifier supplies authorization for another effect.

No managed databases, database provisioning, controller installation, restart or
deployment is included. External application databases and scoped runtime secrets
remain application-owned.

## Executed backend verification

Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, `CGO_ENABLED=0`.
Linux test binaries ran under WSL. Each suite positively discovered its selected
tests, ran once with a ten-minute deadline, and recorded stable source and binary
hashes. No failures or skips occurred in these focused suites.

| Suite | Windows parents / subtests | Linux parents / subtests |
| --- | ---: | ---: |
| Recovery discovery, restrictions and canonical route contract | 8 / 8 | 8 / 8 |
| Supported daemon assembly and exact recovery startup reads | 5 / 6 | 5 / 6 |

Selectors:

```text
^(TestControllerRecovery.*|TestOpenAPIContractMatchesRegisteredRoutes|TestRecoveryOnlyController.*)$
^(TestGatewayRebindLiveHarnessUsesSupportedDaemonAssembly|TestGatewayRebindStartupDispatchPreservesDedicatedRecovery|TestGatewayRebindRecoveryPinBindsImmutableDirection|TestLANRecoveryBatchReadReturnsOnlyFreshStaticHeadWithoutWrites|TestLANSingularRecoveryReadRepeatsExactGatewayAndFullCensus)$
```

The discovery tests cover authorization, session revocation, strict request shape,
invalid startup pins before reads, missing and changing evidence, real SQLite
absence, corrupt protected files, active exact inspection, and retained terminal
LAN/batch/upgrade pins. Active wrapper tests use the real presence reader with a
scripted repository and exact inspector; they do not establish Docker behavior.

All four CLI builds and scoped vet (`cmd/hostd`, `internal/controller`,
`internal/generatedingress`) passed for Windows and Linux. The repository
`scripts/check-generation.ps1` check passed, including generated contracts,
migration mirrors and route drift. Evidence is retained in
`temp/m3-rebind-operator-flow-20261014/` in the enclosing development workspace.

## Executed operator verification

The frozen parent baseline passed all 502 frontend tests in 19 files. The final
frontend sources passed 519 tests in 20 files, including stale mount responses
after recovery conflicts, failed security checks and unavailable responses.
Delayed reads cannot replace a later recovery result or re-enable an old review.

| Command | Result | Raw log SHA-256 |
| --- | --- | --- |
| `pnpm --dir web test --run` | 519 passed; no skips | `72c0aa75ec1a7e95e1b51cd0f2567283d73126372e72059842eb198d6c8c85cb` |
| `pnpm --dir web build` | TypeScript and production build passed | `835c0a53af05ea35ccc6f93466459b8a48148bbfeaa399fe2f1d9a2f6cebb5d4` |
| `pnpm --dir web e2e` | All 7 Playwright cases passed | `146aa6412ea6e88e2f6a040a27bdafe5e2ba7586f0295fce3f363151e019e1b6` |
| `scripts/check-embedded.ps1` | Embedded assets exactly match production rebuild | `b15f1499f6dd79e625e215a1045454bb6ff70ecefc2a77a048655a15e11e9d13` |

The final unit/build/browser receipts pin all tracked and new frontend source
files before and after execution. Each remained unchanged during its accepted
run. All four CLI builds were repeated for Windows and Linux after embedding
this UI. Generated contracts, lockfiles and application runtime code remained
unchanged during frontend verification.

The browser suite includes the real isolated bootstrap/fresh-tab/cancel journey
using the existing fake-runtime hostd fixture. Three added browser cases use
mocked API responses to exercise recovery-only discovery, separate consent,
conflict/status/review sequencing, terminal recovery isolation, the full two-app
roster, a dropped commit response retained across reload, and an active operation
callout when the profile read fails. No automatic commit or recovery retry is
permitted. Existing fresh-LAN-action and connector/focus browser cases also pass.
These tests start temporary loopback servers and do not use the installed local
controller or change its data.

Keyboard focus and Escape restoration were asserted in Chromium. Automated
checks found no horizontal page overflow at widths 375, 768 and 1440. Root and an
independent UX reviewer inspected all six recovery/review screenshots across
those widths; the review dialog scrolls internally to its two approval controls.
No full WCAG conformance claim is made. Security and code review found no
remaining blocking issue in this bounded change.

Development failures were retained as evidence, including stale test fixtures,
unsupported test matcher types, an over-strict single-read browser assertion,
and the delayed-read race found during review. Corrected final runs are listed
above; earlier failed or source-changing runs do not count as acceptance.

Actual local JavaScript tooling was Node 24.15.0 / pnpm 11.14.0. The frozen offline
install preserved the lockfile. Hosted CI uses its configured Node 24.19.0 /
pnpm 11.22.0 and is still required. Documentation workflow/build/accessibility
verification passed: `pnpm --dir docs install --frozen-lockfile --offline`,
`check:workflow`, `build` and `check:accessibility`. The four individual receipts
are recorded in `docs-final.result.json`; the documentation build log SHA-256 is
`c28f96697bc6667caaf892dc1a8559355181e4962500f9d8a6599df6e596eee5`.
This evidence-only summary was added after that successful documentation build.

## Remaining acceptance

PR #153's repaired private-controller Docker recovery journey passed; see its
[acceptance evidence](m3-controller-docker-acceptance.md). PR #154's full supported
daemon Docker matrix and broader CI remain pending. Its repository race partition
exposed an obsolete selected-current fixture. The test-only correction `897e7da`
is published to PR #154 after Windows/Linux checks and independent review; hosted
race verification of that correction is pending.
Mocked browser tests cannot substitute for Docker, a second physical LAN device,
or application-owned external database qualification.

## Rollback

Keep this change on a draft branch until required acceptance is complete. Reverting
the UI and discovery additions removes the new entry points; it does not alter
recorded operations or immutable history. An unresolved operation must still be
recovered using its exact ID and the supported controller protocol. Never erase
protected state or SQL history to dismiss a recovery screen.
