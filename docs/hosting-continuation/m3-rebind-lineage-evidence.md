# M3 LAN gateway rebind lineage evidence

Date: 2026-10-03

Base: published PR #97 head `d949d2c59c7dd1d3e2baafce843e45671e182520`

Branch: `feature/hosting-m3-rebind-lineage` (local and unpublished)

## Scope established

Migration 033 is a dormant, mirrored SQLite lineage and fence. It adds:

- one immutable `prepared` rebind claim bound to the current committed v2
  predecessor, an exact planned successor profile identity, and an exact
  application roster;
- separate `rebind_lan_gateway` and `configure_lan_gateway` administrator
  approvals, each with its own action digest and stored approval time;
- one immutable initial claim event and an immutable, ordered roster whose
  fields come from the current allocation, access head, committed grant, and
  generated runtime active head;
- read-only canonical digest helpers and
  `GatewayRebindStartupSnapshot`, which validates the claim from one SQLite
  read snapshot; and
- SQL fences on allocation, access, grant, disable, profile-head, and generated
  runtime active-head writes, including relevant head insert and delete paths.

The embedded and public 033 files have identical SHA-256
`E75A97C94E6B7C8A44C228D4A8336EA98588373B40CFF760C926BFC77BA9495F`.
Migrations 025 through 032 were not edited. Migration 026's committed-profile
pin triggers remain unchanged.

## Safety boundary

This branch is schema and read validation only. It exposes no production claim
writer or state-transition method, no controller handler, and no generated
ingress `Manager` caller. It does not create a successor profile, advance the
profile head, invoke Docker, change a listener, publish a URL, create an
allocation transfer, or release the fence.

Any directly seeded claim is intentionally unreleasable in migration 033. A
later guarded-writer migration must first add and test the pre-effect Manager
authorization/startup gate. That later migration must atomically replace the
dormant claim lock and the exact migration-026 profile pins with a
purpose-bound commit exception, immutable allocation transfers, and a
protected-receipt and physical-proof-bound fence release. None of those
write-side or cross-store guarantees exists in this branch.

The read-only snapshot is not wired into controller startup yet. Therefore
migration 033 alone does not guarantee that a process observes the fence before
an external effect. The SQL triggers protect the existing SQLite writers, but
pre-effect process locking and Docker attestation remain required future work.

The stored predecessor protected-identity digest is validated only for its
canonical shape and for internal claim consistency in this slice. There is no
protected gateway artifact or attestation source to compare it with yet; test
fixtures therefore use an arbitrary canonical digest. A future startup and
cutover gate must compare the claim's digest with the actual protected state
before it can trust the claim or perform an effect.

## Acceptance evidence

Baseline before implementation:

```text
go test -count=1 ./internal/database ./internal/appaccess
ok github.com/hostd/hostd/internal/database
ok github.com/hostd/hostd/internal/appaccess
```

Focused migration behavior after implementation:

```text
go test -count=1 -run TestLANGatewayRebindLineageMigrationAddsDormantUnreleasableFence ./internal/database
ok github.com/hostd/hostd/internal/database 0.522s
```

That test migrates a populated 032 database containing a committed gateway-v2
upgrade. It verifies both explicit administrator approvals, the exact committed
predecessor, rejection of a nonexistent predecessor upgrade, the initial
immutable event, singleton claim retention, absence of transfer/release tables,
and allocation/access/grant/disable/profile/runtime head fences. It also checks
head insert and delete paths.

Focused snapshot and corruption checks:

```text
go test -count=1 -run GatewayRebind ./internal/appaccess
ok github.com/hostd/hostd/internal/appaccess 1.855s
```

Those tests establish:

- a complete claim returns a deterministic validated snapshot;
- a fresh migrated database with no profile or claim returns a nil current
  profile and an empty claim list;
- duplicate roster entries are rejected and roster history is immutable;
- an incomplete claim fails with `ErrInvalidStoredState` while its SQL fence
  remains active;
- missing, stale, or corrupted roster/claim/event data fails closed;
- a canonical-looking access revision request digest corrupted after bypassing
  its immutable-history trigger is rejected by exact digest recomputation;
- a demoted approver invalidates the snapshot;
- two independent database handles produce one consistent read snapshot while
  a concurrent writer corrupts later state; a fresh read then detects the
  corruption; and
- an operational context cancellation is returned as `context.Canceled` rather
  than being mislabeled as stored-state corruption.

Focused package regression after implementation:

```text
go test -count=1 ./internal/database ./internal/appaccess
ok github.com/hostd/hostd/internal/database 6.014s
ok github.com/hostd/hostd/internal/appaccess 13.569s
```

Repository-wide Go verification on the final staged code with normal Windows
build-cache and protected-directory access:

```text
go test -count=1 -timeout=20m ./...
PASS (all packages; slowest package internal/generatedingress 83.624s)
```

An initial sandboxed attempt at the same full command failed because Windows
denied Go build-cache and protected test-directory access; it also reported a
relay socket abort. The normal-access rerun above passed all packages. The
focused database and app-access packages also passed independently after the
review fixes.

Repository-wide static analysis after the focused review fixes:

```text
go vet ./...
PASS (no diagnostics)
```

## Unverified acceptance work

- No live Docker command or generated-ingress Manager path ran in this branch.
- No process restart, protected successor history, cutover, rollback, or
  receipt-bound release ran.
- No protected predecessor artifact was attested against the claim's stored
  identity digest.
- No physical second-device, DHCP, host firewall, interface disappearance, or
  old-address absence check ran.
- No hosted CI result exists for this unpublished branch.

This evidence does not claim that a LAN gateway rebind is usable or that M3 is
complete.
