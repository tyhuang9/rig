package generatedingress

import (
	"os"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GeneratedCandidateAuthority is the one candidate owner associated with a
// concrete repository registry. Command and invocation methods are defined by
// the candidate execution owner; composition remains fixed here.
type GeneratedCandidateAuthority struct {
	stores      *deployments.GeneratedOwnerStores
	process     runtimeprocess.CommandRunner
	environment GeneratedCandidateEnvironmentStager
	budget      *generatedcapacity.Budget
	options     GeneratedCandidateProcessOptions
	ledger      *candidateCapacityLedger
	heads       *candidateHeadRegistry
	dockerEnv   []string
}

type generatedCandidateAuthorityRegistry struct {
	mu        sync.Mutex
	authority *GeneratedCandidateAuthority
}

var generatedCandidateAuthorityRegistries = struct {
	sync.Mutex
	values map[*deployments.Repository]*generatedCandidateAuthorityRegistry
}{values: make(map[*deployments.Repository]*generatedCandidateAuthorityRegistry)}

// NewGeneratedCandidateAuthority returns the one candidate owner associated
// with the sealed store bundle. This is the relocation surface for the E4
// owner; the generated-domain factory will replace its external construction
// before route authority is enabled.
func NewGeneratedCandidateAuthority(
	stores *deployments.GeneratedOwnerStores,
	process runtimeprocess.CommandRunner,
	environment GeneratedCandidateEnvironmentStager,
	budget *generatedcapacity.Budget,
	options GeneratedCandidateProcessOptions,
) (*GeneratedCandidateAuthority, error) {
	if stores == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	// Capture the sealed bundle by value before validation and retention. A
	// caller may pass the address of its own value copy and later overwrite
	// that variable; the authority must keep the exact dependencies validated
	// for this construction attempt.
	fixedStores := *stores
	stores = &fixedStores
	if !generatedCandidateRepositoryConfigured(stores) ||
		!generatedCandidatePointerIdentity(process) ||
		!generatedCandidatePointerIdentity(environment) ||
		budget == nil || !budget.Configured() ||
		!validGeneratedCandidateProcessOptions(options) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}

	authority, err := newGeneratedCandidateAuthorityConcrete(stores, process, environment, budget, options)
	if err != nil {
		return nil, err
	}

	generatedCandidateAuthorityRegistries.Lock()
	repositoryIdentity := stores.Repository()
	registry := generatedCandidateAuthorityRegistries.values[repositoryIdentity]
	if registry == nil {
		registry = &generatedCandidateAuthorityRegistry{}
		generatedCandidateAuthorityRegistries.values[repositoryIdentity] = registry
	}
	generatedCandidateAuthorityRegistries.Unlock()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.authority != nil {
		existing := registry.authority
		if existing.process != process || existing.environment != environment ||
			existing.budget != budget || existing.options != options {
			return nil, deploymentexecution.ErrInvalidConfiguration
		}
		return existing, nil
	}
	registry.authority = authority
	return authority, nil
}

// newGeneratedCandidateAuthorityConcrete builds one complete candidate owner
// without consulting or publishing through the temporary public repository
// registry. The private generated-domain constructor uses this once after it
// has created the raw budget; NewGeneratedCandidateAuthority preserves the
// public join behavior around the same concrete assembly.
func newGeneratedCandidateAuthorityConcrete(
	stores *deployments.GeneratedOwnerStores,
	process runtimeprocess.CommandRunner,
	environment GeneratedCandidateEnvironmentStager,
	budget *generatedcapacity.Budget,
	options GeneratedCandidateProcessOptions,
) (*GeneratedCandidateAuthority, error) {
	if !generatedCandidateRepositoryConfigured(stores) ||
		!generatedCandidatePointerIdentity(process) ||
		!generatedCandidatePointerIdentity(environment) ||
		budget == nil || !budget.Configured() ||
		!validGeneratedCandidateProcessOptions(options) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	dockerEnv, err := generatedCandidateDockerEnvironment(options)
	if err != nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	ledger, err := newCandidateCapacityLedger(budget)
	if err != nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return &GeneratedCandidateAuthority{
		stores: stores, process: process, environment: environment, budget: budget,
		options: options, ledger: ledger, heads: newCandidateHeadRegistry(), dockerEnv: dockerEnv,
	}, nil
}

func generatedCandidateRepositoryConfigured(stores *deployments.GeneratedOwnerStores) bool {
	return stores != nil && stores.Configured() && stores.Repository() != nil &&
		stores.Database() != nil && stores.ExecutionGuard() != nil &&
		stores.Plans() != nil && stores.Configuration() != nil
}

func generatedCandidatePointerIdentity(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && !reflected.IsNil()
}

func validGeneratedCandidateProcessOptions(options GeneratedCandidateProcessOptions) bool {
	if !generatedCandidateCleanAbsolutePath(options.DockerExecutable) ||
		!generatedCandidateLocalEndpoint(options.DockerEndpoint) {
		return false
	}
	if err := generatedCandidateValidateCommandDirectories(options); err != nil {
		return false
	}
	if options.CommandTimeout < time.Second || options.CommandTimeout > 10*time.Minute ||
		options.HealthTimeout < time.Second || options.HealthTimeout > 30*time.Minute ||
		options.HealthPollInterval < 100*time.Millisecond || options.HealthPollInterval > 30*time.Second ||
		options.OutputLimit < 1024 || options.OutputLimit > runtimeprocess.DefaultOutputLimit {
		return false
	}
	if options.MemoryBytes < 64<<20 || options.MemoryBytes > 64<<30 ||
		options.MilliCPUs < 100 || options.MilliCPUs > 16000 ||
		options.PIDs < 32 || options.PIDs > 4096 ||
		options.TmpfsBytes < 1<<20 || options.TmpfsBytes > 1<<30 ||
		options.LogFiles < 2 || options.LogFiles > 10 {
		return false
	}
	logMegabytes := uint64(0)
	switch options.LogSize {
	case "1m":
		logMegabytes = 1
	case "5m":
		logMegabytes = 5
	case "10m":
		logMegabytes = 10
	case "20m":
		logMegabytes = 20
	case "50m":
		logMegabytes = 50
	default:
		return false
	}
	minimumDisk := logMegabytes*uint64(options.LogFiles)*(1<<20) + 64<<20
	return options.ReplacementDiskBytes >= minimumDisk && options.ReplacementDiskBytes <= 64<<30
}

func generatedCandidateDockerEnvironment(options GeneratedCandidateProcessOptions) ([]string, error) {
	if err := generatedCandidateValidateCommandDirectories(options); err != nil {
		return nil, err
	}
	values := map[string]string{"DOCKER_CONFIG": options.DockerConfigDirectory}
	for _, key := range []string{"PATH", "PATHEXT", "SystemRoot", "TEMP", "TMP", "WINDIR"} {
		if value, exists := os.LookupEnv(key); exists {
			values[key] = value
		}
	}
	if options.DockerEndpoint != "" {
		values["DOCKER_HOST"] = options.DockerEndpoint
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment, nil
}
