# M3 runtime race partition preparation

Status: local CI preparation verified on 2026-10-07. Hosted Linux race duration
and real Docker acceptance remain unverified. This is not M3 completion.

Base: `9d9b5a1405091b389e4668c95cacc581f3238b1d`, on
`feature/hosting-m3-rebind-cross-store-runtime`.

## Purpose and scope

Keep the accumulated M3 runtime tests within separately bounded race jobs while
preserving every test, its complete subtest tree, and required aggregate checks.
Only the shared race runner and its two workflow matrices change. Application
code, test behavior, runtime factories, secrets and protected history do not.

The old history group grew from 88 tests at published PR #136's
`9694b4ecf0086174a4002098a2aa203c96907d50` to 187 in the local Windows inventory.
The latest retained top-level passing timings for 74 of the 99 added tests total
6739.03 seconds. Those are historical Windows results at different reviewed
source snapshots, not one integrated suite, Linux predictions or new acceptance.
They substantiate a capacity risk for the unchanged 32-minute race timeout;
they do not establish that a particular hosted run timed out.

## Resulting partition

Both workflows use these same 13 ingress groups. The generated-runtime workflow
also retains its existing `runtime-packages` row and its eight packages.

| Group | Windows compiled discovery | Linux source inventory |
| --- | ---: | ---: |
| gateway-v2 | 151 | 151 |
| rebind-history | 147 | 147 |
| rebind-config | 55 | 55 |
| rebind-resources | 106 | 106 |
| rebind-typed-withdrawal | 5 | 5 |
| rebind-typed-completed | 8 | 8 |
| rebind-typed-runtime | 22 | 22 |
| rebind-composition-native | 3 | 3 |
| rebind-composition-second-generation | 1 | 1 |
| rebind-composition-legacy-source | 1 | 1 |
| current-lan | 39 | 39 |
| current-serving | 87 | 87 |
| ingress-remainder | 265 | 267 |
| **Total, each selected exactly once** | **890** | **892** |

History excludes the complete typed and concrete-composition families. Their
fallback groups retain future names not assigned to a specialized subgroup.
Current-serving includes managed and inspection entry points; remainder excludes
that complete family. Both Linux-only OS-lock permission/symlink tests stay in
remainder. Subtests are neither split nor filtered.

## Executable verification

Windows, Go 1.26.0, existing Git Bash and PyYAML 6.0.2:

```text
go test -mod=readonly -buildvcs=false -p=1 -list '^Test' ./internal/generatedingress
python C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-race-check-20261007.py
go run -mod=readonly C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-race-coverage-20261007.go C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-race-args-20261007.json C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-test-discovery-20261007.txt
go run -mod=readonly C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-race-coverage-20261007.go C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-race-args-20261007.json C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-linux-test-inventory-20261007.txt
git diff --check
```

All passed, exit 0. Linux inventory was extracted from the test files selected by
`GOOS=linux GOARCH=amd64 go list -mod=readonly -json ./internal/generatedingress`;
it is source selection evidence, not execution of a Linux test binary.
The 158 selected test files also match with CGO enabled and disabled. An initial
PowerShell comparison had an argument-binding error and supplied no evidence;
a strict Python subprocess/JSON comparison passed on rerun.

The Python audit parses both actual YAML matrices and runs the actual Bash
runner's cases with an exported `go` capture function. This function records each
argument and returns 97 before the unchanged jq step. All 14 cases preserve that
failure through `pipefail`. Unknown suite selection exits 1. Syntax checks use
the runner's canonical LF contents because this checkout supports Windows CRLF.

The Go audit applies Go regular expressions to those captured arguments and the
actual inventories, rejecting gaps, duplicate ownership and empty groups. Twelve
synthetic future-family/boundary names also passed. Two deliberately invalid
inputs (missing current-lan and a duplicate current-lan group) were rejected.
No tests were executed by the capture function.

Static assertions and independent source/QA review confirm `-race`, `-count=1`,
`-timeout=32m`, array quoting, nonempty-pass jq predicate, `fail-fast: false`,
shared runner calls and both always-run aggregate success gates remain intact.
The existing runtime-package list is unchanged. Review corrected the stale
five-jobs comment in the repository workflow.

Artifacts remain outside the checkout under
`C:/Users/huang/Documents/Projects/Rig/temp/`:

| Artifact | SHA-256 |
| --- | --- |
| `m3-runtime-test-discovery-20261007.txt` | `59C15029907DD163E9E26A7C2515B5B42CC95CC218E3613B99967FAFC920BAE6` |
| `m3-runtime-race-args-20261007.json` | `304487A665521160CFA967A83F2252A27BA803C477C573854DD642276A6E68D5` |
| `m3-runtime-race-check-20261007.py` | `22BEE48736A892E89FD9105A376492AD65F65A44EFA256B540B600937C8BC84A` |
| `m3-runtime-race-coverage-20261007.go` | `41D7FEF4E596964DFC22667B93861546988B95DA17DE750257FBE8710BB3B8A0` |

## Limits and next gate

The local audit does not execute race-enabled tests or jq's unchanged success
path. More groups add repeated compilation/job startup cost. Missing historical
timings remain unknown; only a hosted run can establish the new groups' duration.
No full source suite or production build was rerun for this configuration-only
change; the prior integrated source evidence remains separate.

Docker still cannot connect to the local Linux-engine named pipe. PR #136 is
open/draft at the revision above with successful required checks, but those checks
do not cover this unpublished runtime branch. The accumulated runtime delivery
needs an aggregate review and a concrete publication proposal before requesting
approval. Publication, a GitHub merge, deployment and production factory
activation are not authorized by this local CI change.

Rollback reverts the shared runner and both matrices together. It removes no
tests, SQL records, protected history, runtime credentials or application-owned
external database configuration.
