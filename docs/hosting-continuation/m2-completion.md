# M2 completion evidence

Date: 2026-09-29. This note closes the **M2 implementation gate** for the
controlled localhost hosting journey. It does not authorize a merge or deploy a
user application. The earlier [combined integration note](m2-combined-integration.md)
records the local-only candidate at `14e5340`; its pending hosted gate was
subsequently executed on the combined implementation revision below.

## Exact source and scope

- Tested implementation head: `b4806055993e8d1ac126217cf6353906341ee276`
  (`feature/hosting-m2-private-archive`, [draft PR #84](https://github.com/tyhuang9/rig/pull/84)).
- PR base: `feature/hosting-m2-reviewed-recovery` at
  `7536c5c09351bdc6212ffe3c58638a62325e4d1a` (PR #79).
- Requirement coverage: M2 R1, R2, R3, R4, and R12, with AUTH, RUN, and relevant
  CFG cases exercised by the controlled journey. The full M0/M1-to-M2 branch
  stack remains unmerged; this note records implementation evidence, not a
  default-branch release.
- Main surfaces: `cmd/hostd` controller and live Docker journeys,
  `internal/generatedingress` Caddy and routing, generated runtime/recovery,
  GitHub connection and archive handling, scoped configuration, the real
  `examples/hosting-notes` frontend/API, and hosted CI workflows. The fixture
  installs public npm packages, including React, Express, `pg`, and `ws`.
  PostgreSQL and HTTPS services are application-owned external fixtures. Rig
  does not provision a managed database or Neon.

## Executed acceptance

| Gate | Exact evidence and result |
| --- | --- |
| Browser → Caddy → static frontend → Node API → external TLS PostgreSQL | [`TestLiveControllerGeneratedDeploymentJourney`](https://github.com/tyhuang9/rig/actions/runs/36619750333/job/109581816434) passed on `b480605`. Its hosted log records Chromium `create` and repeated `read` passes, source staging pass, and an app at `67d6c3b7-406f-45b8-8c01-2461b23e863e.rig.localhost:8080`. Docker CLI/Engine 28.0.4, API 1.48. |
| Durable identities and replacement | The same job recorded fixture source SHA `cccccccccccccccccccccccccccccccccccccccc`, accepted plan `e944b349-bd08-49ac-93a1-e578bb7ced1a/1`, configuration `4b912b14-7fbf-4b0d-9837-655e4c0e7f5c/1`, first deployment `617c8a5d-a3c9-4812-bb45-9d911fb4d134`, and ready release `88a75ad0737ef90b61952db38d4d19b6`. It passed controller reopen, same-source configuration replacement, capacity pause/resume without a rebuild, bad configuration retaining the serving version, gateway readiness failure retaining the serving deployment, and unchanged route attestation until recovery. |
| External dependency outage | `TestLiveControllerGeneratedDeploymentJourney/ExternalDependencyOutage` passed in the same job. During PostgreSQL outage the API returned 503 and the frontend remained healthy. The previous serving head and application containers were retained, and the route was reattested after the same database recovered. |
| Killed controller recovery | [`TestLiveControllerGeneratedProcessKillRecovery`](https://github.com/tyhuang9/rig/actions/runs/36619750333/job/109581816264) passed all six named boundaries exactly once: `before_build`, `before_candidate_start`, `after_route_commit`, `before_drain_stop`, `after_runtime_success`, `after_main_success`. |
| Migration and uncertain completion | [`TestLiveGeneratedMigrationApprovalAndUncertaintyJourney`](https://github.com/tyhuang9/rig/actions/runs/36619750288/job/109581815377) passed; the approved migration, immutable revision, uncertain completion, and rollback controls ran in Docker. |
| Runtime and compatibility | [Docker blue-green lifecycle](https://github.com/tyhuang9/rig/actions/runs/36619750281/job/109581815512), [generated runtime race](https://github.com/tyhuang9/rig/actions/runs/36619750281/job/109581815836), [PostgreSQL/Linux race](https://github.com/tyhuang9/rig/actions/runs/36619750446/job/109581815671), [hosted Chromium](https://github.com/tyhuang9/rig/actions/runs/36619750292/job/109581815733), [Windows controller](https://github.com/tyhuang9/rig/actions/runs/36619750252/job/109581815747), and the other required PR #84 jobs succeeded on the same head. The hosted controller jobs required named pass events without fail/skip and ran owned Docker cleanup checks. |
| Live private GitHub connector | With user-approved `tyhuang9/test-repo`, Rig materialized immutable main SHA `7eca2aabbfbec03d2d1ed881b7c71b9aa3b07e79` as ready release `c2fb9bf06408deda2b8aee8c69688113`; archive SHA-256 `a8fd21c7d7d27718ff10de188bfb0181e9bf85391c92b124452d5deae5ff52d7`, workspace tree SHA-256 `cb8104a8d6f052dee854f6c5cfaa2d45aa6e5a90d8ed53b035eddd34322c2acb`. Repeat materialization reused the release and earlier failed releases remain recorded. On 2026-09-29, the local controller was replaced with a clean `b480605` binary using its existing data root. Bootstrap remained complete; the reloaded Connections page showed `Connected as @tyhuang9`, and Rig fetched a fresh repository picker page containing the private `tyhuang9/test-repo`. The connection stayed `connected` at credential generation 2 without an error. |

The live private repository check proves consent, credential persistence, and
archive retrieval. It does **not** claim a Docker deployment of that user's
application. The controlled fixture supplies the continuous real deployment
gate. The local Windows host has no Docker CLI, so its live container results
come from the linked hosted Linux jobs. The local controller uses fake runtime;
its restart is only the live connector persistence check.

## Failures, skips, and limits

- Early PR #84 hosted runs failed on fixture defects: an idle PostgreSQL pool
  error, incomplete process-kill configuration, a closed test store, and an
  outdated kill hook. Focused commits through `b480605` corrected those, and
  the final exact-head jobs above passed. The failed runs remain in Actions
  history. No runtime safeguard was relaxed to obtain the pass.
- Documentation deployment was skipped by design on a draft PR. CodeRabbit's
  status was green but its code review did not run for the draft. These are not
  evidence of source review.
- The private test repository's custom `db:migrate` script is outside Rig's
  detected migration contract. It was not run, and no private application
  deployment, external database provisioning, or production launch is claimed.
- The hosted journey checks the built image artifacts and their retained
  identity, but its log does not print the image artifact IDs. The deployment
  and release IDs above are independently recorded; the image IDs are verified
  by assertions rather than published as separate log fields.
- A physical second-device LAN test belongs to M3 and is still open. The
  acceptance case catalog referenced by the planning packet was not present
  in this checkout; named test functions and hosted jobs are the executable
  evidence here.

## Compatibility, rollback, and next milestone

The branch keeps existing `.rig.localhost` routes, strict gateway/container
attestation, immutable release and deployment history, scoped runtime secrets,
and conservative pause/uncertainty behavior. The credential fix preserves an
unreadable protected bundle for retry; it never replaces it on a read failure.
The archive fix accepts only the exact bounded private GitHub codeload form.
Closing or reverting the unmerged draft does not modify a deployed production
app. A future rollback must retain the controller data root, immutable release
records, and any application-owned database; Rig does not supply schema
rollback for that database.

M3 is next: explicitly approved, persisted LAN exposure with bounded,
attested per-project listeners. It must preserve the local route and be proven
from another device before claiming the LAN exit gate.
