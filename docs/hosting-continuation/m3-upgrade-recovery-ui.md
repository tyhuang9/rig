# M3 gateway-upgrade recovery UI

Status: local verification and specialist review passed; hosted acceptance pending.
M3 acceptance remains open.
Base: operator-flow draft PR #155 at `7a158617e56b6f471a84a910a4d5476e89730f1a`.

## Scope and authority

Complete the missing administrator screen for an interrupted gateway upgrade.
The existing supported controller already exposes a pinned upgrade GET and POST
in recovery-only mode. This change uses those operations; it introduces no new
runtime effect, approval authority, schema, recovery direction or profile write.

The original approving administrator must resume the existing operation. Another
administrator cannot replace the original actor bound into the saved request.
The browser cannot identify that actor from the public read and must not guess.
The runtime determines whether the retained operation can commit or roll back;
a protected uncertain journal can remain unresolved after a deliberate retry.

The UI requires fresh recovery-only discovery and matching operation, profile,
claim and observation. Review approval is consumed on a changed pre-POST read.
The upgrade action digest binds the approved profile; it does not bind the full
recovery snapshot. Locked server authorization remains the effects boundary.
No new operation ID or automatic mutation/CSRF retry is permitted. Ambiguous
responses retain the exact startup operation and require another status check.
Historical terminal state and current serving observation remain separate;
normal-console isolation is released only by an explicit fresh system-status
check after restart.

If a restart has already cleared recovery discovery, a status check accepts only
version 1, normal mode and an absent recovery property. It offers the explicit
system-status check without claiming a terminal outcome or a serving gateway.
Review and pre-POST discovery still require the exact recovery-only pin. Failed
system checks keep the console isolated. Status results receive keyboard focus
and a live announcement; cancellation restores focus to the review action.

## Baseline and verification plan

A frozen offline frontend install succeeded with an unchanged lockfile. The
unmodified base passed all 519 tests in 20 files (`baseline-tests.result.json`,
raw log SHA-256 `5450f416a15c0bcc5da6b4225f77185459fab5a50fd65d63e325307e6c2f3e1a`).
Evidence is retained in `temp/m3-upgrade-recovery-ui-20261014/` in the enclosing
workspace. The reviewed implementation passed:

| Check | Result | Log SHA-256 |
| --- | --- | --- |
| Frontend unit tests | 544 tests, 21 files | `8e18cae5e0a2482bc1a1aa2e5db944d8d8341ee10640444ef99e6db376c0edb6` |
| TypeScript and production build | Pass | `c133174d00d41e03c5e12da02d7825ce25861a51b3bd477864ca3968bdab3671` |
| New Chromium upgrade journeys | 2 passed | `e2540083d26f1d3546366cd12d6a5c5de3a6b2b6d2bbde343bb7bbf272a9be73` |
| Existing rebind/LAN/source browser cases | 6 passed | `76925ea49084981fd225b1d1853210fa932c782baec7fa3e05a4577c53a556aa` |
| Supported-controller browser case | 1 passed | `96d1c29a2e932cba9641566c0ab62917b12a1f5c6574a6816431b63be54f814f` |
| Canonical embedded asset comparison | Pass | `99e5df72caee362ef4679509006f613e0fe7339a3a55b58e755e244aa8244a53` |
| All four CLI builds, Windows and Linux | Pass | Empty successful build logs; source/asset pins in `final-embedded-cli.result.json` |
| Documentation frozen install, workflow, build, accessibility | All passed | `docs-final.result.json` records each log hash |

Commands are the repository's `pnpm --dir web test`, `pnpm --dir web build`,
selected `pnpm --dir web e2e` files and `scripts/check-embedded.ps1`. Receipts
record exact arguments and source hashes. The six existing browser cases ran
before the final changes confined to the new upgrade component and its tests;
the two new journeys and controller browser ran after those changes. The nine
browser cases all passed. The controller browser uses an isolated fake-runtime
daemon; it does not restart or modify the user's local controller. CLI builds
used `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe`
for Windows and Linux with the verified embedded assets. Final evidence prose
was added after the four documentation checks; its build/accessibility recheck
is recorded separately.

The new journeys exercise fresh-tab discovery, Space/Tab/Enter consent, exact
request identity, a lost response without resubmission, both terminal and cleared
normal discovery, failed and successful system checks, and widths 375/768/1440.
All six screenshots are retained with hashes in `browser-reviewed-screenshots/`.
Mocked APIs do not establish Docker or physical LAN acceptance.

Initial verification found a missing test matcher and a browser expectation that
ignored the existing recovery remount after a failed system check; both test
fixtures were corrected. Review also found and fixed a real missing exit path
after an already-completed restart, plus status/cancel focus and page-title gaps.
Code, security, UX and accessibility reviews cover the resulting bounded changes.
Manual screen-reader execution and hosted CodeRabbit review are not claimed.

## Existing runtime acceptance

PR #154 head `897e7da4db2aa2747b5c289a402bbe7cd3248bc1` passed the hosted
[gateway upgrade/restart/rollback job](https://github.com/tyhuang9/rig/actions/runs/38650368085/job/116147757980).
Each of these tests had exactly one RUN and one PASS, without failures or skips:

- `TestLiveGatewayV2UpgradeCommitAndRestart`
- `TestLiveGatewayV2BindConflictRollsBack`
- `TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests`
- `TestLiveGatewayRebindPublicPassivePredecessor`

The independent complete-cleanup step also passed. Actions checked out merge
`98daa772538a18fee5f4b073e389e353b32315c0`, whose tree
`5ebf0f111ba3f872cfcda09c2e1803c84e1d9646` exactly equals the PR head tree.
Raw log SHA-256: `670990e9b9540763a3ba1beeb5ed27462a3be64efa7e7449079ee17bf16c6d3e`.
The parsed job/run/log receipt is in `temp/m3-ci-review-20261014/`.
This establishes those runtime cases on that parent; it does not establish this
new UI or the separate full supported-controller recovery matrix.

## Rollback and limits

Reverting this UI removes its entry point without changing the retained upgrade
claim or protected journal. Preserve those records and use the supported
controller recovery protocol. Do not delete evidence to exit isolation.
No controller restart, deployment, GitHub target merge, managed database or Neon
provisioning is included. External databases remain application-owned and use
scoped runtime secrets. Full hosted, physical LAN and milestone acceptance remain
required.
