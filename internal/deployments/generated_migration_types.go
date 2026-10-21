package deployments

import (
	"time"
)

// GeneratedMigrationEnvironmentLease is the private staged environment owned
// by one generated migration invocation.
type GeneratedMigrationEnvironmentLease interface {
	Path() string
	Cleanup() error
}

// GeneratedMigrationEnvironmentStager writes caller-owned protected bytes to a
// private file and must clear the supplied buffer before returning.
type GeneratedMigrationEnvironmentStager interface {
	StageGeneratedMigration(operationID string, attempt int, contents []byte) (GeneratedMigrationEnvironmentLease, error)
}

// GeneratedMigrationProcessOptions are fixed at repository composition. None
// of these values can be selected by an invocation holder.
type GeneratedMigrationProcessOptions struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	Timeout               time.Duration
	OutputLimit           int
}
