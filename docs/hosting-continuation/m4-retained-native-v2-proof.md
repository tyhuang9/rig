# M4 retained NativeV2 predecessor accounting

## Observed failure

[PR #243's A0 recovery job](https://github.com/tyhuang9/rig/actions/runs/40278078667/job/121425204148)
at `b276f1eef074002e1e4424ba5c50fa828c0df1d3` failed in
`first_recovery/cold_construction/cold_group_container_correspondence`.
The child ran for 365.26 seconds and independent cleanup succeeded. Raw log
SHA-256: `6da15e15c0f054cec12682a6a11baa352358c1a9d57b3c74fd41083d9ace5edb`.
The closed diagnostic identifies the failed correspondence check; it does
not identify a particular Docker object.

[PR #244's A0 job](https://github.com/tyhuang9/rig/actions/runs/40280090070/job/121431275634)
repeated the same first-recovery refusal at head
`92f8220786af72c2146949a5dd9f6956878798d0`, with a 389.84-second child.
Its verified synthetic checkout was
`809041a85571094ee2e92c42f9cb7da07236ec20`, with the same tree as that head.
Raw log SHA-256:
`cc55ab8a1438eab5ba6bfee7bfe75dad4692bc8285a5f9cefa1cb74e7e86afec`.

## Supported lifecycle and missing proof

The final handover driver stops the NativeV2 predecessor container and
retains it. It removes the temporary stage container, starts the CurrentV1
successor, and records committed handover. The existing authentic A0
forward-recovery fixture establishes the same terminal state: a running
successor, a stopped predecessor, no stage container, and retained gateway
network and volumes.

Cold startup selects historical resources using the broad protected Docker
census. Its group accounting uses a separate, form-specific gateway
expectation. For rebind-origin CurrentV1, that expectation accounts the
successor resources. The existing retained proof accounts only the original
NativeV1 container, network and volume. The authenticated predecessor
NativeV2 state and committed journal are available in the CurrentV1
projection, but their physical resources are not accounted by either set.

This is a source-supported producer/consumer gap consistent with the hosted
failure. Source analysis does not establish which object caused that
particular hosted refusal.

The repaired local path subsequently passed the retained-resource and
container-correspondence checks, then reached a second refusal in the existing
current-gateway attester. That attester accepts an `allowApplicationRoutes`
argument but unconditionally requires exactly one attached and configured
network. Both the authenticated handover result and its physical fixture
contain the ingress network plus the application's networks. Correct cold
validation must verify that complete authenticated set.

## Repair contract

The retained NativeV2 proof must bind the authenticated predecessor state
and committed journal to the actual stopped container, restart configuration,
volumes, network and exact owned inventories. Only fully proved objects may
be accounted. Historical names, raw IDs or stopped status alone confer no
authority.

The original NativeV1 and predecessor NativeV2 physical proofs are independent.
Either complete set may be absent while the other remains present. Partial
owned NativeV2 presence must refuse construction. A shared pinned image may
remain after owned resources are absent; it is a validation input for a
present container and grants no deletion authority.

Complete absence includes the temporary stage container and any additional
owned inventory. Inconsistent protected authority must refuse even when all
physical resources are absent. Retained V2 resource checks use the existing
passive inspection policy and must not issue serving-route probes.

The retained NativeV2 network and volumes have their own physical identities.
The CurrentV1 successor names include the handover generation and operation.
They must remain distinct in the inventory and in the proof; matching image
content does not make their owned resources interchangeable.

Any added proof must participate in deep cloning, canonical digesting and
the final fresh construction comparison. Existing CurrentV1 validation,
application component checks, effect restrictions and immutable history
remain required.

Current-gateway network validation must retain the ingress identity and
address checks while deriving application membership from the authenticated
current state's routes. Terminal application-network records describe the
historical creation, so they must not replace the current route roster.
Each selected network must have the expected application owner and actual
current-container membership. Configured IDs, endpoint addresses, priorities
and IPAM must agree with the observed attachments. Missing, crossed or
additional attachments must refuse construction. Ingress-only startup keeps
its singleton rule. The existing container, volume, restart configuration,
live configuration and challenge checks remain required.

Successful current-gateway attestation performs its existing read-only admin
configuration and static-response challenge checks against the exact current
container. These checks do not permit application traffic, runtime mutation,
capacity probes or interactive commands against retained containers. Exact
passive restart-configuration copies remain part of the retained proofs.
A rejected retained
predecessor must stop before that interactive current-gateway validation.

This work does not change public authority, schemas, application predecessor
retirement, database provisioning or E6 recovery capabilities. External
databases remain application-owned.

## Acceptance status

The regression exercises the cold-group accounting function using the real
recovered SQL and protected history, fresh physical observations, and the
existing manager, budget and candidate-authority constructors. It does not
publish a second domain or reset the process ownership registry. The recovery
fixture already owns its manager; trying to construct another domain in that
same process correctly encounters the separate ownership restriction.

Earlier fixture runs stopped at setup, comparison, ownership, Docker
inventory or application policy checks. They remain recorded and do not
count as reproducing the target defect.

The corrected regression at source SHA-256
`73b8516777f0602cd481d1c422ca144ff6800bd7c3bccda96a8e287ce18dc4dc`
reproduced `cold_group_container_correspondence` on the unchanged production
base `92f8220786af72c2146949a5dd9f6956878798d0`. The positive case took 76.09
seconds using a freshly compiled Go 1.26.7 Windows test binary. Its real
inventory reads, protected-state comparisons and no-effect checks passed
before the expected refusal. Raw log SHA-256:
`e39d415a79ea413ccc13a0e9cee61fc520050bfbf0fe391e758fad8f6ba139db7`.

The fixture preserves the original observations when adapting legacy test
IDs to Docker's wire format, restores the canonical deployment-identity
trigger omitted by its seed helper, and carries the original application's
policy settings into cold startup. It does not rewrite the stored policy
receipt or protected history.

On the same binary, both invalid cases passed: a crossed predecessor container
identity and an unrelated managed container. The parent completed in 149.44
seconds with no skipped cases. Negative log SHA-256:
`52b30a51aaf091a8f35b6eee650b59a4a078a446b8dda909063aa22586c4d436`.

A later local run on frozen successor v12 passed the original correspondence
boundary and failed at `cold_group_gateway_attestation` after 73.59 seconds.
Its failure text incorrectly says "before the target boundary"; source order
places that attestation after correspondence. Raw log SHA-256:
`d973f2d55286483f8ec45c2cc95325b8c6fc8b8fa80af2f4eda13f5ca5aa5ef5`.
This run exposes the current-gateway network-count restriction described
above; it is not a passing recovery acceptance result.

### Final local verification

The final frozen source passed on 2026-10-26 using one freshly compiled
Go 1.26.7 Windows test binary, with `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and `CGO_ENABLED=0`.
The source manifest SHA-256 is
`35cd84af6689c09ca73382e5e485a53a15f0cd2ffc358df5f063dd4990fab784`;
the binary SHA-256 is
`6938600ac8f8c5a1568b978120c20cdafe03057020681767b9a4df4770f75992`.
Discovery found 1,296 unique parent tests, including all four new parents.
The existing `domain-cold-retained` CI selection includes those four.

| Test parent | Result | Time |
| --- | --- | --- |
| `TestGeneratedDomainColdFirstRecoveryAccountsAuthenticatedRetainedNativeV2Predecessor` | PASS | 73.56 s |
| `TestGeneratedDomainColdFirstRecoveryRefusesInvalidRetainedNativeV2Predecessor` | Both invalid envelopes PASS | 146.68 s |
| `TestGeneratedDomainColdRetainedV2PresenceProof` | PASS | 64.61 s |
| `TestGeneratedDomainColdRetainedV2IntegrityAndLifetime` | PASS | 13.45 s |
| `TestGeneratedDomainColdPreservesNoRetainedV1Compatibility` | PASS | 1.73 s |

Each parent ran separately with an anchored `-test.run` expression,
`-test.v`, `-test.count=1` and its recorded bounded timeout. No selected
case failed or skipped. A preliminary exact-command control also passed
in 9.77 seconds on the same binary. Gofmt, `go vet
./internal/generatedingress`, `scripts/check-generation.ps1`, diff checks
and source-stability checks passed. Independent source/security review
accepted the final exact passive-copy oracle.

The authentic positive compares both complete retained proofs across fresh
attestations and verifies unchanged protected state, physical effects and
census. The two invalid envelopes must retain a complete original V1 proof
and return no V2 proof, establishing refusal at the intended boundary.
Success permits only the exact current configuration and static challenge
commands plus the authenticated retained restart-configuration copies.
Unknown copies, extra or altered interactive commands, and interactive
commands on the refusal path fail the oracle.

The presence-only tests establish the inventory predicate for independent
V2 presence or absence; they do not establish a complete public-constructor
journey with an absent original V1. Clone/digest controls establish stored
proof integrity, while the authentic positive establishes two fresh direct
cold-group reads. Neither replaces a fresh-process public-constructor test.
Fixture-only corrections and failed intermediate runs remain in the local
evidence packet; the final test bytes differ from the original reproducer.

No manual UI, local Docker or local Linux race run was performed for this
repair. Hosted Docker recovery, Linux race checks, restored application
accounting and full M4 acceptance remain open. Inherited child, package and
CI time limits are unchanged; hosted execution must establish their adequacy
for the added regression cases.

## Rollback

Revert the repair through the PR workflow to restore the previous validation
behavior, including its observed recovery refusals. The change does not
migrate persisted state or rewrite history, and the retained proof grants
no resource deletion or capacity-release authority.
