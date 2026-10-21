//go:build linux || windows

package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainIdentityFixture struct {
	dataRoot string
	database *sql.DB
	lease    *controllerowner.Lease
	stores   *deployments.GeneratedOwnerStores
	options  generatedDomainOptions
}

type generatedDomainDaemonResponse struct {
	result runtimeprocess.CommandResult
	err    error
}

type generatedDomainDaemonRunner struct {
	responses []generatedDomainDaemonResponse
	requests  []runtimeprocess.CommandRequest
	cancel    context.CancelFunc
}

func (r *generatedDomainDaemonRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	r.requests = append(r.requests, request)
	if r.cancel != nil {
		r.cancel()
	}
	if len(r.responses) == 0 {
		return runtimeprocess.CommandResult{}, errors.New("unexpected daemon inspection")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.result, response.err
}

func newGeneratedDomainIdentityFixture(t *testing.T) generatedDomainIdentityFixture {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	db, err := database.OpenForGeneratedDomain(context.Background(), dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := newGeneratedDomainIdentityFixtureOnDatabase(t, db, dataRoot)
	fixture.lease = lease
	return fixture
}

func newGeneratedDomainIdentityFixtureOnDatabase(t *testing.T, db *sql.DB, dataRoot string) generatedDomainIdentityFixture {
	t.Helper()
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycle := applifecycle.New(db)
	bindingValidator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, bindingValidator)
	if err != nil {
		t.Fatal(err)
	}
	positiveFence, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positiveFence, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	return generatedDomainIdentityFixture{
		dataRoot: dataRoot,
		database: db,
		stores:   stores,
		options: generatedDomainOptions{
			DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerEndpoint: "",
			DockerConfigDirectory: directories.DockerConfigDirectory,
			WorkingDirectory:      directories.WorkingDirectory, DataRoot: dataRoot, HostPort: 8080,
			CommandTimeout: time.Second, PullTimeout: time.Second, HealthTimeout: time.Second,
			HealthPollInterval: 100 * time.Millisecond, OutputLimit: 1024,
			MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 64, TmpfsBytes: 8 << 20,
			LogSize: "1m", LogFiles: 2, ReplacementDiskBytes: 128 << 20,
		},
	}
}

func TestGeneratedDomainPhysicalIdentityRequiresExactSealedFilesystem(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{
		result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon-one"`)},
	}}}
	identity, proof, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, runner, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if identity.database != fixture.database || identity.databaseFile == nil || identity.manager.dataRoot == nil ||
		identity.manager.ingressRoot != nil || identity.manager.daemonID != "daemon-one" ||
		identity.manager.namespace != fixedGeneratedGatewayNamespace() {
		t.Fatalf("unexpected physical identity: %+v", identity)
	}
	if ingress, err := proof.recheck(false); err != nil || ingress != nil {
		t.Fatalf("recheck before ingress: identity=%v error=%v", ingress, err)
	}
	if _, err := newStateStore(fixture.dataRoot); err != nil {
		t.Fatal(err)
	}
	if ingress, err := proof.recheck(true); err != nil || ingress == nil {
		t.Fatalf("recheck after safe ingress creation: identity=%v error=%v", ingress, err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("daemon inspections = %d", len(runner.requests))
	}
	request := runner.requests[0]
	if request.Executable != fixture.options.DockerExecutable ||
		!reflect.DeepEqual(request.Args, []string{"info", "--format", "{{json .ID}}"}) ||
		request.Directory != filepath.Dir(fixture.options.DockerExecutable) ||
		request.Timeout != fixture.options.CommandTimeout || request.OutputLimit != generatedDaemonIdentityOutputLimit ||
		!containsGeneratedDomainEnvironment(request.Env, "DOCKER_CONFIG="+fixture.options.DockerConfigDirectory) {
		t.Fatalf("unexpected daemon request: %+v", request)
	}

	foreignRoot := filepath.Join(t.TempDir(), "foreign-state")
	foreignDatabase, err := database.Open(foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := foreignDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	foreign := newGeneratedDomainIdentityFixtureOnDatabase(t, fixture.database, foreignRoot)
	if !foreign.stores.ConfiguredForDomain(foreignRoot, foreign.options.WorkingDirectory) {
		t.Fatal("foreign-root fixture was not configured before physical identity inspection")
	}
	foreignRunner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{result: runtimeprocess.CommandResult{Stdout: []byte(`"unused"`)}}}}
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), foreign.stores, foreignRunner, foreign.options); err == nil {
		t.Fatal("same database paired with a foreign configured root was accepted")
	}
	if len(foreignRunner.requests) != 0 {
		t.Fatal("daemon was inspected before the foreign root/database mismatch was refused")
	}

	wrongOptions := fixture.options
	wrongOptions.DataRoot = foreignRoot
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, foreignRunner, wrongOptions); err == nil {
		t.Fatal("sealed stores accepted a different requested root")
	}
	if len(foreignRunner.requests) != 0 {
		t.Fatal("daemon was inspected after sealed configuration rejection")
	}
	unsafeOptions := fixture.options
	unsafeParent := generatedDomainDirectoryAlias(t, filepath.Dir(fixture.options.DockerConfigDirectory), "docker-config-parent-alias")
	unsafeOptions.DockerConfigDirectory = filepath.Join(unsafeParent, filepath.Base(fixture.options.DockerConfigDirectory))
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, foreignRunner, unsafeOptions); err == nil {
		t.Fatal("Docker configuration beneath an unsafe ancestor alias was accepted")
	}
	if len(foreignRunner.requests) != 0 {
		t.Fatal("daemon was inspected after unsafe command-directory rejection")
	}
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), nil, runner, fixture.options); err == nil {
		t.Fatal("nil stores were accepted")
	}
	var zero deployments.GeneratedOwnerStores
	if _, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), &zero, runner, fixture.options); err == nil {
		t.Fatal("zero stores were accepted")
	}
}

func TestGeneratedDomainDatabaseIdentityRejectsAmbiguousOrNonPhysicalMain(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	secondDB, err := database.OpenForGeneratedDomain(context.Background(), fixture.dataRoot, fixture.lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	second := newGeneratedDomainIdentityFixtureOnDatabase(t, secondDB, fixture.dataRoot)
	left, err := inspectGeneratedDomainPaths(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	rightMain, err := inspectGeneratedDomainMainDatabase(context.Background(), second.database)
	if err != nil {
		t.Fatal(err)
	}
	rightFile, err := inspectGeneratedDomainRegularFile(rightMain)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.database == second.database || !os.SameFile(left.databaseFile, rightFile) {
		t.Fatal("separate handles did not resolve the same control database")
	}

	attached := filepath.Join(t.TempDir(), "attached.db")
	if _, err := fixture.database.Exec(`ATTACH DATABASE ? AS extra`, attached); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectGeneratedDomainMainDatabase(context.Background(), fixture.database); err == nil {
		t.Fatal("attached database made database_list ambiguous but was accepted")
	}
	if _, err := fixture.database.Exec(`DETACH DATABASE extra`); err != nil {
		t.Fatal(err)
	}

	memory, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memory.Close() })
	if err := database.Migrate(memory); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectGeneratedDomainMainDatabase(context.Background(), memory); err == nil {
		t.Fatal("in-memory main database was accepted")
	}

	for _, path := range []string{"", ":memory:", "file:control.db", "control.db", "/tmp/control.db?mode=rw", "/tmp/control.db#fragment", "/tmp/con\x00trol.db", "/??/control.db", `/Device/HarddiskVolume1/control.db`} {
		if validGeneratedDomainMainDatabasePath(path) {
			t.Fatalf("unsafe or ambiguous main path accepted: %q", path)
		}
	}
}

func TestGeneratedDomainPathProofRejectsReplacementAndUnsafeIngress(t *testing.T) {
	t.Run("data root alias", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		alias := generatedDomainDirectoryAlias(t, fixture.dataRoot, "data-root-alias")
		if _, err := inspectGeneratedDomainPaths(alias); err == nil {
			t.Fatal("data-root symlink or junction was accepted")
		}
	})
	t.Run("ingress alias", func(t *testing.T) {
		dataRoot := newGeneratedDomainPathOnlyFixture(t)
		runtimeRoot := filepath.Join(dataRoot, "runtime")
		if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "foreign-ingress")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		generatedDomainCreateDirectoryAlias(t, target, filepath.Join(runtimeRoot, "generated-ingress"))
		if _, err := inspectGeneratedDomainPaths(dataRoot); err == nil {
			t.Fatal("ingress symlink or junction was accepted")
		}
	})
	t.Run("ingress ancestor alias", func(t *testing.T) {
		dataRoot := newGeneratedDomainPathOnlyFixture(t)
		foreignRuntime := filepath.Join(t.TempDir(), "foreign-runtime")
		if err := os.MkdirAll(filepath.Join(foreignRuntime, "generated-ingress"), 0o700); err != nil {
			t.Fatal(err)
		}
		generatedDomainCreateDirectoryAlias(t, foreignRuntime, filepath.Join(dataRoot, "runtime"))
		if _, err := inspectGeneratedDomainPaths(dataRoot); err == nil {
			t.Fatal("ingress beneath a symlink or junction ancestor was accepted")
		}
	})
	t.Run("ingress appears without construction boundary", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		proof, err := inspectGeneratedDomainPaths(fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newStateStore(fixture.dataRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := proof.recheck(false); err == nil {
			t.Fatal("unapproved absent-to-present ingress transition was accepted")
		}
	})
	t.Run("ingress replaced", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		if _, err := newStateStore(fixture.dataRoot); err != nil {
			t.Fatal(err)
		}
		proof, err := inspectGeneratedDomainPaths(fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		moved := proof.ingressPath + "-old"
		if err := os.Rename(proof.ingressPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(proof.ingressPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := proof.recheck(true); err == nil {
			t.Fatal("replaced ingress directory was accepted")
		}
	})
	t.Run("database replaced", func(t *testing.T) {
		dataRoot := newGeneratedDomainPathOnlyFixture(t)
		proof, err := inspectGeneratedDomainPaths(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		moved := proof.databasePath + ".old"
		if err := os.Rename(proof.databasePath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(proof.databasePath, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := proof.recheck(false); err == nil {
			t.Fatal("replaced control database was accepted")
		}
	})
	t.Run("root replaced", func(t *testing.T) {
		dataRoot := newGeneratedDomainPathOnlyFixture(t)
		proof, err := inspectGeneratedDomainPaths(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		moved := dataRoot + "-old"
		if err := os.Rename(dataRoot, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dataRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(proof.databasePath, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := proof.recheck(false); err == nil {
			t.Fatal("replaced data root was accepted")
		}
	})
	t.Run("regular file blocks ingress directory", func(t *testing.T) {
		dataRoot := newGeneratedDomainPathOnlyFixture(t)
		runtimeRoot := filepath.Join(dataRoot, "runtime")
		if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runtimeRoot, "generated-ingress"), []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectGeneratedDomainPaths(dataRoot); err == nil {
			t.Fatal("non-directory ingress path was accepted")
		}
	})
}

func newGeneratedDomainPathOnlyFixture(t *testing.T) string {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "control.db"), []byte("path-only-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataRoot
}

func TestGeneratedDockerDaemonIdentityRequiresExactBoundedObservation(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "docker")
	environment := []string{"DOCKER_CONFIG=" + t.TempDir()}
	for _, test := range []struct {
		name   string
		result runtimeprocess.CommandResult
		err    error
	}{
		{name: "empty output"},
		{name: "empty ID", result: runtimeprocess.CommandResult{Stdout: []byte(`""`)}},
		{name: "whitespace ID", result: runtimeprocess.CommandResult{Stdout: []byte(`" daemon "`)}},
		{name: "control ID", result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon\u0000id"`)}},
		{name: "unicode control ID", result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon\u0085id"`)}},
		{name: "malformed", result: runtimeprocess.CommandResult{Stdout: []byte(`{"ID":"daemon"}`)}},
		{name: "extra JSON", result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon" "extra"`)}},
		{name: "invalid UTF-8", result: runtimeprocess.CommandResult{Stdout: []byte{'"', 0xff, '"'}}},
		{name: "oversized", result: runtimeprocess.CommandResult{Stdout: []byte(`"` + strings.Repeat("a", generatedDaemonIdentityMaximum+1) + `"`)}},
		{name: "stdout truncated", result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon"`), StdoutTruncated: true}},
		{name: "stderr truncated", result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon"`), StderrTruncated: true}},
		{name: "command error", result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon"`)}, err: errors.New("failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{result: test.result, err: test.err}}}
			if id, err := inspectGeneratedDockerDaemonIdentity(context.Background(), runner, executable, environment, time.Second); err == nil || id != "" {
				t.Fatalf("invalid daemon observation accepted: id=%q error=%v", id, err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{result: runtimeprocess.CommandResult{Stdout: []byte(`"unused"`)}}}}
	if _, err := inspectGeneratedDockerDaemonIdentity(ctx, runner, executable, environment, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel error = %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatal("pre-cancelled inspection invoked the runner")
	}

	for _, test := range []struct {
		name      string
		err       error
		expected  []error
		forbidden string
	}{
		{name: "cancelled", err: context.Canceled, expected: []error{context.Canceled}},
		{name: "deadline", err: context.DeadlineExceeded, expected: []error{context.DeadlineExceeded}},
		{name: "termination", err: runtimeprocess.ErrTerminationFailed, expected: []error{runtimeprocess.ErrTerminationFailed}},
		{name: "cancelled termination with private cause", err: errors.Join(context.Canceled, runtimeprocess.ErrTerminationFailed, errors.New("private-marker")), expected: []error{context.Canceled, runtimeprocess.ErrTerminationFailed}, forbidden: "private-marker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := []byte(`"secret-daemon"`)
			stderr := []byte("private failure")
			runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{
				result: runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr}, err: test.err,
			}}}
			_, err := inspectGeneratedDockerDaemonIdentity(context.Background(), runner, executable, environment, time.Second)
			for _, expected := range test.expected {
				if !errors.Is(err, expected) {
					t.Fatalf("cause not preserved: error=%v expected=%v", err, expected)
				}
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("daemon error exposed private nested cause: %v", err)
			}
			if !generatedDomainBytesCleared(stdout) || !generatedDomainBytesCleared(stderr) {
				t.Fatal("daemon output buffers were not cleared")
			}
		})
	}

	t.Run("context cancellation accompanying termination", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		stdout := []byte(`"secret-daemon"`)
		stderr := []byte("private failure")
		runner := &generatedDomainDaemonRunner{
			cancel: cancel,
			responses: []generatedDomainDaemonResponse{{
				result: runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr},
				err:    runtimeprocess.ErrTerminationFailed,
			}},
		}
		_, err := inspectGeneratedDockerDaemonIdentity(ctx, runner, executable, environment, time.Second)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, runtimeprocess.ErrTerminationFailed) {
			t.Fatalf("combined causes not preserved: %v", err)
		}
		if !generatedDomainBytesCleared(stdout) || !generatedDomainBytesCleared(stderr) {
			t.Fatal("combined-failure daemon output buffers were not cleared")
		}
	})

	stdout := []byte(`"daemon-ok"`)
	stderr := []byte("ignored warning")
	runner = &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{result: runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr}}}}
	if id, err := inspectGeneratedDockerDaemonIdentity(context.Background(), runner, executable, environment, time.Second); err != nil || id != "daemon-ok" {
		t.Fatalf("valid daemon observation: id=%q error=%v", id, err)
	}
	if !generatedDomainBytesCleared(stdout) || !generatedDomainBytesCleared(stderr) {
		t.Fatal("successful daemon output buffers were not cleared")
	}
}

func TestGeneratedDomainDaemonIdentityUsesDaemonNotEndpointSpelling(t *testing.T) {
	fixture := newGeneratedDomainIdentityFixture(t)
	aliases := generatedDomainDockerEndpointAliases()
	if len(aliases) != 2 || aliases[0] == aliases[1] {
		t.Fatalf("endpoint aliases = %v", aliases)
	}
	identities := make([]generatedDomainPhysicalIdentity, 0, len(aliases))
	for _, endpoint := range aliases {
		options := fixture.options
		options.DockerEndpoint = endpoint
		runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{{result: runtimeprocess.CommandResult{Stdout: []byte(`"shared-daemon"`)}}}}
		identity, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, runner, options)
		if err != nil {
			t.Fatalf("endpoint %q: %v", endpoint, err)
		}
		if len(runner.requests) != 1 {
			t.Fatalf("endpoint %q daemon requests = %d", endpoint, len(runner.requests))
		}
		host, present := generatedDomainEnvironmentValue(runner.requests[0].Env, "DOCKER_HOST")
		if (endpoint == "" && present) || (endpoint != "" && (!present || host != endpoint)) {
			t.Fatalf("endpoint %q produced DOCKER_HOST value=%q present=%t", endpoint, host, present)
		}
		identities = append(identities, identity)
	}
	if !generatedDomainDaemonNamespaceConflict(identities[0], identities[1]) {
		t.Fatal("endpoint aliases reporting the same daemon did not conflict")
	}
	runner := &generatedDomainDaemonRunner{responses: []generatedDomainDaemonResponse{
		{result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon-one"`)}},
		{result: runtimeprocess.CommandResult{Stdout: []byte(`"daemon-two"`)}},
	}}
	first, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, runner, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), fixture.stores, runner, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	if generatedDomainDaemonNamespaceConflict(first, second) {
		t.Fatal("different daemon IDs at the same endpoint conflicted")
	}
}

func TestGeneratedDomainCollisionPredicatesAreIndependent(t *testing.T) {
	leftFiles := generatedDomainTestFileIdentities(t, "left")
	rightFiles := generatedDomainTestFileIdentities(t, "right")
	aliasRoot := generatedDomainIdentityAliasInfo(t, leftFiles.rootPath, true, "root-alias")
	aliasDB := generatedDomainIdentityAliasInfo(t, leftFiles.databasePath, false, "db-alias")
	namespace := fixedGeneratedGatewayNamespace()
	left := generatedDomainPhysicalIdentity{manager: generatedManagerPhysicalIdentity{dataRoot: leftFiles.root, daemonID: "daemon-a", namespace: namespace}, databaseFile: leftFiles.database}

	sameRootDifferentDB := generatedDomainPhysicalIdentity{manager: generatedManagerPhysicalIdentity{dataRoot: aliasRoot, daemonID: "daemon-b", namespace: namespace}, databaseFile: rightFiles.database}
	if !generatedDomainRootOrDatabaseConflict(left, sameRootDifferentDB) || generatedDomainDaemonNamespaceConflict(left, sameRootDifferentDB) {
		t.Fatal("root conflict was not independent from daemon identity")
	}
	sameDBDifferentRoot := generatedDomainPhysicalIdentity{manager: generatedManagerPhysicalIdentity{dataRoot: rightFiles.root, daemonID: "daemon-b", namespace: namespace}, databaseFile: aliasDB}
	if !generatedDomainRootOrDatabaseConflict(left, sameDBDifferentRoot) || generatedDomainDaemonNamespaceConflict(left, sameDBDifferentRoot) {
		t.Fatal("database conflict was not independent from daemon identity")
	}
	sameDaemonDifferentFiles := generatedDomainPhysicalIdentity{manager: generatedManagerPhysicalIdentity{dataRoot: rightFiles.root, daemonID: "daemon-a", namespace: namespace}, databaseFile: rightFiles.database}
	if generatedDomainRootOrDatabaseConflict(left, sameDaemonDifferentFiles) || !generatedDomainDaemonNamespaceConflict(left, sameDaemonDifferentFiles) {
		t.Fatal("daemon/namespace conflict was not independent from roots")
	}
	differentNamespace := sameDaemonDifferentFiles
	differentNamespace.manager.namespace.network = "foreign-network"
	if generatedDomainDaemonNamespaceConflict(left, differentNamespace) {
		t.Fatal("different fixed namespace conflicted")
	}
	if generatedDomainRootOrDatabaseConflict(generatedDomainPhysicalIdentity{}, left) || generatedDomainDaemonNamespaceConflict(generatedDomainPhysicalIdentity{}, left) {
		t.Fatal("zero domain identity conflicted")
	}

	legacy := generatedManagerPhysicalIdentity{dataRoot: aliasRoot, daemonID: "daemon-a", namespace: namespace}
	if !generatedLegacyRootConflict(legacy, left) || !generatedLegacyDaemonNamespaceConflict(legacy, left) {
		t.Fatal("legacy root and daemon conflicts were not independently recognized")
	}
	legacy = generatedManagerPhysicalIdentity{ingressRoot: generatedDomainIdentityAliasInfo(t, rightFiles.rootPath, true, "ingress-alias"), daemonID: "daemon-b", namespace: namespace}
	domainWithIngress := left
	domainWithIngress.manager.ingressRoot = rightFiles.root
	if !generatedLegacyRootConflict(legacy, domainWithIngress) || generatedLegacyDaemonNamespaceConflict(legacy, domainWithIngress) {
		t.Fatal("legacy ingress conflict depended on daemon identity")
	}
}

type generatedDomainTestFiles struct {
	rootPath     string
	databasePath string
	root         os.FileInfo
	database     os.FileInfo
}

func generatedDomainTestFileIdentities(t *testing.T, name string) generatedDomainTestFiles {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "control.db")
	if err := os.WriteFile(file, []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	return generatedDomainTestFiles{rootPath: root, databasePath: file, root: rootInfo, database: fileInfo}
}

func containsGeneratedDomainEnvironment(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func generatedDomainEnvironmentValue(values []string, key string) (string, bool) {
	prefix := key + "="
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix), true
		}
	}
	return "", false
}

func generatedDomainBytesCleared(values []byte) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}
