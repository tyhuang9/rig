# Normal retained replay failure diagnostics

PR 174's partition 06 failed at `boundary/r01-rollback-intent` during the
first recovery. Job `116812254622`, run `38845583152`, tested head
`4289f02c2001379703559665b7d525b3fa05f733` through an equal-tree checkout.
The case failed after 252.03 seconds; independent resource cleanup passed.
The raw log SHA-256 is
`2d8742da66e58a08cc280516e96501b974101d6374bfd5fdc7b8e19c37d03643`.

The diagnostic GET passed its terminal-shape checks and the fixture retained
the exact original request. The subsequent POST failed with either a transport
error or a non-200 response. The old generic error did not distinguish these
cases. It does not establish a particular response code, observed ABORT, or
production root cause.

The test harness now reports only numeric HTTP status and a fixed outcome:
gateway unavailable, capability unavailable, conflict, approval mismatch,
forbidden, invalid request, other problem, canceled transport, deadline, or
other transport error. Known public problem codes must match their expected
HTTP status. Unknown or malformed responses remain other problems. Transport
errors take precedence over any response bytes. Response bodies, arbitrary
codes, error text, authority fields and retained requests are never echoed.

Every failure still returns no usable replay status. Successful replay still
requires the exact retained request, independently verified current and terminal
proofs, terminal identity, and a released fence. No production code, admission,
authorization, protected history, manifest, expected disposition, timeout,
workflow, or database behavior changes.

## Local verification

At base `1736c38b34d17d172e459694b0618ed088c34b19`, the existing two replay
and receipt control parents passed on Windows in 0.768 seconds.

After the patch, the same two parents and all 40 nested controls passed on
Windows and actual WSL execution of a Linux amd64 test binary. Controls include
real HTTP refusals, secret-bearing problem fields, malformed bodies, mismatched
status/code pairs, an unexpected 201, a disconnected POST, and wrapped
canceled/deadline errors. They also retain successful COMMIT/ABORT replay,
request retention, and rejection of invalid or missing proofs. Failed requests
are checked for exactly one GET and one POST without retry or fallback.

Commands used Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`:

```text
go test -tags=live_docker ./cmd/hostd -run '^(TestLiveHostdRebindNormalReplayControls|TestLiveHostdRebindTerminalReceiptBindingControls)$' -count=1 -timeout=3m -json
go test -c -tags=live_docker -o <external-evidence-directory>/hostd-normal-replay-linux.test ./cmd/hostd
# The second command used GOOS=linux and GOARCH=amd64; execute its binary in WSL:
hostd-normal-replay-linux.test -test.run='^(TestLiveHostdRebindNormalReplayControls|TestLiveHostdRebindTerminalReceiptBindingControls)$' -test.count=1 -test.v -test.timeout=3m
pwsh -NoProfile -File scripts/check-generation.ps1
git diff --check
```

All commands passed. Generation verification includes migration mirrors,
OpenAPI generation and controller route/schema consistency. Exact command
arguments, timestamps, exit codes and output hashes are retained in
`temp/m3-lan-external-db-20261015/normal-replay-diagnostics-final-verification`.

## Required hosted evidence

No local Docker or Linux race compiler is available. The existing hosted
partition 06 starts with exact R01 and must capture its new outcome before a
behavior correction is selected. These controls establish diagnostic privacy
and failure handling; they do not establish live rollback acceptance or M3
completion. Rollback is a regular revert of this test-only change.
