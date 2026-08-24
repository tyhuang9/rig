package service

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	maxProviderBody  = 1 << 20
	maxProviderPages = 100
)

type providerError struct{ code string }

func (e *providerError) Error() string {
	if e == nil || e.code == "" {
		return "github request failed"
	}
	return "github request failed: " + e.code
}

func appJWT(appID int64, key *rsa.PrivateKey, now time.Time, entropy io.Reader) ([]byte, error) {
	if appID <= 0 || key == nil || key.N == nil || entropy == nil || now.IsZero() {
		return nil, ErrInvalidOptions
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{
		IssuedAt:  now.UTC().Add(-time.Minute).Unix(),
		ExpiresAt: now.UTC().Add(9 * time.Minute).Unix(),
		Issuer:    strconv.FormatInt(appID, 10),
	})
	if err != nil {
		return nil, err
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	signingInput := []byte(header + "." + payload)
	digest := sha256.Sum256(signingInput)
	signature, err := rsa.SignPKCS1v15(entropy, key, crypto.SHA256, digest[:])
	if err != nil {
		return nil, err
	}
	defer clear(signature)
	token := make([]byte, 0, len(signingInput)+1+base64.RawURLEncoding.EncodedLen(len(signature)))
	token = append(token, signingInput...)
	token = append(token, '.')
	token = base64.RawURLEncoding.AppendEncode(token, signature)
	return token, nil
}

type oauthTokens struct {
	access []byte
}

func (t *oauthTokens) Destroy() {
	if t == nil {
		return
	}
	clear(t.access)
}

func (s *Service) exchangeOAuthCode(ctx context.Context, code, verifier []byte, repositoryID int64) (oauthTokens, error) {
	if len(code) == 0 || len(code) > 1024 || len(verifier) < 43 || len(verifier) > 128 || repositoryID <= 0 {
		return oauthTokens{}, &providerError{code: "oauth.invalid"}
	}
	form := url.Values{
		"client_id":     {s.githubClientID},
		"client_secret": {string(s.githubClientSecret)},
		"code":          {string(code)},
		"code_verifier": {string(verifier)},
		"redirect_uri":  {s.callbackURL()},
		"repository_id": {strconv.FormatInt(repositoryID, 10)},
	}
	formBody := []byte(form.Encode())
	defer clear(formBody)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, githubWebOrigin+"/login/oauth/access_token", bytes.NewReader(formBody))
	if err != nil {
		return oauthTokens{}, &providerError{code: "oauth.request"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, body, err := s.doBounded(request, http.StatusOK)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		clear(body)
		return oauthTokens{}, err
	}
	defer clear(body)
	var wire struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := decodeProviderJSON(body, &wire); err != nil || wire.Error != "" || len(wire.AccessToken) < 8 || len(wire.AccessToken) > 1024 || len(wire.RefreshToken) > 1024 {
		return oauthTokens{}, &providerError{code: "oauth.response"}
	}
	tokens := oauthTokens{access: []byte(wire.AccessToken)}
	wire.AccessToken = ""
	wire.RefreshToken = ""
	return tokens, nil
}

func (s *Service) verifyUserRepositoryAccess(ctx context.Context, token []byte, installationID, repositoryID int64) error {
	if len(token) == 0 || installationID <= 0 || repositoryID <= 0 {
		return &providerError{code: "authorization.invalid"}
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := s.getUserJSON(ctx, token, "/user", nil, &user); err != nil || user.ID <= 0 {
		return &providerError{code: "authorization.user"}
	}
	foundInstallation := false
	installationPagesExhausted := true
	for page := 1; page <= maxProviderPages; page++ {
		var installations struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := s.getUserJSON(ctx, token, "/user/installations", query, &installations); err != nil {
			return err
		}
		if len(installations.Installations) > 100 {
			return &providerError{code: "api.response"}
		}
		for _, installation := range installations.Installations {
			if installation.ID <= 0 {
				return &providerError{code: "api.response"}
			}
			if installation.ID == installationID {
				foundInstallation = true
			}
		}
		if len(installations.Installations) < 100 {
			installationPagesExhausted = false
			break
		}
	}
	if !foundInstallation {
		if installationPagesExhausted {
			return &providerError{code: "api.pages"}
		}
		return &providerError{code: "authorization.installation"}
	}
	repositoryPagesExhausted := true
	for page := 1; page <= maxProviderPages; page++ {
		var repositories struct {
			Repositories []struct {
				ID int64 `json:"id"`
			} `json:"repositories"`
		}
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		path := "/user/installations/" + strconv.FormatInt(installationID, 10) + "/repositories"
		if err := s.getUserJSON(ctx, token, path, query, &repositories); err != nil {
			return err
		}
		if len(repositories.Repositories) > 100 {
			return &providerError{code: "api.response"}
		}
		for _, repository := range repositories.Repositories {
			if repository.ID <= 0 {
				return &providerError{code: "api.response"}
			}
			if repository.ID == repositoryID {
				return nil
			}
		}
		if len(repositories.Repositories) < 100 {
			repositoryPagesExhausted = false
			break
		}
	}
	if repositoryPagesExhausted {
		return &providerError{code: "api.pages"}
	}
	return &providerError{code: "authorization.repository"}
}

func (s *Service) getUserJSON(ctx context.Context, token []byte, path string, query url.Values, target any) error {
	endpoint := githubAPIOrigin + path
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &providerError{code: "api.request"}
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	setGitHubHeaders(request)
	response, body, err := s.doBounded(request, http.StatusOK)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		clear(body)
		return err
	}
	defer clear(body)
	if err := decodeProviderJSON(body, target); err != nil {
		return &providerError{code: "api.response"}
	}
	return nil
}

func (s *Service) doBounded(request *http.Request, expectedStatus int) (*http.Response, []byte, error) {
	response, err := s.http.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		return nil, nil, &providerError{code: "unavailable"}
	}
	if response == nil || response.Body == nil {
		return response, nil, &providerError{code: "response"}
	}
	// A client that followed a redirect could leak authorization to a different
	// origin. Reject any response whose final request differs from the request.
	if response.Request != nil && response.Request.URL.String() != request.URL.String() {
		return response, nil, &providerError{code: "redirect"}
	}
	if response.StatusCode != expectedStatus {
		return response, nil, &providerError{code: "status"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBody+1))
	if err != nil {
		clear(body)
		return response, nil, &providerError{code: "read"}
	}
	if len(body) > maxProviderBody {
		clear(body)
		return response, nil, &providerError{code: "too_large"}
	}
	return response, body, nil
}

func decodeProviderJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing provider response")
	}
	return nil
}

func setGitHubHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
}
