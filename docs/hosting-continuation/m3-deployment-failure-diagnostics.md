# M3 live fixture deployment failure diagnostics

## Observed failure

PR #174 head `4289f02c2001379703559665b7d525b3fa05f733`, hosted tree
`e61c66c66ec034fb54ab4942d39504fa58c23297`, failed partition 04 in
[job 116812254558](https://github.com/tyhuang9/rig/actions/runs/38845583152/job/116812254558).
The final `boundary/s10-gateway-lock-release` case failed after 9.05 seconds
while establishing its baseline deployment, before the recovery boundary.
Both components had terminal failure state, but the underlying diagnostic
codes were absent from the log. The cause remains unknown.

Failure occurred before the fixture captured immutable v1 predecessor
identities. Cleanup therefore retained uncertain resources and failed; the
independent cleanup step also failed. This change preserves those safeguards.
It does not establish that earlier cases leaked resources or that disk,
daemon, or runtime behavior caused the failure.

Raw log SHA-256:
`0e2d97da4920864991a1365bfebcc83bd4640d2eb7bb43485ee868b850f4d571`.
Failure archive SHA-256:
`3e8917fc2080a27503dd9bb9a77ee6576d796067f450f19c0de3fe3162d6188f`.

## Added evidence

The existing failure-only helper receives the job already fetched by the
polling loop. It validates the expected job/application identities and
deployment type before observing bounded deployment/runtime history.
The summary adds `job_read`, `job_status`, `job_code`, and
`runtime_diagnostic`. Statuses and diagnostic codes have finite allowlists;
unknown values become `unknown`, empty codes become `none`, and unavailable
or crossed evidence contributes no invented classification.

No job details, checkpoint JSON, raw errors, identifiers, or request bodies
are printed. The overwritten terminal job phase is not presented as the
original failure stage. There are no new queries, retries, host probes,
timeouts, production changes, workflow changes, or cleanup exceptions.
The existing ten-row history limit and shared two-second diagnostic context
remain in place.

## Local verification — 2026-10-16

At base `1736c38b34d17d172e459694b0618ed088c34b19`, all three existing
diagnostic parents passed (0.648s package) and canonical generation passed.
The final six parents and 27 subcases passed on native Windows (0.782s
package) and in a Linux binary actually executed in WSL. The `live_docker`
tag compiled the changed caller. Tests cover useful failure distinctions,
secret-bearing and malformed fields, empty codes, crossed job identities,
unavailable/crossed runtime evidence, exact read counts, and the existing
cancellation-safe shared context. No test failed or skipped in the final runs.

Commands: `go test -tags=live_docker -json ./cmd/hostd -run
'^TestLanTwoApp(DeploymentFailureObservation|FailureDiagnosticContext)'
-count=1 -timeout=2m`; Linux used `GOOS=linux GOARCH=amd64 go test -c
-tags=live_docker ./cmd/hostd` followed by direct WSL execution of that binary
with the same selector. Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`.
`pwsh -NoProfile -File scripts/check-generation.ps1`, formatting, and
whitespace checks passed.

The initial WSL invocation failed before tests because PowerShell transformed
an unquoted dotted flag; explicit argument passing ran the same binary
successfully. A formatting check also identified CRLF in the existing live
caller; normalization preserved its one-line Git diff. Both observations are
retained in the local evidence record.

Hosted Docker and race execution remain unverified for this revision. These
diagnostics do not repair or accept the failed s10 case, partition 04, or M3.
Rollback is a revert of these test-only changes; no production state changes.
