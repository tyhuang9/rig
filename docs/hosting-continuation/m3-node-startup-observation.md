# M3 host Node startup observation

## Failure and scope

PR #161 at `2a5812f21a81d085288ffd702903c835995bb04d` failed
[Windows ingress job 116493505572](https://github.com/tyhuang9/rig/actions/runs/38755432983/job/116493505572).
`TestLiveExternalTLSNodeTrust` failed in 15.23 seconds with
`node-startup-deadline`, `no-observed-js-entry`, unknown Node major, and
`exit-nonzero`. Raw log SHA-256:
`0d89258f36884ad9f292814ba75b0748f079b966e52573cd549a8d7423ba5878`.
The actual checkout tree matches the published revision.

The retained stderr was empty after the deadline branch terminated and reaped
the child. This does not establish that JavaScript never ran, that Node crashed
independently, or that TLS validation failed. Windows ingress inherits PATH Node;
the separate core job's Node setup does not configure ingress. Passing PR #163
and failing PR #161 used different Windows runner image revisions, without an
identified executable in these observations. A passing run does not explain the
intermittent failure.

This branch adds test diagnostics only. It preserves the selected executable,
probe JavaScript, TLS CA and peer checks, strict response decoder, all existing
deadlines, one Wait owner and one result consumer. It adds no retries, runtime
pinning, warm-up process, workflows, production behavior or controller activation.
The full recovery matrix continues on PR #161's unchanged revision.

## Additional evidence

- At the existing timeout branch, separately sample whether the startup and
  completed-Wait notifications have been observed. These are non-atomic samples
  when the branch runs, not observations at the timer's exact deadline.
- Record Kill as `returned-nil`, `already-finished`, or `other-error`, and elapsed
  milliseconds from immediately before Kill through the existing Wait return.
  Normal completion and failed start retain `not-attempted`. Nil Kill is not a
  claim about the exact time the child terminated.
- After Wait, report stdout byte count and `empty`, `invalid`, `reachable-204`,
  or `other-valid`, using the existing strict decoder. Report retained stderr
  count and truncation. A valid response never changes a timeout into success.
- After execution, inspect the selected executable as a regular file, reading
  at most 128 MiB plus one overflow byte. Report only a fixed availability class,
  file size and SHA-256. This describes the file at inspection time; it does not
  prove those exact bytes executed. The read limit is not a filesystem I/O time
  bound. Unavailable metadata does not replace the original probe result.
- Report only presence booleans for `NODE_OPTIONS`, `NODE_EXTRA_CA_CERTS`,
  `NODE_PATH` and `OPENSSL_CONF`; an explicitly empty variable remains present.
  Paths, environment values, raw stdout/stderr, certificates and arbitrary
  error messages are excluded.

The existing two-second `WaitDelay` bounds pipe drainage after process exit;
it is not an unconditional kill/reap bound. This diagnostic patch preserves
that behavior and makes no new cleanup guarantee.

## Verification and limits

Before edits, seven native baseline parents and 21 subcases passed. Focused
controls cover pending/already-ready notifications, all Kill categories, sole
Wait consumption, empty/invalid/trailing/valid stdout, unavailable/nonregular/
oversized/readable executable files, redaction, failed start, normal exit and a
valid response followed by a completion timeout.

Initial scoped execution passed 14 Windows parents and 26 subcases, including
actual Node TLS in 0.18 seconds. Linux passed 13 control parents and 26 subcases;
the actual Node test skipped because this WSL environment has no Node. The
verification wrapper deliberately returned failure for that incomplete roster;
Linux TLS acceptance is not claimed. The exact-source final receipts supersede
these initial counts if a review change requires another scoped run.

Windows/Linux CLI builds and live-tag vet plus canonical generation checks
passed. Final scoped execution, docs checks, formatting and independent reviews
are recorded with publication evidence. Local Docker and race checks are
unavailable. Hosted Windows/Linux TLS and race runs remain required; no cause
or repair of the intermittent startup failure, full M3 acceptance, deployment,
or target-branch merge is claimed. Reverting this test-only commit removes the
additional diagnostics without affecting application data or runtime behavior.

Receipts are retained in wrapper
`temp/m3-lan-external-db-20261015/node-startup-observation-verification/`.
