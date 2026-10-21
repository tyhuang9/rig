package generatedingress

import "time"

// generatedDomainOptions is the private fixed configuration shared by the
// future generated-domain construction path. It deliberately contains no
// caller-supplied manager, capacity source, callback, proof, or authority.
type generatedDomainOptions struct {
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

func validGeneratedDomainIdentityOptions(options generatedDomainOptions) bool {
	return validAbsoluteDirectory(options.DataRoot) && validOptions(Options{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint,
		DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
		HostPort: options.HostPort, CommandTimeout: options.CommandTimeout,
		PullTimeout: options.PullTimeout, OutputLimit: options.OutputLimit,
	}) && validGeneratedCandidateProcessOptions(generatedDomainCandidateOptions(options))
}

func generatedDomainCandidateOptions(options generatedDomainOptions) GeneratedCandidateProcessOptions {
	return GeneratedCandidateProcessOptions{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint,
		DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
		CommandTimeout: options.CommandTimeout, HealthTimeout: options.HealthTimeout,
		HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		MemoryBytes: options.MemoryBytes, MilliCPUs: options.MilliCPUs, PIDs: options.PIDs,
		TmpfsBytes: options.TmpfsBytes, LogSize: options.LogSize, LogFiles: options.LogFiles,
		ReplacementDiskBytes: options.ReplacementDiskBytes,
	}
}
