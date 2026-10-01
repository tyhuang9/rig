# M3 recovery-only LAN operator UI: local evidence

This local branch combines the normal operator UI at `99a18e2` with the
proved, administrator-only recovery head read (`5d051b5` and its evidence
commit `affafc1`). The authenticated browser probes no-store system status
before mounting normal routes. Exact `503 gateway_reconciliation_required`
selects an isolated recovery screen. Other status failures remain
indeterminate. The screen reads the exact pinned grant or disable head and
observation, presents the operation for explicit review, and brackets one
retained-ID POST with head, observation, and head reads. It never renders a LAN
URL or automatically repeats an ambiguous POST.

Security review found that an already-open normal tab could retain its old
controls after a recovery-only controller restart. The browser now reacts to
the exact recovery code from any shared API request, rechecks status on focus
and visibility return, and probes a visible normal tab every 30 seconds. A
status probe that remains unsettled for five seconds moves the UI to an
indeterminate screen. Recovery and indeterminate modes clear cached normal
data and reject late cache writes until a fresh normal status response.
Monotonic probe sequencing prevents an older success from overriding newer
recovery evidence. Foreground checks hide normal controls while pending and
restore visible main-content focus after success.

Security follow-up found the original indefinite stale-tab issue addressed
and no new exploitable issue in the patch. A continuously visible tab with no
API activity can still show its previous LAN address until the next status
poll: the scheduled interval is 30 seconds, followed by a five-second probe
deadline if that request does not settle. Browser timer throttling may extend
the interval. This is a detection window for the browser, not proof that a
prior route is safe. M3 acceptance must account for this residual and the
backend's separately attested route behavior.

## Verified locally

- Direct `web/node_modules/.bin/tsc.cmd -b`: passed after the final visible
  focus and status validation changes.
- Direct `web/node_modules/.bin/vitest.cmd run`: 19 test files, 472 tests
  passed after the final changes. Focused recovery/API tests: 2 files, 81 tests
  passed. They include exact and non-exact recovery codes, an already-open
  tab, overlapping probes, foreground focus, stale head and observation,
  administrator denial, ambiguous POST, retained payloads, malformed status,
  post-action focus, and sign out.
- Direct `web/node_modules/.bin/vite.cmd build`: passed. Vite emitted its
  existing warning about a main bundle larger than 500 kB.
- `scripts/embed-web.ps1` ran after verifying its target was the ordinary
  `internal/controller/ui` directory within this worktree. All four files
  in `web/dist` and the embedded directory matched by relative path and
  SHA-256.
- `go test -count=1 -p 1 ./...`: passed on this combined branch before the
  final UI embed. `go test -count=1 -p 1 ./internal/controller ./cmd/hostd`:
  passed after embedding the final bundle.
- `go run ./cmd/openapi-gen -check`: passed.
- Text colors introduced for the recovery screen were checked against their
  backgrounds; measured contrast ratios ranged from 5.78:1 to 14.23:1.

The `scripts/check-embedded.ps1` wrapper invokes `pnpm --dir web build`.
This worktree uses an ignored `web/node_modules` junction to an existing
installation; pnpm attempts to purge it and aborts without a TTY. Direct
typecheck, production build, and exact embedded-file SHA-256 comparison were
used instead. No dependency or lockfile change was made.

## Remaining acceptance

These are local automated and static checks. The recovery-only screen has
not been exercised against a live controller restart, Docker workloads, a
physical second LAN device, or an application-owned external database.
Manual keyboard and screen-reader checks at 375 px, 768 px, and desktop
widths remain unverified. CodeRabbit review was unavailable because its WSL
CLI reported `not_authenticated`; the separate security review identified
the stale-tab issue and confirmed the bounded fix in static follow-up. This
branch has not been published, merged, or deployed. M3 acceptance remains open.
