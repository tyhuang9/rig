# M3 combined acceptance candidate

Status: locally verified integration candidate; M3 acceptance remains incomplete.
Evidence recorded on 2026-10-15 UTC. This branch collects the already reviewed
controller startup, operator recovery UI and LAN transport changes into one
source revision for hosted verification.

## Hosted acceptance cross-reference — 2026-10-18

The [M3 hosted acceptance addendum](m3-hosted-acceptance-20261018.md) records PR #186's immutable hosted result at head `4200796e3e41450e192368976b64089c84445d90`, including the resumed pending-route case and full 63-case Docker matrix. It supplements this candidate's local and earlier hosted records; M3 remains open for physical second-device qualification and manual screen-reader testing.

## Source and scope

The tested source is `82d489691b95b0c23c1ab45355478d183f9a04aa` on
`feature/hosting-m3-integrated-acceptance`. The draft targets PR #158's branch,
`feature/hosting-m3-recovery-background-isolation`.

| Input | Exact revision | Integration |
| --- | --- | --- |
| PR #158: recovery background isolation | `d574d699afefc8d0f453097252308649089b36d9` | Starting revision |
| PR #156: upgrade recovery UI, including PR #155 operator flow | `34561ad76b552ebda9dcc5b7c75f0e24be034c16` | `ef33c25833e9a676c89850b0c37f8025eabb2ab6` |
| PR #157: expanded LAN transport acceptance | `893ebf6efb359f818222cf5ee16b9296a07b4e87` | `82d489691b95b0c23c1ab45355478d183f9a04aa` |

Both integrations preserve history and completed without conflicts. The only
overlapping production file is `cmd/hostd/main.go`: it retains the private
background-start observer and supplies the reviewed normal discovery and
restricted recovery dependencies. Both recovery returns remain before worker,
auto-deploy and relay startup. The UI, controller and API files match PR #156;
the transport fixture and its two Go consumers match PR #157; the background
helpers, scenario and workflow match PR #158. No new production behavior was
added while combining these inputs.

Authentication, exact-operation pins, fresh status checks, immutable history,
fences, custody, runtime census and terminal readback remain required. There is
no new schema migration. External databases remain application-owned and use
scoped runtime secrets; no managed database or Neon provisioning is introduced.

## Executed local checks

Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'` and `CGO_ENABLED=0`.
Windows and Linux test binaries were compiled separately; Linux binaries ran
under WSL. Each suite required a nonempty exact test inventory, ran once with
a ten-minute deadline and captured test2json output. Every discovered test ran;
there were no failures or skips in the final Go suites.

| Check | Result |
| --- | --- |
| Hostd startup, recovery, background and live-harness controls | 43 parents / 92 subcases passed on each OS; live-tag compilation passed |
| Recovery controller authentication, discovery and OpenAPI contract | 8 parents / 8 subcases passed on each OS |
| Four CLI builds, scoped vet and live-tag hostd vet | All six OS/check combinations passed |
| Dashboard frozen offline install and unit suite | 544 tests in 21 files passed |
| Dashboard Playwright suite | All 9 browser cases passed |
| Canonical embedded-dashboard verification | Production TypeScript/Vite build passed; file sets and SHA-256 content match |
| Canonical generation verification | Migration mirror, generated OpenAPI artifacts and registered routes passed |
| Hosting-notes frozen offline install, tests and production build | 21 tests passed; no failures, cancellations or skips; build passed |

Node 24.15.0 and pnpm 11.22.0 were used. The browser suite uses isolated test
data and controller ports: it covers a real bootstrap/fake-runtime journey and
mocked recovery UI flows. It does not prove recovery against real Docker or a
physical LAN. No existing user controller was restarted. Manual screen-reader
verification was not performed in this integration pass.

The initial embedded check launched the older `powershell.exe` environment,
where `Get-FileHash` was unavailable after the successful dashboard build. The
same canonical script passed under PowerShell 7.6.5. No repository script or
embedded asset was changed to obtain that pass. The failed receipt and raw log
remain retained alongside the successful `embedded-pwsh` receipt.

Reproducible selected Go commands, run for each target OS:

```sh
go test -tags live_docker -count=1 -timeout=10m ./cmd/hostd -run '^Test(AutoDeploy|ControllerRelay|NewAutoDeployRunner|NewControllerRelayRuntime|GeneratedAwareAutoDeployRunner|GatewayRebindLiveRequiredManifest|GatewayRebindLiveHarnessUsesSupportedDaemonAssembly|GatewayRebindBackground|LiveHostdRebindDockerMutation|LiveGatewayRebindBackground|GatewayRebindStartup|GatewayRebindRecoveryPin|GeneratedCompositionRecoversBeforeDeploymentJobAndWorker|LANRecoveryBatchReadReturnsOnlyFreshStaticHeadWithoutWrites|LANSingularRecoveryReadRepeatsExactGatewayAndFullCensus)'
go test -count=1 -timeout=10m ./internal/controller -run '^(TestControllerRecovery.*|TestOpenAPIContractMatchesRegisteredRoutes|TestRecoveryOnlyController.*)$'
go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go vet ./cmd/hostd ./internal/autodeploy ./internal/controllerrelay ./internal/controller
go vet -tags live_docker ./cmd/hostd
```

Repository checks:

```sh
pnpm --dir web install --frozen-lockfile --offline
pnpm --dir web test
pnpm --dir web e2e
pwsh -NoProfile -File scripts/check-embedded.ps1
pwsh -NoProfile -File scripts/check-generation.ps1
pnpm --dir examples/hosting-notes install --frozen-lockfile --offline
pnpm --dir examples/hosting-notes test
pnpm --dir examples/hosting-notes build
```

The documentation publication gate runs frozen offline installation,
`pnpm --dir docs check:workflow`, `pnpm --dir docs build`, and
`pnpm --dir docs check:accessibility`. Exact outcomes are retained in the final
documentation receipt and reported with the draft.

## Evidence identities

Local receipts, exact discovered names, binary hashes, raw logs and before/after
source hashes are in `temp/m3-integrated-acceptance-20261015` in the workspace
wrapper. All checks above retained identical covered source hashes throughout
execution. Code and security reviews found no remaining integration issue.

| Raw log | SHA-256 |
| --- | --- |
| `combined-hostd-native.tests.log` | `10da960681f9797307488100e6cd6759bdc3a441a7d82adf27ace997f253ddcd` |
| `combined-hostd-wsl.tests.log` | `92d0d5d0e65e69814dbdeafa0b0782e23ecc8750be2095c1cb29d71617990b5c` |
| `combined-api-native.tests.log` | `9b7060494bca12779f980d062620521a09c7b88c62d3b7e30512cdb65cb6b2ee` |
| `combined-api-wsl.tests.log` | `6d6b9019748f7423c84ddf3b4f5ed37a2c47e2359c3ba22c5e361db27998f1fb` |
| `web-tests.log` | `1c0573ac1d325ab940cc5b9b40a20741fe0269a22b818009d06c8b58169fd5e5` |
| `web-browser.log` | `ee3a3e5965b6a7841bab8b93400faffacaca63aa5d813636b648a11e488643fe` |
| `embedded-pwsh.log` | `b2a7ee573a4181f56e3b5d90a509d818d14a76c951791b4c74e4946bf97917d8` |
| `generation-pwsh.log` | `07074b98c967cc4cb340e874b485299208be9947a9181037cb9201bb18a17c28` |
| `fixture-test.log` | `417fcd054dbda96198d33f4b67bff698b628b36fdf3d7b81449da0196fa1ae6f` |

## Hosted evidence and outstanding acceptance

PR #157's expanded two-app LAN Docker journey passed at `893ebf6` in
[job 116179486758](https://github.com/tyhuang9/rig/actions/runs/38660229698/job/116179486758),
including independent resource cleanup. The tested checkout tree matched that
head. PR #153's Windows history partition passed 155 parents and 506 subcases
at `60be5bc` in
[job 116171153610](https://github.com/tyhuang9/rig/actions/runs/38657604639/job/116171153610).
These are evidence for those revisions, not a pass for this combined candidate.

The combined revision still requires hosted execution of the supported-controller
matrix: all 33 boundaries and 30 scenarios, including enabled background
isolation, plus the expanded two-app LAN journey, Linux race and Windows gates.
Local live-tag compilation does not establish those results. This workstation
has no running Docker engine and no local race toolchain. No existing check,
deadline, cleanup step or workflow prerequisite was removed.

Separately, M3 still requires physical second-device verification, the complete
serving roster across redeploy/restart/address drift and interface withdrawal
and return, stale-address withdrawal, and application-owned external-database
create/read acceptance. Later milestones remain separate work.

## Risk and rollback

The main integration risk is the interaction between recovery startup, the
operator view and the shared real-container fixture. Local source equality and
focused tests reduce that risk; hosted execution remains required. Existing
UI actions still require fresh state and explicit operator consent.

This candidate has not been deployed. It can be withheld while the separate
drafts remain available. Any future rollback must preserve protected operation
records and immutable history and use a revision compatible with retained
operations; deleting state or reverting authority records is not a rollback
procedure. There is no data migration to reverse in this integration.
