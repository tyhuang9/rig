// Package service implements the bounded HTTP surface for the official
// GitHub webhook relay. It deliberately owns no listener or background
// process lifecycle so provider, clock, entropy, and persistence behavior can
// be injected and tested independently.
package service

import (
	"bytes"
	"context"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/relay/store"
)

const (
	githubWebOrigin  = "https://github.com"
	githubAPIOrigin  = "https://api.github.com"
	githubAPIVersion = "2026-03-10"
)

var ErrInvalidOptions = errors.New("relay service invalid options")

// Store is the persistence contract used by the HTTP and recovery services.
type Store interface {
	CreateEnrollment(context.Context, store.EnrollmentInput) (string, error)
	ClaimEnrollmentState(context.Context, []byte) (store.EnrollmentClaim, error)
	CompleteEnrollment(context.Context, string) error
	FailEnrollment(context.Context, string, string) error
	PollEnrollment(context.Context, []byte) (store.EnrollmentStatus, error)
	SourceRoutes(context.Context, int64, int64, string) ([]store.SourceRoute, error)
	PushSourceEvent(context.Context, store.SourceEvent, []store.SourceRoute) (store.SourcePushResult, error)
	PushIgnoredDelivery(context.Context, string, string, time.Time) (bool, error)
	AccessRoutes(context.Context, int64, int64) ([]string, error)
	PushAccessEvent(context.Context, store.AccessEventInput, []store.AccessRoute) (store.AccessPushResult, error)
	PushAccessEvents(context.Context, store.AccessEventBatchInput) (store.AccessPushResult, error)
	StartRecoveryScan(context.Context, time.Time, time.Time) (store.RecoveryCursor, error)
	AdvanceRecoveryCursor(context.Context, store.RecoveryCursor, string) (store.RecoveryCursor, error)
	CompleteRecoveryScan(context.Context, store.RecoveryCursor) error
	DiscoverRecoveryDelivery(context.Context, store.RecoveryDelivery) (bool, error)
	ClaimRecovery(context.Context, int, time.Duration) ([]store.RecoveryClaim, error)
	RecordRecoveryAttempt(context.Context, store.RecoveryClaim, time.Time, string) error
}

type Options struct {
	Transport           http.RoundTripper
	Store               Store
	Now                 func() time.Time
	Random              io.Reader
	PublicBaseURL       *url.URL
	GitHubClientID      string
	GitHubClientSecret  []byte
	GitHubAppID         int64
	GitHubPrivateKey    *rsa.PrivateKey
	WebhookSecret       []byte
	EnrollmentKey       []byte
	RecoveryWindow      time.Duration
	ProviderTimeout     time.Duration
	LoopbackDevelopment bool
	Observer            Observer
}

// Observer receives only closed, aggregate outcome codes. Implementations
// must not attach request, provider, repository, or network identifiers.
type Observer interface {
	ObserveWebhook(outcome string)
}

type Service struct {
	http               *http.Client
	store              Store
	now                func() time.Time
	random             io.Reader
	publicBaseURL      *url.URL
	githubClientID     string
	githubClientSecret []byte
	githubAppID        int64
	githubPrivateKey   *rsa.PrivateKey
	webhookSecret      []byte
	enrollmentKey      []byte
	recoveryWindow     time.Duration
	enrollmentLimiter  *enrollmentLimiter
	observer           Observer
}

func New(options Options) (*Service, error) {
	if options.ProviderTimeout == 0 {
		options.ProviderTimeout = 15 * time.Second
	}
	if options.Transport == nil || options.Store == nil || options.Now == nil || options.Random == nil ||
		!validPublicBaseURL(options.PublicBaseURL, options.LoopbackDevelopment) || !validIdentifier(options.GitHubClientID, 255) ||
		len(options.GitHubClientSecret) < 16 || len(options.GitHubClientSecret) > 4<<10 || bytes.IndexByte(options.GitHubClientSecret, 0) >= 0 || options.GitHubAppID <= 0 ||
		options.GitHubPrivateKey == nil || options.GitHubPrivateKey.N == nil || options.GitHubPrivateKey.N.BitLen() < 2048 || options.GitHubPrivateKey.Validate() != nil ||
		len(options.WebhookSecret) < 16 || len(options.WebhookSecret) > 64<<10 || bytes.IndexByte(options.WebhookSecret, 0) >= 0 ||
		len(options.EnrollmentKey) != 32 || options.RecoveryWindow <= 0 || options.RecoveryWindow > 72*time.Hour {
		return nil, ErrInvalidOptions
	}
	if options.ProviderTimeout < time.Second || options.ProviderTimeout > time.Minute {
		return nil, ErrInvalidOptions
	}
	limiterKey := make([]byte, 32)
	if _, err := io.ReadFull(options.Random, limiterKey); err != nil {
		clear(limiterKey)
		return nil, ErrInvalidOptions
	}
	defer clear(limiterKey)
	base := *options.PublicBaseURL
	return &Service{
		http: &http.Client{
			Transport:     options.Transport,
			Timeout:       options.ProviderTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		store:              options.Store,
		now:                options.Now,
		random:             options.Random,
		publicBaseURL:      &base,
		githubClientID:     options.GitHubClientID,
		githubClientSecret: append([]byte(nil), options.GitHubClientSecret...),
		githubAppID:        options.GitHubAppID,
		githubPrivateKey:   options.GitHubPrivateKey,
		webhookSecret:      append([]byte(nil), options.WebhookSecret...),
		enrollmentKey:      append([]byte(nil), options.EnrollmentKey...),
		recoveryWindow:     options.RecoveryWindow,
		enrollmentLimiter:  newEnrollmentLimiter(limiterKey, options.Now().UTC()),
		observer:           options.Observer,
	}, nil
}

func (s *Service) observeWebhook(outcome string) {
	if s != nil && s.observer != nil {
		s.observer.ObserveWebhook(outcome)
	}
}

func validPublicBaseURL(value *url.URL, development bool) bool {
	if value == nil || value.Host == "" || value.User != nil || value.Opaque != "" || value.RawPath != "" || value.RawQuery != "" || value.ForceQuery || value.Fragment != "" || value.RawFragment != "" || (value.Path != "" && value.Path != "/") {
		return false
	}
	if value.Scheme == "https" {
		return true
	}
	if value.Scheme != "http" || !development {
		return false
	}
	ip := net.ParseIP(value.Hostname())
	return ip != nil && ip.IsLoopback()
}

func validIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._-", r) {
			return false
		}
	}
	return true
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	clear(s.githubClientSecret)
	clear(s.webhookSecret)
	clear(s.enrollmentKey)
	if s.enrollmentLimiter != nil {
		s.enrollmentLimiter.Close()
	}
	s.http.CloseIdleConnections()
}
