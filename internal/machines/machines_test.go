package machines_test

import (
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/runtime/docker"
)

func TestUpdateLocalDiagnosticsPersistsVersionsAndResources(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := machines.New(db)
	machine, err := store.EnsureLocal()
	if err != nil {
		t.Fatal(err)
	}
	resources := docker.HostResources{MemoryTotalBytes: 100, MemoryAvailableBytes: 40, DiskTotalBytes: 200, DiskAvailableBytes: 80}
	if err := store.UpdateLocalDiagnostics("27.1.2", "2.29.1", resources); err != nil {
		t.Fatal(err)
	}
	machine, err = store.Get(machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if machine.DockerVersion != "27.1.2" || machine.ComposeVersion != "2.29.1" || machine.Resources["memoryTotalBytes"] != float64(100) || machine.Resources["diskAvailableBytes"] != float64(80) {
		t.Fatalf("unexpected persisted machine diagnostics: %#v", machine)
	}
}
