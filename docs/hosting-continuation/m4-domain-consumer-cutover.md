# M4 generated-domain consumer cutover

Status: draft [PR #222](https://github.com/tyhuang9/rig/pull/222), initially
published at `c676c5ca669ca3162fcdb3bdfb183e264aa78a8b`. The selected local Windows and
native Linux matrix and four added regression tests passed with explicit
environment/live-test skips. The first hosted run subsequently finished with
68 failed checks, 61 successful checks and two skipped checks. Repairs are in
progress. Native V1 fallback correction `f400b19` advances hosted Docker beyond
the startup probe to healthy containers, Chromium create and the external-outage
subtest. Fresh controller startup and gateway/rebind fixture failures remain.
The following sections record the cold-process, diagnostic and fixture-lifetime
repairs, with their actual verification boundaries. Hosted Docker and race
acceptance remain required. Receipt-owned predecessor retirement remains open;
M4 is incomplete.

The current status and final verification sections supersede earlier pending
checkpoint descriptions below. Historical failures and source-only reviews are
retained with their original acceptance limits.

## Rebind authority and corruption-fixture alignment

Commit `3daf9ce085740cb3033d3ec45c4a2320d47ddd45` updates two tests without
changing production. Raw construction must install coherent concrete drivers,
but it still has no generated-domain view. The corrected assertion requires
typed mutation to refuse with no leave capability and no runner command.

The existing fourth-inventory-read corruption control now drops the newer
activation guard as well as the existing validity guard in its private fixture
database. This allows the deliberate generation change to reach the reader
under test. The result must remain empty with `DiagnosticRouteUnresolved`.
Production triggers and common fixture setup are unchanged.

Windows and native Linux execution at 04:45:40–04:46:11 UTC on 24 October
passed both selected parents and nine distinct children on each platform:
11 unique passes each, no failures or skips. Exact discovery, retained binaries
and unchanged source hashes bind the results. Formatting, ingress vet and
`scripts/check-generation.ps1` passed. Root independently checked all raw logs,
binaries, discovery, source and review hashes before the exact two-file commit.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `rebind-history-two-fixtures-20261024T044535Z/receipt.json` | `22339fdebcbef1995cee3a4693709d6e8b5750af1e91c3ac4b128147f7c7e97f` |
| `rebind-runtime-composition-preclaim-test-source-security-review-20261024.md` | `1244a99c27203ce3dd9d05d1845768377a2dfb5d53caa5799145fb7015423864` |
| `rebind-two-fixtures-root-audit-20261024.json` | `19c387767fba8e987f3b2f2877ad76c12cbf158dc929e6cd98c2dfda2dde0204` |
| `pr222-rebind-runtime-composition-preclaim-final-integration-20261024.md` | `4389f7ea43e0160002be7f7793fd18721cdede4268ad1f9685626f034dfe8e43` |

This is focused fixture verification. Hosted race execution, Docker journeys
and the complete suite remain separate gates.

## Application namespace handoff to complete cold accounting

Local commit `19b21bd9b6290c310dbe5de8712b15c2235c966a` corrects the A0
startup ordering. Previously, an application-bearing native V2 gateway was
checked against a gateway-only object set before its application resources
could reach complete cold accounting. The authentic public-constructor
regression first reproduced `startup_observation/docker_attestation` on
24 October at 04:20:57–04:21:00 UTC. Earlier fixture-setup failures are retained
and are not counted as that reproduction.

The production change moves the existing gateway-only comparison after the
valid application-bearing passive return. Typed expectation validation,
gateway-only exactness, complete censuses and daemon checks remain. The
passive result grants no authority. Before returning a public domain,
unchanged cold reconstruction accounts for every application, image, network,
builder and protected gateway object, then requires the full gateway proof.
Pinned image, container, volumes, network, journal, restart config, live config
and static challenge checks remain. `generated_domain_cold.go` has no Git diff.

The new fixture uses two applications with two components each, distinct image
identities, normal candidate/activation/grant/upgrade writers, reused sealed
stores and the persisted builder. It checks the exact 17-object pre-call
census and restores the canonical deployment-identity trigger before calling
the public constructor. Successful reconstruction requires two cold groups.
Missing, crossed, extra, unowned and inconsistent-builder cases must refuse
without mutations, capacity probes, admin requests or challenges. Separate
gateway-only controls cover exact, missing, extra, crossed and malformed state.

Windows composite verification at 04:36:42–04:40:07 closes three parents and
45 distinct children: six application cases, five gateway cases and 34 existing
cold-roster cases. The first run failed overall because a gateway assertion
incorrectly required a private error sentinel. Its application and cold-roster
passes remain valid. The final assertion-only correction checks the existing
closed public diagnostic; the rebuilt gateway parent and all five children
pass. An exact predecessor reconstruction, explicitly created after execution,
matches the earlier source hash and proves only gateway assertions changed.
It is not represented as a source copy captured before execution.

Vet, tagged compilation, generation checks and formatting pass. The earlier
formatter status field was blank; its empty output and the successful final
formatter check provide the actual evidence. Root independently audited source,
binaries, discovery, raw test counts, static status and source-impact closure.
SQL comparisons include schema and application rows but normalize
`schema_migrations.applied_at`; those timestamp values are not independently
bound. Protected file bytes and identities are compared separately.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `a0-gateway-final-successor-20261024T043952Z/composite-receipt.json` | `359a9ed0ab5e65f1d2e6b25be2a79b625e55b7a08894819562988628056503fa` |
| `a0-prepared-claim-namespace-final-test-source-security-binding-20261024.md` | `df66ae8154444c4fb7b871af861bfa966adacf94abe5fc48c5ac8f33473cdc59` |
| `a0-namespace-root-audit-20261024.json` | `6480411d1f7a011bc1f81a69fd3bdc8ce4a2a333777725aebe9b8cb14c049bba` |
| `pr222-a0-prepared-claim-namespace-final-integration-20261024.md` | `20de2f6a46a2e71666ca772906a3ef8f4a331a6caf7beb31d22a75977953564c` |

This establishes the local ordering repair. Actual Docker A0 startup, Linux
race execution and the complete hosted journey remain unverified. The hosted
incident may have a later independent failure; M4 remains incomplete.

## Stopped-stage replay in a genuine child process

Commit `bdfd6a28991597163301abf40382b5e80285d92b` changes only
`gateway_rebind_stage_container_live_test.go`. The previous fixture called
`New` a second time while its original generated domain still reserved the
same root and daemon. The replacement runs the same test binary in a fresh
process with a pointer `ExecRunner`, the real SQLite repository and the
original fence/current-state adapters.

The child selects the exact persisted sequence-six intent and invokes the
production replay with its real inspector. A counting driver refuses container
creation. Success requires zero creates, unchanged history bytes and exact
physical proof. The parent retains every history, physical snapshot/census,
SQL claim, predecessor state/resources, route and request-count assertion.
Later sequence-nine cleanup is unchanged. Production code is unchanged.

An exclusive private manifest binds the operation and contained fixture paths.
Both manifest input and child output are bounded. Child capture enforces its
64 KiB limit while draining output; the completion parser requires the exact
PASS and single success marker. Failure reporting exposes only a fixed stage,
never arbitrary child stderr. Review found and corrected an earlier unbounded
capture and missing failure attribution before the final source freeze.

On 24 October at 03:59:01–03:59:23 UTC, tagged binaries discovered the protocol
parent, live replay child and live stopped-stage parent on Windows and native
Linux. Only the protocol parent executed: 24 unique passes on Windows and 25
on Linux, with no failures or skips. Linux additionally executed the broad
control-directory refusal. Formatting, tagged vet, canonical generation and
diff checks passed at 03:59:44–03:59:49. Security review found no remaining
protocol issue; root verified source, binaries, discovery, raw outcomes and
static evidence.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `stopped-stage-fresh-process-final-20261024T035901Z/receipt.json` | `3ceef3a0c8f529e1efadde15c4fed2bc4c34e09e144c93546f5f27307f51236d` |
| `stopped-stage-fresh-process-static-20261024T035943Z/receipt.json` | `8373904abbe0128891e4ec2837990ac8757fa70516f2fe561e03f69cf4c148e1` |
| `stopped-stage-fresh-process-source-security-review-20261024.md` | `829cfb1eabe7372d1ab2845c852a86146942b9840d53b42c04516a57165c7f39` |
| `stopped-stage-fresh-process-root-audit-20261024.json` | `8321d2cd8c1f80d0dbce1d665d93c4f8a12766b8fc72ec23f4bff94baa7da4f4` |

The live parent and child are discovery-only locally. Actual Docker replay,
unchanged physical state and cleanup require hosted execution; protocol
success is not a Docker acceptance result.

## Hosted checkpoint before the A0 and fixture updates

Published `34a526897cbea3708b3ca7a030ef0eef089a8625` contains the stopped-stage
fresh-process harness. Its synthetic merge is
`e0528685b47bd86129565b524f74608fde85e3f5`; tree
`97438a19c3bfeb3bed7d070a910b86d86042eb7d` matches the published head.
The API binding has SHA-256
`399b667fb7d8a069ef2135115bedaca755a5f8579eca748e364bd6363c620e49`.

At 04:50:37 UTC on 24 October, the partial snapshot has 12 failed, 29 successful,
15 running, 84 queued and one skipped check. Snapshot SHA-256:
`3680e0e0b703d8350095211c8dfa1abbd018258ebebe8397aa63728b65a3daf8`.
All 12 failures are retained once in
`pr222-34a-fourth-failure-batch-20261024.json`, SHA-256
`ddaa136c90ea8c568ee038369f95953cd3220d52b889503a9c9e2cdb0ef833af`.

- [TLS restart](https://github.com/tyhuang9/rig/actions/runs/40062072641/job/120777536253)
  and [process-kill restart](https://github.com/tyhuang9/rig/actions/runs/40062072641/job/120777536306)
  still refuse at `cold_construction/cold_group_attestation`; the individual
  rejecting predicate remains unresolved.
- [Full-hostd A0](https://github.com/tyhuang9/rig/actions/runs/40062072608/job/120777536290)
  still refuses at `startup_observation/docker_attestation`. This head predates
  the local A0 repair and therefore does not test it.
- Migration, LAN and external-database LAN jobs remain blocked at predecessor
  retirement. Controller recovery, proposal, rollback/replay, data-volume,
  stage-start and final-config fixtures retain construction failures. Their
  logs establish separate boundaries, not a shared cause.
- The [stopped-stage Docker job](https://github.com/tyhuang9/rig/actions/runs/40062072608/job/120777536330)
  remains queued at this observation. Its queued `startedAt` field is not proof
  that Docker execution began. The next publication is held locally so that
  another push does not cancel this needed run.

No failure is waived. Complete Docker/race acceptance, E5, physical second-device
LAN qualification and M4 remain open. No live-controller refresh or deployment
was performed.

## Hosted progress after the inspect-template repair

Published `94e86e763c39de60073dea8bc9592e7b124bed88` advances both controller
children past retained-builder inspection to
`cold_construction/cold_group_attestation`:

- [TLS controller restart](https://github.com/tyhuang9/rig/actions/runs/40059540802/job/120769849546),
  raw SHA-256 `015b58a30c1cd699372757bd37b8a0763b1bf2144e0dda0d0ca76593ada8955d`.
- [Process-kill restart](https://github.com/tyhuang9/rig/actions/runs/40059540802/job/120769849358),
  raw SHA-256 `a74c5103df0afd6774f64541b84f68a8d4ae267708be1919dcd88d1e2083fc8e`.

Before the later restart refusal, the TLS job records healthy frontend/API
containers, Chromium note creation, and external PostgreSQL outage/recovery
with application and database containers preserved. Those successful stages
do not constitute acceptance of the complete controller journey.

The component-group diagnostic still covers image, container, network and
complete-census checks. Its individual rejecting predicate is unresolved;
the map does not justify relaxing receipt, identity or ownership checks.

At 04:16:37 UTC on 24 October, the partial snapshot has 19 failures, 30
successes, 12 running, 80 queued and two skipped. All failures are captured in
`pr222-94e-fifth-failure-batch-20261024.json`, SHA-256
`480b7941507cfa5670b91cbee7117bf6a291e39ce6c9a0d85a544586ab7371db`.
The jobs run synthetic merge `23520432c23b9dcf68983cd2c6f57a84956d6de4`,
tree `f3654854844beb79dab6cf7e8154affa52d1e948`, identical to that published head.

The [full-hostd diagnostic](https://github.com/tyhuang9/rig/actions/runs/40059540747/job/120769975257)
still refuses at `startup_observation/docker_attestation` for `a0-sql-claim`.
Its raw SHA-256 is
`e47943f2294aef88ac20d9d54362752c4d717a08f7ebcd6c97afadb8bf9ddeba`.
A source trace identifies an ordering conflict: app-bearing startup checks
the Docker namespace against gateway-only names before handing the apps to
complete cold accounting. A regression and repair remain pending; this trace
alone does not establish the exact first Docker object rejected by the job.

The [Windows rebind-history job](https://github.com/tyhuang9/rig/actions/runs/40059540752/job/120770440286)
and [Linux race counterpart](https://github.com/tyhuang9/rig/actions/runs/40059540775/job/120770292539)
fail the same two tests. The constructor test expects nil drivers, although
the raw manager has coherent drivers without a generated-domain authority.
The deliberate active-head corruption test encounters the newer exact-receipt
SQL guard before it reaches the reader refusal under test. The proposed
test-only corrections must preserve those runtime safeguards. The logs report
neither a data race nor a timeout. Raw SHA-256 values are respectively
`a1810b3c783d98411c84ee2cac44e515eca0714e37aebc0d51e5b86337865c2e`
and `41fda44f181abbb9ae125530136856815a58ee6f93268d21f39b0f10cdaa15ed`.

## Retained-builder Docker inspect template repair

Commit `c31be537fac54aa23eb56dc20a39fbd5dfcbc792` changes two container-inspect
selectors to the exported Docker fields `CPUPeriod` and `CPUQuota`. Output JSON
keys remain `CpuPeriod` and `CpuQuota`. Strict decoding, policy checks, identity,
resource limits, reads and effects remain unchanged.

The complete [Docker CLI v28.0.4 renderer](https://github.com/docker/cli/blob/v28.0.4/cli/command/inspect/inspector.go)
tries the typed response first, then raw JSON with `UseNumber` and
`missingkey=error`. The former complete template fails typed execution at
`CpuPeriod`; its raw fallback also fails because `.ID` addresses a raw `Id` key.
The corrected template succeeds on the typed path and strict-decodes to the
existing projection. A regression models both renderer paths and explicitly
binds the successful template to the actual production constant.

The original typed-only red and review were insufficient to establish the
whole CLI contract. Their evidence and the subsequent hold remain preserved.
The first full-renderer attempt incorrectly expected the legacy fallback to
succeed; its failure established the second boundary. The final successor
checks both legacy failures and corrected success. An intermediate test lacked
the production-constant binding; its results are superseded by the final test.
An earlier tool yield was initially mistaken for process termination; the
original process completed normally and was not duplicated.

Final Windows execution at 03:38:46–03:39:23 UTC on 24 October passes both
selected parents and all 34 distinct roster children, with no failures or
skips. Explicit discovery, retained binary and before/after source hashes bind
the result. Formatting, `go vet ./internal/generatedingress`, canonical
generation and diff checks passed at 03:39:36–03:39:41. Source/security review
closed the fallback hold; root independently verified the receipts, source
hashes, binary, raw outcomes, official source chain and review.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `cold-builder-template-contract-20261024T0331Z/renderer-fallback-authoritative-addendum.json` | `12d39019e22abb68f9da70d0afabc2438cd1933952eff6c2ba1db8f764ae14df` |
| `cold-builder-template-final2-20261024T033846Z/receipt.json` | `eb3dcc9a5475408baca35d8b9349cfa7cafd0d799e5e6a6b89cb7e5bf11ea50d` |
| `cold-builder-template-final2-static-20261024T033936Z/receipt.json` | `15d6678a71f4025abf0d38d6e3c305ebf187de0b4fc5ddefa03948d99b2d7248` |
| `cold-builder-template-docker-cli-final-security-supplement-20261024.md` | `e29591458d8888c983fddadf284899eaeb1665b621ed42086bdb57fe4258882f` |
| `cold-builder-template-root-audit-20261024.json` | `ccb3506eea44b817df91208c479e13860cab9402fa882de595baa2d142577857` |

This establishes a rendering contract repair. Actual hosted Docker execution
must still verify the controller journeys; no sole-cause or M4 acceptance is
claimed. Local Docker and race execution remain unavailable.

## Hosted opener successor

Published `ec5a899969f05cec7a304daa3291b5ace7f970c8` advances the
[full-hostd diagnostic](https://github.com/tyhuang9/rig/actions/runs/40058334785/job/120766427522)
from `startup_observation/physical_identity` to
`startup_observation/docker_attestation`. Its raw log SHA-256 is
`ed7029711a594a5e1a3ab96fc9baef4fd18e1d8a867428fa37bda2e4159d9670`.
This confirms progress past the earlier identity refusal, not successful
controller startup. The next predicate requires a separate source trace.

The same head's TLS and process-kill controller jobs retain
`cold_construction/cold_builder_container_read`. The stopped-container and
data-volume replay fixtures still fail at fresh manager construction. Their
same-process lifetime issue is mapped but not yet repaired. Predecessor
retirement remains open.

At 03:41:25 UTC the partial snapshot has 15 failures, 15 successes, 16 running,
94 queued and one skipped. All 15 failures are retained once in
`pr222-ec5-third-failure-batch-20261024.json`, SHA-256
`559affa71af16d5c7a7b1ac7a0ec85df92b237b2882c63bfeee76c0a701e45c8`.
The jobs execute synthetic merge `d7ce62a41057cac98f51126621b6e675baa32ad5`,
tree `59b2cbf896ac54272f6fa4397feb0e8a0f8683c7`, identical to the published head.
No failures have been waived or converted to acceptance.

## Authenticated database opening in full hostd

The full-controller `startup_observation/physical_identity` failure exposed a
concrete opener mismatch. `runServerWithAssembly` held the controller-owner
lease but passed an ordinary `database.Open` handle into generated-domain
construction. That constructor requires protected database provenance issued
by `OpenForGeneratedDomain`. The working controller restart helper already
used that authenticated opener.

Commit `b32a771e3e16652ae580372ba04e27a6e298e343` routes the existing server
database open through a private mode-aware helper. Generated hosting uses
`OpenForGeneratedDomain` with the configured root and already-held owner lease;
disabled and Compose-only modes retain the ordinary opener. Acquisition,
recovery, emergency-stop and teardown order remain unchanged. The database
still closes before its owner lease. No provenance check is relaxed.

The regression exercises the actual production helper with the old plain-open
behavior first. It fails for missing provenance with the exact lease and for
incorrectly returning databases with missing or foreign leases; ordinary modes
pass. The repaired parent and all five children pass on Windows and native
Linux. They require accepted exact-owner provenance, ordinary modes remaining
unsealed, and missing/foreign leases returning no database or target file.

The red ran at 03:19:59–03:20:04 UTC on 24 October; Windows successor at
03:20:39–03:20:48 and native Linux/extra tagged compilation at 03:21:41–03:21:57.
Formatting, hostd vet, canonical generation, diff checks, ordinary compilation
and live-tagged hostd compilation pass. Source/security review found no issue;
root verification binds both final sources, actual raw results and binaries.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `hostd-generated-database-opener-red-20261024T031959Z/receipt.json` | `4174203146839dc1dafe98f73fe2f727ab4df40fa82a3817062d00666c040e79` |
| `hostd-generated-database-opener-successor-20261024T032035Z/receipt.json` | `f25fbff7f1a5449cf8017fe0fafce9b49e15fba652a794a3b3ecf13e44ff48b8` |
| `hostd-generated-database-opener-linux-20261024T032137Z/receipt.json` | `8629fbeff2b02e9de0a5401a6ead597270d54170fb0cd25b79a5ac156a8b4281` |
| `hostd-generated-database-opener-source-security-review-20261024.md` | `3974e77b756d7de17b60ab7b1154647aed2f04105954465453ad0b349120da96` |
| `hostd-generated-database-opener-root-audit-20261024.json` | `42f9da72bdf192a04f0f44c810db8420a800d3e7a676987f80590822fac59f9e` |

This establishes the opener contract repair. Full-hostd and real Docker
acceptance still require hosted execution; it is not evidence that this was
the only startup failure. Builder validation and predecessor retirement remain
separate work.

## Hosted builder read attribution

On published `22b40e4bc42bf974014f9792f7447cface1c098c`, both controller
restart jobs now report `cold_construction/cold_builder_container_read`:

- [TLS restart](https://github.com/tyhuang9/rig/actions/runs/40057623879/job/120763994032),
  raw SHA-256 `07d1bf3afd82844b58dc8df42efd88e992f874ef00c388db38845cd4dee4d64e`.
- [Process-kill restart](https://github.com/tyhuang9/rig/actions/runs/40057623879/job/120763993817),
  raw SHA-256 `d40b38732b90f85f16317240e0a26d6d941a6099277369fb1a3f9dccb725769f`.

Both execute synthetic merge `b7be999e90a59ea498513eda8bf9379cf50a8902`, tree
`b337eb5f51d823247bc472e03f094f381662f486`, identical to the published head.
This narrows the failure to the container read boundary, which also includes
decode and image-identity checks. It does not yet prove the exact cause.
At 03:22:05 UTC the partial snapshot has two failures, four successful checks,
13 running, 121 queued and one skipped. Both logs are indexed in
`pr222-22b-first-failure-batch-20261024.json`, SHA-256
`262362275bd4898b9c01a0548804607bbb0b89bc475f738e3be4ac8b4c0d7e13`.

## Retained-builder invariant attribution

The current controller failure is still collapsed at retained-builder validation.
A six-file diagnostic change adds eleven fixed reasons at the existing protected
identity, census, container/network/image read and policy, census-to-detail
correspondence, repeated observation and final protected-identity boundaries.
The existing predicates, short-circuit order, read count, effects and collapsed
error causes remain unchanged. Input and fallback reasons remain available.

The controls exercise actual failures at every new boundary and require the
specific reason, refusal sentinel, absence of the injected private read error
throughout the error tree, and the existing no-effect checks. Both hostd
projection surfaces accept the new reasons only with `cold_construction`.
Review identified a missing wrong-stage control; two refusal rows were added
for `empty_construction/cold_builder_identity` before the final freeze.

The initial Windows run at 03:10:49–03:11:47 UTC on 24 October passed the two
ingress parents, the hostd projection parent, formatting of all six files,
ingress and tagged hostd vet, canonical generation and diff checks. Only the
hostd control file subsequently changed. Its successor at 03:12:25–03:12:34
passed tagged compilation, exact discovery/execution, formatting, tagged vet,
canonical generation and diff checks. Unchanged ingress evidence is retained.

The final composite contains 54 unique ingress passes (two parents and 52
children) and 175 hostd passes (one parent and 174 children): 229 passes with
no failures or skips. Counts come from terminal raw records, deduplicating
child-process echoes. Source/privacy review found no remaining issue; root
verification matched all six final sources, binaries, raw results and discovery.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `cold-builder-finite-attribution-20261024T031043Z/receipt.json` | `d4dd65bb5c58983e081790d8cdcc7387a8341f3f8c0fb818e726fb140190768e` |
| `cold-builder-finite-attribution-successor-20261024T031222Z/receipt.json` | `4d57705d9a78208e17e56fed5ca46c0d46a9df5fb83f575e47467cdd33067eb2` |
| `cold-builder-invariant-attribution-source-security-review-20261024.md` | `dec764fa57a4fc178096b5708aa663801fe6a0f8c797af29ec4f863dd79b0083` |
| `cold-builder-invariant-attribution-root-audit-20261024.json` | `5a08624676324314a6b13c2d76063f3459dd075917dca1d794a0f308bad0db15` |

This is diagnostic coverage. Actual hosted predicate identification, Docker
and race acceptance, predecessor retirement and M4 remain incomplete.

## Hosted results after network-policy alignment

Published `6da5c421714d385913bcbee265a1644ada08e3e9` executes through synthetic
merge `5dcaff21e545f7a30c87eeb009174331f3340f4c`, with tree
`37207f309a61aef7654f4e1f534aa3e9522913ee`, identical to the published head.
Both controller restart jobs still fail at
`cold_construction/cold_builder_attestation`:

- [Process-kill restart](https://github.com/tyhuang9/rig/actions/runs/40056485792/job/120760495254),
  raw SHA-256 `be4833d684c2527050ff52e50625d91e16bd3e3a66453d581308cd4c5e8adc13`.
- [TLS restart](https://github.com/tyhuang9/rig/actions/runs/40056485792/job/120760495494),
  raw SHA-256 `dec0cc73435a7648db9f11ecbfb295bcf80f3e28ba5567713b6b66f69bf07fee`.

The local network-policy defect is established, but its repair is insufficient
for these paths. The underlying hosted builder predicate remains unknown.

The separate [full-controller first-boundary test](https://github.com/tyhuang9/rig/actions/runs/40056485869/job/120760686607)
now retains `startup_observation/physical_identity` for the `a0-sql-claim`
child's failure before daemon assembly. Its raw SHA-256 is
`b8f6ef4691b664961e1e393f8dfe898b904f5743cbfd7f2e84765b5876d47dfe`.
This demonstrates that the diagnostic projection works on the hosted failure;
it identifies a distinct boundary, without proving its deeper cause.

At 03:05:11 UTC on 24 October, the partial snapshot has six failed checks,
six successful, 16 running, 112 queued and one skipped. The other failures
remain predecessor retirement, typed-manager opening and final-config replay
construction. All six exact-checkout logs are indexed in
`pr222-6da5-first-failure-batch-20261024.json`, SHA-256
`bde022654b0180de93c551a2b51348c57f047c35d229263f540356e7961fdc38`.
The snapshot SHA-256 is
`4924f675dc3b4a78ced2f9fe4a86f514b2f442bdaa28ff21a1741cbfac02d5ec`.
The controller workflow is terminal; broader hosted acceptance remains pending.

At 03:12:01 UTC, the snapshot has 12 failures, 12 successful checks, 17 running,
99 queued and one skipped. New captures repeat predecessor-retirement refusal,
typed-manager opening, proposal-manager opening and fresh-manager replay
construction. The current stopped-stage job fails at the same direct `New`
call before replay. All 12 exact-checkout logs are indexed in
`pr222-6da5-second-failure-batch-20261024.json`, SHA-256
`4eec75c0f57de1f3e9085ad184dbc1027e59c1133e12b4a9610e2d1aac737af8`.
The snapshot SHA-256 is
`92dcc0657d8890e74da6a190e29918a666e2c44192d5f9461be05f11e796d93f`.

## Full-controller diagnostic projection

The full-hostd test harness previously discarded an existing closed construction
reason from the production JSON log, leaving only a stderr digest on readiness
failure. A separate test-only change recognizes the exact construction-failure
message and projects only its known stage/reason pair alongside the existing
digest. Production logging and startup behavior are unchanged.

The bounded parser rejects duplicate or case-folded relevant keys, non-string or
missing errors, malformed records, unknown/crossed pairs and prefixed/suffixed
private content. A recognized invalid construction record cannot fall through to
generic startup classification. Other metadata remains private; existing byte,
record and first-primary ordering limits remain.

Windows compilation, exact changed-parent discovery and execution pass:
`TestLiveHostdRebindClosedDiagnosticControls` and its 150 children, 151 unique
passes with no failure or skip. Formatting, tagged hostd vet, canonical
generation and diff checks pass. Source/privacy review is clear. The network
repair's two source hashes remain unchanged; its successful checks are reused.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `full-hostd-construction-projection-20261024T025616Z/focused-receipt.json` | `7b552ec2e9238bcf06f1cc16e6f71834e22c944ba2d8553ed5efad2ff8026f8f` |
| `full-hostd-construction-projection-20261024T025616Z/static-receipt.json` | `511c17451f0d516c5a201df845b704b06c874345343e806831d80ce9f339823a` |
| `full-hostd-construction-projection-source-security-review-20261024.md` | `64a1e350b658a29bfdbe8d593b8c1eef92f4db8c20913bd34c7bbab9e0bc6439` |
| `full-hostd-construction-projection-root-audit-20261024.json` | `7131991e5b0663cfbb91c72602ad3e7fbd35ed720311ec52e67c226852aa73a4` |

Actual focused execution was 02:56:16–02:56:23 UTC on 24 October. Root verification
matched current source, binary, raw results and discovery. This improves safe
attribution; actual full-controller Docker behavior remains a hosted gate.

## Builder network policy alignment

Commit `ddedf04286e3ec4425ccfd6a92f142d7ea208163` aligns cold reconstruction with
the existing shared builder network policy. The shared policy accepts ordinary
additional bridge options while requiring ICC to be disabled; cold startup
incorrectly required an options map containing only that one key. A regression
fixture adds `enable_ip_masquerade=true` consistently, confirms the shared writer
policy accepts it, and reproduces `cold_builder_attestation` on unchanged
production.

The repair retains exact identity, ownership, bridge/local/non-internal policy,
ICC=false, sole network membership and container attachment checks. It requires
the complete census options map to match the first detailed inspection, retains
complete first/second physical equality and final attestation, and changes no
writer, schema, lifecycle, capacity, traffic or cleanup operation.

The Windows successor passes the cold parent and all 31 unique children, plus
the shared network matcher parent: 33 passes, no failure or skip. Controls
independently exercise missing, true and other invalid ICC values at both
census and detailed boundaries, consistent extra options, crossed maps and
addition/removal/value drift between reads. Formatting of both changed files,
vet of ingress and builder packages, canonical generation and diff checks pass.
Source/security and final integration review found no blocker for this bounded
repair; publication still requires the recorded documentation and identity
checks.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `cold-builder-network-options-red-successor-20261024T024835Z/selector-successor-receipt.json` | `768dc80e0fe2a01cf359eec05820ef541b942908faa8914624ffbd220ef1f787` |
| `cold-builder-network-options-successor-20261024T025132Z/receipt.json` | `8d7e2f94522c1f68ad98b33ca8383579caa88c71007e89936d53d8c5108c0bea` |
| `cold-builder-network-options-successor-20261024T025132Z/static-receipt.json` | `e7c8a671d1db9f1e202f65f601a7be4044ee135cd5ff5e74de32787150d3fd68` |
| `cold-builder-network-options-source-security-review-20261024.md` | `41baa546b678362f1a68852bf3d50d46ca1460191ce7b24991f7f55df356a138` |
| `cold-builder-network-options-root-audit-20261024.json` | `8f5746069dbd895d677cb5576bb23073b218cafec54147437650c7bc395b8025` |
| `pr222-cold-builder-network-options-final-integration-20261024.md` | `1c91eedcde1722acef2cb6cb0fbfd5675c4be52bbef78cf68a24ba0ed537a930` |

The authoritative red ran at 02:49:14–02:49:32 UTC; the successful behavior run
at 02:51:32–02:52:07 UTC on 24 October. Earlier harness/selector attempts remain
retained. Root verification matched current sources, binaries, raw results,
discovery and review bindings. Actual hosted option maps remain unobserved:
the local contract defect is established, but this repair has not yet passed
hosted Docker or proved the sole restart cause. Linux race, E5 and M4 remain
unaccepted.

## Hosted attribution on the diagnostic successor

Published `92b687d6a20101871e7136fe566b76faa83de8e1` executes through synthetic
merge `d5af8c08d07fc14d43ef5c9970c4847612cc872f`, tree
`fa0edf55de5ae33d1567b591a29daa6ac387f3ce`, identical to the published head.
Both real controller restart children now report
`cold_construction/cold_builder_attestation`:

- [TLS restart](https://github.com/tyhuang9/rig/actions/runs/40055093411/job/120756223879),
  raw SHA-256 `70be001bfca2bf71ad9fb3812beb0a151be6493efb26a635777c02cc71ad44b3`.
- [Process-kill restart](https://github.com/tyhuang9/rig/actions/runs/40055093411/job/120756223651),
  raw SHA-256 `149fb591b176898c0376cc5fae5799f680e7fe6912c457be8296ef32e67d4384`.

This identifies the retained-builder validation boundary, without yet proving
the exact failing predicate. The 02:45 UTC snapshot has four failed checks,
seven successful, 15 running, 114 queued and one skipped. Migration still
pauses at predecessor retirement; the stopped-stage test still fails before
fresh-manager replay. All four logs are captured against the exact checkout in
`pr222-92b-first-failure-batch-20261024.json`, SHA-256
`5029456e1990ca7b710821859d76b578725c3bead2eb9b4308902eb4d62cafe5`.

The 02:52 UTC snapshot has 11 failed checks, ten successful, 15 running, 104
queued and one skipped. Both LAN checks repeat predecessor-retirement refusal;
runtime/controller-recovery and stage/final-config tests repeat typed-manager
or fresh-manager failures. One stage-start case refuses initial classification
after an earlier uncertain stage; this does not prove carryover as its cause.
All 11 exact-checkout logs are indexed in
`pr222-92b-second-failure-batch-20261024.json`, SHA-256
`9ac6d8c8aef7930c160006ec12edf4ddb0eee651f4309318db0f48c015c23c09`.

## Cold-accounting boundary attribution

The remaining hosted `cold_accounting` refusal does not identify which
reconstruction check failed. A focused diagnostic change adds seven closed
reasons under the existing `cold_construction` stage: group reconstruction,
builder attestation, reservation, ownership, head binding, group attestation and
manifest creation. The outer boundary preserves the nested reason. Hostd accepts
only the corresponding fixed stage/reason pairs.

This change preserves the existing checks, execution order, effects and error
cause visibility. It adds no raw error, path, identifier or secret output, and
does not claim a repair of the unidentified hosted predicate.

Windows execution on 24 October passed the two ingress parents with 39 children
(41 unique passes), the existing process-kill boundary parent with ten children
(11 passes), and the changed closed-projection parent with 136 children
(137 passes). All seven added projection rows ran. There were no failures or
skips in the final selection. Counts deduplicate child-process echoes.
Formatting, ingress vet, live-tagged hostd vet, diff checks and canonical
generation passed. Source/privacy review and its narrow test-expectation
supplement found no blocker.

Two earlier attempts remain recorded: a runner array-shape bug stopped discovery
after compilation, then an existing removed-frontend assertion expected the old
generic reason. The assertion now requires the exact `cold_group_attestation`
reason while retaining refusal and no-effect checks. Root review also caught
the hostd selector pointing to a neighboring boundary parent. The changed
projection parent was subsequently discovered and executed on the same retained
binary; successful earlier behavior was not replayed for that correction.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `cold-accounting-attribution-successor-20261024T023512Z/receipt.json` | `8ae97df00c98960ad573050f6a46a04a008c3dcb3933a95f14c0f8aef0c00845` |
| `cold-accounting-attribution-successor-20261024T023512Z/hostd-projection-supplement-receipt.json` | `f9fae753179e4b08b5d5a8293109e0c925c6b1d8755f89c76ccf50c9e873052a` |
| `cold-accounting-attribution-source-security-review-20261024.md` | `69bc2e41f49d23ea491506e9925c34bc2004645bf7f097ca249c07254dc0a925` |
| `cold-accounting-attribution-test-successor-security-supplement-20261024.md` | `ab0d94673acc865345d9a6b9c7fb295f34d0532088954ce5eb1d2f72ea314334` |
| `cold-accounting-attribution-root-audit-20261024.json` | `6fa1b0fcb6e5840fdf38fd2347aee0423c731e3a6ec31efcd4e012cdb48a90ae` |

The main execution ran at 02:35:14–02:35:56 UTC; the projection supplement at
02:36:22–02:36:24 UTC. Root verification matched the six current source hashes,
retained binaries, discovery, command exits and raw results. Actual Docker,
Linux race, full-suite and hosted root-cause acceptance remain unverified.

## Hosted verification after the builder repair

Published `07919b6eddb95d41651ca6611fdaca39a1708b68` ran through synthetic
merge `eb59a88e342f1a2c91d867552a842f5ae2c2ddcd`, whose tree
`880032441243b69bd66130545f6a76925f41ca37` equals the published head.
Both real controller children still fail at
`construction=cold_construction/cold_accounting`:

- [TLS controller restart](https://github.com/tyhuang9/rig/actions/runs/40053901074/job/120752467987),
  raw SHA-256 `d287a2906a01cdd24c0eaa942612ad72c7360c0bed2865a3d4ff063291305fb7`.
- [Process-kill restart](https://github.com/tyhuang9/rig/actions/runs/40053901074/job/120752467768),
  raw SHA-256 `880ad67ca23dd4a0032999e4ed33f1172110360b01c42ce4d1cd70b69c144b13`.

The local builder regression is fixed, but these results do not prove that its
predicate was the actual or only hosted failure. Further reconstruction
investigation is required. The 02:28 UTC snapshot has six failed checks, nine
successful, 15 running, 110 queued and one skipped. Both LAN jobs and migration
still reach unfinished predecessor retirement; the data-volume job still fails
fresh-manager replay. All six failure logs are captured against this exact
checkout in `pr222-07919-first-failure-batch-20261024.json`, SHA-256
`a8bd90a54ab6af55b0623dfd458b59d8e6ce4d3668ca0e06ef04e46f92ad55de`.

At 02:33 UTC, the same head has nine failed checks, 15 successful, 16 running,
100 queued and one skipped. Both final-config copy variants fail restarting the
manager before sequence-ten replay. The full-hostd first-boundary diagnostic
fails at `a0-sql-claim`: its child exits before daemon assembly, with only a
closed stderr digest available. The log does not identify its internal cause.
The three additional raw hashes are, respectively,
`ec466338eec641885beb8566d4086f4d1b97cf0ebba255ecebcbbc5dbdbdd54d`,
`bc969bb2bb440a94c6adeaa946186cbe1d19cc951c37c0657708defd958f9d72`
and `cbd4b16880ace65bd0e579913ff93eb3cba695fae54901903fe0bc7c6305d6b8`.
All nine captures are indexed in `pr222-07919-second-failure-batch-20261024.json`,
SHA-256 `5ed87c693b40dc5364a6cb51f8915bb86820baf29a7343dc093b903671768f57`.
These are partial check counts, not counts of independent defects.

## Retained builder accounting repair

Commit `8c67e25b22f20426c307cbdd8423576d041dbec2` accounts for the persistent
BuildKit container and network during cold reconstruction. A neutral internal
package shares the existing identity schema, naming rules and fixed resource
policy. The image builder remains the sole identity writer. Startup reads only
existing protected directories and files, correlates them with the exact
container, network and referenced image, then repeats the checks before accepting
accounting. It neither creates state nor contacts BuildKit or application ports.

Both absent builder state/resources and one exact persisted pair are valid.
Incomplete, extra, crossed or changed resources refuse startup. The final check
binds the identity file object and contents, the protected directory, exact
container attachment network ID, pinned image reference, resource limits,
network membership and running state. Existing accepted identity name suffixes
remain valid. Ordinary gateway/application labels do not identify a builder.
The runtime parent and builder directory must both already be private; startup
does not repair their permissions. Receipts, capacity and immutable history
retain their existing requirements.

The selected verification results are:

| Selection | Windows | Native Linux |
| --- | --- | --- |
| Identity codec and static policy: 3 parents | 15 passes | Not selected |
| Existing image-builder regressions: 10 parents | 16 passes | Not selected |
| Protected-directory controls: 3 parents | 6 passes, 2 OS skips | 8 passes |
| Two-component cold reconstruction: 1 parent, 21 children | 22 passes | 22 passes |

Counts include unique parents and children, without counting repeated subprocess
echoes. Windows skips symlink creation and portable permission broadening; both
execute and pass on Linux. The cold controls cover the two positive states,
missing/crossed applications, malformed or replaced identity, re-encoding,
resource-only/identity-only state, extra/orphan objects, configuration, image,
attachment, membership and running-state drift. Eight-file formatting,
four-package vet, diff checks and `pwsh -NoProfile -File
scripts/check-generation.ps1` passed.

The first Windows run retained global exit status, exact discovery, binaries
and raw output, but a function-local status accumulator omitted its per-package
status file. An additive record discloses that limitation. Its unchanged
identity/image cases were reused. The required permission correction received
fresh protected-directory and cold tests on both platforms, with proper status
records. No tests were replayed solely to manufacture missing metadata.

Final source manifests match all 1,209 Go/module/SQL entries, including new
files. Independent source security review is clear for this delta. The root
audit verifies source/binary/raw hashes, exact discovery, actual OS skips and
the Linux permission refusal. Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `cold-builder-ingress-final-20261024T021142Z/final-evidence-receipt.json` | `3468cbf7a8251a9f17563bea7b9fd5676ef78d393aeb4d83408c7728b92ce3d9` |
| `cold-builder-accounting-source-security-review-20261024.md` | `3927edda725fa2d2278e4a633b544e506804ca02bc3e5f9c2e0b5a01d935ee26` |
| `cold-builder-accounting-root-audit-20261024.json` | `cb78425e80a6f6ddddb91d05306875206437e785946060b0e449e0c8544aa5a8` |

Actual Docker restart and Linux race execution remain hosted gates. These
controlled tests do not prove that builder accounting was the only hosted
failure. Predecessor retirement and M4 remain incomplete.

## Completed second source session release

Commit `198638233c5d52541030821b7c5cb2b6172b5f82` closes the second completed
candidate session in the shared live rebind source fixture. The source already
has two active applications, validated runtime and seeded passive lineage when
the exact session is closed. Its map entry and immutable history remain
available, and registered cleanup is idempotent. This releases the deployment
effects scope before a later stage or child process acquires it. No timeout,
child-process boundary, SQL claim, production lock or assertion is weakened.

The existing `TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes` passes.
Tagged ingress compilation and seven affected live-parent discoveries pass;
the seventh indirect consumer was discovered separately against the same
retained binary after the initial six-name selector omitted it. Formatting,
tagged vet, diff checks and canonical generation pass. Actual Docker journeys
were not run locally.

Fixture receipt `gateway-rebind-second-session-close-20261024T021711Z/receipt.json`
has SHA-256 `8dd5fd7f4c1cde7b0fd48e577074258509ae0ebf23b71274de274cf070369af6`.
The seventh-parent addendum SHA-256 is
`58190884d2304c197852abc29cef81e8c22b00ab8f94a80f144b922989081b85`;
independent source review SHA-256 is
`654c0cdcd1148ffd510d499031337e455ad3c06a0cfeec4d62756eca7bb0ec5e`.
Zero-output compile, formatter and vet log files were not created by the
PowerShell pipeline; their original command, time and exit records remain.
The diff log contains only a line-ending warning. An earlier metadata note
incorrectly listed that existing diff log as absent; it is retained and
corrected additively without rerunning the checks.

## Hosted restart refusal narrowed to cold accounting

Published `2ebd18c5d0ff65236e80eaa193fa70edf1d4fbca` ran through synthetic
merge `39271cfe2bb0c32842b9f71828f7555f8768ce76`, with identical tree
`63d54e783fd13d6601fdbaabed30a4c83837bff7`. Both restart jobs now retain
`construction=cold_construction/cold_accounting` in their private diagnostic:

- [Process-kill recovery](https://github.com/tyhuang9/rig/actions/runs/40050347719/job/120741406528)
  fails in its `before_build` child before the durable barrier, after healthy
  API/frontend containers and the Chromium create journey. Raw SHA-256:
  `b8dd79e69c9e8ecbec5eac107c116589aa1313e34e8fd82b7511d7b66d5fc739`.
- [TLS controller journey](https://github.com/tyhuang9/rig/actions/runs/40050347719/job/120741406762)
  passes the external PostgreSQL outage/recovery subtest before its restart
  child fails at the same boundary. Raw SHA-256:
  `e4f7238906352527766d93c5e6c0d75aa9d20ae3f7011ce14413bcc7c6943f31`.

The label follows the error returned by `reconstructGeneratedDomainCold`.
It narrows investigation to retained accounting reconstruction, but does not
identify the exact census, receipt, policy, budget or durable-state predicate.
The passing internal two-component simulation remains insufficient to explain
the actual Docker failure. No persisted run artifacts were available from the
controller workflow; these observations come from its retained job logs.

The migration job on this head still reaches the separate unfinished
receipt-owned predecessor retirement step. Neither restart journey passes,
and M4 remains incomplete.

### Retained builder regression reproduced locally

Source inspection found that the image builder deliberately retains its
BuildKit container and network after builds. Startup includes these resources
in its census, but the cold expected sets currently account only for gateway
and application resources. The prior passing two-component simulation omitted
both the protected builder identity and the retained builder pair.

The corrected simulated fixture adds `builder-identity-v2.json` and its exact
container/network pair. On unchanged production source at `2ebd18c`, the
`exact_api_and_frontend` child fails with `cold_construction/cold_accounting`.
Missing/crossed application and builder controls continue to refuse startup.
This is a reproduced accounting defect in a controlled simulation; the hosted
logs do not contain a persisted Docker census proving that it is their only
failed predicate.

The red run occurred at 01:43:35–01:43:53 UTC on 2026-10-24. Its retained
receipt, `cold-builder-accounting-red-20261024T0145Z/receipt.json`, has SHA-256
`96ecf4a161e56c96674aee5481fe747349fc1cbd1bfb483415c67f7a4cb8e227`;
the folder label is approximate. Raw SHA-256:
`9b7ae6995a26a0402c05d2b21a08672ea018dbbd93c4fc7f49199cb983fbd0f2`.
The command, exit status, source hash and actual times are retained. The fix
must preserve both empty-builder and exact persisted-builder positive states,
reject incomplete or foreign resources, and keep observation free of writes.

The 02:16 UTC snapshot has 14 failed checks, 45 successful checks, 13 running,
68 queued and one skipped. Both LAN jobs still fail at predecessor retirement;
gateway and rebind jobs still fail at fresh-manager construction. These remain
separate unaccepted journeys. All 14 immutable failure logs are indexed in
`pr222-2ebd-fifth-failure-batch-20261024.json`, SHA-256
`478c65e3542c644670f1e2a69415d89f7450fc5e75b8b0d108cd6accda243b45`.

The [prepared-claim process job](https://github.com/tyhuang9/rig/actions/runs/40050347874/job/120742102090)
advances past the initial-manager correction in `17f400c`, but its admission
child fails after 5.02 seconds with `context deadline exceeded` at
`gateway_rebind_cross_store_live_test.go:403`, before the SQL claim exit boundary.
Raw SHA-256: `107390feb3ed08e782603b73e0b5fc164fa95e82774f4ea2c2a33c7fe69c2fe6`.
This records progress and a new failure boundary, not a passed recovery journey.

## Construction boundary attribution follow-up

Commit `2f534b74ad595523308f4268e5503f6e9a7da658` closes the observed
attribution gap at public/private construction entry, reservation, owner
construction and publication. Cold-construction reasons distinguish input,
path identity, state store, assembly, fence, repeated-observation stability,
accounting reconstruction, lock acquisition, release and final validation.
Generic stable/empty fallbacks use construction-stage labels rather than
claiming a final or released boundary was reached.

The wrapper preserves direct diagnostic identity and existing cause visibility.
When a closed diagnostic is nested inside a private prefix or joined cause, it
copies only the closed pair into its outer message and retains the original
unwrap chain. Previously collapsed causes stay hidden. The owner's failure
record receives that same final diagnostic. Construction predicates, operation
ordering, lock/release handling and ownership remain unchanged. The private
child projection accepts only the corresponding exact new pairs.

Focused Windows checks passed on the final six source files:

- `TestGeneratedDomainConstructionDiagnosticClosedAttribution` covers public
  input, collapsed physical causes, sticky recovery, incomplete cold accounting,
  mixed-error privacy and existing observation cases. An initial new assertion
  incorrectly expected a collapsed runner cause to remain visible; the corrected
  test preserves the prior behavior. This run has tool-session output only and
  an additive receipt, without a pre-run raw artifact or source manifest.
- `TestLiveHostdRebindClosedDiagnosticControls` passed one parent and 129 distinct
  children, including actual failing subprocess controls. Its command, raw log
  and exit are retained; source hashes were recorded after execution only.
- The existing final-classification/release, canonical-input/sticky-failure,
  complete-owner-join and two-component-cold parents passed. Exact discovery,
  four parent passes and three direct child passes are retained with identical
  source manifests before/after. Nested subprocess echoes are not extra cases.
- Six-file formatting, ingress vet, tagged controller vet and canonical
  generation passed, with raw logs and identical source manifests.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

| Artifact | SHA-256 |
| --- | --- |
| `construction-boundary-attribution-20261024T013000Z/receipt.json` | `90fe510ce574c189e487aa3e33fd582f403f468bb7f4e26365dcb5077f6ab214` |
| `construction-boundary-regression-20261024T013600Z/receipt.json` | `c6839db42cd855a2ae230d87fed4e5026ad0c8f8f0a4dd8ad3b82a68bac903a5` |
| `construction-boundary-static-20261024T012645Z/receipt.json` | `758f6593775cf34930e855799cdcba9a3f1b042b41f1a414f2366cb54380a33b` |
| `construction-boundary-root-audit-20261024.json` | `1f77c4c53410a6e351af9903f067f4fb69b36d2b31657cd9b7cafba5c9a60b62` |
| Source/security review | `f3dfbb4c32688316c717ea1e4366579091008956123561467ce91935065c7a84` |

The first two folder labels are not execution timestamps: retained hostd output
was written from 01:25:14 to 01:25:21 UTC, and factory discovery/execution from
01:26:16 to 01:26:32 UTC on 24 October. Receipts state this explicitly. No
retained `go test` binary or local Docker/race evidence is claimed. The root
audit independently checks retained raw results and final source; it does not
upgrade the tool-output-only diagnostic run into raw-audited evidence.

These changes make failure attribution usable; they do not identify or fix the
hosted construction predicate. Genuine restart/replay and complete CI, E5 and
M4 acceptance remain open.

## Prepared-claim fixture retains its completed owner

Local commit `17f400c15ae0c6bea6f8002502aa5698a1102820` replaces only the
initial redundant `New` call in
`TestLiveGatewayRebindCrossStorePreparedClaimProcessRecovery` with the fixture's
completed manager and a read of its options. The completed domain already owns
the same database and installs the fence/current-state repository callbacks.
The fixture's separate repository wrapper is database-equivalent; this change
does not rely on wrapper pointer equality or change any callback.

The actual abrupt child still opens its database and constructs a fresh manager
in a separate process. Its post-claim exit, two recovery processes, immutable
history, active fence, runtime heads, grant provenance, Docker inventory, routed
request count and LAN assertions are unchanged. The unrelated proposal test's
fresh-manager replay is also unchanged. This corrects parent setup; it does not
replace fresh-process recovery with an in-process replay.

Tagged ingress compilation, exact parent discovery, formatting, tagged vet and
canonical generation passed on unchanged source. Actual Docker execution was
not run locally. Receipt
`prepared-claim-owner-reuse-20261024T011520Z/receipt.json` has SHA-256
`21317414051edc70a1591502db8b9a86c521e694846ad36bae2d05c89d85f9cf`.
Root audit `prepared-claim-owner-reuse-root-audit-20261024.json`, SHA-256
`2ed7f81d52edeb9212031b15d13f59cd88bb55bc87deb08f525e6c3599564738`,
binds the exact block-only diff, source, binary, discovery and command exits.
Compile, formatter and vet have exit records without dedicated raw logs.
Independent source QA SHA-256:
`20166f8f343de5ae982088f671b33203c5659d3a792346c42b0171dc7da1cd39`.

## Hosted child attribution remains incomplete

Published `c4591b325bb28fd82b36c72399e9d0b2ce657974` ran through synthetic
merge `5d78315a8f9f7086669a8f487273288f76f47c6a`, with identical tree
`46ab4b2ba235d1c88cbf85d03b1fc36a25f28b0d`. Its
[TLS child](https://github.com/tyhuang9/rig/actions/runs/40048828365/job/120736636827)
again reports `controller_restart/generated_domain/failed`, and its
[process-kill child](https://github.com/tyhuang9/rig/actions/runs/40048828365/job/120736637032)
reports `process_kill/runtime_composition/failed`. Neither includes a
construction stage/reason pair. Raw SHA-256 values are
`b69ca73be916c1e0f17c1b94aa506cbe4c79ba7032275f076f73a8b9a258f051`
and `c18a407b1ba89e1a9b7356d454968df5e729abb1d7b1ebe348c33f242fe040e1`.

This confirms incomplete attribution; it does not identify the failed predicate.
The same head's prepared-claim test fails at the initial redundant manager
construction addressed above. Its final-config lost-acknowledgment test still
fails the intended fresh-manager replay. The migration journey still pauses at
receipt-owned predecessor retirement. These remain failed hosted journeys.

## Closed attribution for child construction failures

Commit `ce73d337090bfa6251a5fb10847c26a3d4c40365` adds optional paired
`construction_stage` and `construction_reason` fields to the private test-child
diagnostic. Only the existing controller-restart/generated-domain and
process-kill/runtime-composition failure sites can emit an exact existing
allowlisted pair. Public APIs, production construction decisions and fatal
messages are unchanged. Unknown, partial, duplicate, mistyped and ineligible
fields remain rejected, with the existing record and byte limits retained.

The error traversal inspects at most 32 nodes. Security review found that the
initial multi-error loop could still iterate an oversized child slice; the
successor evaluates that slice once and caps it before iteration. The original
review and passing pre-correction control run remain retained. The corrected
Windows tagged control passes one parent and 123 distinct children, including
two actual failing child processes that contain private token/path causes but
emit only the closed diagnostic. No failure or skip was observed. Formatting,
tagged controller vet and canonical generation also pass on unchanged source.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `child-construction-attribution-successor-20261024T010558Z/receipt.json`,
  SHA-256 `b46053438a481b20854c375a15ef46a387a4d99a58dab8926f3b9368c7c62a00`.
- `child-construction-attribution-static-20261024T010626Z/receipt.json`,
  SHA-256 `ae0e0d06874a1203592c57566d870ce17dc4ef8fa3bb241e717f04efb44d1343`.
- `child-construction-attribution-root-audit-20261024.json`, SHA-256
  `f27deb34639bd78befc7b5f376de9d5e2a39238661039b6f40aa077427275d24`,
  independently binds source, binary, exact discovery, raw unique results and
  static records. Formatter and vet retain exit status without dedicated logs.
- Successor source/security review SHA-256
  `d747b1c914b7e622b879f904077856ca4185cb9d5cdc00baeedb2277efe28d3a`.

This is partial attribution, not a construction repair. Early physical and
reservation failures and portions of cold reconstruction still return bare
sentinels or wrapped errors without a finite pair. Absence of the new fields in
hosted output cannot identify the failed predicate. Actual Docker restart,
full hosted/race acceptance, E5 and M4 completion remain open.

## Retained API and frontend construction control

Local commit `de8c592a9748dffe3fe29fe8f33d9c57bdabe080` adds a focused control
for `newGeneratedDomainConstruction` with a retained API and static frontend.
The fixture creates distinct images, containers and invocation receipts on one
network, records health, activates a two-endpoint route, finishes durable state
and releases its original execution, reservation and effects scopes before
reconstruction. Missing or mismatched frontend census entries must be refused.
All three cases require construction to avoid mutation and capacity commands.

The optional explicit-plan fixture path supplies the new component roles and
distinct images. Existing default fixtures retain their previous image values
and roster behavior. No production file changes. The first attempt failed in
the simulated ingress setup before construction and is retained as a fixture
failure. After correcting that simulator, the exact Windows parent and three
distinct cases passed. Subprocess echoes are not counted as additional cases.
Formatting, ingress vet and canonical generation also passed on unchanged
final source.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `two-component-cold-final-20261024T005529Z/receipt.json`, SHA-256
  `638469b1607d85aa7d22325b21537ae3583470359454506f5e8438a4bcc00613`.
- `two-component-cold-static-20261024T005814Z/receipt.json`, SHA-256
  `fcc73663d8e972a2f35d1c2a711e6c766e6b9e295aadc646804e8ca6410bf8be`.
- `two-component-cold-root-audit-20261024.json`, SHA-256
  `bd5d4dcee447c80002d7cd8c548f6529e4f9a525068d14d00d61e901a0943121`,
  binds the three source files, retained binary, discovery, distinct raw results
  and static records. Formatter and vet have exit status without dedicated raw
  logs.

This passing simulation does not reproduce the hosted failure. It covers
internal construction in isolated test processes, not the public constructor,
an actual controller restart or real Docker. Those acceptance gaps remain open;
no production fix or root-cause claim follows from this control.

## Hosted LAN progress after the trace-driver correction

Published `3fb6b9aae0f85079a26f4d33907eebb6517322ff` is bound to synthetic merge
`6bc3ad5fc5421d61dab0254382a29b74a7fa3f1b`; both have tree
`24d3cf7fc5975a279f710b77d0a15fcc867473ac`. At 00:56:21 UTC on 24 October,
the partial run had six failed checks, six successful, 13 running, 115 queued
and one skipped. Snapshot SHA-256:
`69132d096e1dc57ef436564233a32969bc8ebe32ec9f6fb08db2f04aba449755`.

The [two-app LAN job](https://github.com/tyhuang9/rig/actions/runs/40046853497/job/120730457647)
now reaches the redeploy call at `hosting_lan_two_app_live_test.go:322`.
Source ordering shows that its preceding committed upgrade, exact bindings,
two grants, routing matrix, note isolation and network-namespace probes all
completed. Its outer grant trace reports successful authorization, activation,
release, grant and commit resolution for both applications. Redeployment then
pauses explicitly at `previous_retirement` / `retirement_unavailable`, with two
active components and a draining runtime. Raw log SHA-256:
`ac20cb928faf958e4060ff6688e239cf82557619c547b5caa9557e1715ec3844`.

The [LAN external-database job](https://github.com/tyhuang9/rig/actions/runs/40046853497/job/120730457736)
similarly reaches redeployment at `hosting_lan_database_live_test.go:395` after
its upgrade, grant, attested route, scoped-container and frontend-secret checks,
external fixture identity checks and initial note write/read. It then records
the same explicit retirement pause. Raw log SHA-256:
`44d3ce655869a96f8cd9c80537edc22fd3bc9164e3c3cebe1aa3d842bc4a31a3`.

These observed advances close the initial incompatible-hook failure. Both full
journeys still fail, and later restart, persistence and redeployment assertions
are not accepted. Docker namespace probes do not replace physical second-device
LAN qualification. TLS and process-kill children still fail construction; the
migration journey reaches the retirement pause, and stage-copy replay still
fails its legacy Manager reopen. M4 and E5 remain incomplete.

## LAN live fixtures preserve the completed domain's driver family

Commit `4fd8c4b645dd9c98e5ea523a223ea03a913d0188` removes exactly two calls to
`TraceGatewayV2LANGrantForLiveDocker`, one in each controller LAN fixture.
Those calls installed a private driver after complete-domain construction.
Typed admission requires the sealed driver's identity to remain unchanged, so
even the earlier upgrade request was refused before any grant trace could run.
The outer server wrapper still records grant outcomes; all upgrade, route,
database, Docker and restart assertions remain. Production guards are unchanged.

Both exact live parents were discovered. Live-tagged controller compilation and
vet, formatting, canonical generation and diff checks passed. The two Docker
journeys were not executed locally. Evidence under
`pr222-ci-repair-20261023T2125Z/`:

- `lan-live-hook-removal-20261024T004407Z/receipt.json`, SHA-256
  `9a3cc5482414accd6c6b11eb476f240ce6cbf1304293bba5c5a1a4660f2acdbf`.
- `lan-live-hook-removal-root-audit-20261024.json`, SHA-256
  `5daeaa982ac40e83427bdfef9c26cdb4a6bf51783d2978de5db2cbcfd98d283a`.
  The audit proves the exact two deletions and retained assertions. The original
  metadata contains a stale proposed output-directory label; the actual receipt
  timestamps and source hashes identify the run. Formatter and vet evidence
  retains exit records without dedicated raw logs.
- `lan-upgrade-503-hook-incompatibility-20261024.md`, SHA-256
  `8fb30b25ea1882e2f92f452dfda66476ce9fccae13b797507c09f93d36647d4f`,
  records the immutable baseline source cause and final scoped review.

## Follow-up Windows loopback drift fixture

Commit `4ff67898ec861b13b3f9d95d2d4f0aaa617d750f` fixes the negative fixture
`TestGatewayRebindFinalHandoverFreshLoopbackHeadDriftPreventsAllEffects`. It now
uses the actual guarded loopback application's ID after checking fixture
provenance. Its deliberately corrupting SQL also drops the newer activation
guard alongside the two existing test-only guard removals. Production triggers
remain unchanged. The test still requires exactly one modified row, rejected
handover, 12 unchanged protected records, no terminal record and zero effects.

The first ID-only attempt failed at the activation guard and remains recorded.
The corrected source passed exact parent discovery and execution on Windows,
with no failure or skip, plus compilation, formatting, ingress vet and canonical
generation. Final source SHA-256:
`fff4930ee58080ee4b70f7cd4c9608b4681f47ea25368b7ffe3365b31842afee`.
Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `fresh-loopback-head-drift-successor-20261024T003015Z/receipt.json`, SHA-256
  `bcbf3685e735158af5eec71f1f68b3824b6ce87475dcc4bdbe79e846641a101f`.
- `fresh-loopback-head-drift-root-audit-20261024.json`, SHA-256
  `697f95ae99d9114d0273ebd288f0a99fefa88e10e5a7b9683b23eb8417be5626`.

This is a focused local fixture result, not full Windows, race or Docker
acceptance. The correction is not present in hosted head `7791d31` below.

## Hosted four-unit checkpoint: remaining construction and replay failures

Published head `7791d316ed03af6b57bcda4469a2f31ba5bb59b9` was tested through
merge `8156e3854e8535e03f2bf10cb9d2584a3a4133c6`, whose tree matches the head.
At 00:46:17 UTC on 24 October it had 18 failed checks, 19 successful, 14 running,
90 queued and two skipped. Snapshot SHA-256:
`d7da043d6ccbf510459d4698ea6b564e1bb6f2f7c3ed5669f593be4f4fcee336`.
These are partial check counts, not a defect count or milestone acceptance.

The TLS and process-kill journeys again pass healthy-container and Chromium
checks before fresh-child construction fails. Their finite diagnostics now
identify generated-domain and runtime-composition stages respectively. LAN
tests reach upgrade requests returning `lan_gateway_unavailable`.

The V2 fixture correction advances both hosted parents to their later legacy
Manager reconstruction. Several rebind fixtures fail at the same unsupported
same-process reconstruction after a complete domain has reserved the namespace.
Some occur before the actual test journey; others occur only after actual
stopped-stage or serving-stage assertions. Their intended fresh replay and
lost-acknowledgment checks must be preserved when correcting the fixtures.

The [migration approval journey](https://github.com/tyhuang9/rig/actions/runs/40045049124/job/120724748245)
explicitly records `previous_retirement` / `retirement_unavailable`: migration
succeeded with one runner call, while attempt two waits for reconciliation with
its runtime draining. That reaches the unfinished predecessor-retirement step;
it does not establish migration replay or justify bypassing the pause. Raw log
SHA-256: `220d501766c327deef127fa2adcdc279bfd1fbec1632b04f21af2764dc78b566`.

The first ten failure logs are indexed in
`pr222-7791-first-failure-batch-20261024.json`, SHA-256
`bf3654ada60662f791fcb7cb80fda0327ec4236af1bbf36727533fec5cc91de5`.
The read-only Manager boundary map is
`pr222-7791-manager-reopen-source-map-20261024.md`, SHA-256
`a146882b03c1f59318da144f6f6dc6276e7050587a1dca1258f6009ccf54741f`.
Both live under the existing `ci-followups-20261020/` evidence directory.
Six additional failures are indexed in
`pr222-7791-second-failure-batch-20261024.json`, SHA-256
`ec503cae0480b43e95e72253d40eeb4ddb61f2f04fabff88c1a8b0d5eb2fd551`.
The final two captured jobs repeat the initial cross-store Manager reopen and
the Windows drift fixture failure already corrected locally. Their raw log
SHA-256 values are respectively
`3b1f1f7e1ecd9ea96a48768cfc611b0a7291c40c54bebb698a2fc6bd2220dcdc`
and `f49204b793bf676b9e5c6d8d1747a236b6ff9f4381c95b9acbdf0215316fcd80`.
The cold construction baseline, actual Docker replays and E5 retirement remain
open. M4 is incomplete.

## Concrete gateway V2 fixture and completed-session release

Commit `8ab5b21c9289fa4678d391efbbdd7cf7c53fe078` repairs two fixture
incompatibilities in `gateway_v2_live_test.go`.

The two live upgrade parents no longer install diagnostic driver replacements.
The complete generated domain requires its concrete driver family; replacing
three private V2 drivers made its own reference-family check refuse before an
operation or trace event existed. Production ownership checks, actual default
drivers and the durable-operation failure diagnostic remain. Isolated tracing
helpers and their controls are unchanged.

The shared fixture also closes its exact initial candidate session after route
activation, successful V1 response, source-state load and source attestation.
Previously that session held the OS deployment-effects lease until test cleanup,
while later rebind stages tried to acquire the same lease. The session's map entry
remains available for read-only published history. Later mutations use separate
sessions; registered cleanup remains idempotent. No production lock, deadline or
authority check changes.

The published `f400b19`
[successor data-volume job](https://github.com/tyhuang9/rig/actions/runs/40041826101/job/120715216438)
had already completed an actual default-driver V2 upgrade and profile-claim
advancement, then failed at successor-network staging after the fixture's
18-minute context expired. It retained resources without an exact sequence-five
binding. That partial progress supports the source diagnosis but does not prove
the new release fixes the complete Docker journey.

Default and live-tagged ingress compilation, exact discovery of the two live
upgrade parents, formatting, ingress vet and canonical generation passed on the
final one-file source. Docker behavior was not executed locally. Evidence under
`pr222-ci-repair-20261023T2125Z/`:

- `v2-live-fixture-20261024T002329Z/receipt.json`, SHA-256
  `98a3ebd2b5dc07ef9b8a3df90eed7bfe9828e3f85037a0451b055c767f178e7d`.
- `v2-live-fixture-root-audit-20261024.json`, SHA-256
  `c7e10f8538b869b4b9f174b549e575e3163364bba5938281600752fa78cdbd8c`.
- Scoped source security review SHA-256
  `878e495b6ebf2e55ac5d3331bc27ca42f7d27c1e94e9fb0c65a925031630e1cf`.

The aggregate four-unit review, SHA-256
`bca159d1ae7bfb7fc9a9098e375901c970286add66b6e51667b7c3a6150e12fc`,
found no source blocker across the 11 changed Go test paths. Its original pending
commit, documentation and publication conditions remain recorded; the publication
receipt and additive review closure bind the final checkpoint separately.

### Hosted status before the four-unit update

At 00:23:18 UTC on 24 October, `f400b19` had 11 failed checks, 21 successful,
12 running, 96 queued and one skipped. Snapshot SHA-256:
`6a0515b139886c582dcf1ded9ca3f073df0eb433822acc225378954e2655c0a0`.
All 11 completed failure logs were retained against the verified synthetic merge.
These are check counts, not distinct defects or results for the new source.

The [Windows handover job](https://github.com/tyhuang9/rig/actions/runs/40041826106/job/120715501297)
still fails `TestGatewayRebindFinalHandoverFreshLoopbackHeadDriftPreventsAllEffects`
because its intended drift update affects zero rows. Raw SHA-256:
`a0a4ec46f5e520b50fe377f89aea2c15bbcf7c81ad8e8b74df9fcd922241e869`.
That separate fixture correction remains queued. Full Docker, PostgreSQL, Linux
race, receipt-owned predecessor retirement and milestone acceptance remain open.

## Lifecycle and router fixture lifetime completion

Commit `d345cd744222baa04358bf59b2dc0f401da9c8c4` uses the existing retained-root
helper for the lifecycle adapter fixture and the router fixture's running-domain
path. Each package adds the existing `RunTestMain` cleanup wrapper. The router's
ordinary refusal fixture, assertions, database and lease cleanup, production
identity registry and shared helper remain unchanged.

The published `f400b19` fast verification job failed two lifecycle parents and
four router parents with retained-identity conflicts. The correction was checked
with those families together in their package processes, plus the lifecycle
positive control: seven native Linux parents and 17 children passed, with no
failure or skip. Both binaries were cross-compiled for Linux, then run through
WSL with private native `/tmp` roots. Formatting of all four files, both package
vet checks and canonical generation passed.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `lifetime2-packages-20261024T001729Z/receipt.json`, SHA-256
  `7ea9d8c5af475bdf0838555726c54a4ffdf2a87c9130285f5b339647d0c8503a`.
- `lifetime2-packages-root-audit-20261024.json`, SHA-256
  `f5976ba616780f33ec28efc727d4bfe989255365bf5afb1a90b9700c9503a671`,
  verifies source bytes, ELF binaries, exact discovery, raw test census and all
  command log hashes. The additive execution note distinguishes the Windows
  launcher environment from the Linux binaries actually executed.

Independent source review found only these intended fixture-lifetime changes.
The results establish focused non-race regression coverage; hosted race and full
repository acceptance remain open. No specific reused filesystem identity is
claimed from the original failure log.

## Closed child startup diagnostics

Commit `9e9d4ec95fb54343e32cf29678b81f31d1d00214` adds a private diagnostic to
the controller-restart and process-kill fixtures. A failed startup emits only a
fixed child kind, allowlisted stage and `failed` result to stderr. The parent
keeps its existing stderr hash and appends that closed code after the child exits.
Child stdout remains uncaptured. Runtime errors, credentials, paths and arbitrary
command output are not forwarded.

Parsing considers at most 8 KiB and 32 trailing records. Unknown fields, duplicate
keys, crossed kind/stage pairs, non-string values and trailing JSON are refused.
The existing failure paths, ownership, deadlines, worker behavior and readiness
protocol remain unchanged. Capacity pressure is labeled only before readiness.

Windows verification passed one exactly discovered parent and 111 unique child
cases, including a real failing subprocess that writes private values to stdout
and its fatal message. Neither captured stderr nor the projected diagnostic
contains those values. Four explicit cases exercise both byte and record bounds.
Live-tagged compilation, formatting of the three changed files, tagged hostd vet
and canonical generation also passed with unchanged source.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `child-startup-diagnostic-final-20261024T001415Z/receipt.json`, SHA-256
  `62436c7015148612492583a644578b2d4efada8c28f5f61fb064ae36b0918632`.
- `child-startup-diagnostic-root-audit-20261024.json`, SHA-256
  `db7efe78d599a96ada89ddc7629a2232501267babd8f9e25e5a793953c589ad3`,
  independently verifies current source, binary, runner, raw results and reviews.
- Source security review SHA-256
  `b9bf314f3a556748b16055b45039368fb5997912746764ff6fb9f8d6f79edb9e`,
  with additive four-boundary-case review
  `2c1bba679e442fc7add85b68acc141d85a3ecf52224cefbb5c24fa8e0bae4919`.

Two earlier harness attempts passed all behavior flags as one argument and ran
no tests. Their receipts remain explicitly failed. The final run records and
validates four distinct behavior arguments before invocation; it does not reuse
either attempt as a behavior pass. These checks establish diagnostic behavior,
not successful Docker restart or the cause of the original child exits.

## Cold LAN and migration controller fixtures

Commit `551c6529d35dedf97e91e7f7447e5d3e65071030` repairs the three live fixture
files for the two-app LAN, application-owned database LAN and migration journeys.
Initial controllers now acquire a protected owner lease, open the sealed database
and construct a complete generated domain using the same real Docker runner.
The migration path preserves its authority observer and migration-runner factory.
Authentic test roots use the existing process-lifetime retention helper.

Restart closes the original worker, API, database and owner lease before starting
an exact test in a fresh process. The child reconstructs its own owner graph,
domain and worker. The parent keeps authentication values in memory; the private
manifest carries only fixture paths and a closed scenario. Entry requires the
selected test's opt-in and local Linux Docker. Bounded decoding rejects unknown
or duplicate keys, trailing data, crossed paths and symlinks; private object and
opened-file checks remain required. This is not an atomic defense against a
hostile writer with the same operating-system identity.

Review found and corrected six fixture issues: root lifetime, private manifest
validation, a migration count checked before worker completion, dropped worker
errors, missing deferred worker shutdown, and an omitted explicit recovered
gateway-serving assertion. The child now cancels and joins its worker before
checking the final zero-migration count. Worker errors become a closed failure
bit. Fatal cleanup closes the API, joins the worker, closes the database, then
releases ownership. The parent verifies the original gateway operation and its
serving observation through the fresh authenticated API. Existing LAN identity,
secret-scoping, immutable release, migration-attempt and database-counter checks
remain in place.

Native Linux verification discovers five exact parents and executes the one
Docker-independent protocol parent with seven passing children, including
symlink refusal. Four actual Docker journey parents are compiled and discovered
but remain unexecuted locally. Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `cold-runtime-restart-successor-20261024T000057Z/receipt.json`, SHA-256
  `d620dabce36800a40206a1e8dfd0fbf2c921857b936a18ec4947a4a02e869627`.
- `cold-runtime-restart-behavior-root-audit-20261024.json`, SHA-256
  `8fa5461129f3750eabcb08fa91999b387d0c0ebbd215bca798169a11ba689419`.
  It verifies raw results, binary and runner, and reconstructs the tested source
  by reversing exactly the later seven-line migration database-close bookkeeping
  change. The protocol source did not change; the original binary and receipt
  retain their earlier migration source hash.
- `cold-runtime-restart-static-20261024T000419Z/receipt.json`, SHA-256
  `799f61a48764c14848f0a7883c28b4084aa37222b26525470a5e46a12e840de2`:
  final three-file formatting, live-tagged hostd vet and compilation, and canonical
  generation pass with stable source.
- `cold-runtime-restart-static-root-audit-20261024.json`, SHA-256
  `2677378428dfd107fcfb60e5effafaa28505f5fc6f2fed1b065418a2c47fd635`,
  independently binds the final source, static logs and tagged binary.
- Successor security review SHA-256
  `c7f3fe021252afa1a31511082b15c75d6e89fbd64b1f736588f2afcdddd6ea06`,
  with additive cleanup-order and test-count erratum
  `72b57d1ea428bd7e39e71bcc41ec20153d94598c37c0f307bbf18043520db008`.
  The original six-finding review is retained unchanged.

These controls establish the fixture protocol and source checks, not successful
Docker restart, migration recovery, complete CI or milestone acceptance.

## Native V1 unmatched-host response correction

The correction was published in `f400b19929bdb9dbb8e2fcc8c8c983d34976cc3f`.
Its hosted run advances past the original startup probe failure. The
[controller TLS job](https://github.com/tyhuang9/rig/actions/runs/40041826113/job/120714445899)
reaches healthy API and frontend containers, passes the `ExternalDependencyOutage`
subtest, and records a successful deployed Chromium create journey. It then
fails when a fresh controller child exits with code 1 before readiness. The
[process-kill job](https://github.com/tyhuang9/rig/actions/runs/40041826113/job/120714445660)
also reaches healthy containers and Chromium create, then fails at the
`before_build` child's readiness boundary. These are partial journey results.

The 23:56:14 UTC snapshot has six failed, five successful, 13 running, 116 queued
and one skipped check. All six completed failure logs were retained against
synthetic merge `b599e594bb1d50691c1c047c0e8762be22d939ae`, whose parents and tree
match the published source and unchanged PR #220 base. Snapshot SHA-256:
`ab7e735784e018ac3a835820865a52ae207b3c1219cf3b013ec701b0d3e38d49`.
Failure index `pr222-f400-first-failure-batch-20261023.json` has SHA-256
`66df864b1e441e40c63c1e0a50d1f47e9e3f371115b3c0b752386cabd8f104c3`.

Both failed child diagnostics are `empty`. The fixture captures only stderr;
its Go test failure output is discarded on stdout. Several child startup stages
can fail before readiness, so the underlying cause remains unproven. A private
closed diagnostic is required; arbitrary child output must remain private.
Read-only source map `pr222-f400-child-reporting-map-20261023.md`, SHA-256
`35b3a6162a708ee10b62196afda5f195ebbea43ed773dedc8c547acee1835d25`,
records that boundary. Full Docker/race and M4 acceptance remain open.

Commit `b13ddf5b72bbba90a566362ca78f5877a2fc3c66` adds one empty 404 fallback after
the native V1 gateway's application proxy routes, including an empty bootstrap.
Previously, V1 generated only host-matched proxies, while the existing startup
probe required an unmatched host to return an empty 404. The pinned
[Caddy v2.11.4 source](https://github.com/caddyserver/caddy/blob/v2.11.4/modules/caddyhttp/server.go)
leaves an unhandled request at the default 200 response. This
source mismatch explains a concrete path to the observed static-response refusal;
the hosted log does not expose the actual response status.

V2 already had the fallback. It now receives that fallback from the shared V1
builder, retaining the same order: state-bound probes, application proxies, one
404 fallback. LAN listeners, host restrictions and per-port routes are unchanged.
The probe, exact canonical configuration comparison and refusal guards remain.
Previously retained V1 configurations without the fallback differ from the new
canonical config and remain refused. This change does not adopt or rewrite them;
they need an explicit later migration or recovery decision.

Six exactly discovered parents pass under native Linux, with two direct fallback
subcases and three distinct bootstrap subprocess cases. The controls cover empty
and populated V1 routes, intended-host proxy precedence, V2 probe order and local
route equivalence, LAN isolation, stopped-gateway repair and native bootstrap
receipts. Echoed subprocess results are counted once. There are no failures or
skips. This is controlled non-race execution, without real Caddy or Docker.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `static-response-404-20261023T234433Z/receipt.json`, SHA-256
  `d7f0f77249c9796f91f973e6247be411dae587c4a8b821492d6c19f7b4acaf7e`.
  The pre-run script, exact selector, discovery, binary, source manifests and raw
  execution output are retained. Native temporary roots are under private `/tmp`.
- Its additive `census-erratum.json`, SHA-256
  `b571a19adbd6a7b07e9e184d2528854c456e7decfbc726fdfcc6d23cfb71bd87`,
  includes indented child passes without changing the original receipt or replaying.
- `static-response-404-static-20261023T234541Z/receipt.json`, SHA-256
  `9d4ae04a5fbdc5b708fc9ca3c4105bebf062fcac6e35346c9233219a8ceb596d`:
  five-file formatting, ingress vet, live-tagged compilation and canonical
  generation pass with unchanged source.
- `static-response-404-source-security-review.md`, SHA-256
  `a6ea5153a64369b883d9a775e91f3e0ba37dad619a01690cd02769341f91f349`,
  finds no blocker in the exact five-file change.
- `static-response-404-root-audit-20261023.json`, SHA-256
  `7b67a72c970bb9ec917313fc26253b9c05ad0473a3041ca77fa406bee10b2c63`,
  verifies the current source, binaries, runners, raw census, static logs and
  review bindings independently.

The preceding `44ca010` head's 23:47:50 UTC snapshot had 14 failed checks, five
successful, 14 running, 107 queued and one skipped. Snapshot SHA-256:
`5b5371fcf357fdd3c36a7bd83c8360dbd616bd48b8b242dc92408c71a4609b37`.
Its synthetic merge `8e54614e1f03a7f0e695a470f715dfc90252f66b` has the verified
PR #220 base and `44ca010` as parents, and the same tree as that head. These
results precede the fallback correction. Full Docker/race acceptance, genuine
cold LAN/migration fixture composition and predecessor retirement remain open.

## Runtime fixture lifetime completion

Commit `2fb2f9a66dd3e73a842e57356c9359eb7bc42b2d` changes seven authentic test-root
allocations in four runtime test files to the existing process-lifetime helper.
The initial caller map missed three allocations; a complete search of the sealed
fixture's callers identified and included them before publication. Test assertions,
the helper, package cleanup and production identity guards are unchanged.

Nine selected parents passed in one native Linux process, with five child passes
and no failure or skip. This includes the three sequential capacity-source cases
from the hosted failure, candidate lifecycle, both network fixture paths, log
limits, image/cleanup behavior and concurrent retained-capacity release. Exact
discovery matched the roster, all 14 unique RUN names have matching PASS records,
and temporary roots used private native Linux `/tmp`.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `runtime-lifetime-completion-final-20261023T233557Z/receipt.json`, SHA-256
  `bbe481934b3623118afe2d384531005372e63b459b919aa263f4ec989262c748`.
  It retains the runner, binary, original commands, environment and source hashes.
- `runtime-lifetime-completion-root-audit-20261023.json`, SHA-256
  `cf22763563647e81705462f093d5825e9837af0fb54934f029418cb1cb421f2e`.
  The audit adds the five indented child passes omitted by the receipt's summary
  parser without changing or replaying the raw run.
- `runtime-lifetime-static-20261023T233628Z/receipt.json`, SHA-256
  `ce47b39b23b19154904075677625fad4378d68576110775df2eccec65feb2b04`:
  four-file formatting and runtime vet pass with stable source.

An earlier path/TMPDIR harness attempt failed before discovery or test execution
and remains retained. Independent source review and root diff review found only
the seven intended substitutions. These focused non-race results do not establish
that the full 47-parent hosted failure set passes or identify a specific reused
physical identity. Hosted race verification remains required.

Canonical generation also passed using bundled PowerShell 7.6.5. Receipt
`runtime-lifetime-canonical-20261023T233904Z/receipt.json` has SHA-256
`bda5dc575b04951fda3526696905051a8934d9bae7f6193169a89f97cbd3e970`.
The four-file source stayed unchanged. Formatting, vet and canonical execution
used the local Windows toolchain with empty `GOFLAGS` and `CGO_ENABLED=0`.

### Hosted attribution result

The 23:39:09 UTC snapshot for `cc1ccce` contains 18 failed, five successful,
14 running, 103 queued and one skipped check, SHA-256
`a4e01933afcf156e26880d0e15ebfd7ae415f0a67945827c256ad5d28b121303`.
All 18 completed failure logs were retained once against verified merge
`4ba5565a37cfff4e2be96f3ce19a287874c37412`, whose tree equals the published head.
The controller TLS, process-kill, final-handover and supported-hostd diagnostic
jobs report `stage=empty_final_locked reason=docker_static_response`.
The [TLS log](https://github.com/tyhuang9/rig/actions/runs/40040252281/job/120709392868)
has raw SHA-256
`f4033109fa47c18ea65523487434b58f73e69e0fdd2eaaa0e21a4d2ea5214e6f`.
This identifies the failing probe boundary; its command/response cause still
requires investigation. The probe and refusal guard remain required.

## Current startup and relay diagnostics

At 23:24:30 UTC on 23 October, the `f4a0165` hosted run had 25 failed checks,
20 successful checks, 11 running, 84 queued and one skipped. Snapshot SHA-256:
`22c4e5c33cc35cac4bdb0b5fe34b7d0b021eb4a84cb746c3b1c9906a2f642947`.
All 25 completed failure logs were retained once. The synthetic merge is
`7b10839bffdc48fabb3b5930438702bbcad34b5e`, with the unchanged PR #220 base and
this published head as parents; tree `f9585c8a2162d802f2bc74662812656d30dfe806`
equals the head tree.

The Docker projection correction has not resolved the construction refusal.
The [controller TLS job](https://github.com/tyhuang9/rig/actions/runs/40037388827/job/120700099882)
and rebind jobs still return the generic refusal. Several constructor branches
produce that error, so these logs do not identify a failed database, root or
Docker predicate. The image-authority `BuildAndReuse` child passed, while its
following `AuthorityLostBeforeBuildLoad` child failed construction; this is not
a passing image-authority parent. LAN and migration fixtures still require
complete domain composition. These are check outcomes, not distinct defect counts.

The runtime core race job also reports retained-identity conflicts in 47 outer
parents and 19 children, without a reported data race or timeout. Source review
found authentic candidate, retained-runtime and network fixtures still using
test-scoped roots. Their missing process-lifetime retention requires a separate
test-only correction; the log does not prove which physical identity was reused.
The existing Windows loopback-head drift fixture also still updates zero rows.

### Windows diagnostic fixture

Commit `c374e3927b89ea526422016c78f9b57a31f5eafa` gives the diagnostic fixture
the supported pointer runner. It retains the original malformed upgrade-claim,
typed diagnostic and wrapped-error assertions. The exact discovered parent
`TestGatewayStartupDiagnosticUsesDirectTaggedIngressResult` passes locally.
The hosted Windows failure on the earlier published head is preserved separately.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- `windows-diagnostic-successor-20261023T230631Z/receipt.json`, SHA-256
  `c378aa4bae989bfef6b5b3e85bbaf38e0905a10491b241b087f51c626a378e20`.
- `windows-diagnostic-root-audit-20261023.json`, SHA-256
  `6456316cb28132d3ee7b9e7e11d1e93d22441755e07ddd7f6157ad116ff15971`.

### Closed relay seed failure projection

Commit `aab482704b093ba24f9eca23a5c7fb9676a18706` stops the seven-statement
controller seed at its first error and emits only the fixed `controller_seed`
phase and one of `deadline`, `canceled`, `constraint` or `other`. Classification
uses context sentinels, a concrete SQLite constraint, then observed context state.
Context fallback attributes an opaque interruption; it does not prove that a
deadline caused the hosted failure. The setup deadline and race timeout remain
unchanged. SQL, arguments, identifiers, paths and driver error text are excluded.

The workflow projects only the exact test/package/output event and an anchored
closed diagnostic. Its shell contract retains the original three accepted and
22 rejected cases and adds five accepted and 25 rejected cases. The Go controls
pass three outer parents and eight direct children, including first-error stop,
classification precedence and a fatal-helper subprocess privacy check.

Evidence under the same directory:

- `relay-seed-diagnostic-successor-20261023T231000Z/receipt.json`, SHA-256
  `6a505adedc4d13059290638f83cd0d8a94a6aba0cfd91d09affcc964600039c9`.
- `relay-seed-projection-changed-20261023T223658Z/receipt.json`, SHA-256
  `7b63cd19b8341ad33df13077f69772629bde66ca98c15cbb587585713195a9df`.
- Source security review in the Go successor directory, SHA-256
  `be76b93c4c7d0f4dd005b35fbe36aa5fe3d90683076ea1759e68788e84971927`.
- `relay-checkpoint-root-audit-20261023.json`, SHA-256
  `fd3fb728b8dd4242c9393f592a3e57679773a32db6bd3abd8f93a85c790a9841`.

Both diagnostic Go files pass formatting, two-package vet and canonical
generation checks. Static receipt
`windows-relay-static-20261023T231230Z/receipt.json` has SHA-256
`7c7bdff2e7fb20778cb0a68d5c3fd1864669e177328ce9b5f02e57f937de5c81`.
Execution used Windows/amd64, local toolchain selection, empty `GOFLAGS` and
`CGO_ENABLED=0`; shell projection ran under native Linux with pinned jq 1.8.1.
Original harness and compilation failures remain retained. These focused
results do not establish PostgreSQL, Docker, race, full CI or M4 acceptance.

### Closed startup-construction attribution

Commit `a4dcbff734a287dc2a06d0c83d79b905afb748d7` labels factory stages and finite observation
reasons, including Docker image, container, volume, network, restart config,
live config and static response. Empty construction distinguishes its locked
and released final checks. Native V1 outer predicates retain the nested reason.
The change preserves checks, read order, locks, capacity and authority behavior.

Boundaries that previously replaced an underlying error still replace it. Only
the closed reason crosses those boundaries; the previous sentinel or fixed error
remains available through unwrapping. Direct reader boundaries preserve causes
only where they were already observable. No arbitrary error text, paths, command
arguments, SQL or identities are added to the diagnostic. There is no exported
diagnostic extractor or new authority input.

The original source review and its erratum rejected missing final-stage labels,
newly exposed collapsed causes and lost Native V1 reasons. Those findings were
corrected before acceptance. Successor source security review
`startup-construction-attribution-successor-source-security-review.md` under
the same evidence directory has SHA-256
`649fe1c17f9eb6b52e0bae823cf67a3346862b93bf024f2458161ed66cc1bdd5`.

The retained Windows binary run in
`construction-attribution-corrected-20261023T2332Z/` passes exactly two discovered
parents: the new closed-attribution test and the existing canonical-input/sticky
classification test. The new parent has four direct and eleven nested child
passes; the canonical parent has four distinct subprocess-owned child passes.
Echoed subprocess output is not counted as additional tests. Raw SHA-256 is
`56b893894e39dd67992664a2d3ec908983c1b04d3359d5fdb8949f99443782c8`.
Both five-file manifests match
`48ba9204fb950580cc3ffb9816bd95ad7042307020d702f3cdf8d9ebd4def8ae`.
Controls inject failures at real reader boundaries, verify recursive absence of
discarded causes and preserve the existing sentinel identities and sticky refusal.
This diagnostic evidence does not resolve the hosted failure; the next Docker
run must supply the actual stage and reason.

The post-execution behavior receipt has SHA-256
`e8966d1232dff74dcbd9656c7afc4bd5a428078b41c3010dce4ad55f76d08246`.
Original command text, environment, statuses, binary and source/raw hashes are
retained; no standalone runner script was saved before execution. Root audit
`construction-attribution-root-audit-20261023.json` has SHA-256
`ad511659ac2b343e16614e2152398c53380734b28ea7d2a24ad069e6e88bd331`.
Formatting, ingress vet, live-tagged compilation and canonical generation pass.
The initial canonical command used an absent PowerShell path; its harness
failure remains recorded, followed by a successful run using bundled PowerShell
7.6.5. Static receipt
`construction-attribution-static-successor-20261023T2336Z/receipt.json` has
SHA-256 `096715c3a17273ddbe8af5c187152c25cc26f4e85d6b10297b627d2b6612e2f9`.
Both gates used Windows/amd64, local toolchain selection, empty `GOFLAGS` and
`CGO_ENABLED=0`. Tagged compilation executed no Docker tests.

## Current Docker reader and fixture-lifetime checkpoint

The 22:48:15 UTC snapshot for published `9117de6` contains 24 failed,
12 successful, 14 running, 91 queued and two skipped checks. Its SHA-256 is
`a8b369a5a6b98f4f128a69689b06170fa03a3581b4c2bf2b304d6e36067e7dbd`.
All 24 completed failure logs were captured once. GitHub tested merge
`b6a459e870daa1db40efec83ea6dc6d04bf2130b`, whose parents are the unchanged
PR #220 base and this published head; tree
`5c6eb61564a4728cc6f5b7a228c54156e8322a3c` equals the head tree.

The Docker jobs now reach a generic construction refusal after bootstrap.
The final-handover job reports the gateway running, whereas the earlier
checkpoint reported Created. This establishes progress through bootstrap,
not successful construction or a completed deployment. The
[controller TLS job](https://github.com/tyhuang9/rig/actions/runs/40034221139/job/120689618493)
also refuses construction; raw SHA-256 is
`18d5cdb0efd5823892f79bffcc9fc52b5f040e0bcccee9db3c968cf7932fa973`.
The LAN and migration fixtures still omit completed-domain dependencies.

Source inspection found a separate concrete incompatibility: the strict startup
reader decodes full Docker mount, endpoint, configured-network and network-inspect
objects into partial Rig structs. Docker 28 supplies additional nested fields,
which the strict decoder rejects. Controlled runners emit only the smaller Rig
shape, so they do not reproduce this difference. The exact first predicate in
the hosted refusal has not been observed. The bounded reader projection and
representative Docker-shape controls below have passed focused local checks;
the successor hosted run must establish actual Docker acceptance.

The source diagnosis and its additive nested-shape correction are retained as
`ci-followups-20261020/pr222-9117-construction-reader-source-diagnosis-20261023.md`
(SHA-256 `409d734a24971b8a48fc67d930dba04bbb399bef9f26e7d397ccf026e1ea7c77`)
and `pr222-9117-construction-reader-nested-shapes-20261023.md`
(`f9babcf21ca7b26e3dc46d670d6741d51acde707359962c1483ced1b405970a0`).

### Authentic test roots live until package exit

Commit `92fa55ca862f1f5ba255e5ac721dac3590f58af8` changes only test fixtures
and test support. Authentic fixtures in eight packages retain unique private
roots until `TestMain` finishes, matching the process lifetime of sealed
registry identities. The production registry, collision predicates and absent-root
guards are unchanged. Identity/drift fixtures that deliberately remove roots
continue to use their existing scoped lifetime.

The helper removes only its recorded process root after validating its lexical
scope and the exact non-symlink parent, root and exclusive marker identities.
It proves absence after deletion, and cleanup failure makes an otherwise passing
package fail. The process root and fixture children are private; the parent may
be the system temporary directory. These are observed identity checks, not an
atomic guarantee against a hostile concurrent writer.

Focused native Linux execution passed nine outer parents and four direct
children across eight package binaries. All eight invoke the new `TestMain` and
exit zero. Separate cleanup controls passed two parents and three children,
including preservation of an external symlink target. Windows could not create
that symlink and records the limitation. The earlier Windows fixture run passed
ten selected parents, including its platform collision control; selected image
and controller children do not constitute full parent scenario sweeps.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- Exact 21-file manifest: `lifetime-cleanup-source-20261023T223929Z/source.json`,
  SHA-256 `60e3b21df68c6ef0c6250af2bc5ffd8a70e7c108cb8a30c5bdbf08a8212b368d`.
- Native Linux commands, binaries and raw logs:
  `lifetime-linux-20261023T224044Z/`; temporary files used private Linux `/tmp`.
- Cleanup controls: `lifetime-cleanup-focused-20261023T224004Z/`.
- Source security review: `retained-root-cleanup-security-successor.md` in the
  manifest directory, SHA-256
  `75fc147212fb3265bb3b041be1d4858ee2ea7512cb9782d85bb73f2bd4d50efb`.
- Independent source/command/binary/raw audit:
  `retained-lifetime-root-audit-20261023.json`, SHA-256
  `73e0e52c6496a56651ad96f1ee8ffd079dacc73a14acd90ac03fd9fea4bdfb5f`.

Formatting and diff checks exited zero with empty stdout. The initial static
receipt incorrectly treated 11 Windows line-ending warnings as failures;
`lifetime-static-20261023T224228Z/receipt-erratum.json` preserves and corrects
that classification without replaying tests. Its SHA-256 is
`77e22568b4bc2509f254ded410a0025eadb6ec592821587beab7041e9e7fbac2`.
These focused results do not establish full-suite, race or Docker acceptance.

### Startup-only Docker inspection projection

Commit `8ee108102a984a4916883d85640f74ff41eeb042` adds private container and
network templates for the startup reader. They explicitly project the fields
already consumed by Rig's validators from nested mounts, network attachments,
configured networks, IPAM and members. The global inspection formats remain
unchanged. Detailed reads still require exact full IDs and strict JSON decoding,
including unknown-field, recursive duplicate-key and trailing-value refusal.
Ownership, census, resource limits, port and network validation, command bounds,
body clearing and observation ordering remain intact.

The initial five-parent Windows gate passed four parents and failed the shared
empty-owner transcript because its controlled runner returned the old network
shape. After correcting that narrow test-support dispatch, a successor passed
the shared transcript and direct projection parent, plus all seven test-support
parents. Together, these runs cover five distinct ingress parents and seven
test-support parents. The original failure remains recorded; the four original
passes were not rerun wholesale.

The direct projection controls render the actual templates against representative
Docker-shaped objects. They cover omitted unconsumed fields; nil, empty and
multiple nested collections; refusal of the old full payload shape; and unknown
or duplicate fields in both detailed readers. These are controlled template and
reader tests, not execution of the Docker CLI. Stable gateway, typed recovery
and empty bootstrap behavior are covered by the other selected ingress parents.

Evidence under `pr222-ci-repair-20261023T2125Z/`:

- Initial gate: `startup-projection-20261023T225223Z/receipt.json`, SHA-256
  `32dbd7b141d52fd3d9228133a6f7cd23898061878f6703ab047360c4a563aefe`.
  Its additive `receipt-census-erratum.json` restores the test names that the
  receipt parser incorrectly recorded as empty; it does not change raw outcomes.
- Focused successor: `startup-projection-successor-20261023T225709Z/receipt.json`,
  SHA-256 `9d59ff578a7ad8b4935ab6594795eece4a58f78fc3fdd419993d06998bd25225`.
  Both four-file manifests match
  `65bc34cda6be1bdf97e27add4d90c38d6b21c20fd931e30856f4af59c8672135`.
- Source security review in the successor directory:
  `startup-projection-source-security-review.md`, SHA-256
  `1750a38d2672cb93d0dc1048add383ff1f941d85adb2226638db21982b2e0787`.
- Independent raw/binary/source audit: `startup-projection-root-audit-20261023.json`,
  SHA-256 `66181ace6b4918aec9a51b08447bb660650d3670c9a25ceff2f39ec264ae8c8c`.

Focused execution used Windows/amd64, `CGO_ENABLED=0`, automatic toolchain
selection and empty `GOFLAGS`. Separate formatting, ingress/test-support vet,
live-tagged ingress compilation and canonical-generation checks passed with
local toolchain selection and empty `GOFLAGS`. No module files changed. Static
receipt `startup-projection-static-20261023T225859Z/receipt.json` has SHA-256
`b3f834b42e8470fd8562fd6729ad24da2bf5edbebb8cb06a2c057e84cbb12865`.
Tagged compilation ran no Docker tests. Full-suite, native Linux/race and hosted
Docker acceptance for this reader change remain open.

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
