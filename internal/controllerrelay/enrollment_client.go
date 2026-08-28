package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	relayEnrollmentStartPath = "/v1/enrollments"
	relayEnrollmentPollPath  = "/v1/enrollments/status"
	maxRelayResponseBytes    = 16 << 10
	maxRelayRequestBytes     = 16 << 10
	relayRequestTimeout      = 20 * time.Second
)

type EnrollmentClient interface {
	Start(context.Context, RelayEnrollmentRequest) (RelayEnrollmentStart, error)
	Poll(context.Context, []byte) (RelayEnrollmentStatus, error)
}

type RelayEnrollmentRequest struct {
	ControllerID   string
	KeyID          string
	PublicKey      string
	InstallationID int64
	RepositoryID   int64
	RequestNonce   string
	IssuedAt       time.Time
	ExpiresAt      time.Time
	Signature      string
}

func (request RelayEnrollmentRequest) String() string   { return "signed relay enrollment request" }
func (request RelayEnrollmentRequest) GoString() string { return request.String() }
func (request RelayEnrollmentRequest) LogValue() slog.Value {
	return slog.GroupValue(slog.String("state", "signed"))
}

type RelayEnrollmentStart struct {
	AuthorizationURL string
	PollToken        []byte
}

func (start RelayEnrollmentStart) String() string   { return "relay enrollment authorization" }
func (start RelayEnrollmentStart) GoString() string { return start.String() }
func (start RelayEnrollmentStart) LogValue() slog.Value {
	return slog.GroupValue(slog.String("state", "protected"))
}

func (start *RelayEnrollmentStart) Destroy() {
	if start == nil {
		return
	}
	clear(start.PollToken)
	start.PollToken = nil
}

type RelayEnrollmentStatus struct {
	Status      string
	CompletedAt *time.Time
}

type ClientError struct {
	Code       string
	RetryAfter time.Duration
}

func (err *ClientError) Error() string {
	if err == nil {
		return "relay enrollment request failed"
	}
	return "relay enrollment request failed: " + err.Code
}
func (err *ClientError) String() string   { return err.Error() }
func (err *ClientError) GoString() string { return err.Error() }
func (err *ClientError) LogValue() slog.Value {
	if err == nil {
		return slog.GroupValue(slog.String("code", "relay_unavailable"))
	}
	return slog.GroupValue(slog.String("code", err.Code), slog.Duration("retry_after", err.RetryAfter))
}

func IsClientCode(err error, code string) bool {
	var clientErr *ClientError
	return errors.As(err, &clientErr) && clientErr.Code == code
}

type RelayHTTPSClient struct {
	origin *url.URL
	client *http.Client
}

// NewRelayHTTPSClient accepts an exact HTTPS origin. A nil transport creates a
// hardened production transport that never consults ambient proxy settings.
// Tests may inject a RoundTripper; redirect policy and total timeout remain
// owned by this client in either case.
func NewRelayHTTPSClient(rawOrigin string, transport http.RoundTripper) (*RelayHTTPSClient, error) {
	origin, err := parseCanonicalHTTPSOrigin(rawOrigin)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           16,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        30 * time.Second,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 16 << 10,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}
	}
	return &RelayHTTPSClient{
		origin: origin,
		client: &http.Client{
			Transport: transport,
			Timeout:   relayRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *RelayHTTPSClient) Start(ctx context.Context, input RelayEnrollmentRequest) (RelayEnrollmentStart, error) {
	if client == nil || client.client == nil || client.origin == nil || !validRelayEnrollmentRequest(input) {
		return RelayEnrollmentStart{}, &ClientError{Code: "invalid_request"}
	}
	wire := struct {
		ControllerID   string    `json:"controllerId"`
		KeyID          string    `json:"keyId"`
		PublicKey      string    `json:"publicKey"`
		InstallationID int64     `json:"installationId"`
		RepositoryID   int64     `json:"repositoryId"`
		RequestNonce   string    `json:"requestNonce"`
		IssuedAt       time.Time `json:"issuedAt"`
		ExpiresAt      time.Time `json:"expiresAt"`
		Signature      string    `json:"signature"`
	}{
		ControllerID: input.ControllerID, KeyID: input.KeyID, PublicKey: input.PublicKey,
		InstallationID: input.InstallationID, RepositoryID: input.RepositoryID,
		RequestNonce: input.RequestNonce, IssuedAt: input.IssuedAt.UTC(), ExpiresAt: input.ExpiresAt.UTC(), Signature: input.Signature,
	}
	responseBody, err := client.postJSON(ctx, relayEnrollmentStartPath, wire, http.StatusCreated)
	if err != nil {
		return RelayEnrollmentStart{}, err
	}
	defer clear(responseBody)
	var response struct {
		AuthorizationURL string `json:"authorizationUrl"`
		PollToken        string `json:"pollToken"`
	}
	if err = decodeStrictJSON(responseBody, &response); err != nil || !validAuthorizationURL(response.AuthorizationURL) {
		return RelayEnrollmentStart{}, &ClientError{Code: "invalid_response"}
	}
	pollToken, err := decodeCanonicalBase64URL(response.PollToken, pollTokenBytes)
	if err != nil {
		return RelayEnrollmentStart{}, &ClientError{Code: "invalid_response"}
	}
	return RelayEnrollmentStart{AuthorizationURL: response.AuthorizationURL, PollToken: pollToken}, nil
}

func (client *RelayHTTPSClient) Poll(ctx context.Context, pollToken []byte) (RelayEnrollmentStatus, error) {
	if client == nil || client.client == nil || client.origin == nil || len(pollToken) != pollTokenBytes {
		return RelayEnrollmentStatus{}, &ClientError{Code: "invalid_request"}
	}
	encodedToken := base64.RawURLEncoding.EncodeToString(pollToken)
	wire := struct {
		PollToken string `json:"pollToken"`
	}{PollToken: encodedToken}
	responseBody, err := client.postJSON(ctx, relayEnrollmentPollPath, wire, http.StatusOK)
	if err != nil {
		return RelayEnrollmentStatus{}, err
	}
	defer clear(responseBody)
	var response struct {
		Status      string     `json:"status"`
		FailureCode string     `json:"failureCode,omitempty"`
		CompletedAt *time.Time `json:"completedAt,omitempty"`
	}
	if err = decodeStrictJSON(responseBody, &response); err != nil {
		return RelayEnrollmentStatus{}, &ClientError{Code: "invalid_response"}
	}
	if !validRelayStatus(response.Status, response.FailureCode, response.CompletedAt) {
		return RelayEnrollmentStatus{}, &ClientError{Code: "invalid_response"}
	}
	if response.CompletedAt != nil {
		completed := response.CompletedAt.UTC()
		response.CompletedAt = &completed
	}
	// failureCode is deliberately not returned: relay/provider diagnostics are
	// mapped to one controller-owned terminal code by the service.
	return RelayEnrollmentStatus{Status: response.Status, CompletedAt: response.CompletedAt}, nil
}

func (client *RelayHTTPSClient) postJSON(ctx context.Context, path string, value any, expectedStatus int) ([]byte, error) {
	if ctx == nil {
		return nil, &ClientError{Code: "invalid_request"}
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > maxRelayRequestBytes {
		clear(body)
		return nil, &ClientError{Code: "invalid_request"}
	}
	defer clear(body)
	target := *client.origin
	target.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &ClientError{Code: "invalid_request"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "rig-controller-relay/1")
	response, err := client.client.Do(request)
	if err != nil {
		return nil, &ClientError{Code: "relay_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		discardBounded(response.Body)
		return nil, mapRelayStatus(response)
	}
	if !exactJSONContentType(response.Header.Get("Content-Type")) {
		discardBounded(response.Body)
		return nil, &ClientError{Code: "invalid_response"}
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxRelayResponseBytes+1))
	if readErr != nil || len(responseBody) == 0 || len(responseBody) > maxRelayResponseBytes {
		clear(responseBody)
		return nil, &ClientError{Code: "invalid_response"}
	}
	return responseBody, nil
}

func parseCanonicalHTTPSOrigin(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 2048 || strings.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("relay HTTPS origin is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("relay origin must be an exact HTTPS origin")
	}
	if parsed.String() != raw || parsed.Host != strings.ToLower(parsed.Host) || strings.Contains(parsed.Host, "%") {
		return nil, errors.New("relay HTTPS origin is not canonical")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") || strings.IndexFunc(hostname, func(r rune) bool { return r <= 0x20 || r >= 0x7f }) >= 0 {
		return nil, errors.New("relay HTTPS origin host is invalid")
	}
	port := parsed.Port()
	if port == "443" {
		return nil, errors.New("relay HTTPS origin contains a default port")
	}
	if port != "" {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 || strconv.FormatUint(value, 10) != port {
			return nil, errors.New("relay HTTPS origin port is invalid")
		}
	}
	if strings.Contains(hostname, ":") {
		ip := net.ParseIP(hostname)
		if ip == nil || "["+ip.String()+"]"+(func() string {
			if port == "" {
				return ""
			}
			return ":" + port
		})() != parsed.Host {
			return nil, errors.New("relay HTTPS origin IPv6 host is not canonical")
		}
	}
	return parsed, nil
}

func validRelayEnrollmentRequest(input RelayEnrollmentRequest) bool {
	if !validCanonicalUUID(input.ControllerID) || !validCanonicalUUID(input.KeyID) || input.InstallationID <= 0 || input.RepositoryID <= 0 || input.IssuedAt.IsZero() || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(input.IssuedAt) || input.ExpiresAt.Sub(input.IssuedAt) > 10*time.Minute {
		return false
	}
	if _, err := decodeCanonicalBase64URL(input.PublicKey, ed25519.PublicKeySize); err != nil {
		return false
	}
	if _, err := decodeCanonicalBase64URL(input.RequestNonce, protocol.NonceBytes); err != nil {
		return false
	}
	if _, err := decodeCanonicalBase64URL(input.Signature, ed25519.SignatureSize); err != nil {
		return false
	}
	return true
}

func decodeCanonicalBase64URL(value string, expectedBytes int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != expectedBytes || base64.RawURLEncoding.EncodeToString(decoded) != value {
		clear(decoded)
		return nil, errors.New("invalid canonical base64url")
	}
	return decoded, nil
}

func validAuthorizationURL(raw string) bool {
	if raw == "" || len(raw) > 4096 || strings.IndexByte(raw, 0) >= 0 {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" && parsed.String() == raw
}

func validRelayStatus(status, failureCode string, completedAt *time.Time) bool {
	switch status {
	case "pending":
		return failureCode == "" && completedAt == nil
	case "authorized", "denied", "expired":
		return failureCode == "" && completedAt != nil && !completedAt.IsZero()
	case "failed":
		return failureCode != "" && len(failureCode) <= 64 && completedAt != nil && !completedAt.IsZero()
	default:
		return false
	}
}

func exactJSONContentType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json" && len(parameters) == 0
}

func mapRelayStatus(response *http.Response) error {
	retryAfter := boundedRetryAfter(response.Header.Get("Retry-After"))
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		return &ClientError{Code: "relay_rate_limited", RetryAfter: retryAfter}
	case http.StatusNotFound:
		return &ClientError{Code: "enrollment_not_found"}
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity:
		return &ClientError{Code: "relay_rejected"}
	default:
		return &ClientError{Code: "relay_unavailable", RetryAfter: retryAfter}
	}
}

func boundedRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseUint(value, 10, 64)
	if err != nil || seconds == 0 {
		return 0
	}
	duration := time.Duration(seconds) * time.Second
	if duration > 5*time.Minute {
		return 5 * time.Minute
	}
	return duration
}

func discardBounded(body io.Reader) {
	if body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(body, maxRelayResponseBytes))
	}
}

func (status RelayEnrollmentStatus) String() string {
	return fmt.Sprintf("relay enrollment status %s", status.Status)
}
func (status RelayEnrollmentStatus) GoString() string { return status.String() }
func (status RelayEnrollmentStatus) LogValue() slog.Value {
	return slog.GroupValue(slog.String("status", status.Status))
}
