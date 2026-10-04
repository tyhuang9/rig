# M3 LAN gateway rebind runtime fence evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-lineage` at `985552fb9d2975c39f683e360b3275a9c300bb47`

Branch: `feature/hosting-m3-rebind-runtime-fence` (local and unpublished)

## Scope

The production controller now checks the validated migration-033 SQLite
snapshot before gateway startup inspection, before runtime recovery, and before
starting its worker. Generated ingress requires the same fresh check after its
process mutex and cross-process gateway lock. The deployment Router requires a
fresh check before creating a deployment or dispatching Compose or generated
runtime work. A prepared claim or an unreadable/inconsistent snapshot blocks
these paths. Failed job checks return the existing safe, cancelable
`runtime_unavailable` error.

The emergency stop path is a separate, narrow generated-ingress function. It
uses the gateway locks but does not require a working SQLite fence; it may stop
only a journal-bound, exact-owned gateway container. A valid prepared claim at
startup currently invokes this emergency stop, interrupting served routes. A
future claim writer must explicitly accept and plan for that availability
effect before enabling claim creation.

This branch has no claim writer, transfer, release, successor profile, Docker
cutover, controller rebind action, or URL publication. Direct SQL insertion of
a claim while a deployment is already in progress is not made atomic by the
Router's entry check. A future writer must drain in-flight deployment work and
take the gateway OS lock before its SQLite write transaction. The predecessor
protected identity digest remains unattested against protected artifacts, as
documented in the lineage evidence.

## Automated evidence

Baseline at `985552f` before the change:

```text
go test -count=1 -timeout=20m ./...             PASS
```

Focused checks after the change:

```text
go test -count=1 ./internal/appaccess ./internal/runtimeexecutor ./cmd/hostd
PASS (appaccess 16.849s, runtimeexecutor 0.253s, hostd 9.976s)
```

These cover a real valid prepared claim and incomplete lineage through the
production repository check, fresh/closed/canceled database reads, startup
with generated runtime on and off, pre-recovery composition, gateway lock
ordering/release, blocked gateway and LAN effects, exact-owned emergency stop,
and Compose/generated dispatch blocked before deployment creation.

```text
go test -count=1 -timeout=20m ./...             PASS (all packages)
go test -count=1 -timeout=20m ./internal/generatedingress
                                                  PASS (74.942s, isolated rerun)
go vet ./...                                      PASS
go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedimage ./internal/generatedingress
                                                  PASS (compiled; no tests run)
```

An overlapping second ingress test run failed with broad route-drift errors;
one representative failure reproduced on unchanged base `985552f`. Both the
full branch suite and a subsequent isolated ingress rerun passed. The test
processes shared local host resources during the overlapping run, so that run
is not evidence of a branch regression; the isolated rerun is the acceptance
result.

The Windows host has CGO disabled, so the race detector did not run. No Docker
daemon was available for live tagged tests. Tagged compilation establishes
constructor compatibility only; it does not establish Docker or second-device
acceptance.

## Review and remaining gate

An independent security/final integration review found no critical or high
defect in this dormant slice. CodeRabbit CLI was unavailable locally in WSL,
so there is no local CodeRabbit result. This branch remains unpublished and
has no hosted CI result. M3 acceptance remains open pending hosted Docker
checks and physical second-device evidence; no merge or deployment is
authorized by these local results.
