# M3 LAN external-database acceptance

Status: locally verified acceptance fixture; hosted Docker execution and
physical second-device qualification remain outstanding. Local evidence recorded
on 2026-10-15 UTC.

## Purpose and scope

This candidate starts from PR #159 at
`ef8ceb63bf1983d6e07dc6b0e4c4ce4fee52f02e`. It adds a focused sibling to the
existing two-app LAN transport test. That transport fixture deliberately keeps
notes in memory; a note retained by the other unchanged app does not establish
database persistence across replacement of the app that owns the note.

`TestLiveControllerLANDatabaseJourney` uses the normal, unchanged Hosting Notes
API with its existing application-owned TLS PostgreSQL fixture. Through
authenticated controller APIs it reviews the generated plan, stores scoped
configuration, deploys the frontend/API pair, configures the LAN profile,
upgrades the gateway and grants access. Requests use the URL returned by the
fresh LAN access observation.

The journey requires:

- A uniquely identified row created and read through the attested LAN URL.
- Redeployment of that same app, with a new configuration, deployment and
  release and replacement of both running component containers.
- The original row after redeployment and after closing and reopening the
  controller database, repositories, runtime composition and HTTP server.
- The same external PostgreSQL container and data volume throughout, with an
  unchanged app-private network separate from the database fixture network.
- Exact API-only delivery of the database URL, TLS CA and disposable sentinel;
  their absence from the frontend environment and served frontend bundle.
- Cleanup of the exact test-owned app, gateway, builder and external fixture
  resources, with an independent workflow check for leftovers.

There is no production database feature or migration in this change. Databases
remain application-owned and configured through scoped runtime secrets. The
test fixture project is `rig-lan-database-test`, its network is
`rig-lan-database-external`, and both must be absent before execution. Credentials
and certificate keys are disposable, generated at runtime and never printed.

## Hosted gate

The dedicated `LAN application-owned TLS database persistence` job in
`.github/workflows/hosting-lan-ci.yml` uses a disposable Ubuntu runner, pinned
Go/Node/pnpm setup and a frozen fixture dependency installation. The new journey
requires `RIG_RUN_LIVE_LAN_DATABASE=1` and rejects a remote Docker daemon or a
non-Linux host. Missing or skipped required tests must fail the result check.
The existing two-app job and the 63-case recovery matrix keep their original
scope and requirements.

The independent cleanup step always runs and fails if managed containers,
networks, volumes, generated images, builders, gateway staging resources or
external fixture resources remain. It reports leftovers without removing
resources of uncertain ownership.

## Verification boundaries

| Executed local check | Result |
| --- | --- |
| Windows and Linux live-tag compilation | Passed; the exact new live parent was discovered once on each OS without executing it |
| Focused helper controls on Windows and Linux | 8 tests passed on each OS, with no failures or skips |
| Production hostd build and live-tag vet | Both passed for Windows and Linux |
| Workflow validation | 4 shell steps parsed; all 15 actual jq positive/negative controls behaved as expected |
| Existing LAN workflow preservation | Removing the new sibling job yields the same parsed workflow as the base |
| Formatting and whitespace | `gofmt -l` and `git diff --check` passed |

Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'` and `CGO_ENABLED=0`. Linux test
binaries ran under WSL. The focused selection was:

```sh
go test -tags live_docker -count=1 -timeout=10m ./cmd/hostd -run '^Test(LANDatabase|ControllerJourneyStageSource|ControllerJourneyControllerProbeTargetUsesLoopbackControllerPort|ControllerJourneyAppControllerProbeClassifierFailsClosed|ControllerJourneyAppProbeAttestationBoundaries)'
go build ./cmd/hostd
go vet -tags live_docker ./cmd/hostd
```

The four new helper controls cover decimal row identifiers without numeric
conversion, exact environment values, configuration scope/sensitivity and
redirect refusal. The four retained controls cover fixture source staging and
controller-probe/namespace attestation. Workflow controls include an empty
result, absent live/helper tests, wrong names/package, duplicate run/pass,
failed/skipped parent and failed/skipped child. The complete cases pass.

Local receipts are retained in
`temp/m3-lan-external-db-20261015` in the workspace wrapper. Source and binary
hashes, exact test discovery, execution results and raw log hashes distinguish
local checks from hosted acceptance. The local workstation has no running Docker
engine or local race toolchain. Live-tag compilation alone is not a Docker pass.

| Raw test log | SHA-256 |
| --- | --- |
| `lan-db-native.tests.log` | `f093577d331f2a62955389a83c517209a157da4da3fa647388c05a56042f9139` |
| `lan-db-wsl.tests.log` | `0c5e0a42dac30bd35dcc11ffe4e6eb940c6fc4d049c381177acac9e7b5b61e34` |

Independent security review identified a redirect false-pass risk; the request
helper now rejects redirects and the regression control passed on both OSes.
The frozen security recheck found no remaining blocker. The production source,
Notes application, lockfiles and original recovery matrix are unchanged from
the locally verified combined baseline; their full suites were not repeated for
this test/CI-only change. Hosted acceptance of the combined baseline is still
pending.

This is an HTTP request from the runner to its selected private address. It does
not simulate a physical phone or laptop, independently administered database
provider, provider firewall, external DNS, or a separate host's routing. The
controller reopen uses fresh production components in the test process; it is
not the separate supported-daemon crash/recovery matrix.

M3 still requires the combined candidate's hosted matrix, Windows/Linux race
and LAN checks, plus the physical procedure in
[the LAN design](./m3-lan-design.md#verification-plan). The row-persistence gate
does not waive that procedure or establish M3 completion.

## Manual verification and rollback

1. Use a disposable Linux Docker host with the workflow's toolchain and frozen
   Hosting Notes dependencies. Ensure the controller and external fixture
   resource names are unused.
2. Run the exact test command recorded in the workflow with its explicit opt-in.
   Expect the live parent and helper controls to run and pass, with the final
   log identifying the app, both deployments, external database container,
   volume, attested LAN URL and retained row ID without credentials.
3. Run the workflow's independent cleanup check. Expect no matching resources.
   A failure or timeout is an unaccepted run, even if an earlier assertion passed.
4. Complete the separate physical-device procedure and record its actual result
   before claiming the corresponding M3 requirement.

No user controller is restarted or deployed by local verification or draft PR
publication. A failed acceptance candidate can be withheld. Any test resource
cleanup is limited to the preflighted disposable host and fixture identity;
retained production operation history and database state are not rollback targets.
