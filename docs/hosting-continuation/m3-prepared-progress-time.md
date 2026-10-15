# M3 prepared progress timestamp ordering

Status: deterministic defect reproduced and repaired; Windows and Linux scoped
tests, CLI builds and static checks passed. Replacement hosted acceptance is
pending. M3 remains open.
Base: supported-controller PR #154 at `2a0674beea2b90ba0320ace994da8ef7f747bb1c`.

## Observed failure and reproduced defect

PR #156's [Windows recovery-guards job](https://github.com/tyhuang9/rig/actions/runs/38657655912/job/116171144464)
failed inside the shared fixture for
`TestGatewayRebindInteractiveSessionGuardsCurrentAttestation/default_nil` before
the session guard under test ran. The fixture expected a retained database commit
but had discarded the original commit error and reported only a nil snapshot
error. Other cases passed. Log SHA-256:
`45c088a220a4b95b72e63645f63711da627934c9715156cbf70ff43fddbc2605`.
The same case passed locally in 27.88 seconds, so a blind rerun does not establish
the cause or a repair.

Inspection found an independently reproducible defect: SQL claim creation and
first protected progress construction read the wall clock consecutively. If those
reads are equal, the correctly strict installer rejects the new progress time.
The existing after-claim test hook pins the candidate time to the real SQL claim's
`CreatedAt`, without sleeps or replacing the repository. Before the production
repair, `TestGatewayRebindPreparedProgressTimeFollowsSQLClaim/private/equal`
failed with `route_reconciliation_required` and identical claim/candidate times.
Red log SHA-256:
`662926ae9f2167f03ce8da65c9f810c9a50ee66c8b30ca53d16b259d1c115151`.

This proves the equal-clock defect. The original hosted log does not prove that
it failed for this reason. Shared fixture diagnostics now retain the original
commit error, SQL phase/fence, driver call counts and protected-history counts,
so another failure can be investigated directly.

## Repair and invariants

At the three construction callers (private preparation, strict admission and
prepared recovery), use the existing `gatewayRebindTimeStrictlyAfter` helper to
construct first progress after the validated retained SQL claim. An equal or
backward clock yields `CreatedAt + 1ns`; an already-later clock is preserved.
This is logical record ordering, not a replacement for authorization or proof.

- Keep the installer's strict `occurredAt.After(claim.CreatedAt)` validation.
- Keep all session, approval, claim, physical proof, roster and runtime-head gates.
- Never alter the SQL claim or previously persisted protected records.
- Exact replay returns saved timestamps/digests without consulting the clock or
  physical proof reader and without external effects.
- Do not add sleeps, retries, weaker validators or longer workflow deadlines.

Production source SHA-256:
`ccc93c2ce53d605ac47457af5065a336386e7116a4572d4866fdcc7f501d3dd7`.

## Executable verification

Evidence is retained in `temp/m3-retained-commit-20261015/` in the enclosing
workspace. Nine real-SQL cases cover each of the three construction paths with
equal, later and backward clock readings. They verify exact first-record time,
retained SQL/fence/current authority, predecessor history, strict refusal of
explicit unordered installer input, and immutable recovery replay.

Run the new parent together with all five recovery guard parents:

```sh
go test -count=1 -timeout=10m ./internal/generatedingress -run '^TestGatewayRebind(PreparedProgressTimeFollowsSQLClaim|ForwardConfirmation.*|Interactive.*|RollbackConfirmation.*)$'
```

The local runner compiles/discovers exact Windows and Linux binaries, executes
the complete selected parents with JSON output, and requires matching RUN/PASS
sets without failures or skips. Linux executes through WSL with race detection
disabled; no Linux Go/C compiler was available on PATH. Hosted race
verification remains required. Source/binary/log hashes and exact commands are
recorded in `time-green-*.result.json`.

Linux passed all six parents and 29 subcases in 42.565 seconds. Log SHA-256:
`ed327fdf229f552de48e032d1bc0a84961aadca397796fe0ce7ec90679857a4b`.
Windows passed the same six parents and 29 subcases in 357.203 seconds. Log SHA-256:
`d82ce801dcb235e8b65c5ac7892f75a3f3517623e19fa9b3b2c424a91735ec2f`.
Both runs had zero failures or skips and retained matching source hashes.

Both Windows and Linux targets passed these checks with Go 1.26.7,
`CGO_ENABLED=0`, and `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`:

```sh
go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go vet ./cmd/hostd ./internal/controller ./internal/generatedingress
```

Exact commands, source pins and all four successful steps are recorded in
`final-cli.result.json`. Documentation verification uses the frozen offline
install followed by `check:workflow`, `build` and `check:accessibility`; its
per-step results and source pins are retained in `docs-final.result.json`.
The local documentation toolchain is Node 24.15 / pnpm 11.14; hosted CI uses
Node 24.19 / pnpm 11.22. Code and security review approved the bounded
construction-only change.

## Rollback and remaining gates

Reverting the construction helper and its three callers restores the old behavior
without modifying stored history or schema, but also restores the reproduced
equal/backward-clock rejection. Do not delete claims or protected records to clear
that failure. Retain diagnostics and regression evidence when investigating it.
Current-head Windows/race, supported-controller Docker and expanded LAN transport
acceptance remain required. Physical second-device and application-owned external
database acceptance are separate. No GitHub merge, deployment or managed database
provisioning is part of this repair.
