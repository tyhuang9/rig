# M4 private generated-domain construction

## Delivery status

The scoped construction and preservation checks passed on Windows and native
Linux. Hosted Docker and race verification remain pending. This is private
construction; production consumer integration remains a later change.

Base: classifier PR #217, commit
`cc0735629b64c539111f1cef64381c23916ed2b0`.

## Purpose and scope

The private constructor joins the physical-domain registry, assembles one complete
owner, and publishes it internally only after classification and release checks
succeed. The owner contains one Manager, one capacity budget, and one candidate
authority with its ledger and route-head family. Exact concurrent callers join
that same result. A canceled waiter does not cancel the owner; an owner failure
remains a sticky refusal for later callers.

The entry point remains package-private. Normal hostd, runtime, executor and
controller composition have not moved to this constructor. The next consumer
change must wire route authority and shared accounting together before exposing
the public factory. Receipt-owned predecessor retirement remains a later step.

Application-owned external databases continue to use scoped runtime secrets.
This change adds no managed database or provider provisioning.

## Construction boundaries

### Existing stable domain

The constructor validates sealed stores, database and path identity, the daemon,
process latches, and the complete startup classification. It assembles a fresh
Manager bound to the sealed database's concrete application-access repository.
A final complete observation must still describe the same stable domain.

This path performs no persistent creation, chmod, Docker mutation, route write,
application request, capacity snapshot, or capacity acquisition. It does not
acquire the effects or gateway file locks because those primitives create their
lock files. The registry supplies process-local exclusion; repeated observations
do not establish atomicity against another privileged process. Subsequent effect
operations retain their own authorization, locking and attestation requirements.

### Proven empty domain

Only a fully classified empty domain can enter bootstrap. The constructor takes
the effects lease, registered construction activity, Manager exclusion and gateway
lock, and permits only its captured absent-to-present directory transition.
The original reservation and ancestor/database identities remain immutable.

The allowed effects form a fixed sequence: obtain the pinned image if necessary,
create the configuration volume, network and stopped gateway container, copy and
read back the empty configuration, install the initial protected route state,
and start the acknowledged container. The configuration uses the exact inspected
ingress network address required by the canonical native-V1 classifier.

Each effect requires a fresh SQL rebind-fence check, unchanged physical identity,
complete namespace observation, and all accumulated creation receipts. Network
and container receipts retain full immutable IDs. Volume creation uses a fresh
256-bit nonce and an exact label/driver/options/mountpoint/creation-time record;
the nonce is visible resource metadata, not a credential or caller authority.

The route bundle is installed with the existing create-only protected-file
primitive and strict codec. Existing destinations, uncertain installation,
lost acknowledgments, cancellation, drift and release uncertainty refuse
construction. The constructor does not adopt uncertain resources, retry effects,
repair state, or stop/remove residual resources.

The empty path checks the completed stable classification, releases exclusions
in reverse order, and checks authority and classification again before internal
publication. Failure leaves the reservation refused and preserves the residual
evidence for an explicitly authorized recovery path.

## Manager aliases and classifier compatibility

The Manager view remains anchored to its original owner. Valid aliases must share
the complete concrete dependency, latch, driver and store family; an arbitrary
copy cannot establish authority by supplying a callback with the same code
address. SQL authority remains bound to the internally installed repository.

Native-V1 startup recognition accepts the existing two-label configuration volume
or the exact optional bootstrap nonce label. The nonce must be 64 lowercase hex
characters and remain unchanged across observations. Other unexpected labels,
malformed nonces, changed values and mismatched resources remain refusals.
Recognition of existing metadata does not grant creation or adoption authority.

## Verification

The pre-change baseline passed the selected 18 unique parents on Windows and
native Linux. Each platform recorded 67 child passes plus five isolated duplicate
parent events: 90 raw RUN/PASS events, with no failures or skips.

Final verification binds production freeze 3 and the final four test files:

| Selection | Windows | Native Linux |
| --- | --- | --- |
| Eight new constructor parents | 72 raw RUN/PASS events | 72 raw RUN/PASS events |
| Eighteen affected preservation parents | 96 raw RUN/PASS events | 96 raw RUN/PASS events |
| Failures / skips in final selected checks | 0 / 0 | 0 / 0 |

Raw event counts include isolated subprocess output. The selection contains
26 distinct parent tests on each platform. Windows combines the unchanged final
binary's eight-new-plus-two-preservation run with the remaining 16 preservation
parents. Linux executes the complete eight-new-plus-18-preservation selection.
The focused Windows run records the 13 changed source hashes and its retained
binary hash; it has no contemporaneous full 1,068-file manifest. The successor
runs and final gates record the full 1,068 Go/module entries. Comparing the prior
full manifest with the successor shows exactly the reviewed registry correction.
The final independent audit rehashed every current source entry and both binaries.
This is the explicit source-recording limit of the retained Windows evidence.

The constructor checks exercise sealed inputs, one-owner joining and cancellation,
sticky refusal, complete Manager assembly and aliases, stable no-write construction,
owned path transitions, create-only protected state, exact bootstrap receipts,
lost acknowledgments, per-effect namespace/SQL drift and final release checks.
The preservation selection covers the affected registry, Manager, candidate,
state, identity and classifier paths, including nonce compatibility.

Additional passed gates:

- Format check on all 13 changed Go files.
- `go vet` and `go build` for `internal/generatedingress`,
  `internal/generatedruntime`, `internal/generatedexecutor`, `internal/controller`
  and `cmd/hostd`, for Windows and Linux.
- `go test -c -tags=live_docker` for generated ingress and hostd on both targets:
  four successful compilations; those binaries were not executed.
- Canonical `pwsh -NoProfile -File scripts/check-generation.ps1`.
- Independent security and aggregate source reviews, including the final
  owned-alias versus unrelated-legacy admission correction.

Commands used Go 1.26.7, `GOTOOLCHAIN=local`, readonly modules, CGO disabled,
explicit amd64 Windows/Linux targets, and native Linux execution through WSL.
Local Docker and race execution remain unavailable. Documentation validation is
recorded with the delivery evidence and PR description.

Evidence SHA-256:

- Final matrix validation: `ebfb26cc3c00459143fa7dc0554a39374eb1d5f39b8a21f4acb92c768fc56e1e`.
- Static/build/compile/canonical gates: `b7e2ef9d335056648d0e361ab496ddd350144534afaee531f7729bdd63539652`.
- Production freeze 3: `4a249331a30704121cc475c87b6260afa3a3c1936a2882a96ecb9d84b9865fa0`.
- Final test freeze: `5c3e0778032615de3b66bb65bf8e78ccd50038e98214c619d6cd88029037bde9`.
- Full source manifest (path, hash and size digest): `9967e4ba3fa681f90450435031781923fa5a554943d5f357541faed4e3cd9eb7`.

Earlier failures are retained. The initial complete Windows matrix caught two
legacy admission regressions; Linux execution had not started. The corrected
predicate now refuses forged owned aliases while allowing unrelated legacy
Managers through their existing admission checks. A later reporting-script
`NameError` occurred after successful execution. Its additive validation receipt
rehashes the saved outputs, sources and binaries without rerunning the tests.

## Operational limits and rollback

There is no production caller or activation in this delivery, so existing hostd
startup continues through its current path. Reverting this change before the
consumer cutover removes the private implementation and its compatibility
addition; retain the accepted classifier and registry dependencies.

Once a later caller uses this constructor, bootstrap failures can leave protected
files or Docker resources. Those are deliberately retained; rollback must not
delete or adopt them blindly. An approved recovery procedure must reconcile their
exact ownership and current physical state. This document does not authorize
deployment, automatic cleanup, or a GitHub target-branch merge.
