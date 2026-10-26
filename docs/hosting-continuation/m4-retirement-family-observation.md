# M4 retirement error families

## Purpose and observed failure

Continue from [PR #241](https://github.com/tyhuang9/rig/pull/241) at
461f6e414d5b2beff547493ec9a719d612cdf8d8. Its preceding hosted TLS run
[reached a paused replacement](https://github.com/tyhuang9/rig/actions/runs/40263404413/job/121380423432)
with route_reconciliation_required. The retained diagnostic was
previous_retirement/unknown. Raw log SHA-256:
134eabe4febc69ea6701272931287f1501a94672cc916f11be287e9955ceb4cd.

That result establishes a nonnil retirement handle whose completion returned
an unclassified error. It does not establish which inner operation refused
completion or whether the handle retained valid authority at that point.
Cold-start gateway selection and process-kill retirement-unavailable failures
have separate evidence.

## Observation contract

Classify the remaining source-supported error families at the existing final
fallback. Preserve the earlier sealed-cause branch, including its early
unknown result, and existing cleanup, readiness, context, state and
deployment-cancellation precedence.

| Observed family | Fixed cause, only with previous_retirement |
| --- | --- |
| Recognized candidate inspection or command error | candidate_observation_failed |
| Recognized private accounting refusal | accounting_unresolved |
| More than one recognized non-storage family | identity_ambiguous |

The candidate recognizer accepts only the ten source-reachable codes:
runtime_unavailable, process_termination_failed, runtime_output_truncated,
candidate_hardening_failed, image_unavailable, image_drift_detected,
network_drift_detected, candidate_cleanup_failed, cancelled and runtime_timeout.
It traverses standard unwrap trees and ignores typed nil values. Arbitrary
codes and custom first-match As behavior do not provide classification.

The internal generatedingress package exposes a boolean predicate for exactly
eight existing private accounting identities: capacity invalid, closing,
stale and busy; receipt mismatch; mutation unquiesced; physical transition;
and command entered. Pins changed, confirmed absent, cleanup closed and
operation kind remain excluded. The predicate cannot create an error,
receipt, token, lease or authority, and does not wrap the returned error.

Within the final fallback:

1. Count distinct existing non-storage identities, the candidate family and
   the accounting family. Duplicates within one family count once.
2. More than one recognized family yields identity_ambiguous, independent of
   join order.
3. One existing identity retains its cause, including when joined with
   storage unavailability or unmatched text.
4. Without an existing identity or ambiguity, storage unavailability retains
   precedence over a lone candidate or accounting family.
5. Otherwise a lone candidate or accounting family supplies its fixed cause;
   unmatched errors remain unknown.

These observations never authorize an operation. Completion, ledger state,
effect ordering, returned error trees, retries, time limits and cleanup retain
their existing behavior. Output contains only the finite stage/cause pair.
Raw errors, SQL, identifiers, paths, credentials and Docker output remain
excluded. The current A0 readiness phases and construction reasons are
preserved.

## Verification and evidence limits

The focused verification roster contains six parents across generatedingress,
generatedexecutor and the hostd fixture controls. Fresh test binaries must
discover every selected parent and report actual RUN/PASS records. Relevant
formatting, vet, canonical generation, diff and documentation checks are
required before delivery.

Accounting production evidence comes from the owning package's authentic
Complete refusal fixture. It checks the private error identity, the new
predicate and unchanged completion/state behavior. Executor accounting and
mixed-family precedence are exercised through a private pure decision table;
production derives its accounting input exclusively from that predicate.
These are separate pieces of evidence, linked by source inspection.

The existing executor boundary fixture can inject a constructible candidate
error before delegating to Complete. That row verifies routing, pause and
absence of effects. It does not prove that authentic Complete produced an
accounting error. No accounting sentinel exporter, factory or authority-bearing
test hook is introduced to manufacture such evidence.

Hostd controls verify the three fixed causes, stage pairing, taint and bounds
refusal, while retaining A0 phase and construction controls. Local results do
not establish hosted TLS recovery, cold-start acceptance, E6 or full M4.
The next hosted run must identify the actual family; that label alone does
not establish the behavioral repair.

## Rollback

Revert this observation change through the PR path, preserving history and
evidence. Revert the classifier and matching hostd vocabulary together. No
data migration, database provisioning or runtime authority change is involved.
