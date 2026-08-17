package githubapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDeviceAndTokenRequestsUseFixedOriginsFormsAndSanitizedErrors(t *testing.T) {
	var calls int
	client := testClient(t, func(request *http.Request) *http.Response {
		calls++
		if request.URL.Host != "github.com" || request.URL.Scheme != "https" || request.URL.RawQuery != "" {
			t.Fatalf("request URL = %s", request.URL.String())
		}
		if request.Header.Get("X-GitHub-Api-Version") != APIVersion || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("headers = %#v", request.Header)
		}
		if request.Header.Get("User-Agent") != userAgent {
			t.Fatalf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(request.Body)
		if calls == 1 {
			if string(body) != "client_id=client-123" {
				t.Fatalf("device body = %q", body)
			}
			return jsonResponse(200, `{"device_code":"device-secret","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
		}
		if strings.Contains(request.URL.String(), "device-secret") || !strings.Contains(string(body), "device_code=device-secret") {
			t.Fatalf("poll request leaked credential in URL or omitted form: %s %q", request.URL, body)
		}
		return jsonResponse(200, `{"error":"access_denied","error_description":"raw provider secret description"}`)
	})

	device, err := client.StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.VerificationURI != VerificationURI || device.DeviceCode != "device-secret" {
		t.Fatalf("device = %#v", device)
	}
	_, err = client.PollDevice(context.Background(), device.DeviceCode)
	if !IsCode(err, "access_denied") || strings.Contains(err.Error(), "description") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("sanitized poll error = %v", err)
	}
}

func TestTokenRequiresExpiringAccessAndRefreshCredentials(t *testing.T) {
	client := testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"access_token":"ghu_example","refresh_token":"ghr_example","token_type":"bearer","expires_in":28800,"refresh_token_expires_in":15897600}`)
	})
	bundle, err := client.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccessExpiresIn.Seconds() != 28800 || bundle.RefreshExpiresIn.Seconds() != 15897600 {
		t.Fatalf("bundle expiries = %#v", bundle)
	}

	client = testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"access_token":"access","refresh_token":"refresh","token_type":"bearer"}`)
	})
	if _, err := client.Refresh(context.Background(), "old-refresh"); !IsCode(err, "invalid_response") {
		t.Fatalf("missing expiries error = %v", err)
	}

	client = testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"access_token":"access","refresh_token":"refresh","token_type":"bearer","expires_in":2147483647,"refresh_token_expires_in":2147483647}`)
	})
	if _, err := client.Refresh(context.Background(), "old-refresh"); !IsCode(err, "invalid_response") {
		t.Fatalf("oversized expiries error = %v", err)
	}
}

func TestCurrentUserAndInstallationsUseBoundedCanonicalAPIRequests(t *testing.T) {
	var calls int
	client := testClient(t, func(request *http.Request) *http.Response {
		calls++
		if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("API request = %s headers=%#v", request.URL, request.Header)
		}
		if calls == 1 {
			if request.URL.Path != "/user" || request.URL.RawQuery != "" {
				t.Fatalf("user URL = %s", request.URL)
			}
			return jsonResponse(200, `{"id":42,"login":"octocat"}`)
		}
		if request.URL.Path != "/user/installations" || request.URL.Query().Get("page") != "2" || request.URL.Query().Get("per_page") != "100" {
			t.Fatalf("installations URL = %s", request.URL)
		}
		return jsonResponse(200, `{"total_count":1,"installations":[{"id":7,"account":{"login":"acme","type":"Organization"},"target_type":"Organization","repository_selection":"selected","suspended_at":null}]}`)
	})
	user, err := client.CurrentUser(context.Background(), "access")
	if err != nil || user.ID != "42" || user.Login != "octocat" {
		t.Fatalf("user = %#v, err = %v", user, err)
	}
	page, err := client.Installations(context.Background(), "access", 2, 100)
	if err != nil || len(page.Installations) != 1 || page.Installations[0].ID != 7 {
		t.Fatalf("page = %#v, err = %v", page, err)
	}
	if _, err := client.Installations(context.Background(), "access", 1, 101); !IsCode(err, "invalid_request") {
		t.Fatalf("perPage bound error = %v", err)
	}
}

func TestClientRejectsRedirectsOversizedBodiesAndUnknownProviderErrors(t *testing.T) {
	client := testClient(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/steal"}}, Body: io.NopCloser(strings.NewReader("redirect"))}
	})
	if _, err := client.StartDevice(context.Background()); !IsCode(err, "provider_rejected") {
		t.Fatalf("redirect error = %v", err)
	}

	client = testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"padding":"`+strings.Repeat("x", maxResponseBytes)+`"}`)
	})
	if _, err := client.StartDevice(context.Background()); !IsCode(err, "response_too_large") {
		t.Fatalf("oversized error = %v", err)
	}

	client = testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"error":"provider_added_new_error","error_description":"never expose me"}`)
	})
	_, err := client.PollDevice(context.Background(), "device")
	if !IsCode(err, "oauth_failed") || strings.Contains(err.Error(), "expose") {
		t.Fatalf("unknown OAuth error = %v", err)
	}
}

func TestNewRejectsUnsafeClientIDs(t *testing.T) {
	for _, value := range []string{"", "with space", "line\nbreak", strings.Repeat("x", 256)} {
		if _, err := New(value); err == nil {
			t.Errorf("New(%q) succeeded", value)
		}
	}
}

func testClient(t *testing.T, response func(*http.Request) *http.Response) *Client {
	t.Helper()
	client, err := newWithTransport("client-123", roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestForbiddenRateLimitClassificationAndRedaction(t *testing.T) {
	client := testClient(t, func(*http.Request) *http.Response {
		response := jsonResponse(http.StatusForbidden, `{"message":"secret provider description"}`)
		response.Header.Set("X-RateLimit-Remaining", "0")
		return response
	})
	if _, err := client.CurrentUser(context.Background(), "ghu_sensitive"); !IsCode(err, "rate_limited") {
		t.Fatalf("rate-limit error = %v", err)
	}
	bundle := TokenBundle{AccessToken: "ghu_sensitive", RefreshToken: "ghr_sensitive"}
	if rendered := bundle.String() + bundle.GoString(); strings.Contains(rendered, "sensitive") {
		t.Fatalf("token bundle rendered secrets: %s", rendered)
	}
	authorization := DeviceAuthorization{DeviceCode: "device-sensitive", UserCode: "ABCD"}
	if rendered := authorization.String() + authorization.GoString(); strings.Contains(rendered, "device-sensitive") || strings.Contains(rendered, "ABCD") {
		t.Fatalf("device authorization rendered one-time material: %s", rendered)
	}
	encoded, err := json.Marshal(struct {
		Device DeviceAuthorization `json:"device"`
		Tokens TokenBundle         `json:"tokens"`
	}{authorization, bundle})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sensitive") {
		t.Fatalf("JSON rendered credentials: %s", encoded)
	}
}

func TestOAuthDeviceOutcomesAreAllowlisted(t *testing.T) {
	for providerCode, want := range map[string]string{
		"authorization_pending": "authorization_pending",
		"slow_down":             "slow_down",
		"expired_token":         "expired_token",
		"access_denied":         "access_denied",
	} {
		t.Run(providerCode, func(t *testing.T) {
			client := testClient(t, func(*http.Request) *http.Response {
				return jsonResponse(200, `{"error":"`+providerCode+`","error_description":"raw description"}`)
			})
			if _, err := client.PollDevice(context.Background(), "device"); !IsCode(err, want) || strings.Contains(err.Error(), "raw") {
				t.Fatalf("outcome error = %v", err)
			}
		})
	}
}

func TestRefreshGrantContainsNoSecretOrQueryCredentials(t *testing.T) {
	client := testClient(t, func(request *http.Request) *http.Response {
		if request.URL.RawQuery != "" {
			t.Fatalf("refresh query = %q", request.URL.RawQuery)
		}
		body, _ := io.ReadAll(request.Body)
		got := string(body)
		if got != "client_id=client-123&grant_type=refresh_token&refresh_token=refresh-sensitive" || strings.Contains(got, "client_secret") {
			t.Fatalf("refresh form = %q", got)
		}
		return jsonResponse(200, `{"access_token":"access","refresh_token":"refresh","token_type":"bearer","expires_in":3600,"refresh_token_expires_in":7200}`)
	})
	if _, err := client.Refresh(context.Background(), "refresh-sensitive"); err != nil {
		t.Fatal(err)
	}
}

func TestTransportCancellationMalformedAndTrailingJSON(t *testing.T) {
	client, err := newWithTransport("client-123", roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.StartDevice(ctx); !IsCode(err, "provider_unavailable") {
		t.Fatalf("cancelled request error = %v", err)
	}

	for name, body := range map[string]string{"malformed": `{`, "trailing": `{} {}`} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(*http.Request) *http.Response { return jsonResponse(200, body) })
			if _, err := client.StartDevice(context.Background()); !IsCode(err, "invalid_response") {
				t.Fatalf("JSON error = %v", err)
			}
		})
	}
}

func TestHTTPStatusClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header http.Header
		code   string
	}{
		{"unauthorized", 401, nil, "unauthorized"},
		{"forbidden", 403, nil, "forbidden"},
		{"rate-limited-retry", 403, http.Header{"Retry-After": {"10"}}, "rate_limited"},
		{"rate-limited-remaining", 403, http.Header{"X-RateLimit-Remaining": {"0"}}, "rate_limited"},
		{"too-many", 429, nil, "rate_limited"},
		{"server", 503, nil, "provider_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, func(*http.Request) *http.Response {
				response := jsonResponse(test.status, `{"message":"raw provider body"}`)
				for key, values := range test.header {
					for _, value := range values {
						response.Header.Add(key, value)
					}
				}
				return response
			})
			_, err := client.CurrentUser(context.Background(), "access")
			if !IsCode(err, test.code) || strings.Contains(err.Error(), "raw") {
				t.Fatalf("status error = %v", err)
			}
		})
	}
}

func TestInstallationsRejectInvalidProviderPayloads(t *testing.T) {
	for name, body := range map[string]string{
		"total":     `{"total_count":0,"installations":[{"id":7,"account":{"login":"acme","type":"Organization"},"target_type":"Organization","repository_selection":"selected"}]}`,
		"account":   `{"total_count":1,"installations":[{"id":7,"account":{"login":"acme","type":"Unknown"},"target_type":"Organization","repository_selection":"selected"}]}`,
		"selection": `{"total_count":1,"installations":[{"id":7,"account":{"login":"acme","type":"Organization"},"target_type":"Organization","repository_selection":"private"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(*http.Request) *http.Response { return jsonResponse(200, body) })
			if _, err := client.Installations(context.Background(), "access", 1, 10); !IsCode(err, "invalid_response") {
				t.Fatalf("invalid payload error = %v", err)
			}
		})
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
