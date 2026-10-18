# M3 pending-route observation evidence

Date: 2026-10-18

Status: the first hosted observer run exposed an earlier runtime failure;
pending-route recovery acceptance remains incomplete. The branch starts from PR #183 at
`b91cfb545128e2ae7f585c5345f1b3d8db7d84cb`.

## Observed failure

PR #183's [Docker matrix run](https://github.com/tyhuang9/rig/actions/runs/38908258022)
started every one of the 63 required cases exactly once. Sixty-two passed.
`scenario/pending-route-restart` reached the 30-minute Go package alarm without
a terminal case event. Partition 06's cleanup check then failed; the other seven
partitions passed cleanup. This is not full matrix or M3 acceptance.

The failed [partition 06 job](https://github.com/tyhuang9/rig/actions/runs/38908258022/job/117029024899)
checked out tree `d25982c78790787bda29ab20672e4a8844b053e7`, matching the
published head. Its raw log SHA-256 is
`7bfff88d1d4a23570bdd56212eb6163687142f8b405e0243df9248468ab032cb`.
Artifact `12057564005` has ZIP SHA-256
`9a11c83cc918e3f8d20b324f0fa133495fd7f741a35609a564494ef1e17bc761`.
It retains Go test events, timeout stacks and case results, but no child stderr,
database, deployment records or boundary directory.

The stack shows the parent waiting for a boundary marker while the child remains
alive. The generic wait uses the fixture's 5-hour-40-minute context, which
outlasts the package deadline. The earlier deployment check establishes only
that the job has left `queued`; it does not observe a later terminal failure.
The exact runtime phase that prevented route publication is unknown.

The same partition passed both `lost-cutover-ack` and
`lost-successor-serving-ack`. The PR #183 production cutover repair therefore
has direct Docker evidence. Its Windows typed-handover suite and both Linux race
suites also passed. Those results do not establish the pending-route contract.

## Scope and acceptance

This change bounds pending-route checkpoint observation and adds finite,
allowlisted deployment diagnostics. It preserves the successful Docker-command
hook, exact marker identity, process restart and recovery assertions. Production
runtime code, workflow time limits and cleanup requirements remain unchanged.

Local controls establish marker success, terminal job refusal, bounded
stall handling, exact child reap, job identity and diagnostic confidentiality.
The unchanged live Docker case must still pass on this branch. A bounded failure
is diagnostic evidence for further investigation, not acceptance of recovery.

The observation budget takes the earlier fixture or test deadline, preserves
the existing ten-minute cleanup reserve, and caps observation at ten minutes.
The previous two-minute limit for a still-queued job is retained. Two existing
job/event repository reads do not accept a cancellation context; SQLite's
five-second busy timeout bounds ordinary lock contention, but this is not a
hard wall-clock guarantee during an exceptional database or filesystem stall.

No deployment or target-branch merge is performed. Reverting this harness change
restores the previous test behavior without changing production state.

## Verification record

The first Windows candidate check failed during compilation: removing the
`jobs` import from the scenarios file broke two existing references. No tests
executed. The failed output is retained at
`temp/m3-lan-external-db-20261015/pending-route-observation-verification/windows-controls.stdout`
with SHA-256
`ac04282f3366519500b81fab421aaa76dc92cc1ccd8dc565559f96c5f1b2305d`.
This result supplies no acceptance evidence; the corrected candidate must pass
the same workflow-selected controls before publication.

The corrected candidate restored that import and added a real temporary SQLite
observer control after QA identified that the initial tests used only simulated
snapshots. That control persists an exact deployment and a historical foreign
deployment, reads through the production job/deployment/runtime repositories,
and proves private checkpoint, error and event-message fields are persisted
before asserting they are absent from diagnostics.

The first WSL invocation used the repository working directory instead of the
`cmd/hostd` package directory. Source-relative manifest checks failed. The same
compiled binary passed when rerun from the package directory; no source repair
or recompilation was needed. Both runs are retained. The failed invocation's raw
output SHA-256 is
`45767047f7c1e23a8c3bfa2c5205e3fad397870b9e4c5d65fb2bcd4fac4fc055`.

Final local evidence is rooted at
`C:/Users/huang/Documents/Projects/Rig/temp/m3-lan-external-db-20261015/pending-route-observation-verification/v2`.
Go `1.26.7` used `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`.

| Check | Actual result |
| --- | --- |
| Windows workflow-selected preflight controls | 25 parents and 299 nested names, each RUN/PASS once; zero FAIL/SKIP; package 18.174 seconds |
| Linux binary executed under WSL from `cmd/hostd` | Identical 324-name roster, each RUN/PASS once; zero FAIL/SKIP; process wall time 19.184315 seconds |
| New pending-route subtree on Windows | 28 named results passed; 4.86 seconds, including the 0.45-second real SQLite control |
| `gofmt -l` on the three changed Go files | Empty output |
| `git diff --check` | Exit 0 |
| `go vet -tags live_docker ./cmd/hostd` | Exit 0 |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Exit 0; migration mirrors, generated OpenAPI artifacts and route/schema contract passed |

The Windows command was `go test -tags live_docker -json -v -count=1
-timeout=2m ./cmd/hostd -run <selector>`, where the exact 25-parent selector is
read from the unchanged preflight in `.github/workflows/hosting-gateway-v2-ci.yml`.
The full argument vector is retained in `windows-controls.json`. Linux used
`GOOS=linux GOARCH=amd64 go test -tags live_docker -c` and the same selector with
`-test.count=1 -test.timeout=2m`; the complete build and execution vectors are in
`linux-build.json` and `wsl-controls-package-cwd.json`. The Linux binary SHA-256
is `1cd0f8cc754daf0a69ac92990cee8691a6f5fc895be95b8f67a887584465f47c`.
Offline `test2json` timestamps/package elapsed values describe conversion, not
Linux execution duration; the wall time above comes from the process receipt.

Binding receipts prove these source SHA-256 values stayed unchanged throughout
the successful checks:

- observer/helper/controls: `7df4f504500c558099f63f8c0bf3d6309ffe7b819b3f10d3e50ef1709ab794f0`;
- scenario caller: `057c157ae9c6c9990129750f118a0644a1799f3831ea3f69eecec0054b9eb316`;
- startup-control registration: `8bf1df7a37cc970cc0002a8ca162df966164c8c1525620c9403425fbb0229920`.

Evidence SHA-256 values:

- `windows-controls-scope.json`: `5cc8fafb94154e130206c211c116a0b063263e4a3689d2d84a1792ca00ea28be`;
- `wsl-controls-package-cwd-scope.json`: `6bb754096b93d874e171a927bc095812609f6a9d2639b4df4dcec2321b2ee6eb`;
- Windows test output: `b7c63284df82ea3c02863de88e01125befe36267e04b3e7aac6749fcc3edf959`;
- successful WSL raw output: `efd98c23942594aa6558eb9a1ff4fb0a86ab90f57348cd9a886725ba4365d7dc`;
- canonical output: `b5d2e01c8124768d75a2806ceb258ec154275510774bc1e48cd5e65005d54074`.

Independent security and QA reviews found no remaining blocking issues after
the import repair and real-database coverage addition. The reviews were static;
the executable results above were run separately by the primary agent. Local
Docker, local race checks, the live recovery contract and a physical second
device were not verified by these checks. Hosted Docker CI remains necessary.

## Hosted result and diagnostic-code follow-up

PR #184 head `7b7784b18d2e759786e62ea59c70ca352cd10b1a` ran the bounded
observer in [partition 06, attempt 2](https://github.com/tyhuang9/rig/actions/runs/39095790337/job/117590530917).
The checkout tree matched that head. All 324 preflight control names and the
first seven owned recovery cases passed. `scenario/pending-route-restart`
failed after 282.32 seconds; independent cleanup passed. The observer captured
an exact terminal job at progress 60, after `apply_runtime` started, with failed
deployment, runtime and both component states. It did not observe the expected
publication marker. The observer therefore made the earlier failure visible
without reaching the prior package alarm. Recovery itself remains unverified.

The log SHA-256 is
`85fb95016e64d7091b2dc9c2373ac4d228a64edfcddcb518732ddac5ab2931aa`.
Artifact `12125579815`, ZIP SHA-256
`95f1a589574b18b037f9ff0310e23cc9ce47ed49dfa203573d6c5fb52b3f1d11`,
contains 17 case/event/result files. It contains no database or further cause
details. The child's nonzero exit was collected after cleanup killed and reaped
it; this does not prove a preceding daemon crash. A missing marker also does
not distinguish a failed delegated command from a later marker-write failure.
For this run, the independent durable event sequence ends in failure after
`apply_runtime` at progress 60, with no `wait_for_health` at progress 85. Executor
ordering places route switching and the selected publish command after that
health phase, so this run stopped before the selected command. The underlying
runtime cause remains unknown.

The follow-up adds strict, source-defined allowlists for four already-loaded
machine-code fields: job error, deployment diagnostic, runtime diagnostic and
anonymous component diagnostics. Empty values render as `none`; arbitrary or
future values render as `unknown`. Private error details, failure summaries,
checkpoints, event messages, IDs, component names and raw stderr stay excluded.
The existing exact-identity checks and marker-first ordering remain in place.
Production code, command hooks, time budgets and cleanup contracts are unchanged.

### Follow-up verification

Evidence is retained under
`C:/Users/huang/Documents/Projects/Rig/temp/m3-lan-external-db-20261015/pending-route-code-verification`.
The same Go version, read-only module settings and unchanged 25-parent workflow
selector described above were used. Candidate bindings confirm that the three
PR Go files were unchanged throughout all checks. The observer SHA-256 is
`d13d3d6c0f052cd155f0a03ada65b2d3c6331853f4b5ea53cf69fc2a2eb3db32`;
the scenario caller and startup registration retain their earlier hashes.

| Check | Actual result |
| --- | --- |
| Windows workflow-selected controls | 25 parents and 304 nested names, each RUN/PASS once; zero FAIL/SKIP; package 17.398 seconds |
| Linux binary executed under WSL from `cmd/hostd` | Identical 329-name roster, each RUN/PASS once; zero FAIL/SKIP; process wall time 17.807605 seconds |
| Pending-route subtree on Windows | 33 named results passed; 4.66 seconds; real SQLite selection, private-field exclusion and known/unknown codes included |
| `gofmt -l`, `git diff --check` | Empty formatter output; both exit 0 |
| `go vet -tags live_docker ./cmd/hostd` | Exit 0 |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Exit 0 |

The complete argument vectors, raw output and source bindings are retained with
the checks. The Linux binary SHA-256 is
`cbad72b125cf68975ca2e0f1b30f2f72e9b7882cc86585c40fde8c4e104005da`.
Offline `test2json` timestamps describe conversion; Linux elapsed time above
comes from the process receipt. Evidence SHA-256 values:

- Windows scope: `126588092eceff61d2c24e124cd00293e1e0d06f41a7426b8a0c074898afdfbb`;
- Linux scope: `a90bebbd86820caa732b76f344323aa40512feecf140c78809ada50e0538c6a0`;
- Windows output: `584316adad744a01d5f63aa5b3773bb59e506bd311e4d223c33e772a52d251b9`;
- Linux raw output: `e499e2f1a9280e996c9aa3e980e26ac3040c24fa0dbbf538bf389d0f68608f9e`;
- canonical output: `bb54ee941957e0205d38f0675e7ed85c31dff99ee154fa53b12fca6738504229`.

Hosted execution must still establish the unchanged pending-route recovery
contract. These checks used no local Docker or race compiler, and diagnostic
visibility alone is not M3 acceptance. This follow-up changes only test
diagnostics and controls; reverting it restores the preceding diagnostic format.
