# Background-isolation sentinel expiry

PR174 job `116812254507` (run `38845583152`) failed
`scenario/recovery-background-isolation` after 208.49 seconds. Its six earlier
scenarios passed, and independent fixture cleanup succeeded. These are scoped
observations, not acceptance of the failed partition. The exact source was
`4289f02c2001379703559665b7d525b3fa05f733`; raw log SHA256:
`6cafc487154c7b16ccf89447f5b196ff59f3bfb0fb360a7bba21b6d74e8f49bf`.

## Reproduction and correction

The fixture created a real expired auto-deploy lease, then formatted its
expected expiry with `time.RFC3339Nano`. That format trims zero fractional
seconds, while the repository persists nine fractional digits. The fixture's
exact string comparison therefore rejected an untouched row.

A new control reproduces this with the actual SQLite database, claim and
readback helpers, before starting any child or Docker resource. Both strings
parsed to the same instant, but the original code failed exact comparison.
The existing background repository control passed in 0.480s, and the new
regression failed in 0.460s. The retained red log SHA256 is
`cdbf4cd172f3c853bc8d5f3a1a8852057c5d13f587be49a69f9a4f6e11d3c0f6`.

The fixture now reads the exact stored expiry after `ClaimDue`, checks that its
parsed instant and token match the returned lease, and retains those bytes.
Subsequent unchanged-state checks still compare exact bytes. Even an alternate
format of the same instant is rejected after the baseline is captured.

The refusal diagnostic now reports only boolean match flags. It no longer
dumps the full sentinel struct, including its test lease token and identifiers.
All ten previous checks remain: enabled state, lease fence/token/expiry,
relay epoch/fence/state, active keys, credentials, and credential issues.
Normal-startup recovery and its advanced-state checks are unchanged.

## Verification

The new live-tag control is included exactly once in both existing hosted
preflight selectors and their required-pass lists. Each now contains 25 unique
parents. No Docker case manifest, runtime, migration, or production code changed.

All 25 preflight parents passed on Windows and actual WSL execution of the
Linux amd64 test binary. The new parent includes the real seed/readback control
and eleven drift subcases, including same-instant/different-text expiry.
The failure diagnostics are checked for a false match flag and absence of
private token, application ID, and injected private text.

Commands used Go1.26.7, `GOTOOLCHAIN=local`, readonly module flags and
`CGO_ENABLED=0`:

```text
go test -tags=live_docker ./cmd/hostd -run <exact-workflow-preflight-pattern> -count=1 -timeout=2m -json
go test -c -tags=live_docker -o <evidence-directory>/hostd-background-controls-linux.test ./cmd/hostd
# Build with GOOS=linux GOARCH=amd64, then execute in WSL:
hostd-background-controls-linux.test -test.run=<exact-workflow-preflight-pattern> -test.count=1 -test.v -test.timeout=2m
pwsh -NoProfile -File scripts/check-generation.ps1
git diff --check
```

All passed. Exact expanded command arguments, parent inventory, timestamps,
exit codes and output hashes are retained in
`temp/m3-lan-external-db-20261015/background-sentinel-expiry-verification`.
Windows command elapsed17.359s, WSL command elapsed16.344s; these include test
process overhead and are not performance guarantees.

## Limits and rollback

No local Docker or Linux race compiler is available. The real hosted isolation
scenario must still prove that the recovery-only child leaves all sentinels
untouched and that a later normal child advances them. This local reproduction
does not establish the absence of every possible live background defect.
M3 remains incomplete. Revert the test-only patch to roll back; no application
state, credentials or infrastructure need changes.
