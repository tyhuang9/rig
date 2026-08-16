package auth_test

import (
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
)

func TestBootstrapLoginAndLogout(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := auth.New(db)
	token, err := s.EnsureBootstrapToken()
	if err != nil || token == "" {
		t.Fatalf("token: %v", err)
	}
	u, session, err := s.Bootstrap(token, "admin", "this is a secure passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(session.Token); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.RotateCSRF(session.Token)
	if err != nil || rotated == "" || rotated == session.CSRF {
		t.Fatalf("RotateCSRF() = %q, %v", rotated, err)
	}
	_, csrfHash, err := s.Authenticate(session.Token)
	if err != nil || !s.CheckCSRF(csrfHash, rotated) || s.CheckCSRF(csrfHash, session.CSRF) {
		t.Fatal("CSRF rotation did not replace the stored hash")
	}
	if err := s.Logout(session.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(session.Token); err == nil {
		t.Fatal("revoked session authenticated")
	}
	if _, _, err := s.Login(u.Username, "wrong passphrase"); err == nil {
		t.Fatal("invalid credentials accepted")
	}
	if _, _, err := s.Login(u.Username, "this is a secure passphrase"); err != nil {
		t.Fatal(err)
	}
}
