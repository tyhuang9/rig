# M4 generated image and builder authority

## Scope

This unit extends deployment authority checks to generated image selection,
shared BuildKit preparation, build/load, physical image reuse and the final
handoff out of image preparation. It follows the application-network boundary.
External databases remain application-owned and configured through scoped
runtime secrets. No managed database or Neon provisioning is included.

## Authority and command boundaries

The compiler receives the original worker Claim and immutable deployment,
release, configuration, plan and component identities. The concrete deployment
repository pins the original effects scope and copies the expected policy
findings. It rechecks the current binding, attempt, predecessor, plan, approval
and runtime state at fixed operations.

Only a newly selected Building artifact attempt grants permission to prepare
the builder and issue build/load. An existing Building attempt remains
unresolved; retrying does not grant a second build. Builder preparation requires
the same concrete repository and an opaque selection from that invocation.

The seven builder mutations and build/load each prepare their fixed command
before the final authority check. They preserve bounded execution, protected
Docker configuration, owned-resource validation and hard build-storage quotas.
SQL transactions do not span external process execution or resource waits.
Physical reconciliation after a lost acknowledgement still requires fresh
authority before the next operation.

## Image observations and immutable results

Artifact storage lives in `internal/generatedimagestate`; compatibility aliases
preserve existing generated-image types and errors without making repository
authority depend on the Docker compiler. The schema is unchanged.

Runtime composition injects the real generated runtime engine as the compiler's
image validator. Reusing Ready requires a fresh physical inspection followed by
a guarded result transaction. A narrowly classified definite absence or a fully
decoded contradictory image can permit one replacement attempt for a Pending
component. Ambiguous output, transport failure, cancellation, truncation and a
fabricated error code do not permit replacement. An attached ImageReady
component cannot rebuild through this path.

The selected deployment configuration can differ from the configuration captured
when its immutable source release was created. The compiler exports the selected,
guarded configuration without rewriting that release. Runtime-only configuration
changes can reuse the same physically attested image. Public build values remain
part of the build definition and can require a new image.

Successful build observation records artifact Ready and component ImageReady
atomically. A failed result transaction preserves the earlier evidence and
does not fall back to a terminal failure write. Uncertain process completion,
missing or invalid image IDs, inspection failures, and cleanup uncertainty after
a builder/build command attempt retain the Building attempt and original error
causes. A deterministic failure before any command attempt can be recorded
through the guarded failure transaction. Diagnostics expose finite codes rather
than process output or runtime credentials.

The final guarded transaction validates the entire accepted component
aggregate and derives Migrating or StartingCandidate from persisted migration
state. It does not execute or approve the next command family. When a candidate
may already exist, image-boundary refusal retains the whole capacity reservation.

## Local verification — 2026-10-20

The tested code commit is `4a9394042698961ae850e0e5175426be041dda2c`,
tree `7c42bf7699de5e43c9009f3b30f41c303ff51eb5`. The subsequent delivery
documentation commit changes only this document. The draft targets the
application-network authority branch at `d36654535519e343e77f8316b4c6d30fd7b16d19`.

Go 1.26.7 was pinned with `GOTOOLCHAIN=local`,
`GOFLAGS="-mod=readonly -p=1 -buildvcs=false"`, and `CGO_ENABLED=0`.
Windows tests used `go test -json -count=1 -timeout=10m` with exact discovered
selectors. Linux binaries were cross-compiled with `GOOS=linux GOARCH=amd64`,
then executed natively under WSL using `-test.count=1 -test.timeout=5m` and
converted with `go tool test2json`. Each discovered test had to produce exactly
one terminal event; missing tests failed the harness.

| Package | Selected parent tests | Child tests |
| --- | ---: | ---: |
| `cmd/hostd` (composition selection) | 14 | 54 |
| `internal/deploymentexecution` | 16 | 41 |
| `internal/deployments` | 40 | 100 |
| `internal/generatedexecutor` | 48 | 49 |
| `internal/generatedimage` | 79 | 154 |
| `internal/generatedimagestate` | 4 | 0 |
| `internal/generatedruntime` | 57 | 78 |
| `internal/generatedruntimestate` | 5 | 0 |
| **Total on each OS** | **263** | **476** |

Linux passed all 739 named tests with no skips. Windows passed 732 and explicitly
skipped seven existing symlink-privilege cases: three parent tests and four
Next-cache child tests. There were no failures. The three actual Docker image
parents were deliberately excluded from these local runs.

The retained evidence root is
`temp/m3-lan-external-db-20261015/`. Receipt files contain exact commands,
discovered selectors, raw-log hashes, source hashes and before/after checks:

| Receipt under the evidence root | SHA-256 |
| --- | --- |
| `m4-generated-image-authority-verification/final-windows-reviewed-4a93940/receipt.json` | `ed7d74057b1894859b9fa379ff3f1fd4ead83089fbb0bcb11e5c2554c78a5b30` |
| `m4-lifecycle-coordinator-verification/generated-image-final-reviewed/linux/receipt.json` | `a3aafb26c3e80c716e14b7569441f32c7fb34900735744620d7ecc01b95300d7` |
| `m4-generated-image-authority-verification/final-supplemental-reviewed-4a93940/receipt.json` | `bc22ddcdb2030d9eb6f899fb514371fac34cea9e10b20fbb4a6c7e749a1c503e` |
| `m4-generated-image-authority-verification/final-committed-format-reviewed-4a93940/receipt.json` | `652b8fe9905b66ac0f3c2ce8d469295b6cd85c6ada01fde36514c21be43dbd0e` |

Supplemental checks passed six pure TLS/configuration/ingress tests on each OS,
four live-tag package compile checks, `go vet` on the affected packages and live
callers, `go build ./cmd/hostd`, and
`pwsh -NoProfile -File scripts/check-generation.ps1`.
The supplemental harness stopped at its formatter check because eight Windows
checkout files used CRLF. A separate recorded check verified that all 37
committed Go files exactly matched pinned `gofmt` output and those eight checkout
differences consisted only of CRLF. No source edit was needed. The final
aggregate `git diff --check` also passed. The failed formatter receipt remains
unchanged; it is not reported as an entirely successful harness run.

Tests cover transactional rollback, all eight final command checks, canonical
image evidence and bounded replacement, retained capacity, scope lifetime,
frontend public-build/runtime-secret separation and controller composition.
Specific boundary tests exercise a real SQLite commit followed by a lost
acknowledgement, cancellation while waiting for the build slot and manager
mutex, and two application Claims using the same shared builder manager.

During verification, a same-release configuration check incorrectly equated the
release's captured configuration with the selected deployment configuration.
The repair and regression test preserve the immutable capture while exporting
the selected revision. An executor fixture also incorrectly treated the
selection's `created` flag as validity; its Ready-reuse path was corrected.
Earlier failing logs are retained. Final local results above include both repairs.

## Hosted acceptance still required

Hosted Docker acceptance includes the existing compiler, manual recipe, hosting,
Next and recipe-matrix fixtures. `TestLiveGeneratedImageAuthority` adds actual
build and Ready reuse plus authority loss immediately before build/load.
Its workflow requires the parent and both named subcases to pass and retains
only explicit sanitized evidence. Local fake-runner checks do not replace this
acceptance gate.

The new artifact filter passed real-jq checks for valid evidence and nine invalid
inputs. The shared runtime package selector now includes the relocated artifact
storage tests, preserving their hosted race coverage. Local runs were not race
instrumented and did not run Docker. The dependency's successful hosted TLS and
migration jobs establish that dependency only. This image-authority draft still
requires its own hosted Docker and race results.

No controller was refreshed or activated for these checks. Physical second-device
and manual screen-reader acceptance remain separate outstanding work.

## Hosted compiler fixture correction on 2026-10-20

At published head `db2b0c161635a899b2c4f303da1c8fddaf4dc557`, the
[Docker hosting job](https://github.com/tyhuang9/rig/actions/runs/39480849678/job/118828692866)
reported `image_validation_uncertain` after a successful image build in
`TestLiveGeneratedImageCompiler`. The later image-authority gate did not run.
The same job's database roundtrip fixture passed using the runtime image
inspection path, so this evidence does not establish a parser incompatibility.
Raw log SHA-256:
`c28685b0ff46db704cac6a45f2a83b1a7c49f344866704eb43bfff0aa0a8d3d0`.

The compiler fixture supplied its builder-owned Docker configuration directory
to the runtime validator. That validator requires an empty controller-owned
configuration directory before inspection; build tooling may populate its own
directory. The fixture now calls production `PrepareControllerDirectories`
and uses those separate runtime configuration and working directories, as
production composition and the passing database fixture already do. The empty
configuration safeguard, parser and production code are unchanged.

Pinned Go 1.26.7 focused Windows checks passed nine generated-image parents and
43 children, plus two controller-directory parents, with no skips or failures.
The affected live compiler test compiled and explicitly skipped because local
Linux Docker is unavailable. Formatting and diff checks passed. Evidence is in
`temp/m3-lan-external-db-20261015/m4-generated-image-authority-verification/live-config-repair/`.
This repair still requires a fresh hosted compiler and image-authority run;
local checks do not prove the physical failure is resolved.

## Limits and rollback

Keep deployment activation disabled while the remaining M4 boundaries are
unfinished. Migration commands, candidate lifecycle commands, route publication,
draining, terminal completion and startup/retry authority are separate work.
In particular, startup artifact recovery still requires E6 review; this unit
does not establish safe completion of interrupted builds after process restart.

Rollback must preserve immutable artifact history, pending intent, uncertain
runtime resources and retained capacity. Do not restore an unbound compiler
overload, discard unresolved Building evidence or automatically rebuild merely
to clear an error. This draft authorizes neither deployment nor controller
activation.
