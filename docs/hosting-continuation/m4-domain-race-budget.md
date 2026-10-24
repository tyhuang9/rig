# M4 generated-domain race budgets

## Scope and observed failure

Source commit `3dc97886f65f2f7d22c0ffdc2fccbc46e406787b` is a focused
test and CI change over consumer baseline
`69e0ef4f37e31c089b8ee3ebe113c36067ce99d4`. It changes two test files,
the race runner and its shell contract, and three workflow matrices.

The prior [domain-construction race job](https://github.com/tyhuang9/rig/actions/runs/40068387850/job/120796974846)
exhausted 90-second parent budgets while running costly rows sequentially,
then exhausted the separate 32-minute package budget. Completed rows took
about 20–25 seconds. Its raw log SHA-256 is
`17e4becbd06a340817d736f3e5cc54f6a8f44c6fa505f96c9a30eee54984c0e1`.
That failed gate remains failed evidence.

## Changes and invariants

All 59 affected row names, case bodies, assertions and fresh-process isolation
are preserved. The rows now occupy 23 parents, each with at most three cases.
The cap is structural; hosted race execution must establish actual duration.

| Suite | Selected parents | Affected rows |
| --- | ---: | ---: |
| `domain-a0` | 4 | 11 |
| `domain-cold` | 19 | 48 |

Both suites are added to the generated runtime, GitHub deployment and Windows
controller matrices. Their families are excluded from `domain-construction`.
Existing 90-second child-process, 32-minute package, 34-minute step and
42-minute job limits are unchanged. Race detection remains enabled where
previously configured. The runner additionally requires exactly four or
nineteen successful outer parents. Empty output, child-only success, failed
Go execution or failed output capture cannot satisfy the gate.

Production behavior, application secrets, database ownership, runtime authority
and lifecycle state are unchanged. The additional six matrix jobs can increase
queue time. Reverting this patch restores the previous grouping and its observed
timeout risk; there is no runtime migration or state rollback.

## Executed local verification

On 2026-10-24, Windows Go 1.27 compiled the ingress test binary and discovered
all 23 intended parents. One focused execution passed those 23 parents and all
59 child rows: 82 distinct RUN/PASS names, zero failures and zero skips.

- Binary SHA-256:
  `3c80702bbed66227a084c3763980dca656b7da11fe1c011e31b2a0d233cfefab`.
- Focused raw result SHA-256:
  `b132caee72cf2cc60a670dcbac43de0af9be10f3c15ce991dee5640d0eb9b361`.
- Exact argv receipt SHA-256:
  `478fef1a0af8e06001938b2517fa68212fd386a026e0d57612bf3f43f5ca6f8e`.

The same binary discovered 1,232 ingress parents. Actual shell-selector
expansion assigned each parent exactly once across 33 ingress suites, including
all 65 generated-domain parents. The contract ran with pinned real jq 1.8.1.
Its fake Go captured command selection only; it did not execute a race suite.
The shell contract, `go vet ./internal/generatedingress`, `git diff --check`
and canonical `scripts/check-generation.ps1` all passed. Canonical verification
used bundled PowerShell 7.6.5. All seven source hashes were unchanged throughout.

Raw `gofmt -l` listed the two CRLF files. Their exact LF-normalized current
copies were formatter-clean. An owner receipt incorrectly called an edited,
pre-verification snapshot a pre-edit baseline. The original receipt is retained
with an additive correction. A separate root check extracted both actual Git
baseline blobs and ran `gofmt -d`: exit zero, no stdout or stderr. This corrects
the provenance claim without rewriting source or repeating behavior tests.

Retained local evidence under the 2026-10-24 race-budget verification directory:

| Record | SHA-256 |
| --- | --- |
| Final gates manifest | `4ef636697ba6f26b01cf041536e402fa0dd930c981039335b3d12db02d5d2957` |
| Independent root audit | `5210a49ab5a321521caa485cf7945bfbb447db639b33ad8f1dee356939357170` |
| Genuine Git-baseline formatter receipt | `641085208666e1a1f0d6ec2720f417c827df14e05b6886f68e7eacbe431796a9` |
| Snapshot provenance correction | `b474ffa9a74320d6cf1c080b68668dc154c3b16909ad71476819aabd08f02222` |
| Source/security review | `8dc630c27a42b7201ff8b50a28686345696e8f8dfc8de623110ee1f40fd667c1` |
| Final integration review | `c91e356dd2497d615315e91b08b276a66ebeee1413769e2d9f7fc33ff0d3ee36` |
| Execution clarification | `2cf5a318445ba26cbee28cf8ccb935a027f8c1de0980dbcc9ba2122bb8afd417` |

## Remaining acceptance

Hosted Linux race duration and complete CI remain required. Local checks do not
establish Docker acceptance, retirement completion, retained-gateway startup,
complete M4 readiness or deployment readiness. The independent predecessor
retirement and retained-V1 correspondence work proceeds separately.

Documentation delivery uses the existing frozen-lockfile installation,
`check:workflow`, `build` and `check:accessibility` commands. Their exact delivery
results are recorded with the draft publication evidence.
