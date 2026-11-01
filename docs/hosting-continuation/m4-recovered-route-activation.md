# Recover interrupted route activation

Status: the bounded local compatibility suite and static checks pass on the continuation of [PR #259](https://github.com/tyhuang9/rig/pull/259). Hosted Docker and Linux race verification remain incomplete. M4 is incomplete.

Hosted CI has not run against these implementation changes; the historical hosted observations below describe the parent revision.

## Purpose

A process can stop after publishing the candidate route but before recording activation (042). The database still reports `SwitchingRoute`, and its deployment head still identifies the predecessor. Recovery must distinguish this interruption from a different route change and must preserve the exact predecessor and candidate capacity charges.

The published parent implementation clears the protected pending transition before 042. In the CurrentV1 state machine, the configuration-file move precedes that clearing step, so an interruption immediately after the move can still retain `Pending`. A later interruption can lose it. An interrupted state without that transition does not retain enough evidence to reconstruct the exact previous route safely. Such ambiguous historical states must refuse recovery; receipts or a deployment's own head cannot supply missing authority.

## Recovery contract

Retain the existing protected `Pending` transition, including its exact previous and proposed routes, until activation is durably recorded and independently read back. Use the existing protected format and immutable activation history.

Recovery also needs to distinguish the original candidate creator from the worker recording activation. The parent activation constraint requires both attempts to be identical, which a fresh worker after a crash cannot satisfy. The additive internal migration 046 records activation writer identity separately. Historical rows remain untouched: an absent writer pair means the original creator was also the writer. A partial writer pair is invalid. Exact current job, attempt and execution-binding checks remain required; older creator provenance cannot authorize a new write.

Cold construction may recognize only a pending transition that agrees with the exact SQL state, immutable candidate evidence, protected identities and physical observations. Construction restores accounting and identity; it does not authorize route mutation or manufacture activation. The candidate charge remains candidate-owned and the predecessor remains active until the normal activation boundary promotes ownership.

Previous-versus-proposed classification and its selected gateway proof must use the same bounded live/restart configuration observation, tied to the exact reader, immutable container and restart file. Mixed configurations or failed reads must refuse classification. This startup observation is descriptive only; the later scoped worker must obtain fresh physical evidence before acting.

A fresh scoped worker must revalidate the claim, deployment head, retained transition, exact candidate identity and health, and live route before continuing:

- If the exact proposed route is already serving, finish activation under fresh authority without repeating physical publication.
- If the exact previous route is still serving, authorize clearing of that exact retained transition, verify the cleared state, recheck the worker claim and follow the ordinary switch path.
- If routes, protected state or physical identities are mixed, missing, foreign or changed, refuse and retain conservative accounting.

An interruption after 042 but before clearing the pending transition requires idempotent finalization after exact readback. This is recovery across separate stores, not an atomic transaction across those stores. No capacity or coordinator mutex may span external commands or SQL. Preserve the existing bounded gateway lock around protected-state and physical phases, release it before the 042 SQL transaction, and reacquire it for revalidated finalization. The scoped OS effects lease spans the worker transaction lifecycle. After activation, the existing [recovered predecessor retirement](m4-recovered-predecessor-retirement.md), accounting handoff and completion paths apply.

Every finalization retry requires another exact activation-receipt and durable current-claim check. The effects lease does not freeze job cancellation or worker identity. An uncertain first attempt may require reconciliation, but ignoring caller cancellation must never bypass a durable cancelling, stale or crossed claim.

For CurrentV1, physical observations while the transition is pending must match the exact projection of that pending revision. The final stored state has its own subsequent revision and digest. Begin, finish and rollback validate the appropriate exact state at each boundary; they must not compare a pending observation to the later persisted revision or accept an arbitrary revision offset.

Public route observation continues to withhold the URL while a protected transition is unresolved, even when direct requests reach the proposed route. The hosted crash test must check that refusal alongside exact retained-transition and SQL evidence, direct traffic and immutable container identities. Normal public observation must succeed after recovery finalizes the transition.

## Verification to record

Establish the failing baseline using the real sealed physical publication path, discard the old handles, then reconstruct cold state before obtaining a fresh worker claim. Do not fabricate `Pending` or an activation receipt in place of the real transition.

Verify supported gateway modes, both exact route observations, stale and crossed evidence, cancellation, uncertain physical outcomes, interrupted finalization, exact accounting and immutable-history preservation. The existing hosted `TestLiveControllerGeneratedProcessKillRecovery/after_route_commit` child supplies the real process-crash check; keep its assertions, required roster and time limits intact.

Record exact source and command identities, discovered cases, raw results and remaining limitations. Local simulated physical behavior cannot establish hosted Docker acceptance.

### Observed baseline

At base `8525d9cfc3eb089914157d4b6939724b6ccb355e`, the regression uses the authentic LegacyV1 activation path. It waits for the successful proposed-configuration reload, delegates the following restart-configuration move, then terminates that invocation with `runtime.Goexit` before 042. The test joins the invocation and leaves the old handles before constructing a new domain.

The captured state has the proposed live and restart configurations, cleared `Pending`, the predecessor SQL head, healthy predecessor and successor containers, and no successor receipts 042–045. Cold construction refuses with `cold_construction / cold_group_reconstruction`. Prefix, SQL and protected-history preservation, physical-effect counts, and capacity checks all run before the expected failure.

The pinned Go 1.26.7 compile and exact test discovery passed. `TestGeneratedDomainRestartAccountingSwitchingRouteRecoversActivationWithoutSecondShare` then failed at that intended boundary in 1.114 seconds. This establishes the defect; it is not a passing recovery test or an actual process-kill result. Earlier setup failures are excluded. The successful reload precedes the selected move; this baseline does not claim a numeric reload-count assertion.

A later development checkpoint on 2026-10-31 successfully reconstructed cold state and reached activation under a fresh worker, then failed the activation SQL constraint. The original candidate creator was attempt 2 and the current worker attempt 3. The constraint required its single creator-attempt field to match both. This establishes why separate writer provenance is necessary; the complete recovery test still failed and is not implementation acceptance.

Frozen evidence:

- Test source SHA-256: `2f3a46df35279f3a4f8a7638e1193819feb0adc300e6ef234bbc63542bdbe7ff`.
- Execution receipt SHA-256: `7c1bfc28b5386595bb66bae87059d9386c2bb50f2583cefa5b66fcec2273099f`.
- Raw failure SHA-256: `7b874b904e070b56ec19e543832f2d79fcc41176e45d47d246750fadb1bdfca0`.
- Independent 14-artifact audit SHA-256: `8537e47a4ae385e9d0a65bc82223c72384010d557bfc35370daa1f8536e35269`.

### Parent hosted observations — 2026-10-31

PR #259's completed hosted run used head `8525d9cfc3eb089914157d4b6939724b6ccb355e` and merge `c9666e0e8668ae2d9b0bdbbbc7760fe8a78eb347`. Its process-kill test recorded these passing children:

| Child | Duration |
| --- | --- |
| `before_drain_stop` | 134.35 seconds |
| `after_predecessor_retirement_before_handoff` | 123.99 seconds |
| `before_build` | 102.38 seconds |

The next child, `before_candidate_start`, failed during cold container attestation. Consequently the parent failed and `after_route_commit` did not run. The passing children establish only those exact boundaries on the parent source; they do not establish this activation repair or complete milestone acceptance. The captured raw job log SHA-256 is `f8549e0354975e4257ba308d9bf769937db7a6a1b9577f870191f255cf0b0015`.

### Focused local checkpoints — 2026-10-31

The writer-provenance migration and reader checkpoint passed 23 unique cases: five parents and 18 nested cases, with no failures or skips. This covers fresh installation, identical migration mirrors, preserved historical upgrade, transactional rollback, legacy writer normalization, partial identities, crossed jobs and bindings, cancellation, and creator/writer/completion ordering. Its source-manifest file SHA-256 is `86b7240641233fb8731a741a8cc51e930c134a4aa8cae76fef5259b96d440eb4`. The database and reader raw hashes are `4d169e24914e6c4c7466b124c48b4fd2ba4c3fbc5b16cf3d5d80ec114be1efd8` and `1b343c7c0bf7e80c492f582ecacffbfdec10247fbb44407f263535bd3bc547b2`.

The authentic pre-042 replacement journey then passed on a later frozen source. It discards the old handles, reconstructs cold state, obtains a fresh worker claim, and completes 042–045 without another route publication or capacity share. It verifies the original candidate creator and fresh activation writer separately. The outer parent took 1.57 seconds; nested process output repeats that same case and must not be counted as additional cases. Six separate seed controls check exact roster restoration and refusal of missing, crossed, post-activation, partial or non-present inputs.

That recovery source-manifest file SHA-256 is `e9ce7caa074662c2acead69a74fa7e79fe422e45203fbc3804bd16ec5e80051d`; its binary SHA-256 is `a7d446c6341e5a80ee71623d96de78da0a339f79834ef7a370d5c803404517ab`, and its central raw-result SHA-256 is `70ebe79625229dc0ae3bdc749347c245fee91722e8b874ef35d889fbe070ad3f`.

A later frozen checkpoint passed 18 unique cases across four parents, with no failures or skips. It reruns the authentic replacement journey and seed controls alongside exact configuration-observation binding, owned-buffer handling, read failures, and previous/proposed/mixed/ambiguous classification. Compilation, exact discovery and execution all passed. The raw output has 20 passing events because the central journey repeats its parent name in nested process output; those repeats are not additional cases.

That checkpoint's source-manifest file SHA-256 is `96c081749c97f3ae16d641b3d2ae2339900184ab8b5d49a2f734d561ac9bf4b9`, binary SHA-256 is `5d6707b39d0af5e4d9758b344d80485ab931f8dbef2a9e090d02e3c4c5882639`, and raw-result SHA-256 is `3dc7098c3d007634ce5529b1643a018dca43f08a89135e277d87f556ff50a141`. Independent captured-evidence audit SHA-256 is `fb7b4fb076d5a9e575c45de55acfec09c49f394546110a957a3b506614d41b04`. A discovery-launch argument error occurred before any test process started; the corrected discovery used the same compiled binary, and that launch diagnostic is excluded from acceptance.

The post-042/pre-clear positive journey also passed as one independently discovered parent in 1.59 seconds. It uses the real activation producer to retain `Pending` after the atomic activation write, reconstructs cold state, obtains a fresh claim and completes recovery. Assertions preserve the exact activation ID, creator, writer and head; require no repeat publication or capacity share; and check pending-marker clearing and predecessor retirement. The established test clock verifies exactly one requested `maximumDrain` wait. This does not measure production drain timing.

Its source-manifest file SHA-256 is `95b56b3a07cbf724a8ca71fb52ed8c7aeb55e4b249f40565f611f298a0cd523d`, binary SHA-256 is `4794102d110d334cfdcdce43edee2bf506dfe8912b02298a3c60d3f0733f25ae`, and accepted raw-result SHA-256 is `1120ddeb1231729e62bf2d6b89965fa85f0ac6125f73b7eaabac4a8a57ac4ac7`. The original manifest's summary hash used an incorrectly described sorting operation. A separate preserved correction, SHA-256 `bdf5bbe786a9941590997bfe625bce804c7f80ac25f9eb129ce386f85aa531f4`, records the canonical ordinal-path composite `a2bdbad6c1d3fd910eaa927b920f2f0b4e7a5b8db5a7726e8f3a44fc02ed58a7`. The independent audit verifies the original manifest, all 31 copied files, that correction, compilation, discovery and accepted target; its SHA-256 is `46ff7f464150c5b924be8eb4d995ab66d050d9fc851faaf8b7a57bc1e1927143`.

An earlier malformed selector launched unintended tests and was stopped. Its output is excluded. The accepted target-only retry followed recorded process quiescence, unchanged source hashes and exact argument validation, using the same compiled binary and successful discovery. Three nested passing events in the accepted output represent the same parent, not three cases.

### Recovered claim-gate checkpoint — 2026-11-01

Compilation, exact discovery and the focused run passed on a later frozen source: seven unique cases across two parents, with no failures or skips. The run repeats post-activation recovery, directly checks refusal for stale attempts, durable cancellation and crossed job/binding at the first finalization gate, and exercises the real `CompleteRecovered` entry point for a lost reply after persistence. A still-current worker reconciles that lost reply; an attempt changed before retry is refused. The retry refusal preserves the sole activation receipt, creates no 043–045 records, and performs no reload, restart installation, stop, remove or new capacity reservation. Read-only attestation commands are not counted as physical mutations.

Source-manifest file SHA-256: `9d2a795926f78b786abcb9e9d2dbae6a5f3cc339513e3b4ea761388fa6f85bcb`; binary: `edabc5ea0a62a217705ee656378d0e813fe9ab0590acc8f153de6c884ddc1ab6`; raw result: `7b90ba9497d95832f9d9ae1b6bfc11b449a77e2d849e1c29657ba52dca81fda9`; independent audit: `993ad6261046c8319979415addf9f2d3ca7eef54d1f9affadf00ee861e5a40df`. Twenty raw passing events include repeated isolated-process output for those seven cases. Earlier fixture and launch failures remain excluded.

This checkpoint does not yet establish exact live/restart file-byte preservation or complete dual-roster debt invariants. Caller-only cancellation, refusal before an uncommitted clear, and ordinary fresh-activation finalization still need their dedicated controls.

A subsequent finish-gate checkpoint passed ten unique cases across three parents, with no failures or skips. It adds genuine recovered-entry coverage for a first finalization failure before persistence followed by an attempt change, and caller-only cancellation that reconciles the marker before a later fresh worker completes. The refused or cancelled boundary checks now include exact live/restart configuration bytes and both capacity rosters, as well as immutable receipts and absence of premature completion/effects. Its separate ordinary-activation test covers the shared helper only; the actual activation entry-path mutation controls remain outstanding.

That source-manifest file SHA-256 is `e895d08c4649daf42092ed6974d7997a0eb8db3a74e5a246e2794d50c1cc86ec`; binary SHA-256 is `b6c471aea30e25399fd247dbce130398a7a88d45e3dd636ee834f0cc804988f6`; raw-result SHA-256 is `f679f7fa87ec9eaed7ee0f4118670dec03ca5166ca32242c2bbb918e14b47eb6`; independent audit SHA-256 is `836aebfbe7945a9adb7e532597a3e2f489d9243389192f41855d2e9623ac19bd`. Twenty-nine raw passing events represent those ten distinct cases.

The actual fresh `Activate` entry path subsequently passed four unique cases: one parent and three children. The unchanged positive control reaches the real activation write and finalization. Durable cancellation before the first finalization attempt and an attempt change after an injected pre-persistence failure both refuse finalization. The checks preserve the retained transition, exact activation history, configuration bytes and both live capacity charges. The ordinary switch performs two restart-file installations and one proposed-route reload; finalization refusal adds no physical publication. Compilation, exact discovery and execution passed with no failures or skips.

That checkpoint's source-manifest file SHA-256 is `69ceade486df397c1e96205369f9f44a945ccf73fb5e89d6f1930f58473739af`; binary SHA-256 is `cf0354d8b584e18f12fcb8bcf76b8ff3a4fc5a86a7b7910f66c0d17a0ba85152`; raw-result SHA-256 is `8e194ba2eac9b0a40b47418bff1e12c1109648261a5a02376aa334d6f61714d7`; independent audit SHA-256 is `258a8267b8bd53a456b5f09fa21642f4667898be8fce1c9987dfee33712e6698`. Eleven raw passing events represent those four distinct cases. Earlier fixture failures are excluded. This checkpoint does not establish the separately tracked lost-acknowledgment compatibility test.

The existing `TestGeneratedRouteActivationResolvesLostDurableCommitAcknowledgement` compatibility test then passed separately in 0.61 seconds, after compilation and exact discovery. Its fixture now uses the existing strict configuration-read adapter at manager construction, matching the new physical readback contract without replacing the manager's runner or weakening the original receipt, head and duplicate-publication assertions. Source-manifest file SHA-256: `617bde98e6e7bae09163b49c31c2ea7889403c8953cb30d3d2d816f42c41a89d`; raw-result SHA-256: `0474d8015e11bab58803f7edb0f501ab7080736d4f8029db7538ae75ef4f1f49`; independent audit SHA-256: `8e5da9474bbe10a2c18612778b05a88cccff66a04a6dfbf03b1f3839ff8d6271`. This is one passing case with no failures or skips.

Initial-deployment recovery then passed seven unique cases in a separately compiled and discovered parent, with no failures or skips. The authentic first switch is interrupted after publication but before 042, leaving no previous route and a canonical empty generation-zero head. A fresh worker completes generation one without another publication or capacity reservation. The exact restored candidate charge becomes the retained active-route charge. Six helper-level controls reject nonempty or crossed head projections; the actual pinning path also refuses a nonempty source slot. Those helper controls use cloned snapshots and do not claim to mutate or exercise corrupted live database history.

That checkpoint's source-manifest file SHA-256 is `b1408fa9481664dc57543bbd94eab78a9fcdb32cdc75c89f4895308471934d63`; binary SHA-256 is `3c93c38af5ca7aa24f8eccac66e7a1b68032f50185ae581e20e398008be7255b`; raw-result SHA-256 is `e3b2a1fba7a7df477662b44295345e86660177f56e26b96b31f3479ec14505fe`; independent audit SHA-256 is `a43fcd75d1b3816fa7dcb433ffe7db54634e51a93ddbd7c4813d71e26ab94c8e`. Nine raw passing events represent those seven distinct cases. The outer parent took 1.25 seconds.

The authentic LegacyV1 exact-previous journey also passed as one independently discovered parent in 1.58 seconds. It interrupts publication while the previous route still serves, reconstructs cold state, obtains a fresh worker claim, clears the exact retained transition under authorization, performs the ordinary switch and completes 042–045. It checks original creator and fresh writer identities, immutable history, exact capacity ownership and physical effects without a second reservation. Three nested passing events represent that one case; compilation and execution passed with no failures or skips.

Source-manifest file SHA-256: `bddd34d283e123369823c8bf221842a69b639fbc4a41cffe93228a8aaa63d5f0`; binary SHA-256: `16bbe29e78bc28164740941ae8487e5777c99a5200425a2e5c33b0070917e649`; raw-result SHA-256: `e8a2d7f02a6c080b2c01b160c104a565f19a349725890da314bc27c7de5f37f5`; independent audit SHA-256: `66264b6fc975ee6f0535284e304ab0774821b6ad9c3179d91eab12bef5bb49b4`. Earlier failed or unexecuted fixture versions are excluded.

The subsequent PreviousExact authority checkpoint passed eight unique cases across one parent and seven children in 10.33 seconds. Five entry-path controls cover a lost clear reply, a failure before persistence followed by an attempt change, stale and cancelling workers before clearing, and an attempt change after a successful clear. The pre-persistence failure retains the full decoded protected state and `Pending` when retry authorization fails. A fresh claim in the same constructed domain can finish after an authorized clear. Two additional helper controls reject crossed binding and expected-head inputs without rewriting immutable database history. The no-expected cold-reader helper also refuses the resulting markerless input; this is not a separate OS-process acceptance test.

Compilation, exact discovery and execution passed with no failures or skips. Twenty-three raw passing events represent those eight distinct cases. Source-manifest file SHA-256: `89bfccecf379bb878f49e4446d499b99172decbba4c9d718d18411e1a2d19877`; binary SHA-256: `44c9591474dc3c7061b4df3c685d2775521a24e0111a9bce3865d0b4dbc936f7`; raw-result SHA-256: `04d349a385ca5c0143a13775ef55837d87acc5e56bc803eab8cb73981b60a5d0`; independent audit SHA-256: `6a1b4f843dea5dba2a3ef14eb81162ed14ad04b49731d90a69b85a66394fd0e8`.

The three gateway-manager matrices then passed 27 unique cases: three parents with eight children each. LegacyV1, NativeV2 and CurrentV1 each cover exact clearing, already-cleared replay, crossed protected state, proposed/mixed/unknown physical configurations, missing protected state and cancellation. The Legacy controls distinguish a durably stored empty initial state from a missing file. This exposed and repaired a production ambiguity: ordinary bootstrap may interpret an absent file as empty, while recovery and its readbacks now require the file through the same protected-read operation.

CurrentV1 checks compare the complete cleared state, exact revision and recomputed digest, origin, LAN configuration, applications and selection. They construct the real retained-V2 projection and preserve its source, native state, journal, lineage and terminal history; only its current-state reference advances. These are manager-level controls, not additional full controller-crash journeys. The CurrentV1 parent took 83.24 seconds, LegacyV1 0.31 seconds and NativeV2 1.78 seconds.

Compilation, exact discovery and execution passed with 27 passing events and no failures or skips. Source-manifest file SHA-256: `7bd710d7d5a5d503d877d8e20069bfed6a4a40267e2d99f63c1c79b40f3a7f19`; binary SHA-256: `f511867bed871673806d4c322c95860d4028b6c3866a91a2caf62c0d2a5b0c37`; raw-result SHA-256: `a519637fb159ad3377529b1c88145163a79eff8b3852902f230eb452e727fb06`; independent audit SHA-256: `5a5c5c9e25cc8edc0d5bd33070ad8937913134d2c119ce0d9e4e81a1932e5909`. Earlier failed or unexecuted fixture versions remain excluded.

The hosted-test bridge compiled with the `live_docker` tag and discovered both the pure boundary-controls parent and the required live process-kill parent. Only the pure controls ran locally: 26 unique cases passed in 0.01 seconds, with no failures or skips. They check the exact retained transition and predecessor-head prefix and reject missing or crossed identities and prematurely written 042–045 records. The live bridge preserves the boundary roster and time limits, checks SQL in the parent before and after killing the child, expects unresolved public observation at the interrupted route boundary, retains direct-traffic and container checks, and verifies original creator, fresh writer and current-worker identities after recovery.

Source-manifest file SHA-256: `65134004e8c5f05d4a1d08290e9ee0b629ced65a63ffb370db35a9727772e684`; tagged binary SHA-256: `69c08b958a7d7999a4e05030537a31ea9d3188cc005e4e45c9c1512344269dd1`; pure-result SHA-256: `2b0c66c5445e5f98e401e24f0d864d048805ee2285eea591d45dad78e3e27d89`; independent audit SHA-256: `4641da0ac18b689eb87e1f55db6e9b54f4f9a2292f95b98f05762430ca8788ff`. Discovering the live parent does not establish Docker execution.

The cold protected-mode projection checkpoint then passed three unique cases: one parent with NativeV2 and CurrentV1-with-retained-V2 children. It executes the production classifier, owned projection cloning and gateway-route extraction, including companion isolation and crossed-origin refusal. The NativeV2 case exposed two startup contradictions: stable topology selection rejected a retained route marker, and the LAN classifier treated it as an unknown LAN operation. Cold route selection now validates the exact committed topology using an owned marker-cleared copy while preserving the original marker. A route marker supplies no LAN recovery authority: unresolved LAN claims still refuse, and the ordinary stable/rebind selectors retain their restrictions.

Compilation, exact discovery and execution passed with no failures or skips. Source-manifest file SHA-256: `8c850f6d04d8cfc10acc9333062174117afea96fa75f85b2c8e5483307df4867`; binary SHA-256: `2224e29d2cb451e710aa4652bd1f8277a160893d59b76f435d98c6233e18a9d8`; raw-result SHA-256: `69ec277f80868b97a930e3bc7aea977ff094b7cee27375168ef879a299755a48`; independent audit SHA-256: `b18d00996487eaa0d3d0128aba30314990313b65f199af07472128d214affbe7`. The parent took 10.15 seconds. Earlier failed or superseded projection packets are excluded.

These remain layered checks. The authentic LegacyV1 journey executes the common worker-pinning path; the two protected-mode children exercise their classifier/projection inputs; the manager matrices exercise all three mutation consumers. Independent source review follows the connections between these layers. Full NativeV2 and CurrentV1 cold-construction-to-worker-pinning journeys remain unexecuted and must not be inferred from this composition. Final compatibility and static checks still require verification. No actual Docker process-kill acceptance is established here.

The first final compatibility attempt then failed: exact discovery found all 67 selected ingress parents, of which 48 passed and 19 failed, with no missing or skipped parents. Failures include a cold-runtime timeout and existing activation, accounting, multicomponent and rebind consumers. The complete failed run is excluded from implementation acceptance while those causes are investigated. Its raw-result SHA-256 is `7cd2f4b22be0362164c369da2fef86ab3422aebd0c3bbfab5cf9d5629fca98dd`.

The subsequent compatibility corrections preserve the existing fault and refusal assertions while giving older fixtures the required construction-time physical-read adapter and truthful retained-transition outcomes. A direct isolated trace showed the timeout followed a fixture read failure: cleanup waited on a scope still held by the aborted test. The corrected cold-runtime case passes without a timeout change.

The rebind consumers also exposed a production revision mismatch in CurrentV1 begin, finish and rollback. Their pre-persistence physical checks compared the pending projection against the later durable effective revision. Those three checks now use the exact pending projection; immediate application and post-persistence checks still require the exact durable effective state. No revision-offset tolerance was added.

A focused checkpoint passed all six selected parents and 23 unique cases in 187 seconds, with no failures or skips. It includes both real rebind consumers, gateway-selection attribution, SQL rollback fault cases, four CurrentV1 revision controls and the eight-row CurrentV1 clear matrix. One revision control supplies a valid later revision and digest as pre-persistence proof and requires refusal with the marker unchanged and no effects. Source-manifest file SHA-256: `eb290833d222a2308b7c7018f0689a72718e509bbd289773e338b98438d9d95b`; binary SHA-256: `6c75a6cec7a2ff67ede21402e4dcf308a522a4b66ce554c6b120898366bf97eb`; raw-result SHA-256: `6d90248631f50e9b528359dcfbbb874b84c4a719a0fd085e727fe48001e7a9d0`; independent audit SHA-256: `8f35ee02d493cf3f4462cfae31d5d97609ad7c726b2d2bb2a95c6bb47fc31ae7`. Twenty-five raw passing events include repeated isolated-process output. The complete final compatibility run remains required.

### Final bounded compatibility suite — 2026-11-01

All 89 selected parents passed on one frozen source, with 360 distinct passing cases, 465 raw passing events and no failures or skips. Nested process output accounts for repeated events. This final execution supersedes the earlier checkpoints for these selected cases; their counts must not be added to this total.

| Package | Selected parents | Distinct cases | Target duration |
| --- | ---: | ---: | ---: |
| `internal/generatedingress` | 67 | 239 | 335.659 seconds |
| `internal/database` | 4 | 20 | 5.580 seconds |
| `internal/generatedruntimestate` | 9 | 47 | 16.100 seconds |
| `internal/deploymentexecution` | 2 | 9 | 3.636 seconds |
| `internal/deployments` | 4 | 12 | 0.789 seconds |
| `internal/jobs` | 2 | 7 | 0.613 seconds |
| `cmd/hostd`, pure controls with `live_docker` tag | 1 | 26 | 0.036 seconds |

The accompanying source file `docs/hosting-continuation/m4-recovered-route-activation-test-roster.json` records the exact package selectors, expected parents, time limits and tag. Each package was compiled with `go test -c`, discovered using the binary's exact anchored `-test.list` selector, then executed with `-test.v`, `-test.count=1`, its exact anchored `-test.run` selector and the recorded timeout. Go 1.26.7 used `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and `CGO_ENABLED=0`. The hostd live parent was discovered but not run; its separate target selector executes only the pure controls.

The 46-path source-manifest file SHA-256 is `1fefa7de2b908315dda86d1acd08e8c0a2770238301594d69576fbc3e687adae`; its content composite is `2bbd40890a947d69593255a24a88d7a296778386ef915f6742a5409b0f492bf8`. The ingress raw-result SHA-256 is `1255c35da2fdead5c48e39e19f049cebbc640a9e75e54fd011d494658d84b335`. Independent audit of all 21 compile/discovery/target stages, their source copies, binaries, arguments, output hashes and case census has SHA-256 `2adc1088ae42efcdf386b7292d4a4958424d1a45c1121aff90fad2b576440423`.

Read-only formatting verification (`gofmt -d`) produced no changes for all 44 changed Go files. The following static checks passed with no output or errors:

```text
go vet ./internal/database ./internal/generatedruntimestate ./internal/deploymentexecution ./internal/deployments ./internal/jobs ./internal/generatedingress ./cmd/hostd
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go run ./cmd/openapi-gen -check
```

The controller contract was separately compiled, exactly discovered and executed with `-test.run ^TestOpenAPIContractMatchesRegisteredRoutes$ -test.count=1 -test.timeout=5m`; that additional parent passed in 0.02 seconds. It is separate from the 89-parent compatibility total. The independent audit of these seven static/contract stages has SHA-256 `2e5c653bbf3ab077f915c37fc0285c1500f09cacee8049eb29c03d12e3a859c7`. All 28 native stages passed, and the 46 captured source files remained unchanged. Final documentation-build results are recorded with the PR so they can bind the completed document without modifying it afterward.

This is a bounded Windows suite, not a full repository run. Hosted Docker, Linux race checks and the full NativeV2/CurrentV1 cold-to-pinning journeys remain unexecuted.

## Rollback and limits

Do not run an older binary while a newly retained pending transition exists: older startup recovery may roll it back without understanding this protocol. Finish recovery with the new binary, or establish a verified return to the previous route before downgrading. Preserve all immutable receipts and conservative capacity debt.

The internal migration is additive and must preserve existing receipt values and digests. Do not remove the new writer fields or rewrite receipts to accommodate older code. Upgrade compatibility remains part of the required verification; a source revert alone is not an approved live-state rollback.

External databases remain application-owned through scoped runtime secrets. This work adds no managed database service or Neon provisioning. Publication as a draft does not authorize deployment or a GitHub merge.
