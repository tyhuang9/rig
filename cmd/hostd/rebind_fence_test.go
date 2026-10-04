package main

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

func TestRebindFenceCheckRequiresReadableValidatedSnapshot(t *testing.T) {
	if rebindFenceCheck(nil) != nil {
		t.Fatal("nil database produced a permissive fence check")
	}
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	check := rebindFenceCheck(db)
	if check == nil {
		t.Fatal("database did not produce a fence check")
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("fresh database was fenced: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fence read error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background()); err == nil {
		t.Fatal("closed database was treated as a clear fence")
	}
}
