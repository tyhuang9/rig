# M3 LAN publication design and verification plan

Status: implementation plan, 2026-09-29. Base: M2 draft PR #84 at
`1063c3c15fcb612fd3e75156596337f5d6422fb1`. This branch is local and
unpublished. No LAN listener or firewall rule is enabled by this document.

## Branch purpose and invariants

Add explicit, durable per-application LAN access through a bounded Caddy
listener pool while preserving the existing `.rig.localhost` route. Application
containers remain on private Docker networks; only the attested gateway owns
host-published ports. External databases remain application-owned and enter
the server only through scoped runtime secrets.

- An administrator explicitly approves the exact host interface, IPv4 address,
  port pool, and gateway upgrade action. A separate action approves each app's
  LAN exposure. Stale revision, changed digest, replay with changed input, or
  a non-administrator fails without a bind or route change.
- Port ownership is durable and unique across apps. `reserved` belongs to one
  operation; `active` or `uncertain` is never offered to another app. A failed
  operation releases only its own reservation after proving that no LAN route
  or listener can still serve it.
- M3 accepts only private RFC 1918 IPv4 addresses for LAN profiles. The
  controller must also prove the selected interface owns the address before
  binding or reporting it. An app with an unreleased allocation cannot be
  archived until a later disable flow has removed and attested its route.
- Existing local routes, committed serving heads, immutable deployment/release
  history, and v1 gateway state survive a failed gateway upgrade. An outcome
  that cannot be proved enters durable uncertainty and withholds the LAN URL.
- A LAN port serves its assigned app only. Client `Host` must never select a
  different app. An unassigned listener returns no app content. No listener
  forwards to Rig's controller or Caddy admin; automatic HTTPS is disabled for
  LAN HTTP. The existing local-route attestation remains separate.
- There is no wildcard host binding, automatic interface fallback, IPv6 LAN
  exposure, firewall change, per-app gateway, or per-app published container
  port. A displayed URL requires fresh binding, route, access-revision, and
  serving-deployment attestation.

## Delivery units

1. **Allocator foundation:** mirrored SQLite migration, append-only approved
   gateway/access revisions, CAS heads, and unique port ownership. Unit and
   migrated-database tests cover concurrency, replay, exhaustion, failure
   cleanup, and uncertainty. This unit cannot publish a listener.
2. **Gateway v2:** explicit maintenance operation and protected migration
   journal. Stage a hardened v2 Caddy with the exact selected-IP pool and
   empty 404 listeners while v1 retains loopback service; prove actual Docker
   binds. Transfer loopback to a final v2 container under one ingress mutex.
   Retain v1 state and container for verified rollback. Fail closed if either
   v2 commit or rollback is uncertain. Startup never auto-upgrades v1.
3. **App access action and observation:** transactionally reserve one port,
   reload one aggregate config, attest the assigned route, then mark the
   access revision active. Disabling first attests 404, then frees the port.
   Deployment switches update local and LAN routes in the same reload.
   Authenticated no-store API returns a LAN URL only with fresh provenance.
4. **Operator UI and diagnostics:** explicit Local only / Share on LAN choice,
   selected interface and URL, conflict/exhaustion/unavailable states,
   deliberate rebind guidance, and copyable address. No secret values enter
   the browser read model.

The v2 container must retain the pinned image, user, capabilities, read-only
root, resource limits, owned network, and exact-port drift checks. Its Caddy
config uses a separate server per LAN port so a forged `Host` cannot select a
different app. Assigned ports accept the approved LAN IPv4 as `Host` and return
404 for any other authority; the live route matrix must prove this policy. A
persistent, owned
gateway `/data` volume should be introduced with the v2 identity so later
certificate work does not require another data-layout migration; M3 must not
publish public 80/443 or issue certificates.

## Verification plan

- **Fast inner loop:** migrated SQLite repository tests, Caddy config unit
  tests, and focused controller authorization/attestation tests after each
  unit. Normal `go test ./...`, web tests/build, OpenAPI/migration mirror, and
  embedded-asset checks before PR readiness.
- **Hosted Docker:** actual bind-collision and rollback tests; v1→v2 upgrade
  with controller restart; two app/port isolation across deployment switch;
  unassigned 404; wrong `Host`; absent controller/admin access; selected-IP
  binding drift; interface disappearance; all owned resource cleanup. A
  network-namespace client exercises HTTP, SPA assets, `/api` paths, uploads,
  SSE, WebSocket, queries, headers, and request bodies.
- **Physical gate:** from a phone or laptop on the same LAN, use the displayed
  URL to create/read a note in the external database. Verify both apps remain
  stable across redeployment and controller restart. Record platform, Docker
  version, interface/address, exact Rig/source and plan/config/deployment
  identities, test result, and cleanup without credentials. A namespace test
  does not substitute for this device test.

Docker documents exact `HOST_IP:HOST_PORT:CONTAINER_PORT` publication, while
also warning that routing can make a specifically published IP reachable by
other paths on some Engine configurations. Docker Desktop forwards inbound
connections through its backend. The selected IP is therefore an exact bind
and advertised-address invariant, not a promise of strict NIC isolation.
Physical routing and firewall behavior must be observed on each qualified
platform. Sources: [port publishing](https://docs.docker.com/engine/network/port-publishing/),
[Docker Desktop networking](https://docs.docker.com/desktop/features/networking/networking-how-tos/).
