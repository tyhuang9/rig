# A0 recovery lifecycle CI budget

## Observed boundary

[PR #245's A0 Docker job](https://github.com/tyhuang9/rig/actions/runs/40302965729/job/121505057406)
ran published head `71c4640e8a84e3b4288d2d39bac06339a804aa2a` through
synthetic checkout `23b271cc719d3e6fab08b758cb0e59181e4e3a72`, with the
same tree. The selected `boundary/a0-sql-claim` case failed after 485.35
seconds. Raw log SHA-256:
`92ad6b81450fbe7b2b4263dca1b3ff17115101e053c1fe7c0e871f0c8ffe4169`.

The first recovery readiness marker appeared at 10:16:18 UTC. The test
then completed its recovery request, expected terminal-disposition
assertion and recovery-process reap. At 10:16:29 it started the third
process's readiness check, which refused before waiting:

```text
deadline leaves no bounded wait time after 10m0s cleanup reserve
```

The third process's normal-mode, repeated terminal and final serving
assertions were not reached. Independent fixture cleanup passed. These
facts establish progress through first recovery, not complete A0
acceptance or restored application accounting.

## Budget correction

The readiness helper uses the earlier of its fixed ten-minute cap and
the enclosing deadline minus the fixed ten-minute cleanup reserve. The
previous 18-minute Go deadline therefore admitted readiness waits only
before eight minutes elapsed. The observed third arrival was about
480.66 seconds after package start, making its available budget negative.
This refusal says nothing about the third child's speed or readiness.

The corrected workflow envelopes are:

| Scope | Previous limit | Corrected limit |
| --- | --- | --- |
| Go A0 invocation | 18 minutes | 32 minutes |
| A0 test step | 20 minutes | 34 minutes |
| First-boundary job | 30 minutes | 50 minutes |
| Independent cleanup step | 3 minutes | 3 minutes |

The four preceding steps retain their declared 2, 3, 1 and 3 minute
limits. Their combined maxima, the 34-minute test step and the three-minute
cleanup step total 46 minutes, leaving four minutes inside the job limit.
The job limit must encompass these steps; extending only the Go command
and test step would allow the old job timeout to interrupt cleanup.

At the observed third arrival, the 32-minute Go envelope leaves enough
time for the unchanged ten-minute readiness cap and ten-minute reserve.
It permits twelve minutes of preceding work before that full cap starts
shrinking. This is a bounded adjustment informed by the observed run.
The intervening Docker, recovery and assertion work has no single proven
worst-case duration, so these limits do not guarantee every run completes.

Keep the readiness cap, cleanup reserve, phase order, authenticated probe,
child deadlines, exact A0 selector, failure-sensitive shell/JQ checks and
independent `if: always()` cleanup unchanged. No production runtime,
ownership, authority, schema or application-database behavior changes.

## Verification and remaining acceptance

Focused Windows verification passed on 2026-10-26 with pinned Go 1.26.7,
`GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and
`CGO_ENABLED=0`. A fresh `go test -tags live_docker -c` build and exact
outer-parent discovery passed. Executing
`TestLiveHostdRebindChildStartupControls/readiness_budget` produced eight
unique passes: the outer parent, readiness group and six table rows, with
zero failures or skips. The former deadline refuses at the late-third
arrival; the corrected deadline admits the unchanged cap. Existing
no-deadline, ample-deadline, inherited-deadline and exhausted-reserve
controls passed in the same selection.

Formatting, tagged vet, canonical generation, the exact workflow-budget
contract and existing supported-rebind workflow contract also passed.
These checks bind the job/step/Go/cleanup limits, unchanged selector,
failure-sensitive shell/JQ census and independent cleanup. Source/security
review found no blocking issue. Source manifest SHA-256:
`1d74508d099dca3673a633a2782c5b17a38b8ff0483b4f8cb810ac98320065f5`;
test binary SHA-256:
`a7d2f852db00162f3b450d3b003f139c654bd88dbfa88160bc38aa773e7d6b85`.

Hosted acceptance must prove first recovery, third-normal authenticated
readiness, terminal and serving assertions, child reap, test/package PASS
and independent cleanup. Until that job succeeds, full A0 and restored
application accounting remain open. No local Docker acceptance or manual
UI verification is claimed.

## Rollback

Revert this CI/test change through the PR workflow to restore the previous
limits and their observed early budget refusal. No deployment, data
migration or immutable-history change is involved.
