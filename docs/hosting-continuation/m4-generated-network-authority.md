# Generated application-network authority

Local source and executable acceptance recorded on 2026-10-18. Hosted Docker and race gates remain pending for this branch.

## Scope

This M4 unit adds fresh execution-authority checks to generated application-network creation and observation. It builds on coordinator source `3160faa51a01e0691d093f6e46c07dfd50356006` in draft PR #189. It does not enable the public lifecycle capability or complete M4.

The runtime engine requires a concrete deployment repository configured with the same database and original effects scope. Network requests carry the original worker Claim, deployment, release, configuration revision, plan revision and expected policy finding set. The current generated executor expects an empty set; the repository compares the complete persisted set and refuses any added finding. The repository pins that scope and owns the fixed create/observation checks; callers cannot supply a replacement authority callback.

Both checks require the main deployment to remain `applying`. Generated runtime state must remain in `building`, `migrating`, or `starting_candidate`, with the exact immutable provenance and candidate/predecessor slot relationship. Current active state must still match the recorded predecessor. The accepted recorded plan, migration approval, policy findings and approvals are revalidated alongside the original running job attempt, canonical binding/history and pending-start fence.

The engine pins authority before taking the network mutex. It inspects the physical network and prepares the fixed Docker create request before the final check, then checks cancellation and invokes that request. SQL transactions do not span process execution. Adopting an existing exact network, including reconciliation after a failed create, requires a fresh observation check. Error classification preserves original process, storage and cancellation causes.

Explicit executor network calls and the candidate path's internal network check use the same typed request. Refusal does not authorize cleanup, network replacement or terminal failure writes. If an earlier, resumed, name-only or unknown candidate may exist, the complete replacement reservation is retained before unwinding. Retention is irreversible for the process lifetime, owner checked and shared by aliases; deferred release and component unconsumption cannot free it. A wholly unattempted deployment can still release its unused reservation. Per-component reclamation is separate work.

Deterministic names and slots now live in the small `generatedidentity` package to avoid a runtime/repository/recovery import cycle. Existing runtime wrappers and recovery retain the same name format and validation semantics.

## Verification

The final source freeze contains 29 changed files. Both platform runs used Go 1.26.7, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`. Exact expected-parent discovery was checked against raw test events; every selected test ran once and passed, without skips.

| Executed scope | Windows parents / subtests | Native Linux parents / subtests |
| --- | ---: | ---: |
| Full guard, deployment repository, identity, runtime, executor and recovery packages | 184 / 249 | 184 / 249 |
| Retained lifecycle package | 6 / 3 | 6 / 3 |
| Controller runtime composition fixtures | 14 / 54 | 14 / 54 |
| Changed ingress capacity-diagnostic fixture | Compilation only | 1 / 0 |
| Total executed | 204 / 306 | 205 / 306 |

The core Windows command was `go test -json ./internal/deploymentexecution ./internal/deployments ./internal/generatedidentity ./internal/generatedruntime ./internal/generatedexecutor ./internal/generatedrecovery -count=1`. The lifecycle command was `go test -json ./internal/generatedlifecycle -count=1`. Controller composition selected `^(TestResolveRuntimeDockerExecutableOnlyForGeneratedRuntime|TestRuntimeComposition.*|TestGeneratedComposition.*|TestGeneratedRuntimeImpliesManagedIngressAndServerCapability)$` in `./cmd/hostd`. Native Linux binaries were compiled with `GOOS=linux GOARCH=amd64 go test -c`, executed under WSL with `-test.count=1`, and audited through `go tool test2json`. The supplemental ingress selector was exactly `^TestLowIngressCapacityBecomesReplacementCapacityDiagnostic$`.

Affected-package `go vet`, `go vet -tags=live_integration ./internal/generatedingress`, a Windows hostd build, `pwsh -NoProfile -File scripts/check-generation.ps1`, `gofmt -l` and `git diff --check` passed. Direct ingress live fixtures compiled under `live_integration`; no live Docker test ran locally. Broad repository, Linux race and actual Docker acceptance remain hosted gates. Local documentation dependencies were installed with `pnpm --dir docs install --frozen-lockfile --offline`; the documentation build is recorded in the publication evidence.

Evidence is retained under `temp/m3-lan-external-db-20261015/` in the implementation workspace:

| Artifact | SHA-256 |
| --- | --- |
| `m4-generated-network-authority-verification/checkpoint-11-source-freeze-sha256.json` | `a394a15c811b7a9ee9bef3a8f7005f8073c24ac9679c43226b0157dd41ea7a3f` |
| `m4-generated-network-authority-verification/checkpoint-11-source-freeze.patch` | `98f9abbf74ee06febf7ce5310430155391bca243e6fa71ea469c5db8e027ecef` |
| `m4-generated-network-authority-verification/checkpoint-22-verification-summary-v2.json` | `772d34671a3b9f0b5735762ce4a24d68f7fd07e41f14328272345e5cb9e30716` |
| `m4-lifecycle-coordinator-verification/generated-network-final/linux/receipt.json` | `3d2dc61efc529fb80595d3677a7d8faa3feb878022473785efd72096686fb5e9` |
| `m4-lifecycle-coordinator-verification/generated-network-final/ingress-supplement/receipt.json` | `64aeb4549a36d11400311263f8063054aa64d1efdee876b0e824d0fe378e8d6e` |

All frozen source hashes matched before and after execution. Independent QA checked all 31 Windows evidence artifacts, raw discovery/counts and the source manifest. Source security review passed. The final integration review checks the native receipts and published aggregate as a separate gate.

Historical failed fixture attempts remain retained and are excluded from final acceptance. A lost session handle caused redundant Windows checkpoint 12 to overlap checkpoint 13. Both eventually passed; checkpoint 21 corrects the earlier empty-capture narrative and summary 22 discloses the overlap. Only checkpoint 13 is counted in the table. The original records were preserved.

Required behavior includes stale authority before create and exact-network observation; migration-approval loss; original error/cancellation preservation; scope lifetime and a database writer while the runner is blocked; actual engine/aggregate retention after first-component creation and second-component network refusal; resumed name-only/unknown candidate retention; and competing-capacity rejection after unwind. Existing source/configuration/secret/route/recovery assertions remain required.

## Hosted live-caller correction

The first PR #190 run at `8ff3fb0170ec60fb69400d6eea4af8e6f10c6f91` exposed two stale direct `live_docker` callers of the now-mandatory guarded engine constructor. The original local gate compiled ingress live fixtures but missed these hostd and generated-image fixtures. The [migration job](https://github.com/tyhuang9/rig/actions/runs/39175210898/job/117816344072), [restart job](https://github.com/tyhuang9/rig/actions/runs/39175210909/job/117816344283), [TLS notes job](https://github.com/tyhuang9/rig/actions/runs/39175210909/job/117816344456), and [hosting/blue-green job](https://github.com/tyhuang9/rig/actions/runs/39175210943/job/117816345757) failed during compilation, before their Docker assertions.

The focused correction migrates Next cache and TLS database direct fixtures to a `live_docker`-only helper. It uses the real same-database repository, binding, original Claim, effects scope and persisted provenance. Synthetic fixture history supplies the network authority prerequisite; these direct tests do not establish full durable-controller execution. Real container, Next cache, secret isolation, TLS failure, retained blue-slot and replacement assertions remain in the callers. Production authorization has no optional fallback.

Final repair verification used pinned Go 1.26.7:

- `go test -json -tags=live_docker ./internal/generatedruntime/liveauthority -run '^TestAuthorityBindsConcreteScopedNetworkExecution$' -count=1`: one parent passed, no failures or skips.
- `go test -json -tags=live_docker ./cmd/hostd ./internal/generatedimage ./internal/generatedingress ./internal/generatedruntime/liveauthority -run '^$' -count=1`: all four packages compiled; zero selected tests is intentional compile-only evidence.
- The same four packages cross-compiled with `GOOS=linux GOARCH=amd64 go test -c -tags=live_docker`; binaries were not executed. Both Windows and Linux-target live-tag vet passed.
- Exact repaired-parent discovery passed. Independent security reviewed the final four-file freeze. All 28 retained verification artifacts and source hashes were independently audited.

Repair evidence is in `m4-lifecycle-coordinator-verification/network-live-caller-repair/`: source manifest `checkpoint-12-source-freeze-sha256.json` SHA-256 `bd61a8dcf9003c102f4fdd7f251f5fd423bb255ad38ffc922c591caef7707df6`; summary `checkpoint-14-verification-summary.json` SHA-256 `eee2b48da77c560fe2388224c1932946b0fd483bf3ab091ba6c3b201762a13f0`. Root audit SHA-256 is `6510c0fe7fb767d40ca84c4d9e8a679dae62b21e2184a0295e5a19389cc2ae44`. Earlier failed fixture attempts remain in the summary and are excluded from acceptance. This correction still requires actual hosted Docker execution.

## Limits and rollback

This unit governs the application-network command family only. Image builds, migrations, other candidate commands, route/restoration/drain authority, startup completion repair and full Compose ownership/restart lifecycle remain separate work. It adds no managed database or Neon provisioning; external databases remain application-owned and use scoped runtime secrets.

Retaining the entire aggregate is intentionally conservative and can hold capacity until controller restart. No fine-grained capacity recovery or exactly-once Docker guarantee is claimed. A refused or uncertain network operation preserves runtime evidence rather than deleting resources to reclaim capacity.

No controller activation, deployment or GitHub target-branch merge is part of this draft. Keep lifecycle capability disabled while its remaining gates are unfinished. Rollback must preserve immutable deployment history, pending intent, retained resources and unresolved evidence; do not remove resources or discard safety checks merely to clear a failure.
