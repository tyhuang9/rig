package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRelayHTTPSClientRequiresCanonicalHTTPSOrigin(t *testing.T) {
	invalid := []string{
		"", "http://relay.example", "https://relay.example/", "https://USER@relay.example",
		"https://relay.example?x=1", "https://relay.example#fragment", "HTTPS://relay.example",
		"https://RELAY.example", "https://relay.example:443", "https://relay.example.",
		"https://relay.example/path", "https://relay.example:0001",
	}
	for _, origin := range invalid {
		if _, err := NewRelayHTTPSClient(origin, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })); err == nil {
			t.Errorf("accepted noncanonical origin %q", origin)
		}
	}
	for _, origin := range []string{"https://relay.example", "https://relay.example:8443", "https://127.0.0.1:8443", "https://[::1]:8443"} {
		if _, err := NewRelayHTTPSClient(origin, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })); err != nil {
			t.Errorf("rejected canonical origin %q: %v", origin, err)
		}
	}
}

func TestRelayHTTPSClientDefaultTransportIsBoundedAndProxyFree(t *testing.T) {
	client, err := NewRelayHTTPSClient("https://relay.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport %T", client.client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default client consults an ambient proxy")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatal("TLS 1.2 minimum is not enforced")
	}
	if transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 || transport.MaxResponseHeaderBytes <= 0 || client.client.Timeout <= 0 {
		t.Fatal("transport time/header/body bounds are incomplete")
	}
}

func TestRelayHTTPSClientStartUsesExactWireContract(t *testing.T) {
	poll := bytes.Repeat([]byte{0x42}, pollTokenBytes)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://relay.example/v1/enrollments" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected headers %#v", request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err = json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"controllerId", "keyId", "publicKey", "installationId", "repositoryId", "requestNonce", "issuedAt", "expiresAt", "signature"} {
			if _, ok := wire[key]; !ok {
				t.Fatalf("missing wire field %q in %s", key, body)
			}
		}
		response := `{"authorizationUrl":"https://github.com/login/oauth/authorize?client_id=abc","pollToken":"` + base64.RawURLEncoding.EncodeToString(poll) + `"}`
		return jsonResponse(http.StatusCreated, response), nil
	})
	client, err := NewRelayHTTPSClient("https://relay.example", transport)
	if err != nil {
		t.Fatal(err)
	}
	started, err := client.Start(context.Background(), validClientEnrollmentRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer started.Destroy()
	if !bytes.Equal(started.PollToken, poll) || !strings.HasPrefix(started.AuthorizationURL, "https://github.com/") {
		t.Fatalf("unexpected start result %#v", started)
	}
}

func TestRelayHTTPSClientRejectsRedirectWithoutFollowing(t *testing.T) {
	followed := false
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer destination.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	client, err := NewRelayHTTPSClient(redirector.URL, redirector.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Start(context.Background(), validClientEnrollmentRequest())
	if err == nil || followed {
		t.Fatalf("redirect result err=%v followed=%v", err, followed)
	}
}

func TestRelayHTTPSClientRejectsOversizeDuplicateUnknownAndNonJSONResponses(t *testing.T) {
	poll := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, pollTokenBytes))
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "oversize", contentType: "application/json", body: strings.Repeat("x", maxRelayResponseBytes+1)},
		{name: "duplicate", contentType: "application/json", body: `{"authorizationUrl":"https://github.com/a","authorizationUrl":"https://github.com/b","pollToken":"` + poll + `"}`},
		{name: "unknown", contentType: "application/json", body: `{"authorizationUrl":"https://github.com/a","pollToken":"` + poll + `","token":"leak"}`},
		{name: "charset", contentType: "application/json; charset=utf-8", body: `{"authorizationUrl":"https://github.com/a","pollToken":"` + poll + `"}`},
		{name: "script URL", contentType: "application/json", body: `{"authorizationUrl":"javascript:alert(1)","pollToken":"` + poll + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewRelayHTTPSClient("https://relay.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
				response := &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}
				response.Header.Set("Content-Type", test.contentType)
				return response, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = client.Start(context.Background(), validClientEnrollmentRequest()); !IsClientCode(err, "invalid_response") {
				t.Fatalf("expected invalid_response, got %v", err)
			}
		})
	}
}

func TestRelayHTTPSClientPollStatusAndSecretFreeErrorMapping(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	responses := []struct {
		status int
		body   string
		code   string
	}{
		{status: http.StatusOK, body: `{"status":"pending"}`},
		{status: http.StatusOK, body: `{"status":"authorized","completedAt":"` + now.Format(time.RFC3339) + `"}`},
		{status: http.StatusOK, body: `{"status":"denied","completedAt":"` + now.Format(time.RFC3339) + `"}`},
		{status: http.StatusOK, body: `{"status":"expired","completedAt":"` + now.Format(time.RFC3339) + `"}`},
		{status: http.StatusOK, body: `{"status":"failed","failureCode":"oauth.private_provider_detail","completedAt":"` + now.Format(time.RFC3339) + `"}`},
		{status: http.StatusNotFound, body: `{"providerToken":"ghu_super_secret","detail":"private relay diagnostic"}`, code: "enrollment_not_found"},
		{status: http.StatusTooManyRequests, body: `{"detail":"private relay diagnostic"}`, code: "relay_rate_limited"},
		{status: http.StatusBadRequest, body: `{"detail":"private relay diagnostic"}`, code: "relay_rejected"},
		{status: http.StatusInternalServerError, body: `{"detail":"private relay diagnostic"}`, code: "relay_unavailable"},
	}
	for index, response := range responses {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			client, err := NewRelayHTTPSClient("https://relay.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
				result := jsonResponse(response.status, response.body)
				result.Header.Set("Retry-After", "999999")
				return result, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			status, pollErr := client.Poll(context.Background(), bytes.Repeat([]byte{7}, pollTokenBytes))
			if response.code == "" {
				if pollErr != nil || status.Status == "" {
					t.Fatalf("valid status rejected: status=%#v err=%v", status, pollErr)
				}
				return
			}
			if !IsClientCode(pollErr, response.code) {
				t.Fatalf("expected %s, got %v", response.code, pollErr)
			}
			errorText := fmt.Sprintf("%v %#v", pollErr, pollErr)
			if strings.Contains(errorText, "ghu_") || strings.Contains(errorText, "private relay diagnostic") {
				t.Fatalf("relay body leaked through error: %s", errorText)
			}
			var clientErr *ClientError
			if response.code == "relay_rate_limited" && (!errorsAs(pollErr, &clientErr) || clientErr.RetryAfter != 5*time.Minute) {
				t.Fatalf("retry-after was not safely bounded: %#v", clientErr)
			}
		})
	}
}

func TestRelayHTTPSClientRejectsInvalidPollShapes(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, body := range []string{
		`{"status":"pending","completedAt":"` + now + `"}`,
		`{"status":"authorized"}`,
		`{"status":"failed","completedAt":"` + now + `"}`,
		`{"status":"unknown"}`,
		`{"status":"pending","status":"authorized"}`,
	} {
		client, err := NewRelayHTTPSClient("https://relay.example", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, body), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Poll(context.Background(), bytes.Repeat([]byte{8}, pollTokenBytes)); !IsClientCode(err, "invalid_response") {
			t.Fatalf("invalid poll shape accepted %s: %v", body, err)
		}
	}
}

func validClientEnrollmentRequest() RelayEnrollmentRequest {
	privateKey := testPrivateKey(1)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	return RelayEnrollmentRequest{
		ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID,
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), InstallationID: 7, RepositoryID: 8,
		RequestNonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
		IssuedAt:     now, ExpiresAt: now.Add(10 * time.Minute),
		Signature: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, ed25519.SignatureSize)),
	}
}

func jsonResponse(status int, body string) *http.Response {
	response := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	response.Header.Set("Content-Type", "application/json")
	return response
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
