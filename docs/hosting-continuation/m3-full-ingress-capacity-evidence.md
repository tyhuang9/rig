# M3 full ingress verification and CI capacity correction

Status: original full Windows run completed on 2026-10-08 with **two package
timeouts**, retained below. The CI correction, all nine changed selections and
complete 900-parent reconciliation passed. Local Windows ingress evidence is
complete with the recorded skips; hosted race/Docker acceptance remains pending.
This is not M3 completion.

## Source and scope

Runtime source: `9470a79b5896e53ed9ccc8f2f8843a40ce74f803` on
`feature/hosting-m3-rebind-cross-store-runtime`. The original run started and
finished at that revision with a clean checkout. This correction changes the
shared runner and the matrices in generated-runtime lifecycle, GitHub deployment
and Windows controller CI. It does not change Go source, test assertions, schema,
runtime safeguards, external database configuration or protected history.

The separate admission process-test unit through `34ca3f9` (including `2cb38b5`)
is excluded. The three
approved startup, legacy and Docker integrations remain in runtime history.

Git ancestry checks on 2026-10-08 confirmed sources `6924a32`, `7435e23`,
`8826dd3` and their integration commits `07c7f59`, `d849635`, `709b61f`
are ancestors of the runtime head. No history rewrite was performed.

The same-day GitHub read confirmed [PR #136](https://github.com/tyhuang9/rig/pull/136)
remains open/draft at `9694b4ecf0086174a4002098a2aa203c96907d50`, targeting
`feature/hosting-m3-rebind-final-config-intent`. Its reported checks passed
(documentation deployment was skipped); those results apply to that published
base, not the unpublished runtime source or this CI correction.

## Original complete attempt (retained failures)

Go 1.26.0 on Windows, at most two Go processes, existing local cache,
`GOFLAGS=-mod=readonly -buildvcs=false -p=1`, `GOTOOLCHAIN=local`. Live Docker/test
opt-ins were removed from the environment. Commands used each exact shared
runner selection with `go test -json -count=1 -timeout=32m` and the ingress
package. This executed the Go selections; it did not execute jq's success path.

All 900 discovered parent tests were assigned exactly once. Eleven groups passed;
two reached the package timeout. Overall: 835 parent passes, 1,861 subcase passes,
26 parent skips, three subcase skips and **39 unfinished parents**. No failing
assertion event preceded either package timeout. Partial passes in a failed
package do not establish a passing package or full-suite acceptance.

| Original group | Result | Parent passes | Parent skips | Unfinished | Seconds |
| --- | --- | ---: | ---: | ---: | ---: |
| gateway-v2 | PASS | 155 | 0 | 0 | 162.670 |
| rebind-history | PASS | 146 | 1 | 0 | 1900.844 |
| rebind-config | PASS | 55 | 0 | 0 | 437.008 |
| rebind-resources | TIMEOUT | 82 | 0 | 24 | 1920.420 |
| rebind-typed-withdrawal | PASS | 5 | 0 | 0 | 1195.951 |
| rebind-typed-completed | PASS | 8 | 0 | 0 | 1251.920 |
| rebind-typed-runtime | PASS | 22 | 0 | 0 | 1113.733 |
| rebind-composition-native | PASS | 3 | 0 | 0 | 972.818 |
| rebind-composition-second-generation | PASS | 1 | 0 | 0 | 960.989 |
| rebind-composition-legacy-source | PASS | 1 | 0 | 0 | 715.037 |
| current-lan | PASS | 44 | 0 | 0 | 1680.996 |
| current-serving | TIMEOUT | 72 | 0 | 15 | 1920.423 |
| ingress-remainder | PASS | 241 | 25 | 0 | 343.477 |

The resource timeout occurred while a stage-start test had run about two seconds;
completed parent times already totaled 1,917.88 seconds. The current-serving
timeout occurred 19.18 seconds into the concrete SQL/restore composition test;
completed parent times totaled 1,900.81 seconds. This supports aggregate capacity
exhaustion, not an individually stalled test. The missing 39 outcomes are listed
verbatim in the original summary and will be covered by complete replacement
selections, not only by tail reruns.

History passed at 1,900.844 seconds, leaving about 19 seconds of its limit. LAN
passed at 1,680.996 seconds. Their natural long-running families are also split
in this one correction, using these measured results.

## Complementary replacement selections

- Move committed/current rebind authority into `rebind-current`.
- Move final handover into `rebind-handover`; its rollback family has its own row.
- Move managed and ordinary current LAN recovery into `current-lan-recovery`.
- Move managed/ordinary physical current drivers and serving restoration into
  `current-runtime`.
- Retain each original group's complementary fallback for future test names.

The same 18 ingress rows appear in all three workflows. The lifecycle workflow
also retains its unchanged eight-package `runtime-packages` row. Default race
mode, explicit Windows plain mode, count one, the 32-minute limit, pipefail,
nonempty pass guard, fail-fast false and required always-run aggregate gates are
unchanged. No test or subtree is removed or subdivided.

| Group | Windows discovered | Linux discovered |
| --- | ---: | ---: |
| gateway-v2 | 155 | 155 |
| rebind-history | 137 | 137 |
| rebind-current | 10 | 10 |
| rebind-config | 55 | 55 |
| rebind-resources | 78 | 78 |
| rebind-handover | 25 | 25 |
| rebind-handover-rollback | 3 | 3 |
| rebind-typed-withdrawal | 5 | 5 |
| rebind-typed-completed | 8 | 8 |
| rebind-typed-runtime | 22 | 22 |
| rebind-composition-native | 3 | 3 |
| rebind-composition-second-generation | 1 | 1 |
| rebind-composition-legacy-source | 1 | 1 |
| current-lan | 12 | 12 |
| current-lan-recovery | 32 | 32 |
| current-runtime | 32 | 32 |
| current-serving | 55 | 55 |
| ingress-remainder | 266 | 268 |
| **Total, selected once each** | **900** | **902** |

## Executed supporting checks

The actual Bash runner was syntax-checked and invoked in 57 suite/mode variants
with an exported argument-capture `go` function that returns 97. Every failure
propagated; default mode equaled race and plain removed only `-race`. Four invalid
arguments were rejected. Six actual Windows aggregate cases and four Linux-fast
package-inventory fixtures passed, including failure, skip, cancellation, absent
and duplicate ingress cases. All three parsed YAML matrices matched.

The Go regex audit used those captured arguments, not approximate Python regexes.
It selected each actual Windows and Linux test exactly once with no empty group.
All 32 synthetic future-family/boundary names had their expected single owner.
Linux discovery came from the compiled Linux test binary executed in WSL. That
inventory is discovery evidence, not execution of all 902 Linux tests.

Full affected consumer suites were rerun at the unchanged Go source: **229 parent
tests and 361 subcases passed**, no skips. `cmd/hostd` took 30.880s and
`internal/controller` took 86.561s. Compiled discovery matched terminal outcomes
exactly. Compared with the prior 49-package baseline at `2950279`, the Go import
and test-import dependency analysis identified only these two additional
packages as consumers of the changed ingress production package. The other
47 baseline packages retain their earlier separately recorded results.

```text
go test -json -count=1 -timeout=30m ./cmd/hostd ./internal/controller
```

Six targeted filesystem-protection parents and five subcases passed in **actual
WSL Linux execution**, 1.700s, with no skips. They cover Windows-skipped unsafe
hard links, symlink artifacts and parents, preservation of directory permissions,
and the two Linux-only OS-lock link/permission tests. The binary was cross-compiled
using GOOS=linux, GOARCH=amd64, CGO_ENABLED=0 and run using `wsl.exe --exec` through
`go tool test2json`. This is plain Linux execution, not race instrumentation.

The remaining 24 Windows parent skips are opt-in live tests/helper gates; none
is claimed as real Docker acceptance.

The independent orchestrator reviewed the four-file diff and returned source GO:
complementary families/fallbacks, three matrix parity, required aggregate gates,
race/default/plain flags and timeout remain intact. This review did not execute
the new groups or establish hosted timing. `git diff --check` passed.

## Completed replacement run and reconciliation

All nine changed groups passed from the exact Go source above, with the shared
runner's verified arguments, unchanged 32-minute timeout and at most two Go test
processes. Revision, all four CI-file hashes and the empty Go-source diff were
checked before/after. The only pending checkout changes were the four reviewed
CI files and this evidence document.

| Revised group | Parent passes | Parent skips | Subcase passes | Seconds |
| --- | ---: | ---: | ---: | ---: |
| rebind-history | 136 | 1 | 445 | 1102.252 |
| rebind-current | 10 | 0 | 35 | 748.410 |
| rebind-resources | 78 | 0 | 183 | 534.025 |
| rebind-handover | 25 | 0 | 283 | 796.269 |
| rebind-handover-rollback | 3 | 0 | 15 | 661.414 |
| current-lan | 12 | 0 | 41 | 645.580 |
| current-lan-recovery | 32 | 0 | 28 | 966.771 |
| current-runtime | 32 | 0 | 37 | 1147.880 |
| current-serving | 55 | 0 | 56 | 1254.186 |

These nine groups establish 383 parent passes, 1,123 subcase passes, one parent
skip and three subcase skips. The unchanged nine original groups retain identical
source, exact selectors and terminal inventories; their passing results are
reused separately. The resulting disjoint Windows inventory is **900 parents:
874 passed and 26 skipped**, plus **1,950 passing subcases and three skipped
subcases**. There are no missing, duplicate or extra parent outcomes.

All 39 parents unfinished in the original timeouts now have actual passing
outcomes. The complete skip-name set is unchanged; the reconciliation rejects
substituting a skip for any of those 39 passes. The original two failed packages
remain failed historical attempts, not retroactively relabeled green.

The reconciliation independently parses saved JSON events, verifies each log
hash, source/environment identity, exact command arguments, expected inventory,
package success and global unique coverage. The new runner and matrices retain
their tested hashes. Its result is `m3-capacity-reconciled-20261008.json`.

```text
python C:/Users/huang/Documents/Projects/Rig/temp/m3-capacity-ci-check-20261008.py
python C:/Users/huang/Documents/Projects/Rig/temp/m3-run-capacity-partitions-20261008.py
python C:/Users/huang/Documents/Projects/Rig/temp/m3-reconcile-capacity-20261008.py
```

These were local evidence launchers around the repository's shared selections;
CI still calls `scripts/check-generated-runtime-race.sh`. The reconciler first
refused to report success while groups were pending, then passed after all nine
finished. Final results were not inferred from source review or partial logs.

Artifacts: `C:/Users/huang/Documents/Projects/Rig/temp/`. Original immutable logs
are in `m3-runtime-ingress-9470a79-20261007/`; replacement logs are separate under
`m3-capacity-ingress-20261008/`. Each group records command, process, JSON output,
exit status, expected/missing/extra/duplicate parents and SHA-256. The original
summary retains all individual log hashes.

| Artifact relative to temp | SHA-256 |
| --- | --- |
| `m3-runtime-ingress-9470a79-20261007/summary.json` | `AA0F60B6E7E215A4942F4CB9E4B55D1FF7BE43D47A90DEC7656A61D754317C4A` |
| `m3-capacity-ci-check-20261008.py` | `A7C8D7D73D4699F0AC42AB2134F20E61FA062FA5E0EF1E451B511FE1C65D8B46` |
| `m3-capacity-ci-check-20261008.result.json` | `24930D82EBAA7037827DB789FB6A2A5D000B023C86062C28193FA237051B403E` |
| `m3-capacity-coverage-20261008.go` | `9CE761C706AC6CCB7DD68D6EF1FF60DC29CC59716BD7426DA68C100FE9F2AE97` |
| `m3-capacity-ingress-partitions-20261008.json` | `7A95CC974EDC866F6FE15D1B29F73E57E8F1FC19086FF1294782B3F7F8253686` |
| `m3-capacity-windows-coverage-20261008.json` | `FF99156E91AD6FD5626366C2CE5368F222EFC799DD6C9B12E0BB16C4524639B7` |
| `m3-capacity-linux-coverage-20261008.json` | `B3CF218BA1F8490E761D8B717201B16BFC12FD1439154B18B76BDC4AA0396243` |
| `m3-runtime-consumers-9470a79-20261008.jsonl` | `98E3EF82E827BAEAFA85883E79C6BB908E3CFA6CB7DFC1116620987938BB9DD2` |
| `m3-runtime-linux-platform-9470a79-20261008.jsonl` | `86181D866A07F6F570D0A806B8DE537F6B8629E206BCD3EE3CE56172378370D0` |
| `m3-capacity-ingress-20261008/summary.json` | `75E061AD1358BD3C019998C5B32004D081364A45DEC532ABEDA45617B49D9FC1` |
| `m3-capacity-ingress-20261008/ci-source.json` | `0332E14FA931F499D7B4082BD833AD0BF4066C920763D02F229D2EDDE21D8953` |
| `m3-capacity-reconciled-20261008.json` | `293230B2203851B0139F9126532C742EC4B0AA49DA5F6977578B85B81A700A77` |
| `m3-reconcile-capacity-20261008.py` | `C032A7BDFC081945E59FA09E123F6475D000791B86B6C4B287C41FA6B09665FE` |

## Limits and rollback

Hosted Go is 1.26.7; these local checks use 1.26.0. Hosted Linux race, actual
Docker effects, full cross-process crash matrix and second-device LAN acceptance
remain unverified. The local Docker Linux-engine endpoint is absent; Windows
Docker Desktop would not establish the disposable native-Linux host-network
invariants required by the live gate. No controller restart or deployment was
performed. Production rebind factories remain closed.

CodeRabbit remains unavailable; its installer was rejected by automatic approval
review without installation authorization. No CodeRabbit result is claimed.

The prior publication proposal for `9470a79` is held. A new concrete publication
request must use the final verified revision. No publication or merge occurred.
Rollback reverts the shared runner and all three workflow matrices together;
it does not change runtime state, credentials, application-owned databases or
immutable history.
