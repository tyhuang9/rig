# Completed legacy Current accounting after restart

## Purpose

Preserve the configured capacity of an authenticated, completed initial deployment when the controller starts in a fresh process. The deployment's immutable lifecycle binding remains `legacy_uninitialized` until a lifecycle action establishes a different binding.

The normal executor can complete route activation, predecessor retirement, accounting handoff, and route completion while retaining that initial binding. Cold lifecycle validation accepts the canonical revision-zero binding with no authorizer. The subsequent completed-Current debt validator previously required a Running binding and historical lifecycle authorizer, so this legitimate state was refused as `cold_construction/cold_ownership`.

The six-line production correction accepts the canonical legacy binding after the existing completed-Current evidence checks. The Running-binding authorizer checks remain intact. Tests reuse the initial portion of the existing authentic accounting fixture and add focused classifier controls.

## Required invariants

- Validate the authentic activation, retirement, accounting handoff, completion, job, and success-event evidence before restoring configured debt.
- Accept only the canonical legacy binding: the correct application and deployment job, revision zero, and no lifecycle authorizer.
- Preserve the Running-binding historical-authorizer checks and all partial, crossed, duplicate, and noncanonical evidence refusals.
- Preserve the existing behavior for old state without completion evidence. Do not fabricate receipts, rewrite bindings, repeat deployment effects, or acquire a second capacity share.
- Keep configured charges for existing application containers, including unhealthy containers. External databases remain application-owned.

## Local acceptance evidence — 2026-10-26

The unchanged new regression first failed on the base revision at `cold_construction/cold_ownership` in 0.86 seconds. Its authentic initial deployment produced the full 042 activation, 043 retirement aggregate, both 044 accounting records, and 045 completion receipt. Binding, protected-state, effect, and capacity assertions passed before that expected failure.

After the correction, a freshly compiled Go 1.26.7 test binary discovered exactly four requested parents and passed 16 unique cases: four parents and twelve children. Nested process-isolation output repeats three parents and is counted once per case.

| Parent | Result |
| --- | --- |
| `TestGeneratedDomainRestartAccountingInitialLegacyCurrentRestoresConfiguredDebt` | PASS, 1.05s |
| `TestGeneratedDomainRestartAccountingCompletedCurrentSupportsDistinctReplacement` | PASS, 1.98s |
| `TestGeneratedDomainRestartAccountingInterruptedDrainingRestoresConfiguredDebt` | PASS, 1.46s |
| `TestGeneratedDomainColdCurrentDebtEvidenceRequiresTotalAbsenceOrExactChain` | PASS, including twelve refusal controls |

The authentic initial regression checks reconstructed coordinator debt and the exact retained token, immutable history and protected state, no repeated candidate effects, and no new capacity probe. The classifier also accepts a completed legacy chain with a predecessor; that positive row is not a second physical restart journey. Invalid revision or authorizer, partial, duplicate, and crossed evidence remain refused. Existing total-absence behavior remains unchanged.

Actual local checks used `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`: fresh `go test -c` for `./internal/generatedingress`, exact-parent discovery and execution with `-test.count=1 -test.timeout=10m`, `gofmt -d` on the three source files, `go vet ./internal/generatedingress`, `scripts/check-generation.ps1`, and `git diff --check`. All passed, with source hashes unchanged through verification. Source/security review also passed.

The qualified RED receipt SHA-256 is `d183239f222127696e1f3ba72cf1c7a4d920b4ecccd96ced94d493341d2329b1`; the GREEN packet index is `181fbc2080b2e9243ce6a9d5ee95f2163b7ef04090299e508bd4b712d15bd615`; the GREEN raw behavior log is `b62ca3526d2c2e60c7d754faf555bb8a11589d952ae0b2fee69b915634c4726a`.

## Remaining verification

The hosted TLS controller journey and process-kill matrix on PR #250 reached `cold_ownership` at their first cold start after a successful initial deployment. These logs identify the failing boundary, not the exact rejected predicate. This change requires its own hosted Docker and Linux race verification; earlier CI failures or successful unrelated jobs are not acceptance evidence. No local Docker execution or live-controller refresh was performed. Fresh-worker completion after interrupted Draining remains a separate unfinished unit.

## Scope and rollback

This work concerns cold reconstruction and its tests. It does not add managed databases, change runtime secrets, authorize a new deployment, or relax Docker ownership checks. Reverting the change restores the prior startup refusal for the affected legacy state. No migration or history rewrite is planned.
