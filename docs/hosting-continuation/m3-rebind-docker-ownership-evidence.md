# M3 LAN rebind predecessor Docker ownership evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-preclaim-attestor` at `4bd7e8a`

Branch: `feature/hosting-m3-rebind-docker-ownership` (local and unpublished)

## Scope and invariant

The read-only preclaim API now combines the zero-claim SQLite/protected-history
proof with two exact Docker observations while holding the Manager and gateway
locks. It accepts only the committed journal's pinned image, stopped historical
v1 identity, final container ID and complete Rig labels, ingress network ID,
config/data volume identities, configured old-address bindings, exact route
configuration, application-network membership, and stable exclusive Rig-owned
inventories. Both Docker observations must be individually valid and deeply
equal. SQLite, protected history, and the v1 source are rechecked after the
second Docker observation. Missing evidence or a changed observation fails
closed.

The old host-publication probe is skipped. A missing or changed predecessor NIC
therefore does not reject ownership by itself, and this proof never claims the
old URL is available. A stopped exact final container is accepted only with no
effective host bind, no live network membership, an exact restart config, and
stable route endpoint identities. A positive restart count is tolerated only
for this stopped rebind ownership proof, covering a failed automatic restart
after the old address vanished. Existing serving and transfer recovery checks
retain their zero-restart requirement.

This API cannot insert a claim, mutate Docker, or authorize successor effects.
The future writer must repeat ownership under its deployment-effects lease and
claim-insert transaction, then attest the inserted claim before any effect.

## Automated evidence

| Check | Result |
| --- | --- |
| Focused `go test ./internal/generatedingress -run 'TestGatewayRebindDocker\|TestInspectGatewayRebindPreclaimWithDocker' -count=1` | PASS after the final recheck change. Covers running and stopped success, owner/config/inventory failures, stable two-read preclaim, inter-read drift, and changes to SQLite/protected/v1 source during the second Docker observation. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages after the final recheck and cancellation change. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS, compilation only; no Docker daemon exercise. |
| `git diff --check` | PASS before the local commit. |

The fixture for the combined test uses an actual digest of its synthetic v1
Docker identity and adapts the exact journal-bound resource IDs. The test also
confirms the two-read path does not change SQLite, protected artifacts, or
Docker command counts. It injects Docker observations rather than running a
real daemon, so the production observer's CLI and old-NIC behavior still need
hosted Docker and LAN acceptance.

Security review found no actionable exploit in this read-only slice. Code
review found a protected-history/v1-source read-window gap; the final recheck
and drift tests closed that finding on reviewer recheck. A final cancellation
check was added after the last v1 source read. The reviewer
also asked for a production-observer test proving that the old host probe is
skipped while in-container probes remain active. The injected-observer test
does not establish that, so it remains an explicit hosted Docker gate. A prior
Codex Security diff-scan invocation rejected the worktree before a scan
existed; no plugin scan result is claimed.

## Remaining gates

Migration 034, effects admission, transaction-scoped claim insertion and
reattest, transfer-aware readers, guarded predecessor stop, live Docker/LAN
cutover, Linux race, and second-device acceptance remain open. No claim writer,
Docker mutation, URL publication, merge, or deployment is added or authorized
by this evidence. This local branch is not published.
