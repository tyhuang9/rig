# M4 retained gateway final-consistency attribution

## Observed refusal

PR #233 at `c4fe39ebaa82ae35752163da97f632e8f4ce54c8` ran synthetic
checkout `57f55af24d93d061fb5d4024d9b46692db455f81` in the
[cold-start A0 Docker job](https://github.com/tyhuang9/rig/actions/runs/40250689876/job/121341703106).
The `a0-sql-claim` case failed after 230.97 seconds before daemon assembly,
reporting `cold_construction/cold_retained_gateway_final_consistency`.
Raw log SHA-256:
`d0fc02f059b95a78b8e741bd24ef33d1423a2f7c50cdd009186d40b6757b3458`.

This identifies the final retained gateway comparison as the refusing boundary.
It does not identify the failing field or establish temporal drift between reads.
The comparison checks the census against fixed native-V1 descriptors, with only
the detailed container and network IDs and volume mountpoint/creation timestamp
overlaid. An extra Rig label present in both observations can also fail this
exact comparison.

## Change and invariants

The existing boolean matcher remains unchanged and solely decides acceptance.
After its existing refusal, a private pure helper classifies the already available
values into three fixed reason families:

| Reason | Compared family |
| --- | --- |
| `cold_retained_gateway_final_identity` | Container/network IDs and the required empty volume ID |
| `cold_retained_gateway_final_labels` | Exact Rig labels, retaining the existing valid volume bootstrap nonce exception |
| `cold_retained_gateway_final_projection` | Network/volume driver, scope and options, plus volume mountpoint and creation timestamp |

A specific reason is emitted only when exactly one family differs. Zero,
multiple, or inconsistent classifications retain
`cold_retained_gateway_final_consistency`. Foreign labels retain the existing
matching semantics. The helper returns no compared values, paths, IDs, label
keys, timestamps, or nested error text.

The refusal cause, nested diagnostic precedence, empty proof, and ordering remain
unchanged. Classification adds no Docker reads, writes, capacity requests, or
traffic. The two live hostd diagnostic projections admit only the exact new
`cold_construction` pairs through their existing closed grammar.

## Verification and remaining acceptance

The focused verification roster is:

- `TestGeneratedDomainColdRetainedGatewayFinalConsistencyAttribution`
- `TestGeneratedDomainColdRetainedGatewayFinalConsistencyFallback`
- `TestGeneratedDomainConstructionDiagnosticClosedAttribution`
- `TestControllerProcessKillBoundaryControls`
- `TestLiveHostdRebindClosedDiagnosticControls`

The real private-boundary cases cover identity, labels, projection and mixed
families, checking refusal, empty proof and the exact existing five-read prefix.
The delivery evidence records discovery, actual outcomes, compilation, vet,
canonical generation and documentation checks separately.

Hosted A0 must run again to identify the actual failing family. This diagnostic
change does not repair or accept the retained gateway, prove successful cold
startup, or complete M4. Other Docker failures require their own evidence.

## Rollback

Revert this diagnostic change to restore the generic final-consistency reason.
The same objects continue to be refused. No database migration, deployment or
runtime configuration change is involved.
