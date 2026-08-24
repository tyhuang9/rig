package service

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/url"
	"testing"
	"time"
)

type fakeHTTP func(*http.Request) (*http.Response, error)

func (f fakeHTTP) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestNewRejectsMissingDependencies(t *testing.T) {
	if _, err := New(Options{}); err != ErrInvalidOptions {
		t.Fatalf("New error = %v", err)
	}
}

func TestValidPublicBaseURLRejectsAlternateRepresentations(t *testing.T) {
	tests := []struct {
		name        string
		value       *url.URL
		development bool
		valid       bool
	}{
		{name: "https", value: &url.URL{Scheme: "https", Host: "relay.example.test"}, valid: true},
		{name: "https slash", value: &url.URL{Scheme: "https", Host: "relay.example.test", Path: "/"}, valid: true},
		{name: "loopback development", value: &url.URL{Scheme: "http", Host: "127.0.0.1:8080"}, development: true, valid: true},
		{name: "loopback production", value: &url.URL{Scheme: "http", Host: "127.0.0.1:8080"}},
		{name: "opaque", value: &url.URL{Scheme: "https", Host: "relay.example.test", Opaque: "alternate"}},
		{name: "raw path", value: &url.URL{Scheme: "https", Host: "relay.example.test", Path: "/", RawPath: "%2f"}},
		{name: "raw fragment", value: &url.URL{Scheme: "https", Host: "relay.example.test", RawFragment: "alternate"}},
		{name: "force query", value: &url.URL{Scheme: "https", Host: "relay.example.test", ForceQuery: true}},
		{name: "query", value: &url.URL{Scheme: "https", Host: "relay.example.test", RawQuery: "x=1"}},
		{name: "fragment", value: &url.URL{Scheme: "https", Host: "relay.example.test", Fragment: "x"}},
		{name: "path", value: &url.URL{Scheme: "https", Host: "relay.example.test", Path: "/prefix"}},
		{name: "userinfo", value: &url.URL{Scheme: "https", Host: "relay.example.test", User: url.User("user")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validPublicBaseURL(test.value, test.development); got != test.valid {
				t.Fatalf("validPublicBaseURL() = %t, want %t", got, test.valid)
			}
		})
	}

	s := &Service{publicBaseURL: &url.URL{Scheme: "https", Host: "relay.example.test"}}
	if got := s.callbackURL(); got != "https://relay.example.test/v1/github/callback" {
		t.Fatalf("callbackURL() = %q", got)
	}
}

func TestCloseDestroysCopiedSecrets(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://relay.example.test")
	secret := bytes.Repeat([]byte{1}, 32)
	s, err := New(Options{
		Transport: fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }),
		Store:     &fakeStore{}, Now: time.Now, Random: rand.Reader, PublicBaseURL: base,
		GitHubClientID: "client", GitHubClientSecret: bytes.Repeat([]byte{2}, 16), GitHubAppID: 1,
		GitHubPrivateKey: key, WebhookSecret: secret, EnrollmentKey: secret,
		RecoveryWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, material := range [][]byte{s.githubClientSecret, s.webhookSecret, s.enrollmentKey} {
		if !bytes.Equal(material, make([]byte, len(material))) {
			t.Fatal("service secret was not destroyed")
		}
	}
	if bytes.Equal(secret, make([]byte, len(secret))) {
		t.Fatal("caller-owned secret was modified")
	}
}

type fakeStore struct{ Store }
