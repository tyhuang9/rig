package main

import (
	"context"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	"github.com/hostd/hostd/internal/runtime/docker"
)

func TestDeploymentEffectsAdmissionReleasesLockWhenFenceReadFails(t *testing.T) {
	root := t.TempDir()
	directories, err := docker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := deploymentEffectsAdmission(nil, directories.WorkingDirectory); err == nil {
		t.Fatal("admission accepted a missing database")
	}
	admit, err := deploymentEffectsAdmission(db, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	release, err := admit(context.Background())
	if err != nil {
		t.Fatalf("fresh database admission: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE lan_gateway_rebind_claims`); err != nil {
		t.Fatal(err)
	}
	if release, err := admit(context.Background()); err == nil || release != nil {
		t.Fatalf("corrupt fence admitted release=%t error=%v", release != nil, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err = deploymenteffects.Acquire(ctx, directories.WorkingDirectory)
	if err != nil {
		t.Fatalf("failed fence retained deployment effects lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
