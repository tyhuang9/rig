package generatedexecutor

import (
	"context"
	"database/sql"
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
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

// executorDomainFixture creates the real generated-domain owner before a test
// inserts application, deployment, or runtime rows. Tests retain control only
// of the candidate process transcript and environment; the budget and
// candidate authority stay sealed in the domain.
type executorDomainFixture struct {
	db            *sql.DB
	bindings      *deploymentbinding.Repository
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	repository    *deployments.Repository
	domain        *generatedingress.GeneratedDomain
	process       *generateddomain.Runner
	directories   runtimedocker.ControllerDirectories
}

type executorDomainEnvironment interface {
	generatedingress.GeneratedCandidateEnvironmentStager
	deployments.GeneratedMigrationEnvironmentStager
}

func newExecutorDomainFixture(t *testing.T, dataRoot string, fallback runtimeprocess.CommandRunner,
	environment executorDomainEnvironment, options generatedruntime.EngineOptions,
) executorDomainFixture {
	t.Helper()
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
	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
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
	process := generateddomain.NewRunner(generateddomain.Options{
		Fallback: fallback,
	})
	repository, err := deployments.NewWithExecutionGuardAndGeneratedMigration(db, guard, plans, configuration,
		environment, process, deployments.GeneratedMigrationProcessOptions{
			DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory,
			WorkingDirectory: options.WorkingDirectory, Timeout: options.CommandTimeout, OutputLimit: options.OutputLimit,
		})
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	domain, err := generatedingress.NewGeneratedDomain(context.Background(), stores, process, environment,
		generatedingress.GeneratedDomainOptions{
			DataRoot: dataRoot, DockerExecutable: options.DockerExecutable,
			DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
			CommandTimeout: options.CommandTimeout, PullTimeout: time.Minute,
			HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval,
			OutputLimit: options.OutputLimit, MemoryBytes: options.Limits.MemoryBytes,
			MilliCPUs: options.Limits.MilliCPUs, PIDs: options.Limits.PIDs,
			TmpfsBytes: options.Limits.TmpfsBytes, LogSize: options.Limits.LogSize,
			LogFiles: options.Limits.LogFiles, ReplacementDiskBytes: options.ReplacementDiskBytes,
		})
	if err != nil {
		t.Fatalf("construct generated domain: %v; commands=%#v", err, process.Commands())
	}
	return executorDomainFixture{
		db: db, bindings: bindings, plans: plans, configuration: configuration,
		repository: repository, domain: domain, process: process, directories: directories,
	}
}

// prepareExecutorDomainDatabase writes only the cached empty current-schema
// database. Callers invoke it while dataRoot is still empty, then close the
// ordinary fixture handle before creating directories, acquiring the
// controller lease, or opening the sealed generated-domain connection.
func prepareExecutorDomainDatabase(t *testing.T, dataRoot string) {
	t.Helper()
	prepared, err := databasefixture.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
}
