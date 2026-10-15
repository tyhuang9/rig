# M3 recovery background startup isolation

Status: locally verified candidate, 2026-10-15. Hosted Docker acceptance is pending.

## Purpose and scope

The supported controller crash matrix previously disabled GitHub and relay in
its child processes. The ordinary queued-job sentinel proved worker isolation,
but did not exercise enabled auto-deploy or relay startup. This change adds one
independently observable scenario, `recovery-background-isolation`, while
retaining the existing 33 crash boundaries and 29 scenarios.

The production startup order already returns into the restricted recovery
server before launching either background service. A private, nil-by-default
observer now reports a finite component identifier immediately before each
enabled service starts. It has no return value, runtime factory, authority,
credentials or external interface. Existing startup and admission decisions
remain unchanged.

## Required behavior

The disposable fixture seeds an expired auto-deploy lease through the real
repository and a disconnected relay session with a known epoch before the
proposal captures its application census. It contains no active or pending
relay key or protected relay credential. Child-owned loopback HTTPS and a
rejecting GitHub transport keep the control independent of external services.

The recovery child must retain both sentinels and record zero component starts
through readiness, authenticated requests and successful reconciliation in that
same process. A separately reaped, fresh normal child must record exactly one
start of each component and perform both real database transitions: clearing
the expired lease and advancing the relay session epoch and fence into the
exact safe `needs_attention` / `key_revoked` state. The positive control
is required; a marker alone cannot establish that either runtime ran.

Each child has separate bounded ledgers. GitHub transport attempts and accepted
relay TCP connections are observed separately; a TLS handshake failure cannot
hide a relay connection from the latter check. Ledgers are checked again after
each child is killed and reaped. Test recording errors fail the run.

## Verification record

The unchanged base is `988a13891a2ebf8548efe86188710f72855d8f20` from draft
PR #154. Its focused Windows baseline passed 25 parent tests and nine subcases,
with no failure or skip. The first invocation used the repository root instead
of the Go package directory and failed two source-file checks; that runner
error and its raw output were retained, then the same binary passed from the
correct directory. Baseline output SHA-256:
`6c53290bc55185f159201f3808a46f61c920607623f8b08b5f2fffe681740c0c`.

The final Windows and Linux binaries each discovered and passed 32 parent tests
and nine subcases, with no failure or skip. Both were compiled with
`-tags live_docker`, run once, and checked against unchanged source hashes.
The Linux binary was cross-compiled on Windows and executed in WSL. These are
plain tests; hosted race detection remains required.

| Check | Actual result | Output SHA-256 |
| --- | --- | --- |
| Windows selected hostd controls | 32 parents / 9 subcases passed | `a307226ba0b8e149c0c82cb2077506953d27eae84ff4c732eff5380d36257cf1` |
| Linux selected hostd controls | 32 parents / 9 subcases passed | `83088613fa43738a7a345704fe50fef03cd39771a19d1d655ff3d530ff6ac2ed` |
| Actual workflow jq filters | 65 controls passed: complete events accepted; missing, skipped, failed and duplicate required events rejected | Workflow source: `168691dba858038fc97f6e75aa54061c2b75cde05f71148fb387c635d2a2e951` |

The first candidate run on both platforms exposed an existing source-anchor
check that depended on `runner: runner` spacing. Adding the observer caused
gofmt to align that field. The control now normalizes whitespace for required
anchors, while retaining every required anchor and checking forbidden runtime
substitutes against the original source. Both final suites passed after that
repair; first-run logs are retained.

The fast SQL control runs the real auto-deploy coordinator with a fixed clock,
a five-second deadline, and assertions that exactly one lease was recovered.
Its relay half verifies the real repository epoch transition; the actual live
child remains responsible for the supervisor's stable keyless final state.
The TLS control proves that a failed certificate handshake is still recorded
as an accepted relay TCP connection, without disabling certificate checks.

Reproducible selected Go test command (run on each target platform):

```sh
go test -tags live_docker -count=1 -timeout=10m ./cmd/hostd -run '^Test(AutoDeploy|ControllerRelay|NewAutoDeployRunner|NewControllerRelayRuntime|GeneratedAwareAutoDeployRunner|GatewayRebindLiveRequiredManifest|GatewayRebindLiveHarnessUsesSupportedDaemonAssembly|GatewayRebindBackground|LiveHostdRebindDockerMutation|LiveGatewayRebindBackground)'
```

Both Windows and Linux CLI build and vet checks use the repository toolchain:

```sh
go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go vet ./cmd/hostd ./internal/autodeploy ./internal/controllerrelay
go vet -tags live_docker ./cmd/hostd
```

Publication also requires the existing documentation checks:

```sh
pnpm --dir docs install --frozen-lockfile --offline
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

Local verification uses Go 1.26.7, Node 24.15.0 and pnpm 11.22.0. Local
receipts, exact binary hashes, discovered test names and before/after source
hashes are under `temp/m3-recovery-background-isolation-20261015` in the
workspace wrapper. Code and security reviews found no remaining source issue;
these reviews do not establish Docker acceptance.

## Hosted gate and limitations

The workflow must discover and pass every selected helper control before
creating Docker resources. The full gate requires the parent, all 33 boundary
cases and all 30 scenarios, including the new scenario by exact name, with no
required skip or failure. Independent resource and child cleanup remains a
separate mandatory step. Existing dependencies and deadlines are preserved.

Local compilation and helper controls cannot establish actual Docker crash
recovery. This workstation currently has no running Docker engine; hosted
Linux execution remains required. Physical second-device LAN checks and the
application-owned external database create/read journey remain separate M3
acceptance requirements. No database provisioning is introduced.

## Risk and rollback

The only production change is a package-private, nil-by-default observation
point. A broken test observer can stop its test child; no production caller
supplies one. The fixture adds a test application before the immutable proposal
census and must not bypass authorization, custody, fences, physical proof or
history checks. Reverting this branch removes the observation seam and added
acceptance case without a schema migration or persisted production-data change.
