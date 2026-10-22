# M4 ordinary executor migration fixture

## Observed hosted failure

PR #207 at `f3fe1c269105e996226bbfc86bc24fd875919980` hit the 40-minute
generated-executor package timeout in
[repository race job 120024003907](https://github.com/tyhuang9/rig/actions/runs/39835893578/job/120024003907).
The active parent,
`TestGeneratedExecutorPersistsOnlyPrivatelyObservedNonzeroMigrationExit`,
had run for 19 seconds. Its stack was executing `database.Open` /
`migrateFS` from `newExecutorMigrationAuthority`.

The log contains 52 parent starts, 51 parent passes and all 55 child passes.
One parent was interrupted by the package timeout. There is no individual
assertion-failure event or data-race warning. The timeout remains failed
acceptance. Raw-log SHA256:
`f7cc7a62932dee28a868bd8b257eb0a00aab58adcf59f0ff4aa4022750836533`.
The tested merge `eac95a8706dd3d04328d3dc3d607e54511328c16` was verified to
have the exact published head tree.

## Focused change

The ordinary `newExecutorMigrationAuthority` helper now opens its empty
temporary root with the existing `databasefixture.Open`. That helper builds
the empty production schema once per process and writes an exclusive private
copy for each test, retaining production connection settings and migration-ledger
validation. All lifecycle, binding, claim, plan, configuration, image-artifact
and runtime setup still occurs separately for each test.

The Git diff changes one import and one opener call. The sealed candidate
fixture, its production provenance, the prepared-schema helper and all test
assertions remain unchanged. This unit changes no product behavior, database
provisioning, external database configuration, runtime safeguard or CI limit.

## Executed verification

Pinned Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, amd64:

| Check | Actual result |
| --- | --- |
| Native Linux before/after comparison | Same three parent tests and two children, each repeated three times; all passed, zero skips. Process wall time 6.797 seconds before and 1.203 seconds after. |
| Complete affected ordinary roster | Five parents and four children passed on Windows and native Linux, zero skips/failures; exact test discovery and RUN/PASS names checked. |
| Format and whitespace | `gofmt -l internal/generatedexecutor/migration_authority_test.go` empty; `git diff --check` passed. |
| Scoped static check | `go vet ./internal/generatedexecutor` passed for Windows and Linux. |
| Canonical generation | `pwsh -NoProfile -File scripts/check-generation.ps1` passed. |

The complete affected parent roster is:

- `TestGeneratedExecutorRejectsCallerOnlyMigrationOutcomes`
- `TestGeneratedExecutorPersistsOnlyPrivatelyObservedNonzeroMigrationExit`
- `TestGeneratedExecutorRetainsCandidateRiskOnSucceededHandoffUncertainty`
- `TestGeneratedExecutorWaitsBeforeMutationWhenMigrationApprovalIsMissing`
- `TestGeneratedExecutorDoesNotRerunInterruptedOrFailedMigration`

The first three form the fixed before/after sample. This is one local comparison
with count three, not a hosted-race benchmark or a whole-package performance
guarantee. Linux binaries were cross-compiled on Windows and executed under WSL
Ubuntu. All five tests were compiled and executed against the final formatted
source on both platforms.

Evidence under `m4-executor-migration-fixture-verification/`:

| Receipt | SHA256 |
| --- | --- |
| Baseline `baseline-20261022T202316Z/receipt.json` | `114ace82ff8ef95a285e9e420c9f51af49119b530e5a18567853ab886f424d8b` |
| Candidate sample `candidate-20261022T202535Z/receipt.json` | `aac4e91f65301abb4df4f55595e66ca231e363e07ada60042d04b847bc29723c` |
| Final five-parent verification `candidate-checks-20261022T202726Z/receipt.json` | `051079d68e9609c927760b1380dd7c0e40bd29a1bcbf7ef294985d2cbb57552f` |
| Final 1,048-file Go/module manifest | `0940bfad07bf77b48a5d74691b5e39df4390a3b85421604fdf4545d3aebec8bf` |

The initial baseline discovery script failed due to CRLF shell line endings.
Its successful successor used LF scripts and the unchanged, hash-verified
compiled binary. The first final format check also caught CRLF; the recorded
transition proves that only line endings changed. Both final test binaries
were rebuilt afterward. Original failures and successful successors remain
separate; the benchmark source differs from final source only by this recorded
newline normalization.

## Remaining acceptance and rollback

Hosted repository and generated-runtime race jobs must still establish package
completion. The untouched sealed fixtures continue to have their original
setup cost. Controller Docker route failures remain separate work. No new UI
or manual application flow is introduced, and this change does not complete M4.

Rollback restores the ordinary fixture's original import and opener. No
production data, runtime resource or application configuration is changed.
