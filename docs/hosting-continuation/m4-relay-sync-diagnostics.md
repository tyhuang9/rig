# M4 relay initial sync diagnostics

## Observed hosted failure

PR 190 job `118795504853` (run `39471228660`) failed while waiting for the
initial authenticated subscription sync after `20.08s`. The preserved raw log
is under `temp/m3-lan-external-db-20261015/m4-lifecycle-coordinator-verification/ci-followups-20261020/pr190-job-118795504853/raw.log`
and has SHA-256
`38cd21a9b224454900a95f295324bc0fcbf59eea1f1cafb478ef3c13ba6f4f15`.

The failure showed SQLite active subscriptions `1`, sync heads `1`, inflight
sync sets `1`, PostgreSQL relay sessions `1`, controller leases `1`, sync
commands `0`, and current relay subscriptions `0`. The supervisor was still
`authenticating` with an empty outcome. These observations are consistent with
setup-budget exhaustion plus the aggregate cost of the one-second handshake and
write/persistence bounds, but they do not prove a production fault.

## Scoped diagnostic change

The existing outage assertion remains failed when it fails. Its failure line
now adds a fixed, finite context:

- `initial_sync_trigger` is `setup_deadline` for the parent setup context
  branch or `initial_sync_deadline` for the local five-second wait branch.
- `setup_remaining_ms` is sampled once at initial-sync wait entry;
  `setup_elapsed_ms` is measured when the failure is reported, and
  `initial_sync_wait_ms` covers that wait. All three are clamped milliseconds
  with no absolute timestamps.
- `supervisor_epoch` and `supervisor_fence` are capped aggregates.
- A fixed subset of concurrency-safe WSS history counters is retained for
  handshake, authenticated, closed, store failure, read failure, and write
  failure, together with current active, authenticated, and lease gauges.

The existing seven database counts, sanitized supervisor state/outcome/paused
fields, 20-second setup budget, five-second sync wait, 20-second journey,
one-second controller-transport handshake and write/persistence bounds, the
server's default 10-second WSS handshake setting, five-to-25-millisecond retry
backoff, real reopen, and RUN assertions are unchanged. The workflow parser has
an explicit allowlist for every new field and does not retain generic output,
errors, database text, identifiers, URLs, keys, or configuration.

Pure tests cover duration and count clamping, trigger capture for parent versus
local deadline branches, unknown trigger/state sanitization, and exclusion of
unrequested WSS history counters. They do not add sleeps or alter authentication
behavior.

## Verification status

Local verification completed on 2026-10-20 against base
`d36654535519e343e77f8316b4c6d30fd7b16d19` plus the four scoped files.
Pinned Go 1.26.7 used readonly modules, serialized package work and CGO disabled.
The exact selector included the four new diagnostic parents and
`TestRelayOutageControllerOpenRetriesOnlyTransientSQLiteLocks`.

| Check | Result |
| --- | --- |
| Windows `go test -json -count=1 -timeout=3m` with exact selector | Five parents and ten children passed; zero failures/skips |
| Cross-compiled test binary executed natively under WSL, same selector/budget | Same 15 named tests passed; zero failures/skips |
| `go vet ./internal/autodeploy` | Passed |
| Git Bash `scripts/check-generation.sh` | Passed |
| Pinned `gofmt -l` on both changed Go files | Empty output |
| `git diff --check` | Passed |

The local receipt is
`temp/m4-relay-sync-diagnostics-verification-retry4/summary.json`,
SHA-256 `d47ac6a6ca19a0e3700ab220b68793d6d7688bd8b94d2451cae65d57bf638164`.
The final Windows and Linux raw logs have hashes
`a452dfd1055c592fd3fa8d73e9e1a8f29237ace32b868f2e3900f3ec3d788555`
and `bedc47fa927d3148383f63c35996901bf9be7fbd45d73b601846a5f540183e98`.
Source hashes before and after matched. Earlier attempts are retained: the first
exposed a duration-type compile error, which was corrected; later harness
attempts corrected shell selection, CRLF handling and a Git-warning assertion.
No test or production time budget was increased.

Real jq validation caught an error path for malformed non-string `Output`:
regex errors could echo a prefix of the malformed value. Both diagnostic
pipelines now require a string before regex parsing. The final replay ran 69
valid and invalid inputs through each of the two actual workflow filters.
All 138 invocations produced exactly the expected allowlisted output, empty
stderr, and no short or full secret markers. The extended filter emitted 34
typed objects and rejected 35 cases; the location filter emitted only
filename/line objects for 52 cases and rejected 17.

The jq receipt is
`temp/m3-lan-external-db-20261015/m4-relay-sync-diagnostics-verification/jq-typed-output-recheck/receipt.json`,
SHA-256 `fd77569d260cbdee14d2a487d5d3e9254a4006474ce21870d17d97eaff23aba6`.
The tested workflow hash is
`82576ea5d4093b21d9cecd752955ff79f2976c6b1a6c681a0797bbe0251d7c1e`.
The earlier malformed-type evidence remains preserved. Independent security
review found no remaining source blocker.

Hosted PostgreSQL race execution remains pending. These local helper and
parser results do not establish initial synchronization or the outage journey.
This patch diagnoses the existing CI failure; its production cause remains
unproven.

The change is test and CI diagnostics only. Reverting it removes the added
failure context and whitelist without changing production relay behavior.
