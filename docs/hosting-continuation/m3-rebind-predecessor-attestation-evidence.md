# M3 LAN gateway rebind predecessor attestation evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-startup-admission` at `2771f05`

Branch: `feature/hosting-m3-rebind-predecessor-attestation` (local and unpublished)

## Scope and invariant

This slice is a read-only comparison of a validated migration-033 prepared
claim with the currently committed protected gateway-v2 predecessor. It does
not authorize a rebind. The SQLite startup snapshot and protected history are
read while holding the gateway process mutex and cross-process OS lock. The
caller of any future effect must first hold the deployment effects lease and
must recheck the claim and physical state at its own commit boundary.

The comparison must require the exact predecessor profile, committed upgrade
operation, protected identity digest, and per-app LAN binding roster, with no
extra protected LAN grants. A second read of both stores detects changes
before the inspection returns. The roster's historical grant protected-state
digest is not compared with the current mutable route-state digest. The
immutable migration-033 roster and all earlier migrations remain unchanged.

No claim writer, migration 034, successor artifact, Docker mutation, URL,
allocation transfer, rollback, or fence release is added here. Existing
startup and runtime fences remain active; a successful inspection only reports
that the old protected predecessor matches the prepared claim at inspection
time.

## Automated evidence

The base `2771f05` passed `go test -count=1 -timeout=20m ./...` before this
change. The combined branch passed:

```text
go test -count=1 -timeout=20m ./...             PASS (all packages; generatedingress 88.806s)
go vet ./...                                      PASS (no diagnostics)
go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedimage ./internal/generatedingress
                                                  PASS (compiled; no live tests run)
```

Focused checks also passed:

```text
go test ./internal/appaccess                     PASS
go vet ./internal/appaccess                      PASS
go test ./internal/generatedingress -run 'GatewayRebindPredecessor' -count=1
                                                  PASS (4.677s before review; 2.983s after review)
go vet ./internal/generatedingress               PASS
git diff --check                                  PASS (LF/CRLF warnings only)
```

The appaccess tests derive every grant projection from the same validated
SQLite read transaction and reject a grant profile digest or proof operation
that disagrees with the predecessor. The generated-ingress tests use a real
prepared SQLite claim and protected committed-v2 fixture. They cover a
matching read-only inspection while the normal fence is active; identity,
profile, network, approver, grant, route-slot, missing/extra binding, pending,
and recovery mismatches; corrupt or replaced protected history; SQLite drift;
mid-inspection cancellation; Manager/OS lock order; and release failure. The
inspection performs no Docker command and does not alter SQLite or protected
artifacts in the positive test.

The Go build cache created under this worktree for isolated package testing
was removed before the combined full suite. It was not staged.

Manual code review and security review found no blocking defect for the
read-only slice. The review identified a package-global test checkpoint, which
was replaced with a private helper parameter, and clarified that grant bindings
follow roster order. The full Go suite, repository-wide vet, and tagged compile
above were rerun after those review fixes. Final integration gave a conditional
GO for this local read-only slice, with rebind activation still blocked.

## Remaining gates

The protected route format does not contain serving deployment/release IDs or
route generation, so those remain validated SQLite facts rather than
cross-store equality fields. The inspection deliberately does not observe
live Docker topology and has no production startup caller. The future caller
must hold the deployment effects lease before acquiring the Manager/gateway
locks, then recheck the claim and physical state before any effect or commit.

The writer's drain, protected successor history, physical cutover and
rollback, hosted Docker gates, Linux race, and physical second-device
acceptance remain open. The local Docker daemon is unavailable, and this
Windows host cannot run the Go race detector with CGO disabled. No merge or
deployment is authorized by this read-only evidence.
