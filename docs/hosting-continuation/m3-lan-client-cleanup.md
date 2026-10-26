# LAN probe cleanup before controller restart

This focused test-fixture repair is based on draft PR #253 at
`89fed8965a8768e4cbed64a3889bf6eaf2657ad0`. Hosted behavior remains pending.

## Failure and correction

PR #249's hosted two-app LAN journey failed during its fresh controller restart
with `cold_group_network_correspondence` after 277.26 seconds. The test had already
run two independent transport probes, each creating a bridge labelled
`io.rig.managed=lan-test-client`. Their network cleanup was registered only with
`t.Cleanup`, so those bridges remained until the parent test finished.

The startup census includes objects carrying Rig labels. These temporary client
bridges have no corresponding application, builder or gateway ownership record.
Source inspection establishes that they reach the reported correspondence refusal
while they remain present. The historical log contains only the closed diagnostic
reason and does not identify the offending network.

`lanTwoAppNamespaceProbe` now invokes its existing cleanup when the helper returns,
while retaining the test cleanup callback as a fallback. The cleanup still checks
the exact operation ID, ownership labels, captured network ID and network name;
it retains uncertain resources and reports errors. Its bounded independent
context still permits cleanup after request cancellation. Deferred cleanup also
runs when the helper fails with `t.Fatal`.

The production census, network labels, application behavior and hosted test
assertions are unchanged. The correction changes only when the existing guarded
fixture cleanup runs.

## Verification

Local verification on 26 October 2026 passed:

| Check | Result |
| --- | --- |
| `go test -tags live_docker -c -o <external evidence path> ./cmd/hostd` | Fresh binary compiled |
| Binary `-test.list=^TestLiveControllerTwoAppLANJourney$` | Exactly one matching parent discovered |
| `go vet -tags live_docker ./cmd/hostd` | Passed |
| `gofmt cmd/hostd/hosting_lan_two_app_live_test.go` | Output matched source after CRLF-to-LF normalization |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| Owned-path `git diff --check` and before/after source hashes | Passed |
| Cleanup body comparison | All 44 body lines preserved |

The checks used Go 1.26.7 with `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`.
The source freeze SHA-256 is
`f4f543286b71d41db9527b774447091aa5285570f270e45d7c158a05c9687d2d`;
the fresh binary SHA-256 is
`e43c8e63acaa291c9ee8cfbbb2aba4d2b4418407be2937f44f69c82205cbded3`.
Versioned commands, raw output and verification receipts are in the task's
`pr253-lan-client-cleanup-20261026/v1` evidence directory.

No local Docker execution is claimed. The required hosted behavior check is the existing
`TestLiveControllerTwoAppLANJourney` parent in `hosting-lan-ci.yml`, which runs:

```sh
go test -tags=live_docker -json -v -count=1 -timeout=66m ./cmd/hostd -run '^TestLiveControllerTwoAppLANJourney$'
```

Its prerequisites and disposable-resource authorization remain configured by that
workflow. A successful compile or discovered test name does not establish that the
Docker journey passed. Preserve its complete run result and cleanup evidence.

The source finding is recorded in the task's
`pr253-lan-client-network-cold-census-source-finding-20261026.md`, SHA-256
`7f2b9cafa3a3f1ee3fc824c3bd36e0159acbe27001d70e64cd175eb028284cac`.
The historical failure is PR #249 job `121559600019`, raw-log SHA-256
`fd4e6dbe6834ee4c6d46a45686c2659ff00700c1b029118997b53b91213bbf92`.

## Rollback

Revert this test-only change if needed. This restores end-of-test cleanup timing;
it does not alter production runtime behavior or persisted history.
