package sourceconnections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/githubapp"
)

type Provider interface {
	StartDevice(context.Context) (githubapp.DeviceAuthorization, error)
	PollDevice(context.Context, string) (githubapp.TokenBundle, error)
	Refresh(context.Context, string) (githubapp.TokenBundle, error)
	CurrentUser(context.Context, string) (githubapp.User, error)
	Installations(context.Context, string, int, int) (githubapp.InstallationPage, error)
}

type repositoryProvider interface {
	Repositories(context.Context, string, int64, int, int) (githubapp.RepositoryPage, error)
	Repository(context.Context, string, int64, int64) (githubapp.Repository, error)
	Branches(context.Context, string, string, string, int, int) (githubapp.BranchPage, error)
	Branch(context.Context, string, string, string, string) (githubapp.Branch, error)
	Tree(context.Context, string, string, string, string) (githubapp.Tree, error)
	Content(context.Context, string, string, string, string, string) ([]byte, error)
}

type archiveProvider interface {
	Archive(context.Context, string, string, string, string) (io.ReadCloser, error)
}

type Error struct {
	Code       string
	RetryAfter time.Duration
}

func (err *Error) Error() string { return "source connection: " + err.Code }

func IsCode(err error, code string) bool {
	var serviceError *Error
	return errors.As(err, &serviceError) && serviceError.Code == code
}

type Service struct {
	repository  *Repository
	provider    Provider
	credentials CredentialStore
	appSlug     string
	now         func() time.Time
	locks       keyedLocks
}

func NewService(repository *Repository, provider Provider, credentials CredentialStore, appSlug string, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, provider: provider, credentials: credentials, appSlug: appSlug, now: now}
}

func (service *Service) ProviderEnabled() bool {
	return service.provider != nil && service.appSlug != ""
}

func (service *Service) InstallURL() string {
	if !service.ProviderEnabled() {
		return ""
	}
	return githubapp.WebOrigin + "/apps/" + service.appSlug + "/installations/new"
}

func (service *Service) List(ctx context.Context, owner string) ([]Connection, error) {
	return service.repository.List(ctx, owner)
}

func (service *Service) Get(ctx context.Context, owner, id string) (Connection, error) {
	connection, err := service.repository.Get(ctx, owner, id)
	if err != nil {
		return Connection{}, connectionError(err)
	}
	return connection, nil
}

func (service *Service) Default(ctx context.Context, owner string) (Connection, bool, error) {
	connection, configured, err := service.repository.Default(ctx, owner)
	if err != nil {
		return Connection{}, false, internalError()
	}
	return connection, configured, nil
}

func (service *Service) StartDefault(ctx context.Context, owner string) (ConnectionAuthorization, error) {
	if service.provider == nil || service.appSlug == "" {
		return ConnectionAuthorization{}, &Error{Code: "provider_unavailable"}
	}
	authorization, err := service.provider.StartDevice(ctx)
	if err != nil {
		return ConnectionAuthorization{}, providerError(err)
	}
	now := service.now().UTC()
	expiresAt := now.Add(authorization.ExpiresIn)
	unlockOwner := service.locks.lock("owner:" + owner)
	defer unlockOwner()
	existing, configured, err := service.repository.Default(ctx, owner)
	if err != nil {
		return ConnectionAuthorization{}, internalError()
	}
	var unlockConnection func()
	if configured {
		unlockConnection = service.locks.lock(existing.ID)
		defer unlockConnection()
	}
	connection, attempt, superseded, err := service.repository.StartDefaultAuthorization(ctx, owner, expiresAt, authorization.Interval, now.Add(authorization.Interval), now)
	if err != nil {
		return ConnectionAuthorization{}, internalError()
	}
	if !configured {
		unlockConnection = service.locks.lock(connection.ID)
		defer unlockConnection()
		currentAttempt, readErr := service.repository.Authorization(ctx, owner, connection.ID, attempt.ID)
		if readErr != nil {
			return ConnectionAuthorization{}, internalError()
		}
		if currentAttempt.Status != "pending" {
			_ = service.destroyAttemptCredentials(attempt.ID)
			return ConnectionAuthorization{}, &Error{Code: "authorization_failed"}
		}
		attempt = currentAttempt
	}
	for _, id := range superseded {
		if err := service.destroyAttemptCredentials(id); err != nil {
			return ConnectionAuthorization{}, internalError()
		}
	}
	if err := service.credentials.WriteDevice(attempt.ID, authorization.DeviceCode); err != nil {
		_ = service.repository.MarkAuthorization(ctx, owner, connection.ID, attempt.ID, "failed", "credential_write_failed", now)
		_ = service.destroyAttemptCredentials(attempt.ID)
		return ConnectionAuthorization{}, internalError()
	}
	return ConnectionAuthorization{AuthorizationID: attempt.ID, DeviceStart: DeviceStart{
		ConnectionID: connection.ID, UserCode: authorization.UserCode, VerificationURI: githubapp.VerificationURI,
		InstallURL: service.InstallURL(), ExpiresAt: expiresAt, PollInterval: authorization.Interval,
	}}, nil
}

func (service *Service) PollDefault(ctx context.Context, owner, connectionID, authorizationID string) (AuthorizationStatus, error) {
	unlock := service.locks.lock(connectionID)
	defer unlock()
	attempt, err := service.repository.Authorization(ctx, owner, connectionID, authorizationID)
	if err != nil {
		return AuthorizationStatus{}, connectionError(err)
	}
	connection, err := service.repository.Get(ctx, owner, connectionID)
	if err != nil {
		return AuthorizationStatus{}, connectionError(err)
	}
	if attempt.Status == "connected" {
		if err := service.destroyAttemptCredentials(attempt.ID); err != nil {
			return AuthorizationStatus{}, internalError()
		}
		return AuthorizationStatus{Authorization: attempt, Connection: connection}, nil
	}
	if attempt.Status != "pending" {
		return AuthorizationStatus{Authorization: attempt, Connection: connection}, authorizationStatusError(attempt.Status)
	}
	if staged, readErr := service.credentials.ReadBundle(attempt.ID); readErr == nil {
		return service.finishDefaultBundle(ctx, owner, connection, attempt, staged)
	} else if !credentialMissing(readErr) {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "credential_invalid")
	}
	now := service.now().UTC()
	exchange, exchangeErr := service.credentials.ReadExchange(attempt.ID)
	if exchangeErr == nil {
		if now.Before(attempt.NextPollAt) {
			return AuthorizationStatus{}, &Error{Code: "poll_too_soon", RetryAfter: attempt.NextPollAt.Sub(now)}
		}
		return service.finalizeDefaultExchange(ctx, owner, connection, attempt, exchange)
	}
	if !credentialMissing(exchangeErr) {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "credential_invalid")
	}
	if !now.Before(attempt.PendingExpiresAt) {
		return AuthorizationStatus{}, service.endAuthorization(ctx, owner, attempt, "expired", "authorization_expired")
	}
	if now.Before(attempt.NextPollAt) {
		return AuthorizationStatus{}, &Error{Code: "poll_too_soon", RetryAfter: attempt.NextPollAt.Sub(now)}
	}
	deviceCode, err := service.credentials.ReadDevice(attempt.ID)
	if err != nil {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "device_credential_missing")
	}
	tokens, err := service.provider.PollDevice(ctx, deviceCode)
	deviceCode = ""
	if err != nil {
		return AuthorizationStatus{}, service.handleDefaultPollError(ctx, owner, attempt, err, service.now().UTC())
	}
	postProviderNow := service.now().UTC()
	exchange = TokenExchange{Version: tokenBundleVersion, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccessExpiresAt: postProviderNow.Add(tokens.AccessExpiresIn), RefreshExpiresAt: postProviderNow.Add(tokens.RefreshExpiresIn)}
	if err := service.credentials.WriteExchange(attempt.ID, exchange); err != nil {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "credential_write_failed")
	}
	if err := service.repository.AdvanceAuthorization(ctx, owner, connectionID, authorizationID, attempt.PollInterval, postProviderNow.Add(attempt.PollInterval), postProviderNow); err != nil {
		return AuthorizationStatus{}, internalError()
	}
	attempt.NextPollAt = postProviderNow.Add(attempt.PollInterval)
	return service.finalizeDefaultExchange(ctx, owner, connection, attempt, exchange)
}

func (service *Service) GetAuthorizationStatus(ctx context.Context, owner, connectionID, authorizationID string) (AuthorizationStatus, error) {
	attempt, err := service.repository.Authorization(ctx, owner, connectionID, authorizationID)
	if err != nil {
		return AuthorizationStatus{}, connectionError(err)
	}
	connection, err := service.repository.Get(ctx, owner, connectionID)
	if err != nil {
		return AuthorizationStatus{}, connectionError(err)
	}
	return AuthorizationStatus{Authorization: attempt, Connection: connection}, nil
}

func (service *Service) finalizeDefaultExchange(ctx context.Context, owner string, connection Connection, attempt AuthorizationAttempt, exchange TokenExchange) (AuthorizationStatus, error) {
	now := service.now().UTC()
	if !now.Before(exchange.RefreshExpiresAt) {
		return AuthorizationStatus{}, service.endAuthorization(ctx, owner, attempt, "expired", "authorization_expired")
	}
	if !now.Before(exchange.AccessExpiresAt) {
		tokens, err := service.provider.Refresh(ctx, exchange.RefreshToken)
		if err != nil {
			if githubapp.IsCode(err, "oauth_failed") || githubapp.IsCode(err, "unauthorized") || githubapp.IsCode(err, "expired_token") || githubapp.IsCode(err, "access_denied") {
				return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_invalid")
			}
			return AuthorizationStatus{}, service.advanceDefaultProviderError(ctx, owner, attempt, err)
		}
		exchange = TokenExchange{Version: tokenBundleVersion, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccessExpiresAt: now.Add(tokens.AccessExpiresIn), RefreshExpiresAt: now.Add(tokens.RefreshExpiresIn)}
		if err := service.credentials.WriteExchange(attempt.ID, exchange); err != nil {
			return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "credential_write_failed")
		}
	}
	user, err := service.provider.CurrentUser(ctx, exchange.AccessToken)
	if err != nil {
		if githubapp.IsCode(err, "unauthorized") {
			return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_invalid")
		}
		return AuthorizationStatus{}, service.advanceDefaultProviderError(ctx, owner, attempt, err)
	}
	if connection.ProviderUserID != "" && connection.ProviderUserID != user.ID {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_identity_mismatch")
	}
	bundle := TokenBundle{Version: tokenBundleVersion, Generation: attempt.CredentialGeneration + 1, AccessToken: exchange.AccessToken, RefreshToken: exchange.RefreshToken, AccessExpiresAt: exchange.AccessExpiresAt, RefreshExpiresAt: exchange.RefreshExpiresAt, ProviderUserID: user.ID, ProviderLogin: user.Login}
	if err := service.credentials.WriteBundle(attempt.ID, bundle); err != nil {
		return AuthorizationStatus{}, internalError()
	}
	return service.finishDefaultBundle(ctx, owner, connection, attempt, bundle)
}

func (service *Service) finishDefaultBundle(ctx context.Context, owner string, connection Connection, attempt AuthorizationAttempt, bundle TokenBundle) (AuthorizationStatus, error) {
	if connection.ProviderUserID != "" && connection.ProviderUserID != bundle.ProviderUserID {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_identity_mismatch")
	}
	previous, previousErr := service.credentials.ReadBundle(connection.ID)
	if previousErr == nil && connection.CredentialGeneration == bundle.Generation && attempt.CredentialGeneration+1 == bundle.Generation && tokenBundlesEqual(previous, bundle) {
		if err := service.repository.ReconcilePromotedAuthorization(ctx, owner, attempt, bundle, service.now().UTC()); err != nil {
			if errors.Is(err, ErrIdentityMismatch) {
				return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_identity_mismatch")
			}
			if errors.Is(err, ErrStaleGeneration) {
				return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_superseded")
			}
			return AuthorizationStatus{}, internalError()
		}
		if err := service.destroyAttemptCredentials(attempt.ID); err != nil {
			return AuthorizationStatus{}, internalError()
		}
		attempt.Status = "connected"
		return AuthorizationStatus{Authorization: attempt, Connection: connection}, nil
	}
	if connection.CredentialGeneration != attempt.CredentialGeneration {
		return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_superseded")
	}
	if err := service.credentials.WriteBundle(connection.ID, bundle); err != nil {
		return AuthorizationStatus{}, internalError()
	}
	if err := service.repository.FinishAuthorization(ctx, owner, attempt, bundle, service.now().UTC()); err != nil {
		if previousErr == nil {
			_ = service.credentials.WriteBundle(connection.ID, previous)
		} else if credentialMissing(previousErr) {
			_ = service.credentials.RemoveBundle(connection.ID)
		}
		if errors.Is(err, ErrIdentityMismatch) {
			return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_identity_mismatch")
		}
		if errors.Is(err, ErrIdentityExists) {
			return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "identity_already_connected")
		}
		if errors.Is(err, ErrStaleGeneration) {
			return AuthorizationStatus{}, service.failAuthorization(ctx, owner, attempt, "authorization_superseded")
		}
		return AuthorizationStatus{}, internalError()
	}
	if err := service.destroyAttemptCredentials(attempt.ID); err != nil {
		return AuthorizationStatus{}, internalError()
	}
	connected, err := service.repository.Get(ctx, owner, connection.ID)
	if err != nil {
		return AuthorizationStatus{}, internalError()
	}
	attempt.Status = "connected"
	return AuthorizationStatus{Authorization: attempt, Connection: connected}, nil
}

func tokenBundlesEqual(left, right TokenBundle) bool {
	return left.Version == right.Version && left.Generation == right.Generation &&
		left.AccessToken == right.AccessToken && left.RefreshToken == right.RefreshToken &&
		left.AccessExpiresAt.Equal(right.AccessExpiresAt) && left.RefreshExpiresAt.Equal(right.RefreshExpiresAt) &&
		left.ProviderUserID == right.ProviderUserID && left.ProviderLogin == right.ProviderLogin
}

func (service *Service) handleDefaultPollError(ctx context.Context, owner string, attempt AuthorizationAttempt, providerErr error, now time.Time) error {
	interval := attempt.PollInterval
	switch {
	case githubapp.IsCode(providerErr, "authorization_pending"):
		if err := service.repository.AdvanceAuthorization(ctx, owner, attempt.ConnectionID, attempt.ID, interval, now.Add(interval), now); err != nil {
			return internalError()
		}
		return &Error{Code: "authorization_pending", RetryAfter: interval}
	case githubapp.IsCode(providerErr, "slow_down"):
		if interval > 295*time.Second {
			interval = 300 * time.Second
		} else {
			interval += 5 * time.Second
		}
		if err := service.repository.AdvanceAuthorization(ctx, owner, attempt.ConnectionID, attempt.ID, interval, now.Add(interval), now); err != nil {
			return internalError()
		}
		return &Error{Code: "authorization_pending", RetryAfter: interval}
	case githubapp.IsCode(providerErr, "expired_token"):
		return service.endAuthorization(ctx, owner, attempt, "expired", "authorization_expired")
	case githubapp.IsCode(providerErr, "access_denied"):
		return service.endAuthorization(ctx, owner, attempt, "denied", "authorization_denied")
	default:
		return service.advanceDefaultProviderError(ctx, owner, attempt, providerErr)
	}
}

func (service *Service) advanceDefaultProviderError(ctx context.Context, owner string, attempt AuthorizationAttempt, providerErr error) error {
	now := service.now().UTC()
	if err := service.repository.AdvanceAuthorization(ctx, owner, attempt.ConnectionID, attempt.ID, attempt.PollInterval, now.Add(attempt.PollInterval), now); err != nil {
		return internalError()
	}
	return providerError(providerErr)
}

func (service *Service) endAuthorization(ctx context.Context, owner string, attempt AuthorizationAttempt, status, code string) error {
	if err := service.destroyAttemptCredentials(attempt.ID); err != nil {
		return internalError()
	}
	if err := service.repository.MarkAuthorization(ctx, owner, attempt.ConnectionID, attempt.ID, status, code, service.now().UTC()); err != nil {
		return internalError()
	}
	return &Error{Code: code}
}

func (service *Service) failAuthorization(ctx context.Context, owner string, attempt AuthorizationAttempt, code string) error {
	if err := service.destroyAttemptCredentials(attempt.ID); err != nil {
		return internalError()
	}
	status := "failed"
	if code == "authorization_superseded" {
		status = "superseded"
	}
	if err := service.repository.MarkAuthorization(ctx, owner, attempt.ConnectionID, attempt.ID, status, code, service.now().UTC()); err != nil {
		return internalError()
	}
	if code == "authorization_identity_mismatch" || code == "identity_already_connected" || code == "authorization_superseded" {
		return &Error{Code: code}
	}
	return &Error{Code: "authorization_failed"}
}

func authorizationStatusError(status string) error {
	switch status {
	case "denied":
		return &Error{Code: "authorization_denied"}
	case "expired":
		return &Error{Code: "authorization_expired"}
	case "superseded":
		return &Error{Code: "authorization_superseded"}
	default:
		return &Error{Code: "authorization_failed"}
	}
}

func (service *Service) Start(ctx context.Context, owner string) (DeviceStart, error) {
	if service.provider == nil || service.appSlug == "" {
		return DeviceStart{}, &Error{Code: "provider_unavailable"}
	}
	authorization, err := service.provider.StartDevice(ctx)
	if err != nil {
		return DeviceStart{}, providerError(err)
	}
	now := service.now().UTC()
	expiresAt := now.Add(authorization.ExpiresIn)
	connection, err := service.repository.CreatePending(ctx, owner, expiresAt, authorization.Interval, now.Add(authorization.Interval), now)
	if err != nil {
		return DeviceStart{}, internalError()
	}
	if err := service.credentials.WriteDevice(connection.ID, authorization.DeviceCode); err != nil {
		if terminalErr := service.purgeAndMark(ctx, owner, connection.ID, StatusAccessLost, "credential_write_failed"); !IsCode(terminalErr, "source_access_lost") {
			return DeviceStart{}, terminalErr
		}
		return DeviceStart{}, internalError()
	}
	return DeviceStart{
		ConnectionID: connection.ID, UserCode: authorization.UserCode, VerificationURI: githubapp.VerificationURI,
		InstallURL: service.InstallURL(), ExpiresAt: expiresAt, PollInterval: authorization.Interval,
	}, nil
}

func (service *Service) Poll(ctx context.Context, owner, id string) (Connection, error) {
	unlock := service.locks.lock(id)
	defer unlock()
	connection, err := service.repository.Get(ctx, owner, id)
	if err != nil {
		return Connection{}, connectionError(err)
	}
	if connection.Status == StatusConnected {
		if _, err := service.loadBundle(ctx, owner, connection); err != nil {
			return Connection{}, err
		}
		return service.repository.Get(ctx, owner, id)
	}
	if connection.Status != StatusPending {
		return connection, statusError(connection.Status)
	}
	if bundle, readErr := service.credentials.ReadBundle(id); readErr == nil {
		if connectErr := service.finishBundle(ctx, owner, id, bundle); connectErr != nil {
			return Connection{}, connectErr
		}
		return service.repository.Get(ctx, owner, id)
	} else if !credentialMissing(readErr) {
		return Connection{}, service.loseAccess(ctx, owner, id, "credential_invalid")
	}
	now := service.now().UTC()
	exchange, exchangeErr := service.credentials.ReadExchange(id)
	if exchangeErr == nil {
		if connection.NextPollAt != nil && now.Before(*connection.NextPollAt) {
			return Connection{}, &Error{Code: "poll_too_soon", RetryAfter: connection.NextPollAt.Sub(now)}
		}
		return service.finalizeExchange(ctx, owner, connection, exchange)
	}
	if !credentialMissing(exchangeErr) {
		return Connection{}, service.loseAccess(ctx, owner, id, "credential_invalid")
	}
	if connection.PendingExpiresAt == nil || !now.Before(*connection.PendingExpiresAt) {
		return Connection{}, service.purgeAndMark(ctx, owner, id, StatusExpired, "authorization_expired")
	}
	if connection.NextPollAt != nil && now.Before(*connection.NextPollAt) {
		return Connection{}, &Error{Code: "poll_too_soon", RetryAfter: connection.NextPollAt.Sub(now)}
	}
	if service.provider == nil {
		return Connection{}, &Error{Code: "provider_unavailable"}
	}
	deviceCode, err := service.credentials.ReadDevice(id)
	if err != nil {
		return Connection{}, service.loseAccess(ctx, owner, id, "device_credential_missing")
	}
	tokens, err := service.provider.PollDevice(ctx, deviceCode)
	deviceCode = ""
	if err != nil {
		return Connection{}, service.handlePollError(ctx, owner, connection, err, service.now().UTC())
	}
	postProviderNow := service.now().UTC()
	issuedExchange := TokenExchange{Version: tokenBundleVersion, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccessExpiresAt: postProviderNow.Add(tokens.AccessExpiresIn), RefreshExpiresAt: postProviderNow.Add(tokens.RefreshExpiresIn)}
	if err := service.credentials.WriteExchange(id, issuedExchange); err != nil {
		if terminalErr := service.purgeAndMark(ctx, owner, id, StatusAccessLost, "credential_write_failed"); !IsCode(terminalErr, "source_access_lost") {
			return Connection{}, terminalErr
		}
		return Connection{}, internalError()
	}
	if err := service.repository.AdvancePoll(ctx, owner, id, connection.PollInterval, postProviderNow.Add(connection.PollInterval), postProviderNow); err != nil {
		return Connection{}, internalError()
	}
	return service.finalizeExchange(ctx, owner, connection, issuedExchange)
}

func (service *Service) finalizeExchange(ctx context.Context, owner string, connection Connection, exchange TokenExchange) (Connection, error) {
	if service.provider == nil {
		return Connection{}, &Error{Code: "provider_unavailable"}
	}
	now := service.now().UTC()
	if !now.Before(exchange.RefreshExpiresAt) {
		return Connection{}, service.purgeAndMark(ctx, owner, connection.ID, StatusAccessLost, "refresh_expired")
	}
	if !now.Before(exchange.AccessExpiresAt) {
		tokens, err := service.provider.Refresh(ctx, exchange.RefreshToken)
		if err != nil {
			if githubapp.IsCode(err, "oauth_failed") || githubapp.IsCode(err, "unauthorized") || githubapp.IsCode(err, "expired_token") || githubapp.IsCode(err, "access_denied") {
				return Connection{}, service.purgeAndMark(ctx, owner, connection.ID, StatusAccessLost, "refresh_invalid")
			}
			return Connection{}, service.finalizationProviderError(ctx, owner, connection, err)
		}
		exchange = TokenExchange{Version: tokenBundleVersion, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccessExpiresAt: now.Add(tokens.AccessExpiresIn), RefreshExpiresAt: now.Add(tokens.RefreshExpiresIn)}
		if err := service.credentials.WriteExchange(connection.ID, exchange); err != nil {
			return Connection{}, service.purgeAndMark(ctx, owner, connection.ID, StatusAccessLost, "credential_rotation_failed")
		}
	}
	user, err := service.provider.CurrentUser(ctx, exchange.AccessToken)
	if err != nil {
		if githubapp.IsCode(err, "unauthorized") {
			return Connection{}, service.purgeAndMark(ctx, owner, connection.ID, StatusAccessLost, "source_access_lost")
		}
		return Connection{}, service.finalizationProviderError(ctx, owner, connection, err)
	}
	bundle := TokenBundle{Version: tokenBundleVersion, Generation: 1, AccessToken: exchange.AccessToken, RefreshToken: exchange.RefreshToken, AccessExpiresAt: exchange.AccessExpiresAt, RefreshExpiresAt: exchange.RefreshExpiresAt, ProviderUserID: user.ID, ProviderLogin: user.Login}
	if err := service.credentials.WriteBundle(connection.ID, bundle); err != nil {
		return Connection{}, internalError()
	}
	if err := service.finishBundle(ctx, owner, connection.ID, bundle); err != nil {
		return Connection{}, err
	}
	return service.repository.Get(ctx, owner, connection.ID)
}

func (service *Service) finalizationProviderError(ctx context.Context, owner string, connection Connection, providerErr error) error {
	now := service.now().UTC()
	if err := service.repository.AdvancePoll(ctx, owner, connection.ID, connection.PollInterval, now.Add(connection.PollInterval), now); err != nil {
		return internalError()
	}
	return providerError(providerErr)
}

func (service *Service) Refresh(ctx context.Context, owner, id string) (Connection, error) {
	unlock := service.locks.lock(id)
	defer unlock()
	connection, err := service.repository.Get(ctx, owner, id)
	if err != nil {
		return Connection{}, connectionError(err)
	}
	if connection.Status != StatusConnected && connection.Status != StatusAccessLost {
		return Connection{}, statusError(connection.Status)
	}
	if service.provider == nil {
		return Connection{}, &Error{Code: "provider_unavailable"}
	}
	bundle, err := service.loadBundle(ctx, owner, connection)
	if err != nil {
		return Connection{}, err
	}
	if _, err := service.refreshLocked(ctx, owner, connection, bundle); err != nil {
		return Connection{}, err
	}
	return service.repository.Get(ctx, owner, id)
}

func (service *Service) Installations(ctx context.Context, owner, id string, page, perPage int) (InstallationPage, error) {
	unlock := service.locks.lock(id)
	defer unlock()
	connection, err := service.repository.Get(ctx, owner, id)
	if err != nil {
		return InstallationPage{}, connectionError(err)
	}
	if connection.Status != StatusConnected {
		return InstallationPage{}, statusError(connection.Status)
	}
	if service.provider == nil {
		return InstallationPage{}, &Error{Code: "provider_unavailable"}
	}
	bundle, err := service.loadBundle(ctx, owner, connection)
	if err != nil {
		return InstallationPage{}, err
	}
	now := service.now().UTC()
	if !now.Before(bundle.AccessExpiresAt) {
		bundle, err = service.refreshLocked(ctx, owner, connection, bundle)
		if err != nil {
			return InstallationPage{}, err
		}
	}
	providerPage, err := service.provider.Installations(ctx, bundle.AccessToken, page, perPage)
	if githubapp.IsCode(err, "unauthorized") {
		bundle, err = service.refreshLocked(ctx, owner, connection, bundle)
		if err != nil {
			return InstallationPage{}, err
		}
		providerPage, err = service.provider.Installations(ctx, bundle.AccessToken, page, perPage)
		if githubapp.IsCode(err, "unauthorized") {
			return InstallationPage{}, service.loseAccess(ctx, owner, id, "repeated_unauthorized")
		}
	}
	if err != nil {
		return InstallationPage{}, providerError(err)
	}
	installations := make([]Installation, 0, len(providerPage.Installations))
	for _, item := range providerPage.Installations {
		installations = append(installations, Installation{ID: item.ID, AccountLogin: item.AccountLogin, AccountType: item.AccountType, TargetType: item.TargetType, RepositorySelection: item.RepositorySelection, SuspendedAt: item.SuspendedAt, CachedAt: now})
	}
	if err := service.repository.UpsertInstallationPage(ctx, owner, id, installations); err != nil {
		return InstallationPage{}, connectionError(err)
	}
	return InstallationPage{Page: page, PerPage: perPage, TotalCount: providerPage.TotalCount, Installations: installations}, nil
}

func (service *Service) Repositories(ctx context.Context, owner, id string, installationID int64, page, perPage int) (RepositoryPage, error) {
	var providerPage githubapp.RepositoryPage
	err := service.withAccess(ctx, owner, id, func(provider repositoryProvider, token string) error {
		var err error
		providerPage, err = provider.Repositories(ctx, token, installationID, page, perPage)
		return err
	})
	if err != nil {
		return RepositoryPage{}, sourceOperationError(err)
	}
	result := RepositoryPage{Page: page, PerPage: perPage, TotalCount: providerPage.TotalCount, Repositories: make([]SourceRepository, 0, len(providerPage.Repositories))}
	for _, item := range providerPage.Repositories {
		result.Repositories = append(result.Repositories, SourceRepository{ID: item.ID, Owner: item.Owner, Name: item.Name, DefaultBranch: item.DefaultBranch, Private: item.Private, Archived: item.Archived, Disabled: item.Disabled})
	}
	return result, nil
}

func (service *Service) DefaultRepositories(ctx context.Context, owner, query string, page, perPage int) (RepositorySelectionPage, error) {
	connection, configured, err := service.repository.Default(ctx, owner)
	if err != nil {
		return RepositorySelectionPage{}, internalError()
	}
	if !configured {
		return RepositorySelectionPage{}, &Error{Code: "connection_not_found"}
	}
	query = strings.ToLower(strings.TrimSpace(query))
	const providerPageSize = 100
	const maximumProviderPages = 100
	var selections []RepositorySelection
	err = service.withAccess(ctx, owner, connection.ID, func(provider repositoryProvider, token string) error {
		attemptSelections := make([]RepositorySelection, 0)
		providerPages := 0
		var installations []githubapp.Installation
		installationIDs := make(map[int64]struct{})
		installationTotal := -1
		for installationPage := 1; ; installationPage++ {
			providerPages++
			if providerPages > maximumProviderPages {
				return &githubapp.Error{Code: "response_too_large"}
			}
			result, listErr := service.provider.Installations(ctx, token, installationPage, providerPageSize)
			if listErr != nil {
				return listErr
			}
			if installationTotal < 0 {
				installationTotal = result.TotalCount
			} else if result.TotalCount != installationTotal {
				return &githubapp.Error{Code: "invalid_response"}
			}
			for _, installation := range result.Installations {
				if _, duplicate := installationIDs[installation.ID]; duplicate {
					return &githubapp.Error{Code: "invalid_response"}
				}
				installationIDs[installation.ID] = struct{}{}
				installations = append(installations, installation)
			}
			if len(installations) > installationTotal {
				return &githubapp.Error{Code: "invalid_response"}
			}
			if len(installations) >= installationTotal {
				break
			}
			if len(result.Installations) == 0 {
				return &githubapp.Error{Code: "invalid_response"}
			}
		}
		for _, installation := range installations {
			if installation.SuspendedAt != nil {
				continue
			}
			repositoryTotal := -1
			seen := 0
			repositoryIDs := make(map[int64]struct{})
			for repositoryPage := 1; ; repositoryPage++ {
				providerPages++
				if providerPages > maximumProviderPages {
					return &githubapp.Error{Code: "response_too_large"}
				}
				result, listErr := provider.Repositories(ctx, token, installation.ID, repositoryPage, providerPageSize)
				if listErr != nil {
					return listErr
				}
				if repositoryTotal < 0 {
					repositoryTotal = result.TotalCount
				} else if result.TotalCount != repositoryTotal {
					return &githubapp.Error{Code: "invalid_response"}
				}
				for _, item := range result.Repositories {
					if _, duplicate := repositoryIDs[item.ID]; duplicate {
						return &githubapp.Error{Code: "invalid_response"}
					}
					repositoryIDs[item.ID] = struct{}{}
					seen++
					if item.Archived || item.Disabled {
						continue
					}
					fullName := strings.ToLower(item.Owner + "/" + item.Name)
					if query != "" && !strings.Contains(fullName, query) && !strings.Contains(strings.ToLower(installation.AccountLogin), query) {
						continue
					}
					attemptSelections = append(attemptSelections, RepositorySelection{ConnectionID: connection.ID, InstallationID: installation.ID, AccountLogin: installation.AccountLogin, SourceRepository: sourceRepository(item)})
				}
				if seen > repositoryTotal {
					return &githubapp.Error{Code: "invalid_response"}
				}
				if seen >= repositoryTotal {
					break
				}
				if len(result.Repositories) == 0 {
					return &githubapp.Error{Code: "invalid_response"}
				}
			}
		}
		selections = attemptSelections
		return nil
	})
	if err != nil {
		return RepositorySelectionPage{}, sourceOperationError(err)
	}
	sort.Slice(selections, func(i, j int) bool {
		left := strings.ToLower(selections[i].Owner + "/" + selections[i].Name)
		right := strings.ToLower(selections[j].Owner + "/" + selections[j].Name)
		if left != right {
			return left < right
		}
		if selections[i].InstallationID != selections[j].InstallationID {
			return selections[i].InstallationID < selections[j].InstallationID
		}
		return selections[i].ID < selections[j].ID
	})
	total := len(selections)
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	return RepositorySelectionPage{Page: page, PerPage: perPage, TotalCount: total, Repositories: selections[start:end]}, nil
}

func (service *Service) Repository(ctx context.Context, owner, id string, installationID, repositoryID int64) (SourceRepository, error) {
	var item githubapp.Repository
	err := service.withAccess(ctx, owner, id, func(provider repositoryProvider, token string) error {
		var err error
		item, err = provider.Repository(ctx, token, installationID, repositoryID)
		return err
	})
	if err != nil {
		return SourceRepository{}, sourceOperationError(err)
	}
	return sourceRepository(item), nil
}

func (service *Service) Branches(ctx context.Context, owner, id string, installationID, repositoryID int64, page, perPage int) (BranchPage, error) {
	var providerPage githubapp.BranchPage
	err := service.withAccess(ctx, owner, id, func(provider repositoryProvider, token string) error {
		repository, err := provider.Repository(ctx, token, installationID, repositoryID)
		if err != nil {
			return err
		}
		providerPage, err = provider.Branches(ctx, token, repository.Owner, repository.Name, page, perPage)
		return err
	})
	if err != nil {
		return BranchPage{}, sourceOperationError(err)
	}
	result := BranchPage{Page: page, PerPage: perPage, Branches: make([]Branch, 0, len(providerPage.Branches))}
	for _, item := range providerPage.Branches {
		result.Branches = append(result.Branches, Branch{Name: item.Name, SHA: item.SHA, Protected: item.Protected})
	}
	return result, nil
}

func (service *Service) Resolve(ctx context.Context, owner, id string, installationID, repositoryID int64, branch string) (SourceRepository, Branch, error) {
	var repository githubapp.Repository
	var resolved githubapp.Branch
	err := service.withAccess(ctx, owner, id, func(provider repositoryProvider, token string) error {
		var err error
		repository, err = provider.Repository(ctx, token, installationID, repositoryID)
		if err != nil {
			return err
		}
		resolved, err = provider.Branch(ctx, token, repository.Owner, repository.Name, branch)
		return err
	})
	if err != nil {
		return SourceRepository{}, Branch{}, sourceOperationError(err)
	}
	return sourceRepository(repository), Branch{Name: resolved.Name, SHA: resolved.SHA, Protected: resolved.Protected}, nil
}

func (service *Service) ReadTree(ctx context.Context, owner, id string, installationID int64, repository SourceRepository, sha string) (githubapp.Tree, error) {
	var result githubapp.Tree
	err := service.withAccess(ctx, owner, id, func(provider repositoryProvider, token string) error {
		canonical, err := provider.Repository(ctx, token, installationID, repository.ID)
		if err != nil {
			return err
		}
		result, err = provider.Tree(ctx, token, canonical.Owner, canonical.Name, sha)
		return err
	})
	if err != nil {
		return githubapp.Tree{}, sourceOperationError(err)
	}
	return result, nil
}

func (service *Service) ReadContent(ctx context.Context, owner, id string, installationID int64, repository SourceRepository, path, sha string) ([]byte, error) {
	var result []byte
	err := service.withAccess(ctx, owner, id, func(provider repositoryProvider, token string) error {
		canonical, err := provider.Repository(ctx, token, installationID, repository.ID)
		if err != nil {
			return err
		}
		result, err = provider.Content(ctx, token, canonical.Owner, canonical.Name, path, sha)
		return err
	})
	if err != nil {
		return nil, sourceOperationError(err)
	}
	return result, nil
}

// DownloadArchive opens a short-lived, authenticated archive stream for an
// immutable commit. Callers must close the result promptly and never retain
// the access token or provider response metadata.
func (service *Service) DownloadArchive(ctx context.Context, owner, id string, installationID int64, repository SourceRepository, sha string) (io.ReadCloser, error) {
	provider, ok := service.provider.(archiveProvider)
	if !ok {
		return nil, &Error{Code: "provider_unavailable"}
	}
	var result io.ReadCloser
	err := service.withAccess(ctx, owner, id, func(repositoryProvider repositoryProvider, token string) error {
		canonical, err := repositoryProvider.Repository(ctx, token, installationID, repository.ID)
		if err != nil {
			return err
		}
		result, err = provider.Archive(ctx, token, canonical.Owner, canonical.Name, sha)
		return err
	})
	if err != nil {
		return nil, sourceOperationError(err)
	}
	if result == nil {
		return nil, &Error{Code: "provider_unavailable"}
	}
	return result, nil
}

func sourceRepository(item githubapp.Repository) SourceRepository {
	return SourceRepository{ID: item.ID, Owner: item.Owner, Name: item.Name, DefaultBranch: item.DefaultBranch, Private: item.Private, Archived: item.Archived, Disabled: item.Disabled}
}

func (service *Service) withAccess(ctx context.Context, owner, id string, operation func(repositoryProvider, string) error) error {
	provider, ok := service.provider.(repositoryProvider)
	if !ok {
		return &Error{Code: "provider_unavailable"}
	}
	unlock := service.locks.lock(id)
	defer unlock()
	connection, err := service.repository.Get(ctx, owner, id)
	if err != nil {
		return connectionError(err)
	}
	if connection.Status != StatusConnected {
		return statusError(connection.Status)
	}
	bundle, err := service.loadBundle(ctx, owner, connection)
	if err != nil {
		return err
	}
	err = operation(provider, bundle.AccessToken)
	if githubapp.IsCode(err, "unauthorized") {
		bundle, err = service.refreshLocked(ctx, owner, connection, bundle)
		if err != nil {
			return err
		}
		err = operation(provider, bundle.AccessToken)
		if githubapp.IsCode(err, "unauthorized") {
			return service.loseAccess(ctx, owner, id, "repeated_unauthorized")
		}
	}
	return err
}

func sourceOperationError(err error) error {
	var serviceErr *Error
	if errors.As(err, &serviceErr) {
		return err
	}
	if githubapp.IsCode(err, "not_found") || githubapp.IsCode(err, "forbidden") {
		return &Error{Code: "invalid_source"}
	}
	if githubapp.IsCode(err, "response_too_large") {
		return &Error{Code: "source_too_large"}
	}
	if githubapp.IsCode(err, "invalid_request") || githubapp.IsCode(err, "invalid_response") || githubapp.IsCode(err, "provider_rejected") {
		return &Error{Code: "invalid_source"}
	}
	return providerError(err)
}

func (service *Service) Disconnect(ctx context.Context, owner, id string) error {
	unlock := service.locks.lock(id)
	defer unlock()
	if _, err := service.repository.Get(ctx, owner, id); err != nil {
		return connectionError(err)
	}
	pending, err := service.repository.PendingAuthorizationIDs(ctx, owner, id)
	if err != nil {
		return internalError()
	}
	for _, authorizationID := range pending {
		if err := service.destroyAttemptCredentials(authorizationID); err != nil {
			return internalError()
		}
	}
	if err := service.destroyCredentials(id); err != nil {
		return internalError()
	}
	if err := service.repository.Disconnect(ctx, owner, id, service.now().UTC()); err != nil {
		return connectionError(err)
	}
	return nil
}

func (service *Service) handlePollError(ctx context.Context, owner string, connection Connection, err error, now time.Time) error {
	interval := connection.PollInterval
	switch {
	case githubapp.IsCode(err, "authorization_pending"):
		if updateErr := service.repository.AdvancePoll(ctx, owner, connection.ID, interval, now.Add(interval), now); updateErr != nil {
			return internalError()
		}
		return &Error{Code: "authorization_pending", RetryAfter: interval}
	case githubapp.IsCode(err, "slow_down"):
		if interval > 295*time.Second {
			interval = 300 * time.Second
		} else {
			interval += 5 * time.Second
		}
		if updateErr := service.repository.AdvancePoll(ctx, owner, connection.ID, interval, now.Add(interval), now); updateErr != nil {
			return internalError()
		}
		return &Error{Code: "authorization_pending", RetryAfter: interval}
	case githubapp.IsCode(err, "expired_token"), githubapp.IsCode(err, "access_denied"):
		status, code := StatusExpired, "authorization_expired"
		if githubapp.IsCode(err, "access_denied") {
			status, code = StatusDenied, "authorization_denied"
		}
		return service.purgeAndMark(ctx, owner, connection.ID, status, code)
	default:
		if updateErr := service.repository.AdvancePoll(ctx, owner, connection.ID, interval, now.Add(interval), now); updateErr != nil {
			return internalError()
		}
		return providerError(err)
	}
}

func (service *Service) finishBundle(ctx context.Context, owner, id string, bundle TokenBundle) error {
	if err := service.repository.Connect(ctx, owner, id, bundle, service.now().UTC()); err != nil {
		if errors.Is(err, ErrIdentityExists) {
			if terminalErr := service.purgeAndMark(ctx, owner, id, StatusAccessLost, "identity_already_connected"); !IsCode(terminalErr, "source_access_lost") {
				return terminalErr
			}
			return &Error{Code: "identity_already_connected"}
		}
		return internalError()
	}
	if err := service.credentials.RemoveDevice(id); err != nil {
		return internalError()
	}
	if err := service.credentials.RemoveExchange(id); err != nil {
		return internalError()
	}
	return nil
}

func (service *Service) loadBundle(ctx context.Context, owner string, connection Connection) (TokenBundle, error) {
	bundle, err := service.credentials.ReadBundle(connection.ID)
	if err != nil {
		return TokenBundle{}, service.loseAccess(ctx, owner, connection.ID, "credential_missing")
	}
	if bundle.Generation < connection.CredentialGeneration || bundle.ProviderUserID != connection.ProviderUserID {
		return TokenBundle{}, service.loseAccess(ctx, owner, connection.ID, "credential_generation_invalid")
	}
	if bundle.Generation > connection.CredentialGeneration {
		if err := service.repository.Connect(ctx, owner, connection.ID, bundle, service.now().UTC()); err != nil {
			return TokenBundle{}, internalError()
		}
	}
	if err := service.credentials.RemoveDevice(connection.ID); err != nil {
		return TokenBundle{}, internalError()
	}
	if err := service.credentials.RemoveExchange(connection.ID); err != nil {
		return TokenBundle{}, internalError()
	}
	return bundle, nil
}

func (service *Service) refreshLocked(ctx context.Context, owner string, connection Connection, bundle TokenBundle) (TokenBundle, error) {
	now := service.now().UTC()
	if !now.Before(bundle.RefreshExpiresAt) {
		return TokenBundle{}, service.loseAccess(ctx, owner, connection.ID, "refresh_expired")
	}
	tokens, err := service.provider.Refresh(ctx, bundle.RefreshToken)
	if err != nil {
		if githubapp.IsCode(err, "oauth_failed") || githubapp.IsCode(err, "unauthorized") || githubapp.IsCode(err, "expired_token") || githubapp.IsCode(err, "access_denied") {
			return TokenBundle{}, service.loseAccess(ctx, owner, connection.ID, "refresh_invalid")
		}
		return TokenBundle{}, providerError(err)
	}
	next := TokenBundle{Version: tokenBundleVersion, Generation: bundle.Generation + 1, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccessExpiresAt: now.Add(tokens.AccessExpiresIn), RefreshExpiresAt: now.Add(tokens.RefreshExpiresIn), ProviderUserID: bundle.ProviderUserID, ProviderLogin: bundle.ProviderLogin}
	if err := service.credentials.WriteBundle(connection.ID, next); err != nil {
		return TokenBundle{}, service.loseAccess(ctx, owner, connection.ID, "credential_rotation_failed")
	}
	if err := service.repository.Connect(ctx, owner, connection.ID, next, now); err != nil {
		return TokenBundle{}, internalError()
	}
	return next, nil
}

func (service *Service) loseAccess(ctx context.Context, owner, id, reason string) error {
	return service.purgeAndMark(ctx, owner, id, StatusAccessLost, reason)
}

func (service *Service) purgeAndMark(ctx context.Context, owner, id, status, reason string) error {
	if err := service.destroyCredentials(id); err != nil {
		return internalError()
	}
	if err := service.repository.MarkTerminal(ctx, owner, id, status, reason, service.now().UTC()); err != nil {
		return internalError()
	}
	code := reason
	if status == StatusAccessLost {
		code = "source_access_lost"
	}
	return &Error{Code: code}
}

func (service *Service) destroyCredentials(id string) error {
	deviceErr := service.credentials.RemoveDevice(id)
	exchangeErr := service.credentials.RemoveExchange(id)
	bundleErr := service.credentials.RemoveBundle(id)
	return errors.Join(deviceErr, exchangeErr, bundleErr)
}

func (service *Service) destroyAttemptCredentials(id string) error {
	return service.destroyCredentials(id)
}

func providerError(err error) error {
	for _, code := range []string{"provider_unavailable", "rate_limited"} {
		if githubapp.IsCode(err, code) {
			return &Error{Code: code}
		}
	}
	if githubapp.IsCode(err, "forbidden") {
		return &Error{Code: "authentication_required"}
	}
	return &Error{Code: "provider_unavailable"}
}

func connectionError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: "connection_not_found"}
	}
	return internalError()
}

func statusError(status string) error {
	switch status {
	case StatusAccessLost:
		return &Error{Code: "source_access_lost"}
	case StatusDenied:
		return &Error{Code: "authorization_denied"}
	case StatusExpired:
		return &Error{Code: "authorization_expired"}
	case StatusDisconnected:
		return &Error{Code: "connection_not_found"}
	default:
		return &Error{Code: "invalid_connection_state"}
	}
}

func internalError() error { return &Error{Code: "internal_error"} }

type keyedLockEntry struct {
	mutex sync.Mutex
	refs  int
}

type keyedLocks struct {
	mutex  sync.Mutex
	values map[string]*keyedLockEntry
}

func (locks *keyedLocks) lock(key string) func() {
	locks.mutex.Lock()
	if locks.values == nil {
		locks.values = make(map[string]*keyedLockEntry)
	}
	entry := locks.values[key]
	if entry == nil {
		entry = &keyedLockEntry{}
		locks.values[key] = entry
	}
	entry.refs++
	locks.mutex.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		locks.mutex.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.values, key)
		}
		locks.mutex.Unlock()
	}
}

func (connection Connection) String() string {
	return fmt.Sprintf("GitHub source connection %s (%s)", connection.ID, connection.Status)
}
