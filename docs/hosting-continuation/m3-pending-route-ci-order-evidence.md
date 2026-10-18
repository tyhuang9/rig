# M3 pending-route CI order evidence

Date: 2026-10-18

## Change

`scenario/pending-route-restart` now runs first in `partition-06`. The other
seven partition-06 cases retain their prior relative order. All eight groups,
the 63-case membership, group-size limits, runner controls, workflow deadlines,
and cleanup behavior are unchanged.

## Reason and acceptance effect

Hosted run `39103009798`, job `117610734612`, failed
`scenario/pending-route-restart` at published head
`9cdbd109cc3aeceab42a92d596eff21bead6451d` after the preceding partition-06
work. Moving this existing case first shortens the path to repeat diagnostic
feedback. It is a scheduling change only; it is not a runtime fix.

The matrix runner stops a partition at its first failure. If pending-route fails
first, the remaining seven partition-06 cases are unexecuted in that attempt
but remain required. Success still requires all 63 exact cases, canonical event
verification, independent fixture cleanup, and the complete eight-partition
aggregate.

## Verification record

Base contract SHA-256: `b2d92f994d9bc9ac849d9c524eaa82e7165bc513dbb615cd306a6933e007a82d`.

2026-10-18 local controls completed successfully:

| Environment | Command | Result |
| --- | --- | --- |
| Windows | `C:/Users/huang/AppData/Local/Programs/Python/Python312/python.exe -X utf8 -B scripts/check-supported-rebind-matrix.py --check-contract` | exit 0 |
| Windows | `C:/Users/huang/AppData/Local/Programs/Python/Python312/python.exe -X utf8 -B scripts/check-supported-rebind-matrix.py --self-test` | exit 0 |
| WSL | `python3 -B scripts/check-supported-rebind-matrix.py --check-contract` | exit 0 |
| WSL | `python3 -B scripts/check-supported-rebind-matrix.py --self-test` | exit 0 |

An in-memory comparison against `git show HEAD:scripts/check-supported-rebind-matrix-contract.json` verified the exact partition-06 permutation, eight unchanged group IDs, 63 unique cases, and maximum group size eight.

| Source | SHA-256 |
| --- | --- |
| Post-change contract | `4437b3a25bcf5ddec73ea6611ec5ef28e6b9c97c378074af33f05856d3d210c0` |
| Contract checker | `6555bc3e3029629c41a34e2ff269df0a8a14159341e6014964a5ab7b4f0731e5` |
| Live manifest | `30a7221b1abad076b9c764b1d27be311f96d5fb2309315e572a981a90f75642d` |
| CI workflow | `9567a9eba2ef6febc89ccdcb3590b44107910f05916ddaca991cedf44494b02f` |

The canonical generation gate also completed successfully with the pinned local
Go 1.26.7 toolchain and `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, and `CGO_ENABLED=0`:

```text
pwsh -NoProfile -File scripts/check-generation.ps1
ok  github.com/hostd/hostd/internal/controller  0.209s
```

`git diff --check` completed after this final evidence record.
