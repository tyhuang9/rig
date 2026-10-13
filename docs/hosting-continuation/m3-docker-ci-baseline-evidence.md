# M3 combined Docker CI baseline

Date: 2026-10-12. Purpose: assemble the four approved repairs into one locally
verified candidate for hosted CI, preserving their commit history.

## Exact integration

Branch: `feature/hosting-m3-docker-ci-baseline`. The tested integration head is
`dd1b45003f37bbbbaa5b4b044755e3cc33fd0bf5`, tree
`219828e488322b06ee74928655c49fe57411b5d0`. A later documentation checkpoint
may add this evidence page without changing the tested source.

The branch starts at `220a6ac3544a848c76ffb36266e4f8d9b76cb04d`, the final
container label repair. Three explicit merge commits preserve the approved
inputs in this order:

| Merge | First parent | Second parent / approved input |
| --- | --- | --- |
| `2444c2c210832cfd5b1c5b2ad8b84d56d030b555` | `220a6ac3544a848c76ffb36266e4f8d9b76cb04d` | `6e04e7b583d886a5485d9c99e8d96fbf5a87c7ea` — repository race partitions |
| `0b0e1f7431eaa53f4a8c6ed304f2aa8bec8ebcef` | `2444c2c210832cfd5b1c5b2ad8b84d56d030b555` | `b94c469c07ad83ee03487e9a512f58b94e5b6526` — stopped-stage snapshot comparison |
| `dd1b45003f37bbbbaa5b4b044755e3cc33fd0bf5` | `0b0e1f7431eaa53f4a8c6ed304f2aa8bec8ebcef` | `7fd33786b672368f00654d68e5150b686eb5dd96` — host Node TLS diagnostics |

All merges completed without conflict. The four inputs are ancestors, the
source branches are unchanged, and each merged component file equals its
approved Git blob. The separate controller recovery checkpoint `1f093ff`
was not integrated. The original aggregate has 13 changed paths relative to
published PR #145 head `cb83fa480c16eec33c8b38e9846ae421139bbce0`.

Machine-readable commands, exact parents, component blobs, source pins, exit
codes and raw-log hashes are retained locally in
`temp/m3-docker-ci-baseline-20261012/`. `integration.json` records the Git
operations. These local receipts are not hosted CI artifacts.

## Behavior and invariants

- Final-container ownership uses the existing exact Rig-label validator.
  Required values and rejection of unexpected `io.rig.*` labels remain intact.
  Foreign image metadata gives no authority; repeated observations still
  require the complete label map to agree.
- The stopped-stage live test validates each observation, then compares copied,
  canonical mount slices. Other container fields remain fully compared.
- TLS diagnostics add bounded, finite failure context to the test probe. They
  retain CA and peer-name verification and the existing timeout budgets.
- Repository race checks run in bounded partitions. Discovery must find real
  runnable parents, the controller selections are complementary, and process,
  pipeline and aggregate failures remain failures. Race detection and the
  existing job/test time limits are retained.

These changes neither activate a runtime factory nor rewrite durable authority.
They add no database provisioning. External application databases remain
application-owned and configured through scoped runtime secrets.

## Executed checks on the combined source

Go 1.26.7 was used with
`GOTOOLCHAIN=local GOFLAGS="-mod=readonly -p=1 -buildvcs=false"`.
Each compiled-test gate required discovery of every expected parent and exact
RUN/PASS events, with no skipped or failed tests in its passing invocation.
All 887 Go, SQL and module input hashes remained unchanged.

| Check | Actual result |
| --- | --- |
| Linux regression gate, cross-compiled with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`, executed in WSL | Five parents and 46 descendants passed. Compile 11.281 s; discovery 1.750 s; test command 17.063 s. |
| Native Windows TLS gate, first sandbox execution | Compilation and discovery passed. `TestLiveExternalTLSNodeTrust` failed in 0.09 s after Node completed normally; the overall test command correctly exited 1. Failure retained. |
| Same frozen Windows test binary outside the sandbox | All five parents and 19 descendants passed, including the real local Node/HTTPS control; discovery 0.015 s, test command 1.000 s. Source, binary and tool identities were unchanged. |
| Selector Bash syntax and contract | The first syntax attempt failed on Windows checkout CRLF (`do\r`) before any contract or Go test ran. Both syntax checks and the contract then passed using an unchanged temporary mirror of the exact committed LF Git blobs. |
| Controller partition audit | 486 non-ingress Go/module inputs and package directories match the retained compiled Linux inventory: 116 runnable controller parents = 26 lifecycle + 90 remainder; 51 packages = 43 remainder + six fixed heavy packages + controller + ingress. This was a coverage proof, not a fresh controller/race execution. |
| Canonical LF formatting and aggregate `git diff --check` | Six affected Go files and the aggregate diff passed. |
| `go vet ./internal/generatedingress` | Passed, 42.546 s. |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed, 42.547 s. |
| `pnpm --dir docs install --frozen-lockfile --offline` | Passed, 3.547 s; tracked inputs unchanged. |

The five Linux parents were:

```text
TestGatewayCurrentTerminalImageMetadataRequiresNamedIDAgreement
TestGatewayFinalOwnershipLabelsPreserveExactRigAuthority
TestGatewayCurrentImageMetadataRequiresStableObservation
TestGatewayRebindCurrentAttestationAcceptsImageMetadataAfterDatabaseCommit
TestGatewayRebindStageContainerSnapshotComparisonPreservesExactObservation
```

The five native Windows parents were:

```text
TestLiveExternalTLSCertificateTrust
TestLiveExternalTLSNodeTrust
TestLiveNodeTLSDiagnosticContext
TestLiveNodeTLSDiagnosticStderrBufferBoundsOutput
TestLiveNodeTLSDiagnosticProcessOutcome
```

Both gates compiled `./internal/generatedingress` with `go test -c` and ran
the listed exact parent alternation with `-test.count=1`, verbose test2json
output and an explicit timeout (Linux 10 minutes; Windows 5 minutes).
The Windows rerun reused the first binary rather than recompiling it.

Selected raw evidence hashes:

| Artifact | SHA-256 |
| --- | --- |
| Linux passing test log | `ee9f8bdef5eb3c0c47e6fc5a626baefea5de30ff8f1fb7b25956da919f0acd25` |
| Windows failed test log | `d70ce6d46ce2eceed11086043591e268a7cdb044b7ff4ac96a7b649eedf46112` |
| Windows passing test log | `8f94ae750ff3d4dfb44cb8fc74b7dcb2de0b3799b50df894dd64f2d056b11f9c` |
| Identical Windows binary used by both executions | `d38a26ff2a6deedb133a575026cad8d79784b3e58f0c1c1e11482ae02f5a7dd4` |
| Passing selector contract receipt | `6df115d3d8ec51946eefc108a1700ad82c3155deeed3ab0a56f216b45262ac4e` |

The documentation verification commands are `pnpm --dir docs check:workflow`,
`pnpm --dir docs build`, and `pnpm --dir docs check:accessibility`. Their
separate receipt freezes this page and is retained in
`temp/m3-docker-ci-baseline-docs-check-20261012/result.json`.

## Limits and next acceptance gate

The two Windows executions distinguish an execution-boundary-dependent local
failure. They do not establish its exact cause, reproduce the hosted
five-second timeout, or prove that the historical hosted TLS failure is fixed.

The local Docker engine was unavailable. No actual Docker forward/rollback/
replay or cleanup gate, full repository race suite, full-controller crash
matrix, or physical second-device LAN test was executed for this combined
candidate. Component PR results apply to their own tested revisions. Hosted
CI must verify this combined source before production wiring proceeds.
M3 remains incomplete.

The aggregate source review covers the exact original 13 paths; documentation
and final integration review are recorded separately with the final candidate.
Review approval is not Docker acceptance or permission to publish, merge, or
deploy. The user's authorization for this checkpoint is local integration and
verification only.

The code can be reverted while preserving history. If any future operation
has already committed database authority, preserve its receipts, SQL history,
selected successor and fence; recovery remains forward-only. Reverting code
does not authorize rolling back database authority or deleting retained
resources.
