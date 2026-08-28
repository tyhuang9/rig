# Milestone 1 — Foundation with a real local control plane

- [x] Establish repository layout, toolchain metadata, durable architecture and design documentation.
- [x] Add SQLite migration and repositories for the Phase A control-plane records.
- [x] Implement authenticated API shell, bootstrap, sessions, CSRF, diagnostics, and API contract.
- [x] Implement durable serialized fake-runtime jobs, events, replay, cancellation, and restart recovery.
- [x] Implement the embedded React dashboard and CLI.
- [x] Add fixtures, scripts, unit/API/frontend tests and run static verification.
- [x] Capture browser screenshots and complete full production process/e2e verification.

## GitHub-connected controller deployment

- [x] Add atomic cancellable job execution and preserve fake-runtime parity.
- [x] Add protected GitHub App device-flow connections and live repository discovery.
- [x] Preserve local sources while adding typed GitHub sources and an accessible source wizard.
- [x] Materialize bounded immutable commit snapshots without requiring Git or a user checkout.
- [x] Add versioned visible/secret configuration and immutable release provenance.
- [x] Add the opt-in controller-local Compose runtime, policy findings, exact approvals, health waiting, and explicit prior-release recovery.
- [x] Add the separately deployable PostgreSQL-backed relay and versioned authenticated WSS protocol.
- [x] Add latest-head automatic deployment, reconciliation, coalescing, and pause/resume behavior.
- [x] Add deployment, approval, auto-deploy, and relay-management dashboard experiences.
- [x] Add hosted deterministic Chromium execution for the embedded hostd and GitHub source-wizard flows.
- [x] Publish the implementation as a strictly ordered stack of narrow pull requests without merging them.

## External promotion gates

- [ ] Exercise the private-repository, live GitHub App, deployed relay, and offline latest-head acceptance flow in staging.
- [ ] Exercise live Docker Compose mutation, health failure, approval/resume, cancellation, crash recovery, and prior-release recovery on disposable Linux and Windows hosts.
- [ ] Exercise live PostgreSQL restore, TLS/SNI proxying, relay recovery/redelivery, reconnect/load behavior, and alert routing beyond hosted PostgreSQL integration tests.
- [ ] Run container hardening, multi-architecture image, SBOM, signature, and provenance checks beyond hosted Linux race, real-filesystem permission/no-follow, and native linux/amd64 image evidence.
- [ ] Run additional real-browser, screen-reader, and physical-device coverage beyond the hosted Chromium workflow.

## Explicitly deferred

- [ ] Caddy configuration, remote deployment agents, backup/restore product workflows, and movement workflows.
- [ ] GitHub Enterprise Server, pull-request refs, submodules, Git LFS expansion, and Git history.
