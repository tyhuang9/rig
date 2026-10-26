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
