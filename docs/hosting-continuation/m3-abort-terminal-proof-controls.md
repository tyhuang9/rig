# M3 ABORT terminal proof controls

Date: 2026-10-16

Status: bounded component repair and local verification complete. Executable
candidate: `ea896a8dc3e21f1ee8f97ddd64079065dcc8ea6a` (tree
`dcdea007fa1594f40e71853002b9b48ec737bec7`). This evidence does not establish
complete exported-wrapper success, actual Docker acceptance, or complete M3
acceptance.

## Finding and repair

The typed rollback observer performed the required repeated predecessor reads,
successor absence and ownership checks, withdrawal check, repeated stage read,
and final effect guard. It then returned the raw physical route observation as
`PredecessorRoutesDigest`. The durable ABORT terminal format validates the
purpose-bound digest of the protected predecessor checkpoint instead. A real
rollback could therefore reach a stable, read-only terminal state and still be
refused by the terminal proof reader because the two valid digests described
different evidence.

After all existing physical and authority checks pass,
`observeGatewayRebindRollbackServing` now derives the route digest with the
existing `gatewayRebindTypedCheckpointRoutesDigest` helper. A helper error or
invalid SHA-256 refuses the observation. No earlier physical check was moved or
weakened.

The change does not alter generic predecessor reads, raw route observations,
forward proof formatting, facade comparisons, receipt formats, admission,
database schema, migrations, or workflows. The defect was reproduced
independently of the hosted R01 investigation; whether it explains that failure
remains unverified.

## Component controls

`TestGatewayRebindAbortedTerminalProofCoreIsFreshAndReadOnly` starts with the
native typed rollback fixture, reaches a real ABORT through the production
reconcile path, applies the protected receipt and retained rollback under the
coordinator, and reopens the durable readers and manager. Its positive case
requires the exact `rolled_back`/`ABORT` identity and sequence, `DBCommit=false`,
the predecessor selection, and verified current and terminal digests. It also
requires no active claim, recovery, or fence. Recovery and startup SQL
snapshots, protected file identity, latches, and the mutation-effect log remain
unchanged.

The late-revival case records a successful first current proof, injects the
successor resource externally, then enters the ABORT terminal observer. The
observer refuses with a zero response, no claim or transition write, and no
production mutation effect. The injected successor remains present after the
refusal, proving that the read-only terminal path did not silently repair the
external drift.

The exported-wrapper negative removes predecessor inventory. Source ordering
requires that inventory before the direct host probes; the control proves
failure with unchanged SQL, protected state, and effects. Its command-probe
counters do not observe direct host probes and are not presented as doing so.

`TestGatewayRebindRollbackTerminalObserverBindsCheckpointRoutesDigest` is a
limited observer output-contract control. It first produces a stable native
rollback, then uses the observer's explicit predecessor-reader seam to verify
both checkpoint union formats. The gateway-upgrade checkpoint is the native
rollback checkpoint. The gateway-current checkpoint changes the protected
checkpoint input while retaining that stable native transport observation; it
does not constitute a full rebound-current physical or SQL terminal
acceptance. Missing and mixed checkpoint unions refuse after the two physical
reads. Stopped predecessor, route drift between reads, and final-guard denial
also refuse with their expected read counts and no mutation effects.

All three new test parents match the shared `^Test(GatewayRebind|InspectGatewayRebind)`
selection and none of its specialized exclusions in
`scripts/check-generated-runtime-race.sh`, so each belongs to `rebind-history`
exactly once. No workflow change was needed. The completed Linux history jobs
in PRs #175 and #177 took roughly 24 to 26 minutes in total job duration. The package
deadline remains 32 minutes; the added durable controls increase the risk of
approaching the hosted job limit. No Linux race or hosted workflow run is
claimed here.

## Red reproduction

The pre-change COMMIT baseline at base `75b3f4d52cfde2ced05a0f606224a69384e89c89`
passed its two parents and three nested cases: five test pass events, no failure
or skip, and a package pass at 336.178 seconds. Its JSON log is
`BASE/abort-terminal-proof-controls-verification/baseline-windows.json`, SHA-256
`C4820ED42D1E66DAF72FA28252548600463798AFB6023F2D1351BC2B5CD8EAAE`.

The unchanged regression was committed separately as
`99bd69f7199b3ce222f0f298f6ac847b16ea8756` (tree
`cc5bf0bdb0cc856fa4c26e001a25f4b905fd630e`, test blob
`9adae0acf12866eaca15cba2e693f9ca259e065d`). The bound `/verified` execution
failed closed in 94.16 seconds and the package failed in 94.30 seconds, with no
pass or skip. The execution receipt recorded the same commit, tree, blob, source
bytes, SHA-256, and clean status before and after the run. Log:
`abort-terminal-route-digest-verification/bound-red/bound-red-windows.json`,
SHA-256
`78F037A704AE923B624476D756135C6C508C841B90378973837FFEC529B8F160`.

The historical source-binding QA supplement was reconfirmed at 08:28:12. The
final supplement hashes are
`2DD9BAFFE032C70B3087925DB67518BFCA1B1E7B96CA88BF6CC295D6703074C6`
for Markdown and
`F61A34E0473691E03FE953EBBE2A65017829F4E02E2FB69EAF70F9C7C3B1CD3A`
for JSON. This is a binding of the earlier clean execution receipt; it is not a
claim about later working-tree state.

## Final executable evidence

The final Windows and WSL runs selected the same seven parents:

- the two existing COMMIT terminal parents;
- the two new ABORT terminal parents;
- `TestObserveGatewayRebindRollbackServingUsesRetainedAttemptWithoutEffects`;
- `TestGatewayRebindTypedRollbackRequiresFinalPhysicalReadbacks`; and
- the new checkpoint-digest observer parent.

Both receipts required the exact seven-parent and 14-subcase roster, 21 test
pass events, zero failures, zero skips, and one package pass. Both record the
same candidate commit and tree, production blob
`c0e0b489ce4dd722883f46666b17b388da4f8416`, test blob
`9b900dd17dd2747c9379656b1b9634a3e3f777c6`, and unchanged source SHA-256 values
before and after execution.

| Environment | Result | Evidence |
| --- | --- | --- |
| Windows amd64, Go 1.26.7, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0` | Exit 0; exact roster; package elapsed 931.221s. | `abort-terminal-route-digest-verification/final-windows/final-windows.json`, SHA-256 `C3A4A1494D66EA5A2C115D49BB4B9A29ED104533C36E88F5A3DAEE03A0AD9116`; receipt SHA-256 `AD868B674765C16441918B0B9A32E7B5581872117E885821BE11BB2CE26376A7`. |
| Linux amd64 under WSL2, cross-compiled by pinned Go 1.26.7 with `CGO_ENABLED=0` and executed as an actual Linux binary | Cross-compile, Linux execution, and offline `test2json` conversion each exited 0; exact roster. The recorded 86.451s interval covers compilation, execution, and conversion. The JSON package elapsed belongs to the later offline conversion and is not a Linux package runtime measurement. | `abort-terminal-route-digest-verification/final-wsl/final-wsl.json`, SHA-256 `601546446041F3194D4AD6C84E16CBFC2905A40E298A6775EBB8789AA0FFF29E`; raw log SHA-256 `F6A7348C538B178432EF63009403313FFC9A4132010FB935E33592B586CE8015`; receipt SHA-256 `2EF8B674712DD483634BE0B2BEBA33EA89805559977BD64FB8ADCA4640FEBF5F`. |

The WSL receipt records the exact Python-launched `wsl.exe --exec` argv and the
Linux binary SHA-256
`547EB86C2269C13D33B7518E799DDAA4B0C8A23669062511659F191E065E9854`.
This was an actual WSL execution, not a cross-compile-only result.

The canonical `pwsh -NoProfile -File scripts/check-generation.ps1` invocation
resolved the pinned Go 1.26.7 executable and exited 0. The receipt also records
exact Git-blob `gofmt` equality, `git diff --check`, only the expected production
and test paths at the executable commit, unchanged source hashes, and identical
base/head trees for both migration directories and `.github/workflows`.
Canonical receipt:
`abort-terminal-route-digest-verification/canonical/canonical-receipt.json`;
generation log SHA-256
`206E8A0F5AF0AE28333EE2524C7E138884F8974C32891D13E8256EAF26851557`.

## Limits and continuation

The positive ABORT component proof uses production durable SQL/protected readers
and controlled physical transport. The current-checkpoint observer case is the
explicitly limited seam described above. The exported wrapper has a fail-closed
missing-inventory control; a successful exported-wrapper run over actual Docker
is still pending. Actual Docker, the hosted Linux race partition, second-device
LAN behavior, publication, merge, deployment, and controller restart are not
established or authorized by this evidence.

Reverting the observer change would restore a digest mismatch for valid typed
rollback terminals. Rollback of this code must preserve SQL, protected history,
and external resources; deleting durable evidence or repairing external drift
would invalidate the read-only terminal invariant.
