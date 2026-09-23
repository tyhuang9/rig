package generatedmigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type fakeConfiguration struct {
	requested []string
	value     appconfig.ExecutionConfiguration
	err       error
}

func (f *fakeConfiguration) ExportRevisionKeysForExecution(_ context.Context, _, _ string, _ int64, keys []string) (appconfig.ExecutionConfiguration, error) {
	f.requested = append([]string(nil), keys...)
	return f.value, f.err
}

type fakeLease struct {
	path    string
	cleaned bool
}

func (l *fakeLease) Path() string   { return l.path }
func (l *fakeLease) Cleanup() error { l.cleaned = true; return nil }

type fakeStager struct {
	contents []byte
	lease    *fakeLease
}

func (s *fakeStager) Stage(_ string, _ int, contents []byte) (generatedruntime.EnvironmentLease, error) {
	s.contents = append([]byte(nil), contents...)
	clear(contents)
	return s.lease, nil
}

type fakeRunner struct {
	requests []runtimeprocess.CommandRequest
	step     int
	failStep int
}

func (r *fakeRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	r.step++
	if r.step == r.failStep {
		return runtimeprocess.CommandResult{Stderr: []byte("private failure")}, errors.New("private failure")
	}
	switch request.Args[1] {
	case "create":
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("a", 64) + "\n")}, nil
	case "wait":
		return runtimeprocess.CommandResult{Stdout: []byte("0\n")}, nil
	default:
		return runtimeprocess.CommandResult{}, nil
	}
}

func TestRunnerUsesOnlyAllowedConfigurationAndExactCommandArgument(t *testing.T) {
	runner, configuration, stager, commands := newFixture(t)
	request := validMigrationRequest()
	request.Command = `npm run migrate && echo "$DATABASE_URL" $(touch /host)`
	if err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(configuration.requested) != 1 || configuration.requested[0] != "DATABASE_URL" {
		t.Fatalf("requested keys=%#v", configuration.requested)
	}
	if string(stager.contents) != "DATABASE_URL='secret'\n" || !stager.lease.cleaned {
		t.Fatalf("staging=%q cleaned=%v", stager.contents, stager.lease.cleaned)
	}
	if len(commands.requests) != 4 {
		t.Fatalf("requests=%d", len(commands.requests))
	}
	create := commands.requests[0]
	if got := create.Args[len(create.Args)-1]; got != request.Command || create.Args[len(create.Args)-2] != "-lc" || create.Args[len(create.Args)-3] != "/bin/sh" {
		t.Fatalf("command boundary=%#v", create.Args[len(create.Args)-3:])
	}
	joined := strings.Join(create.Args, " ")
	for _, required := range []string{"--read-only", "--cap-drop ALL", "no-new-privileges:true", "--network rig-a-", "--env-file", "--pids-limit 256", "--memory-swap 536870912"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("create lacks %q: %#v", required, create.Args)
		}
	}
}

func TestRunnerFailureCleansContainerAndReturnsStableCode(t *testing.T) {
	runner, _, stager, commands := newFixture(t)
	commands.failStep = 3
	err := runner.Run(context.Background(), validMigrationRequest())
	if !IsCode(err, "migration_failed") || !stager.lease.cleaned {
		t.Fatalf("error=%v cleaned=%v", err, stager.lease.cleaned)
	}
	if got := commands.requests[len(commands.requests)-1].Args; len(got) < 3 || got[0] != "container" || got[1] != "rm" || got[2] != "--force" {
		t.Fatalf("cleanup=%#v", got)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("secret error escaped: %v", err)
	}
}

func TestRunnerRejectsInvalidInputBeforeConfigurationOrDocker(t *testing.T) {
	runner, configuration, _, commands := newFixture(t)
	request := validMigrationRequest()
	request.AllowedEnvironmentKeys = []string{"TOKEN"}
	if err := runner.Run(context.Background(), request); !IsCode(err, "validation_failed") {
		t.Fatalf("error=%v", err)
	}
	if configuration.requested != nil || len(commands.requests) != 0 {
		t.Fatal("invalid input crossed execution boundary")
	}
}

func newFixture(t *testing.T) (*Runner, *fakeConfiguration, *fakeStager, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	docker := filepath.Join(root, "docker.exe")
	config := filepath.Join(root, "docker-config")
	work := filepath.Join(root, "work")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := &fakeConfiguration{value: appconfig.ExecutionConfiguration{RevisionID: "55555555-5555-5555-5555-555555555555", RevisionNumber: 1, Environment: []byte("DATABASE_URL='secret'\n")}}
	stager := &fakeStager{lease: &fakeLease{path: filepath.Join(root, "runtime.env")}}
	commands := &fakeRunner{}
	runner, err := New(configuration, stager, commands, Options{DockerExecutable: docker, DockerConfigDirectory: config, WorkingDirectory: work})
	if err != nil {
		t.Fatal(err)
	}
	return runner, configuration, stager, commands
}

func validMigrationRequest() generatedruntime.MigrationRequest {
	return generatedruntime.MigrationRequest{
		AppID: "11111111-1111-1111-1111-111111111111", ReleaseID: "22222222-2222-2222-2222-222222222222", DeploymentID: "33333333-3333-3333-3333-333333333333", ArtifactID: "44444444-4444-4444-4444-444444444444", DeploymentPlanRevisionID: "66666666-6666-6666-6666-666666666666", ComponentName: "api", RootDirectory: "apps/api", ImageContentID: "sha256:" + strings.Repeat("a", 64), Command: "npm run migrate", ConfigurationRevisionID: "55555555-5555-5555-5555-555555555555", ConfigurationRevisionNumber: 1, AllowedEnvironmentKeys: []string{"DATABASE_URL"},
	}
}
