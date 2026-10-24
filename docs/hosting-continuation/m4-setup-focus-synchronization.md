# M4 setup focus test synchronization

## Purpose and observed failure

Synchronize the submitted-job lookup test with the setup page's initial
heading-focus effect before beginning its simulated lookup interaction.

[PR #211's Windows controller check](https://github.com/tyhuang9/rig/actions/runs/39846179695/job/120059928529)
ran head `e2a00c9063dd6ba8b7f6d6997858b1a6fe2183b5`. Its dashboard suite
passed 543 of 544 tests. The failing lookup test expected focus on its button
while lookup was pending, but the initial review heading had focus. The raw
log SHA-256 is
`a7248ad61bd74a662968faed4ff70561382a929c31e2627023fd84ed6c7a6208`.

The tested merge checkout was `b8539c597d66dde0f08c89a4b63c99926ca997df`,
whose verified tree `b832ad7a8eaf0d9c185674e659ce6b7ddeed3316` matches the PR
head's tree. The source repair starts from that exact head on a separate
branch.

## Change and scope

The test previously found a rendered lookup button and focused it immediately.
The component schedules its initial heading focus in a React effect; finding
the button does not establish that this effect has completed. The test now
waits for the expected initial heading focus, then starts the same lookup
interaction.

This adds a setup assertion and retains every existing assertion for lookup
focus, progress announcement, pending accessibility state, failed-result
announcement and retry availability. It adds no sleep, longer timeout, skipped
test or retry loop. Scope is test synchronization; the hosted failure alone
does not establish a production focus defect.

## Verification

Executed locally on Windows on 2026-10-23 with the repository's pnpm 11.22.0
toolchain:

| Command | Result |
| --- | --- |
| `pnpm --dir web install --frozen-lockfile --offline` | Passed |
| `pnpm --dir web test src/application-setup.test.tsx` before the edit | 21/21 passed |
| `pnpm --dir web build` before the edit | Passed |
| `pnpm --dir web test` after the edit | 544/544 passed across 21 files |
| `pnpm --dir web typecheck` after the edit | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| `git diff --check` | Passed |

The local baseline did not reproduce the intermittent hosted failure. The
retained hosted log is the failure evidence. All tracked dashboard files were
hashed before and after each local run and remained unchanged during it.
The production build is reused from the baseline because the only code-file
edit is the two-line test precondition.

Local evidence is retained under
`temp/m3-lan-external-db-20261015/m4-setup-focus-verification/`:

- Baseline receipt:
  `1469bdbb7dc5d7af847b7b8381ceb1c0d8f2c47b9b0999a9dc6da4f26963ab04`.
- Final dashboard and type-check receipt:
  `c4c27db0907426aca02679d1d02eb1cc72018be58a67206ef9cbb83f8754d67a`.
- Canonical generation receipt, Go 1.26.7 with readonly modules:
  `3588b2b545fb4c7ad95122424f378e6be6d2d8d583e933532ef27c58a72e2a7f`.

Independent accessibility and test-source review found no issue with the
two-line change. It confirms the added navigation precondition and preservation
of the lookup assertions; its SHA-256 is
`e2c535f41e8298f4beffef18265aa0b151d56d9f0ee3cd0f2448419cfa90406d`.
This review has source-level scope. Hosted CI must verify the published
revision. No manual browser result is claimed for this test-only change.
Runtime Docker acceptance and M4 completion remain separate outstanding work.

## Rollback

Revert the test synchronization commit to restore the preceding test. This
change has no runtime data, schema, secret or deployment effects.
