# M3 pending-route replay after a later current revision

Date: 2026-10-18. Base: `94b7130e6d9dc8f4f8423578edb5f56b0583cfec`.

## Observed failure and bounded repair

[PR 184 partition 06, attempt 1](https://github.com/tyhuang9/rig/actions/runs/39111943369/job/117631018040)
reached the explicit `pending route historical disposition=commit` checkpoint,
then failed its first owned scenario with
`normal retained rebind replay: normal rebind replay proof is incomplete`.
The case ran for 599.02 seconds. All 329 preceding preflight controls passed;
the seven remaining owned cases did not execute. Independent cleanup succeeded.
The log SHA-256 is
`c808a50a1a11ef52490493ac6ff02f27154851cddfc04464664316397cf9555c`.
Artifact `12131653213` records exit 1, no timeout and no residual process group;
its ZIP SHA-256 is
`89ccb175ebe951d71eeebedb0226a2a5f441542c7e08eada7306eadd3577db98`.

That artifact does not retain the replay response body. The exact rejected field
in the hosted failure remains unknown. Source inspection predicts a harness
contract mismatch: later route publication changes the current revision and
digest, so production intentionally classifies the original terminal selection
as superseded. Current proof can remain verified while terminal proof becomes
unavailable. The existing production regression
`TestGatewayRebindCommittedTerminalClassifiesLaterSameLineageRevisionAsSuperseded`
documents this behavior. The generic live replay validator requires both proofs
to remain verified.

This change is limited to the live-test harness and its controls. The route
scenario retains its earlier fully verified historical COMMIT baseline. Its
later replay must have unavailable terminal proof with an empty terminal digest,
verified current proof with a valid digest, a released fence, no creation or
recovery, and exactly the same operation and selected authority across the
baseline, diagnostic and replay. Invalid baselines fail before HTTP I/O. The
same exact retained request bytes are posted; there is no fallback on failure.

The scenario then rechecks durable terminal SQL, receipt, history, fence and
resources using the verified baseline, verifies actual current serving using
the fresh replay status, and reaps the child. Generic strict replay and other
scenario callers retain their existing contract. Production code, workflow
selection, timeouts, recovery hooks, secrets and immutable history are unchanged.

## Executable local evidence

The positive control first failed against the original strict validator with
the same incomplete-proof error: parent and child each ran and failed once,
without skips. Go exited 1 as expected; wall time was 15.468 seconds. Raw output
SHA-256: `ca37fb5daefa55bd5481a8e6a569201d6bef087b2807bca72071cacf3b7c030f`.

After the repair, that positive control establishes a strict verified baseline,
then exercises the production-shaped later-current response through four exact
GET/POST requests and two byte-identical retained POST bodies. Twenty-five
negative cases cover four invalid baselines and 21 replay/diagnostic mutations.
They check zero I/O for invalid baselines, one GET/POST exchange for response
refusals, fixed error categories, empty returned status and no private values.

Verification used Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, and `CGO_ENABLED=0`.
The frozen three Go source files were hashed before and after each check.

| Check | Actual result |
| --- | --- |
| Existing 25-parent workflow preflight | 356 named tests each ran and passed once; no failures or skips; 22.466 seconds wall time |
| Focused replay controls executed under WSL | Same 64 names as the Windows subset, each ran and passed once; no failures or skips; 0.673 seconds execution wall time |
| Formatting | `gofmt -l` on the three changed Go files returned no files |
| Whitespace | `git diff --check` passed |
| Static analysis | `go vet -tags live_docker ./cmd/hostd` passed |
| Canonical generation | `pwsh -NoProfile -File scripts/check-generation.ps1` passed |

The Windows command was `go test -tags live_docker -json -count=1 -timeout=2m
./cmd/hostd -run <exact existing workflow test_pattern>`, using the 25-parent
preflight selection in `.github/workflows/hosting-gateway-v2-ci.yml`.
Raw output SHA-256:
`6f7facd230bdec58ebfc9beec668e38dc0bbd2b849c78d29d0de9404ec2abc25`.

For Linux, the same tagged package was cross-compiled with `GOOS=linux` and
`GOARCH=amd64` using `go test -tags live_docker -c -o <test-binary> ./cmd/hostd`.
The binary was actually executed under WSL from `cmd/hostd`, with
`-test.v=test2json -test.paniconexit0 -test.count=1 -test.timeout=2m
-test.run=^(TestLiveHostdRebindNormalReplayControls)$`.
Raw output SHA-256:
`bbd926a9a25001feffbb3e4d54cc2a3a483e8ffe004b90be27cf3d12df6c97ea`.
Canonical output SHA-256:
`2ccf88b936e0e9c22ed23d45c7c6adf97e27ca38eb12e76d274b34785fa315e7`.

Independent code/security and QA reviews found no must-fix issue. These controls
exercise in-process HTTP exchanges and harness validation. They do not execute
Docker or establish Linux race behavior.

## Remaining acceptance and rollback

Hosted CI must verify the repaired revision against actual Docker: the explicit
COMMIT checkpoint, complete pending-route restart/replay, durable and serving
assertions, all 63 matrix cases, independent cleanup and the aggregate gate.
Previous hosted passes on other revisions do not establish this head's result.
Physical second-device LAN acceptance and the remaining M3 criteria are still
open. M3 is not complete.

A normal reviewed revert restores the previous test-harness contract. There is
no runtime rollout, data migration, deployment or controller activation in this
change.
