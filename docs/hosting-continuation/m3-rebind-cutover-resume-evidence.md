# M3 rebind cutover resume evidence

Date: 2026-10-16

Status: bounded runtime repair and local component verification complete. This
branch starts from `99499dc165319f680cc04236171a5b640a1f0504`, the inherited
PR #182 dependency. Publication and hosted acceptance remain pending.

## Finding and repair

The `lost-cutover-ack` fixture can retain a valid prepared rebind after the
durable sequence-15 cutover intent and exact predecessor stop complete, while
the stop command does not return success. Fresh recovery previously treated
that exact intermediate state as unsafe and selected ABORT. The bound
pre-change control reached a verified `rolled_back` terminal at sequence 17;
the scenario contract requires forward completion.

`serveSuccessor` now resumes only when a stable production observation proves
all of these facts:

- the retained plan began with a running predecessor;
- the same predecessor is now stopped, retains an unambiguous address, has a
  changed stop-bound observation digest, and has no routes;
- the stage is absent and the exact bound final container is present and
  stopped;
- config, data, and ingress resources are present, while the final config
  digest and application topology still match the retained plan; and
- successor routes remain absent.

The existing current-selection authority check remains before this decision.
The existing forward-session guard remains immediately before the final start,
and a fresh stable observation must still prove the exact successor serving
state afterward. Normal cutover now uses the same stopped-state predicate after
its predecessor stop. The change does not broaden rollback-only mode, bypass
the forward guard, repair drift, repeat the predecessor stop, or adopt an
unknown final container.

The live manifest now describes the tested boundary as predecessor-stop
completion under the durable cutover intent and points to `serveSuccessor`.
The distinct `lost-successor-serving-ack` scenario continues to describe an
already serving successor.

## Controls

`TestGatewayRebindTypedHandoverRuntimeRecoversAfterPredecessorStopAcknowledgementLoss`
uses the existing real composition fixture, repository, SQL state, protected
history, typed runtime, and controlled physical transport. The injection fires
only after the exact successful predecessor stop command. Before recovery it
requires prepared SQL with no database commit, rollback permission, sequence 15
`cutover_intent`, no terminal, one predecessor stop, no final start, a stopped
predecessor, an absent stage, and the exact stopped final container.

A fresh `Manager` calls the exported `RecoverGatewayRebindStartup` entry in the
same test process and must produce COMMIT/current authority, sequence 17, one
terminal, and a released fence. The controlled Docker transport must record
exactly one final-container start and no second predecessor stop. The prior SQL
prefix and protected file bytes remain retained. This is fresh-manager API
coverage, not a fresh hostd process or actual Docker execution; the retained
existing after-stop recovery parent supplies the subprocess control.

Refusal controls keep SQL, protected history, protected file identity, and the
effect log unchanged for a failed forward guard, rollback-only mode, and a
foreign final identity. Eight focused predicate cases cover missing stop proof,
ambiguous predecessor address, predecessor routes, stage presence, final
absence, config drift, topology drift, and successor routes. Every mutated
observation receives a recomputed canonical digest. The address, final,
config, and topology cases are asserted to remain generically valid before the
new exact predicate refuses them; the other four are explicitly generic shape
refusals.

The parent name matches the shared
`^TestGatewayRebindTypedHandover` selector in
`scripts/check-generated-runtime-race.sh`. It therefore runs in
`rebind-typed-handover` and is excluded from both `rebind-history` and the
typed-runtime fallback. No workflow or partition change is required.

## Red reproduction and oracle correction

The two existing parents passed at the clean base in 108.251392 seconds. The
first regression run then failed closed on its positive case in 149.82 seconds:
fresh recovery returned verified `rolled_back`/ABORT at sequence 17. Its two
initial refusal cases passed, and the package exited 1. The immutable receipt
is `bound-red-windows.receipt.json`, SHA-256
`A63755E871FF8B3DB77AE0075660FCAA103E402D1561FE059A97640DD8215782`;
the bound summary SHA-256 is
`7F968820299C70D7BBF6907934462FE3487307C12B74897F85EB460520B5B155`.

The first post-repair focused run passed, but review found that the newly added
predicate mutations retained the original observation digest. That made the
negative table reject too early at generic digest validation. The result is
retained as superseded evidence. The correction changes only that table,
recomputes each digest, and distinguishes valid-shape field guards from generic
invalid shapes. Prefix and suffix byte equality are recorded in
`corrected-negative-table-binding.json`, SHA-256
`19A18D449C8D145D99969B1027F8C7C3D2E0C0EA226C9A5557FAAEB034AB9FA3`.
The corrected subtree passed its parent, direct foreign-final control, and all
eight predicate cases once with no failure or skip.

## Final local verification

All evidence below is rooted at the exact absolute directory
`C:\Users\huang\Documents\Projects\Rig\temp\m3-lan-external-db-20261015\BASE\cutover-resume-verification`.
The executable source SHA-256 values were unchanged through final execution:

- runtime: `643D417895C74806D692F9A6045095574A7B70EE7271CA6D21171F1329E113DF`;
- regression: `D8A8B5DFD8EB3A5D38BC7E96E35C98EC1F62AB39F6D3F0495849D9F10FBE5081`;
- manifest: `EF854BA69A6964E725D7161584AEF7208E1E659DD0CA278CF50646164B0C7299`.

Windows verification was deliberately split. The unchanged positive and guard
blocks passed in the original whole-parent run; the table-only binding proves
those blocks are byte-identical in the corrected source. The corrected-source
foreign-final subtree and all eight predicate cases then passed, followed by
both existing parents on the same corrected source. The live manifest parent
also passed. Receipts:

- `corrected-negative-windows.receipt.json`, SHA-256
  `FC50B3EF69ACF7DBBA7E2223F244198B93F94A81BAA2F660C01972C907E7AFB1`;
- `final-existing-windows.receipt.json`, SHA-256
  `B0B2A2EAE51984638A831331621D66D4DE76BA14A65796B90C3F2A71B4BADE1D`;
- `manifest-windows.receipt.json`, SHA-256
  `0DA92DDB18E9FF25BE09FFCD3345220E522CF26EA0CBDCB78D0DC77F30A820DA`.

Pinned Go 1.26.7 cross-compiled the corrected package for Linux amd64, and the
resulting binary executed under WSL2 with the exact three-parent union. The
receipt requires exactly three parents, eleven nested tests, fourteen pass
events, zero failures, zero skips, and one package pass. The recorded 43.824501
second interval covers cross-compilation, actual WSL execution, and offline
`test2json` conversion; it is not a Linux package elapsed measurement. Named
Linux durations were 22.11 seconds for the new parent, 2.84 seconds for the
existing crash-after-stop parent, and 5.70 seconds for the existing forward
sequence parent. Receipt: `final-wsl/final-wsl-receipt.json`, SHA-256
`D088E70A3A013F013B25502FC9B2F3C7D210BF27A6255AC3E4986F3F74D65162`.

`pwsh -NoProfile -File scripts/check-generation.ps1` resolved the pinned Go
1.26.7 executable and exited 0 with unchanged source and Git status. Receipt:
`canonical/canonical-receipt.json`, SHA-256
`23FA53983D5F9D87A922929565AE126C21A1C255D63127396E3790916EBF4B22`.

## Limits and rollback

The positive control uses real SQL and protected history with controlled
physical transport. It does not establish actual Docker acceptance, a local
race run, the hosted `lost-cutover-ack` result, or complete M3 acceptance. The
hosted lost-cutover correction remains unverified until exact-candidate Docker
execution; previously scoped R01 acceptance is unchanged.

Rollback is a revert of the runtime predicate, regression, manifest wording,
and this evidence document. No schema or durable data migration is involved.
After rollback, this exact sequence-15 state again follows the verified ABORT
path; durable SQL, protected history, and external resources must not be
manually rewritten as part of rollback.
