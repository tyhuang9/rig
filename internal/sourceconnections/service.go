package sourceconnections

import (
	"context"
	"errors"
	"fmt"
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

func (service *Service) List(ctx context.Context, owner string) ([]Connection, error) {
	return service.repository.List(ctx, owner)
}

func (service *Service) Start(ctx context.Context, owner string) (DeviceStart, error) {
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
		InstallURL: githubapp.WebOrigin + "/apps/" + service.appSlug + "/installations/new", ExpiresAt: expiresAt, PollInterval: authorization.Interval,
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
	if connection.PendingExpiresAt == nil || !now.Before(*connection.PendingExpiresAt) {
		return Connection{}, service.purgeAndMark(ctx, owner, id, StatusExpired, "authorization_expired")
	}
	if connection.NextPollAt != nil && now.Before(*connection.NextPollAt) {
		return Connection{}, &Error{Code: "poll_too_soon", RetryAfter: connection.NextPollAt.Sub(now)}
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
	if err := service.repository.AdvancePoll(ctx, owner, id, connection.PollInterval, postProviderNow.Add(connection.PollInterval), postProviderNow); err != nil {
		return Connection{}, internalError()
	}
	user, err := service.provider.CurrentUser(ctx, tokens.AccessToken)
	if err != nil {
		if githubapp.IsCode(err, "unauthorized") {
			return Connection{}, service.purgeAndMark(ctx, owner, id, StatusAccessLost, "source_access_lost")
		}
		return Connection{}, providerError(err)
	}
	bundle := TokenBundle{Version: tokenBundleVersion, Generation: 1, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, AccessExpiresAt: postProviderNow.Add(tokens.AccessExpiresIn), RefreshExpiresAt: postProviderNow.Add(tokens.RefreshExpiresIn), ProviderUserID: user.ID, ProviderLogin: user.Login}
	if err := service.credentials.WriteBundle(id, bundle); err != nil {
		if terminalErr := service.purgeAndMark(ctx, owner, id, StatusAccessLost, "credential_write_failed"); !IsCode(terminalErr, "source_access_lost") {
			return Connection{}, terminalErr
		}
		return Connection{}, internalError()
	}
	if err := service.finishBundle(ctx, owner, id, bundle); err != nil {
		return Connection{}, err
	}
	return service.repository.Get(ctx, owner, id)
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

func (service *Service) Disconnect(ctx context.Context, owner, id string) error {
	unlock := service.locks.lock(id)
	defer unlock()
	if _, err := service.repository.Get(ctx, owner, id); err != nil {
		return connectionError(err)
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
	return nil
}

func (service *Service) loadBundle(ctx context.Context, owner string, connection Connection) (TokenBundle, error) {
	if err := service.credentials.RemoveDevice(connection.ID); err != nil {
		return TokenBundle{}, internalError()
	}
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
	bundleErr := service.credentials.RemoveBundle(id)
	return errors.Join(deviceErr, bundleErr)
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
