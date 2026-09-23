package generatedruntime

import (
	"errors"

	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const maximumEnvironmentBytes = 64 << 10

type SecureEnvironmentStager struct{ temporary *securetemp.Manager }

func NewSecureEnvironmentStager(temporary *securetemp.Manager) (*SecureEnvironmentStager, error) {
	if temporary == nil {
		return nil, errors.New("generated runtime temporary manager is required")
	}
	return &SecureEnvironmentStager{temporary: temporary}, nil
}

type secureEnvironmentLease struct{ files *securetemp.Files }

func (l *secureEnvironmentLease) Path() string {
	if l == nil || l.files == nil {
		return ""
	}
	return l.files.EnvPath
}

func (l *secureEnvironmentLease) Cleanup() error {
	if l == nil || l.files == nil {
		return nil
	}
	return l.files.Cleanup()
}

func (s *SecureEnvironmentStager) Stage(operationID string, attempt int, contents []byte) (EnvironmentLease, error) {
	defer clear(contents)
	if s == nil || s.temporary == nil || len(contents) == 0 || len(contents) > maximumEnvironmentBytes {
		return nil, errors.New("invalid generated runtime environment")
	}
	for _, value := range contents {
		if value == 0 {
			return nil, errors.New("invalid generated runtime environment")
		}
	}
	files, err := s.temporary.Create(operationID, attempt)
	if err != nil {
		return nil, err
	}
	if err := files.WriteEnv(contents); err != nil {
		_ = files.Cleanup()
		return nil, err
	}
	return &secureEnvironmentLease{files: files}, nil
}
