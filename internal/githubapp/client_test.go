package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestArchiveFollowsOnlyCanonicalCodeloadWithoutAuthorization(t *testing.T) {
	calls := 0
	client := testClient(t, func(request *http.Request) *http.Response {
		calls++
		if calls == 1 {
			if request.URL.String() != "https://api.github.com/repos/octo/repo/tarball/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || request.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("initial archive request = %s %#v", request.URL, request.Header)
			}
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://codeload.github.com/octo/repo/tar.gz/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, Body: io.NopCloser(strings.NewReader(""))}
		}
		if request.URL.Host != "codeload.github.com" || request.Header.Get("Authorization") != "" {
			t.Fatalf("redirect leaked authorization: %s %#v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("archive"))}
	})
	body, err := client.Archive(context.Background(), "access", "octo", "repo", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if got, _ := io.ReadAll(body); string(got) != "archive" || calls != 2 {
		t.Fatalf("archive = %q calls=%d", got, calls)
	}
	for _, location := range []string{"http://codeload.github.com/a", "https://codeload.github.com:443/a", "https://user@codeload.github.com/a", "https://codeload.github.com/a?token=x", "https://evil.example/a", "https://codeload.github.com/a/../b", "https://codeload.github.com/octo/repo/tar.gz/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "/relative"} {
		if _, err := validArchiveRedirect(location, "octo", "repo", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
			t.Fatalf("unsafe redirect accepted: %q", location)
		}
	}
	if _, err := validArchiveRedirect("https://codeload.github.com/other/repo/tar.gz/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "octo", "repo", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("cross-repository redirect accepted")
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

func TestRepositoriesBranchesAndRenameUseImmutableIDsAndBoundedPagination(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls int
	client := testClient(t, func(request *http.Request) *http.Response {
		calls++
		if request.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch calls {
		case 1:
			if request.URL.Path != "/user/installations/7/repositories" || request.URL.Query().Get("page") != "2" || request.URL.Query().Get("per_page") != "50" {
				t.Fatalf("repository page URL = %s", request.URL)
			}
			return jsonResponse(200, `{"total_count":1,"repositories":[{"id":41,"owner":{"login":"old-owner"},"name":"repo","default_branch":"main","private":true}]}`)
		case 2:
			if request.URL.Path != "/user/installations/7/repositories" || request.URL.Query().Get("page") != "1" || request.URL.Query().Get("per_page") != "100" {
				t.Fatalf("repository lookup page one URL = %s", request.URL)
			}
			return jsonResponse(200, repositoryPageJSON(101, 1, 100))
		case 3:
			if request.URL.Path != "/user/installations/7/repositories" || request.URL.Query().Get("page") != "2" || request.URL.Query().Get("per_page") != "100" {
				t.Fatalf("repository lookup page two URL = %s", request.URL)
			}
			return jsonResponse(200, `{"total_count":101,"repositories":[{"id":141,"owner":{"login":"new-owner"},"name":"renamed","default_branch":"release/v1","private":true}]}`)
		case 4:
			if request.URL.Path != "/repos/new-owner/renamed/branches" || request.URL.Query().Get("page") != "1" {
				t.Fatalf("branches URL = %s", request.URL)
			}
			return jsonResponse(200, `[{"name":"release/v1","commit":{"sha":"`+sha+`"},"protected":true}]`)
		case 5:
			if request.URL.EscapedPath() != "/repos/new-owner/renamed/branches/release%2Fv1" {
				t.Fatalf("branch URL = %s escaped=%s", request.URL, request.URL.EscapedPath())
			}
			return jsonResponse(200, `{"name":"release/v1","commit":{"sha":"`+sha+`"},"protected":true}`)
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil
		}
	})
	page, err := client.Repositories(context.Background(), "access", 7, 2, 50)
	if err != nil || len(page.Repositories) != 1 || page.Repositories[0].ID != 41 {
		t.Fatalf("repository page = %#v err=%v", page, err)
	}
	repository, err := client.Repository(context.Background(), "access", 7, 141)
	if err != nil || repository.Owner != "new-owner" || repository.Name != "renamed" {
		t.Fatalf("repository = %#v err=%v", repository, err)
	}
	branches, err := client.Branches(context.Background(), "access", repository.Owner, repository.Name, 1, 100)
	if err != nil || len(branches.Branches) != 1 || branches.Branches[0].Name != "release/v1" {
		t.Fatalf("branches = %#v err=%v", branches, err)
	}
	branch, err := client.Branch(context.Background(), "access", repository.Owner, repository.Name, "release/v1")
	if err != nil || branch.SHA != sha {
		t.Fatalf("branch = %#v err=%v", branch, err)
	}
}

func TestRepositoryAndBranchValidationRejectsMismatchesAndUnsafeNames(t *testing.T) {
	client := testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"total_count":1,"repositories":[{"id":9,"owner":{"login":"octo"},"name":"repo","default_branch":"main"}]}`)
	})
	if _, err := client.Repository(context.Background(), "access", 7, 8); !IsCode(err, "not_found") {
		t.Fatalf("missing immutable ID error = %v", err)
	}
	for _, branch := range []string{"../main", "/main", "main.lock", "feature\\bad", "bad name"} {
		if _, err := client.Branch(context.Background(), "access", "octo", "repo", branch); !IsCode(err, "invalid_request") {
			t.Errorf("branch %q error = %v", branch, err)
		}
	}
	for _, identity := range [][2]string{{"../octo", "repo"}, {"octo", "../repo"}, {"octo/org", "repo"}, {"octo", "repo name"}} {
		if _, err := client.Branch(context.Background(), "access", identity[0], identity[1], "main"); !IsCode(err, "invalid_request") {
			t.Errorf("repository identity %q/%q error = %v", identity[0], identity[1], err)
		}
	}
	if _, err := client.Repositories(context.Background(), "access", 7, 1, 101); !IsCode(err, "invalid_request") {
		t.Fatalf("pagination error = %v", err)
	}
	client = testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `[{"name":"main","commit":{"sha":"not-a-sha"}}]`)
	})
	if _, err := client.Branches(context.Background(), "access", "octo", "repo", 1, 30); !IsCode(err, "invalid_response") {
		t.Fatalf("invalid branch payload error = %v", err)
	}
}

func TestRepositoryLookupRejectsInconsistentTotalsDuplicatesAndOversizedCollections(t *testing.T) {
	tests := map[string]struct {
		responses []string
		code      string
	}{
		"inconsistent total": {
			responses: []string{repositoryPageJSON(101, 1, 100), `{"total_count":100,"repositories":[{"id":141,"owner":{"login":"octo"},"name":"target","default_branch":"main"}]}`},
			code:      "invalid_response",
		},
		"duplicate id": {
			responses: []string{repositoryPageJSON(101, 1, 100), `{"total_count":101,"repositories":[{"id":1,"owner":{"login":"octo"},"name":"duplicate","default_branch":"main"}]}`},
			code:      "invalid_response",
		},
		"oversized": {
			responses: []string{repositoryPageJSON(repositoryLookupPerPage*maxRepositoryLookupPages+1, 1, 100)},
			code:      "response_too_large",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			call := 0
			client := testClient(t, func(request *http.Request) *http.Response {
				if request.URL.Path != "/user/installations/7/repositories" || call >= len(test.responses) {
					t.Fatalf("unexpected repository lookup request %d: %s", call+1, request.URL)
				}
				response := jsonResponse(200, test.responses[call])
				call++
				return response
			})
			if _, err := client.Repository(context.Background(), "access", 7, 141); !IsCode(err, test.code) {
				t.Fatalf("lookup error = %v", err)
			}
		})
	}
}

func TestTreeAndSelectedContentReadsAreBoundedAndCanonical(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls int
	client := testClient(t, func(request *http.Request) *http.Response {
		calls++
		switch calls {
		case 1:
			if request.URL.Path != "/repos/octo/repo/git/trees/"+sha || request.URL.Query().Get("recursive") != "1" {
				t.Fatalf("tree URL=%s", request.URL)
			}
			return jsonResponse(200, `{"truncated":false,"tree":[{"path":"deploy/compose.yaml","type":"blob","size":13,"sha":"`+sha+`"}]}`)
		case 2:
			if request.URL.EscapedPath() != "/repos/octo/repo/contents/deploy%20files/compose%20%231.yaml" || request.URL.Query().Get("ref") != sha {
				t.Fatalf("content URL=%s", request.URL)
			}
			return jsonResponse(200, `{"type":"file","encoding":"base64","size":13,"content":"c2VydmljZXM6IHt9Cg=="}`)
		default:
			t.Fatal("unexpected request")
			return nil
		}
	})
	tree, err := client.Tree(context.Background(), "access", "octo", "repo", sha)
	if err != nil || len(tree.Entries) != 1 {
		t.Fatalf("tree=%#v err=%v", tree, err)
	}
	content, err := client.Content(context.Background(), "access", "octo", "repo", "deploy files/compose #1.yaml", sha)
	if err != nil || string(content) != "services: {}\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	for _, unsafe := range []string{"../compose.yaml", "/compose.yaml", "C:/compose.yaml", "deploy\\compose.yaml"} {
		if _, err := client.Content(context.Background(), "access", "octo", "repo", unsafe, sha); !IsCode(err, "invalid_request") {
			t.Errorf("path %q error=%v", unsafe, err)
		}
	}
	oversized := testClient(t, func(*http.Request) *http.Response {
		return jsonResponse(200, `{"type":"file","encoding":"base64","size":1048577,"content":""}`)
	})
	if _, err := oversized.Content(context.Background(), "access", "octo", "repo", "compose.yaml", sha); !IsCode(err, "response_too_large") {
		t.Fatalf("oversize error=%v", err)
	}
}

func repositoryPageJSON(total, startID, count int) string {
	items := make([]string, 0, count)
	for index := 0; index < count; index++ {
		id := startID + index
		items = append(items, fmt.Sprintf(`{"id":%d,"owner":{"login":"octo"},"name":"repo-%d","default_branch":"main"}`, id, id))
	}
	return fmt.Sprintf(`{"total_count":%d,"repositories":[%s]}`, total, strings.Join(items, ","))
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
