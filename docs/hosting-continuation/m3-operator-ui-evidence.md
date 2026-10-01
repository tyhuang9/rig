# M3 normal-mode LAN operator UI: local evidence

This branch builds on the durable operator read contract at `08222d3` and the
earlier LAN gateway/grant panel (`bc46b84`). It adds restart-visible reservation,
grant, and disable controls to the normal administrator dashboard. The GET
snapshot is the source of actionable state. A tab retains only an exact
unresolved POST request for replay after an ambiguous result. Retained requests
are tied to the authenticated user ID and cleared on logout or new login. The
dashboard withholds a LAN URL during refresh, removal, and stale or failed
attestation.

## Verified locally

- Direct `web/node_modules/.bin/vitest.cmd run`: 18 test files, 441 tests passed.
  Tests cover exact approval and removal fields, reload recovery, replay of the
  original activation/removal IDs, grant/disable observations, stale review
  rejection, successor reservation after release, cross-app separation, URL
  withholding, viewer and cross-user isolation, and dialog focus and error recovery.
- Direct `web/node_modules/.bin/tsc.cmd -b`: passed.
- Direct `web/node_modules/.bin/vite.cmd build`: passed; Vite emitted its
  existing large-chunk advisory for the main dashboard bundle.
- The four files under `web/dist` and `internal/controller/ui` matched by
  relative path and SHA-256 after the embed script ran.
- `go test -count=1 -p 1 ./internal/controller` and the full
  `go test -count=1 -p 1 ./...`: passed after embedding the UI assets.
  `go run ./cmd/openapi-gen -check`: passed.
- `git diff --check`: passed before final review.

The Windows `scripts/check-embedded.ps1` wrapper invokes `pnpm --dir web build`.
In this worktree, `web/node_modules` is an ignored junction to an existing
installation; pnpm tried to purge it and aborted without a TTY. The direct
typecheck, production build, and exact source/embedded SHA-256 comparison above
were run instead. No dependency installation or lockfile edit was needed.

## Remaining acceptance

This is local source and embedded-bundle evidence. The page has not yet been
manually exercised against the live M3 gateway, Docker workloads, a process
restart, or a remote browser on the private LAN. Recovery-only mode does not
use this normal-mode panel; its protected head discovery is a separate branch.
Keyboard and screen-reader behavior has static review and automated focus
tests, but no manual assistive-technology check yet. No branch in this M3 stack
has been published, merged, or deployed.
