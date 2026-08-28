package controllerrelay

import (
	"net/http"
	"testing"
	"time"
)

func TestRelayControllerSessionURLUsesExactDerivedPath(t *testing.T) {
	for _, test := range []struct {
		origin string
		want   string
	}{
		{origin: "https://relay.example", want: "https://relay.example/v1/controllers/connect"},
		{origin: "https://relay.example:8443", want: "https://relay.example:8443/v1/controllers/connect"},
		{origin: "https://[::1]:8443", want: "https://[::1]:8443/v1/controllers/connect"},
	} {
		origin, err := parseCanonicalHTTPSOrigin(test.origin)
		if err != nil {
			t.Fatalf("parse %q: %v", test.origin, err)
		}
		got, err := relayControllerSessionURL(origin)
		if err != nil || got != test.want {
			t.Fatalf("session URL for %q = %q, %v; want %q", test.origin, got, err, test.want)
		}
		if origin.String() != test.origin {
			t.Fatalf("deriving session URL mutated origin: %q", origin)
		}
	}
	if _, err := relayControllerSessionURL(nil); err == nil {
		t.Fatal("nil relay origin accepted")
	}
}

func TestRelayHTTPClientOwnsRedirectAndTimeoutPolicy(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	client := newRelayHTTPClient(transport, 7*time.Second)
	if _, ok := client.Transport.(roundTripFunc); !ok || client.Timeout != 7*time.Second {
		t.Fatalf("unexpected relay HTTP client %#v", client)
	}
	request, err := http.NewRequest(http.MethodGet, "https://relay.example/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.CheckRedirect(request, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirect policy = %v", err)
	}
}
