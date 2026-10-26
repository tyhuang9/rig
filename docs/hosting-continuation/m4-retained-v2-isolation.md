# Retained Native V2 test-process isolation

## Observed failure

[PR #245's retained-domain race job](https://github.com/tyhuang9/rig/actions/runs/40302965631/job/121505056734)
failed the authenticated retained Native V2 positive case and both invalid
predecessor cases. Each isolated row stopped at 90.02 seconds. The two
negative rows were `crossed_predecessor_identity` and
`unrelated_managed_container`.

The captured checkout was `23b271cc719d3e6fab08b758cb0e59181e4e3a72`,
for published head `71c4640e8a84e3b4288d2d39bac06339a804aa2a`.
Raw log SHA-256:
`1d5a0fc5ddabc07fe7acbafec61561de9593510c0308c6c8e91ab37bd5f876b8`.

Each registry-isolated test process launched a second construction test
process. Both had a 90-second Go test deadline. The captured stack shows
the outer process timing out while waiting for the inner process; its
two-minute command context had not expired. The inner process's progress
and reason for taking that long are not established by this output.

## Bounded correction

Run each test body directly in its existing registry-isolated process.
Keep the isolation guard and every negative table row in a separate
process. Remove only the redundant construction-process wrapper in
`generated_domain_cold_retained_v2_predecessor_test.go`.

Preserve the 90-second deadline, authenticated positive proofs and repeat
attestation, invalid-predecessor refusal, protected-state equality,
command-effect checks and cleanup. Do not reset the registry, change the
production recovery contract, share negative cases, or extend timeouts.
This simplifies the test process hierarchy; it does not by itself prove
that each direct case will finish within the deadline.

## Verification

On 2026-10-26, fresh pinned Windows Go 1.26.7 compilation and exact
discovery of both parents passed. The positive case passed in 73.77
seconds. The crossed-identity and unrelated-container cases passed in
73.71 and 73.57 seconds; their table parent passed in 147.28 seconds.
There were no failures or skips in those executions.

These are three behavioral cases and four unique test names (two
parents and two nested rows). The nine raw RUN/PASS records include
subprocess repetitions and are not nine independent cases. All ran
against one frozen binary, SHA-256
`b8d2dc47b4a2d3f829457a4314bec3e949129f52eebef00ceca9f0d4d1018144`.
The source manifest SHA-256 is
`34eb7938c31bee66f3bf96cc07d9581e9fc79962e1ad4601d7818ac3b8053e30`.

Scoped vet, `scripts/check-generation.ps1`, diff and source-stability
checks passed. Formatting passed for the exact LF representation that
Git records. The earlier direct formatting output contained only the
Windows CRLF-to-LF conversion and was retained; the source was not
rewritten. A failed initial discovery command caused by PowerShell flag
tokenization is also preserved separately from the corrected discovery.
Source review confirmed that only the two wrappers were removed.

Actual Linux race execution remains a hosted CI requirement. This local
run does not establish the earlier grandchild's progress or root cause,
or guarantee future hosted timing. No local Docker or production
acceptance is claimed.

## Rollback

Revert the test change through its PR to restore the previous nested
process arrangement. There is no production data, schema, authority,
runtime-resource or immutable-history migration.

## Follow-up: hosted race deadline

The wrapper removal passed the hosted Windows retained partition on the exact
PR #248 head `e5b5ded1aae368bd2579b5b400f6c6296dd787a6`, synthetic checkout
`6d6cc22545935c98c5995e958f07b7fca206db11`.
[Windows job 121547973705](https://github.com/tyhuang9/rig/actions/runs/40315803530/job/121547973705)
ran the positive case in 74.57 seconds and the two negative cases in 72.61 and
73.05 seconds. The negative parent took 145.67 seconds. Its raw-log SHA-256 is
`a83f08506f7c0766a78758e546651dfcab23351afa75dd64fdb1f8b0164ce681`.

Both hosted Linux race partitions still failed those three cases at the
90-second child deadline:

| Job | Raw-log SHA-256 |
| --- | --- |
| [Repository retained race](https://github.com/tyhuang9/rig/actions/runs/40315803531/job/121547973434) | `7acdd8532bb7d65c1e387305b3227b25875a3807f8587a9526e2df0acbeac3ae` |
| [Generated-runtime retained race](https://github.com/tyhuang9/rig/actions/runs/40315803432/job/121547974537) | `a83ec45cddbb4e29114b190c1b24a66442fe304b5f7f82b83273ff3ff03e26a6` |

All three captured stacks were still executing the authentic A0 fixture's
`RecoverGatewayRebindStartup` call, before the target assertions. They show
runnable canonical history/progress validation. No data-race marker was found;
that does not make either failed race job a pass or establish a deadlock.

The focused follow-up gives only these three fixture cases a 150-second child
deadline and a three-minute outer command context. Other registry-isolated
tests retain their 90-second/two-minute defaults. Each case keeps its own fresh
process, real producer, full proofs, assertions, failure propagation and cleanup.
Invalid bounds must refuse before process creation. No production deadline,
race instrumentation, case selector or workflow envelope changes.

Comparable retained parents completed in 98–133 seconds under race
instrumentation. This supports testing a fixture-specific allowance, but does
not prove that these three cases will finish within 150 seconds. Adding the
three extra 60-second child allowances to the observed failed-step durations
gives conditional projections of 29m26s and 29m39s. These are not worst-case
bounds: runner performance and other test durations can vary. The enclosing
32-minute package, 34-minute step and 42-minute job limits remain unchanged.

The follow-up's fresh pinned Windows Go 1.26.7 binary passed the exact two-parent
selection in 220.250 seconds: isolated child durations were positive 74.02
seconds, crossed identity 73.14 seconds, and unrelated container 72.99 seconds.
Both parents and both named
negative rows passed, with no failure, skip, timeout or stderr output. The
selection used `-test.v -test.timeout=10m` with the exact two-parent selector,
the binary's default count of one, and a 660-second outer runner. Each isolated
child explicitly uses `-test.count=1`. This is local non-race evidence.

The tested source manifest is
`7bdcf441e88cd4ab254233a5cac5eb6ee12b895cfa08c07a2222b0145f20f741`;
the fresh binary is
`18490ac80c76798fb9d23d6e611f9f6c8e262388fad8805f7cc7019d863a5aea`.
The earlier pre-correction freeze was never executed. The tested version also
preserves the original wrapper's `t.Helper()` caller attribution.

Final source manifest
`92dcaa975e694c0fbea8ef0dcc41f107c3e0e0176febc3250e71c765162d0a3d`
differs only by alignment whitespace on the two new timeout constants. Exact
inverse replacements bind it to the tested source; no behavior rerun is claimed
for that formatting-only successor. Final Git LF formatting, diff and source
stability checks passed, recorded in
`f809f8b5aef73a0e65f3cf2f4358b522399c8bdf31d99851da60bd614dcb52aa`.
The earlier formatting output is retained as a failed check. Scoped vet and
canonical generation passed on the behavior-equivalent tested source.

Hosted acceptance requires both Linux race partitions to pass, with all three
cases below their child limits and no race report. If the new deadline is
reached, retain the stack and investigate the measured work before changing
the allowance again. These local and Windows passes do not substitute for
that acceptance.
