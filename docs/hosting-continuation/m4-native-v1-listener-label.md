# M4 native V1 listener label

## Purpose and observed failure

Align the cold-start NativeV1 container descriptor with the existing gateway
creator and validator. This follows [PR #240](https://github.com/tyhuang9/rig/pull/240)
at commit 4c3532bc7de5cf9ee57cc9cd4ba98ca3e75e6fc8.

The [PR #238 A0 diagnostic](https://github.com/tyhuang9/rig/actions/runs/40258708537/job/121366115630)
and [PR #239 repeat](https://github.com/tyhuang9/rig/actions/runs/40260550254/job/121371807810)
both failed before daemon assembly with
cold_construction/cold_retained_gateway_final_labels. Their independent cleanup
steps passed. Neither run reached the requested recovery boundary.

Raw log SHA-256 values:

- PR #238: a3b73b652e10c33b789fa29ec321a9b84520b840cb30a12587027fe460fe195f
- PR #239: b51e6f0e19438c346c8998e3273bf5c4b726519f6a9c5cbc4d8069b312aa008a

The real V1 gateway creator emits io.rig.listener-isolation=v1, and its
inspection validator requires v1. The NativeV1 startup descriptor instead
used the V2 listener version, v2. The exact-label matcher therefore rejected
the producer's V1 label set. Existing retained fixture objects copied their
container labels from the descriptor and missed that disagreement.

## Repair and preserved boundaries

Use the established V1 version in the NativeV1 container descriptor.
Keep V2 descriptors and constants unchanged.

The ownership matcher continues comparing the complete Rig label namespace.
Missing listener labels, crossed V2 labels and extra Rig labels still refuse.
Canonical identities, volume nonce validation, network and volume projections,
protected lineage, active-versus-retained selection and runtime authority
retain their existing rules.

The regression fixture extracts the entire container label map from the real
gateway argument builder and checks it against independent literal values.
It replaces the complete descriptor-derived container map. It rejects
malformed and duplicate label arguments rather than accepting ambiguous input.

## Verification contract and limits

First reproduce the refusal using the producer-derived fixture with production
unchanged. The authentic constructor must fail specifically at the retained
gateway final-label check; an earlier setup failure is insufficient.

Then verify the corrected descriptor with fresh compilation and exact test
discovery. Focused controls cover producer agreement, authentic retained
construction, a V2-upgrade origin, crossed or missing listener labels, an
extra Rig label, active-V1 deferral and unsupported rebind-origin refusal.
Formatting, scoped vet, canonical generation, diff and documentation checks
must pass before publication.

This repair addresses a concrete producer/consumer mismatch. Sanitized hosted
logs do not expose the physical labels or prove this is the only A0 blocker.
Only a subsequent hosted run can establish A0 acceptance. E6 recovery and
complete milestone acceptance remain open.

## Rollback

Revert this descriptor correction through the PR path, preserving the evidence
and regression test history. The prior code would again refuse actual V1
labels during the affected cold-start path. This change introduces no schema
migration, database provisioning, new authority or deployment.

