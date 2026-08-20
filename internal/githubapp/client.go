package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	WebOrigin                 = "https://github.com"
	APIOrigin                 = "https://api.github.com"
	VerificationURI           = WebOrigin + "/login/device"
	APIVersion                = "2026-03-10"
	maxResponseBytes          = 512 << 10
	maxCredentialLen          = 4096
	maxAccessLifetimeSeconds  = 7 * 24 * 60 * 60
	maxRefreshLifetimeSeconds = 2 * 365 * 24 * 60 * 60
	userAgent                 = "hostd-github-app/1"
)

type Error struct{ Code string }

func (e *Error) Error() string { return "github provider: " + e.Code }

func IsCode(err error, code string) bool {
	var providerError *Error
	return errors.As(err, &providerError) && providerError.Code == code
}

type Client struct {
	client   *http.Client
	clientID string
}

type DeviceAuthorization struct {
	DeviceCode      string `json:"-"`
	UserCode        string `json:"-"`
	VerificationURI string
	ExpiresIn       time.Duration
	Interval        time.Duration
}

type TokenBundle struct {
	AccessToken      string `json:"-"`
	RefreshToken     string `json:"-"`
	AccessExpiresIn  time.Duration
	RefreshExpiresIn time.Duration
}

type User struct {
	ID    string
	Login string
}

type Installation struct {
	ID                  int64
	AccountLogin        string
	AccountType         string
	TargetType          string
	RepositorySelection string
	SuspendedAt         *time.Time
}

type InstallationPage struct {
	TotalCount    int
	Installations []Installation
}

type Repository struct {
	ID            int64
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	Archived      bool
	Disabled      bool
}

type RepositoryPage struct {
	TotalCount   int
	Repositories []Repository
}

type Branch struct {
	Name      string
	SHA       string
	Protected bool
}

type BranchPage struct {
	Branches []Branch
}

func New(clientID string) (*Client, error) {
	return newWithTransport(clientID, http.DefaultTransport)
}

func newWithTransport(clientID string, transport http.RoundTripper) (*Client, error) {
	if !validASCII(clientID, 1, 255) {
		return nil, errors.New("github client ID must be 1-255 printable ASCII characters")
	}
	if transport == nil {
		return nil, errors.New("github transport is required")
	}
	return &Client{
		clientID: clientID,
		client: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) StartDevice(ctx context.Context) (DeviceAuthorization, error) {
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := c.form(ctx, WebOrigin+"/login/device/code", url.Values{"client_id": {c.clientID}}, &response); err != nil {
		return DeviceAuthorization{}, err
	}
	if !validSecret(response.DeviceCode) || !validASCII(response.UserCode, 1, 64) || response.VerificationURI != VerificationURI || response.ExpiresIn < 1 || response.ExpiresIn > 3600 || response.Interval < 1 || response.Interval > 300 {
		return DeviceAuthorization{}, &Error{Code: "invalid_response"}
	}
	return DeviceAuthorization{DeviceCode: response.DeviceCode, UserCode: response.UserCode, VerificationURI: VerificationURI, ExpiresIn: time.Duration(response.ExpiresIn) * time.Second, Interval: time.Duration(response.Interval) * time.Second}, nil
}

func (c *Client) PollDevice(ctx context.Context, deviceCode string) (TokenBundle, error) {
	if !validSecret(deviceCode) {
		return TokenBundle{}, &Error{Code: "invalid_request"}
	}
	values := url.Values{
		"client_id":   {c.clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	return c.token(ctx, values)
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenBundle, error) {
	if !validSecret(refreshToken) {
		return TokenBundle{}, &Error{Code: "invalid_request"}
	}
	values := url.Values{
		"client_id":     {c.clientID},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	return c.token(ctx, values)
}

func (c *Client) token(ctx context.Context, values url.Values) (TokenBundle, error) {
	var response struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		TokenType             string `json:"token_type"`
		ExpiresIn             int    `json:"expires_in"`
		RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
	}
	if err := c.form(ctx, WebOrigin+"/login/oauth/access_token", values, &response); err != nil {
		return TokenBundle{}, err
	}
	if response.Error != "" {
		return TokenBundle{}, &Error{Code: oauthErrorCode(response.Error)}
	}
	if !validSecret(response.AccessToken) || !validSecret(response.RefreshToken) || !strings.EqualFold(response.TokenType, "bearer") || response.ExpiresIn < 1 || response.ExpiresIn > maxAccessLifetimeSeconds || response.RefreshTokenExpiresIn < 1 || response.RefreshTokenExpiresIn > maxRefreshLifetimeSeconds {
		return TokenBundle{}, &Error{Code: "invalid_response"}
	}
	return TokenBundle{
		AccessToken:      response.AccessToken,
		RefreshToken:     response.RefreshToken,
		AccessExpiresIn:  time.Duration(response.ExpiresIn) * time.Second,
		RefreshExpiresIn: time.Duration(response.RefreshTokenExpiresIn) * time.Second,
	}, nil
}

func (c *Client) CurrentUser(ctx context.Context, accessToken string) (User, error) {
	if !validSecret(accessToken) {
		return User{}, &Error{Code: "invalid_request"}
	}
	var response struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
	}
	if err := c.api(ctx, APIOrigin+"/user", accessToken, &response); err != nil {
		return User{}, err
	}
	id, err := strconv.ParseInt(response.ID.String(), 10, 64)
	if err != nil || id < 1 || !validASCII(response.Login, 1, 255) {
		return User{}, &Error{Code: "invalid_response"}
	}
	return User{ID: strconv.FormatInt(id, 10), Login: response.Login}, nil
}

func (c *Client) Installations(ctx context.Context, accessToken string, page, perPage int) (InstallationPage, error) {
	if !validSecret(accessToken) || page < 1 || page > 10000 || perPage < 1 || perPage > 100 {
		return InstallationPage{}, &Error{Code: "invalid_request"}
	}
	endpoint := APIOrigin + "/user/installations?page=" + strconv.Itoa(page) + "&per_page=" + strconv.Itoa(perPage)
	var response struct {
		TotalCount    int `json:"total_count"`
		Installations []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
			TargetType          string     `json:"target_type"`
			RepositorySelection string     `json:"repository_selection"`
			SuspendedAt         *time.Time `json:"suspended_at"`
		} `json:"installations"`
	}
	if err := c.api(ctx, endpoint, accessToken, &response); err != nil {
		return InstallationPage{}, err
	}
	if response.TotalCount < len(response.Installations) || len(response.Installations) > perPage {
		return InstallationPage{}, &Error{Code: "invalid_response"}
	}
	result := InstallationPage{TotalCount: response.TotalCount, Installations: make([]Installation, 0, len(response.Installations))}
	for _, item := range response.Installations {
		if item.ID < 1 || !validASCII(item.Account.Login, 1, 255) || !oneOf(item.Account.Type, "User", "Organization", "Enterprise", "Bot") || !oneOf(item.TargetType, "User", "Organization") || !oneOf(item.RepositorySelection, "all", "selected") {
			return InstallationPage{}, &Error{Code: "invalid_response"}
		}
		result.Installations = append(result.Installations, Installation{ID: item.ID, AccountLogin: item.Account.Login, AccountType: item.Account.Type, TargetType: item.TargetType, RepositorySelection: item.RepositorySelection, SuspendedAt: item.SuspendedAt})
	}
	return result, nil
}

func (c *Client) Repositories(ctx context.Context, accessToken string, installationID int64, page, perPage int) (RepositoryPage, error) {
	if !validSecret(accessToken) || installationID < 1 || !validPage(page, perPage) {
		return RepositoryPage{}, &Error{Code: "invalid_request"}
	}
	endpoint := APIOrigin + "/user/installations/" + strconv.FormatInt(installationID, 10) + "/repositories?page=" + strconv.Itoa(page) + "&per_page=" + strconv.Itoa(perPage)
	var response struct {
		TotalCount   int                  `json:"total_count"`
		Repositories []repositoryResponse `json:"repositories"`
	}
	if err := c.api(ctx, endpoint, accessToken, &response); err != nil {
		return RepositoryPage{}, err
	}
	if response.TotalCount < len(response.Repositories) || len(response.Repositories) > perPage {
		return RepositoryPage{}, &Error{Code: "invalid_response"}
	}
	result := RepositoryPage{TotalCount: response.TotalCount, Repositories: make([]Repository, 0, len(response.Repositories))}
	for _, item := range response.Repositories {
		repository, err := validateRepository(item)
		if err != nil {
			return RepositoryPage{}, err
		}
		result.Repositories = append(result.Repositories, repository)
	}
	return result, nil
}

func (c *Client) Repository(ctx context.Context, accessToken string, installationID, repositoryID int64) (Repository, error) {
	if !validSecret(accessToken) || installationID < 1 || repositoryID < 1 {
		return Repository{}, &Error{Code: "invalid_request"}
	}
	endpoint := APIOrigin + "/user/installations/" + strconv.FormatInt(installationID, 10) + "/repositories/" + strconv.FormatInt(repositoryID, 10)
	var response repositoryResponse
	if err := c.api(ctx, endpoint, accessToken, &response); err != nil {
		return Repository{}, err
	}
	repository, err := validateRepository(response)
	if err != nil {
		return Repository{}, err
	}
	if repository.ID != repositoryID {
		return Repository{}, &Error{Code: "invalid_response"}
	}
	return repository, nil
}

func (c *Client) Branches(ctx context.Context, accessToken string, repositoryID int64, page, perPage int) (BranchPage, error) {
	if !validSecret(accessToken) || repositoryID < 1 || !validPage(page, perPage) {
		return BranchPage{}, &Error{Code: "invalid_request"}
	}
	endpoint := APIOrigin + "/repositories/" + strconv.FormatInt(repositoryID, 10) + "/branches?page=" + strconv.Itoa(page) + "&per_page=" + strconv.Itoa(perPage)
	var response []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	if err := c.api(ctx, endpoint, accessToken, &response); err != nil {
		return BranchPage{}, err
	}
	if len(response) > perPage {
		return BranchPage{}, &Error{Code: "invalid_response"}
	}
	result := BranchPage{Branches: make([]Branch, 0, len(response))}
	for _, item := range response {
		if !validBranch(item.Name) || !validSHA(item.Commit.SHA) {
			return BranchPage{}, &Error{Code: "invalid_response"}
		}
		result.Branches = append(result.Branches, Branch{Name: item.Name, SHA: item.Commit.SHA, Protected: item.Protected})
	}
	return result, nil
}

func (c *Client) Branch(ctx context.Context, accessToken string, repositoryID int64, branch string) (Branch, error) {
	if !validSecret(accessToken) || repositoryID < 1 || !validBranch(branch) {
		return Branch{}, &Error{Code: "invalid_request"}
	}
	endpoint := APIOrigin + "/repositories/" + strconv.FormatInt(repositoryID, 10) + "/branches/" + url.PathEscape(branch)
	var response struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	if err := c.api(ctx, endpoint, accessToken, &response); err != nil {
		return Branch{}, err
	}
	if response.Name != branch || !validSHA(response.Commit.SHA) {
		return Branch{}, &Error{Code: "invalid_response"}
	}
	return Branch{Name: response.Name, SHA: response.Commit.SHA, Protected: response.Protected}, nil
}

type repositoryResponse struct {
	ID    int64 `json:"id"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
}

func validateRepository(value repositoryResponse) (Repository, error) {
	if value.ID < 1 || !validASCII(value.Owner.Login, 1, 255) || !validASCII(value.Name, 1, 255) || !validBranch(value.DefaultBranch) {
		return Repository{}, &Error{Code: "invalid_response"}
	}
	return Repository{ID: value.ID, Owner: value.Owner.Login, Name: value.Name, DefaultBranch: value.DefaultBranch, Private: value.Private, Archived: value.Archived, Disabled: value.Disabled}, nil
}

func validPage(page, perPage int) bool {
	return page >= 1 && page <= 10000 && perPage >= 1 && perPage <= 100
}

func validBranch(value string) bool {
	return validASCII(value, 1, 255) && !strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") && !strings.HasSuffix(value, ".lock") && !strings.Contains(value, "..") && !strings.Contains(value, "@{") && !strings.ContainsAny(value, " ~^:?*[\\")
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (c *Client) form(ctx context.Context, endpoint string, values url.Values, target any) error {
	body := values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.execute(request, target)
}

func (c *Client) api(ctx context.Context, endpoint, accessToken string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	return c.execute(request, target)
}

func (c *Client) execute(request *http.Request, target any) error {
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	request.Header.Set("User-Agent", userAgent)
	response, err := c.client.Do(request)
	if err != nil {
		return &Error{Code: "provider_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
		return &Error{Code: statusErrorCode(response)}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return &Error{Code: "provider_unavailable"}
	}
	defer clear(body)
	if len(body) > maxResponseBytes {
		return &Error{Code: "response_too_large"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return &Error{Code: "invalid_response"}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return &Error{Code: "invalid_response"}
	}
	return nil
}

func oauthErrorCode(code string) string {
	switch code {
	case "authorization_pending":
		return "authorization_pending"
	case "slow_down":
		return "slow_down"
	case "expired_token":
		return "expired_token"
	case "access_denied":
		return "access_denied"
	default:
		return "oauth_failed"
	}
}

func statusErrorCode(response *http.Response) string {
	status := response.StatusCode
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		if response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0" {
			return "rate_limited"
		}
		return "forbidden"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		if status >= 500 {
			return "provider_unavailable"
		}
		return "provider_rejected"
	}
}

func (authorization DeviceAuthorization) String() string {
	return fmt.Sprintf("GitHub device authorization (expires in %s)", authorization.ExpiresIn)
}

func (authorization DeviceAuthorization) GoString() string { return authorization.String() }

func (authorization DeviceAuthorization) LogValue() slog.Value {
	return slog.GroupValue(slog.Duration("expires_in", authorization.ExpiresIn))
}

func (bundle TokenBundle) String() string {
	return fmt.Sprintf("GitHub token bundle (access expires in %s, refresh expires in %s)", bundle.AccessExpiresIn, bundle.RefreshExpiresIn)
}

func (bundle TokenBundle) GoString() string { return bundle.String() }

func (bundle TokenBundle) LogValue() slog.Value {
	return slog.GroupValue(slog.Duration("access_expires_in", bundle.AccessExpiresIn), slog.Duration("refresh_expires_in", bundle.RefreshExpiresIn))
}

func validSecret(value string) bool {
	return validASCII(value, 1, maxCredentialLen) && !strings.ContainsAny(value, " \t\r\n")
}

func validASCII(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (p InstallationPage) String() string {
	return fmt.Sprintf("GitHub installation page (%d total, %d returned)", p.TotalCount, len(p.Installations))
}
