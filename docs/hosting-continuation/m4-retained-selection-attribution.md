# M4 retained gateway selection attribution

## Purpose and observed failure

The [PR #241 first-boundary run](https://github.com/tyhuang9/rig/actions/runs/40263404387/job/121380423457)
tested commit 997fb97e647aab479556f4bc7044f1b579b91d8a after the native V1
listener-label correction. It failed before daemon assembly with
cold_construction/cold_retained_gateway_selection. The earlier final-label
refusal was not reported. The parent took 424.08 seconds; manifest and runner
controls and independent fixture cleanup passed. A0 acceptance remains open.

Raw log SHA-256:
852cd74dadfcf705feec1fb6159888e6efa9c9b6b155cb79ace84c01ed2a98cf.

That reason covered both an unsupported retained-authority selection and a
startup-reader consistency refusal. The readiness failure also omitted which
child was starting. The first-recovery marker was emitted only after recovery
readiness, so its absence could not identify the failing child. Elapsed time
does not establish the phase.

## Fixed observations

The retained-authority predicate reports a private status from its existing
decision branches. It remains the sole acceptance decision. The closed
construction diagnostic distinguishes:

| Reason suffix | Observed refusal |
| --- | --- |
| selection_kind | An unsupported current gateway form with the expected producer shape. |
| selection_authority | An inconsistent authority or an unexpected selection shape. |
| selection_reader | Reader consistency checks failed after a supported selection. |

Each full reason begins with cold_retained_gateway_. The kind observation does
not independently establish canonical authority validity. Missing or mixed
current, terminal, native, or upgrade pointers cannot earn that classification.
No status enters an accepted retained proof.

At the three existing crash-matrix readiness calls, a test-only wrapper adds
the fixed caller phase: initial, first_recovery, or third_normal. An unknown
phase refuses. The existing bounded child-output projection remains in use;
the phase adds no operation ID, path, configuration value, or raw error.

## Preserved behavior

Native V2 and current V2 with upgrade origin retain all existing store,
generation, operation, state, journal, topology, lineage and digest checks.
Current V1 and current V2 with rebind origin remain refused. The observation
does not introduce a fallback resolver or a second authority validator.

Reader identity and configuration checks, protected and SQL read order,
whole-triple attestation, runtime effects and capacity accounting remain
unchanged. No retry, timeout, migration, database provisioning, or E6 recovery
behavior is added.

## Verification contract

Run both authentic supported retained-gateway constructors on the final
source: native V2 and current V2 with upgrade origin. Retain the existing
rebind-origin refusal, incomplete-triple and pinned-read cases. Add focused
private controls for an authority mismatch and reader mismatch, each requiring
an empty proof and no later commands or effects.

Those private invariant controls establish classification and refusal; they
do not prove that public construction can produce the crossed inputs. Closed
diagnostic and hostd controls cover the three reasons, fixed phases, invalid
values, wrong stages, tainted output and existing output bounds. Review each
phase literal against its actual child variable.

Require exact test discovery, no missing expected tests, scoped formatting and
vet, canonical generation, aggregate diff review and documentation checks.
The aggregate PR review includes the preceding listener-label correction.

## Remaining acceptance and rollback

The next hosted run must establish the actual phase and refusal family. A
finite diagnostic is not proof of the underlying cause, permission to accept
rebind-origin authority, or completion of A0 or the hosting milestone. The TLS
previous-retirement failure remains a separate investigation.

Revert this observation change through the PR path while preserving the
listener-label repair and recorded evidence. The older generic diagnostic
would return; no data migration or runtime rollback is required.
