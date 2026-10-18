# M3 hosted acceptance addendum — 2026-10-18

Status: the hosted scopes recorded here are accepted for the exact published
source. M3 remains open for physical second-device qualification and manual
screen-reader testing. This addendum preserves earlier pending and failed
histories; it does not implement M4 or claim completion of M3.

## Immutable source and receipt identity

The accepted source is [PR #186](https://github.com/tyhuang9/rig/pull/186) at
head `4200796e3e41450e192368976b64089c84445d90`. The published tree is
`46a89e2de30ab88436c00ad12e784be6ff802e65`; CI checked out
`882ed9c605411ec721abf8b842d8addca4520776`. The hosted matrix ran in
[run 39125153533](https://github.com/tyhuang9/rig/actions/runs/39125153533),
attempt 1. The publication receipt records body SHA-256
`4f8ed27f39b6b0b2e0e506f0d7a492bffa2639bf30365d72584109bf4209cc76`, verified
at `2026-10-18T14:03:18.490262+00:00`.

The final matrix gate was [job 117694712258](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117694712258).
It recorded `MATRIX_RESULT=success` and passed the required aggregate guard.
Its aggregate raw-log SHA-256 is
`7ede40dd66b75974a3210f977098af1379940440dc8674c557198d7a3d803be7`; the
complete matrix scope SHA-256 is
`20dffcfbaa43ddb04e64dc7b59154f21c9c8439393ed911600ea1024e52cebcd`.
The aggregate job performed no checkout or test execution.
CodeRabbit was skipped for the draft; this record does not claim a hosted
CodeRabbit review.

## Historical failure and repaired pending route

The earlier [PR #185 pending-route job](https://github.com/tyhuang9/rig/actions/runs/39116235131/job/117652821298)
remains a failure record. It passed its preflight controls, recorded the
historical COMMIT, replay and durable checks, then failed the LAN `/api/version`
assertion after 610.82 seconds. HTTP status was 200, but the expected
`supported-rebind-a-v1` marker was absent. Its response body was not retained;
the actual app/version is therefore not inferred from that run. Independent
cleanup passed and the seven later partition cases did not execute.

The repaired `scenario/pending-route-restart` passed in 606.50 seconds in
[partition 06, job 117679611094](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611094).
It ran once, emitted the historical COMMIT marker, resumed the exact accepted
deployment after process restart, preserved historical replay and durable proof,
and passed the complete successor LAN matrix with A=v2 and B=v1. The child was
reaped and independent fixture cleanup passed. The prior failed HTTP body is
not reconstructed by this result.

## Scoped hosted acceptance

The rows below are accepted hosted scopes on the immutable head. The Docker
rows are event-reconciled to their retained raw events, source/checkouts and
accepted scope receipts. Counts are the selected named tests or controls for
that job; every listed selected test ran and passed once with no failure or
skip, and the independent fixture cleanup passed where the job used it.

| Hosted scope | Job | Count / result | Raw-log SHA-256 | Accepted scope SHA-256 |
| --- | --- | --- | --- | --- |
| Proposal authority | [117668605053](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117668605053) | 1 / pass | `e387c21bceb944f393aea9a6cbcef761816bf028e6a624cfdfb9b26eca7ef989` | `adc1ec6519fab16a8a9aaa239099be776b74aa22efb301b513980d806093c064` |
| First-boundary Docker prerequisite | [117668604998](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117668604998) | 387 preflight + matrix parent + A0 case / pass | `581547e8785f6e142a0b9ae6425bc4b0ccc065e92af2c29675d830574a5e42a1` | `9906627510d82f4df43540961475b3185fa1c3fa999d35235337a4222b262f8b` |
| HTTP controller recovery | [117668605084](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117668605084) | 1 / pass | `6eda66bde46ee536a34206b9e3edb68675b1307856634b00bead92338bd25ba4` | `acb621ffb338dff8fb78b736e6a1a41954e6814b22617d907b0ff89c7f93f976` |
| Docker rollback and replay | [117668605089](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117668605089) | 1 / pass | `a102e78fe09a0a4b5a6561e3d321a9d376b9e20cb92225528b3932c480d4201b` | `c21fd0956e65f723fdd3fdb4ea63178a2ed36b8701e1a3d5382b099aaf7a6562` |
| Prepared claim recovery | [117668605180](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117668605180) | 1 / pass | `47f00f3acbb4f7927ee2c725cfe1163982323cd11c5ed2f53b9498546a5d7bdb` | `4f7135e603d4ee8b254e66af66b59cde06c797f938b7dc3fd3e4816e1775a30a` |
| Runtime commit and replay | [117668605126](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117668605126) | 1 / pass | `8c24fa77251b6074e6ede8c5b52dec48902397445e8b1615320b9c4ecb7fab7c` | `b284f791e8ab83e42bd1c64b99c446775b8f3730dfe1eeb9a4435de64ad8edd9` |
| LAN application-owned TLS database | [117668604440](https://github.com/tyhuang9/rig/actions/runs/39125153526/job/117668604440) | journey + 4 helper controls / pass | `ae0644c4ac78cafe56713afe024a30c3601434eedaaf59d8d8db390ab30ae97f` | `1280a12ccbad856e78b694a3146ae6c9a9a4ef3c4dea297a959b229e719b710c` |
| Two-app LAN transport and redeploy | [117668604615](https://github.com/tyhuang9/rig/actions/runs/39125153526/job/117668604615) | 1 / pass | `c87ca1b3154b54303ea6f02238792aaf966ce49f27ef51de116974a1da76ed63` | `1c1b7134f2ab519115376454ec040dce3b22d36f064ba09197e0507f16e4b2a0` |
| Controller browser and hosted TLS journey | [117668604517](https://github.com/tyhuang9/rig/actions/runs/39125153506/job/117668604517) | 3 named cases, including outage child / pass | `be5d9267d305d854048e90a1853319908f46d3ee5d47b8dd2301d8b016b83146` | `087e4d7be6a34dd7578d7e3822643b9feb85859ad29087367c017348efffffff` |

The LAN database scope created and read through the attested LAN URL, retained
the row across redeploy and controller/store reopening, and kept the same
external TLS PostgreSQL container and data volume. The transport scope covered
both apps before and after redeploy, HTTP/API forwarding, uploads, WebSocket,
SSE, SPA assets, denied controller/local-host/unassigned-port requests and
fresh manager observations. The browser journey used real Docker, Chromium and
the application-owned TLS database. These fixtures do not establish a physical
second-device journey.

## Full matrix and race evidence

The hosted Docker matrix contains exactly 63 distinct cases in canonical order
across eight partitions. Each partition ran the exact 387 preflight controls
once; all 63 cases passed once, with zero test failures or skips, and every
independent cleanup passed. The partition receipts are event-reconciled to the
same checkout, source and canonical contract:

| Partition | Hosted job | Cases | Scope SHA-256 |
| --- | --- | ---: | --- |
| 01 | [117679611172](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611172) | 8 | `d09c82cf991411131608d318a15492ee2bed0ea3d21e23e7a263348da397faf5` |
| 02 | [117679611068](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611068) | 8 | `c5d145325c8ff15d0d7f31a4fd62f48b075ba57739c53ffcdbc4c8db5b8ba4d2` |
| 03 | [117679611109](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611109) | 8 | `22384d9df19506d9d3e31689c6eaac8ebc5afda8f260587f2a5a64097ad606fe` |
| 04 | [117679611188](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611188) | 8 | `91ce55dd4c33143d8d3a58418ba025e0d07891a740e27b6105657aaf52e63a94` |
| 05 | [117679611119](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611119) | 8 | `9489f1d62227480fb189ea9b3ef20854f7c196ee5abb36911ffb2a9714e9b8ac` |
| 06 | [117679611094](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611094) | 8 | `b8880967f98df9e033805d7aa8446c899972807f6aee88e779d3d8a72827783a` |
| 07 | [117679611131](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611131) | 8 | `20f5e64d6b96d5d8038e513f4a9189a8b299d1c110f754c1c6b5f9d5f38c8049` |
| 08 | [117679611088](https://github.com/tyhuang9/rig/actions/runs/39125153533/job/117679611088) | 7 | `f3eab36e16680f8528e017f8981ca1c0f4c59c70636f34cacf470f6758df358b` |

The Windows run [39125153522](https://github.com/tyhuang9/rig/actions/runs/39125153522)
passed its 28-job inventory and final gate [117674363182](https://github.com/tyhuang9/rig/actions/runs/39125153522/job/117674363182),
with core job [117668604548](https://github.com/tyhuang9/rig/actions/runs/39125153522/job/117668604548)
and verified scope `9cc9d6a27562f516f9fea01f91e4c44f4bd33e5d658be0b9c77a554ed7a7f77a`.
This reconciles the explicit Windows protection events and final job inventory;
it does not independently reconcile every ingress partition.

The Linux race evidence comprises runtime gate [117685456327](https://github.com/tyhuang9/rig/actions/runs/39125153561/job/117685456327)
and repository gate [117686176427](https://github.com/tyhuang9/rig/actions/runs/39125153516/job/117686176427),
with verified scopes `6506f4b2d004fe1ce9bbd1d651a6f02f83fe16ccdef06273a114f979a8fb9099`
and `483b41759fa21992f19765f1457077a2214af13db552bc1b3fc168704db6b7df`.
These are aggregate-guard and job-inventory scopes, not per-test event
reconciliation. They do not establish Docker crash-matrix acceptance or a
physical-device journey.

## Remaining M3 requirements

Physical qualification remains open. A second device must exercise all roster
applications through the displayed LAN address across redeploy and restart,
address drift, old-interface withdrawal and return, and stale-address
withdrawal. The application-owned external database create/read path must be
performed from that device, with platform, Docker/source/config identities and
cleanup recorded without credentials. Namespace probes and hosted fixtures do
not replace this requirement.

Manual screen-reader testing remains unverified. This addendum creates or
implies no new runtime gate. See the original requirements in
[M0 integration baseline](m0-integration-baseline.md),
[M3 LAN design](m3-lan-design.md),
[supported-controller acceptance](m3-supported-controller-acceptance.md),
[combined acceptance](m3-integrated-acceptance.md), and the
[resumed-route evidence](m3-resumed-route-serving-evidence.md).

No target-branch merge, deployment, M4 work or M3 completion claim is made by
this record.
