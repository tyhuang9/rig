# M3 LAN transport acceptance

Status: expanded transport harness passed local tests, build and compile checks;
hosted acceptance pending. M3 remains open.

## Purpose and boundaries

The M3 LAN design requires both assigned application ports to preserve request
bodies, queries, headers, uploads, WebSockets, incremental server-sent events and
built SPA assets from an independent client namespace. The existing two-app gate
covered basic routing and isolation but its namespace client made only GET
requests. Extend that existing gate before and after application A's redeploy.

The staged in-memory API and isolated client are shared fixture files, so focused
Node tests exercise the same code used by the Docker acceptance. The supported
rebind harness now loads that shared API fixture too. Reuse the
existing pinned `ws` dependency and Node 24 client image. Production routing,
authorization, configuration, dependencies and lockfiles remain unchanged.

Verify exact body bytes, encoded URL and repeated query values, repeated headers,
content type, length and digest; a deterministic multipart upload; WebSocket echo,
application marker and clean close; and SSE delivery using a correlated first-event
acknowledgment before releasing a second event, with the required media type and
named event retained. Deadlines and byte limits bound
failures. The SSE barrier distinguishes incremental delivery from a buffered final
response without fixed sleeps.

Retain wrong-Host, controller API, controller-local hostname, controller port,
unassigned port and private-network denials. Application B's note and container
identities must survive A's redeploy. Each namespace probe owns a separately
labelled client network, with exact identity checks before cleanup. The independent
workflow cleanup gate remains required.

This fixture has disposable in-memory notes. It does not establish external
database persistence. Real databases remain application-owned and configured
through scoped runtime secrets; no managed database or Neon provisioning is added.

## Baseline evidence

Base PR #154 head `897e7da4db2aa2747b5c289a402bbe7cd3248bc1` passed the existing
[two-app LAN job](https://github.com/tyhuang9/rig/actions/runs/38650368123/job/116147365038).
`TestLiveControllerTwoAppLANJourney` had exactly one RUN/PASS (223.32 seconds), no
failure/skip, and successful independent Docker cleanup. Checkout
`98daa772538a18fee5f4b073e389e353b32315c0` has tree
`5ebf0f111ba3f872cfcda09c2e1803c84e1d9646`, identical to that PR head.
Raw log SHA-256:
`d0493e381859160372254ea5a0eb1e5c76ae25f0903403a3637af5cbaf6cc898`.
This is baseline evidence, not acceptance of the added assertions.

Evidence files are retained under `temp/m3-lan-transport-acceptance-20261014/`
in the enclosing workspace. Frozen offline fixture installation and the original
14 API tests passed before the change. The first focused execution exposed a
test-client deadline that started after response headers, allowing a buffered SSE
response to hang. That failed attempt was interrupted and retained in the work
record; it is not a pass.

## Verification and outstanding acceptance

| Check | Actual result | Log SHA-256 |
| --- | --- | --- |
| Focused shared fixtures, final | 7 tests passed; no failures, skips or cancellations | `393ff77d7c4ddc6ee3dc82a0a394b5fc8a3385f23934da849844aa2be77e58d0` |
| Full fixture suite, final | 21 tests passed; no failures, skips or cancellations | `02c16c2931dac8fef696ab52f8813b9a605089375bc38e3474481b2b1816ca4b` |
| Fixture production build | Passed | `d439d7f7b1c0ccb7b8a93e473372d18ce7c6ac874e027994cc86f820433882ee` |
| Go formatting, live-tag Windows/Linux test compilation, scoped live-tag vet | All passed | Empty success logs; commands and source pins in `verification-go-final.result.json` |

Final Node checks cover success, changed markers/digests, rejected upgrades,
malformed WebSocket messages/echoes, buffered headers, oversized HTTP/SSE bodies,
malformed multipart uploads, wrong SSE media type/event name, disconnects and
resource cleanup. The original fixture tests also remain discovered.
The exact `node -e <source> -- --rig-lan-transport-client ...` launch form was
executed against a closed ephemeral local port; it failed with the expected
connection refusal, proving argument dispatch rather than an unsupported option.

Commands were `node --test --test-timeout=10000 test/lan-transport.test.js` in the
API directory, `pnpm --dir examples/hosting-notes test`, and
`pnpm --dir examples/hosting-notes build`. Go used `-tags=live_docker` for
`go test -c` (Windows and Linux targets) and `go vet ./cmd/hostd`.
Receipts retain exact arguments and source hashes. The Go compile/vet checks and
frontend build preceded the final JS-only SSE assertion fix; their Go/frontend
inputs did not change. Node tests ran again on the final shared fixtures.
Documentation uses frozen offline installation, workflow, build and accessibility
checks, recorded in `docs-final.result.json`.

The initial compile exposed another reference to the extracted inline API in the
supported-rebind fixture. It now loads the same shared server; successful compile
receipts follow that correction. Review identified the missing SSE media-type and
event-name assertions; the final negative tests cover both. Earlier failed
format/compile receipts remain retained. Local Node 24.15 and pnpm 11.14 differ
from the hosted pinned versions. Local compile or opt-in skips cannot establish
actual Docker acceptance; Docker was not available in this Windows environment.

The unchanged hosted gate must run with `RIG_RUN_LIVE_TWO_APP_LAN=1`:

```sh
go test -tags=live_docker -json -v -count=1 -timeout=66m ./cmd/hostd -run '^TestLiveControllerTwoAppLANJourney$'
```

It retains its 72-minute step and 85-minute job deadlines, exact parent pass
requirement and independent complete-cleanup check. Physical second-device LAN,
external database and full supported-controller crash/recovery acceptance remain
separate requirements.

## Rollback

Revert the shared fixtures, their focused tests and the live-test extraction
together to restore the prior acceptance harness. No production state, schema,
runtime safeguard or immutable operation history is changed.
