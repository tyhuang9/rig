# M3 guarded rebind successor-network stage evidence

Date: 2026-10-03; route-delta correction verified 2026-10-04

Base: `ad9471a`

Branch: `feature/hosting-m3-rebind-guarded-nonterminal`, published as draft
[PR #121](https://github.com/tyhuang9/rig/pull/121).

## Scope and invariant

This slice adds the first private Docker effect for a prepared initial gateway
rebind. It creates only the generation-scoped successor ingress network. It
does not create volumes or containers, publish a host port, join an application
network, change SQLite, alter a route, or mutate the predecessor. Migration
034 remains prepared and fenced. There is no public controller or API caller.

The writer acquires the deployment-effects lease, then the Manager mutex and
raw gateway OS lock, and retains all three through fresh protected, SQLite,
predecessor Docker, pinned-image, host-network, Docker-network, effect,
readback, and protected-progress observations. It repeats the complete
pre-effect attestation adjacent to `docker network create`. The create command
uses rebind-specific `v2-rebind-1` labels bound to the generation, operation,
protected intent, successor identity, network plan, managed-resource kind, and
role. It also fixes a 15-character bridge name derived from the protected
successor identity. It has no container or port-publish arguments.

On a clean Docker create, the writer captures the canonical network ID returned
by Docker. Exact post-create inspection must prove that same ID, deterministic
name, complete label set, bridge/IPAM configuration, empty membership, and the
complete expected successor census before append-only sequence-three progress
can bind the ID and ownership digest. A host-visible Docker bridge may add the
planned subnet route, or that route plus the exact local gateway and broadcast
`/32` routes observed on hosted Linux Docker. The bridge may add only the
planned interface prefix and one gateway candidate bound to the exact derived
bridge name. The existing exact baseline and subnet-only route shapes remain
supported. Partial Linux route deltas, duplicate routes, baseline removals, and
unrelated routes are rejected. Sequence three must preserve the entire
sequence-two stage payload byte-for-byte except for the new network binding.
Every replay rechecks the bound ID, pinned image ID and approved digest,
canonical network-topology digest, predecessor, claim, protected history, host
topology, and exact Docker census while the locks remain held.

An existing network with only sequence-two progress is unresolved, even when
its name and labels look exact. A Docker error, timeout, cancellation, invalid
create output, returned-ID substitution, or rejected sequence-three write is
never adopted or cleaned up automatically. If a sequence-three write reports
an error after actually installing the exact record, a fresh invocation may
replay only that strictly scanned protected binding. This deliberately leaves
the create-before-bind crash window without automatic recovery; closing it
requires a separately reviewed pre-effect nonce or equivalent durable proof.

## Executable evidence

| Check | Result |
| --- | --- |
| `go test -p=1 -count=1 -timeout=5m -run '^TestGatewayRebindStageNetwork' ./internal/generatedingress` | PASS on the corrected final source, including the post-create no-binding regression (34.596s). |
| Combined `TestGatewayRebindStageNetwork`, `TestGatewayRebindProgress`, and `TestGatewayRebindEffectBoundary` suites | PASS, 51.034s on the original source before the route correction. |
| `go test -p=1 -count=1 -timeout=20m ./...` | PASS on the corrected final source, including the new post-create regression; generated-ingress completed in 189.069s. |
| `go vet ./...` and `go build -buildvcs=false ./...` | PASS on the corrected final source. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir docs build` and `git diff --check` | PASS after the evidence update; no production deployment was attempted. |
| Live Docker acceptance | PASS for the private network stage on hosted Linux: [job 111606812391](https://github.com/tyhuang9/rig/actions/runs/37260595415/job/111606812391) passed the named test and residue at code head `ad8d3e2`; [job 111608189048](https://github.com/tyhuang9/rig/actions/runs/37261058988/job/111608189048) passed both at the evidence-only head `f91140c`. The local Docker named pipe remains unavailable. |

Behavioral tests cover clean creation and exact replay, foreign deterministic
names, exact and unexpected owned volumes, topology drift adjacent to create,
returned-ID substitution, stale pinned image and topology bindings, Docker
create uncertainty, cancellation after create, rejected and ambiguously
installed protected writes, bound-ID substitution, extra Docker networks, and
self-consistent sequence-three attempts that substitute sequence-two image or
topology fields. Exact Linux-style bridge candidate/route/interface additions
pass; unrelated host routes and malformed bridge identities fail. Clean create
errors without resources, invalid returned IDs, success without a resource,
fresh SQLite/predecessor drift, and both lock-release errors are also covered.
Checkpoint removal or self-consistent replacement of sequence-two history and
a successor ID duplicated from the baseline Docker inventory fail before a
protected binding. Success asserts the prepared SQLite snapshot is unchanged.
Failure cases assert no unauthorized protected binding and no second Docker
create. The fourth hosted PR #122 diagnostic measured 22 baseline routes and
exactly three additions after network creation: the planned subnet, gateway
`/32`, and broadcast `/32`. It found no removed, duplicate, or other routes.
This correction adds only that complete route alternative to the preexisting
baseline and subnet-only alternatives. A focused route table rejects partial,
duplicate, extra-inside, extra-outside, and missing-baseline changes. Security
review found no new ownership or unrelated-route bypass. The existing Linux
route snapshot retains destination prefixes but not next hops or route tables;
that preexisting semantic limit remains.

The broader generated-runtime race job is a separate PR-readiness gate. Its
last successful pre-correction run took 1074.037s for
`internal/generatedingress` against an 18-minute (1080s) Go test timeout.
After the added route regressions, [PR #121's race job](https://github.com/tyhuang9/rig/actions/runs/37260578548/job/111606761789)
and [PR #122's race job](https://github.com/tyhuang9/rig/actions/runs/37261058991/job/111608365118)
both timed out at 1080s without a race report. The generated-runtime race
workflow now allows 24 minutes for Go tests, 26 minutes for the step, and 35
minutes for the job; test coverage and assertions are unchanged. This is a
CI time-budget correction, not a passing race result. A hosted rerun is required.

## Remaining gates

The config and data volumes, stopped successor container, serving transition,
route publication, terminal protected receipt, SQLite terminal transition,
migration-035 fence release, public caller, passing hosted Linux race and later
Docker-effect checks, physical second-device LAN proof, merge, and deployment
remain open. The create-before-bind crash window requires deliberate recovery
work before this path can be described as automatically crash recoverable.
