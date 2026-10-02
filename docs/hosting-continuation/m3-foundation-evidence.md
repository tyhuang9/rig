# M3 LAN foundation: local evidence

Status: partial M3 implementation, 2026-09-29. This branch remains local and
unpublished. No LAN listener has been started on this local host, and no
controller action or LAN URL is exposed.

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
phase table permits startup to roll back an
incomplete staging attempt or mark it uncertain, never to resume publishing
LAN ports without a new controlled action. A committed journal retains the
initial target digest while allowing later valid app-route changes under the
same protected network plan. The pure validators reject a network subnet that
contains the selected LAN address and rejects duplicate allocation or access
revision identities across apps. These functions are not called by the live
gateway upgrade action or controller yet; committed-state validation is used
by Manager dispatch.

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
and reachability. The controller has no LAN URL or upgrade action yet.

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
cleanup records unresolved state rather than a false rollback. This method has
no controller action or URL display caller yet. Its Docker behavior is tested
with a deterministic driver, not a live daemon.

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
history. This operation still has no authenticated controller caller or LAN URL.

Temporary Caddy configs remain readable by the non-root container user after
copy. The host-side working directory must therefore be private before Manager
initialization and each config copy. Existing controller directories are now
validated instead of accepted by path alone. Unix requires a current-user
0700 leaf and trusted, non-replaceable ancestry (including sticky-parent
semantics). Windows requires a protected current-user-only DACL and holds
rename-blocking handles on every ancestor until the temporary file is removed.
Any failed validation blocks the copy before creating a host config file.

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
The runtime network planner must reject overlap with all relevant host routes
and interfaces using the new hostnetwork helper immediately before and after
mutation; the helper is not wired yet. The journal's actor
field records provenance; it does not authenticate or authorize the actor.
The protected journal phase update checks the expected phase before replacing
the file, but it is not an atomic compare-and-swap across processes. The new
staging and transfer operations hold the handle-based gateway lock across
their complete mutation and attestation loops; journal methods alone do not
enforce that boundary. Every active LAN binding must also be compared with its
approved SQLite row. A fresh upgrade after a `rolled_back` journal needs a
history-preserving retry generation or explicit
operator recovery; fixed create-only paths currently refuse another operation.
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
Authenticated action-specific consent, LAN access activation and disable, UI
diagnostics, and the physical second-device journey remain open. The host-side
private-directory boundary has unit and WSL evidence, but its behavior with a
live Docker config copy and on a locked-down Windows service account remains
unverified. Windows ancestors that allow traversal without `GENERIC_READ` fail
closed until a narrower safe guard is implemented and tested.
