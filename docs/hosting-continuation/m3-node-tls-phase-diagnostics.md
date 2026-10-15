# M3 host-side Node TLS phase diagnostics

Status: diagnostic candidate; the retained Windows timeout remains unresolved.
This independent branch starts from PR #160 at
`950944edc9e028ce407060e4e726521876ae1c14`. Both affected test-source blobs are
identical at that base and PR #161's observed `5ecefd7` candidate.

## Observed failure

[Windows ingress job 116413636281](https://github.com/tyhuang9/rig/actions/runs/38732307211/job/116413636281)
failed `TestLiveExternalTLSNodeTrust` after 12.46s. It reported
`process=exit-nonzero,parent_context=probe-completion-deadline,node_context=startup,node_major=22`.
The actual checkout tree matched `5ecefd7`. The verification aggregation job
failed because that partition failed; it was not a separate implementation
failure. Raw ingress log SHA-256:
`0d64ad14367ca0c0e49609198de6597fa63f8c8eaabd7faba5f9052921093703`.
Evidence is retained in wrapper
`temp/m3-lan-external-db-20261015/pr161-successor-fixture-ci-evidence/116413636281/`.

A complete, valid Node startup record was observed. The five-second completion
allowance then expired and the parent attempted to kill and reap the process.
The nonzero exit is compatible with that parent action; it does not prove an
independent Node crash. No request-created phase, TLS rejection or response was
observed. Existing supervision controls passed in that job.

The previous trace had one interval spanning HTTPS module loading, argument/CA
preparation and synchronous request construction. Its 1.5-second request timer
is installed after construction. This evidence does not identify which part
stalled or establish a certificate-validation defect.

## Narrow diagnostic change

Add closed phase markers after HTTPS module loading and immediately before
request construction. Extend strict accepted sequences and parser controls so
the next failure can identify that interval more precisely. Preserve these
invariants:

- Existing 15-second startup, five-second completion, 1.5-second request and
  two-second kill/reap allowances, including the existing startup signal.
- Certificate authority, peer name, `rejectUnauthorized: true`, response
  assertions, stdout JSON contract and behavior when diagnostics are disabled.
- The 512-byte capture bound and rejection of malformed, reordered, partial,
  unknown or truncated traces. Only closed categories and a validated Node
  major version may be reported; no raw stderr, certificate or endpoint data.
- The same hosted Windows ingress partition and Docker probe coverage.

The expanded longest valid sequence has nine phases. Controls must prove that
it fits within the existing byte bound even with the maximum accepted major
version, and that all valid prefixes retain their exact phase category.

## Baseline and acceptance limits

Before implementation, seven diagnostic/certificate parents passed on Windows
and WSL Linux with exact discovery, uncached execution, no skips and stable
source hashes. The actual Windows host-side Node TLS test also passed locally
using Node `v24.15.0`; the failed hosted probe reported Node major `22`.
That different local runtime and successful run do not explain or invalidate
the retained hosted failure.
Baseline logs and source pins are in wrapper
`temp/m3-lan-external-db-20261015/node-tls-phase-verification/`.

The post-change local results and hosted revision must be evaluated separately.
Local Docker/race execution is unavailable, and WSL has no Node. M3's full
supported-controller matrix and physical second-device qualification remain
incomplete. The running controller has not been restarted or deployed.

## Rollback

A reviewed revert removes these additional test diagnostics. No production
source, data, migration, secret scope or runtime authority changes are included.


## Verified local candidate

Post-change Windows and Linux checks each passed exactly nine parents and
23 subcases, with no skips and stable source
hashes. They cover the diagnostic parser, byte bound, startup signal, process
supervision and certificate contracts. The actual Windows Node TLS test also
passed separately using Node `v24.15.0`.

| Scope | Parents | Subcases | Package seconds |
| --- | ---: | ---: | ---: |
| `final-native` | 9 | 23 | 0.240 |
| `final-wsl` | 9 | 23 | 0.405 |
| `final-node` | 1 | 0 | 0.112 |

Verification compiles with `go test -tags live_docker -c`, explicitly discovers
each recorded roster, and runs through `go tool test2json` using
`-test.count=1 -test.timeout=10m`. Both CLI builds and package vet passed, along
with canonical `pwsh -NoProfile -File scripts/check-generation.ps1`, formatting
and whitespace checks. Final docs workflow, build and accessibility receipts
are retained with publication evidence. Broad unrelated suites were not rerun.

The aggregate source comparison removes only the two new closed diagnostic
calls and then requires the entire probe-containing source file to equal the
base version. This confirms no other probe, timing or TLS assertion edits.
The strict parser also tests every valid sequence prefix and the longest
nine-phase trace with Node major `65535` against the unchanged 512-byte buffer.

- `final-native` raw log SHA-256: `405781bf3cc1351dbee986e1fb4abce40fce3398c81ba434a495df59f1063969`.

- `final-wsl` raw log SHA-256: `e1b1c598108dcc558c02c9c48ec9c876e6afbf5c1e547c682dc41feb50d90484`.

- `final-node` raw log SHA-256: `263f44e9dee52c3fa8379f0f2ad6c1c84bb4a72b6ca1bd1a90d1fd737d461600`.


The original hosted failure remains unresolved. These local results do not
establish the behavior of the hosted Node 22 environment or Docker/race gates.
The published revision must run the existing Windows ingress partition and
preserved hosted probe coverage before acceptance can be claimed.
