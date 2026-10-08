# M3 aggregate verification and CI preparation

Status: partial verification on 2026-10-07. The LAN commit-recovery authority
finding from this checkpoint is repaired and has scoped passing evidence in
[the follow-up record](./m3-lan-commit-recovery-authority-evidence.md). The aggregate
is a draft candidate for separately authorized hosted verification; production
activation and M3 completion remain pending.

Frozen source reviewed/tested here: `2950279a99331b634340f231cc5b1d45469b3818`.
Published base: PR #136 at `9694b4ecf0086174a4002098a2aa203c96907d50`.
The full unpublished scope at this checkpoint is 230 files, 66074 insertions and
254 deletions. Recent integration tests do not establish this whole range.

## Repository verification

Windows, Go 1.26.0, Node v24.15.0. Existing locked frontend packages installed
successfully with `pnpm --dir web install --frozen-lockfile --offline`; no lockfile
changed. Local Go commands used `GOFLAGS=-mod=readonly -buildvcs=false` and the
workspace cache to avoid this machine's Git ownership/VCS-stamping limitation.

Passed, exit 0:

- `pwsh -NoProfile -File scripts/check-generation.ps1`: mirrored migrations,
  generated OpenAPI Go/TypeScript, and controller route/schema test.
- `go vet ./...`.
- `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe`.
- `pnpm --dir web typecheck`.
- `pnpm --dir web test`: 19 files, 502 tests passed; duration 13.61 seconds.
- `pwsh -NoProfile -File scripts/check-embedded.ps1`: production dashboard build
  matches the embedded files byte-for-byte. The existing >500kB bundle warning
  remains; it is not a build failure.

`go list ./...` discovered 50 packages. Excluding exactly
`github.com/hostd/hostd/internal/generatedingress`, the complete remaining 49 were
run with `go test -json -p=2 -count=1 -timeout=30m` followed by those explicit
package paths. All 49 package results passed: **1513 top-level tests and 1769
subtests passed**, zero failed events. There were 23 top-level and 9 subtest skips
for live opt-ins, external PostgreSQL, POSIX permissions and Windows symlink
limitations. Skips are not acceptance of those behaviors. Affected packages
passed: hostd 38.811s, appaccess 115.165s, controller 93.302s, database 18.604s.

The JSON was flushed per line, every package completion checked, exit 0 saved,
and process reaped. Files under `C:/Users/huang/Documents/Projects/Rig/temp/` with
prefix `m3-aggregate-repository-20261007` retain `.command.json`, `.jsonl`, `.exit`
and `.summary.json` (including every skip and package result).
JSON SHA-256: `353CA8C66088942279C137280314ECD5C86E24203A0B275CF2050A1219388C59`.

## CI capacity correction

The earlier race partition change left two monolithic ingress paths: Linux fast
verification's 15-minute package timeout and Windows' 20-minute package timeout.
The current CI-only correction:

- Excludes exactly ingress from the discovered Linux fast and Windows core test
  inventories, rejecting absent/duplicate ingress or an empty remainder.
- Keeps the complete Linux ingress race matrix and its aggregate gate.
- Runs the same 13 complementary groups on Windows, with a validated explicit
  `plain` mode that omits only `-race`. The default mode still requires race
  detection; counts, timeout, package/filter selection and nonempty gate remain.
- Preserves the required name `Windows controller verification` as an always-run
  aggregate requiring both core checks and every Windows ingress group to succeed.
- Preserves all vet/build/frontend/Windows-specific security checks.

The official [Windows 2025 image inventory](https://github.com/actions/runner-images/blob/main/images/windows/Windows2025-Readme.md)
lists Bash and jq. The new Windows job additionally checks their availability and
requires `go env GOOS` to report Windows before running the shared script.

The local audit `temp/m3-aggregate-ci-check-20261007.py` passed 42 actual Bash
runner argument cases (default/race/plain for all groups), four invalid argument
cases, six executions of the real Windows aggregate script, and four executions
of the Linux package-inventory block with complete/missing/duplicate/empty inputs.
The successful inventory retains all 49 non-ingress packages. Injected go-test
exit 97 remains a failure through the pipelines. All three workflow matrices agree.

Go's regex engine evaluated the captured Linux race and Windows plain arguments
against the frozen inventories: all 892 Linux-selected and 890 Windows-discovered
tests occur exactly once; all 12 future-family boundary cases pass. These counts
precede the separately developed authority regression tests. Static source/QA
review accepted the CI change. `git diff --check` passed.

The audit's first run failed on local VCS stamping; the second had a CRLF fixture
that did not model Linux command output. Neither is counted as a pass. The final
run used the established local Go flags and an LF fixture, and passed, exit 0.
Audit SHA-256: `4D40DA05492BDA40E60A3807EA3C6CB3D195AFC5325D4A25E58E5D0EC5FAF2BB`.
Actual jq success-path and hosted matrix durations remain unexecuted locally.

## Review and remaining acceptance

Independent reviews of guarded SQL/appaccess and hostd/controller integration
found no substantiated issue in their scoped source traces. The generated-ingress
review found a current-gateway recovery path that revalidates serving authority
only once before republishing. The native counterpart has an analogous pre-existing
gap. Publication was held for executable regression, repair and review of that
boundary. The follow-up record documents the reproduced failures, repair, scoped
source/security GO, and passing final outcomes, including a corrected test
expectation. Static reviews are not whole-repository execution evidence.

CodeRabbit is missing in WSL (`coderabbit: command not found`). Automatic approval
review rejected downloading/executing its remote installer without explicit
installation approval. No CodeRabbit result is claimed; the separate source
reviews above were required by the user's engineering instructions.

No full ingress suite, Linux race execution, real Docker or second-device LAN
acceptance is established by this record. No production factory, managed database
or Neon provisioning is enabled. CI rollback reverts the shared runner and both
changed workflows together, retaining tests, SQL and protected history. No branch
publication, GitHub merge, deployment or controller restart occurred.
