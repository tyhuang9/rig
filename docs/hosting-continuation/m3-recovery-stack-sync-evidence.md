# M3 recovery stack dependency synchronization

Recorded October 9, 2026 (America/Chicago). The three explicitly authorized
local dependency integrations are complete. Focused verification passed on the
combined source. Updating the published recovery drafts requires separate
authorization. M3 remains incomplete.

## Scope and immutable history

The user authorized incorporating exactly published PR 137 commit
`f49cf793bfae06f3d28fa45d4609a9489075cccb` into the admission-process branch,
then propagating that result through the intent-crash and transferred-lifecycle
branches. All three integrations were ordinary local merge commits, without
conflicts, rebasing, resetting, force-pushing or remote writes.

| Branch purpose | Original approved source | Local integration commit |
| --- | --- | --- |
| Admission process, PR 139 | `34ca3f9820a96f75b08ce069390f8603bf324415` | `7d4cd6a82227ff7b33fc229041b18d99ce908d94` |
| Intent crash, PR 140 | `b3bdc230bdd9cfc49ad388b174d13b26e1854869` | `345518d13d3c7081f17455b19aff707f043e0eb8` |
| Transferred lifecycle, PR 141 | `725c3abc61713cd6db574f82aec3f61e74aefa99` | `fa806b398c9358b0624b6862ed48b6451b90bbef` |

Each commit retains its original source as first parent and the approved
dependency result as second parent. Existing PR targets remain unchanged.
The local Go verification source was the final integration commit, tree
`1fec8f1d5621df0115bd34431453108117eaf7d1`. This additive evidence document
follows that source in a separate documentation commit.

The source audit verified the exact union of the original feature and published
dependency trees, including file modes and blobs. The common ancestor was
`603c38f47be35d2c47c51d24ca4a1fa88a0a228c`. Overlapping repository-fixture and
Markdown corrections had identical blobs. The resulting deltas from published
`f49cf793` contain three, seven and eleven paths respectively; original feature
changes are retained. All ancestry and aggregate whitespace checks passed.

The stage-projection correction `788c508`, repository-capacity correction
`f351c9e`, and predecessor-retirement change `cb388d4` are excluded from these
integrations. The separate authorization to publish `cb388d4` was fulfilled as
draft PR 142, targeting PR 137; it did not authorize integrating that change here.

## Why the dependency was necessary

PR 140's original hosted prepared-claim child failed before its intended crash
boundary. Its JSON transport omitted the derived roster `EntryDigest`, then
production's strict fresh-inspection comparison correctly refused the
incomplete input before claiming SQL. The original fixture was identical in
the three recovery source branches.

The repair already existed in published `1b3eb24`, within `f49cf793`. It derives
only the omitted fields and binds them to the complete approved manifest before
fresh production validation. The same published dependency also contains
direct-Manager repository composition fixes, response assertions, isolated
migrated fixtures, complete CI partitions and bounded diagnostics.

PR 139's hosted synthetic merge included its PR 137 target. PR 140 and PR 141
targeted original source branches that lacked those later published changes.
Their target PRs' own targets were not recursively incorporated into the tested
source. Synchronizing the existing dependency avoids duplicating its repair.

No production guard was weakened to accept an incomplete manifest. SQL and
protected-history ownership, deployment fences, process locks and dormant
production rebind factories remain governed by the existing implementation.
External application databases and scoped runtime secrets are unchanged; no
managed database or Neon provisioning is introduced.

## Exact reuse of completed PR 139 hosted evidence

The first local integration tree is
`e4fa560121565721c96c037ea5c768ddfbfe243e`, exactly equal to GitHub's already-tested
synthetic commit `aa89b378335c2de3b30620babd63106ca84b2a0e` (parents `f49cf793`
and `34ca3f9`). The commit identity differs because parent order and commit
metadata differ; the complete file tree is identical.

The completed [history race job](https://github.com/tyhuang9/rig/actions/runs/37997604606/job/114047523886)
passed all four non-helper admission-process parents and their two protected
preparation cases. The completed [prepared-claim Docker job](https://github.com/tyhuang9/rig/actions/runs/37997604594/job/114047523643)
passed its exact selected parent in 58.53 seconds with live opt-ins enabled.
These are applicable to the identical first integration tree. They are not
reused as acceptance for the later combined intent/lifecycle tree.

Original logs, exact Go event assessments, hashes and immutable GitHub commit
metadata are retained in `temp/pr139-hosted-acceptance-20261009/`, summarized in
`observed-admission-acceptance.md`.

## Fresh combined-source verification

The focused run used Go 1.27.0 on Windows, `GOTOOLCHAIN=local` and
`GOFLAGS=-mod=readonly -buildvcs=false`. Linux/amd64 test binaries were compiled
with CGO disabled and actually executed through WSL. Required tests were
discovered before execution. No live-Docker opt-in was enabled locally.

| Execution | Exact selected outcome |
| --- | --- |
| Actual WSL generated ingress | 11 parents and 14 leaf subcases passed once each |
| Actual WSL hostd contenders | 1 parent passed once |
| Native Windows generated ingress | 10 parents and 12 leaf subcases passed once each; package 49.334s |
| Native Windows hostd contenders | 1 parent passed once; package 2.621s |

All selected cases and packages passed, with no failures, skips or panics.
Every compile, discovery and execution command exited zero. The complete
sequential wrapper, including builds and discovery, took 134.265 seconds.
All 784 Go/module inputs were pinned before and after and independently rehashed
against the working source. The transferred lifecycle ran once on Linux for
this combined source; the native selection excludes that expensive parent.
Earlier Windows execution of the original lifecycle source is separately
recorded and is not relabeled as combined-source acceptance.

The generated-ingress selection comprised:

- Validated child-manifest JSON roundtrip and all five rejection/order cases.
- Production-equivalent migrated database cloning, concurrent clone isolation,
  and refusal of existing database, WAL, SHM and journal paths.
- Boundary-work refusal, cross-process contender serialization, committed-claim
  recovery, and protected preparation recovery after checkpoint, intent and
  first progress record.
- The protected-intent boundary's retained-fence unit control.
- Transferred LAN lifecycle, including missing and crossed proof refusal.
- Strict current-command refusal in the simulated runtime runner.

Hostd's selected parent exercised controller contenders across processes.
These tests retain real SQL/protected-state contracts; simulated runtime
commands are distinguished from actual Docker and physical LAN acceptance.

### Commands and retained logs

Exact anchored selections, environments, source pins, discovery, output hashes
and command arrays are in
`temp/m3-recovery-stack-sync-20261009/verification/focused-final.command.json`.
Native commands use `go test -json -count=1 -timeout=20m -run <selection>
./internal/generatedingress` and the selected hostd test with `-timeout=10m`.
WSL executes the separately compiled test binaries with `-test.count=1`,
`-test.v`, the same corresponding selection and bounded timeouts.

| Retained output | SHA-256 |
| --- | --- |
| WSL ingress | `183650632DB3FF20A47616B034646BB0513F9B22F9053DBBC0CDFAEF15A98844` |
| WSL hostd | `03BFC5C3B7C9BBAF2CE4170538FAC341C062734AEA986BE13AC7A3A766E7F0C7` |
| Native ingress JSON | `173F7AB5FB6D8E547757670968D89E87CC35CCE7C3608083FE4D00DFBEDC54DC` |
| Native hostd JSON | `5C991AF217C315A3ACC2E4A32E8F90A092E0B9EEB18A8EAE1E9C245DADA34759` |

The original runtime wrapper reported false because its expected leaf list
contained only the five transport cases while comparing all observed leaves.
The complete source-derived sets contain fourteen Linux and twelve native
ingress leaves. The original result and logs are preserved. QA and root
independently assessed the full sets, one run/pass per case, command exits,
package results, artifact hashes and unchanged source without rerunning tests.
The original result SHA-256 is
`1E1E6C3561A30EC94A3D9A79FE96EE13B21D9C3914872E08E7C100BBE0154185`.
Corrected records are `focused-final.corrected-assessment.json` and
`root-independent-assessment.json` beside it.

## Static checks and documentation

- `go vet ./internal/generatedingress ./cmd/hostd` passed in 40.844 seconds.
- `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe`
  passed in 39.781 seconds.
- The eight feature Go blobs are gofmt-identical; aggregate `git diff --check`
  passed. Canonical Git bytes avoid confusing checkout CRLF conversion with a
  formatting change.
- The CI script and three workflow blobs equal the published base. All three
  workflow matrices agree, and 36 actual Bash argument captures cover both
  race and plain modes. All eleven required ingress parents map to exactly one
  of eighteen groups. This checks selection; it does not execute a full race
  suite or substitute for hosted CI.

The static wrapper's initial character validator omitted literal digits and
rejected the existing `GatewayV2` pattern after successful vet/build and Bash
capture. Its original script and terminal failure remain preserved. An additive
assessment of the captured arguments and unchanged source passed; no Go check
was rerun. `static/corrected-assessment.json` has SHA-256
`10A52900014699C8E05C79BF0BDB6F8C2817F751E9238BDC51466FE550E480AD`.

An offline, frozen-lockfile documentation install passed in 0.953 seconds with
unchanged manifest and lockfile hashes. The final documentation commands are
executed after this document is frozen:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

Their command records, output hashes and before/after documentation inputs are
retained in `temp/m3-recovery-stack-sync-20261009/docs-final/`. The publication
proposal records their outcomes without editing this checked document afterward.

## Review, limitations and remaining acceptance

The local integration audit is executable evidence of ancestry, tree union,
retained feature blobs and excluded changes. Security and final integration
review cover the combined source and retained verification records. Their final
dispositions are recorded with the concrete publication proposal after these
checks. CodeRabbit is unavailable; no CodeRabbit assessment is claimed.

No controller restart, UI journey, physical NIC change or second-device LAN
check was performed for these local integrations. Actual hosted Docker and
Linux race gates remain required for the new combined intent/lifecycle source.
The original published drafts remain at their prior heads until separately
authorized updates.

The published baseline retains four failed handover/runtime Docker families.
These integrations do not claim to repair them. The stage-projection correction
remains separate. PR 139's original combined-source repository job also hit
three package timeouts before cancellation. The separately reviewed PR 138
fixture-capacity change passed its Linux and Windows workflows and remains a
distinct prerequisite for the eventual M3 baseline; it is not included here.

## Recovery and rollback

The original commits remain immutable ancestors of their local integrations.
No application data, schema, protected record or runtime resource was changed
by this synchronization. If a new combined-source regression appears, preserve
the candidate and evidence and investigate before publication. Do not reset or
delete immutable history, remove fences, or weaken ownership checks to make
serving proceed. Publication, GitHub PR merges and deployment retain their
separate user-approval gates.
