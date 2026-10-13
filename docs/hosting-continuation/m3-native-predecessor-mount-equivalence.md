# M3 native predecessor mount equivalence

Date: 2026-10-13. Branch base: `7ae650028f0a9f392bba5462d294464457187f25`
(PR #151). This focused repair does not establish M3 completion.

## Observed failure and source defect

PR #149's [forward Docker job](https://github.com/tyhuang9/rig/actions/runs/38465889421/job/115532596473)
completed the typed runtime commit and fresh-manager replay, then refused its
initial cleanup attestation. The closing inventory in the first predecessor
retirement failed: two inventory entries, first failure at entry two, both
listener predicates passed once, and the outer runtime attestation never ran.
Independent cleanup failed and retained successor resources.

That run tested merge `e8a659e2e7dee906dc59a4184417342a95a64bfa`, with parents
`cb83fa480c16eec33c8b38e9846ae421139bbce0` and
`a39193ea479168945f035225fe5efb560b79f4b3`; its tree matches the source tree
`f9a01e43b7cf8f85199aeaa957084e40418d33bc`. Log SHA-256:
`0aa3f90f4640da81e83473db0e61d81707bc364cf42600de478d128811fdd059`.

Source inspection found a deterministic equivalence defect in
`nativePredecessorOwnershipInventory`: complete container snapshots read by name
and immutable ID were compared with ordered mount slices, even though the owned
mount validator treats the two exact mounts as an unordered set. The terminal
predecessor path already validates both snapshots and clone-sorts only their
mounts before comparing them. The hosted counters do not prove that mount order
caused that particular failure; another inventory predicate or command failure
remains possible until the repaired hosted journey runs.

PR #151's [forward job](https://github.com/tyhuang9/rig/actions/runs/38469282555/job/115543832635)
subsequently failed during fresh-manager recovery, before runtime attestation:
one retirement entry failed, and no attestation was entered. Cleanup could not
acquire the gateway lock and retained resources. This is a separate observation
from PR #149's closing cleanup inventory failure. It tested merge
`26d182e81e9ab3f4d240d5e3ce4a7b64d6d6d315`, parents `a39193e` / `7ae6500`,
tree `0d9cfb1755c324b9e378694a10977ece95d47e58`; log SHA-256:
`f0f4c759bf5b39ca13b371a6bebaa251cb0b9eeb4b1f7ed8b80ee23b336ef814`.
Its [rollback/replay job](https://github.com/tyhuang9/rig/actions/runs/38469282555/job/115543832388)
passed one parent with no skips in 172.05 seconds. Neither forward log identifies
mount order as the specific failed inventory predicate.

## Scope and invariants

The repair applies the existing terminal comparison pattern to native predecessor inventory.
Independently validate both observations, clone-sort only their exact owned mount
slices, then preserve complete comparison of every other container field and
runtime state. Keep resource ownership, immutable IDs, presence, network members,
volume users, context cancellation and downstream liveness checks intact.

No retries, extra probes, production logging, migrations, history rewriting,
authority changes or secret handling changes are introduced. External databases
remain application-owned and configured through scoped runtime secrets. There
is no managed database or Neon provisioning.

## Verification

Pinned Go 1.26.7 uses `GOTOOLCHAIN=local` and
`GOFLAGS="-mod=readonly -p=1 -buildvcs=false"`. Linux tests are cross-compiled
with `CGO_ENABLED=0`, then actually executed through WSL with `-count=1` and a
10-minute test limit. This is plain execution, not Linux race evidence.

Before the production edit, exact discovery found the three new parents. The
actual Linux run failed only the positive mount-order parent with
`route_reconciliation_required`; both refusal parents and all 14 child cases
passed, with no skips. All 891 Go/SQL/module inputs stayed unchanged. The
137.781-second run is recorded in `native-red-v2-wsl.result.json`; test-log SHA-256:
`c8da62136193a580b47f64fd1f4a0b049b101b298101f9871208b28c6b7dc1af`.

The first development attempt used object equality on file-info snapshots and
failed its fixture invariant. The test now uses the existing identity-aware
protected-history and file comparators, retaining SQL, resource and effect
checks. That failed attempt remains recorded separately; it is not accepted as
the regression proof. The expected child-name manifest was also corrected to
include the two ordered-command cases' `_drift` suffix before the accepted run.

The first repaired Linux run passed all eight selected parents and 41 children.
On Windows, the positive case passed in 80.02 seconds, exposing the cost of
reconstructing the same composed fixture for each refusal child. That Windows
run was deliberately stopped and is not acceptance evidence. Each negative
parent now constructs one fixture; every serial child still uses a fresh
inspection-response wrapper and checks the original SQL/history/files/effects
and retained Docker state immediately afterward. No refusal case is removed.

Final Linux execution after the fixture optimization passed all eight parents
and 41 children, without failures or skips, in 64.406 seconds. This includes
the three native inventory parents, composed cleanup attestation and diagnostic
delegation, and terminal mount/presence controls. Exact discovery and frozen
891-input verification passed; test-log SHA-256:
`b136b8a377fbdb4647ac7896f3dae9303135a7fafccef233c59b463ea423e55b`.

Canonical-LF formatting validation, `git diff --check`,
`go vet ./internal/generatedingress` and the repository's CLI build command
passed on Windows:

```sh
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

Final Windows execution passed all three native inventory parents and 14
children, without failures or skips, in 244.047 seconds. Exact discovery and
frozen-input verification passed; test-log SHA-256:
`a69044f93649fc5e8c229fe0715dc517013f3aab8d6985b09a36e30677e40fbe`.

The documentation gate uses offline, frozen-lockfile installation followed by
`pnpm --dir docs check:workflow`, `pnpm --dir docs build` and
`pnpm --dir docs check:accessibility`. The sandbox pnpm version prerequisite
stalled; its exact child was stopped, and offline installation succeeded outside
the sandbox with tracked inputs unchanged. Gate receipts are retained in
`temp/m3-native-inventory-docs-check-20261013/`.

Independent source/security review, including fixture reuse, found no issues.
CodeRabbit approval is not claimed. Raw command, discovery, source-hash and test-event receipts are in
`temp/m3-native-inventory-mounts-20261013/` in the workspace. The three new parent
names use the existing `TestGatewayCurrentPredecessor` family and belong to the
predecessor CI partition without changing its selector or time limit.

## Remaining acceptance and rollback

Hosted Docker must verify forward commit/replay and cleanup on the repaired
source. Linux race checks remain hosted because local WSL lacks the native
Go/GCC toolchain; local Docker Desktop is stopped and does not establish this
fixture's required shared Linux host/network namespace. Local fake-runner tests
do not establish actual Docker acceptance or physical second-device LAN access.

Revert the focused comparison change through a reviewed commit if necessary.
That restores the previous conservative refusal for differently ordered mounts;
there is no persisted data or schema conversion. Publication authorizes CI only,
not a GitHub merge or deployment.
