# M3 host Node TLS probe diagnostics

Status: focused native behavior, static checks, and scoped source/security review passed. Documentation receipt and final integration disposition are retained separately. This is test-only refusal diagnostics, not a TLS behavior fix.

## Observed failure

This separate local branch starts at published `457538ad08867c5e982de5a93f29a1402fe30bdf`. Hosted Windows job `114391210598` tested merge `07874a5040d60d0cb7b2e736d1ef450b6e5d805d`, whose tree matches that source. `TestLiveExternalTLSNodeTrust` failed in 5.03 seconds with the generic message that the host-side Node TLS probe did not complete.

The existing assertion combines process failure and parent-context cancellation. Its log does not establish whether Node entered JavaScript, reached the request, received a response, or reached the exit callback. The duration is consistent with the existing five-second parent budget but does not prove the cause. Raw evidence is retained at `temp/pr145-457-hosted-acceptance-20261010/job-114391210598-response.json`, SHA256 `C62912691D11905D9D930C299A5D67B44C3DB67D50350E0CA37F118DB05B4A1D`.

## Scope and invariants

The change adds opt-in finite stderr phase markers only to this host probe and bounded diagnostic parsing. The same Node process reports a validated numeric Node major version. Process outcome and parent-context state are separate categorical observations. Empty capture means only that JavaScript entry was not observed; malformed, truncated, and unknown records classify as unknown.

The TLS request, CA and peer-name verification, stdout result protocol, `agent: false`, connection-close header, explicit exit, 1.5-second request budget, and five-second parent budget remain unchanged. No additional process, retry, flush wait, Node pin, production code, or CI configuration is introduced. Logs exclude raw stderr, CA material, URLs, paths, environment values, and error text.

## Existing evidence and final verification

The prior bounded local two-parent run is retained at `temp/pr144-tls-reproduction-20261010/`. Its Go 1.26.7 / Node 24.15.0 pass did not reproduce the hosted failure. The hosted ingress job did not record its Node version, so that pass does not establish equivalent runtime conditions or a fixed failure.

The frozen modified source passed the following native Windows command with `GOTOOLCHAIN=go1.26.7` and `GOFLAGS=-mod=readonly -buildvcs=false`:

```powershell
go test -p=1 -json -v -count=1 -timeout=3m ./internal/generatedingress -run '^(TestLiveNodeTLSDiagnosticContext|TestLiveNodeTLSDiagnosticStderrBufferBoundsOutput|TestLiveNodeTLSDiagnosticProcessOutcome|TestLiveExternalTLSCertificateTrust|TestLiveExternalTLSNodeTrust)$'
```

All five expected parents and 19 leaf subcases ran and passed exactly once; there were no skips or failures. The package passed in 1.321 seconds, and the command returned exit 0. The Node trust parent executed rather than skipping. The JSON log is `temp/m3-node-tls-diagnostics-20261011/original-checkout-artifacts/five-parent-go1.26.7.jsonl`, SHA256 `3489200c432a1898245c0ef3eebdec35092dd06f3f871c7df3c6fe78b9275c6a`.

Scoped `go vet -p=1 ./internal/generatedingress`, Go 1.26.7 read-only `gofmt -d` on both changed Go files, and `git diff --check` returned exit 0. The two reviewed source hashes matched their post-check values. A whole-worktree pre-run manifest was not collected; this limitation is retained in the assessment, rather than claimed as complete input pinning.

An initial run used Go 1.27.0 and passed. After the first Go 1.26.7 pass, an unnecessary artifact-location-only repeat completed before a stop instruction arrived; it also passed in 0.918 seconds. All three logs are preserved. Checkout-local evidence was moved intact into the external artifact directory after commands completed, with matching hashes. Exact commands, exits, source pins, movement provenance, and limitations are recorded in `temp/m3-node-tls-diagnostics-20261011/canonical-root-assessment.txt`, SHA256 `586ad22be2c94bf6269e11c15f5a7cc432d9f90d32eb5f42bed6d99da6483c60`.

The frozen source received scoped security and code review without a source blocker. Its exact focused test inventory is:

- `TestLiveNodeTLSDiagnosticContext`
- `TestLiveNodeTLSDiagnosticStderrBufferBoundsOutput`
- `TestLiveNodeTLSDiagnosticProcessOutcome`
- `TestLiveExternalTLSCertificateTrust`
- `TestLiveExternalTLSNodeTrust`

The existing mandatory ingress selection maps each of these five names to `ingress-remainder` exactly once in both Linux race and Windows plain matrices. No workflow or selector changes are needed. Local verification here is native Windows; no local Linux or hosted result is claimed for the modified source.

Root-owned documentation verification runs the existing `check:workflow`, `build`, and `check:accessibility` commands after this page freezes. Its result is retained at `temp/m3-node-tls-diagnostics-docs-check-final-20261011/result.json`; this page does not preclaim that unrun result.

## Acceptance limits

This change improves the next failure's evidence. It does not claim a root cause, increase a deadline, or classify the hosted failure as harmless. The changed host probe still requires hosted Windows acceptance.

The `request-sent` marker is emitted immediately before `request.end()`: it records reaching that callsite, not completion of the call or network transmission. Stderr capture is best effort, and the existing exit callback does not wait for diagnostic flushing. An absent `stdout-callback` marker therefore does not prove that the callback never ran. Numeric Node major version narrows runtime identity but does not identify its patch release.
