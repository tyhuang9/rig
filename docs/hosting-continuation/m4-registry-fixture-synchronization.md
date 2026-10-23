# M4 registry fixture synchronization

## Observed failure

PR #209 at `491219a6abb14e540a725759654ff96ca0044bf1` failed the
`Generated runtime race tests (ingress-remainder)` job:
<https://github.com/tyhuang9/rig/actions/runs/39838707511/job/120033723349>.
The tested merge was `ff354e9bd5b0f6be1a88bc6180943acbddc484a1`;
its tree equals the PR head's `4d7856ae47043e5c55601b22527e69f57783efb6`.

The structured Go output records 413 parent starts: 387 passes, 25 skips
and one failure, plus 648 passing subcases. Nested subprocess output is
reported separately and is not counted as additional structured events.

`TestGeneratedDomainRegistryFencesLegacyConstructionAndTypedActivity` first
failed to observe an active pairing epoch within its five-second test wait.
That assertion could terminate the subcase before its admitted construction
ticket was released. The retained ticket then blocked reservation progress;
a later subcase remained waiting until the isolated process reached its
90-second timeout.

The first observation's delay is not explained by the log. The later missing
cleanup is supported by both the stack and the fixture's source. No production
registry defect is established by this observation.

## Bounded repair

Keep the five-second observation deadline, the isolated process deadline and
all concurrency assertions. Replace the tight polling loop with a yielding,
bounded state observation. Register cleanup as soon as each ticket, activity
or goroutine is owned so an assertion failure cannot poison a later subcase.
The existing double invocation of the actual activity-release function remains.
A shared test-only cleanup helper releases owned state, cancels the reservation
and joins its goroutine within five seconds. This change is confined to test
synchronization and cleanup.

## Executed verification — 2026-10-22

Pinned Go 1.26.7, readonly modules and CGO disabled were used for Windows/amd64
and native Linux/amd64 through WSL. Each platform discovered and passed these
three outer parent tests with `-test.count=1 -test.timeout=10m -test.v`:

- `TestGeneratedDomainRegistryFencesLegacyConstructionAndTypedActivity`.
- `TestGeneratedDomainRegistryLinearizesConstructionRosterAndFullPublication`.
- `TestGeneratedDomainRegistryFixtureCleanupAfterFatalAllowsLaterReservation`.

The control deliberately fails a nested test after observing an active pairing
epoch. It uses the same cleanup helper as the repaired fixtures. Its subprocess
must complete cleanup and a subsequent independent reservation, then exit with
only the expected failed parent and subcase. The outer test requires those exact
failures and the later-progress marker; a timeout or extra failure is rejected.

On each platform, complete verbose output contains 13 RUN events, 11 PASS events
and the two expected nested FAIL events, with no skips. Those totals include
three outer parent passes, two repeated parent passes from ordinary isolated
processes, six passing subcases and the intentional failed parent/subcase.
All three outer parents passed; the deliberate inner failures are test evidence,
not failing suite results.

Both targets passed `go vet ./internal/generatedingress`. Fresh package test
binaries compiled; read-only gofmt, diff checks and the canonical
`pwsh -NoProfile -File scripts/check-generation.ps1` passed. Go/module source
hashes remained unchanged throughout execution. Formatting before the freeze
changed only CRLF line endings to LF in the two modified test files.

Local execution did not include race instrumentation or real Docker. Hosted
race acceptance remains pending. The first five-second observation delay in
the original job remains unexplained; this repair prevents tight polling and
the confirmed cleanup cascade without extending its deadlines.

Verification receipt `checks-20261022T235121Z/receipt.json`: SHA-256
`529b5bae64db7d3d54df5ba0feffa91ea29bf88eaba235bb6fbd2fc6bc5e529f`.
Full Go/module manifest:
`36451faa3e149e1482d6350423f28543867c96dc64e74dd41bca83841f330a8e`.

| Target | Test output SHA-256 | Binary SHA-256 |
| --- | --- | --- |
| Windows | `bda907106e8a9b1bdef8ae15995b273d0a098169854ebd7ac1c210d7d9130c39` | `1242ae9bc758f6a5cffded3bd00ab6bad0b48d285fe9315f0f005c7a27e2495d` |
| Linux | `2a82feee0061a7c48ab9831ad943608da634ab11eac995506efe8c91807a0b2d` | `e5929fd514041dcba8367432554c4e0573b3bedefd5716b75c11ba68b954ec29` |

Retained failure evidence under
`temp/m3-lan-external-db-20261015/m4-domain-registry-fixture-verification/`:

- `hosted-failure-20261022.json`: SHA-256
  `7969501ac2fe61975dd3655aa23eab249aa86c10738cbdab283f92b9a89354fc`.
- Original job log: SHA-256
  `17243d5a3a253a84a40cc392c3732eaa448808d7a2d83d561ad34c28f54b6e0c`.
- Source diagnosis: SHA-256
  `9ae0b34c6a9c2c4b8979abfd0fe22da1156dcc0f973f6555a7672bd79648cf2b`;
  cause/census clarification:
  `a735c1bbda2676e6064115ce6a3a5e0c7391ab4c8002dfb5342f2d9c7a7ebaf1`.

## Scope and rollback

Reverting the test-only change restores the previous fixture. It changes no
runtime authority, database format, Docker operation or deployment behavior.
Real Docker, physical LAN qualification and M4 completion remain separate work.
