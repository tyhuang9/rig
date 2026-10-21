# M4 generated route contracts

## Scope

The generated ingress boundary now consumes data-only route contracts from
`internal/generatedroute`. `RouteEndpoint` and `RouteSwitchRequest` retain the
existing field names, Go types, and route semantics. `Slot` remains owned by
`internal/generatedidentity`; its blue and green values are used directly by
ingress state and validation code. The controller-owned network label is also
an identity constant in that package.

`internal/generatedruntime` keeps type aliases for `RouteEndpoint`,
`RouteSwitchRequest` and `Slot`, plus a compatibility constant for the network
label. Existing runtime callers
therefore retain source and type identity compatibility while ingress no longer
imports the runtime package for route DTOs. Capacity snapshots and sources are
consumed directly from `internal/generatedcapacity`.

## Non-goals and invariants

This extraction changes package ownership only. It does not change route
validation, state-machine transitions, Docker or Caddy effects, owner
construction, recovery authority, lock ordering, or persistence formats. The
runtime aliases are compatibility shims, not alternate contracts. Tests remain
with their owning packages so the existing route and runtime behavior checks
continue to exercise the same implementations.

External databases remain application-owned and configured through scoped
runtime secrets. No managed database or Neon provisioning is introduced.

## Verification

The owning integration lane should compare the pre-extraction and post-
extraction method bodies and run the existing generated ingress and generated
runtime selectors with pinned Go 1.26.7. The focused static checks for this
branch are:

- no production file under `internal/generatedingress` imports
  `internal/generatedruntime` for route or capacity contracts;
- `generatedruntime.RouteEndpoint` and `generatedruntime.RouteSwitchRequest`
  remain aliases of the new route package types;
- `generatedruntime.Slot` remains an alias of `generatedidentity.Slot`;
- the network ownership value has one identity-owned definition and a runtime
  compatibility alias.

Hosted Docker and race acceptance are still required before M4 completion.

## Executed local evidence — 2026-10-21

At code commit `be5ec5aaf32157074c4e3c10e24baec6773ee2ed`, pinned Go 1.26.7 passed:

| Selection | Windows | Native Linux (WSL) |
| --- | --- | --- |
| Ingress route/configuration/capacity/state | 80 parents / 226 cases | 80 parents / 226 cases |
| Shared identity | 1 parent / 1 case | 1 parent / 1 case |
| Generated runtime | 61 parents / 141 cases | 61 parents / 141 cases |

All selections had zero failures or skips and exact nonempty discovery.
Both platforms also passed ingress/controller compilation with `live_docker`,
scoped vet and controller builds. Live-tag compilation did not execute Docker.
`scripts/check-generation.ps1`, documentation workflow/build/accessibility,
gofmt and diff checks passed. Documentation dependencies were installed with
`pnpm --dir docs install --frozen-lockfile` using pnpm 11.22.0.

The harness preserved all existing ingress/runtime test declarations and
confirmed that ingress no longer imports generatedruntime. All source hashes
were unchanged during verification. The independent reviewer compared the
18 ingress files after explicit symbol substitutions and found no behavior
change; 224 existing test files and 1,155 top-level declarations were unchanged.

Local receipt: `m4-route-contracts-verification/20261021T005613Z/receipt.json`,
SHA-256 `eabe3394e0df8cad725cc01b87bc9a9a808b3c4b42609ca1597d3e5fac73ff6e`. Source review SHA-256 `6a0c72740197cd4e351fcee63e84e20e03235d857d6b780436c9388619adce70`.
This result section was added after the source-stable run.

Hosted Docker/race and CodeRabbit review remain separate gates. The local
CodeRabbit installation has no authenticated session; no local CodeRabbit
review is claimed. This extraction does not implement E5 publication,
retirement or E6 recovery and does not complete M4.

Rollback is a cohesive revert of the extraction and aliases; it changes no
database schema, protected receipt or deployment state.
