# M4 default daemon runner identity

## Defect and repair

At published baseline `445dccfb0265fb139858cb41d85218fc8bbc08d3`, daemon assembly
supplied `runtimeprocess.ExecRunner{}` by value. Generated candidate authority
requires a retained, non-nil pointer runner. Runtime composition only supplied
its pointer default when no runner was passed, so the daemon's non-nil value
reached the real engine constructor and was rejected.

The daemon now normalizes an omitted runner once to `&runtimeprocess.ExecRunner{}`
through a package-private helper used by `runServerWithAssembly`. Supplied
runners and all other assembly fields are preserved. Downstream authority
checks, Docker requests, configuration and recovery behavior are unchanged.

The regression composes the real generated-runtime graph using a migrated
database, protected stores and the secure environment stager. It covers the
daemon default and an injected runner. The existing test-only ingress recovery
seam avoids Docker execution; this test establishes construction, not physical
gateway recovery or complete daemon startup.

## Executed verification — 2026-10-21

Pinned Go 1.26.7, readonly modules, CGO disabled and serial execution:

- A temporary Go build overlay restored the former value default without
  modifying repository source. The default subcase failed inside real engine
  construction with `deployment execution authority configuration is invalid`;
  the injected-runner subcase passed. This is the expected regression result.
- With the repair, all 16 controller-composition parent tests / 75 cases passed
  on Windows and native Linux with `-count=1`, exact discovery and zero skips.
- Both targets passed live-tag controller compilation, controller vet and
  controller builds. No Docker journey was executed locally.
- `pwsh -NoProfile -File scripts/check-generation.ps1`, formatting and diff
  checks passed. Source hashes were unchanged throughout verification.
- Independent two-file source review found no remaining issue and confirmed
  that supplied runner identity and the downstream pointer guard are preserved.

Receipt:
`temp/m3-lan-external-db-20261015/m4-daemon-runner-identity-verification/20261021T015759Z/receipt.json`.
SHA-256: `bb61eb03ea354a95252b2e84e9269f89fa7de5739244c1d9afd8a7698e919b75`.
Source review SHA-256:
`3322f15b7f8965d73e486e6cd115eb33c287c8a392626945ffe82610cda04e6a`.

Hosted Docker/race checks and hosted review remain separate. This correction
does not establish M4 completion or full default-daemon startup acceptance.
External databases remain application-owned through scoped secrets; no managed
database or Neon provisioning is introduced.

## Rollback

Reverting the helper and its call restores the former default-runner rejection.
There is no schema or durable-state change to reverse, and no running daemon
was changed during verification.
