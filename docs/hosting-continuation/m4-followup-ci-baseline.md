# M4 follow-up CI baseline

## Purpose and immutable inputs

Combine the reviewed follow-ups into one checkout for hosted verification.
The integration preserves history and the exact input file blobs; it adds no
production behavior or test-policy changes.

| Input | Commit | Included work |
| --- | --- | --- |
| [PR #237](https://github.com/tyhuang9/rig/pull/237) | `59c5d50fdb647199ea7f68422c589ca60a9f9331` | TLS failed-child observation, including PR #235 cold-start attribution |
| [PR #234](https://github.com/tyhuang9/rig/pull/234) | `f05a6e42687b47dbf230545866f805594d266b8b` | Retain image fixture roots through package completion |
| [PR #236](https://github.com/tyhuang9/rig/pull/236) | `61aba43d03701f164c61722b3448722ae5619b35` | Separate lost-handle closure from direct route-attestation guard coverage |

The new branch starts from PR #237, then integrates PR #234 and PR #236 with
ordinary merge commits. The source integration is
`53b2276327852e9946421e0df986d0df96f465a9`, tree
`f301a9b2e5c9de324a7e90cf964a82715c441214`. All three heads are ancestors.
Their changes are disjoint: 14 paths relative to shared ancestor
`c4fe39ebaa82ae35752163da97f632e8f4ce54c8`, including inherited PR #235 paths.
The delta from PR #237 is five paths before this note. No conflict resolution
or input-blob alteration was needed.

## Existing acceptance evidence

The [PR #234 Docker hosting job](https://github.com/tyhuang9/rig/actions/runs/40253619419/job/121350630878)
passed at synthetic merge `1a21958938e9078bc599e9fa141f67e5aa6f11f3` with
the same tree as that draft head. The image-authority parent and both
`BuildAndReuse` and `AuthorityLostBeforeBuildLoad` passed with normal package
completion. All 22 executed test names in that job passed without skips.
Raw log SHA-256:
`0dd3b2e14666f58c1c1ff6dc2ab827b7e91d3fb237607824abd48ebef3c2aa5d`.
This closes the image fixture's scoped Docker acceptance; it is not a run
of the combined tree.

PR #236 supplied eight guard parents and nine children with 17 distinct
RUN/PASS names. Existing parser controls supplied 225 distinct PASS names;
their source and artifact bindings permit reuse for unchanged diagnostic code.
These local results do not establish the cold-start or TLS Docker journeys.

## Combined verification and remaining acceptance

Verification of the combined tree requires the eight guard parents and nine
children to execute again, tagged package compilation, exact parent discovery,
the existing CI selector coverage check, scoped vet, canonical generation,
formatting and documentation gates. The delivery record binds actual command
receipts and distinguishes reused evidence from newly executed checks.

Hosted CI must establish the complete combined result. Cold startup, TLS
replacement, process-kill recovery and full M4 acceptance remain open.
Sanitized diagnostics report a bounded observation, not necessarily the first
failure. No E6 recovery implementation, database provisioning, managed
database, live controller refresh or deployment is included.

## Rollback

The integration draft is isolated and can be left unpublished or closed.
Its input drafts and immutable commits remain available. A future approved
rollback must preserve those histories and reverify the selected baseline;
no runtime configuration or schema change is introduced here.
