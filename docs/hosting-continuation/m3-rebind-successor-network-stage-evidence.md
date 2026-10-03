# M3 guarded rebind successor-network stage evidence

Date: 2026-10-03

Base: `ad9471a`

Branch: `feature/hosting-m3-rebind-guarded-nonterminal` (local and unpublished)

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
can bind the ID and ownership digest. A host-visible Docker bridge may add only
the planned subnet route and interface prefix and one gateway candidate bound
to the exact derived bridge name; hosts that do not expose the daemon bridge
must retain the exact baseline. Sequence three must preserve the entire
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
| `go test -count=1 -timeout=8m -run '^TestGatewayRebindStageNetwork' ./internal/generatedingress` | PASS, 26.081s on the final source. |
| Combined `TestGatewayRebindStageNetwork`, `TestGatewayRebindProgress`, and `TestGatewayRebindEffectBoundary` suites | PASS, 51.034s on the final source. |
| `go test -count=1 -timeout=20m ./...` | PASS on the final source after review fixes; generated-ingress completed in 174.641s. |
| `go vet ./internal/generatedingress` | PASS on the final source. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| Live Docker acceptance | NOT RUN: the local Docker named pipe `//./pipe/docker_engine` is unavailable. |

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
create.

## Remaining gates

The config and data volumes, stopped successor container, serving transition,
route publication, terminal protected receipt, SQLite terminal transition,
migration-035 fence release, public caller, hosted Linux race and live-Docker
checks, physical second-device LAN proof, PR publication, merge, and deployment
remain open. The create-before-bind crash window requires deliberate recovery
work before this path can be described as automatically crash recoverable.
