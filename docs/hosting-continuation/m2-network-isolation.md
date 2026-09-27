# M2 runtime network isolation qualification

**Purpose:** establish real Docker evidence for application-origin private
network and control-plane isolation while retaining the generated runtime's
ownership, route, hardening and cleanup safeguards.

**Candidate branch:** `feature/hosting-m2-network-isolation`, published as draft
[PR 80](https://github.com/tyhuang9/rig/pull/80), based on gateway-readiness revision
`26a2837a278d120d961fae58391b605c0b290226`.

**Initial source revision:** `bd99246872918c5b6e1539c0e69011dee2ba29c8`.

This records the local candidate and its verification. The first hosted Docker
run exposed a test-probe timeout issue; this page does not claim RUN-09 or M2
completion.

## Actual baseline

On 2026-09-27 the reusable checkout was clean at the gateway candidate, and the
remote branch matched that exact revision. All verification checks on draft
[PR 77](https://github.com/tyhuang9/rig/pull/77) passed at that head, including
its real Docker gateway-readiness and two-application route qualification.
Those route results do not prove application-origin isolation.

Before implementation, with existing dependencies and `GOFLAGS=-mod=readonly`:

```text
go test -p 1 -count=1 ./internal/generatedingress ./internal/generatedruntime
```

Both packages passed: ingress 1.813s, runtime 0.547s. Go verification used
normal Windows filesystem/process permissions and a temporary Go cache.

## Required probe contracts

- Probe from real owned application containers, with their deployed network
  attachments. Do not substitute an unattached diagnostic container for the
  untrusted application actor.
- Resolve and attest owned target identities and network addresses. A direct
  other-application private-IP probe must be paired with a successful request
  to that same live target from its own network. DNS failure alone does not
  prove bridge isolation.
- Check that identical own component aliases reach the correct owned fixture;
  separately check the other application's qualified private alias. Keep
  permitted public app route controls distinct from forbidden private targets.
- Prove ordinary outbound HTTPS from both applications to a controlled
  host-side fixture outside their private bridges. Verify its certificate with
  the fixture CA and correct peer identity; do not disable TLS verification or
  add production network attachments to obtain access.
- Treat any HTTP response from a private or admin target as reachable,
  including errors such as 403 or 404. Require a bounded, recognized transport
  denial; unexpected execution failure is a test failure.
- Pair application-origin Caddy-admin denial with a successful read-only admin
  request from inside the owned Caddy container and fresh application-route
  responses. A missing listener is not an isolation result.
- Distinguish Caddy's admin listener from hostd's controller/API boundary.
  In the continuous controller journey, pair an authenticated successful host
  request to the actual controller listener with frontend/backend-origin
  transport denial through the attested bridge gateway. Validate the actual
  fixture listen address through the production explicit-loopback parser and
  keep controller and external fixture ports disjoint. Caddy-admin proof alone
  does not complete RUN-09.
- Inspect actual owned running frontend/backend containers for host bind or
  Docker socket mounts and host namespace modes. Legitimate owned tmpfs remains
  allowed; existing secret scope and hardening attestation remain in force.
- Use fixed read-only paths, bounded requests and controlled addresses; bypass
  proxies and redirects. Capture only safe probe result metadata, without
  application configuration, credentials or arbitrary response bodies.
- Preserve existing route/ownership attestation, app-scoped network identities,
  positive route coverage, exact named CI pass discovery and owned cleanup.
  A reachable forbidden target must fail the gate and trigger investigation;
  do not suppress it or weaken production checks to obtain a pass.

## Implementation

`TestLiveGeneratedBlueGreenLifecycle` now attests the two actual owned,
healthy application containers and their distinct bridges. It pairs successful
own private-IP and identical-alias requests with denied cross-app private-IP
requests in both directions. Qualified other-network aliases are supplementary
DNS controls. Both applications must reach a host-side HTTPS fixture using
the fixture CA and verified peer name. Both app-network attachments to the
same attested Caddy are checked for private admin denial, paired with its
successful localhost admin request. Fresh public route checks follow these
probes. The suite repeats after the first application's green replacement.

`TestLiveControllerGeneratedDeploymentJourney` now pairs an authenticated
read-only request to its actual hostd API with frontend- and API-origin denial
at the attested app bridge gateway and actual controller port. It validates
the fixture's explicit loopback listener through the production configuration
parser, excludes external fixture ports, and repeats after durable controller
reopen. Actual container inspection strengthens the existing scoped-secret
checks with owned network, namespace and mount checks.

The HTTP probes report received response headers immediately for denied
targets: any HTTP status is reachable. An expected-body positive retains that
received status if its body later stalls or fails. The mandatory Docker
lifecycle test also exercises the exact Node script against 403 and 401
listeners that send headers and deliberately leave their response unfinished,
plus a request whose socket never connects. The probes use a wall-clock
deadline covering connection setup as well as response time.
Execution errors, parent deadlines, truncated output, malformed/trailing JSON,
unknown fields and unrecognized transport errors fail qualification.

These additions are test-only. Existing production routes, runtime limits,
accepted pins, schema and immutable history are unchanged. The existing CI
workflows already require exact named pass events for both live journeys and
complete Docker cleanup; their triggers and pass/skip guards are retained.

## Local verification

After the probe corrections, with `GOFLAGS=-mod=readonly` and a temporary
Go cache on Windows:

```text
go test -p 1 -count=1 -timeout=5m ./internal/generatedingress ./internal/generatedruntime
go vet ./internal/generatedingress ./internal/generatedruntime
pwsh -NoProfile -File scripts/check-generation.ps1
```

All passed: ingress 3.374s, runtime 0.561s, generated OpenAPI/controller check
0.517s. `gofmt` and the owned ingress-file whitespace check passed. The parent
also extracted and executed the exact checked-in Node probe/control constants
locally against controlled loopback HTTP listeners; both stalled-header
controls passed. The updated control additionally passed a never-connected
socket and required a bounded `ETIMEDOUT` result. This verifies probe behavior;
Docker network isolation remains unaccepted. After the hosted TLS failure, the
focused native X.509 chain/peer-name check and the exact Node HTTPS probe against
the controlled fixture both passed with verification enabled.

The complete local delivery checks also passed on the stable source:

```text
go test -p 1 -count=1 -timeout=20m ./...
go vet ./...
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
pwsh -NoProfile -File scripts/check-windows-controller.ps1
go test -p 1 -count=1 -json -tags live_docker ./cmd/hostd -run '^(TestControllerJourneyStageSource|TestControllerJourneyControllerProbeTargetUsesLoopbackControllerPort|TestControllerJourneyAppControllerProbeClassifierFailsClosed|TestControllerJourneyAppProbeAttestationBoundaries)$'
go test -p 1 -count=1 -tags live_docker ./cmd/hostd -run '^$'
go vet -tags live_docker ./cmd/hostd
pnpm --dir docs build
pnpm --dir docs check:accessibility
pnpm --dir docs check:workflow
```

The parent required exactly one named JSON pass for each of the four tagged
helper tests. Tagged compilation passed in 0.538s. The Windows script required
its actual DPAPI, session, secure temporary storage, process-tree and short-path
release passes. Aggregate `gofmt -l` and `git diff --check` were clean. Frontend
source, manifests, lockfiles, generated contracts and workflow files did not
change; their prior baseline verification remains separate from these results.

Verification used normal Windows filesystem/process permissions, a temporary
Go cache, lockfile-respecting existing dependencies and process-only Git trust
for this exact checkout. No VCS stamping or safety check was disabled. Local
Go is 1.27.0; hosted workflow Go is pinned to 1.26.7.

During aggregate inspection the parent corrected two acceptance hazards:
HTTP headers followed by a stalled body could have been mistaken for denial,
and abbreviated `docker ps` IDs could not match full inspected container IDs.
The final candidate records HTTP reachability at headers and discovers
canonical full Docker IDs. Namespace, mount, identity, result-parser and
certificate boundaries have focused coverage.

The independent security specialist found no actionable source issue after
these corrections. The `code-review` skill's local CodeRabbit CLI attempt
stopped at `not_authenticated`; no automated CodeRabbit review or login ran.
The skill prohibits a manual CodeRabbit fallback. That local automated review
is unrun. CodeRabbit's hosted status reported success because it skipped this
draft PR; its bot comment confirms no source review occurred.
No production-readiness or merge approval follows from this local record.

## First hosted result and remaining work

The first hosted Docker gate for draft PR 80 at `e8824130c97113f5595378d916a4a2611bcfb52d`
ran on 2026-09-27. Its controller journey, gateway-readiness and external
database/HTTPS checks passed. The generated blue-green lifecycle failed at its
new application-origin probe with the safe diagnostic "probe did not complete";
the job's complete Docker cleanup step passed. This is a failed RUN-09 gate,
not an accepted isolation result. The new probe had used Node's request socket
timeout, which does not bound an unconnected socket. Revision
`558bf7d2edab289d642d51931a606122bf6dbb25` used an explicit wall-clock
request deadline and labeled execution failures without printing command
output or secrets. Its 2026-09-27 hosted Docker job again failed the blue-green
lifecycle, this time at "invalid or unknown result"; the external TLS fixture
and complete Docker cleanup steps passed. That result is distinct from an
execution timeout and remains unaccepted. The next revision adds bounded,
sanitized classification of an invalid result and its static probe stage so
the actual cause can be identified without exposing response bodies or
credentials. It does not broaden the accepted transport outcomes. At revision
`dad04b920f62e17daa29fd3592bdbb93547a996f`, the third hosted Docker run
identified the first application's external HTTPS probe and Node's
`DEPTH_ZERO_SELF_SIGNED_CERT` transport code; external fixture and complete
Docker cleanup steps again passed. The controlled CA and leaf had both been
created with empty distinguished names. The candidate correction gives them
distinct subjects while keeping the fixture CA, SNI peer name and certificate
verification required. Native Go and exact Node probe controls passed locally;
the hosted application-origin result is pending.

Successful Docker and Linux race qualification require hosted CI here.
Windows has CGO disabled, so local race execution is unavailable and was not
claimed. The live Docker tests are opt-in; their ordinary native-suite skips
do not establish RUN-09. Publication of PR 80 was explicitly authorized; a
different branch requires separate authorization.

No production network/firewall change, schema migration, history rewrite,
managed database or external database provisioning is part of the initial
qualification scope. A real reachable forbidden target would require a
separately reviewed implementation decision. All external databases remain
application-owned and configured through scoped runtime secrets.

The controlled HTTPS listener is in the disposable Linux host namespace,
outside either app's container network namespace. Successful access proves
that bridge isolation permits this outward path. It does not prove public
Internet routing, a particular external provider, or LAN deployment. Existing
controller evidence uses application-owned PostgreSQL and HTTPS fixtures.
Successful Docker and Linux race qualification for this candidate remain
pending.

Other M2 acceptance remains incomplete: the live user GitHub walkthrough,
approved migration and uncertainty boundaries, external-service outage during
replacement, actual process kills at build/route/drain/finalization, and a
tested integration of the unmerged milestone dependencies. A local unit test
or successful prior-head journey does not close these items.

## Rollback

Revert the test changes through the PR path. No production data migration,
runtime configuration or resource deletion is required. Preserve failing
hosted evidence and investigate any reachable forbidden target before
changing a production networking decision.
