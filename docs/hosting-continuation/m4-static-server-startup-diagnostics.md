# M4 static-server diagnostic integration

Status: integrated locally; diagnostic controls passed on Windows and Linux.
The actual Node/symlink test requires hosted verification.

## Existing work and current failure

PR #209's repository job
[120033722474](https://github.com/tyhuang9/rig/actions/runs/39838707486/job/120033722474)
failed `TestManagedStaticServerDoesNotFollowSymlinksOutsideRoot` after 5.04
seconds with loopback connection refused. Node lookup, symlink setup and
`command.Start` had succeeded, but the test did not record child completion or
output. Early child exit, port conflict and delayed readiness remain hypotheses.
The underlying startup cause is not established.

The earlier diagnostic in [draft PR #162](https://github.com/tyhuang9/rig/pull/162)
addresses this same missing evidence. Its `recipe_test.go` preimage is identical
to PR #209's file. This branch preserves that work through local merge
`d517922928df311b9adf1b5268fa0a9c9ed05f04`, whose parents are:

- PR #209: `491219a6abb14e540a725759654ff96ca0044bf1`.
- PR #162: `af5b2aa973c0169789cf138f17ce48a12996ceed`.

The three-file merge exactly retains PR #162's two test source files and
historical M3 evidence document. The latter records historical acceptance;
it is not the successor's verification record.

## Scope and correction

The reused diagnostic captures at most 8 KiB of combined stdout/stderr under
a mutex and emits only closed categories, byte count, truncation, a prefix
digest and an exit code when known. It observes completion before probes and
during the existing 25 ms waits. One goroutine owns `Wait`; cleanup cancels
the child and joins that owner. Raw output and arbitrary error text are not
printed.

Current review found that output capture could leave `Wait` blocked on pipes
retained by a descendant. This successor sets `command.WaitDelay` to 500 ms
before `Start`, following the existing Docker diagnostics process pattern.
The five-second readiness budget, one-second HTTP timeout, generated server
script and symlink fallback/disclosure assertions remain unchanged.

This is test-only instrumentation. It does not change production startup,
runtime authorization, migrations, application databases or configuration.

## Successor verification

On 2026-10-22 the focused matrix discovered and ran exactly
`TestManagedStaticServerReadinessControls` and
`TestManagedStaticServerOutputDiagnosticsControls` (two parents, seven
subcases) on Windows and native Linux: all passed, with zero failures or skips.
Go 1.26.7 built both binaries using `go test -tags live_docker -c` for
`./internal/generatedimage`, with `CGO_ENABLED=0`, `GOTOOLCHAIN=local` and
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`. Each binary used the anchored
union of those two parent names, `-test.count=1 -test.timeout=10m -test.v`.
The Linux binary ran natively under WSL Ubuntu.

Additional checks passed: read-only `gofmt -l` on both test files;
`go vet -tags live_docker ./internal/generatedimage` for both target systems;
`pwsh -NoProfile -File scripts/check-generation.ps1`; and `git diff --check`.
All Go/module source bytes remained unchanged throughout verification.

A separate attempt of `TestManagedStaticServerDoesNotFollowSymlinksOutsideRoot`
skipped on Windows because symlink permission is unavailable, and on Linux
because Node.js is unavailable. Those skips are not acceptance. No privilege,
runtime installation or production environment was changed to bypass them.

The successful local receipt is
`m4-static-server-startup-diagnostics-verification/checks-20261022T231059Z/receipt.json`,
SHA-256 `d77d0e5d150ad51d28d81210d492661d7548cc0bed4aeaa21a637c16fdafc4a3`.

| Evidence | SHA-256 |
| --- | --- |
| Full source manifest | `bb4e0fb2d3956e3c3b4edd7376ae9c3b5d1f412de781981a6b1ee89c84e982b9` |
| Control output, identical on both systems | `e50d6543c034c7a10a5cd139e05bc3e0ec9d824417f868f5c5fd707b0f1bb394` |
| Windows test binary | `bb00d5e425b5b78fcb348f45b1c6474beafe9e65f83964470e06672ebc00e5b2` |
| Linux test binary | `6120c1c5e70a9d87b3ce75ffdd2ad28a7af72f8a4646ea3e5f1a30ec5f50677c` |
| Windows real-process prerequisite skip | `ae2d6d5dc11c9d8e7408fe863aad48629522b926c67a5f5afa904cc896f89fb3` |
| Linux real-process prerequisite skip | `012c18b4e5863025b16c0c8a37a838dfe96d5847b94022af84593a589591eedc` |

The history-preserving integration receipt has SHA-256
`51db440f9d785f5c132c596c7cb74c5b87d9ff0cec1e60a16ba1184c4781b02d`.
The finite source review closing the `WaitDelay` gap has SHA-256
`3f29f8f1ba65af3e31395f8397b0f59cef226a612a17bbd91c5768e839347aef`.
These do not substitute for executable verification.

## Limits and rollback

Local Docker and race execution are unavailable. Hosted CI must exercise the
real Node/symlink test and the diagnostic controls on the published revision.
No physical LAN qualification, deployment or M4 completion is claimed.

Rollback removes the test instrumentation and restores the former readiness
loop. No production data or runtime-resource rollback is required.
