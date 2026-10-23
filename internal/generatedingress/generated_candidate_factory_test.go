package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type candidateFactoryProcess struct {
	runs int
}

func (p *candidateFactoryProcess) Run(
	context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	p.runs++
	return runtimeprocess.CommandResult{}, nil
}

type candidateFactoryProcessValue struct{}

func (candidateFactoryProcessValue) Run(
	context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{}, nil
}

type candidateFactoryEnvironment struct {
	path   string
	stages int
}

func (e *candidateFactoryEnvironment) StageGeneratedCandidate(
	string,
	int,
	[]byte,
) (GeneratedCandidateEnvironmentLease, error) {
	e.stages++
	return &candidateFactoryEnvironmentLease{path: e.path}, nil
}

func (e *candidateFactoryEnvironment) StageGeneratedMigration(
	string,
	int,
	[]byte,
) (deployments.GeneratedMigrationEnvironmentLease, error) {
	e.stages++
	return &candidateFactoryEnvironmentLease{path: e.path}, nil
}

type candidateFactoryEnvironmentValue struct{}

func (candidateFactoryEnvironmentValue) StageGeneratedCandidate(
	string,
	int,
	[]byte,
) (GeneratedCandidateEnvironmentLease, error) {
	return &candidateFactoryEnvironmentLease{}, nil
}

type candidateFactoryEnvironmentLease struct {
	path string
}

func (l *candidateFactoryEnvironmentLease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (*candidateFactoryEnvironmentLease) Cleanup() error { return nil }

type candidateFactoryCapacitySource struct {
	snapshots int
}

func (s *candidateFactoryCapacitySource) Snapshot(context.Context) (generatedcapacity.Snapshot, error) {
	s.snapshots++
	return generatedcapacity.Snapshot{
		MemoryAvailableBytes: 8 << 30,
		DiskAvailableBytes:   8 << 30,
	}, nil
}

type candidateFactoryFixture struct {
	repository  *candidateTestRepository
	process     *candidateFactoryProcess
	environment *candidateFactoryEnvironment
	budget      *generatedcapacity.Budget
	source      *candidateFactoryCapacitySource
	options     GeneratedCandidateProcessOptions
}

func newCandidateFactoryFixture(t *testing.T) *candidateFactoryFixture {
	t.Helper()
	migration := newGeneratedMigrationFixture(t)
	process := &candidateFactoryProcess{}
	environment := &candidateFactoryEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	source := &candidateFactoryCapacitySource{}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	return &candidateFactoryFixture{
		repository:  migration.repository,
		process:     process,
		environment: environment,
		budget:      budget,
		source:      source,
		options: GeneratedCandidateProcessOptions{
			DockerExecutable:      filepath.Join(t.TempDir(), "docker"),
			DockerConfigDirectory: t.TempDir(),
			WorkingDirectory:      t.TempDir(),
			CommandTimeout:        30 * time.Second,
			HealthTimeout:         2 * time.Minute,
			HealthPollInterval:    time.Second,
			OutputLimit:           64 << 10,
			MemoryBytes:           512 << 20,
			MilliCPUs:             1000,
			PIDs:                  256,
			TmpfsBytes:            64 << 20,
			LogSize:               "10m",
			LogFiles:              3,
			ReplacementDiskBytes:  256 << 20,
		},
	}
}

func TestGeneratedCandidateFactorySharesPreAndPostCreationRepositoryCopies(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	first, err := fixture.repository.GeneratedCandidateAuthority(fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 24
	start := make(chan struct{})
	results := make(chan *GeneratedCandidateAuthority, callers)
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			authority, callErr := fixture.repository.GeneratedCandidateAuthority(
				fixture.process, fixture.environment, fixture.budget, fixture.options)
			results <- authority
			errorsSeen <- callErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsSeen)
	for callErr := range errorsSeen {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	if installed := fixture.repository.candidateAuthority(); installed != first {
		t.Fatal("concurrent factory calls did not install an authority")
	}
	for authority := range results {
		if authority != first {
			t.Fatal("concurrent factory call returned a different authority")
		}
	}
	repeated, err := fixture.repository.GeneratedCandidateAuthority(
		fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil || repeated != first {
		t.Fatalf("post-factory repository copy authority=%p error=%v", repeated, err)
	}
	if first.stores.Repository() != fixture.repository.Repository || first.budget != fixture.budget || first.ledger == nil ||
		first.ledger.budget != fixture.budget || first.heads == nil {
		t.Fatal("candidate authority did not retain its exact winning composition")
	}
	copiedAuthority := *first
	if copiedAuthority.heads != first.heads {
		t.Fatal("candidate authority copy forked the shared head registry")
	}
	if fixture.source.snapshots != 0 || fixture.process.runs != 0 || fixture.environment.stages != 0 {
		t.Fatalf(
			"factory performed external work: snapshots=%d runs=%d stages=%d",
			fixture.source.snapshots,
			fixture.process.runs,
			fixture.environment.stages,
		)
	}

	t.Run("concurrent first construction", func(t *testing.T) {
		concurrent := newCandidateFactoryFixture(t)
		start := make(chan struct{})
		results := make(chan *GeneratedCandidateAuthority, callers)
		errorsSeen := make(chan error, callers)
		var wait sync.WaitGroup
		for range callers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				authority, callErr := concurrent.repository.GeneratedCandidateAuthority(
					concurrent.process, concurrent.environment, concurrent.budget, concurrent.options)
				results <- authority
				errorsSeen <- callErr
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		close(errorsSeen)
		for callErr := range errorsSeen {
			if callErr != nil {
				t.Fatal(callErr)
			}
		}
		installed := concurrent.repository.candidateAuthority()
		if installed == nil || installed.ledger == nil || installed.heads == nil ||
			installed.stores.Repository() != concurrent.repository.Repository ||
			installed.process != concurrent.process || installed.environment != concurrent.environment ||
			installed.budget != concurrent.budget || installed.options != concurrent.options {
			t.Fatal("concurrent first construction lost its exact composition")
		}
		for authority := range results {
			if authority != installed || authority.ledger != installed.ledger || authority.heads != installed.heads {
				t.Fatal("concurrent first construction forked candidate ownership")
			}
		}
	})
}

func TestGeneratedCandidateFactoryRejectsCompositionDriftWithoutMutation(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	first, err := fixture.repository.GeneratedCandidateAuthority(
		fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	originalLedger := first.ledger
	originalHeads := first.heads
	originalDockerEnv := append([]string(nil), first.dockerEnv...)

	copiedBudget := *fixture.budget
	changedOptions := fixture.options
	changedOptions.HealthPollInterval += time.Millisecond
	for _, test := range []struct {
		name        string
		process     runtimeprocess.CommandRunner
		environment GeneratedCandidateEnvironmentStager
		budget      *generatedcapacity.Budget
		options     GeneratedCandidateProcessOptions
	}{
		{name: "process", process: &candidateFactoryProcess{}, environment: fixture.environment, budget: fixture.budget, options: fixture.options},
		{name: "environment", process: fixture.process, environment: &candidateFactoryEnvironment{}, budget: fixture.budget, options: fixture.options},
		{name: "budget pointer", process: fixture.process, environment: fixture.environment, budget: &copiedBudget, options: fixture.options},
		{name: "options", process: fixture.process, environment: fixture.environment, budget: fixture.budget, options: changedOptions},
	} {
		t.Run(test.name, func(t *testing.T) {
			if authority, callErr := fixture.repository.GeneratedCandidateAuthority(
				test.process, test.environment, test.budget, test.options,
			); authority != nil || !errors.Is(callErr, deploymentexecution.ErrInvalidConfiguration) {
				t.Fatalf("composition drift authority=%p error=%v", authority, callErr)
			}
			if fixture.repository.candidateAuthority() != first ||
				first.process != fixture.process || first.environment != fixture.environment ||
				first.budget != fixture.budget || first.options != fixture.options ||
				first.ledger != originalLedger || first.heads != originalHeads ||
				strings.Join(first.dockerEnv, "\x00") != strings.Join(originalDockerEnv, "\x00") {
				t.Fatal("rejected repeat mutated the installed authority")
			}
		})
	}

	repeated, err := fixture.repository.GeneratedCandidateAuthority(
		fixture.process, fixture.environment, fixture.budget, fixture.options)
	if err != nil || repeated != first {
		t.Fatalf("exact repeat after refusals authority=%p error=%v", repeated, err)
	}
}

func TestGeneratedCandidateFactoryRejectsInvalidDependenciesAndDatabaseScope(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	assertRejected := func(
		t *testing.T,
		stores *deployments.GeneratedOwnerStores,
		process runtimeprocess.CommandRunner,
		environment GeneratedCandidateEnvironmentStager,
		budget *generatedcapacity.Budget,
		options GeneratedCandidateProcessOptions,
	) {
		t.Helper()
		authority, err := newGeneratedCandidateAuthorityConcrete(stores, process, environment, budget, options)
		if authority != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
			t.Fatalf("invalid factory authority=%p error=%v", authority, err)
		}
		if fixture.repository.candidateAuthority() != nil {
			t.Fatal("invalid factory call installed an authority")
		}
	}

	storesCopy := *fixture.repository.stores
	assertRejected(t, &storesCopy, candidateFactoryProcessValue{}, fixture.environment, fixture.budget, fixture.options)
	assertRejected(t, &storesCopy, fixture.process, candidateFactoryEnvironmentValue{}, fixture.budget, fixture.options)
	var nilProcess *candidateFactoryProcess
	assertRejected(t, &storesCopy, nilProcess, fixture.environment, fixture.budget, fixture.options)
	var nilEnvironment *candidateFactoryEnvironment
	assertRejected(t, &storesCopy, fixture.process, nilEnvironment, fixture.budget, fixture.options)
	assertRejected(t, &storesCopy, fixture.process, fixture.environment, nil, fixture.options)
	assertRejected(t, &storesCopy, fixture.process, fixture.environment, &generatedcapacity.Budget{}, fixture.options)
	invalidOptions := fixture.options
	invalidOptions.OutputLimit = 1
	assertRejected(t, &storesCopy, fixture.process, fixture.environment, fixture.budget, invalidOptions)
	assertRejected(t, nil, fixture.process, fixture.environment, fixture.budget, fixture.options)
	assertRejected(t, &deployments.GeneratedOwnerStores{}, fixture.process, fixture.environment, fixture.budget, fixture.options)

	if err := os.WriteFile(
		filepath.Join(fixture.options.DockerConfigDirectory, "config.json"),
		[]byte("{}"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, &storesCopy, fixture.process, fixture.environment, fixture.budget, fixture.options)
}

func TestGeneratedCandidateFactoryBuildsSanitizedFixedDockerEnvironment(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	fixture.options.DockerEndpoint = "npipe:////./pipe/docker_engine"
	t.Setenv("CANDIDATE_FACTORY_SECRET", "must-not-escape")
	authority, err := fixture.repository.GeneratedCandidateAuthority(
		fixture.process,
		fixture.environment,
		fixture.budget,
		fixture.options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.StringsAreSorted(authority.dockerEnv) {
		t.Fatalf("Docker environment is not sorted: %#v", authority.dockerEnv)
	}
	allowed := map[string]bool{
		"DOCKER_CONFIG": true,
		"DOCKER_HOST":   true,
		"PATH":          true,
		"PATHEXT":       true,
		"SystemRoot":    true,
		"TEMP":          true,
		"TMP":           true,
		"WINDIR":        true,
	}
	seen := make(map[string]string, len(authority.dockerEnv))
	for _, value := range authority.dockerEnv {
		key, content, found := strings.Cut(value, "=")
		if !found || !allowed[key] {
			t.Fatalf("unexpected Docker environment value %q", value)
		}
		seen[key] = content
	}
	if seen["DOCKER_CONFIG"] != fixture.options.DockerConfigDirectory ||
		seen["DOCKER_HOST"] != fixture.options.DockerEndpoint {
		t.Fatalf("Docker environment = %#v", authority.dockerEnv)
	}
	for _, value := range authority.dockerEnv {
		if strings.Contains(value, "must-not-escape") || strings.HasPrefix(value, "CANDIDATE_FACTORY_SECRET=") {
			t.Fatalf("untrusted environment escaped: %q", value)
		}
	}
	if fixture.source.snapshots != 0 || fixture.process.runs != 0 || fixture.environment.stages != 0 {
		t.Fatal("factory performed capacity, process or environment work")
	}
}

func TestGeneratedCandidateProcessOptionsRequireExactSafeBounds(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	if !validGeneratedCandidateProcessOptions(fixture.options) {
		t.Fatal("fixture options are invalid")
	}
	for _, test := range []struct {
		name   string
		mutate func(*GeneratedCandidateProcessOptions)
	}{
		{name: "relative executable", mutate: func(o *GeneratedCandidateProcessOptions) { o.DockerExecutable = "docker" }},
		{name: "remote endpoint", mutate: func(o *GeneratedCandidateProcessOptions) { o.DockerEndpoint = "tcp://example.test:2376" }},
		{name: "short command timeout", mutate: func(o *GeneratedCandidateProcessOptions) { o.CommandTimeout = time.Millisecond }},
		{name: "small output", mutate: func(o *GeneratedCandidateProcessOptions) { o.OutputLimit = 1 }},
		{name: "small memory", mutate: func(o *GeneratedCandidateProcessOptions) { o.MemoryBytes = 1 }},
		{name: "invalid log size", mutate: func(o *GeneratedCandidateProcessOptions) { o.LogSize = "2m" }},
		{name: "disk below log headroom", mutate: func(o *GeneratedCandidateProcessOptions) { o.ReplacementDiskBytes = 64 << 20 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := fixture.options
			test.mutate(&options)
			if validGeneratedCandidateProcessOptions(options) {
				t.Fatalf("invalid options accepted: %+v", options)
			}
		})
	}
}

func TestGeneratedCandidateFactoryRejectsUnsafeCommandDirectories(t *testing.T) {
	fixture := newCandidateFactoryFixture(t)
	missingDirectory := filepath.Join(t.TempDir(), "missing")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeAlias := candidateFactoryUnsafeDirectoryAlias(t)
	unsafeAncestor := filepath.Join(unsafeAlias, "nested")
	if err := os.Mkdir(unsafeAncestor, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*GeneratedCandidateProcessOptions)
	}{
		{name: "missing working directory", mutate: func(o *GeneratedCandidateProcessOptions) { o.WorkingDirectory = missingDirectory }},
		{name: "file working directory", mutate: func(o *GeneratedCandidateProcessOptions) { o.WorkingDirectory = file }},
		{name: "alias working directory", mutate: func(o *GeneratedCandidateProcessOptions) { o.WorkingDirectory = unsafeAlias }},
		{name: "alias ancestor working directory", mutate: func(o *GeneratedCandidateProcessOptions) { o.WorkingDirectory = unsafeAncestor }},
		{name: "missing Docker configuration", mutate: func(o *GeneratedCandidateProcessOptions) { o.DockerConfigDirectory = missingDirectory }},
		{name: "file Docker configuration", mutate: func(o *GeneratedCandidateProcessOptions) { o.DockerConfigDirectory = file }},
		{name: "alias Docker configuration", mutate: func(o *GeneratedCandidateProcessOptions) { o.DockerConfigDirectory = unsafeAlias }},
		{name: "alias ancestor Docker configuration", mutate: func(o *GeneratedCandidateProcessOptions) { o.DockerConfigDirectory = unsafeAncestor }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := fixture.options
			test.mutate(&options)
			if validGeneratedCandidateProcessOptions(options) {
				t.Fatal("unsafe candidate directories passed option validation")
			}
			authority, err := fixture.repository.GeneratedCandidateAuthority(
				fixture.process,
				fixture.environment,
				fixture.budget,
				options,
			)
			if authority != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
				t.Fatalf("unsafe directories authority=%p error=%v", authority, err)
			}
			if fixture.repository.candidateAuthority() != nil {
				t.Fatal("unsafe directory call installed an authority")
			}
		})
	}
}
