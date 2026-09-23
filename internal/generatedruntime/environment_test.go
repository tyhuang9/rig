package generatedruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

func TestSecureEnvironmentStagerWritesAndRemovesProtectedEnvironment(t *testing.T) {
	manager, err := securetemp.New(filepath.Clean(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	stager, err := NewSecureEnvironmentStager(manager)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("TOKEN='synthetic-secret'\n")
	lease, err := stager.Stage(uuid.NewString(), 1, contents)
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range contents {
		if value != 0 {
			t.Fatalf("caller environment byte %d was not cleared", index)
		}
	}
	if body, err := os.ReadFile(lease.Path()); err != nil || string(body) != "TOKEN='synthetic-secret'\n" {
		t.Fatalf("protected environment mismatch: %q %v", body, err)
	}
	directory := filepath.Dir(lease.Path())
	if err := lease.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("environment directory retained: %v", err)
	}
}

func TestSecureEnvironmentStagerRejectsNULAndStillClearsInput(t *testing.T) {
	manager, err := securetemp.New(filepath.Clean(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	stager, _ := NewSecureEnvironmentStager(manager)
	contents := []byte{'A', '=', 'x', 0, 'y'}
	if _, err := stager.Stage(uuid.NewString(), 1, contents); err == nil {
		t.Fatal("NUL-bearing environment accepted")
	}
	for _, value := range contents {
		if value != 0 {
			t.Fatal("rejected environment was not cleared")
		}
	}
}
