# M4 generated-domain consumer cutover

Status: draft [PR #222](https://github.com/tyhuang9/rig/pull/222), initially
published at `c676c5ca669ca3162fcdb3bdfb183e264aa78a8b`. The selected local Windows and
native Linux matrix and four added regression tests passed with explicit
environment/live-test skips. The first hosted run subsequently finished with
68 failed checks, 61 successful checks and two skipped checks. Repairs are in
progress, with the reviewed follow-up checkpoint recorded below through
`213f8415c0ee4409de05f5eb9d26c9fadabdc130`; this is not hosted acceptance. Receipt-owned predecessor retirement
remains open. M4 is incomplete.

The current status and final verification sections supersede earlier pending
checkpoint descriptions below. Historical failures and source-only reviews are
retained with their original acceptance limits.

## Follow-up hosted run: 23 October 2026

The reviewed repair checkpoint was published to the same draft PR at
`c5b68a1253d98f3b21a014d7bfc8aa6d04133eae`. GitHub's synthetic merge
`ba6d2922d42160544461c6a84180133a8f558b45` has the unchanged PR #220 base and
this head as parents. Its tree `850c39cedf3c5853b05af39ecdbd694968aa7874`
equals the published head tree.

The first six completed failed jobs were retained separately from the original
run. The TLS controller, gateway readiness and final-handover journeys now
reach a bootstrap **container** receipt rejection. Their logs do not identify
the exact failed container predicate. The final-handover job reports Docker
28.0.4 and a gateway container in Created state. A source comparison found that
bootstrap requires a network endpoint immediately after create, whereas the
existing stopped-container validator distinguishes configured networking from
an attached endpoint. This is a repair candidate requiring verification, not
proof of the observed predicate or permission to weaken identity checks.

The two migration jobs still reject omitted completed-domain dependencies, and
the legacy composition job rejects missing predecessor migration provenance.
The latter now has a focused local repair below; the live controller fixtures
remain open. Generic construction failures cannot all be assigned to one cause.
This run is ongoing; no hosted acceptance or milestone completion is claimed.
The bounded diagnosis record is
`ci-followups-20261020/pr222-c5-bootstrap-container-diagnosis-20261023.md`,
SHA-256 `2c000e2abdbf3a3170a747ef9eb2df7e9afc5f467a03b8a8b15725acc0dc6d45`.

The later Linux executor job `120672535525` identifies the constructor's
retained-root-or-database-identity conflict branch. It does not establish inode
reuse or justify removing retained identity checks. The Windows core job
`120672535046` separately exposes a diagnostic fixture passing a value runner
where construction requires pointer identity. Both remain repair work.

The 22:27:04 UTC snapshot on `c5b68a1` contains 42 failed, 37 successful,
15 running, 47 queued and two skipped checks. All 42 completed failure logs
were retained once. These are check counts, not distinct defect counts. The
crash-matrix aggregate failed because its prerequisite matrix was skipped; it
did not execute a runtime test. Snapshot SHA-256:
`a9dbf3b2398f557b1fc2f7fb889264d6711c97cfd83aba1162b1ba346468b61b`.

### Complete application-network fixture census

Local commit `1b21667b08d7183bf8249060cf375a4c81943379` repairs two test fixtures.
They now bind every application network from the existing physical observation,
with equal nonempty name/ID censuses, validated identities and deterministic
ordering. This replaces the unrelated single-network template binding; the
production full-census checks and drift assertions remain unchanged.

The initial five-parent Windows command hit Go's default ten-minute package
timeout after four complete parent passes and eleven child passes. The fifth
parent had not completed. Its successor ran only that parent with the existing
CI timeout (`-timeout=32m`) and passed it plus two children in 234.16 seconds.
The combined evidence is five parent passes and thirteen child passes, with the
original timeout preserved. Both runs used `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly`, and `CGO_ENABLED=0` on the same two reviewed file hashes.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- Initial run: `typed-network-20261023T164643/raw.log`, SHA-256
  `d7857c9f0f473b02b5674c72a0d2ef9e55f377a40d6386503bbc56188a8b8dc4`.
- Fifth-only successor: `typed-network-20261023T164643-fifth/raw.log`, SHA-256
  `ea27b8d90254c2dcf21391f6057fc9698068b2492d153d52f9c9e99d31e364eb`.
- Source review: `typed-network-source-review.json`, SHA-256
  `cf140b326e167ba10b5fbbd66c282f5c8076412ca99c223ced728673e3ac8b5b`.
- Independent raw/command/source audit: `typed-network-root-precommit-audit-20261023.json`,
  SHA-256 `8936023788023a87db88307b13799aa167113abb56a5c06a264926e141163731`.

These focused tests do not establish Docker, race, or full-suite acceptance.

### Canonical legacy fixture and deliberate drift boundary

Commit `c43c29b70f3c1dfbd64dc8aedc5f967cf9eeb471` preserves the original
canonical predecessor runner and migration family through the current-state
fixture. The real redeploy receives all five serving-roster fields, including
release and slot. It does not invent provenance or rewrite history. The
existing malformed-runtime-head test additionally removes migration 042's
activation trigger in its deliberate-corruption setup; production guards and
the refusal, zero-copy and eleven-progress-record assertions remain intact.

The first four-parent run passed the deliberate-drift parent but failed three
legacy parents because the roster projection omitted serving slot. After that
projection was corrected, a three-parent successor passed both census/lineage
parents and the concrete legacy composition parent, including commit and
rollback children. The successor ran from 22:06:49 to 22:18:16 UTC, exited zero,
and used `-count=1 -v -timeout=32m`. Its recorded environment overrides are
unset; it must not be described as using the initial run's explicit overrides.

Evidence under `pr222-ci-repair-20261023T2125Z/` includes the original
`legacy-drift-20261023T170228` and successor
`legacy-successor-20261023T220649Z` command, raw and before/after source records.
The independent audit is `legacy-fixture-root-precommit-audit-20261023.json`,
SHA-256 `d66ac415dac16f0f0901818662d61434386fe8dda1b4852d8150d50c65872026`.
Scoped source review SHA-256:
`64533eb3a19eb65f7d22efee664deaf080cc8364cbe28ed5ea21777d82f5d2d0`.

### Bootstrap networking before and after acknowledged start

Commit `213f8415c0ee4409de05f5eb9d26c9fadabdc130` validates network topology
against the private receipt's acknowledged start phase. Before start, Docker
may omit the configured network ID and network membership. Any nonempty ID
must match the independently observed receipt network; any visible member must
be the exact receipt container with its expected name and address. The stopped
container still requires exact configured networking, requested address and
gateway priority, with no endpoint ID, runtime address or effective host port.
After acknowledged start, the exact network ID, member, runtime address and
effective host bindings are mandatory before the live configuration check.

Ownership, census, image, volume, physical identity, protected state, SQL state
and fence checks remain. Lost acknowledgments and drift still retain uncertain
resources without retry, adoption or cleanup. The original hosted logs do not
expose inspected fields, so the exact hosted predicate remains unproved until
the successor Docker run.

The focused Windows command passed four outer parents and 38 unique children,
including omitted/exposed stopped topology, strict running topology, crossed
identities, sticky drift and lost acknowledgments:

```text
go test ./internal/generatedingress -run '^(TestGeneratedDomainEmptyBootstrapReceipts|TestGeneratedDomainBootstrapPhaseTopologyValidation|TestGeneratedDomainBootstrapPhaseDriftIsSticky|TestGeneratedDomainEmptyBootstrapEffectBoundaries)$' -count=1 -v -timeout=32m
```

Evidence: `bootstrap-phase-20261023T222156Z/`, actual 22:21:56–22:22:12 UTC,
exit zero; its environment overrides are unset. Raw SHA-256:
`7d289be6f300f5186142e927c1fdc53619792c39398c02c7e17e4769d1cea2c6`.
Independent audit `bootstrap-phase-root-precommit-audit-20261023.json`, SHA-256
`20e54332af9fcea3c1890220c6d5e706862c53ef4ad64641eedb844c3675e1f3`, checks the
raw census, unchanged source bytes and static artifacts. Scoped security review
with its additive stopped-projection clarification has SHA-256
`f0879cca0012902bcc068a635809702a7d58d4f7e517e2769645ae4371a0786e`.

Readonly formatting, `go vet ./internal/generatedingress`, and
`go test -tags live_docker ./internal/generatedingress -run '^$' -count=1`
passed on the frozen source with explicit local toolchain, readonly modules,
CGO disabled and Windows/amd64. Static receipt SHA-256:
`44c314d952b71b074b0774390b68548158e4ecb4736ed6bd7487e810feec7aa8`.
Tagged compilation ran no Docker tests. Additional fixture lifetime,
constructor, deliberate-drift and cold-process controller repairs remain open;
the full suite and hosted Docker/race gates are not accepted by this checkpoint.

The existing canonical-generation check also passed on the frozen source via
PowerShell 7.6.5. Its first attempt failed in the Windows PowerShell 5 harness
because `Get-FileHash` was unavailable; that failed attempt is retained. The
successful `bootstrap-canonical-20261023T222859Z/receipt.json` has SHA-256
`aee4cda1d2e6b803d99fc00bedad034e690535e0231a8a7a64c2ec8424dffbae`.

## First hosted run: 23 October 2026

The terminal snapshot at 21:15:54 UTC covers the exact published head above.
GitHub tested merge `badbe7e60ee6a6351514974bb3de39ab5ad89cb8`, whose parents
are the PR #220 base `46e0885c5c850d7cfe901b4ec142c151f30eac7a` and that
PR #222 head. Its tree `bb507663a860897a227674032e60b31bebe8d4e7` equals the
published head tree. The failures therefore apply to this source revision.

All 68 completed failed-job logs were captured once, with job metadata and
SHA-256 receipts. The terminal failure census has SHA-256
`c805ebf01b47ae29be07d6be758f3a4d683b8245be8a7ac56502b633e2703946`;
the check snapshot has SHA-256
`04425d0cd0de69c0bbe5ae81f219307bb1b69f16353fb7e7ee04e117e5f0d4a1`.
They are retained under the local acceptance evidence directory
`m4-lifecycle-coordinator-verification/ci-followups-20261020/` as
`pr222-terminal-failure-census-20261023.json` and
`pr222-checks-20261023T211554Z.json`. Counts describe checks, not distinct defects.

An additive JSON-event census clarification,
`pr222-terminal-json-census-erratum-20261023.json`, has SHA-256
`110a785e4f9164e9fe1f90a9100ccc73534dde7ec167fece69a47572fbac8c99`.
It distinguishes actual test failure events from echoed subprocess output.
The registry cleanup-after-fatal control passed: its intentional child failure
was echoed by the successful parent. The two candidate-head tests are the actual
failing tests in that Windows ingress-remainder job. Original logs and the
original text census are preserved.

Initial diagnosis identified these separate causes or investigation areas:

- Docker bootstrap compares an expected nil options map with an observed empty
  map. The repair must normalize that equivalent representation while retaining
  exact resource identity, labels, mountpoint, creation time and census checks.
- Three live ingress fixtures pass a not-yet-created child directory into
  protected controller-directory preparation. The fixture must create its own
  private state root first; secure temporary-directory validation stays intact.
- LAN and migration controller journeys still omit the completed generated
  domain required by normal composition. Initial and restart fixtures must use
  the sealed opener and supported domain construction, preserving real Docker
  execution and ownership checks.
- Several historical-state tests use direct writes now refused by migration
  042. Their historical or deliberate-corruption setup needs correction while
  preserving production activation guards and the original assertions.
- Ordinary package execution exposes construction-authority failures that the
  selected local matrix did not establish. The cause remains under investigation;
  a fixed-daemon-ID hypothesis was withdrawn after inspecting the published
  unique-ID fixture implementation.
- Additional rebind fixture failures and two timeout signatures remain to be
  resolved. No captured log contained `WARNING: DATA RACE`; that absence does
  not establish race acceptance for failed or timed-out checks.

These findings supersede any inference that the selected local matrix proves
the entire hosted suite. Local repair checkpoints and subsequent hosted results
will be recorded separately. The E5 retirement worktree is held until this
baseline is repaired.

### Bootstrap and private live-state repair

Local commit `b40cc5b55a316d1a340d2e67862cadfef3711f5d` changes five files.
Expected bootstrap volume/network options now use the same `cloneStringMap`
canonicalization as the observed census. The bootstrap fixture supplies the
actual nil-volume-options representation. Three live ingress fixtures create
their own private child state directory before protected preparation.

Verification on Windows used:

```text
go test ./internal/generatedingress -run ^(TestGeneratedDomainEmptyBootstrapReceipts|TestGeneratedDomainEmptyBootstrapEffectBoundaries)$ -count=1 -v
go test -tags live_docker ./internal/generatedingress -run '^$' -count=1
```

Both commands exited zero. The first passed two outer parent tests and 13
unique child scenarios: 11 outer subtests and two subprocess-owned receipt
scenarios. Echoed subprocess repetitions are counted once. The second command
only verifies tagged compilation; it does not run Docker tests.

The bounded security review found no weakened identity, namespace, census,
authority or effect-ordering check. Evidence is retained in
`m4-lifecycle-coordinator-verification/pr222-ci-repair-20261023T2125Z/`:

- `bootstrap.raw.log`: SHA-256
  `a1cdb92adf59715268c8e281de86268acd4276365f9294a75aac24e194ec138b`.
- `bootstrap-options-source-security-review.md`: SHA-256
  `772c458ffeaeb26b05f96fa1ef70f27f795be677cd890d0f140f90865d1fa50a`.
- `bootstrap-root-commit-record-20261023.json`: SHA-256
  `c962867b7dbdfc7ab88dabe994107ef98c578b5a1b13f9729e1e37f2c54461cb`.

This checkpoint does not establish live Docker or Linux race acceptance.

### Cumulative ingress race timeouts

Both failed remainder jobs reached the unchanged 32-minute package deadline.
Job `120324870922` had completed 284 parents totaling 1903.41 seconds; its active
parent had run for 16 seconds. Job `120324872741` had completed 281 parents
totaling 1844.34 seconds; its active parent had run for 75 seconds. These logs
support splitting the cumulative workload, not declaring the active parent
deadlocked. The retained timing record is
`pr222-ingress-timeout-parent-durations-20261023.json`, SHA-256
`7fb86178f45dac8077460d681163d6ff4b77de0b90f15d9bec9759829510bcbd`.

Commit `eebffe2959c3a58fbee16870eb0dae78c0c08ee6` separates complete generated-domain construction, stable
startup classification, other domain startup, capacity, and gateway-operation
parent families from the remainder. All three workflow matrices use the same
selectors. Race detection, the 32-minute Go timeout, 34-minute hosted step
timeout, complete subtest trees and aggregate gates remain required.

The shell contract passed 60 argument checks, 24 representative parent-selection
controls, three exact workflow matrices and 40 refusal/failure controls. Both
shell syntax checks and `git diff --check` passed. The receipt is
`m4-lifecycle-coordinator-verification/pr222-ci-partitions-20261023T213117Z/receipt.json`,
SHA-256 `31fe0a0c5b4cb883a338b77bc47f88f89ef724922d7a02dc73569ddb7dfbd1af`.
It uses stubbed Go output with the existing pinned real jq executable; it is not
Go test execution. The first attempt at `213039Z` failed because jq was absent
from PATH and remains recorded.

Actual Windows `go test ./internal/generatedingress -list ^Test` discovered
1,201 unique parent tests. All 31 partitions were nonempty; their union contains
every discovered parent exactly once, with no missing or duplicated parent.
Root independently expanded every include/exclude filter from the actual shell
source and matched those selections against the retained discovery output.
This proves selection coverage for that discovery, not test execution or Linux
race timing. The root audit/commit receipt has SHA-256
`0e5779f6036f9c9434ca90f83f3432707e15dacc0a15751cccb6798c74231e31`.
Hosted execution of the revised partitions remains pending.

### Constructor refusal attribution

Commit `312308608bfb133490ef8c35ddb97e48a8dde9b8` adds four closed diagnostic
categories to existing constructor refusals: invalid post-inspection input,
retained root/database identity conflict, retained daemon namespace conflict,
and input that changed before admission. `errors.Join` retains the existing
deployment-execution configuration sentinel. No identity predicate, lock
boundary, admission rule or effect ordering changes. Diagnostic text contains
no paths, IDs, database rows, raw causes or credentials.

The focused registry parent passed with six unique subprocess child scenarios.
Source and security review passed. This direct Windows command recorded CGO
disabled, `GOTOOLCHAIN=auto` and empty `GOFLAGS`; it must not be described using
the pinned environment from a different checkpoint. Evidence is in the repair
directory's `constructor-attribution/` folder:

- Coordinator source/evidence audit: SHA-256
  `44e1648e007a16b40dc4aa0c1e40cdcaaffce10d8ddf838a0f02e5d1ed202a21`.
- Root commit record: SHA-256
  `7d55166f1742ae7cb47ebf7976ce1dfb19b7e68f0d1733b975e15054dbd16c8a`.

The underlying hosted failure remains unresolved. Native Linux package,
repeat and temporary-directory-churn attempts did not reproduce it. No
filesystem-identity hypothesis is accepted as the cause, and no retained
registry entry is discarded by this change. First-failure sequences from the
hosted logs are retained in
`pr222-first-authority-failure-sequences-20261023.json`, SHA-256
`284a69b14bb9631aff4d911c512f6711eee30116e5cd30efbf9ab578f8f75e44`.

### Historical and authority fixture corrections

Commit `c1b6c13247347bd619c3e6cb55a81c99ac510b5a` changes five test files:

- The live assembly source assertion follows the sealed database opener and
  completed-domain composition helper now used by the fixture.
- Migration 037 tests use the checked-in migration set through 037, preserving
  the release-identity boundary they are intended to test.
- Named disposable malformed-state fixtures account for the new 042 guards
  when deliberately constructing states that production correctly prohibits.
  Production guards and the downstream refusal assertions remain intact.
- Candidate self-head tests provide a canonical receipt for their positive
  read-only/successor cases and explicitly refuse missing or malformed receipts.
  They retain predecessor-mutation, generation-drift and overflow checks.

Eight selected hostd/database/deployment parents and both candidate-head parents
passed on Windows. A third typed-network parent in the shared candidate-head
log belongs to separate unfinished work and is excluded from this checkpoint's
acceptance. Bounded independent review found no actionable issue. The root
five-file commit record has SHA-256
`a1a68eed95c4f6cf62a762e77478512bf579b8586f5533fd4f5b1b42902b8b62`.

### Follow-up checkpoint static checks and limits

Readonly formatting, vet for `cmd/hostd`, `internal/database`,
`internal/deployments` and `internal/generatedingress`, and the existing
canonical generation check passed. Those commands used `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly` and CGO disabled. The 1,191-file source manifest was
unchanged across that static run, with SHA-256
`e4a41f01042de17c71df23dbafaf9bb335e19db7244d8b8b66f5f9eee07e9cdc`.
The static artifact manifest has SHA-256
`cbf618c77e3d2a17a0f036674be12b65746e7a1a919700f228286ee487b3840b`.

The static worktree included an unpublished test-only network-fixture change.
Another related test-only edit followed that run. Both are excluded from this
publication checkpoint; the static evidence is not an exact clean-checkout
compile claim. Published production/source changes and selected regression
evidence have separate file hashes and bounded reviews. Final documentation
checks and the exact published head are recorded in the publication receipt.

Remaining work includes current hosted constructor attribution, live controller
composition/restart fixtures, typed/rebind fixture corrections, the bounded
relay-outage setup failure, actual Docker/race acceptance and receipt-owned
predecessor retirement. The follow-up is a draft CI checkpoint, not milestone
completion or deployment approval.

The tested activation and cold-accounting core is recorded in local commit
`59489c20eda5124b9408e8402e51c7a329cf1dde`. Local merge
`fdbc84ac8da5cc1dca7f32190223d74654926b5c` preserves that core and incorporates
[PR #221](https://github.com/tyhuang9/rig/pull/221), the separately verified
setup-focus test correction. The merge retains all 28 core file blobs and both
PR #221 file blobs exactly. Normal controller/executor wiring was subsequently
committed in `3783596b3bbdd043fa1b49344759e368b286aecf`; the later retry corrections
are described at the end of this record.

## Purpose

Connect normal controller startup, generated deployment routing and retained
runtime accounting to the same generated-domain owner. This is one cohesive
change: the public factory, consumers, accounting and closure of ordinary
Manager mutation methods must be delivered together.

Frontend and backend containers remain hosted by Rig. External databases remain
application-owned and configured through scoped runtime secrets. This work adds
no managed database or Neon provisioning.

## Accepted starting point

[PR #220](https://github.com/tyhuang9/rig/pull/220), commit
`46e0885c5c850d7cfe901b4ec142c151f30eac7a`, provides the private construction
prerequisite. Its [separate record](m4-generated-domain-private-construction.md)
describes the accepted scope and evidence limitations.

The local combined baseline is commit
`34e6435617e812e876956433e0c9dd60b475d784`, tree
`313b5f55e613df897518cef29414bf7c89b96256`. Five explicit merge commits preserve
the complete reviewed histories of PRs #214, #215, #216, #218 and #219. They
combine CI partitioning and diagnostics, static-server diagnostics, and the
separate LAN and registry fixture corrections. PR #214 includes the PR #208
route-pause attribution work.

All 51 contributed files retain their exact input Git blobs: 35 code/test
files, five workflow/script files and 11 documentation files. The five inputs
add 37 paths relative to PR #220, with no conflicting paths or conflict
resolution. This is source-integrity evidence, not public-runtime acceptance.

## Required implementation boundaries

- One completed domain owns the Manager view, candidate authority, route
  authority, ledger/head family and capacity budget. Normal composition must
  not adopt the startup Manager or allocate a second budget.
- Route publication and activation require the original deployment claim,
  complete candidate and predecessor rosters, exact head/route epochs and a
  durable activation receipt. An uncertain commit cannot authorize restoration.
- Retained accounting must use the same budget as candidate replacement.
  Ordinary restart requires narrow reconstruction from exact persisted
  receipts and observed resources. Missing, crossed or ambiguous ownership
  evidence remains a sticky refusal; descriptive snapshots alone do not grant
  authority.
- Startup must finish its protected typed-recovery decision, release the
  outer effects lease before construction, then reacquire it and freshly
  reattest the same completed domain before composing workers. Construction
  owns its internal exclusions; callers cannot supply a success proof.
- Recovery-only startup continues through its existing typed path and creates
  no normal generated domain. Public ordinary `Switch`, `Provision` and
  `Recover` must become unconditional refusals only with their consumers moved
  to the domain services.
- Keep the exact SQL and receipt-label checks that prevent legacy removal of
  receipt-owned predecessors. Receipt-owned retirement remains separate work;
  a safe retirement pause must not be relabeled as successful deployment.
- Preserve the route-pause stage/cause diagnostics from PR #208 and the
  immutable history and uncertainty records used for recovery.

Cold-accounting reconstruction is a deliberate addition to the earlier cutover
map. PR #220 refuses retained generated state; wiring it directly into normal
startup would reject retained-host restart. Its empty bootstrap also acquires
the effects lease itself, so calling it while the outer startup lease is held
would contend with that lease. These findings motivate the two startup changes
above; they do not retroactively expand PR #220's acceptance.

## Combined-baseline verification

Executed on 2026-10-23 with Go 1.26.7, readonly modules, CGO disabled and explicit
Windows/Linux amd64 targets. Linux binaries ran natively through WSL. The
selected parent names were discovered before execution; a missing parent fails
the check.

| Package | Selected parent |
| --- | --- |
| generatedingress | `TestGeneratedDomainConstructionJoinsOneCompleteOwner` |
| generatedingress | `TestGeneratedDomainRegistryFencesLegacyConstructionAndTypedActivity` |
| generatedingress | `TestGatewayCurrentLANCommitRecoveryWithdrawsAfterAuthorityLoss` |
| generatedingress | `TestGatewayCurrentStateMachineGrantCommitsAndQuarantinesFailedResolution` |
| generatedexecutor | `TestGeneratedExecutorRoutePauseDiagnosticControls` |

Each platform passed five unique parents and 12 unique subcases, with 19 raw
RUN/PASS events including isolated subprocess echoes. There were no failed or
skipped tests. Test compilation, production builds and vet passed for hostd,
autodeploy, generatedexecutor, generatedimage and generatedingress on both
targets. Ten compiled test binaries are retained; only the selected ingress
and executor parents were executed.

The canonical `pwsh -NoProfile -File scripts/check-generation.ps1` passed.
The existing Bash contracts also passed:

- `scripts/check-generated-runtime-race_test.sh`: 42 selector invocations,
  three workflow matrices and 16 refusal/failure cases.
- `scripts/check-relay-outage-diagnostic-projection_test.sh`: three accepted
  and 22 rejected diagnostic projections.

The original harness omitted the task-local jq directory from its Linux PATH.
After all Go and canonical checks had passed, its first Bash step exited 127
with `jq: command not found`. The retained, checksum-verified jq 1.8.1 binary
was then used for a separate exact-source shell check. Both scripts passed;
no Go tests were rerun. The failed harness receipt remains part of the record.

All 1,071 Go/module source hashes remained unchanged through the baseline.
Aggregate diff checking passed. Previous immutable unit evidence is reused for
the independently reviewed inputs; this finite selection checks their new
interactions without claiming whole-repository acceptance.

### Local evidence identities

Evidence lives under
`temp/m3-lan-external-db-20261015/m4-domain-consumer-cutover-verification/`.

| Record | SHA-256 |
| --- | --- |
| Whole-parent integration | `b63cc6bd5bf91130294d5b52da44a205fee2adcf2bb0dd143a3d19d869062392` |
| Exact parent source union | `75f9d18f705a9b8b18d6a59ea534c422ac36987965b6d032b82c0567ce10e02e` |
| Original Go/canonical harness, including jq PATH failure | `910c20baaa487dde1173aca3cc8886499666e3baae3bb139a69b55f8edc9e8c5` |
| Corrected shell environment | `1a24a55620fa8160482dbdc09637828199329ebbc3ed34ac5a283f47ce51bd39` |
| Accepted combined baseline | `5f4b640753aac5238d8202e4d93f2dd3271f2caa646bfc71b5ca313b8c1795cb` |

## Focused activation checkpoint

On 2026-10-23, four Windows tests passed against a retained 14-file source
snapshot: two database migration tests and two route-authority tests. The
test binaries were compiled with Go 1.26.7 and the environment described above.
The selected runs used `-test.v -test.count=1` with
`-test.run=^TestGeneratedRouteActivationsMigration` and
`-test.run=^TestGeneratedRouteAuthority`. All four selected parents executed;
none failed or skipped.

The route tests exercise the physical route path through test drivers and the
original deployment claim. They verify a single receipt/head/roster commit,
refusal of copied authority and execution values, refusal of reuse, and a
changed claim after route proof causing zero SQL activation commit. These are
local controlled tests; they do not establish live Docker behavior.

Security review of the provisional interface found two issues: an exported
proof-bearing writer could accept caller-authored activation evidence, and
shallow Go value copies could duplicate the intended one-use execution. The
successor moves the writer into the private route owner, checks original
receiver identity, and shares execution liveness state. Scoped review confirms
both corrections in the tested snapshot. The original findings and separate
copy-boundary erratum remain in the evidence record.

| Record | SHA-256 |
| --- | --- |
| Focused activation receipt | `5a441a812ebe88845e5b765ff99406d5a1e6f6f36befae9a56e19a7370e1cf00` |
| Tested 14-file snapshot manifest | `ae6e8444b56e80793fe65458b7e6cdf2b8886be39ca35398ade7c47c1249709d` |
| Scoped successor security review | `e9f0d3d8e88d276ee109274201d50210152c40d0b427ad32d0ea44d3cce50a1a` |

This checkpoint does not accept the complete consumer change. Native Linux
execution, transaction failure injection, lost-acknowledgement cases,
predecessor restoration, concurrent calls and the final consumer inventory
remain required. Startup and shared-accounting implementation is still in
progress. Two preceding harness failures are retained separately; they are
not counted as successful test runs.

## Completed-join and claim checkpoint

Two further Windows parents passed on 2026-10-23:

- `TestGeneratedDomainConstructionJoinsOneCompleteOwner` holds the outer
  effects lease during the join and verifies the same owner family is reused,
  protected state bytes remain unchanged, owner registries remain empty, and
  no constructor mutation or capacity probe is dispatched. It also checks
  Docker and accounting drift refusal, successful exact rejoin after Docker
  drift is removed, and rejection of a different environment owner.
- `TestGuardRouteActivationUsesExactWaitingHealthStage` passes four subcases:
  `waiting_health` is accepted; `applying`, `preparing`, and `succeeded` are
  refused.

Both compiled test binaries ran with `-test.v -test.count=1` and an anchored
selector for the respective parent. The join's isolated subprocess repeats
its parent in the raw log; it is counted once. All parents and four subcases
passed, with no failures or skips. The retained receipt binds commands,
environment, both binaries and all raw logs:
`541d7b676c508b97c44af92c7f69d67bd24b4acf8f41d128ec540bfa423b55ae`.

Its 1,175-file before/after manifest covers Go, module and SQL sources and is
unchanged during the run. This scope differs from the Go/module-only baseline
manifest. These controls establish the bounded join and claim behavior;
retained-accounting reconstruction and normal controller composition still
require implementation and verification.

## Expanded migration checkpoint

The three selected Windows migration parents passed on 2026-10-23:

- `TestGeneratedRouteActivationsMigrationFreshSchemaAndMirror`
- `TestGeneratedRouteActivationsMigrationUpgradeHasNoHistoricalBackfill`
- `TestGeneratedRouteActivationsMigrationActivationGuards`

They cover the embedded/public schema mirror, repeated migration without
fabricating historical receipts, unchanged legacy head identity, immutable
receipts, duplicate or crossed activation refusal, and head/phase guards.
The guard test also exercises valid receipt-linked head and phase transitions.
Each rejected operation checks the retained state afterward.

The exact anchored selector was discovered and executed from a Windows test
binary; all three parents passed with no failed or skipped tests. The receipt
is `310257140e522b254a86cc5f52f02d00218c570ecc086d3368acb4b85a1a4c5f`.
It binds the binary and the changed test file's before/after hash. This focused
checkpoint does not supply a complete source manifest or replace final
cross-platform transaction and consumer verification. Earlier fixture and
harness failures remain retained in the local evidence.

## First cold-reconstruction checkpoint

The Windows `TestGeneratedDomainConstructionReconstructsColdAccounting` parent
and its `quiescent_current` subcase passed on 2026-10-23. The fixture creates,
starts and health-checks a candidate, activates it through the sealed route
operation and original claim, and finishes its runtime state before constructing
a new owner from retained evidence. The reconstructed manifest identifies the
same active deployment and component without adding a duplicate capacity charge.
The test also rejects unexpected mutation or capacity-probe commands during
this quiescent reconstruction.

The exact selector was
`-test.run=^TestGeneratedDomainConstructionReconstructsColdAccounting$/^quiescent_current$`,
with `-test.v -test.count=1 -test.timeout=90s`. One parent and one subcase ran and
passed; none failed or skipped. The receipt is
`3bc20f8800f128f08236e756dc3b31080931c588edb21e989fa203a018bbf9c5`,
the retained raw log hashes to
`979485daf6acb06df597a416992ea0f60d3173895fd23105509b0f757218c5af`,
and all 1,177 Go/module/SQL source entries were unchanged during the checkpoint.
The binary hash was independently matched to the receipt.

This controlled-driver test reaches the real constructor and ownership code;
it is not live Docker acceptance. It does not exercise acquisition for an
interrupted deployment, concurrent drift, every cold phase, or native Linux.
Implementation continues after this source snapshot, so final verification
must bind the completed consumer source separately.

A separate read-only security review found no actionable issue in the retained
18-file production snapshot. It checked exact durable/resource joins, capacity
retention and lock order, and the cold-only gateway eligibility change with its
full static, restart-config, live-config and nonrouted challenge checks. The
snapshot manifest hashes to
`eef3167ba7c52245692712c6977e46a06a4973f802656234d68ded100f6cedeb`,
and the review to
`9fb5e87328a556c3f734d908507c6f0b733f50be0c00b363c71ec27f8b491575`.
This scoped source review does not close the remaining execution tests or
accept the complete consumer integration.

## Retained-capacity and transaction-failure checkpoints

A successor Windows run passed both `quiescent_current` and `retained_risk`
under `TestGeneratedDomainConstructionReconstructsColdAccounting`. The latter
creates a real candidate through the sealed path, leaves it in the interrupted
health-waiting state, and reconstructs its exact receipt and retained capacity
charge. It also verifies one capacity probe during acquisition. One unique
parent and two unique subcases passed; the separate invocations produce four
raw RUN/PASS events with no failures or skips.

The receipt is
`a2c09e90517f1573f893c97f8fd22b1abc73d60b1175d0c389805dc757f74593`.
It corrects the original report's omission of indented child PASS lines by
parsing the preserved raw logs. The original receipt remains retained, and
this reporting correction did not rerun tests.

The same compiled binary then passed three activation parents and 11 subcases:

- `TestGeneratedRouteActivationTransactionFailuresRollBackAllSQL`: four
  reached statement failures and a deferred-constraint failure at commit;
- `TestGeneratedRouteActivationExactPersistedReplayAndCrossedFields`: a real
  sealed activation before exact replay and crossed receipt, roster, route
  proof and original-claim rejection;
- `TestGeneratedRouteExecutionIsOneShotAcrossConcurrentAndFailedOriginalUse`:
  concurrent one-use enforcement and consumption after a failed use.

The activation receipt is
`6c64daff20f6d2346d30696bd24a4010a3a7e5b15edeb3aae86b59ef66231b63`.
All 14 raw RUN/PASS events were verified, with no failures or skips. Each
checkpoint retained an unchanged 1,177-entry Go/module/SQL source manifest;
both binary hashes and all executed test logs were independently matched.

That checkpoint did not establish an actual lost acknowledgement after a
durable commit or predecessor restoration. The next checkpoint covers those
two cases; the complete cold-state matrix, native Linux execution, and live
Docker behavior remain outstanding.

## Shared accounting and predecessor checkpoint

On 2026-10-23, a successor Windows binary passed seven parents and seven
subcases, with 14 RUN/PASS events and no failed or skipped tests:

- Three candidate-accounting parents reject foreign process/environment
  composition, altered options, copied owners, foreign or crossed release
  handles, repeated release, and invalid counts. They verify fixed memory/disk
  limits and contention between retained and replacement capacity in the same
  budget. Interface aliases share the original release state.
- `TestGeneratedRouteActivationResolvesLostDurableCommitAcknowledgement`
  injects an error after the real database commit succeeds. It verifies exact
  durable evidence without restoring the previous physical route.
- Three predecessor parents exercise authentic successive activations for one
  application, rollback and physical restoration after a precommit failure,
  and retention of the proved physical successor when the head cannot be read.

The command compiled `./internal/generatedingress` and ran the exact seven-name
anchored selector with `-test.v -test.count=1`. The retained receipt is
`773c93400c677f2a724edaa537933dee925c7ebe8584569846ba8a4d99dc585b`.
The raw log hashes to
`723f9a053f77e5da54abdd28f7ba4618f3647d194ed5dffd2e3dc888de9accd0`,
and the binary to
`621c64db3f2b7108fb6465c0682976900c3197cd92612bb97d7aeb9f7b943377`.
All 1,083 Go/module entries in its before/after manifest were unchanged; this
manifest does not include SQL files. Raw results, binary identity, and source
manifest equality were independently checked.

This is a focused controlled-driver checkpoint, not completed runtime-adapter,
controller-startup, native Linux, or Docker acceptance. Runtime release timing
and the complete consumer integration still require their own checks. The
earlier unbound four-parent report is superseded by this saved checkpoint.

## Consumer source review checkpoint

A separate security review found no actionable vulnerability in the ten held
consumer production files. Its report hashes to
`174a7fd11ecac51242f9a3a85da7d6a9134749ced16d5ebe35aa67510980252c`;
all ten source hashes were independently matched after the review. The scope
included exact candidate-owner identity, opaque once-only retained charges,
runtime release conditions, route activation, startup lease handoff, and the
existing SQL and container-label predecessor deletion guards.

The preceding source review identified one diagnostics regression: adapting
every route failure erased its safe state/context cause. The correction keeps
only a closed cause enum in the error wrapper and retains no underlying error
or unwrapping method. The successor security review includes that correction;
execution of the migrated diagnostic tests remains pending.

This review does not accept the temporary public candidate constructor or
registry as final architecture. Their removal, ordinary manager mutation
closure, authentic consumer fixture migration, and the final executable
verification remain required before publishing the complete cutover.

## Controller composition checkpoint

A focused Windows run on 2026-10-23 compiled `./cmd/hostd` and executed four
selected parents with `-test.v -test.count=1 -test.timeout=180s`:

- `TestRuntimeCompositionEnablementAndRouterSelection`
- `TestHostdDaemonAssemblyRunnerComposesGeneratedRuntime`
- `TestGeneratedCompositionUsesExactCompleteDomainOwner`
- `TestGatewayRebindStartupFreshDatabaseTransitionsToExactDomainOwner`

All four parents and seven subcases passed, with 11 RUN/PASS events and no
failures or skips. The tests cover enablement/router selection, runner assembly,
and the fresh startup sequence: protected preparation, lease release, domain
construction, lease reacquisition, exact same-owner join and composition. A
stale checkpoint is refused. The receipt is
`3c7e739ddefb5258332f16e7eed7a859394dfddf350501454c9ab9c0b164c37c`.
Its raw log and binary hashes were independently matched, and all 1,183
Go/module/SQL source entries remained unchanged during execution.

The controlled Docker transcript used by these fixtures passed its own
`TestGeneratedDomainSharedTranscriptConstructsExactEmptyOwner` control. Receipt
`031bd0b0980b654b438c519f2afa01434225bc805bcc1a4cb5d83db4cd811b16`
binds the unchanged 1,183-entry source manifest, compiled binary and raw log.
One unique parent repeats through three subprocess isolation levels; all three
RUN/PASS events succeeded. This is fixture fidelity evidence only.

Neither checkpoint runs Docker or establishes retained-host recovery, the
complete executor/runtime integration, or native Linux acceptance. The migrated
live Next.js fixture still requires tagged compilation and hosted execution.

## Remaining acceptance and rollback boundary

At the local core checkpoint, `go build ./cmd/hostd ./internal/autodeploy
./internal/generatedexecutor ./internal/generatedimage ./internal/generatedingress`
passed for Windows and Linux amd64. Read-only `gofmt -d` was clean for all 25
changed Go files. Receipt
`489426b714e6871e9acb588fa6cb201a8b1a5bdf83d94db80877c456a5750dae`
binds the 27 held core paths and both module files across those gates. The
separate in-progress predecessor test was excluded from this commit boundary.
An additive fieldwise manifest comparison corrected a reporting-only JSON
representation mismatch; the original receipt is retained and no gate was
rerun for that correction. These builds are not Linux test execution.

Completion of implementation, the remaining failure/boundary tests, aggregate
security and source review, final caller inventory, and hosted Docker/race
verification remain outstanding for this cutover. Local Docker and race
execution are unavailable.
No live controller activation or deployment was performed to establish this
baseline. M4 is incomplete.

Before activation, the cutover can be withheld or reverted as one change while
retaining its reviewed prerequisites. After any later activated run, a source
rollback does not authorize deletion or adoption of residual resources,
discarding capacity charges, or rewriting immutable ownership evidence.

Migration 042 is additive and retains immutable activation rows. If that
migration has run, preserve it during a source rollback. Its head-update and
route-to-draining guards intentionally prevent an older binary from performing
the former split activation writes. Rolling back code therefore does not
restore the old activation behavior. Removing the schema, fabricating receipts
for historical activations, or downgrading the database is outside this
rollback procedure.

## Hosted predecessor-retirement failure evidence

On 2026-10-23, the completed Docker jobs for draft PR #213 at
`8af5c8d0a8dc00b062f4948eb583c4ef4e6598ef` reported the closed diagnostic
`previous_retirement / retirement_unavailable`. The GitHub test checkout
`39d38ab1ee3d75a7ca704b8d4f775d7ea81cbfdc` and the published head both have tree
`18ed9feea98eda5dc0843bf3d87d9e95d7b34e1f`; the checkout includes the expected
base and PR parents.

| Hosted job | Result and observed boundary |
| --- | --- |
| [Authenticated controller/TLS](https://github.com/tyhuang9/rig/actions/runs/39853566454/job/120085802070) | Failed while retiring the previous deployment |
| [Migration approval/uncertainty](https://github.com/tyhuang9/rig/actions/runs/39853566355/job/120085801499) | Failed while retiring the previous deployment |
| [Two-application LAN redeploy](https://github.com/tyhuang9/rig/actions/runs/39853566381/job/120085801717) | Failed while retiring the previous deployment |

The process-kill recovery job `120085802491` and external-database LAN job
`120085802380` reached the same retirement boundary. These results establish
an unfinished receipt-owned retirement path. They do **not** establish a
successful redeployment or successful recovery. The SQL and physical receipt
ownership checks remain required; a safe pause is not an acceptance pass.

The six-job capture (including the separately failing repository relay test)
is recorded in `pr213-six-failures-triage-20261023T0357.json`, SHA-256
`791ea3240e29ebaca21b3606bae1d62937a768408afc68478866a8f36f8fb19e`.
It binds each raw log hash, discovered test count and checkout identity. The
relay log supplies a failed test location but no specific state diagnosis;
its earlier projection repair is already in the local integration baseline
and still needs hosted confirmation. These are evidence from the published
predecessor, not executable acceptance of the current uncommitted consumer
integration.

## Controller restart fixture source review

The controller Docker journey now creates fresh child controller processes for
both restart boundaries. Each child acquires its own controller-owner lease,
opens its own sealed database, reconstructs the concrete domain, and starts the
real worker and API. Parent database handles are used only for queries. Build
and archive-fetch counts are collected from the children and carried across
the second restart; capacity pressure uses bounded opaque reservations on the
same domain budget.

A separate source security review found no blocking finding in this fixture
change. It bound `cmd/hostd/hosting_controller_live_test.go` to SHA-256
`3e23f62962045a22fb4a668c346363533e1cce6863aa78b944da4f61f7bd28c7`
before formatting. The review artifact
`controller-restart-file-protocol-source-security-review-20261023.md` has
SHA-256 `5a49e322512fcd538ee10f04a9302ad6b6c7cbb841efdfb5568f85241173ce91`.
The root independently read the review and matched both hashes.

This review covers process ownership, bounded private file coordination,
shutdown, counter continuity, capacity handles and preservation of the durable
assertions. The protocol test and actual Docker restart journey have not yet
been accepted on this source snapshot. Source review is not runtime evidence.

## Capacity resume through the concrete route owner

On Windows amd64, Go 1.26.7 compiled the shared domain transcript and executor
packages, then passed these five explicitly discovered parents:

- `TestRunnerServesExactCapacityProbeByContainerName`
- `TestRunnerAppliesExactGatewayRouteConfigAndPromotion`
- `TestRunnerTracksExactGatewayAttachmentsAndCanDelegateCapacityProbe`
- `TestRunnerServesGatewayReadinessOnlyOverExactAttachedNetwork`
- `TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement`

There were five RUN/PASS events, no subcases, failures or skips. The executor
control preserves accepted configuration and plan pins through two capacity
pauses and a changed configuration head, then completes the same deployment
on its third attempt with one compile, candidate and route activation. Its
route delegate uses the authentic domain authority. Process responses remain
controlled; this does not establish actual Docker or cold-process behavior.

The earlier attempt failed at route activation because the shared transcript
omitted the fixed Caddy readiness command on the attached application network.
The correction recognizes that exact command and verifies the tracked network
attachment. It does not relax production admission or route checks. Earlier
failed receipts and the earlier missing-parent discovery evidence are retained.

The accepted checkpoint ran from 04:23:03.906Z to 04:23:10.150Z on 2026-10-23.
`focused-executor-capacity-20261023T042303Z/receipt.json` has SHA-256
`7b3cb13bcab6756ad766f367f169a52d8a0118b0bf8c1d97e362791b3c722863`.
The root matched all four raw-log hashes, both compiled binaries and equal
before/after manifests covering 1,087 Go/module files. The manifest excludes
SQL. Its SHA-256 is
`9a6c5f51cd77717894045850355a2597bb57514fdb014f65fe76650d557a463d`.
Helper output is bound by
`9742ff63087ffd2ac737e4bb72d44b6219224a9dac51dc1e04e81a0208b3388d`;
executor output by
`ad9006e8b1ceebe44fe2e6d416a427cdf92f8e9a21a4d2a8c0865e7d5b32e8bb`.
Final consumer, Linux, live Docker, retained recovery and receipt-owned
predecessor retirement acceptance remain open.

## Capacity diagnostics through the concrete domain

A focused Windows amd64 checkpoint compiled `internal/generatedruntime` and
passed both selected parents with three subcases (five RUN/PASS events and no
failures or skips):

- `TestGeneratedRuntimeCapacitySourceFailuresPreserveClosedDiagnostics`
  covers source failure, cancellation and deadline expiry.
- `TestGeneratedRuntimeRejectsCapacityBeforeDockerMutation` preserves the
  capacity-before-effects refusal.

The source change keeps the fixed capacity request and output truncation checks,
while preserving cancellation before converting source errors to closed
runtime diagnostics. It needs the final security review of the combined diff.
This is controlled-process evidence, not Docker, cold restart or full consumer
acceptance.

The actual run was 2026-10-23 04:27:25.216Z–04:27:34.567Z.
`focused-runtime-capacity-20261023T042724Z/receipt.json` has SHA-256
`83c08404b00c9818f0f75ebfd5d4647f8ad922598ab7148c6c046fa083a46e8b`.
The root independently matched both raw logs, the executable hash, the exact
parent/subcase discovery and equal before/after manifests containing 1,088
unique Go/module entries. The manifest excludes SQL and has SHA-256
`0c486a9f6cc6a6b179afcc87b80afc6c9f5b250593368140ccd9606065b9253b`.
Test output is bound by
`e50d1073e6abe9a9f9dfb819a29eea17057ed55d759443721505bd554acb127f`;
the executable by
`72367ea6644689fcee691f3edb2499e09edbaca7dd584e5e01d3e8d8331cb9db`.
Earlier failed attempts remain recorded.

## Gateway primitive preservation and ordinary-manager refusal

A focused Windows checkpoint passed 16 selected parents and 44 subcases
(60 RUN/PASS events, no failures or skips). It covers committed-v2 route changes,
protected rollback, reload-only crash recovery, candidate/network rejection,
observation budgets, legacy format markers and lock contention. These tests
call the actual private production primitives. They do not copy deleted
Recover/Provision orchestration into test-only dispatchers and do not establish
full domain construction acceptance.

`TestOrdinaryManagerMutatorsRefuseEveryShapeBeforeGatewayWork` additionally
checks nil, zero, constructed and copied Managers, both legacy and committed-v2
states, and nil/live contexts. Public Switch, Provision and Recover must return
validation refusal without candidate authority, gateway lock acquisition or
Docker commands. Actual successful activation and recovery are covered through
the sealed domain tests and the explicitly identified private primitive tests.

The two renamed positive parents are
`TestCommittedV2PrivateSwitchObserveRecoveryCoreAndSnapshot` and
`TestLegacyV1FenceAllowsPrivateRouteAndReadPathsWithoutMarkers`.
Earlier milestone evidence retains the historical test names; no active CI
selector references those old names.

The actual checkpoint was 2026-10-23 04:38:42.412Z–04:38:55.846Z.
`focused-manager-v2-version-20261023T043842Z/receipt.json` has SHA-256
`5ee81b5a8df6a480a9fe87a43da3798e2e7be4091747d844b637e0b1108d9a58`.
Its preserved metadata incorrectly split the recorded `-test.run` argument;
the additive `receipt-argv-census-erratum.json` records the actual single
argument and exact discovery, SHA-256
`29d2c4fa4e5c7ad73f6f0fdfc72f7313ba82a46ea6c1939b5b3c249ac5b1a9a6`.
The root matched both raw logs, the binary, all discovered cases and the three
bound test source files. Test output SHA-256 is
`85c39abecba97ac66e46c116a04ecc5c076388d1adb31812b867b5a32b97c28a`;
the binary is
`0272bfa53a1d1f2099a68edac8516bd16888fe359c72fc0c5f2e2e3639917b0f`.
This narrow receipt does not include a complete production source manifest.
The final combined verification must close that gap; Linux and Docker remain
unverified by this checkpoint.

## Legacy active-head writer guard

`TestComponentLifecycleRejectsLegacyActiveHeadWrite` passed on Windows at
2026-10-23 04:41:19.666Z–04:41:22.276Z. It preserves the component lifecycle
checks, then requires two legacy write attempts to hit the exact activation
receipt guard. Both attempts leave the full head, healthy component and runtime
phase unchanged and create no receipt. Invalid draining and stale-generation
refusals remain covered. Successful activation and replay are exercised by the
sealed route authority tests.

`focused-runtime-state-legacy-head-20261023T044119Z/receipt.json` has SHA-256
`a1d4707335185b1ff2e0e4f73df5656ed750b7f3061f75e9a506a9572917dd8f`.
The root matched the one explicitly discovered passing parent, raw output
`a9058ab4879ff4dfb34c286dc0d5beeec51f9cd6c9dfd099aa9732ea5d2d0555`
and test source
`e72b9b3b6f855393b588580d0a01a658d6e1802c3aa747ab331c23255993bb0c`.
No child, failure or skip occurred. This receipt binds only that test source
and `go test` output; it retains no executable or complete source manifest.
The final combined verification remains required.

## Cold restart activation lineage checkpoint

The focused Windows cold-boundary test passed at 2026-10-23
04:49:22.254Z–04:49:28.284Z. A receipt remains immutable after its original
worker attempt; canonical recovery may advance that same job's current attempt.
The classifier now accepts the closed range `1 <= creator attempt <= current
attempt` while retaining exact job, application, deployment, head, roster,
predecessor and receipt joins. Zero, future and crossed-job controls refuse.

The real no-effect successor restart, retained-risk resume without a second
capacity charge, and stopped-physical-disagreement refusal all passed. This is
one parent with seven distinct child paths (four direct and three nested).
The raw transcript has 15 RUN/PASS events because isolated children are echoed;
it is not evidence of 15 independent tests. There were no failures or skips.

`focused-cold-lineage-successor-20261023T044922Z/receipt.json` has SHA-256
`78f29cfc6261d0871b402289706b045ab5612f2f1a9a84adfd53447fc313967f`.
The root independently matched compile/run logs, the retained Windows binary,
all discovered cases and equal manifests containing 1,089 unique Go/module
entries. SQL is excluded. Raw output SHA-256 is
`15c8931a0e7296e8289ed0c022908db51c4a4c44c3977270bd27df3629184807`;
the binary is
`77f996ec21301f8ea93b38d8126c793544d123f0616d85667ea0bc0edf67c54f`.
The source manifest is
`8dd6bfc746295a3cad72773606a75fb3be209cc8b9b14172c23ab3f937d352b2`.

The scoped security report
`cold-activation-attempt-lineage-source-security-review-20261023.md`
(SHA-256 `2ff682d95efc302c9763faffb4556e6b4b26885d31b895b0a0909bf62559e483`)
returned GO with no findings. It binds production source
`74640876112718296e6de4d48a6fbeff40e178c8a99fa891255626cc8ef994bf`
and focused test source
`c396b44235ef0b28c52b5c6a8f372c7234ba6198d2b963c3598e041d1a276768`.
Earlier failed runs remain recorded. Linux, race, live Docker and the final
combined consumer verification are still required.

## Hosted retirement evidence remains open

PR #208's hosted crash partition 06 failed in
`TestLiveControllerGatewayRebindCrashMatrix/scenario/pending-route-restart`.
It reached a waiting-user reconciliation state with both components active and
explicit `previous_retirement/retirement_unavailable`. This is evidence of the
remaining predecessor retirement implementation; it is not a successful
redeployment or grounds to weaken the success assertion.

The [hosted job](https://github.com/tyhuang9/rig/actions/runs/39838080772/job/120107403827)
tested checkout `3e3758d59358db6079f80499dbc0b825c4c605ce`, whose tree
`ebdd1527a46260a86d32f0f6fef12852af6dde43` matches that PR head. The retained
raw log has SHA-256
`463a96acaa9102af23f8d70648583989465260257e004760c624f5f8a4c18fc4`.
It records 26 parents (25 passed, one failed) and 393 children (392 passed,
one failed). Triage `pr208-pending-route-retirement-triage-20261023T0501.json`
has SHA-256 `20ca28d9fdd68bbf7e633df100af1f7378fecbcc17bafdd36752fe8c66716056`.

## Historical SQL fixture conversion awaiting execution

The app-access fixtures now distinguish pre-042 stored history from current
runtime activation. A private, package-local schema template applies the actual
001–041 migration files once, checks contiguous discovery, checkpoints SQLite,
and copies the empty template into an isolated database for each historical
case. It does not alter the ordinary current-schema `appAccessDB` helper.
Historical component and head transitions retain the original SQL constraints.
The public migration upgrades populated history before current assertions and
requires zero newly minted activation receipts or receipt-bound historical heads.

Extra-head and repeated-rebind cases finish their historical runtime sequence
before upgrading. The current v2 SQL-constraint tests still exercise the current
schema. Existing corruption tests explicitly remove the new activation guard
only within their deliberate corruption setup so their read-refusal assertions
remain reachable. Positive fixtures never remove that guard.

Two tests that require a real current mutation retain the sealed domain owner:
`TestGatewayRebindRuntimeHeadsReadsAreIndependentAndDoNotMutateState` and
`TestHostingGatewayStartupRuntimeCensusUsesOneTransaction`. The latter keeps a
separate sealed reader and performs the genuine replacement inside its existing
read-snapshot barrier. The replacement remains in its actual draining state;
these tests do not claim predecessor retirement or successful deployment completion.

The source handback records 18 files and preservation of all 66 existing test
entry points. `appaccess-historical-source-handback-20261023T0535.json` has
SHA-256 `fa82d98082073c47c8bafb2bdc8ca03e883e3d9c9dd537c55deb5d4dfb2d5790`.
Its hashes precede formatting and exclude the two new external-adapter files.
`git diff --check -- internal/appaccess` passed. Compilation, runtime results,
review and final source binding remain pending; this is not acceptance evidence.

## Additional hosted retirement failures

Four completed PR #214 jobs again reached the explicit
`previous_retirement/retirement_unavailable` boundary:

| Hosted gate | Result |
| --- | --- |
| [Controller/TLS journey](https://github.com/tyhuang9/rig/actions/runs/39860576366/job/120109924417) | Replacement remained waiting for reconciliation |
| [Migration approval and uncertainty](https://github.com/tyhuang9/rig/actions/runs/39860576372/job/120109925303) | Expected successful deployment remained paused |
| [Two-app LAN journey](https://github.com/tyhuang9/rig/actions/runs/39860576531/job/120109924808) | Replacement retirement unavailable |
| [Process kill and recovery](https://github.com/tyhuang9/rig/actions/runs/39860576366/job/120109924101) | Original job did not converge after before-build restart |

The tested merge checkout `61f4fb789593e387724da767169b1b34ecd92cef` has tree
`937b4f9c74adf43782c3341772f3618861f06c7c`, equal to exact PR head
`dbf7c091f094361a30aeb2d0e6a24f9f967b4a9e`; both were verified through GitHub's
Git commit API. Each completed job was captured once with immutable metadata
and raw-log hashes. Triage
`pr214-four-retirement-failures-triage-20261023T0536.json` has SHA-256
`c5200675c03b4ea62aaddb6e029b1567677824cbd598bc6edc448e86f07c903b`.
These are failures of the published predecessor baseline. E5 retirement remains
required, and the existing successful-deployment assertions remain unchanged.

## Historical migration checkpoint and snapshot coverage hold

On Windows, the exact two `TestLANGatewayRebindSQLLedgerMigration` parents
passed at 2026-10-23 05:37:09.040Z–05:37:09.384Z after compilation and explicit
discovery. They preserve populated schema-033 history through the actual 034
migration and verify the dormant mirrored migration. The coordinator matched
both retained executables, all six command logs and equal source manifests
containing 1,190 unique Go, module and SQL entries.

`focused-historical-snapshot-20261023T053652Z/receipt.json` has SHA-256
`ab574b5dca08d0bc1f5d62180af68cbe55b4650a4a97e2bb182875628df26501`.
The migration run's raw log is
`b4bc3fe304031db8104fded5e50474aae153de549ee95bf64304fb7bc04a7aa3`;
its executable is
`77669cfc6f7e37d8131b5e33367a2fe4ddc2b590e0a4b858d56986e2af1b6d86`.
The shared source manifest is
`74b51385787325cd6146e701e4b36a4916dc6ffcfa4aa35e80216f6e7649e2ab`.

The same receipt records five startup-snapshot parents and 25 children passing.
That execution is not accepted as closure of the original snapshot contract:
four malformed generation-zero read cases had been replaced with current write
guard checks, dropping their read-refusal and no-write assertions. Review
requires restoring those original negative read cases within their deliberate
corruption fixtures. The passing 034 checks are unaffected. The independent
audit has SHA-256
`ba174bdc111cf1b7badebfa17ad2bb650861b9d6659b93cc569b00c96a6015f5`.
Linux, race, Docker and combined consumer verification remain open.

## Snapshot coverage restored and additional focused checkpoints

The snapshot coverage hold above is resolved by the 05:41 successor. All four
malformed generation-zero cases now first prove the current activation guard
refuses the write without changing the head, then construct deliberate corruption
and require the original invalid-state error, empty result and zero writes during
reading. The root inspected these assertions and matched the compiled executable,
raw logs, explicit discovery and unchanged source manifests for each checkpoint:

| Windows checkpoint | Actual test interval (UTC, 2026-10-23) | Discovered passing cases | Receipt SHA-256 |
| --- | --- | --- | --- |
| Snapshot read guard (`focused-snapshot-read-guard-20261023T054126Z`) | 05:41:29.325–05:41:41.532 | 5 parents, 25 children | `c26cffe3bba5d3aed0a3f1305b4e7c87e61a0bbd61078f2b997e549cadce7ed1` |
| Retained cancellation (`focused-retained-cancellation-20261023T054020Z`) | 05:40:26.576–05:40:30.296 | 2 parents, 3 children | `71b4c3d9ebd66d3b916a2d5a84e386bdeaccadcdb9ac36955a665b4408737b36` |
| Current observation deadline (`focused-current-observation-20261023T054340Z`) | 05:43:52.503–05:44:44.471 | 1 parent, 5 children | `f16229fd7e77917feb3a0b2ef61cdca3172589263331b3dc084f4787e8536900` |

Each receipt binds 1,190 unique Go/module/SQL source entries before and after
execution, with no failing or skipped cases. Their coordinator audits have
SHA-256 values `5f3a416b2e37fd6a355eb6f97edf1535573389dc780305a4bb6e5e76a2ad0645`,
`85c4f793a44f1d42e971d8bb32f8355c634bca24309aa722b72f8cb7a96d0d1b`, and
`776f5ea3360c1680c8c79ab01e0076c1f7e85615527f3a7e5800caab2d48feb3`, respectively.

Retained capacity now preserves the closed cancellation diagnostic when the
capacity source reports cancellation while the caller's context remains live.
Current-rebind observation now separates its short lock/selection budget from
the caller-capped physical-observation budget. Focused controls cover the default
budget, caller deadline, cancellation, pending state and crossed protected/SQL
state without fallback. The earlier three-child observation run passed but
security review identified the deadline defect; its evidence is retained.
Successor security review of the corrected deadline is pending. These focused
Windows results do not establish Linux, race, Docker or complete consumer closure.

The public authority closure review returned scoped GO with no findings;
`consumer-public-closure-source-security-review-20261023.md` has SHA-256
`cae0243239ca10d873efe80931e63983d61125b4e1e1f573f8ccc6162da869b5`.
The root matched all nine complete-file bindings. Its Manager coverage is limited
to unchanged unconditional mutation-refusal methods while the separate observation
repair proceeds; it does not claim whole-file review of that repair.

## Lifecycle operation fixture conversion awaiting execution

The shared retained-serving fixture in `internal/applifecycle/operation_repository_test.go`
now builds its baseline under an isolated cached copy of the actual 001–041
schema, applies the original constrained component/head/runtime transitions,
and migrates to the current schema before any lifecycle operation is exercised.
It checks that the exact succeeded deployment, active head and full component
roster survive without invented activation receipts. All 17 test bodies and
non-fixture helpers are byte-for-byte unchanged; ordinary lifecycle database
fixtures remain current. The final source diff check passed after removing an
extra trailing blank line.

The pre-format source SHA-256 is
`f8033d7bbe3198e713fd658ce830232aaee3d3f4bcdb3ef11b54459cefd7de2b`.
`applifecycle-operation-historical-source-handback-normalized-20261023T0543.json`
has SHA-256 `29182686dbeb28ed515a21f0663c6fae92c35c56e08bbaaf0e1084d32aa2d543`.
This is a source handback, with compilation and execution still pending.

## Observation review closure and authentic appaccess checkpoint

The deadline finding is closed by the scoped successor security review
`current-rebind-observation-deadline-security-recheck-20261023.md`, SHA-256
`98e9d936b59ff2899e6f508f6efc7be7ad9452e5958457a26336f42b53c14c89`.
The root matched its three exact source bindings and focused receipt. Lock and
selection retain their short budget; exact current rebind observation receives a
fresh caller-capped budget under the same locks, with selection revalidation and
closed cancellation/refusal behavior. This resolves the earlier pending review;
it does not establish complete consumer acceptance.

The appaccess conversion also received a source-only coverage review with no
findings, preserving all 66 existing parent tests. Its coordinator transcription
`appaccess-historical-coverage-source-review-20261023.json` has SHA-256
`7a730b087c74649abeab97be5077b7151b3b64171d261f8f9dbc363a14b0a771`.
The root matched the three authorized successor source hashes. The shared binding
audit is `root-source-review-binding-audit-20261023T0556.json`, SHA-256
`1a6b2109fe113f54a912f6a22538dcfa012f734996bba60de9f0c1a15531eae3`.

Three appaccess parents passed on Windows from 05:50:27.649 to 05:50:30.807 UTC:
independent immutable reads, the one-transaction census barrier, and supported
historical startup state after reopen. The first two exercise actual domain
activation and retained physical identities. Explicit discovery matched all
three, with no failures or skips. The root verified all command exits, raw logs,
executable and equal 1,190-entry Go/module/SQL manifests.

- Receipt: `focused-appaccess-runtime-adapter-20261023T055024Z/receipt.json`,
  SHA-256 `3334163f1215f35487a864f3f67d2dbd710be62e72508b8197b0848f0383667b`.
- Test log: `190239a87e446fc0336ca5c8fe81763f77433e4ab1ffcf9674b5122daaade530`.
- Executable: `1aeb3995c25c97ecb2d9c4e41be8e7a7c325c15620554a275970f7e03cb0d172`.
- Source manifest: `b149e7f9399278084233b72c0148b2cb4f1a2977ce0adb8644f3edc8acb07d4d`.
- Independent audit: `f0f1416c44b1883ba05b125a53174e8d1a87c0523bec539622d5bbde1a930fe8`.

## Remaining observer and guard fixture handbacks

The pending-route SQLite observer control now creates its completed serving
history under the actual cached 001–041 schema, migrates publicly, and checks
that the exact head and component survive without invented activation receipts.
Its current pending-deployment setup and all later observer/privacy assertions
are byte-for-byte unchanged. The live observer itself is unchanged. Source
handback `hostd-pending-route-historical-source-handback-20261023T0556.json` has
SHA-256 `09930e0a441c88ff378b55cff9e58ece0a811b0e3ec2c83fd6a21d159cabb089`;
pre-format source is `a2373f3a377f900ba33dffeba80a1c36d47a7d7047b541a7f7218a6f9ededf7d`.

The execution guard fixture now creates its serving baseline through one sealed
owner and domain, the original candidate session, real health/route activation,
and published history. The same owner and exact per-container/network physical
observations support subsequent applications. Pins include the actual protected
configuration and plan revisions. All 15 original test bodies are unchanged;
a bounded exit-one subprocess helper supplies the physical absence result.
Source handback `guard-authentic-activation-source-handback-20261023T0604.json`
records the actual timestamp internally (the filename is only a label), SHA-256
`d22a8e5e8799bc9461a81b07d008b841628eef8f8a0b34377dc6632d706701ff`;
pre-format source is `ee258e678e1d49755ddad160378aa6aaf0a49bc025f37de993f695c32c4e99b7`.

Both handback diff checks passed. Their source author ran no Go or formatter;
execution belongs to the coordinated verification lane. These handbacks are not
test acceptance. Linux, race, Docker, final aggregate review and the full consumer
matrix remain open.

## Additional PR 214 hosted failures

Two additional completed jobs were preserved once at 06:01:58–59 UTC. Both logs
identify checkout `61f4fb789593e387724da767169b1b34ecd92cef`, whose tree
`937b4f9c74adf43782c3341772f3618861f06c7c` was already verified against immutable
PR head `dbf7c091f094361a30aeb2d0e6a24f9f967b4a9e`.

- [Repository race job 120109923870](https://github.com/tyhuang9/rig/actions/runs/39860576426/job/120109923870)
  hit the generatedexecutor package's 40-minute deadline during
  `TestGeneratedExecutorRoutePauseAttribution/final_runtime_advance`.
  The active stack was parsing SQLite migration SQL in `database.migrateFS`,
  called by `database.Open` in the sealed executor fixture. Across its recorded
  test events, 1,309 parents and 1,997 children passed, seven parents and two
  children skipped, and one parent/child remained unfinished. This is a timeout,
  with no prior test assertion or race report establishing a runtime defect.
  Raw SHA-256: `146d4df56fb961c877a81c214e9d52295963d8a961cda8202c05a54bc6f5a468`.
- [LAN database job 120109925179](https://github.com/tyhuang9/rig/actions/runs/39860576531/job/120109925179)
  again reports `previous_retirement` / `retirement_unavailable`, with the exact
  job waiting for reconciliation and its successor runtime draining. Four
  parents passed and the strict LAN journey assertion failed. It remains the
  known E5 implementation gap.
  Raw SHA-256: `de4979a262e0254259b66e6824bb20ca8b727cbf2e299b7974384f8b90d92e0e`.

The coordinated implementation review is evaluating reuse of the existing empty
schema cache before opening the real sealed domain connection. Any such change
must preserve production file/lease validation, test coverage and the existing
race timeout. No optimization result or hosted acceptance is claimed here.
Triage `pr214-repository-deadline-and-landb-triage-20261023T0604.json` has SHA-256
`5869525969e2572ee026f20a4a8e460102858d2db8a39b00abefe1f80f2676bd`.

## Completed consumer implementation and retry corrections

Commit `3783596b3bbdd043fa1b49344759e368b286aecf` saves the controller and executor
consumer conversion and its ownership fixtures. The executor cache optimization
now prepares the existing empty migrated template before creating files, closes
that ordinary database handle, and then opens the real sealed domain under its
controller lease. It changes no production migration or opening path. Its
performance under hosted race execution remains unverified.

The aggregate code/caller review returned GO after correcting one withdrawn
finding. The retained `Repository.SwitchActive` method has no production caller;
migration 042 refuses ordinary active-head mutation, while its self-head no-op
does not write. The source review does not require deleting this guarded API.
The additive correction record is
`consumer-aggregate-code-caller-review-correction-20261023.md`, SHA-256
`7d07a283c3bce71054ad14f98621ed3e3e0027659ab8859977c30d56717f577d`.

Commit `08b07d6930b036767a21375252129457159487f8` adds read-only attestation before
resuming a runtime already in Draining. Durable activation alone does not prove
that the physical route still matches. The same sealed owner now joins the
current claim and attempt, activation creator lineage, active head, full rosters,
protected recorded plan and exact physical route under the observation locks.
Failure pauses before predecessor cleanup or completion; retry does not publish
the route again. A genuine second attempt retains the original creator attempt.

The initial security review found a missing join to the durable receipt creator.
The correction checks creator job identity and a positive creator attempt no
greater than the current claim attempt. Focused Windows execution passed five
parents and three children, including physical drift, stale/future current
claims, crossed creator job, zero/future creator attempt and genuine resumed
claim behavior. Receipt
`focused-route-reattest-lineage-20261023T064131Z/receipt.json` has SHA-256
`89467ade9734d565e3cd3c19d434d1e193c43d684d98b95b36d5e82a9abc4350`.
Its equal 1,177-entry source manifests include Go, modules and controller SQL;
they exclude 14 unrelated relay SQL files. Scoped successor security GO is
`draining-retry-lineage-security-successor-review-20261023.md`, SHA-256
`d72402b08c845339fb12fa20c3678c06fac2bea10fefd6e6bdea8037c7e8a4a4`.

The final matrix found that one appaccess negative control now encountered the
042 guard before the older rebind fence. Commit
`1ebfeb37f2d5cb2d4f31f88c1528a3f42c6099af` adds an assertion for the new refusal,
then removes only that trigger inside the disposable negative fixture so the
original rebind-fence and unchanged-history checks still execute. The focused
parent and all four children passed. Receipt
`focused-appaccess-runtime-head-guards-20261023T065634Z/receipt.json` has SHA-256
`3d26eeca96482e7015ae829f651bb5dad84e7ad584278ea21133c21554f26dac`.

The matrix also exposed a production retry defect after repository-head drift.
The plan store labels an exact previously accepted revision Superseded when a
newer revision becomes current. Commit
`9743793fc3f76775fd51dbaf0f4cde3e0395c2fd` accepts that derived state only for the
runtime's recorded app, plan ID, revision number and protected digest. Persisted
acceptance must still be accepted; latest, draft, rejected or crossed plans
cannot substitute for the original. All other claim, receipt, roster, head and
physical-route checks remain. The same commit names the empty-technology test
case explicitly, preserving its empty input.

Both regression parents and all three technology children passed. Receipt
`focused-generatedexecutor-successor-20261023T070647Z/receipt.json` has SHA-256
`1f7c83685779bdf71abe2d4b524e26dab5ee1af11f69aafbc9f7e9b49daa8676`;
the raw log is
`294ef393729a24a111acb6b52292029c0074bc0b760f988fdb4d6147032d3be3`.
The original summary missed indented child PASS lines; the preserved log shows
all three, and the additive census correction records them without rerunning.
Both 1,191-entry Go/module/SQL source manifests match. Narrow security GO is
`draining-retry-superseded-plan-security-review-20261023.md`, SHA-256
`c5a19fe274d7baecfe06356928cd51fe84dd88974ef55ddaa0f49bd242443224`.

These results are focused Windows evidence. The remaining matrix and final
review must establish the accepted aggregate; Docker and race execution still
require hosted CI.

## Final matrix checkpoints and ingress fixture repairs

The Windows executor matrix on `9743793` passed all 60 selected parents and 88
children. Generated-image execution passed 16 parents and skipped its two known
live Docker parents; two live children within the authority test also skipped.
The runner initially omitted those known skips from its allowlist. Its successor
retains the original results and binaries without claiming Docker execution.

The ingress checkpoint `final-matrix-20261023T071208Z/receipt.json` has SHA-256
`a1abe1f648f86445723b88376ace257aeec18ad7cb655ce9ea48e1c68dbb305d`.
It discovered and ran 238 parents: 222 passed, ten failed and six known live
Docker parents skipped. All 1,191 Go/module/SQL source entries were unchanged
during that run. The run-local roster and runner copies preserve the exact
selection. Earlier shared roster paths were overwritten during continuation;
their retained hashes, selectors, command arguments and raw outputs remain the
evidence, rather than the present contents of those shared paths.

The failures exposed fixture assumptions after conversion to real sealed route
activation: repeated actors used a fixed unique username; already completed
deployments were completed again through stale SQL updates; an unfinished
admission case needed an actually unfinished sealed execution; and redeploy now
performs five read-only attestations around its single physical application.
The focused 18-parent successor passed 13 parents and retained five failures.
The remaining setup activated a second app after gateway upgrade/rebind state
was already established, or supplied no gateway network census to a retained-app
startup fixture. Those corrections are under verification. No completed
aggregate or milestone acceptance is claimed at this checkpoint.

The successor fixes are saved in
`fdbe08a2cf30b95d1360546b4498a9365e905a37`. The shared two-app variant creates
both genuine serving runtimes before installing gateway profile, grant, upgrade
and rebind records. It retains exact per-app heads, endpoints and network labels.
The final-config fixture no longer drops and reconstructs rebind triggers just
to seed its loopback app. Intentional stale-head negative cases remove the newer
activation guard only inside their disposable corruption setup so the original
downstream refusal assertions still execute. Retained-app startup uses the real
public constructor and the canonical fake gateway network/container census;
gateway-only construction cases retain their original setup.

The final focused run actually executed from 07:44:08 to 07:47:29 UTC on
2026-10-23; its directory's `0752Z` suffix is a label, not its execution time.
All 18 discovered parents and 54 children ran and passed, with no failures or
skips. Receipt
`focused-ingress-fixture-successor-final-20261023T0752Z/receipt.json` has SHA-256
`77e84205f247d9d82a20eb7ebcc71a5dd61a086c13ae7e92759ac0b69fba19c8`;
raw log `64b7592f638d8715041dd2d074145c7ab72baee40313812c818b2faaeed412fe`;
compiled binary `96297e4cc68ca984722b7b7fe4d739d8482d92c9486e5296bdcdde8c39c5f4d3`.
Both 1,191-entry source manifests are
`0ee9727298dbb2dc420084e9ef7ae1ccc16569132d27cb81108e78eef1673a80`.
An independent precommit audit verified those source bytes, hashes and the raw
RUN/PASS census. The commit contains exactly the ten test files; it changes no
production guards. Eight related parents were absent from the earlier selected
roster, bringing the final standard selection to 586. Remaining aggregate and
Linux checks are still required.

The next Windows checks retained additional failures on `fdbe08a`:

- Lifecycle discovered seven parents: three passed and four failed while
  constructing an unsupported two-server explicit plan. Its three children
  passed. Receipt `final-matrix-successor-20261023T075853Z/receipt.json`:
  `7190ceade7943b52f5f51f5f10bcb30e42610a94bbd410cc157c0a54b8dcfe06`.
- Recovery passed 35 parents and 51 children. Runtime discovered 59 parents:
  53 passed and six failed; all 29 children passed. The runtime failures cover
  cancelled capacity admission, retained reservation aliases, maximum-log disk
  admission, ambiguous create ownership, whole stopped-roster reservation and
  cancelled retained admission. Receipt
  `final-matrix-windows-independent-20261023T080045Z/receipt.json`:
  `54251c217f4fa4083454164bc70f2424344c134cc0e96673eddb97549a82711d`.
  An early three-failure report was incomplete; the terminal raw log proves six.
- The independent continuation passed runtime-state 12 parents/25 children,
  runtime-executor 13/40, and generated-domain test support five parents. The
  tagged restart-file protocol passed its parent; bounded startup controls
  passed one parent and 64 children, including the required pending-route
  observation child. Tagged hostd/image compilation and the affected production
  build passed. Vet failed on an intentional copied-Manager test because it
  copies a mutex. Receipt
  `final-matrix-windows-independent-20261023T080311Z/receipt.json`:
  `6b7426eca8c4fad6829dcbefb176723abc6b24ad38f4d674876c29028fdcb305`.

These are distinct retained checkpoints, not an all-green matrix. The planned
two-component fixture correction preserves the API server and web static site,
their exact commands and both lifecycle identities. Remaining runtime fixture
and vet corrections require executable successor evidence. Native Linux final
execution remains pending at this checkpoint.

Commit `dacabb0cd080f1c4d0bc3adc4ac0e5f3b9652469` resolves those focused failures.
Its only production change checks an already-cancelled retained caller after
nil validation and before entering the effects scope, returning the fixed
cancelled diagnostic without acquiring capacity or issuing Docker commands.
Missing, foreign, expired or wrong-directory live scopes still refuse. The
focused security review has SHA-256
`9395ce8dc0a58aa7eb103f9d913411913d1242b00d434088c85c2a4c2c8ca1d0`.

The test corrections retain both lifecycle components with supported server and
static plans and exact commands, account for the real predecessor's capacity,
preserve the direct 550 MiB versus 564 MiB disk boundary, and use a stopped
container identity distinct from the fake gateway. Cancellation tests establish
both zero effects and availability of the sole capacity share to a fresh caller.
The copied-Manager negative control deliberately copies through reflection and
still proves every public mutator refuses before gateway locks or commands.

Focused Windows execution passed lifecycle seven parents/three children, the six
failed runtime parents, and the Manager refusal parent/ten children: 14 parents
and 13 children total, with no failure or skip. Ingress vet passed. Original
receipt `focused-lifecycle-runtime-successor-20261023T081300Z/receipt.json`,
SHA-256 `42166699e87999c91f2122d8b331d24e39d8a296a03c298233fdfb8837657332`,
contains pipeline output mixed into its metadata array. The preserved additive
`receipt.erratum.json`, SHA-256
`c7ada166101da5d8f5a104538b1ff6703696f3c3a09e912e9491f9ce0b89549c`,
binds the existing binaries, selectors, discovery and raw results without
rerunning. Both 1,191-entry source manifests match
`0cf0940f2d3248cb5b826d0cb620bd9351923dd99a66e63fc7bbcb2cae443a9a`.
Independent audit `lifecycle-runtime-precommit-audit-20261023T0816.json`,
SHA-256 `c95a68b73a38b9cb49da1926d25701261608797f9eac44831e60cc7490aa74b4`,
verified the current source and full raw census before the exact six-file commit.
Final aggregate/platform verification remains separate.

The bounded source integration review on `fdbe08a` found no new production
integration issue. Original report SHA-256
`9e2f3e60a6d408dd75710ade7bd5e79b00e87bc47bfdaf519672176496503cd0`
is paired with additive erratum
`be17f88ff7cf409dbfdad062778b143079dc06e79a082a55bd6e6747da5f5517`:
586 means selected tests, not completed passes on both platforms. The erratum
also records CodeRabbit's unavailability. Source review does not replace the
failed or unexecuted gates.

Additional PR #215 jobs ran its older immutable head
`31419a93c112fcfcf1e063eb427ae7a3786ece65` through the previously verified merge
checkout `3bd71c363f16edbf7b4a0359021e8450a922becb`:

- [Windows current-serving job](https://github.com/tyhuang9/rig/actions/runs/39868278165/job/120135916078)
  failed seven grant/recovery/disable parents with reconciliation refusals.
  All seven passed in the newer `9743793` Windows ingress checkpoint above.
  Raw SHA-256: `4392bdbad0c229449faa9afef416f83e9f5dd97f947e982c19299c1781ca5b47`.
- [Ingress remainder race job](https://github.com/tyhuang9/rig/actions/runs/39868278148/job/120135916163)
  reached its aggregate 32-minute timeout while the physical-identity test had
  run for eight seconds. The active stack was runnable in SQLite migration
  parsing through the identity fixture. This is a timeout, not a reported data
  race. Raw SHA-256: `013bc008356d2d9909a6e02e59b78915369789739448ce836047b0ee3b7a99a3`.
- [Controller TLS journey](https://github.com/tyhuang9/rig/actions/runs/39868278146/job/120135916304)
  observed healthy replacement containers but a job waiting at
  `route_reconciliation_required`. This older head supplies no narrower stage
  attribution, so that log alone does not identify the E5 cause.
  Raw SHA-256: `3c1f73a39c187e216cd6166921ff2823cffdbec561cdddb3e19c704ae5aef808`.

## Final selected matrix on the corrected consumer source

Source commit `dacabb0cd080f1c4d0bc3adc4ac0e5f3b9652469` completed the remaining
matrix from 08:19:00 to 08:26:24 UTC on 2026-10-23. Receipt
`final-matrix-linux-remaining-20261023T081849Z/receipt.json`, SHA-256
`85cebc450cbc2994560113899aef9d4d0aa6f25db7afaeedd22da9b45254c8bb`, binds 65
successful commands, 32 discovery/execution checks and 16 compiled binaries.
Both complete 1,191-entry Go/module/SQL manifests have SHA-256
`b245ed6af93c8e7088740f1cf80c0ea3380ab62da0e35660a3061911b56dae0b`.
The immutable run-local roster hash is
`86f6174dcf8e6e2ed5b96b16005a8068c3798aacc4473f34d9d840ac371f94d3`;
the runner hash is
`d1f6778a42f8a9f5491681be400db3673b7595d924f2c7d6e94699df5a4f2dbe`.

| Package | Selected parents | Native Linux PASS | Explicit parent SKIP |
| --- | ---: | ---: | ---: |
| Controller | 26 | 26 | 0 |
| App access | 67 | 67 | 0 |
| App lifecycle | 17 | 17 | 0 |
| Database | 5 | 5 | 0 |
| Deployment execution | 16 | 16 | 0 |
| Generated executor | 60 | 60 | 0 |
| Generated image | 18 | 16 | 2 |
| Generated ingress | 246 | 239 | 7 |
| Generated lifecycle | 7 | 7 | 0 |
| Generated recovery | 35 | 35 | 0 |
| Generated runtime | 59 | 59 | 0 |
| Generated runtime state | 12 | 12 | 0 |
| Runtime executor | 13 | 13 | 0 |
| Generated-domain test support | 5 | 5 | 0 |
| **Total** | **586** | **577** | **9** |

Windows composite evidence covers the same 586 selected parents: 578 pass and
eight explicit live parents skip (image two, ingress six). Linux additionally
skips the Node/TLS integration without its required local environment. Two image
authority children also skip on both platforms. Subprocess fixtures may emit
duplicate RUN/PASS records; the table counts distinct selected parents. Failed
earlier attempts remain retained and are replaced only by their verified focused
successors and source-impact closure.

Live-tagged controller/image compilation and discovery passed. The two runnable
tagged controls passed on both platforms: restart-file protocol, and startup
controls with one parent and 64 children, including
`bounded_pending_route_observation`. These controlled tests do not establish a
live Docker journey. Final formatting, canonical generation, shell syntax and
contracts, and Windows/Linux affected build and vet all passed. Native Linux
execution used a private mode-700 directory under `/tmp`, not a Windows mount.

Independent raw audit `final-linux-root-audit-20261023.json`, SHA-256
`997f7cd02131a22472e5ed9010082716cc5f3ce7dde8a13fcefc0dcb2989f1eb`, verifies all
65 log hashes, 16 binary hashes, discovery and raw result census, and all current
1,191 source bytes. Hosted Docker and Linux race execution remain required.

A newly completed [PR #216 current-LAN race job](https://github.com/tyhuang9/rig/actions/runs/39870494442/job/120143415012)
failed four recovery-authority parents on its older immutable head. The job has
neither a reported data race nor a timeout. Its raw SHA-256 is
`fc30d1a62dc01c08378d396dbb208dadab1ba4affdbd2c171d6fb849dad3848e`, with the same
previously verified merge checkout `93835b2b61c66fa8cf3918571d0f60d09baeee0e`.
Those four parents are outside the selected 586, so a bounded current-source
successor is required before treating them as addressed. This does not invalidate
the recorded selected matrix or imply that the full repository suite passed.

### Added current-LAN regression closure

The four additional parents subsequently passed on both Windows and native
Linux, with ten child passes on each platform and no failure or skip:

- `TestGatewayCurrentLANCommitRecoveryWithdrawsAfterAuthorityLoss`
- `TestGatewayCurrentLANCommitRecoveryRetainsAuthorityAtCompletion`
- `TestGatewayCurrentLANCommitRecoveryStopsUncertainInitialWithdrawal`
- `TestGatewayCurrentLANCommitRecoveryRejectsUnguardedDriver`

Windows receipt `pr216-current-lan-four-20261023T083259Z/receipt.json`, SHA-256
`7a7d30a5729bc431b0d7cdb89d0f88b5dfabea9b84639cc7171ee033532c441c`, retains the
successful Windows execution and a later Linux discovery failure caused by CRLF
in its shell launcher. No Linux test ran in that failed discovery attempt. The
Linux-only successor `pr216-current-lan-four-linux-successor-20261023T083614Z/receipt.json`,
SHA-256 `1225c28e945ae931e8ce0caa1c37002ed35b86a96f9170728d8c56afa5689681`, passed
after correcting the launcher to LF. Windows was not replayed. Both runs used
retained compiled binaries only after matching all 1,191 current source entries.
There were no application source changes.

The Windows raw SHA-256 is
`6bf612495f0c23d337f5f2e1e3be0f75f7f873e33c3b08ff3ff0ed943083a87b`;
Linux raw SHA-256 is
`b87bdf742c95a361dad405d86bbf85dedd5cc82450660e9a7ca5175617cc4422`.
Independent audit `pr216-four-parent-root-audit-20261023.json`, SHA-256
`ca535aa3dfe2c6bb16b2064626f5cb104b187d2321d25058aa4f9d222078674d`, verifies the
source/binary/raw hashes, exact four-name discovery and complete parent/child
RUN/PASS census. These results supplement the 586-parent matrix rather than
changing its historical totals. They establish current local regression coverage;
the race detector must still run in hosted CI.

The independent matrix QA report, SHA-256
`af2e3d9b4be7bf4804b8e1c22fade157adc5acc23b45ad8a012867a5d094fbab`, reconciles
the Windows composite and final Linux results. The publication evidence also
retains the final integration review and the final documentation receipt. The
documentation gates use the checked-in scripts: `pnpm --dir docs install
--frozen-lockfile`, `pnpm --dir docs check:workflow`, `pnpm --dir docs build`, and
`pnpm --dir docs check:accessibility`. Production source remains frozen at
`dacabb0cd080f1c4d0bc3adc4ac0e5f3b9652469` during these documentation checks.
