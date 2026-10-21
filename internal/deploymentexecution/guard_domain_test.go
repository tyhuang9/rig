package deploymentexecution

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestGuardConfiguredForDomainRejectsUnsupportedExactWorkingDirectory(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	positive := &blockingPositiveFence{db: fixture.db}

	legacyCompatible := `/??/effects`
	if runtime.GOOS == "windows" {
		legacyCompatible = `\\domain.invalid\share\effects`
	}
	nulDirectory := filepath.Join(t.TempDir(), "effects") + "\x00suffix"
	for name, directory := range map[string]string{
		"legacy-compatible namespace": legacyCompatible,
		"UNC":                         `\\domain.invalid\share\effects`,
		"slash-normalized UNC":        `//domain.invalid/share/effects`,
		"object manager":              `\??\C:\effects`,
		"device":                      `\Device\HarddiskVolume1\effects`,
		"absolute NUL":                nulDirectory,
	} {
		t.Run(name, func(t *testing.T) {
			guard := &Guard{
				db:               fixture.db,
				bindings:         fixture.bindings,
				positiveFence:    positive,
				workingDirectory: directory,
			}
			if guard.ConfiguredForDomain(fixture.db, directory) {
				t.Fatal("unsupported exact working-directory spelling accepted")
			}
		})
	}
	for name, directory := range map[string]string{
		"legacy-compatible namespace": legacyCompatible,
		"absolute NUL":                nulDirectory,
	} {
		if !validWorkingDirectory(directory) {
			t.Fatalf("%s did not reach the stronger domain syntax check", name)
		}
		if validDomainWorkingDirectory(directory) {
			t.Fatalf("%s passed the stronger domain syntax check", name)
		}
	}

	var zero Guard
	if zero.ConfiguredForDomain(fixture.db, fixture.directory) {
		t.Fatal("zero guard reported domain configuration")
	}
}
