# M3 stopped-stage snapshot comparison evidence

Date: 2026-10-11. Status: local test-only repair; hosted acceptance pending. This note
does not close M3 or authorize publication, merging, deployment, or activation.
The branch starts from published `c909b4de6290d8ed220ee8e31b53a0bb5c9025b3`.

## Observed failure and limits

[PR #147's stopped-stage Docker job](https://github.com/tyhuang9/rig/actions/runs/38173421692/job/114570650316)
failed `TestLiveGatewayRebindSuccessorStoppedStageContainer` after 58.14 seconds
on test merge `d9e675d4a38ecd6bf01f8e846cd28f44f5f64f26`. The merge contains c909
plus the four-file CI partition change `6e04e7b`; its production and test Go
sources match c909. The failure at
`gateway_rebind_stage_container_live_test.go:265` reported that the physical
snapshot changed during fresh-manager replay. Independent Docker cleanup passed.

The initial observation had passed the exact stopped-container validator.
Fresh-manager replay succeeded, protected history and record bytes remained
equal, and the subsequent inspection returned successfully. Raw
`reflect.DeepEqual` rejected the next observation. The later exact-binding and
owned-container census assertions were not reached. The log does not identify
the changed field, so it does not prove that reordered mounts caused this run
to fail or that the replayed observation was otherwise valid.

Source review establishes a narrower defect: Docker mount slices are retained
in returned order, the ownership validator accepts the same two exact mounts
in either order, and the test compares their raw order. The existing production
mount canonicalizer provides the same unordered representation already used
for other validated observations.

## Repair scope and invariants

The code repair is confined to two test files. A test-only comparator first
validates both original observations against the same intent, stage, and binding.
It copies both complete observations, canonicalizes only copied mount slices,
and compares the complete results. Every other field retains exact comparison.
Caller-owned observations must remain unchanged.

Changed, duplicate, foreign, missing, and extra mounts must still be rejected.
Identity changes and ordering changes in command arguments must also remain
failures. No production validation, Docker action, retry, timeout, ownership
rule, schema, credential handling, runtime state, or immutable history changes.
External databases remain application-owned; this unit introduces no database
provisioning.

## Focused verification

The new parent is
`TestGatewayRebindStageContainerSnapshotComparisonPreservesExactObservation`.
It builds a real stopped-container fixture through the existing binding path
and decodes separate observations for each comparison. Its 20 cases cover
reversed exact mounts, eight malformed mount controls on each side,
identically malformed snapshots, individually valid network identity drift,
and ordered command drift. Every comparison checks that both inputs remain
byte-equivalent to their pre-comparison JSON encodings.

Both this parent and the existing
`TestGatewayRebindStageContainerCreatesBindsAndReplays` belong to the existing
`rebind-resources` partition. The branch does not change CI selection.

Go checks used Go 1.26.7 and `GOFLAGS="-mod=readonly -buildvcs=false -p=1"`.

| Check | Actual result |
| --- | --- |
| RED, original raw comparison, new parent only | Expected child exit 1: independently valid reversed mounts compare unequal; 3.755 seconds package time, no skips. |
| Windows GREEN, new parent only | Exit 0; 1 parent and 20 cases passed, no failures/skips; 2.737 seconds package time. |
| Linux/amd64 test binary cross-compilation | Exit 0 with `CGO_ENABLED=0`. |
| Actual WSL discovery and execution | Discovery and execution exit 0; exactly 2 parents and 20 cases ran and passed once each; no failures/skips. Execution wall time 1.265 seconds. |
| Native vet terminal result | Worker reported a scoped vet run; session 9112 exited 0 with no output. Original argv was not retained, so this is not a separately reproducible scoped vet receipt. |
| Native `hostd` build | `go build -trimpath -o <artifact-directory>/root-hostd.exe ./cmd/hostd` exited 0 in 39.422 seconds; all 798 Go/module input hashes unchanged. |

The RED and Windows GREEN command was:

```sh
go test ./internal/generatedingress -run '^(TestGatewayRebindStageContainerSnapshotComparisonPreservesExactObservation)$' -count=1 -json -timeout=20m
```

The cross-compile command reported by the worker was
`go test -c -o <artifact-directory>/green-generatedingress-linux.test ./internal/generatedingress`,
with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`. The compiled binary was discovered
with `-test.list` for the exact
two parents above, then executed under WSL from `internal/generatedingress`
with `-test.v -test.count=1 -test.timeout=5m -test.run` for the same anchored
selector. Live Docker opt-ins were not enabled.

### Evidence limitations and retained corrections

Local receipts are under `temp/m3-stopped-stage-snapshot-20261011` in the
workspace wrapper. The RED raw JSONL SHA-256 is
`db9daea584641125740035620c56d4b20b3472b5fbd36f8c17bbed28cd1ce614`.
The Linux raw log SHA-256 is
`80e4318edf2d970a8dff6872b64f9afd7f71575befe895604754ed00997552ef`.
The executed Linux binary SHA-256 is
`32e7e08cf5e77c1d51907d6aee6e2e21c519f517e1b9dbb0f8535728efc9dbbe`.

The first Linux result evaluator incorrectly ignored indented subtest PASS
lines and exited 1 even though both child commands exited 0. Its original
result remains unchanged. An independent assessment of the retained raw log
confirmed the exact RUN/PASS names and multiplicities. All 798 Go/module file
hashes remained equal across discovery and execution, and the binary hash
remained unchanged. These pins cover execution, not the earlier compilation.

The Windows GREEN output was delivered untruncated in terminal session 63716,
but was not redirected into a durable raw JSONL file. The saved provenance
record contains the command, terminal identifiers and received result summary.
Neither that run nor the earlier cross-compilation captured a complete
before/after source manifest. The scoped vet result is likewise retained as
terminal output rather than a redirected log, and its exact argv was not
retained. Native `go test` did perform Go's default subset of vet checks.
These limitations are not replaced by repeat runs.

The first raw `gofmt -l` check reported the live test because it uses CRLF line
endings. Both files' bytes, after converting CRLF to LF in memory only, exactly
match Go 1.26.7 `gofmt` output. No source rewrite was needed; original raw hashes
and the failed first formatter receipt are retained. `git diff --check` passed.

The frozen documentation install used pnpm 11.22.0 with
`pnpm --dir docs install --frozen-lockfile --offline`; it exited 0, reused all
129 packages without downloads, and left documentation/manifests unchanged.
The first sandboxed pnpm version lookup failed before any install started;
that failure is retained separately. The retry used the existing user cache.

Final documentation commands for the frozen evidence page are
`pnpm --dir docs check:workflow`, `pnpm --dir docs build`, and
`pnpm --dir docs check:accessibility`. Their results and final aggregate review
are recorded in the publication proposal, after this page is frozen.
Independent source/security review found no blocker in the comparison or its
controls. No new hosted Docker or race pass is claimed by these local results.

## Remaining acceptance and rollback

After a separately approved publication, the actual stopped-stage Docker parent
and independent owned-resource cleanup must pass on the new GitHub test merge.
The separate typed-runtime current-attestation refusal remains open; the held
`cb83fa4` diagnostic update is not part of this branch. Full M3 recovery and
physical LAN acceptance remain incomplete.

Reverting this test-only change restores the original order-sensitive assertion.
There is no production state or data migration to reverse.
