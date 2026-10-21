package controllerowner

import (
	"context"
	"sync"
	"testing"
)

func TestControllerOwnerLeaseValidatesExactLiveDirectory(t *testing.T) {
	directory := ownerTestDirectory(t)
	lease, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ValidateForDirectory(directory); err != nil {
		t.Fatalf("live lease validation: %v", err)
	}
	foreign := ownerTestDirectory(t)
	if err := lease.ValidateForDirectory(foreign); err == nil {
		t.Fatal("lease validated for a foreign controller directory")
	}
	var zero Lease
	if err := zero.ValidateForDirectory(directory); err == nil {
		t.Fatal("zero lease validated")
	}
	if err := (*Lease)(nil).ValidateForDirectory(directory); err == nil {
		t.Fatal("nil lease validated")
	}

	copiedValue := *lease
	copyView := &copiedValue
	if err := copyView.ValidateForDirectory(directory); err == nil {
		t.Fatal("copied lease view validated")
	}
	if err := copyView.Close(); err == nil {
		t.Fatal("copied lease view closed the shared lock")
	}
	if err := lease.ValidateForDirectory(directory); err != nil {
		t.Fatalf("copy operation changed original lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.ValidateForDirectory(directory); err == nil {
		t.Fatal("closed lease validated")
	}
}

func TestControllerOwnerLeaseCloseAndValidationAreRaceSafe(t *testing.T) {
	directory := ownerTestDirectory(t)
	lease, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		for range 100 {
			_ = lease.ValidateForDirectory(directory)
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		_ = lease.Close()
	}()
	close(start)
	wait.Wait()
	if err := lease.ValidateForDirectory(directory); err == nil {
		t.Fatal("lease validated after concurrent Close completed")
	}
}
