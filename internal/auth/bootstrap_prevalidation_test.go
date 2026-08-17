package auth

import (
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

func TestBootstrapRejectsInvalidAndCompletedStateBeforeHashing(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := New(db)
	token, err := service.EnsureBootstrapToken()
	if err != nil || token == "" {
		t.Fatalf("EnsureBootstrapToken() = %q, %v", token, err)
	}
	realHasher := service.hashPassphrase
	hashCalls := 0
	service.hashPassphrase = func(passphrase string) (string, error) {
		hashCalls++
		return realHasher(passphrase)
	}

	if _, _, err := service.Bootstrap("wrong-token", "admin", "this is a secure passphrase"); err == nil {
		t.Fatal("invalid bootstrap token was accepted")
	}
	if hashCalls != 0 {
		t.Fatalf("invalid bootstrap token invoked password hashing %d times", hashCalls)
	}
	if _, _, err := service.Bootstrap(token, "admin", "this is a secure passphrase"); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 1 {
		t.Fatalf("valid bootstrap invoked password hashing %d times", hashCalls)
	}
	if _, _, err := service.Bootstrap(token, "second", "this is another secure passphrase"); err == nil {
		t.Fatal("completed bootstrap was accepted")
	}
	if hashCalls != 1 {
		t.Fatalf("completed bootstrap invoked password hashing %d times", hashCalls)
	}
}
