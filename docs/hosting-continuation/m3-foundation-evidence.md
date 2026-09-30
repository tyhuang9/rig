# M3 LAN foundation: local evidence

Status: partial M3 implementation, 2026-09-30. These M3 branches remain local
and unpublished. No LAN listener has been started on this local host. The
administrator profile API records desired state; the new upgrade action is
implemented and locally tested, but no LAN URL is exposed.

## Exact revisions and scope

- Base: M2 draft PR #84 head `1063c3c15fcb612fd3e75156596337f5d6422fb1`.
- SQLite allocator: `5fb39921c9dd29fef8c916ecc6f5144fb9418eb3`.
- Pure Caddy v2 config builder: `accf5fb4b755547f55f61de4bed34d21e0c63b09`.
- Protected v1-to-v2 gateway journal: `9b0f3ae`, implemented in
  `internal/generatedingress/upgrade_state.go` and its focused tests.
- Cross-process gateway lock on current Manager operations: `0695892`.
- Immutable Docker resource bindings in the migration journal: `48152f9`.
- Exact v2 resource creation arguments and host preflight: `0195695`.
- ID-bound observer and read-only migration recovery: `50bc2f9`.
- Journaled v2 gateway staging: `1a6eb3e`.
- Staging acceptance record: `0ba9751`.
- Journaled v2 transfer and bounded rollback: `aff192f`.
- Automatic private gateway network selection and recheck: `0d04d14`.
- Authenticated desired LAN gateway profile API: `f2768bf`.
- Single-snapshot upgrade authorization recheck: `c75eb51`.
- Protected upgrade-generation history scanner: `759734b`.
- Explicit journaled-rollback retirement: `ff8508c`.
- Protected pre-journal abort history: `99a965e`.
- Fresh-proof pre-journal abort coordinator: `da93a12`.
- Authenticated gateway upgrade action: `9fdf586`.
- Recovery-only startup and pinned controller operation: `5a63430`.

The allocator stores approved desired gateway and per-app access revisions,
action digests, compare-and-swap heads, and unique durable port ownership. It
retains uncertain allocations and rejects archiving an app with an unreleased
port. Separate database-handle contention tests exercise allocation and
approval races. The config builder preserves the v1 `.rig.localhost` server,
creates one bounded Caddy server per LAN port, requires the approved private
IPv4 Host for assigned routes, and gives wrong Hosts and unassigned ports a
generic 404. Its output is data only; no gateway is created or reloaded.

The gateway journal unit writes a separate protected v2 state and migration
journal while retaining the v1 bundle. Its initial v2 state deep-copies the
v1 routes with zero LAN assignments. The journal binds the exact v1 and v2
state digests, profile revision, selected interface and address, fixed v2
resource identity, explicit upgrade action digest, and approving actor ID.
Protected create-only artifacts accept exact replay when an identical file
already exists; a reported write durability failure stops the operation even
if immediate readback sees the new bytes. Changed replay payloads fail. The
phase table defines recovery transitions for an incomplete staging attempt,
including rollback or an uncertain state; it never authorizes publishing LAN
ports without a new controlled action. Startup now selects a restricted
controller for an unfinished approved operation. A committed journal
retains the initial target digest while allowing later valid app-route changes
under the same protected network plan. The pure validators reject a network
subnet that contains the selected LAN address and rejects duplicate allocation
or access revision identities across apps. Preparation and Manager dispatch use
these validators; their real Docker behavior remains unverified on this host.

The Manager holds a persistent handle-based gateway lock across
route switching, startup provision/recovery, route observation callbacks, and
capacity observations. Two independent Manager instances targeting one data
root cannot issue competing Docker commands while one holds the lock. A
process-exit test proves the next instance can acquire the lock; an injected
post-switch release failure preserves the executor's
`CandidateMayBeLive` signal so it does not clean a potentially serving
container. The lock also serializes committed-v2 route changes; the new staging
operation has not been invoked against live Docker.

The new `internal/hostnetwork` helper enumerates currently assigned RFC 1918
IPv4 addresses on active, non-loopback host interfaces. An approved interface
index/name and exact address must still match; duplicate address ownership,
an unavailable interface, a changed DHCP address, and wildcard/public
addresses fail closed. This is an interface-ownership prerequisite only. It
does not by itself inspect host routes or authorize a bind.

The separate host-route snapshot reads Windows' IPv4 forwarding table or a
Linux netlink route dump plus all assigned interface prefixes. A pure check
rejects a proposed private Docker subnet when it overlaps any more-specific
host route or interface prefix; only the default route is ignored. Snapshot
completion is private to the package, so a caller cannot fabricate a usable
empty snapshot. Linux reads the current network namespace. The route and
interface reads are sequential. The stage operation calls the preflight before
creating the ingress network and checks the selected interface before stage
start; final cutover must revalidate again around its own Docker mutation.

The read-only v2 gateway observer checks pinned Docker identity, protected v1
source identity, live and restart Caddy configuration, explicit bindings,
owned resource inventories, application-network attachments, and route/404
probes. It rechecks resource inventories and container process generations
after probing. The committed-v2 Manager calls it under the gateway lock to
attest serving and recovery state. Stage/final require host-side publication
and reachability. The controller has an authenticated upgrade action but no
LAN URL.

The v1 compatibility fence now checks for either protected v2 state or
migration-journal marker while holding the gateway lock. A marker blocks
legacy route switching, provisioning/recovery, observation, and capacity
reads before v1 state or Docker access. It preserves the route candidate when
Switch is blocked. The fence treats orphaned, malformed, rolled-back, and
committed markers alike on legacy paths. The Manager now dispatches an exact
committed v2 marker pair to a separate serving path; partial, corrupt, and
noncommitted pairs still fail closed before v1 Docker work.

The committed-v2 Manager path uses a protected pending route record before a
Caddy reload. It preserves the last committed app routes and LAN bindings,
requires exact final-v2 topology and candidate endpoint/network preflight,
then reloads, reattests, and commits. Restart recovery distinguishes an exact
committed config from an exact proposed config and rolls the latter back. It
also recognizes the narrowly attested crash window where the live config is
proposed but the restart config is still committed, then restores and reattests
the committed config. Any other mixed or unknown topology remains unresolved
without automatic mutation. New
application networks are rejected before the pending write because v2 does not
yet have a journaled gateway network-attachment transaction. Existing attached
application networks can be switched after immutable endpoint, health, alias,
network, and transport checks. Provision/recovery, local route observation, and
capacity use the committed-v2 owner rather than falling through to v1. The v1
observation bound remains 15 seconds; committed-v2 observation gets a bounded
three-minute post-lock budget while respecting shorter caller deadlines.

The read-only v2 observer can now classify exact v1, stage, and final states
when its Docker evidence is complete. It proves immutable endpoint ownership,
health, and unique alias membership before and after probes. Stage/final host
publication must return a distinct port-specific Caddy challenge body on every
selected-IP pool port, both through the host and directly through the attested
container; generic 404 listeners and loopback publication fail. After commit,
historical v1 app networks and endpoint health may change while the stopped v1
gateway core identity remains pinned. Precommit rollback phases retain their
strict v1 dependency. No app-serving final gateway, LAN access action, or LAN
URL is enabled by this serving-path slice.

The migration journal now binds the pinned image, ingress network, both volume
creation identities, and stage/final container IDs as each resource is made.
Bindings are monotonic and phase-gated; a changed replay or a replacement
resource fails closed. The observer compares found Docker resources with those
protected bindings before accepting a serving topology. A separate read-only
recovery classifier recognizes only specific `stage_intent` and
`transfer_intent` crash windows with exact resource, config, network, and
rollback evidence. It never authorizes mutation on unknown or drifting input.

Pure argv builders define the exact v2 Docker network, volume, and hardened
stage/final container creations. The stage publishes only the selected private
IPv4 port pool; the final also publishes the v1 loopback port. Host preflight
checks that the approved interface still owns the exact address and that the
planned Docker subnet does not overlap current host routes or interface
prefixes.

`Manager.StageGatewayV2` is an explicit local operation over an already
prepared, protected migration. It holds the Manager and cross-process gateway
locks through the phase loop. It proves v1, writes `stage_intent`, creates and
durably binds the pinned image, ingress network, two volumes, and stopped stage
container, then copies and rereads the exact 404-only Caddy restart config.
After stopped-stage identity proof and a final selected-interface check, it
starts the stage and requires exact live identity, listener challenge, and 404
proof before writing `staged`. It never stops v1 or enters transfer. A crash
after binding a stopped stage but before config copy is compensated only after
exact bound-resource, stopped/no-host-binding, and v1-serving proof; that proof
cannot start a listener. Create-before-bind ambiguity, drift, or incomplete
cleanup records unresolved state rather than a false rollback. The
authenticated controller reaches staging through the upgrade coordinator; it
does not display a LAN URL. Docker behavior is tested with a deterministic
driver, not a live daemon.

`Manager.TransferGatewayV2` now advances an exact staged migration through
`transfer_intent`, `v2_serving`, and `committed` under the same Manager and OS
gateway locks. It copies and rereads the final config through the still-running
stage, then stops and removes only the bound stage while v1 serves. It creates
the final container stopped with the ingress and all existing app networks in
one deterministic Docker create command, binds its immutable ID, and requires
stopped-container proof before stopping v1. Immediately before v1 stop/start,
the production driver reattests the journal-bound v1 identity and issues the
Docker command by immutable container ID. After final start it checks exact
topology, state-bound per-app loopback challenges, the LAN challenge/404
proof, and selected-interface ownership before commit. Known failures restore
and reattest v1; unknown or unbound Docker outcomes remain unresolved. Tests
cover interrupted and reported-failed protected writes at transfer intent,
final ID binding, v2 serving, and rollback intent without replacing immutable
history. The authenticated controller reaches transfer through the upgrade
coordinator, but still exposes no LAN URL.

Temporary Caddy configs remain readable by the non-root container user after
copy. The host-side working directory must therefore be private before Manager
initialization and each config copy. Existing controller directories are now
validated instead of accepted by path alone. Unix requires a current-user
0700 leaf and trusted, non-replaceable ancestry (including sticky-parent
semantics). Windows requires a protected current-user-only DACL and holds
rename-blocking handles on every ancestor until the temporary file is removed.
Any failed validation blocks the copy before creating a host config file.

Migration 026 now records an administrator-approved gateway upgrade claim
bound to the exact current profile revision and one operation ID. Prepared,
serving, unresolved, and committed claims pin the profile in SQLite; only a
rolled-back claim releases the pin. Claim identity, transition events, and
upgrade audit rows are retained. The initial prepared event, profile-head pin,
and legal transitions have database triggers as well as repository checks.
An unresolved claim can move only to committed or rolled back after the
controller obtains fresh topology evidence; an exact synchronous upgrade can
move prepared to committed. The repository itself does not inspect Docker.

`Manager.UpgradeGatewayV2` now holds one Manager and OS gateway lock across
source-v1 attestation, host preflight, protected preparation, stage, transfer,
and final attestation. It resumes only the same operation, profile, actor,
digest, network, and host-port binding. Ambiguous writes or topology remain
unresolved. A rolled-back outcome requires fresh exact-v1 proof, and a lock
release failure downgrades either final outcome to unresolved. The authenticated
controller action now calls this method. It derives the administrator from a
session and verifies the exact durable claim inside the Manager lock before
protected history or Docker work; a supplied actor and digest alone are not
authorization.

The gateway network planner now selects the first available RFC 1918 `/28`
under the Manager and OS gateway lock before the first protected journal
write. It excludes the approved interface's full LAN prefix, current host routes and
interface prefixes, and all Docker network IPv4 subnets from a stable
ID-bound list/inspect/relist inventory. Unknown address-bearing Docker
networks fail closed; only the exact built-in `host` and `none` networks may
have empty IPAM. The journal retains the selected subnet and fixed gateway
and container addresses, so replay cannot silently choose another plan. A
fresh host and Docker inventory check runs before network creation. These
checks have unit evidence but no live Docker run.

The authenticated administrator API at `/api/v1/system/lan-gateway-profile`
now reads the desired profile, current host interface candidates, and an
optional server-canonical proposal digest. POST requires session, CSRF, an
exact approval digest, a fresh unique interface/address selection, and an
expected profile revision. It derives the actor from the session, retains
repository CAS/idempotency history, and returns no-store responses. An exact
committed request replays from immutable history even if interface discovery
later fails; new writes still require a fresh interface check. It does
not call the gateway upgrade Manager, bind a port, or claim that a listener
serves traffic.

The upgrade authorization repository checks the exact operation claim,
administrator role, action and request digests, complete claim-event history,
canonical profile revision, and current profile head in one read-only SQLite
snapshot. The authenticated upgrade route now invokes this check from inside
the gateway lock before protected history or Docker work and again before the
first Docker mutation.

The generated-ingress history scanner keeps the fixed v2 pair as generation
zero, allocates create-only operation-scoped files for later generations, and
rejects gaps, duplicates, partial or corrupt history, and unknown reserved
artifacts. An exact retry can repair a state-only preparation crash after
fresh v1 and no-v2-resource proof; ordinary v1 management stays fenced until
a terminal rolled-back generation has a protected retirement receipt. The
explicit rollback finalizer removes one exact idle v2 resource between full
observations, then reads back that receipt only after proving the current v1
gateway serves and no v2 resource remains. The coordinator reports
`rolled_back` only after finalization and successful lock release. Protected
artifact fingerprints detect changes that persist across scanner snapshots;
same-user mutation confined to a separate protected read is outside the
cooperating-writer model.

The pre-journal abort unit records an operation-scoped, create-only receipt
only when no migration journal exists. It supports an empty generation or an
exact state-only preparation crash and rejects a journal, competing operation,
or contradictory artifact. The coordinator freshly proves that the current
v1 gateway serves with restart configuration and stable endpoints, and that
the deterministic and label-owned v2 Docker resources are absent. Abort and
receipt replay do not select or persist a network plan. A complete journal
installed despite a reported write failure stays unresolved for a later exact
retry. An abort result becomes terminal only after receipt readback, a final
history scan, and successful gateway-lock release.

## Executed verification

| Check | Result |
| --- | --- |
| `go test -count=1 ./...` | Passed after both units were complete. |
| `go vet ./...` | Passed. |
| `go run ./cmd/openapi-gen -check` | Passed; no API change. |
| `pnpm --dir web test` | Passed, 400 tests in 16 files, on full rerun. |
| `pnpm --dir web build` | Passed; existing large-chunk warning. |
| `powershell -ExecutionPolicy Bypass -File scripts/check-embedded.ps1` | Passed; embedded dashboard matched deterministic build. |
| `pnpm --dir docs build` | Passed in normal Windows context. |
| `pnpm --dir docs check:workflow` and `check:accessibility` | Passed, with accessibility run after build. |
| Mirrored `025_lan_access.sql` SHA-256 | Both copies: `35B2FC694317AAE0F2EAD64FB919AE6B1280108C4A2862D45FDD2F43CF9006C5`. |
| `go test ./internal/generatedingress` and `go vet ./internal/generatedingress` | Passed for the journal unit. |
| `go test -count=1 ./...` after the journal unit | Passed with normal local Windows permissions. |
| `go vet ./...` after the journal unit | Passed. |
| `go test -count=1 ./...` and `go vet ./...` after the Manager lock | Passed with normal local Windows permissions. |
| Gateway lock contention and process-exit tests | Passed on Windows; independent Manager contention and injected release-failure tests passed. |
| Linux amd64 ingress test package cross-compilation | Passed; Linux runtime tests remain unrun locally. |
| `go test -count=1 ./internal/hostnetwork` and `go vet ./internal/hostnetwork` | Passed for the interface-selection helper. |
| `go test -count=1 ./internal/hostnetwork` after the route snapshot | Passed on Windows, including a native route-read smoke test. |
| Linux route tests under WSL and Windows/Linux test cross-compilation | Passed as reported by the host-route implementation agent; WSL reads the current network namespace. |
| `go test -count=1 ./...` and `go vet ./...` with normal Windows permissions after both read-only slices | Passed. |
| `go test -count=1 ./internal/generatedingress` and `go vet ./internal/generatedingress` after observer review fixes | Passed. |
| `go test -count=1 ./...` and `go vet ./...` after the observer corrective pass | Passed with normal Windows permissions. |
| `go test ./internal/generatedingress -run 'TestLegacyV1Fence' -count=5` | Passed for the v1 compatibility fence, including lock contention. |
| `go test -count=1 ./...` and `go vet ./...` after the v1 compatibility fence | Passed with normal Windows permissions. |
| `go test -count=1 ./...` and `go vet ./...` after committed-v2 Manager and observer integration | Passed with normal Windows permissions. |
| Linux amd64 `go test -c` for `./internal/generatedingress` | Cross-compiled the test package; Linux runtime execution was not available locally. |
| `git diff --check` after committed-v2 integration | Passed; Git reported only working-copy LF/CRLF conversion warnings. |
| `pnpm --dir docs build` after the evidence update | Passed without render errors after escaping Vue template braces in the recorded Docker command. |
| `go test -count=1 ./internal/generatedingress` and `go vet ./internal/generatedingress` after journal binding, creator/preflight, and recovery observer | Passed. |
| `go test -count=1 ./...` after these M3 prerequisites | Passed with normal local Windows permissions. The sandboxed run failed in unchanged workspace/Docker fixture tests with `Access is denied` and Docker unavailable. |
| `go vet ./...` after these M3 prerequisites | Passed with normal local Windows permissions. |
| Linux amd64 `go test -c` for `./internal/generatedingress` after recovery observer | Cross-compiled; Linux runtime tests were not executed locally. |
| Independent code and security reviews of the prerequisite diff | No confirmed blocker for a local commit. Both require live Docker verification before M3 acceptance. |
| `go test -count=1 ./internal/generatedingress` after staging review fixes | Passed with normal local Windows permissions. |
| `go test -count=1 ./...` and `go vet ./...` after staging review fixes | Passed with normal local Windows permissions. |
| Linux amd64 `go test -c` for `./internal/generatedingress` after staging review fixes | Cross-compiled; Linux runtime tests were not executed locally. |
| `gofmt -d` and `git diff --cached --check` for the staging slice | Passed. |
| Staging code and security reviews | A Docker container-ID command defect and a bound-stage pre-config crash gap were corrected before commit. Security review found no confirmed exploitable finding for this local 404-only slice. |
| `go test -count=1 ./...` after transfer | Passed with normal local Windows permissions; all Go packages completed. |
| `go vet ./...` after transfer | Passed with normal local Windows permissions. |
| Linux amd64 `go test -c` for `./internal/generatedingress` after transfer | Cross-compiled; Linux runtime tests were not executed locally. The exact local output artifact was removed after compilation. |
| `gofmt -d` and `git diff --cached --check` for transfer | Passed. |
| `pnpm --dir docs build` after the design update | Passed without render errors. |
| Transfer code and security reviews | The name-only v1 stop/start flaw was corrected with fresh identity attestation and ID-bound commands. Durability-window tests requested by review were added. Security review noted temporary host-config visibility when the working directory is traversable. `docker.PrepareControllerDirectories` creates a private directory when absent but does not verify the mode or ACL of an existing one. Docker behavior remains unverified live. |
| `go test -count=1 ./...` and `go vet ./...` after private-directory guards | Passed serially with normal local Windows permissions. |
| `pnpm --dir web test` during the private-directory work | Passed, 400/400 tests, serially. The final guard edit changed only Go code; web tests were not rerun afterward. |
| Windows working-directory and ancestry-guard tests | Passed, including blocked leaf, parent, and higher-ancestor renames while a guard is held and allowed rename after close. |
| Linux amd64 securetemp test binary run under WSL | Passed the private-directory tests, including a 0700 leaf beneath a sticky writable parent, a readable ancestor, and rejection of a writable nonsticky ancestor and symlink. The first sticky-parent test fixture used numeric `01777`, which Go's FileMode did not interpret as `ModeSticky`; the corrected fixture passed. |
| `git diff --cached --check` and independent security re-review | Passed. Review found the earlier writable-ancestor exposure closed for unprivileged local users. Windows traversal-only ancestor ACLs may be rejected by the fail-closed `GENERIC_READ` guard. |
| Mirrored `026_lan_gateway_upgrade_claims.sql` SHA-256 | Both copies: `749870419020B7127EA4C002B1C66E143C0887AAB5756F86FDCE7D7271AF2824`. |
| `go test -count=1 ./internal/appaccess ./internal/database ./internal/generatedingress` after review corrections | Passed with normal local Windows permissions. Includes two-handle claim contention and read snapshot tests, direct-SQL pin/history rejection, exact retry/recovery transitions, coordinator rollback drift and lock-release tests. |
| `go test -count=1 ./...` and `go vet ./...` after review corrections | Passed serially with normal local Windows permissions. |
| `pnpm --dir web test` during claim/coordinator work | Passed, 400/400 tests in 16 files. The later review corrections changed only Go and SQL; web tests were not rerun afterward. |
| `go run ./cmd/openapi-gen -check` during claim/coordinator work | Passed; no API route or schema changed in these slices. |
| Claim/coordinator code and security review | Five correctness findings were fixed and re-reviewed: recovery transitions, preinserted profile-head pin, one-snapshot claim read, initial prepared event, and rollback lock-release outcome. Security review found no reachable production caller; authenticated claim binding before an endpoint remains a hard gate. |
| `go test -count=1 ./internal/generatedingress ./internal/controller ./cmd/hostd` after network/profile integration | Passed with normal Windows permissions. An initial sandboxed run failed in existing workspace/ingress fixtures; it also exposed one stale `/28` overlap fixture, which was corrected before this pass. |
| `go test -count=1 -p 1 ./...` after final network/profile review fixes | Passed across all Go packages with normal Windows permissions. Two earlier full runs with default package concurrency failed in timing-sensitive localhost relay/TLS/process tests; each failing package passed when run separately. |
| `go test -count=1 ./internal/generatedingress` after empty-IPAM security fix | Passed with normal Windows permissions. |
| `go vet ./...` and `go run ./cmd/openapi-gen -check` after network/profile integration | Passed. |
| `pnpm --dir web test` and `pnpm --dir web build` after generated API contract | Passed: 400 tests in 16 files; production build passed with the existing large-chunk warning. |
| `pnpm --dir docs build`, `gofmt -l`, and `git diff --check` | Passed. Initial sandboxed pnpm commands could not traverse Windows junctions; normal Windows runs passed. |
| Independent security and manual integration reviews of profile API and network planner | Profile API security review found no exploitable issue in this desired-state-only slice. Network security review found empty Docker IPAM could hide an allocated subnet; the planner now rejects it except for exact built-in `host`/`none` networks. Manual review found and verified fixes for exact replay after interface discovery failure, full selected-interface-prefix exclusion, and malformed proposal status mapping. CodeRabbit aggregate review was unavailable because its CLI is not authenticated; manual review found no remaining actionable blocker. |
| `go test -count=10 -run '^TestAuthorizeGatewayProfileUpgrade' ./internal/appaccess`, `go test -count=1 ./internal/appaccess`, `go vet ./internal/appaccess` | Passed for the single-snapshot authorization read, including two-handle consistency, stale head, demoted actor, and corrupt history. |
| `go test -count=1 ./internal/generatedingress` and `go vet ./internal/generatedingress` after history/retirement integration | Passed with normal Windows permissions. A restricted-sandbox run failed broadly in unchanged manager fixtures; the identical normal-permission command passed in 41.315s. |
| `go test -count=1 -p 1 ./...`, `go vet ./...`, `go run ./cmd/openapi-gen -check` after commits `c75eb51`, `759734b`, `ff8508c` | Passed with normal Windows permissions; all Go packages completed. No API contract changed. |
| Independent rollback-history review | Found and fixed an in-place artifact mutation detection gap by adding bounded content fingerprints. No false terminal outcome or lock-order defect remained in read-only review. Live Docker behavior remains unverified. |
| Focused pre-journal abort and history tests | Passed for empty/state-only receipts, network-selection failure, exact replay, stale v1 and Docker topology, wrong operation, receipt write ambiguity, complete-journal ambiguity, later generation, and lock-release failure. |
| `go test -count=1 ./internal/generatedingress` for pre-journal abort | Passed with normal Windows permissions. |
| `go test -count=1 -p 1 ./...`, `go vet ./...`, `go run ./cmd/openapi-gen -check`, `gofmt -l`, and `git diff --check` after pre-journal abort integration | Passed with normal Windows permissions. No API contract changed. |
| Independent pre-journal abort review | Found no confirmed defect in the protected receipt or live-proof result boundary. Controller claim sequencing and live Docker execution remain required. |

The first full web test run had one focus assertion failure in the unchanged
`application-setup.test.tsx`; that exact case passed alone and all 400 tests
passed on a full rerun. The first docs accessibility invocation ran in
parallel with the docs build and failed because `dist/index.html` was not yet
present; the ordered rerun passed. A sandboxed docs build could not traverse
pnpm's Windows junction; the same build passed in normal Windows context.
The first full Go rerun after the journal unit was sandboxed and failed in
unchanged workspace/Docker fixture tests with `Access is denied` and dependent
`invalid_workspace` results. The same exact command passed with normal local
Windows permissions. The focused ingress tests passed in the sandbox.

The Windows Go toolchain has CGO disabled, so `go test -race` was not run. The
storage tests instead exercise separate SQLite handles and repeated contention.
No live Caddy config validation, Docker port bind, cutover/rollback,
second-device LAN request, or database-backed LAN journey has run on this
branch. The journaled staging and transfer tests use fake drivers; they do not
establish Docker's actual stopped inspect shape or host listener behavior.
Those are future M3 gates.
The installed Docker CLI currently cannot reach the Docker Desktop Linux
daemon: `C:\Program Files\Docker\Docker\resources\bin\docker.exe version
--format '&#123;&#123;json .Server&#125;&#125;'` failed because
`//./pipe/dockerDesktopLinuxEngine` does not exist. No live gateway acceptance
claim follows from the read-only observer's unit tests.

## Remaining publication gates

The controller must derive action-specific consent and administrator identity
from an authenticated session; a caller-supplied approval struct is not
authentication. It must attest that the selected interface owns the approved
private IPv4, then prove actual Docker bindings, Caddy routes, current access
revision, and serving deployment before activation or URL display. A
journaled v1-to-v2 gateway upgrade and proven rollback must preserve the v1
loopback route. A disable flow must remove and attest a route before releasing
its port or allowing application archive. Hosted Linux/Docker and Windows
Docker Desktop checks, plus a physical second-device journey using an
application-owned external database, remain open. No managed database or Neon
provisioning belongs to this milestone.
The runtime network planner now rejects overlap with current host routes,
interfaces, and Docker networks before selection and immediately before
network creation. Live Docker behavior and the post-mutation topology proof
remain unverified. The journal's actor
field records provenance; it does not authenticate or authorize the actor.
The protected journal phase update checks the expected phase before replacing
the file, but it is not an atomic compare-and-swap across processes. The new
staging and transfer operations hold the handle-based gateway lock across
their complete mutation and attestation loops; journal methods alone do not
enforce that boundary. Every active LAN binding must also be compared with its
approved SQLite row. After an explicit journaled rollback retirement or
pre-journal abort, a fresh operation can use a new protected generation while
retaining all earlier state, journals, and receipts. The authenticated
controller must still prove the exact receipt or journal outcome before it
releases a prepared SQLite claim.
The first live v2 marker writer must hold the same lock as the v1 compatibility
fence. The lock and state paths must remain bound to one protected directory
identity through the operation; a same-user parent-directory substitution is
still a conditional filesystem threat.
An older running hostd binary does not honor `gateway.lock`; migration must
prove it is stopped before relying on the lock. On Linux, a malicious process
with the same user identity can replace the lock path while a handle is held;
Rig never unlinks it, and the open-time path identity and permission checks
reject unsafe paths before work begins.
The live observer now implements immutable endpoint and unique-alias proof,
host-side selected-address publication proof, post-probe endpoint/config
reinspection, and comparison with protected resource bindings. These have unit
evidence only and require real Docker and Desktop execution. Docker's stopped
container inspect shape for configured networks and effective ports remains
unverified locally. The staging and transfer writers perform config,
selected-interface, gateway-lock, and post-start checks in unit tests. Real
Docker must still confirm that the stopped final container retains all
configured network and port bindings, that manual v1 stop/start preserves the
attested Docker restart count, and that the final Caddy process can read its
copied config. A physical second-device LAN journey remains a distinct
acceptance gate.
The gateway-specific host challenge proves listener identity, but generic
application probes accept any HTTP status. They do not prove that Caddy selected
and forwarded the intended app route. A controlled live Docker route matrix
with distinct backend response markers, wrong-Host and cross-app requests is
required before claiming LAN routing acceptance.

The local cutover implementation now handles those journal phases and
compensation paths, but hosted Docker must prove the real network, bind,
config-read, route, and rollback behavior before this slice is accepted.
Per-app consent, LAN access activation and disable, UI diagnostics, and the
physical second-device journey remain open. The host-side
private-directory boundary has unit and WSL evidence, but its behavior with a
live Docker config copy and on a locked-down Windows service account remains
unverified. Windows ancestors that allow traversal without `GENERIC_READ` fail
closed until a narrower safe guard is implemented and tested.
The authenticated gateway upgrade action is locally tested. The new startup
classifier and restricted controller path are also locally tested, but their
actual restart behavior with live Docker remains unverified. A recovered
operation stays restricted until a later process restart proves normal mode.
Live Docker and second-device checks are also required before M3 acceptance.
Docker volume deletion remains
name-based after immediate exact label, mountpoint, and creation-time
reinspection because Docker exposes no immutable volume ID or conditional
delete. A non-cooperating same-privilege actor replacing a volume in that
window could cause deletion of the replacement; this path requires a live
Docker gate and an explicit single-owner maintenance assumption.

## Authenticated gateway upgrade action: local evidence

The generated-runtime-only `GET` and `POST`
`/api/v1/system/lan-gateway-upgrade` require an administrator session; POST
also requires CSRF. Responses are `no-store`. GET provides a server-computed
approval digest for the current profile revision and separates the durable
claim from a fresh, locked Manager observation. It exposes no LAN URL. POST
accepts only a bounded operation ID, exact profile revision ID and number, and
approval digest. It derives the actor from the session and creates the SQLite
claim before invoking the Manager. The Manager callback reauthenticates that
same session and CSRF token, then rechecks the exact claim and profile in one
SQLite snapshot under the gateway lock. The callback runs before history or
Docker work and again at the first Docker mutation boundary. A late denial
leaves protected preparation unresolved without making a Docker change.

After the Manager releases its lock, the controller records committed or
receipt-backed rolled-back only when proven; otherwise it pins unresolved.
An unresolved invocation
returns 503 even if an earlier durable claim remains committed. Exact
rolled-back replay re-enters the Manager to reattest protected history and
current topology. The read path rechecks the claim after observation to avoid
mixing different claim epochs. `unknown` is used when live availability is not
proved. The API has no app-level LAN consent or public route activation yet.
Normal `GET` excludes rolled-back claims. Recovery-only `GET` reads the exact
operation selected at startup, so its historical rolled-back outcome remains
visible while the restricted process is running. The M3 operator UI still
needs a complete recovery experience before acceptance.

| Check after the action diff | Result |
| --- | --- |
| `go test -count=1 -p 1 ./...` with normal Windows permissions | Passed across all Go packages. The controller package and generated-ingress package passed within this run. |
| `go vet ./...` | Passed. |
| `go run ./cmd/openapi-gen -check` | Passed. |
| `pnpm --dir web build` | Passed; Vite emitted its existing large-chunk warning. |
| `pnpm --dir web test` | Passed, 401 tests in 16 files. |
| `gofmt -l` for the new Go files and `git diff --check` | Passed; Git reported only LF/CRLF conversion notices. |
| Focused action and observation tests | Passed for authorization denial, session revocation, CSRF rotation, exact replay, stale approval, malformed requests, claim/observation drift, lock release, and Docker mutation gating. |
| Independent security review | Two findings were fixed and rechecked: rolled-back replay initially skipped live attestation, and an in-flight revoked session initially remained authorized before Docker mutation. No remaining concrete vulnerability was identified in the local diff. |
| CodeRabbit aggregate review | Unavailable because the CLI is absent from WSL; no manual result is attributed to that tool. |

The restricted Windows sandbox failed in unchanged workspace-policy fixtures
and could not access the usual Go cache. The full Go suite above passed with
normal Windows permissions. The Docker Desktop Linux daemon is unavailable on
this host, so no live container migration, LAN route, rollback, or physical
second-device behavior is verified by this action unit.

## Recovery-only startup: local evidence

The controller reserves its loopback listener before runtime setup, then
acquires one process-lifetime, protected owner lock for its DataRoot before
opening SQLite. For generated runtime it reads the current
gateway profile and every retained upgrade claim in one validated SQLite
transaction, then classifies the protected ingress history under the gateway
locks using read-only topology checks. Invalid or mismatched history fails
startup. An unfinished approved operation starts a controller limited to
bootstrap/session management and `GET`/`POST`
`/api/v1/system/lan-gateway-upgrade`. It does not create the runtime executor,
recover deployments, start workers, auto-deploy, or start the relay. The POST
path rejects any operation ID other than the one selected at startup before
creating a claim. The restricted mode remains latched after rollback until a
new process start repeats the cross-store and live topology proof. Normal mode
performs ingress recovery and a second snapshot/history inspection before
starting background work.

| Check for the recovery-only startup diff | Result |
| --- | --- |
| `go test -count=1 -p 1 ./...` with normal Windows permissions | Passed across all Go packages, including appaccess, generatedingress, controller, hostd, and controllerowner. |
| `go test ./internal/generatedingress -run '^TestGatewayV2Startup' -count=1` | Passed for fresh v1, committed v2, unfinished claims, retained retirement receipts, mismatches, and failed proofs. |
| `go test ./internal/appaccess -count=1` and repeated snapshot tests | Passed for full claim history validation and one-transaction consistency. |
| `go test -count=1 ./internal/controller` | Passed, including exact operation pin, blocked APIs, historical read after rollback, and latched mode. |
| `go test -count=1 ./internal/controllerowner` | Passed for contention, process crash release, and unsafe paths on Windows. The Unix implementation cross-compiled. |
| `go test -count=1 ./cmd/hostd` after early listener reservation | Passed, including an occupied-listener check that proves no controller working directory was created first. |
| Live Docker restart, Linux owner-lock execution, and physical second-device journey | Not run; Docker Desktop Linux daemon and a second device are unavailable locally. |

An independent final-integration rerun of the full Go suite after the listener
change stopped when an unchanged deployments SQLite fixture reported
`database or disk is full (13)` with less than 400 MB free. The earlier full
suite passed; the post-listener hostd suite and `go vet ./...` also passed.
This disk-capacity failure is not counted as a passing rerun or as a code
regression. The temporary Go build cache was cleared and disk space recovered.

The startup tests use controlled fixtures and command runners. They do not
prove a real Docker daemon's topology response or an operator's browser
experience after a process crash. When generated runtime is disabled, startup
rejects any retained SQLite upgrade claim or protected v2 history artifact;
this configuration cannot bypass the recovery gate by starting a different
runtime mode on the same DataRoot.

An older running `hostd` binary does not acquire the new owner lock. Before
upgrading, stop and verify the old controller has exited; listener reservation
detects the common same-address overlap but cannot detect an old process on
another port. Cross-version overlap remains a rollout risk until this
prerequisite is exercised in an actual upgrade.

## Per-app LAN disable ledger: local evidence

The next local M3 branch adds migration 027 without changing migration 025,
historical allocations, or existing enable revisions. A new immutable disable
intent binds an administrator approval to the exact current access revision,
allocation owner, port, and gateway profile. While that intent is pending,
allocation state changes are rejected and the port remains owned. Migration
025's prohibition on releasing an owned allocation remains in force.

This branch deliberately has no finalization or release API or table. A
caller-supplied digest alone cannot prove that Caddy removed the route; the
later ingress slice must produce and verify protected route-absence evidence
before adding a release path. No disable route, LAN URL, UI action, or Docker
mutation is claimed by this storage slice.

Verification on the final intent-only candidate:

- `go test -count=1 ./internal/database` — pass.
- `go test -count=1 ./internal/appaccess` — pass.
- `go test -count=1 -p 1 ./...` — pass across all Go packages.
- `go vet ./...` — pass.
- `git diff --check` — pass.

The migration test runs 027 against a populated pre-027 database, checks that
the original allocation table and five triggers retain their SQL, and checks
that owned and released rows retain their values. The app access tests cover
administrator and exact-owner validation, replay, stale requests, active and
uncertain allocation freezes, direct SQL release denial, and two database
handles contending for the same intent. Live Docker route removal, a physical
second-device visit, and UI behavior remain unverified and outside this branch.
