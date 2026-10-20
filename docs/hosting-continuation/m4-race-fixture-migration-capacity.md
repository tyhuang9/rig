# M4 race-fixture migration capacity repair

## Hosted evidence

PR #191's `PostgreSQL and repository package race tests` job failed only in
the repository-remainder step. The retained source is
`temp/m3-lan-external-db-20261015/m4-lifecycle-coordinator-verification/live-ci-repair-draft-updates/pr191-job-117822655213/`:

- job ID `117822655213`, run `39176960810`, head
  `7873f7e895658b790afaf11710cbb15fb11b3b08`;
- raw log SHA-256
  `7d364ed0e18bd0e7a9835526668fdab110c5bf290620911e78e9d291c18f7d7b`;
- the repository-remainder command began at 01:38:09Z and two packages each
  reached the unchanged 40-minute package timeout.

`internal/applifecycle` completed 133 of 134 observed test events before its
last test started near the deadline. `internal/composeruntime` completed 78 of
80 observed test events before its last subcase started near the deadline. The
timeout stacks place both active tests in fresh SQLite schema migration through
`database.Open` and `database.migrateFS`. The retained log has no prior test
assertion failure or race report. This evidence supports cumulative fixture
setup cost; it does not prove that the previously uncompleted test events pass.

## Narrow repair

Only two high-fanout, test-only fixture constructors now use
`internal/testsupport/databasefixture.Open`:

- `newOperationFixture` in `internal/applifecycle/operation_repository_test.go`;
- `newBoundComposeJob` in `internal/composeruntime/executor_test.go`.

`databasefixture.Open` builds one process-local database using the production
migration path, then gives each call a private schema clone and reopens it with
production connection pragmas and migration-ledger validation. Its existing
contracts cover production-schema equivalence, independent concurrent clones,
exclusive target creation, sidecar rejection, and production reopen. This
repair leaves direct same-root `database.Open` calls alone because those calls
exercise reopen or concurrent-connection behavior.

No production code, test selector, race flag, count, timeout, workflow limit,
Docker configuration, database ownership behavior, or external database
provisioning changes here.

## Verification status

Static source inspection and `git diff --check` passed on this local branch.
Pinned Go 1.26.7 Windows verification passed:

```powershell
go test -json -count=1 -timeout=5m ./internal/applifecycle ./internal/composeruntime ./internal/testsupport/databasefixture
go vet ./internal/applifecycle ./internal/composeruntime ./internal/testsupport/databasefixture
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-generation.ps1
```

The test command exited successfully after discovering 78 parent tests and 301
test events: 299 passed, zero failed, and two symlink-guarded parents skipped
on Windows. `TestPolicyRejectsSymlinkAncestor` and
`TestOpenRejectsControlDatabaseSymlink` each explicitly skip when Windows does
not permit the test symlink. This is a platform limitation, not a claim of
complete no-skip coverage. Windows CGO is unavailable here, so no local race
command was run.

The vet log is empty and the canonical generation check passed. A frozen,
offline documentation install and `pnpm --dir docs build` also passed. Retained
verification artifacts live in
`temp/m3-lan-external-db-20261015/m4-fixture-migration-cost-verification/`:

| Artifact | SHA-256 |
| --- | --- |
| `windows-go-test.jsonl` | `0e8c0a2e7753c3b3adff00c0044d00c0a51ac610f426a329162e02ef513ff09b` |
| `windows-go-test-receipt.json` | `3d8e60c928ea7ccd4daba91c516643d72cbdcfb5560e80bcb3f88af38422b351` |
| `windows-go-vet.log` | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| `windows-check-generation.log` | `0d499fd3823f54074adbdef07e6926977f530d1c831495290467a80010c01635` |
| `docs-install-offline.log` | `6020b2e36e97d208bad3b9de7865ec7ca7cbf4524110d845826efd32bb978098` |
| `docs-build.log` | `99e54b233d82d2a5d1c6e91b8b097f1090b1e462eb1cd3887cf4148a7b26194e` |

Hosted CI must still run the unchanged race command,
`bash scripts/check-repository-race.sh repository-remainder`, to establish
Linux race behavior, the actual remainder duration, and complete matrix
results. This repair does not complete M4, establish all race behavior, or
authorize merge, deployment, or activation.
