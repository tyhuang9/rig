# M4 generated image and builder authority

## Scope

This unit extends deployment authority checks to generated image selection,
shared BuildKit preparation, build/load, physical image reuse and the final
handoff out of image preparation. It follows the application-network boundary.
External databases remain application-owned and configured through scoped
runtime secrets. No managed database or Neon provisioning is included.

## Authority and command boundaries

The compiler receives the original worker Claim and immutable deployment,
release, configuration, plan and component identities. The concrete deployment
repository pins the original effects scope and copies the expected policy
findings. It rechecks the current binding, attempt, predecessor, plan, approval
and runtime state at fixed operations.

Only a newly selected Building artifact attempt grants permission to prepare
the builder and issue build/load. An existing Building attempt remains
unresolved; retrying does not grant a second build. Builder preparation requires
the same concrete repository and an opaque selection from that invocation.

The seven builder mutations and build/load each prepare their fixed command
before the final authority check. They preserve bounded execution, protected
Docker configuration, owned-resource validation and hard build-storage quotas.
SQL transactions do not span external process execution or resource waits.
Physical reconciliation after a lost acknowledgement still requires fresh
authority before the next operation.

## Image observations and immutable results

Artifact storage lives in `internal/generatedimagestate`; compatibility aliases
preserve existing generated-image types and errors without making repository
authority depend on the Docker compiler. The schema is unchanged.

Runtime composition injects the real generated runtime engine as the compiler's
image validator. Reusing Ready requires a fresh physical inspection followed by
a guarded result transaction. A narrowly classified definite absence or a fully
decoded contradictory image can permit one replacement attempt for a Pending
component. Ambiguous output, transport failure, cancellation, truncation and a
fabricated error code do not permit replacement. An attached ImageReady
component cannot rebuild through this path.

Successful build observation records artifact Ready and component ImageReady
atomically. A failed result transaction preserves the earlier evidence and
does not fall back to a terminal failure write. Uncertain process completion,
missing or invalid image IDs, inspection failures and cleanup failures retain
the Building attempt and original error causes. Diagnostics expose finite
codes rather than process output or runtime credentials.

The final guarded transaction validates the entire accepted component
aggregate and derives Migrating or StartingCandidate from persisted migration
state. It does not execute or approve the next command family. When a candidate
may already exist, image-boundary refusal retains the whole capacity reservation.

## Verification status

Implementation and verification are in progress. No full branch acceptance,
actual Docker result or production-readiness claim is made here yet. Final
commands, discovered test counts, source hashes and limitations will be recorded
after the reviewed source freeze.

The required checks cover repository transactions and failure rollback, all
eight final command checks, canonical image evidence and bounded replacement,
lost acknowledgements, retained capacity, original scope lifetime, frontend
public-build/runtime-secret separation and controller composition.

Hosted Docker acceptance includes the existing compiler, manual recipe, hosting,
Next and recipe-matrix fixtures. `TestLiveGeneratedImageAuthority` adds actual
build and Ready reuse plus authority loss immediately before build/load.
Its workflow requires the parent and both named subcases to pass and retains
only explicit sanitized evidence. Local fake-runner checks do not replace this
acceptance gate.

## Limits and rollback

Keep deployment activation disabled while the remaining M4 boundaries are
unfinished. Migration commands, candidate lifecycle commands, route publication,
draining, terminal completion and startup/retry authority are separate work.
In particular, startup artifact recovery still requires E6 review; this unit
does not establish safe completion of interrupted builds after process restart.

Rollback must preserve immutable artifact history, pending intent, uncertain
runtime resources and retained capacity. Do not restore an unbound compiler
overload, discard unresolved Building evidence or automatically rebuild merely
to clear an error. This draft authorizes neither deployment nor controller
activation.
