package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/jobs"
)

func TestDeploymentJobCompositionSuppliesConcreteLifecycleValidator(t *testing.T) {
	if service, bindings, err := newDeploymentJobService(nil); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) || service != nil || bindings != nil {
		t.Fatalf("nil composition service=%v bindings=%v err=%v", service, bindings, err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, bindings, err := newDeploymentJobService(db)
	if err != nil || service == nil || bindings == nil || !bindings.UsesDatabase(db) {
		t.Fatalf("composition service=%v bindings=%v err=%v", service, bindings, err)
	}
	appID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, "composition-"+appID, "Composition", now, now); err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "composition", RequestedBy: "composition-actor",
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !result.Created {
		t.Fatalf("composed create result=%#v err=%v", result, err)
	}
	persisted, err := bindings.Read(context.Background(), result.Job.ID)
	if err != nil || !persisted.Equal(result.Binding) || persisted.ApplicationID() != appID || persisted.Mode() != deploymentbinding.ModeLegacyUninitialized {
		t.Fatalf("composed binding job=%q app=%q mode=%q err=%v", persisted.JobID(), persisted.ApplicationID(), persisted.Mode(), err)
	}
}
