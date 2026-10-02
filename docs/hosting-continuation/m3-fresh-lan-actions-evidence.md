# M3 fresh LAN Open and Copy actions: local evidence

This branch continues the isolated M3 recovery UI at `850ccce`. The normal
application panel now requires a fresh controller proof at the moment an
administrator chooses **Open LAN address** or **Copy address**. This is a local
implementation checkpoint. It has not been pushed, published, merged, or
deployed, and it does not close M3 live acceptance.

## Behavior and failure boundaries

- Each action reads uncached system status, uncached application LAN access,
  then system status again. Both status responses must report `daemon:
  running`, a generated runtime, a nonfake runtime, and all four typed
  capability fields. The controller's administrator-only LAN read remains
  the authority for fresh gateway and route attestation.
- The freshly attested HTTP private IPv4 URL must match the address on screen
  and the latest panel snapshot, including raw and normalized URL, application
  ID, expected revision number, access revision, approval, and allocation
  identity. The UI makes no mutation and creates no replacement claim.
- Open precreates an isolated `about:blank` window during the user gesture,
  nulls its opener, and gives the same blank document a safe title, heading,
  and visible progress status while it holds focus. It navigates only after
  the proof and only if the window is still the original blank document, URL,
  and origin. Copy writes only the
  freshly proved URL. Recovery, operator authority loss, changed access,
  unmount, window change, and a ten-second action deadline stop the action.
  The pending window closes on failure; the address and actions stay hidden
  until a successful manual **Check LAN access**. In-page checking and failure
  announcements remain visible even if access changes to local-only state.

## Verified locally

- `web/node_modules/.bin/tsc.cmd -b`: passed.
- `web/node_modules/.bin/vitest.cmd run`: 19 files, 502 tests passed. Focused
  tests cover ordered reads, no early navigation or copy, mismatched app,
  revision, allocation, and URL, unsafe and malformed status bodies, recovery
  signals, popup blocking/change, clipboard rejection, rapid clicks,
  authority change, unmount, deadline, and late read responses.
- `web/node_modules/.bin/playwright.cmd test e2e/lan-fresh-actions.spec.ts`:
  one Chromium test passed. At a 375 px viewport, keyboard activation held
  the new window at `about:blank` until the ordered proof completed. The test
  verified the blank window's title, heading, and visible status; verified
  granted-permission clipboard content, rendered guidance for an
  injected clipboard denial, popup closure after route drift, and popup
  closure when the operator changed the blank window during the proof. The
  API and LAN target were mocked; this does not prove a live gateway route.
- `web/node_modules/.bin/vite.cmd build`: passed with the existing advisory
  that the main bundle exceeds 500 kB. `scripts/embed-web.ps1` ran after its
  recursive target was verified as the ordinary worktree
  `internal/controller/ui` directory. All four built and embedded files
  matched by relative path and SHA-256.
- `go test -count=1 -p 1 ./...`: passed before the final UI embed.
  `go test -count=1 -p 1 ./internal/controller ./cmd/hostd`: passed after
  embedding. `go run ./cmd/openapi-gen -check` and `git diff --check`: passed.

The embedded-asset check wrapper invokes `pnpm`, which tries to purge this
worktree's ignored dependency junction. Direct typecheck/build and the exact
SHA-256 comparison were used instead. No dependency or lockfile changed.

## Remaining acceptance and limitations

A gateway can change after the last attestation response and before browser
navigation or clipboard use; client-side reads cannot make that transition
atomic. A previously displayed address can also be copied manually until
the normal screen detects recovery. These limits remain part of M3 live
acceptance, alongside physical second-device access, firewall withdrawal,
gateway restart and upgrade, and an application-owned external database.

The local Docker Desktop engine still cannot start: its backend log reported
a Secrets Engine socket rename failure, and `docker version` could not reach
the Linux engine. No Docker state or credentials were reset. No live Docker,
physical LAN, or external database fixture was run for this branch. A local
headless Chromium attempt without clipboard permission produced the expected
copy failure; the committed browser test grants permission for the success
path and injects denial to verify its guidance. Permission UX across other
browsers and manual screen-reader behavior remain unverified.
