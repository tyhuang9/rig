# M4 generated-image construction observation

## Purpose

Expose the existing closed construction diagnostic in the Docker image-authority
test. This change is based on PR #231 at
`4050549efe61c10a88c55afa4c3036858b83323c` and changes one fatal test message.
It does not change production behavior, constructor inputs, authority checks,
build assertions, or cleanup.

## Hosted evidence

PR #228's [Docker hosting job](https://github.com/tyhuang9/rig/actions/runs/40239563025/job/121308249898)
ran head `24d1246ab6b69541c806595471a36080858bd5e7` through synthetic merge
`a62aedfd1954560469b80c08e710085ac10204e3`.
`TestLiveGeneratedImageAuthority/BuildAndReuse` passed in 10.57 seconds.
`AuthorityLostBeforeBuildLoad` failed in 0.67 seconds while constructing the
generated domain, before the compiler and authority-loss hook were created.
The existing message omitted the reason. Raw log SHA-256:
`bed7e048792dba11eaee31474dcdc70ea35e2f463a0b2102ed15e843610c3201`.

The underlying cause is unknown. Each child has a fresh fixture and a unique
daemon identity. Current combined-branch behavior remains unverified in Docker.

## Diagnostic contract

`NewGeneratedDomain` wraps construction failures in an error whose `Error()`
renders only closed stage and reason values. The test now formats that top-level
error with `%v`. It does not unwrap causes or print arbitrary database, Docker,
path, credential, or environment values. The underlying cause is retained by
production error handling.

## Verification

On 2026-10-26, Windows verification completed successfully:

- Tagged compilation: `go test -tags live_docker -c -o <retained-binary> ./internal/generatedimage`.
- Exact outer discovery from that binary: `-test.list '^TestLiveGeneratedImageAuthority$'`.
- Existing privacy control: `go test -count=1 -v -run '^TestGeneratedDomainConstructionDiagnosticClosedAttribution$/^closed_codes_render_without_private_causes$' ./internal/generatedingress`.
  One parent and one child passed, with no failures or skips.
- Scoped vet: `go vet -tags live_docker ./internal/generatedimage`.
- Canonical generation check: `pwsh -NoProfile -File scripts/check-generation.ps1`.
- Formatting and `git diff --check` passed without changing source.

The retained binary SHA-256 is
`9bdff620387f822bc8bc3977da0c6f22445ba5420f97a073b0084eec57ac3229`.
The changed source SHA-256 before and after verification is
`5ce299e87d2fb385ea5566c260b591033774afb1cb0ff0a670379f2f263ce6c0`
(Windows checkout bytes). The two source-declared children are `BuildAndReuse`
and `AuthorityLostBeforeBuildLoad`; listing the outer test does not execute or
discover those dynamic children. Docker and Linux race execution remain hosted
checks. Documentation checks accompany the publication record.

## Acceptance and rollback

Actual Docker execution must identify the finite diagnostic if construction
still fails. This observation is not a production repair or M4 acceptance.
Reverting the one-line test-message change restores the generic failure message;
there is no production configuration or data migration.
