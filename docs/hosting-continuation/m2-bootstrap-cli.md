# M2 first-run bootstrap command evidence

Date: 2026-09-25. Candidate branch: `feature/hosting-bootstrap-cli`, based on `bf782685` (`feature/hosting-m2-recipe-matrix`). This change adds `hostd bootstrap-token` so an operator can reveal the first-run token without finding a file path, including when the controller uses a custom data root. The token remains in a purpose-bound protected file and is printed only when the operator runs the command. The existing `hostctl bootstrap-token --file` path remains available when multiple controllers are awaiting setup or command discovery is unavailable.

## Acceptance evidence

| Check | Result |
| --- | --- |
| `go test ./internal/bootstraplocator ./cmd/hostd ./internal/auth ./internal/secretfile -run 'Test(DefaultStore|Register|ReadToken|RunHostd|ClassifyHostdInvocation|BootstrapToken)' -count=1` | Passed on Windows. Covers custom-root discovery, ambiguity, expiry, malformed/unsafe locators, cleanup, and command dispatch. |
| `go vet ./...` | Passed. |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed. |
| Frontend typecheck | `tsc -b` passed using the locked dependencies already installed in an adjacent worktree. |
| Frontend unit tests | `vitest run` passed: 16 files, 400 tests. |
| Dashboard production build and embed | `tsc -b` and `vite build` passed. Four files were embedded; source and embedded file lists and SHA-256 hashes match. |
| Browser setup test | `playwright test hostd.spec.ts` passed: 1 Chromium test. The test starts an isolated controller, invokes the new zero-argument command, creates the administrator, and checks token and locator removal. The local managed worktree required `GOFLAGS=-buildvcs=false` because Go VCS stamping could not read its Git status; hosted CI still uses its normal checkout. |
| Installed-style local check | Built `hostd.exe`, placed it in the existing Go binary directory on `PATH`, started an isolated fake-runtime controller under a custom temporary data root, and ran `hostd bootstrap-token` from a different directory with no options. The command returned a 43-character token, the controller reported `bootstrapRequired: true`, and neither the token nor passphrase was printed in test output. |
| Security review | No exploitable token disclosure found. Both token and locator are purpose-bound protected files; multiple data roots fail closed. |

The full `go test ./...` suite was attempted. It did not pass in this Windows sandbox: process termination returned `Access is denied` in an existing `cmd/hostd` test, and several unrelated Compose/runtime workspace tests failed with sandbox filesystem access errors. A pre-change focused run showed the same `cmd/hostd` process-termination failure. The frontend package manager could not download packages in this sandbox (`EACCES`); the checks above used the same locked package installation from an adjacent worktree. The canonical `scripts/check-embedded.ps1` command was attempted but its pnpm wrapper stopped before building because it wanted to replace the borrowed module directory without a TTY. The direct production build and file-by-file SHA-256 comparison passed. The package race check could not run because this Windows Go environment has CGO disabled.

The local controller remains fake-runtime only. Administrator creation, GitHub authorization, immutable archive retrieval, and hosted CI for this branch are pending. A hard crash can leave protected files on disk; the command ignores their locator after its 15-minute expiry, but before then another controller can cause a safe ambiguity error. A service running under a different OS account requires that account to run the command, because protected files are account-scoped.
