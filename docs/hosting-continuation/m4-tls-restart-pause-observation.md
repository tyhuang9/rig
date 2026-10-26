# M4 TLS restart-pause observation

## Evidence gap

The [PR #233 authenticated TLS journey](https://github.com/tyhuang9/rig/actions/runs/40250689844/job/121341703454)
reached browser create/read and external PostgreSQL outage/recovery checkpoints,
then failed the healthy-replacement check with `waiting_user` and
`route_reconciliation_required`. The whole parent failed; those checkpoints do
not establish journey acceptance. Raw log SHA-256:
`441200838f25f3667fbbce182763717be2d3c0fb62ebe104feb12b51d181178a`.

The retained log contains no closed route-pause pair. Child stderr was held in
memory, and this workflow does not upload it as an artifact. A different
process-kill test reported `previous_retirement/retirement_unavailable`, but its
helper projects only the newest valid pair. That does not establish the first
cause or the cause of this TLS failure.

## Narrow observation change

The existing restart-child cleanup still stops and waits for the owned child.
Afterward, only when the parent test has failed, it calls the existing
`controllerProcessKillReportClosedDiagnostic` helper. This reports the existing
digest and bounded, fixed diagnostic fields. Raw child stderr and private error
text remain private.

The branch builds on the reviewed cold-construction diagnostic vocabulary in
PR #235. No parser, lifecycle assertion, timeout, production API or recovery
policy changes. Multiple child cleanup callbacks may produce multiple records,
and early-exit reporting may duplicate a record. Each is a newest observation
for one child; the records do not identify the first failure or count failures.

## Verification and acceptance

Local evidence must bind the one-file diff, cleanup-before-report ordering,
tagged hostd compilation, exact discovery of
`TestLiveControllerGeneratedDeploymentJourney`, scoped vet, canonical generation
and documentation checks. Existing unchanged closed-parser controls are reused
with source bindings. No new mirrored parser test is needed.

Hosted execution must run the existing TLS journey. A failure may now expose a
sanitized diagnostic; an empty result remains an evidence limitation. This
change does not repair the pause or establish Docker, E6 or M4 acceptance.

## Rollback

Revert the failure-reporting call and its guard. Child cleanup continues as
before; no runtime or schema rollback is required.
