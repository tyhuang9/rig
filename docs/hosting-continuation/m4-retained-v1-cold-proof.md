# M4 retained gateway proof during startup

Status: source committed at `3581938` on
`feature/hosting-m4-retained-v1-cold-proof`, with local CI-parent integration
`f4d0cb7bab280cc743c9b9d4c95f4d44c4d2dd0a`. Hosted acceptance and M4
completion remain open.

The 25 October follow-up corrects an active Native V1 regression found by PR #225's
hosted tests. The original evidence below remains historical; the follow-up
evidence is recorded separately at the end of the verification section.

## Startup boundary

A committed gateway upgrade can retain the stopped V1 container, its network and
its configuration volume. The startup census includes these objects, while the
canonical serving state selects V2. Previously, the cold domain constructor had
no complete proof for recognizing this retained triple and refused it as an
unexplained group.

The repair recognizes these three resources only through the exact selected
committed upgrade. Supported authority is native V2 or upgrade-origin Current
V2. Rebind-origin, ambiguous, retired, aborted and partial selections do not gain
authority over a present retained triple. An absent triple keeps existing cold
startup behavior and grants no deletion permission.

## Required proof

Both the initial and final startup observations must establish:

- The complete, unique container/network/volume triple with exact canonical names,
  full identities and matching census and detailed inspections.
- The original pinned image, stopped and non-restarting V1 container, no effective
  published ports or attached network endpoints, and exact network/volume identity.
- The unchanged protected V1 source and committed upgrade authority, including
  generation, operation, state, journal and current projection where applicable.
- The exact restart configuration read through the existing bounded container-copy
  operation. The parser requires one regular JSON entry, bounded output and EOF;
  failed or truncated reads refuse construction.

The private manifest retains the full proof, with cloned maps, slices and bytes.
Final verification repeats the reads and compares the whole proof. Its digest is
supplementary and cannot replace the protected source or physical observations.
The existing V2 serving, restart, live configuration and challenge checks still
run. Unknown, extra, crossed, orphaned, replaced or partially present resources
continue to refuse startup.

## Effects and failure behavior

This proof performs bounded inspection and copy reads. It does not create, start,
stop, remove or clean up resources and does not reserve or release capacity.
Existing reconstruction can retain conservative charges for risky groups before
this check; failure does not refund those charges or change their ordering.

Public diagnostics remain closed. Container/configuration contents, protected
source material and raw external errors are not added to diagnostics. A failed
proof returns no usable domain. No migration or history rewrite is introduced.
External databases remain application-owned and configured by scoped runtime
secrets; this change does not provision databases.

## Verification record

Windows non-race checks establish 10 parent tests and 18 children, for 28 unique
passing results. These are a composite of focused runs, with corrected failures
preserved. They are not a single all-green historical aggregate.

| Area | Accepted checks |
| --- | --- |
| Supported startup | Authentic public constructors for native V2 and upgrade-origin Current V2; compatibility with no retained triple |
| Physical proof | Missing/crossed/orphaned resources, running/restarting containers, live network membership, detailed identity, volume identity and configuration mismatch refuse |
| Final observation | Replacement, disappearance, changed restart configuration, newly appearing triple and changed protected identity refuse |
| Unsupported lineage | Direct private guard proves refusal of an exact present triple under rebind-origin authority |
| Public refusal | Separate rebind fixture refuses earlier at `startup_observation/physical_identity`, returns no domain and performs no mutation, capacity probe or interactive gateway request |
| Proof cloning | Two-member network map survives copying, mutations remain independent in both directions, and nil remains nil |

The public rebind fixture does not establish reachability of the private
unsupported-lineage guard: its opener/lease provenance fails earlier. That limit
is explicit in the test name and evidence. Supported-path public integration is
established by the native V2 and Current V2 tests.

The final evidence correction executes seven affected parents and 13 children
on the final compiled binary, without changing source. This replaces 20 earlier
bindings whose test-helper or compile-source provenance was incomplete. Eight
unaffected results remain from the Current V2, final-drift and split unsupported
checks, with explicit source-impact review. All 24 existing test/helper functions
in the executed Current V2 snapshot are byte-identical in final source. The only
later production change saves and copies the original network member map; the
accepted stopped-network path still requires zero members.

Final static checks passed: empty `gofmt -d` output on all three changed Go files,
`go vet ./internal/generatedingress`, bundled PowerShell
`scripts/check-generation.ps1`, and scoped whitespace checks. Exact source and
compiled-binary hashes accompany the commands and output.

| Evidence | SHA-256 |
| --- | --- |
| Final three-file manifest | `5b416ab11b38bd51c99fb6b776afa16c0914145d9d78348b7b7209ab15aad8c4` |
| Original composite, preserved with superseded bindings | `a585f73625f045bbc9181a836d9f80eb32ea95aa96b90f991b1d710d640f6345` |
| Seven-parent execution and corrected composite bindings | `8543f193c37127f1fd642966f619065086fda98dd903c92fc5e0d31dc037b3a7` |
| Independent audit of 68 artifacts and 28 results | `37e156d06372cccc56f3c339500ecb9eb6eb6893fe04d23c31f45636f4c7ea00` |
| Final source/security review | `541c399e7ac926eddf0172e07cbd64e9e7300c7cef0f376a1ef9d53cb19356e3` |

The execution supplement also corrects the review's count for the final split
control: it ran one parent and two children. Earlier compile/setup failures,
incorrect diagnostic expectations, the network-clone defect and a whitespace
harness error remain recorded. A briefly compiled diagnostic decoration was
reverted before discovery or execution; production diagnostics were not changed
to satisfy the fixture.

### CI-parent integration

A conflict-free local merge of published PR #223 at
`bbf6a0f18261b5053759cdcb75d9117d341b6b4e` produced the integration commit
above. All three source files remained byte-identical. The diff against PR #223
contains only those files and this document; the retirement work in PR #224 is
independent. Integration identity receipt:
`0e781aed7cb16d738c03475f69035b9f6d58c792853d5332d69e40dcc7248924`.

Integrated Windows compilation passed. Discovery from that binary found all ten
new parents and 1,242 ingress parents. The 33 actual CI selectors cover that set
exactly once: the clone test belongs to `ingress-remainder`; the other nine belong
to `domain-construction`. The pinned-jq shell contract, with all three workflow
inputs, and canonical generation check passed. All 455 source inputs remained
unchanged. These integration checks perform no behavioral test replay.

The integrated receipt is
`2613a9acae0d5c235242d80c806eb4914a7c5608f2c0a6ebd5d472a30499071b`.
The independent audit of 364 artifacts, source stability and the actual selector
sets is `e73b8b36897fd17630535472248b53151f051736f8e6177df915b8d8868665b4`.
An earlier manifest-harness setup failure occurred before compilation and is
preserved separately. Documentation gates are recorded in the draft PR after this
document is finalized.

### Active Native V1 follow-up

PR #225's initial hosted run exposed an overly broad retained-resource check. A
canonical active Native V1 gateway also has a container, network and volume.
Treating that triple as a retained predecessor rejected ordinary V1 startup before
its existing active gateway proof could run.

The repair recognizes only a valid, nonpending Native V1 projection produced by
the protected resolver, with its exact canonical state digest, names and full
identities and no competing projection. Its triple remains entirely owned by the
active census. The retained proof accounts for none of those objects. Existing
active physical, configuration, network, live and final observations remain
mandatory. Names alone, unsupported lineage and ambiguous projections gain no
authority. Native V2 and upgrade-origin Current V2 retained-proof rules remain
unchanged. No effects, capacity behavior, public diagnostics or deadlines change.

The published baseline at `a6f82a7` reproduced the public runtime-boundary failure
at `cold_construction/cold_group_attestation`. Its isolation helper executed the
whole parent despite the outer child filter; the record therefore includes both
affected positive cases and passing negative cases.

The corrected source compiled successfully, discovered exactly seven selected
parents, and passed its test process. The existing nested isolation harness also
executed the multicomponent `Part2` companion. The actual result is eight unique
parent names and 34 unique child names, all passing. Coverage includes canonical
V1 deferral, names-only refusal, public active V1 startup, complete multicomponent
startup and missing/crossed refusals, closed diagnostic attribution, retained V2
and Current V2 startup, and unsupported rebind lineage.

An initial compile error and an evidence-checker error remain preserved. The
checker rejected the expected companion parent even though the test process
exited zero; an additive correction records the actual count without rerunning
tests. Formatting, scoped ingress vet, canonical generation and whitespace checks
passed on the same two source hashes. Source/security review passed. These are
local Windows non-race checks; hosted verification is still required.

| Follow-up evidence | SHA-256 |
| --- | --- |
| Published baseline RED artifact index | `97a7548a17d5b84207ca0fff287c8b6dfd752f2dc0a7166dcb086386ad621461` |
| Final-source raw passing run | `ed9ed460b68038193884d1241f4bcb9ea50f7a67ad63d98e0ca83b401a191bd0` |
| Companion-parent census correction | `834ffbe4e7ea33c22f3e0d1036bfa129d37f59c29b7be9a73bce3e5c593105e5` |
| Static artifact index | `2f94bf52ebd5777c64b10fab4eb5338680c15799876d9fd76dc5e02aa3370ba6` |
| Final source/security review | `3a05d9c585155e1e9090b1727b5c0fbd8226140839673087529c87faa77b74a5` |
| Root audit of 35 artifacts and actual results | `87847198ba20ccb1eb840bac99242a236ec29b10a10b24e6bc84d33ec84fe960` |

## Rollback and remaining limits

This is a draft development change. Reverting it restores refusal of retained
resources whose correspondence cannot be proved; it does not delete those
resources or alter immutable history. Operational recovery must preserve the
protected state and retained objects rather than deleting them to bypass startup
checks.

The hosted diagnostic on PR #223 reports
`cold_construction/cold_group_correspondence` at the real `a0-sql-claim` boundary.
It does not identify the offending physical object. The local regression proves
the retained-triple gap and repair; only hosted Docker results can establish
whether this resolves that particular journey. The separate retirement,
accounting handoff, completion and restart-recovery work remains open.
