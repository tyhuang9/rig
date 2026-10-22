# M4 private generated-domain startup classifier

Status: private reader tests, static analysis and builds passed locally.
Hosted Docker/race acceptance remains pending.

## Purpose and dependencies

The classifier describes the sealed controller database, protected gateway
history and Docker inventory together. Its possible results are proved empty,
stable gateway-only, exact typed recovery required, or refusal. A result does
not authorize mutation, construction, capacity acquisition or application
traffic.

The branch combines registry PR #209 at
`491219a6abb14e540a725759654ff96ca0044bf1` and startup-snapshot PR #210 at
`4f897add55e0aa78e39c6f98e76eca0505df6662` through local merge
`d8bf04a2d67e3e51beb908e09abfad3918a0439e`. The intended draft targets PR #209.
Neither target PR has been merged into an integration branch.

## Reader invariants

- The sealed startup snapshot comes from the retained canonical database.
  Supplemental caller snapshots do not replace its authority.
- Every classifier-reachable protected scan uses strict versioned validators,
  a maximum of 4,096 entries and a 16 MiB aggregate decrypted-byte budget
  debited before decoding. Unknown, crossed, incomplete or changing history
  refuses classification.
- Docker enumeration covers every resource kind and all image IDs. Reserved
  names, retained SQL/protected identities and every reserved `io.rig.*` or
  `rig.*` label key participate in the census. An unaccounted resource refuses.
- Current V2 requires its exact retained rebind or upgrade origin and canonical
  lineage, including SQL authority. Version order is not an ownership rule.
- Gateway image/static identity, canonical restart configuration and resource
  binding precede the fixed admin read. Canonical live configuration precedes
  any gateway-owned challenge. Application endpoints and unowned listeners
  are never probed.
- App-bearing states retain their independent cold-accounting classification
  after passive namespace validation. They cannot become stable gateway-only
  by omitting application traffic.
- Existing ordinary wrappers retain their validators. The extracted read-only
  paths do not create a manager, bootstrap resources, restart a container,
  restore routes, retire resources or grant capacity.

## Review corrections

Independent review and executable checks identified corrections that are
included in the candidate:

1. Exact recovery resource ownership and canonical restart/live configuration
   must be established before target traffic. A valid-looking resource name
   alone is insufficient.
2. Bounds must apply to transitive canonical scans as well as the initial
   namespace enumeration, including fresh final-history scans.
3. Current-V2 upgrade-origin inspection must validate the pinned image before
   restart inspection, admin reads or challenges.
4. Completed upgrade plus aborted rebind history requires an exact
   bidirectional SQL/protected-history match and canonical terminal evidence.
   Valid aborted intents remain in the complete artifact history but are
   excluded from the commit-only view map.

The accepted nine-file production freeze has SHA-256
`563393664fb85892f2906253b72a0abc5c22c6a145d4a029de772bdf3c3d58d8`.
Independent security recheck:
`35c9c132f78cbf3288b9a94b681e208b2a21e2c011951436039eece4b31be11a`.
Root bounded source recheck:
`afcf5f9a02de16e1830f6892ff917d2808caba86ce2548ff71d6ea73b015c35f`.
These are source-review records; executable acceptance is recorded separately.

An initial complete-census fixture used a noncanonical `control.db` path and
was rejected before Docker observation. The corrected fixture uses canonical
database provenance. This failure did not establish a production namespace
defect. Rejected source freezes and unsuccessful attempts remain in the local
evidence directory rather than being counted as successful verification.

The final harness initially stopped after successful Windows execution because
it disallowed an existing POSIX-only test's explicit Windows skip. Those
completed Windows results are retained. A resumed Linux attempt then failed
because its configured temporary directory was missing, before fixture
behavior. The unexecuted or failed Linux groups require their own successful
results; neither harness failure is counted as a production test pass.
The corrected Linux invocation then exposed a Windows-only hardcoded Docker
path in one concrete-reader test fixture. That fixture now derives the path
from its temporary directory. The production freeze is unchanged; classifier
tests require fresh binaries on both systems after this test-only correction.

## Executable verification

Go 1.26.7 compiled `./internal/generatedingress` separately for Windows/amd64
and Linux/amd64, with `CGO_ENABLED=0`, `GOTOOLCHAIN=local` and
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`. Exact anchored parent selectors
were discovered before each suite and executed with
`-test.count=1 -test.timeout=10m -test.v`. Linux ran natively under WSL.

| Suite | System | Parent results | Subcase results |
| --- | --- | --- | --- |
| Classifier | Windows | 10 passed | 71 passed |
| Classifier | Linux | 10 passed | 71 passed |
| Existing behavior preservation | Windows | 12 passed; 1 POSIX-only skip | 42 passed |
| Existing behavior preservation | Linux | 13 passed | 42 passed |

All four groups have zero failed tests. The sole skipped parent is
`TestGatewayRebindStartupPresenceDoesNotRepairDirectoryPermissions`, which
explicitly skips on Windows and passed on Linux. The Windows preservation
result is retained from the preceding binary: independent review proved that
the portable-path correction changes only a classifier fixture, introduces
no initialization/global/shared-call-path changes, and preserves all nine
production and six preservation-test source identities. Both classifier
suites were rebuilt and rerun after that correction.

Coverage includes sealed outer-entry empty/stable/refusal cases, exact
retained-V1 prepared recovery and crossed-extra refusal, complete app/LAN
census without application traffic, observation changes, resource discovery,
protected-history families and limits. Concrete reader/attestor controls and
pure canonical-helper negative controls remain distinguished from outer-entry
coverage; passing them does not imply every negative case runs through the
outer entry.

The final matrix receipt
`classifier-final-matrix-successor-20261022T231148Z/receipt.json` has SHA-256
`83cf1ade9dbf7f64d220aa04a04040d890b7b21071ca5759850dade0078b9b70`.
Independent evidence audit SHA-256:
`3da67b59343359bd534b25a440bdda6694f8f9e3f3c7c100fd15f78f0cffdc3d`.
It checks all 1,064 current Go/module source entries, 19 frozen source files,
raw output/discovery rosters, three binary bindings and the test-only delta.
The full source digest remains
`4b1e80fcd0166fe5785c8e716cc947d4f9cf8da97d495431c3df19041cb60da7`.

| Raw evidence | SHA-256 |
| --- | --- |
| Windows classifier output | `a95cd1534f3d685a3bd89e4272f0b6e6147d3f0f43928bd69b042fec765310e8` |
| Linux classifier output | `c966f37c1f27b27163cf8cbdba34bb80506548d6237a311c90d24f72ba206ff3` |
| Retained Windows preservation output | `5b59bd81b00e0e26c4c5c5a7cc4055bd1cd8538f4780745de7ffa2aac4eeacf8` |
| Linux preservation output | `ef8e609eac93359c03b428c7b9ec40308c9edcabc9a2cd43edfd137d45746df5` |
| Successor Windows binary | `c49823bfd04a2f1757fbc951e15f0f08117d8f246e7861c9b3857e76d1e057ed` |
| Successor Linux binary | `ea541f5776c438aa7b9d336f754c32eeb44936c9246b5adbe6d2380fac376b4d` |
| Retained Windows preservation binary | `1c6e528374b81ebdf151393fada201232f03f7e0d9aa14f9676f6bb5e4681a70` |

Local evidence lives under
`temp/m3-lan-external-db-20261015/m4-startup-classifier-verification/` in the
workspace wrapper. Static analysis, production builds, live-tag compilation,
and canonical generation passed in the separate final gate receipt
`classifier-final-gates-20261022T231810Z/receipt.json`, SHA-256
`9a1636449f544656ffe944ca357ea1d12194ed510897116428bb38b37e9e3d08`:

- Read-only `gofmt -d` on all thirteen changed Go files returned no diff.
- Windows and Linux `go vet` and `go build` passed for `generatedingress`,
  `generatedruntime`, `generatedexecutor`, `controller`, and `cmd/hostd`.
- `go test -c -tags=live_docker` compiled `generatedingress` and `cmd/hostd`
  for each target system. These four binaries were not executed as Docker
  tests.
- Canonical `pwsh -NoProfile -File scripts/check-generation.ps1` passed.

Local Docker and race execution are unavailable. Hosted Docker/race and
physical LAN acceptance are not implied by fake-runner tests or compilation.

## Remaining work and rollback

Repeated equality across SQLite, protected files and Docker is not an atomic
transaction. It does not exclude privileged external writers or
indistinguishable ABA replacement. File fingerprints do not establish a
same-open-handle guarantee. A future factory must repeat fresh observations at
effect and publication boundaries and retain uncertainty as failure.

This unit has no production startup caller. Private construction, create-only
empty bootstrap, atomic consumer migration, retirement and retained accounting
remain separate implementation work. It does not finish M4 or change external
database ownership, provision managed databases or activate a controller.

Before production wiring, rollback is a source revert of this reader and its
factoring. There is no application-data migration or runtime-resource rollback
in this unit.
