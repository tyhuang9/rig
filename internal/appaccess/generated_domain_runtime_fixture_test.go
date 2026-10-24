package appaccess

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedDomainRuntimeSeed identifies the one genuine generated deployment
// that the external sealed-domain fixture should activate.
type GeneratedDomainRuntimeSeed struct {
	AppID     string
	ReleaseID string
}

// GeneratedDomainRuntimeFixture supplies only actual current runtime facts to
// appaccess tests. The external test package owns its concrete generated
// domain so this package never gains a production dependency on runtime code.
type GeneratedDomainRuntimeFixture interface {
	Database() *sql.DB
	OpenSealedReader(t *testing.T) *sql.DB
	Activate(t *testing.T, seed GeneratedDomainRuntimeSeed) (generatedruntimestate.ActiveHead, []GatewayStartupRuntimeComponent)
	Close()
}

var generatedDomainRuntimeFixtureRegistration struct {
	sync.Mutex
	adapter func(*testing.T) GeneratedDomainRuntimeFixture
}

// RegisterGeneratedDomainRuntimeFixtureAdapter installs the one external test
// implementation. It is test-only and deliberately rejects registration
// ambiguity instead of selecting an adapter by package initialization order.
func RegisterGeneratedDomainRuntimeFixtureAdapter(adapter func(*testing.T) GeneratedDomainRuntimeFixture) {
	if adapter == nil {
		panic("appaccess generated domain runtime fixture adapter is nil")
	}
	generatedDomainRuntimeFixtureRegistration.Lock()
	defer generatedDomainRuntimeFixtureRegistration.Unlock()
	if generatedDomainRuntimeFixtureRegistration.adapter != nil {
		panic("appaccess generated domain runtime fixture adapter is already registered")
	}
	generatedDomainRuntimeFixtureRegistration.adapter = adapter
}

func newGeneratedDomainRuntimeFixture(t *testing.T) GeneratedDomainRuntimeFixture {
	t.Helper()
	generatedDomainRuntimeFixtureRegistration.Lock()
	adapter := generatedDomainRuntimeFixtureRegistration.adapter
	generatedDomainRuntimeFixtureRegistration.Unlock()
	if adapter == nil {
		t.Fatal("appaccess generated domain runtime fixture adapter is unavailable")
	}
	fixture := adapter(t)
	if fixture == nil {
		t.Fatal("appaccess generated domain runtime fixture adapter returned nil")
	}
	return fixture
}
