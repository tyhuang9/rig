package generatedingress

import (
	"context"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GeneratedDomainOptions is the fixed public composition input. It contains
// no Manager, capacity source, lease, callback, proof, or startup result.
type GeneratedDomainOptions struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	DataRoot              string
	HostPort              uint16
	CommandTimeout        time.Duration
	PullTimeout           time.Duration
	HealthTimeout         time.Duration
	HealthPollInterval    time.Duration
	OutputLimit           int
	MemoryBytes           int64
	MilliCPUs             int64
	PIDs                  int64
	TmpfsBytes            int64
	LogSize               string
	LogFiles              int
	ReplacementDiskBytes  uint64
}

// GeneratedDomain is the single published owner graph for generated ingress,
// candidate, route, and retained accounting authority.
type GeneratedDomain struct {
	construction *generatedDomainConstruction
}

// NewGeneratedDomain joins or constructs the exact generated domain. Fresh
// Stable and Empty observations may construct directly; Refused/ColdAccounting
// may construct only after exact retained ownership is reconstructed and
// repeatedly reattested. Typed recovery and every other refusal fail without
// publication.
func NewGeneratedDomain(ctx context.Context, stores *deployments.GeneratedOwnerStores,
	runner runtimeprocess.CommandRunner, environment GeneratedCandidateEnvironmentStager,
	options GeneratedDomainOptions,
) (*GeneratedDomain, error) {
	construction, err := newGeneratedDomainConstruction(ctx, stores, runner, environment,
		privateGeneratedDomainOptions(options))
	if err != nil {
		return nil, err
	}
	if !validGeneratedDomainConstructionResult(construction.reservation, construction) ||
		construction.domain == nil || construction.domain.construction != construction {
		return nil, errGeneratedDomainConstructionRefused
	}
	return construction.domain, nil
}

func (d *GeneratedDomain) Gateway() *Manager {
	if d == nil || d.construction == nil {
		return nil
	}
	return d.construction.manager
}

func (d *GeneratedDomain) Candidates() *GeneratedCandidateAuthority {
	if d == nil || d.construction == nil {
		return nil
	}
	return d.construction.candidates
}

func (d *GeneratedDomain) Routes() *GeneratedRouteAuthority {
	if d == nil || d.construction == nil {
		return nil
	}
	return d.construction.routes
}

func privateGeneratedDomainOptions(options GeneratedDomainOptions) generatedDomainOptions {
	return generatedDomainOptions{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint,
		DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
		DataRoot: options.DataRoot, HostPort: options.HostPort, CommandTimeout: options.CommandTimeout,
		PullTimeout: options.PullTimeout, HealthTimeout: options.HealthTimeout,
		HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		MemoryBytes: options.MemoryBytes, MilliCPUs: options.MilliCPUs, PIDs: options.PIDs,
		TmpfsBytes: options.TmpfsBytes, LogSize: options.LogSize, LogFiles: options.LogFiles,
		ReplacementDiskBytes: options.ReplacementDiskBytes,
	}
}
