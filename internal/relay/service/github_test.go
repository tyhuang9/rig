package service

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAppJWTUsesExactGitHubClaimsAndRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	token, err := appJWT(12345, key, now, bytes.NewReader(bytes.Repeat([]byte{7}, 512)))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(token), ".")
	if len(parts) != 3 {
		t.Fatalf("JWT parts = %d", len(parts))
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header = %s", header)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "12345" || claims.IssuedAt != now.Add(-time.Minute).Unix() || claims.ExpiresAt != now.Add(9*time.Minute).Unix() {
		t.Fatalf("claims = %+v", claims)
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature: %v", err)
	}
}

func TestOAuthRedirectIsNotFollowedAndProviderFailureIsSanitized(t *testing.T) {
	calls := 0
	transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "https://github.com/login/oauth/access_token" {
			t.Fatalf("request URL=%s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/steal"}},
			Body: io.NopCloser(strings.NewReader("provider-sensitive-body")), Request: request,
		}, nil
	})
	s := newEnrollmentTestService(t, &enrollmentStore{}, transport, time.Now().UTC())
	_, err := s.exchangeOAuthCode(t.Context(), []byte("provider-code"), bytes.Repeat([]byte{'v'}, 43), 8)
	if err == nil || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	for _, secret := range []string{"provider-sensitive-body", "evil.example", "provider-code"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider error leaked %q: %v", secret, err)
		}
	}
}

func TestProviderResponseAndPageCardinalityAreBounded(t *testing.T) {
	t.Run("oversized body", func(t *testing.T) {
		transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxProviderBody+1))), Request: request}, nil
		})
		s := newEnrollmentTestService(t, &enrollmentStore{}, transport, time.Now().UTC())
		var target any
		if err := s.getUserJSON(t.Context(), []byte("ghu_access"), "/user", nil, &target); err == nil || strings.Contains(err.Error(), strings.Repeat("x", 32)) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("101 installations", func(t *testing.T) {
		installations := make([]map[string]int64, 101)
		for i := range installations {
			installations[i] = map[string]int64{"id": int64(i + 1)}
		}
		page, _ := json.Marshal(map[string]any{"installations": installations})
		calls := 0
		transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
			calls++
			body := []byte(`{"id":1}`)
			if request.URL.Path == "/user/installations" {
				body = page
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
		})
		s := newEnrollmentTestService(t, &enrollmentStore{}, transport, time.Now().UTC())
		if err := s.verifyUserRepositoryAccess(t.Context(), []byte("ghu_access"), 7, 8); err == nil || calls != 2 {
			t.Fatalf("error=%v calls=%d", err, calls)
		}
	})
}

func TestLiveAuthorizationPageExhaustionIsBoundedAndSanitized(t *testing.T) {
	full := make([]map[string]int64, 100)
	for i := range full {
		full[i] = map[string]int64{"id": int64(1000 + i)}
	}
	installationPage, _ := json.Marshal(map[string]any{"installations": full})
	repositoryPage, _ := json.Marshal(map[string]any{"repositories": full})
	tests := []struct {
		name      string
		transport func(*http.Request) []byte
		calls     int
	}{
		{
			name: "installation pages",
			transport: func(request *http.Request) []byte {
				if request.URL.Path == "/user" {
					return []byte(`{"id":1}`)
				}
				if request.URL.Path != "/user/installations" {
					t.Fatalf("unexpected path %s", request.URL.Path)
				}
				return installationPage
			},
			calls: 101,
		},
		{
			name: "repository pages",
			transport: func(request *http.Request) []byte {
				switch request.URL.Path {
				case "/user":
					return []byte(`{"id":1}`)
				case "/user/installations":
					return []byte(`{"installations":[{"id":7}]}`)
				case "/user/installations/7/repositories":
					return repositoryPage
				default:
					t.Fatalf("unexpected path %s", request.URL.Path)
					return nil
				}
			},
			calls: 102,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
				calls++
				body := test.transport(request)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
			})
			s := newEnrollmentTestService(t, &enrollmentStore{}, transport, time.Now().UTC())
			err := s.verifyUserRepositoryAccess(t.Context(), []byte("ghu_access"), 7, 8)
			if err == nil || err.Error() != "github request failed: api.pages" || calls != test.calls {
				t.Fatalf("error=%v calls=%d, want %d", err, calls, test.calls)
			}
		})
	}
}

func TestLiveAuthorizationPaginatesUserInstallationsAndExactRepositoryMembership(t *testing.T) {
	firstPage := make([]map[string]int64, 100)
	for i := range firstPage {
		firstPage[i] = map[string]int64{"id": int64(1000 + i)}
	}
	installationPage, _ := json.Marshal(map[string]any{"installations": firstPage})
	repositoryPage, _ := json.Marshal(map[string]any{"repositories": firstPage})
	calls := 0
	transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
		calls++
		var body []byte
		switch {
		case request.URL.Path == "/user":
			body = []byte(`{"id":1}`)
		case request.URL.Path == "/user/installations" && request.URL.Query().Get("page") == "1":
			body = installationPage
		case request.URL.Path == "/user/installations" && request.URL.Query().Get("page") == "2":
			body = []byte(`{"installations":[{"id":7}]}`)
		case request.URL.Path == "/user/installations/7/repositories" && request.URL.Query().Get("page") == "1":
			body = repositoryPage
		case request.URL.Path == "/user/installations/7/repositories" && request.URL.Query().Get("page") == "2":
			body = []byte(`{"repositories":[{"id":8}]}`)
		default:
			t.Fatalf("unexpected request %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	})
	s := newEnrollmentTestService(t, &enrollmentStore{}, transport, time.Now().UTC())
	if err := s.verifyUserRepositoryAccess(t.Context(), []byte("ghu_access"), 7, 8); err != nil || calls != 5 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
