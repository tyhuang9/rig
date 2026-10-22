# M4 generated runtime package race partitions

## Observed failure

The generated-runtime `runtime-packages` job exceeded its 34-minute workflow
step on PR #206 at `5e6c130e9b42dfa736c448ea905f76ed1291b829` and PR #207 at
`f3fe1c269105e996226bbfc86bc24fd875919980`. The logs show overlapping package
execution. Nine packages completed successfully; `generatedrecovery` was still
executing tests when the workflow stopped it. Neither log contains a test FAIL
event or a race warning. The incomplete recovery run remains failed acceptance.

| Evidence | Job | Raw-log SHA256 |
| --- | --- | --- |
| PR #206 | [120007930053](https://github.com/tyhuang9/rig/actions/runs/39831326527/job/120007930053) | `713d6347d95f7790a913f42d57fc2266379ab6b1cf13b765f86729ec72c8b576` |
| PR #207 | [120024004394](https://github.com/tyhuang9/rig/actions/runs/39835893597/job/120024004394) | `30ee2a1c0f1e7e9cc66dbd352ee875230002b902a365a33c611a93793650e688` |

Each log's immutable tested merge was checked against its published head tree.
This failure is separate from the controller route/draining failures and the
baseline's successful ingress-remainder race job.

## Change and preserved coverage

Only the generated-runtime lifecycle workflow's single runtime package row is
replaced by four complementary rows:

| Selector | Complete packages under `internal/` |
| --- | --- |
| `runtime-packages-core` | `projectanalysis`, `deploymentplans`, `generatedimage`, `generatedimagestate`, `generatedmigration`, `generatedruntime`, `autodeploy` |
| `runtime-packages-executor` | `generatedexecutor` |
| `runtime-packages-state` | `generatedruntimestate` |
| `runtime-packages-recovery` | `generatedrecovery` |

Their disjoint union is the original ten packages. There are no test-name
filters, renamed tests or dropped subtests. The existing `runtime-packages`
selector remains available for direct callers. All selectors retain the
default `-race`, `-count=1`, `-timeout=32m`, JSON output, pipe failure handling and
nonempty parent-test gate. The workflow keeps its 34-minute step, 42-minute job,
and aggregate requirement that every matrix job succeed.

The repository and Windows workflows have ingress-only matrices for this shared
runner. Those matrices remain unchanged. The shell contract checks all 26
ingress rows in each workflow, the four new lifecycle rows, package arguments
in default/race/plain modes, and the existing aggregate dependencies.

No Go source, module dependency, runtime authority, resource cleanup, protected
history, database behavior or deployment wiring changes. External databases
remain application-owned through scoped runtime secrets.

## Executed local verification

The canonical `pwsh -NoProfile -File scripts/check-generation.ps1` passed on
22 October 2026, 19:56:27–19:56:43 UTC, using pinned Go 1.26.7. All 1,048
Go/module files remained unchanged. Receipt SHA256:
`9f9bebe6679c0dfc4be0896472cfb6e0e391668b55b37f34ce8b58ca8bf45c14`.

The source manifest is
`m4-runtime-package-race-partitions-verification/root-canonical-20261022T195627Z/go-module-manifest.sha256`,
SHA256 `76ad3ae2c7924205be6251ea09034aa1123a35e58ade726a41f4f36f5dbcd2d9`.
Documentation dependencies were installed with
`pnpm --dir docs install --frozen-lockfile` using pnpm 11.22.0; all 129 packages
were reused from the existing store.

Both Bash syntax checks and `bash scripts/check-generated-runtime-race_test.sh`
passed under WSL Ubuntu using the retained jq 1.8.1 binary. The contract ran
42 positive invocations, checked three workflow matrices and passed its 16
existing refusal/failure cases. Its Go executable is a temporary argument-checking
stub; this result is selector verification, not a Go race-test result.

Four additional controls on isolated source copies confirmed that the contract
rejects a missing or duplicate runtime matrix row and an omitted or duplicate
recovery package. A fifth control accepted a CRLF copy of the workflow. The
repository source hashes were unchanged by those controls:

| File | SHA256 |
| --- | --- |
| Runner | `bb7880eb78964ae0cb05ebf75a792cac0768a1264b87349950a14d0869d65655` |
| Contract | `fa0a23ea6b529354c0e197b1fdc4ba9b0270a5ddfb070f5ccba399d2a7f7259e` |
| Lifecycle workflow | `2fd2e904777d1000c370449cd110c14cb2087591086e8bc7792516a5fd470db3` |

The command record is retained in
`m4-runtime-package-race-partitions-verification/command-log.md`.
The initial default-WSL probe could not find jq (exit 127). Initial Bash CRLF
syntax failures were repaired by normalizing the two scripts to LF. Two
disposable-copy harness failures were corrected before the accepted controls;
their causes and results remain recorded. The successor used the existing
checksum-verified jq binary without installing software.

## Remaining acceptance and rollback

Local checks do not establish the duration or race result of the new hosted
groups. Hosted CI must run all four groups and the unchanged aggregate gate.
Controller Docker route failures remain separate work; this change does not
claim M4 completion. There is no product UI or manual application flow changed.

Rollback is a cohesive revert of the selector, contract and matrix changes.
That restores the original combined runtime job and its observed timeout risk.
No application data or runtime resource rollback is involved.
