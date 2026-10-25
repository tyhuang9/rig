# M4 combined recovery CI baseline

Status: local history-preserving integration and focused combined verification
passed on 2026-10-25. Hosted acceptance remains pending. This does not complete M4
or the remaining M3 physical device qualification.

## Purpose and exact inputs

Test the already-reviewed startup proof, accounting/completion, capacity test
partitions and genuine process-restart fixtures together. Isolated branches lack
some sibling fixes, so their full CI results cannot establish the combined state.

| Input | Exact published head | Included work |
| --- | --- | --- |
| PR #229, starting branch | `bd67d264f5a6f8dc6b12f479e77dcd7e0064ed18` | Receipt-owned retirement, accounting handoff and immediate completion |
| PR #225, first merge | `5db8ff0bad46cf563fdaf314411aeaee2adbebd4` | Retained V1 cold proof, active V1 compatibility and closed refusal attribution |
| PR #228, second merge | `24d1246ab6b69541c806595471a36080858bd5e7` | Fresh-process rebind fixtures, session lifetime correction and complementary race partitions |

All three share base `bbf6a0f18261b5053759cdcb75d9117d341b6b4e`.
The two ordinary merge commits preserve the three input histories. The integration
head is `5ef211ebdc2115ee691b4053a0618d49f275c74a`, tree
`55664fb960407e89208f60a3c1b98c481a53793f`.

The inputs change 36, 9 and 18 paths respectively, with no pairwise overlap. The
actual merges had no conflicts or manual resolutions. Every one of the resulting
63 changed Git blobs matches its input branch exactly; all three input heads are
ancestors of the integration head. The proposed draft targets PR #229's branch,
so its additional input delta is 27 paths plus this evidence document.

Local integration identity receipt:
`6c1a509a345ad99e59ef7c07fc5403e5546a75a83a1009d48a350cebd26ecb71`.
This proves the Git union only, not runtime acceptance.

The separate mode/object check verifies all 63 file modes and Git object IDs
against the owning inputs:
`4adb6f12c6a1a92964e2fc4eeb69213573ff7a212348a9c135aa3ca89e879d27`.

## Preserved boundaries

Cold startup observations do not grant retirement or completion authority. The
original physical predicates still decide startup acceptance; the added fixed
reasons only identify refusal paths. Immediate completion still requires the
live receipt-owned execution family. Fresh-process fixtures exercise a new
process without relaxing registry or physical stage proofs.

Stored receipts cannot reconstruct a lost capacity lease or authorize a later
worker. Cross-process E6 recovery remains closed pending authentic cold admission
and a reviewed accounting reconstruction contract. Ordinary capacity acquisition
cannot be substituted for adopting an already-running workload: physical free
resources already reflect its consumption.

External databases remain application-owned and configured through scoped runtime
secrets. This integration adds no database provisioning or application migration.

## Verification

The combined run compiled eight retained test binaries: `generatedingress`,
`generatedexecutor`, `generatedruntime`, `generatedruntimestate`,
`deploymentexecution`, `deployments`, `jobs`, and `cmd/hostd` with `live_docker`
tags. Each used `go test -c -run=^$ -o <retained-binary> <package>`; only the hostd
compile added `-tags=live_docker`. These were Windows builds without race detection.

The retained binaries executed the exact focused selectors from the accepted
combined plan, with verbose output and no skipped tests:

| Package | Selected parents | Actual outer passes | Nested passes |
| --- | ---: | ---: | ---: |
| generatedingress | 17 | 18 | 54 |
| generatedexecutor | 3 | 3 | 2 |
| tagged cmd/hostd, non-Docker controls | 2 | 2 | 220 |

The additional outer pass is the isolated companion
`TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRosterPart2`.
There are 299 unique passing test names, zero failures and zero skips. The
ingress selection covers cold-runtime refusal, exact current/retained rosters,
retained-V1 attribution, accounting/completion, closed replay protocol and
sequential effects scopes. Executor checks cover receipt-owned completion,
precommit failure with retained receipts/capacity and post-activation proof loss.
Tagged hostd checks cover process-kill controls and closed rebind diagnostics.

Actual CI script `-run`/`-skip` arguments were captured and checked against the
retained binaries' full discovery. Ingress selects all 1,283 parents exactly once
across 34 suites. Repository heavy/remainder pairs select all 33
`deploymentexecution`, 63 `generatedruntime` and 27 `generatedruntimestate`
parents exactly once. This is selector coverage, not execution of all those tests.
The five new attribution parents remain in the domain-construction catchall;
the explicit domain-cold selector retains its existing 19-parent count.

Both native shell selector contracts passed with the pinned jq binary. Formatter,
eight scoped `go vet` invocations, the canonical repository check and
`git diff --check` passed. Migration mirrors 043–045 are byte-identical. The 63
input paths remained unchanged before and after verification; the manifest records
Windows working bytes separately from Git blob bytes.

The initial verification harness incorrectly compared full workflow suite lists:
the generated-runtime workflow has four additional package-only suites. Its
failure record is preserved. An additive metadata/selector/static closure checked
the shared ingress lists and actual script arguments without replaying successful
compilation or behavior.

Evidence digests:

- Accepted plan: `592e51251fdeef0e29d2abfbee53793295412df01d548b5568f3475d178f7497`.
- Combined result: `62e2d705c6f8fad66d3de353c6b6705514961e660f93b7f8b7096218bb724e76`.
- Artifact index, 320 verified files: `3f9e291d9ea80e790db4ad39aada7c4e473aaf9207d501731045af90e02282f1`.
- Unchanged source manifest: `bbbe2051a5bfd8305d62380a1c8c15d7a51a3b58d029e18886751c911485acdb`.
- Independent raw-output/source/selector audit: `2aeb13aa3a34029c524c0c1c89b9e7fcb6096807797148c3c5f1768ee61297b2`.
- Combined source/security review: `456f0a1086fcb9e98dfecb4a12853243f0bf69c0f1b5a13c730a76bc8436c36e`.
- Final integration review: `f99972a4c95b54fca5a158010e43538200b9cb1d8eadbd6c120315467c42014d`.

Documentation verification runs frozen offline installation, workflow checks,
production build and accessibility checks before publication. The publication
receipt records their actual exits and logs.

Hosted A0 startup, full Docker and race CI, process-loss recovery and full M4
acceptance remain unverified. No live controller refresh or deployment is part
of this integration.

## Rollback

This is a development acceptance branch. The three published source branches
remain available with their original history. Restore a previously verified
controller version only with its matching state compatibility assessment.
Preserve protected history, immutable receipts and uncertain resources. Do not
delete state or release retained capacity to bypass a failed proof.
