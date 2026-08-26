package controllerrelay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const enrollmentAttemptLifetime = 10 * time.Minute

type enrollmentRepository interface {
	ActiveIdentity(context.Context) (ControllerIdentity, ControllerKey, error)
	CreateIdentity(context.Context, ControllerIdentity, ControllerKey) error
	CreateEnrollment(context.Context, Enrollment) error
	Enrollment(context.Context, string, string) (Enrollment, error)
	MarkEnrollmentPolled(context.Context, string, string, time.Time) error
	CompleteEnrollment(context.Context, string, string, string, string, string, time.Time) (Enrollment, error)
	ClearEnrollmentPollRef(context.Context, string, string, time.Time) error
	RecoverEnrollments(context.Context, time.Time, int) ([]Enrollment, error)
}

// sourceAccess deliberately exposes only an owner-scoped repository lookup.
// The concrete sourceconnections.Service retains and refreshes GitHub tokens
// internally; relay enrollment never receives token material.
type sourceAccess interface {
	Repository(context.Context, string, string, int64, int64) (sourceconnections.SourceRepository, error)
}

type enrollmentCredentials interface {
	WriteControllerKey(ControllerKeyBundle) (string, error)
	ReadControllerKey(string, string, []byte) (ControllerKeyBundle, error)
	RemoveControllerKey(string, string) error
	WriteEnrollmentPollToken(EnrollmentPollToken) (string, error)
	ReadEnrollmentPollToken(string, string) (EnrollmentPollToken, error)
	RemoveEnrollmentPollToken(string, string) error
	EnrollmentPollCredentials(string, int) (EnrollmentPollCredentialPage, error)
}

type EnrollmentService struct {
	repository  enrollmentRepository
	sources     sourceAccess
	credentials enrollmentCredentials
	client      EnrollmentClient
	now         func() time.Time
	entropy     io.Reader
	locks       enrollmentLocks
	recovery    enrollmentRecoveryCursor
}

func NewEnrollmentService(repository enrollmentRepository, sources sourceAccess, credentials enrollmentCredentials, client EnrollmentClient, now func() time.Time, entropy io.Reader) (*EnrollmentService, error) {
	if repository == nil || sources == nil || credentials == nil || client == nil {
		return nil, errors.New("controller relay enrollment dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	return &EnrollmentService{repository: repository, sources: sources, credentials: credentials, client: client, now: now, entropy: entropy}, nil
}

type StartEnrollmentInput struct {
	OwnerUserID    string
	ConnectionID   string
	InstallationID int64
	RepositoryID   int64
}

type EnrollmentStartResult struct {
	EnrollmentID     string
	AuthorizationURL string
	Status           string
	ExpiresAt        time.Time
}

func (result EnrollmentStartResult) String() string {
	return fmt.Sprintf("relay enrollment %s (%s)", result.EnrollmentID, result.Status)
}
func (result EnrollmentStartResult) GoString() string { return result.String() }
func (result EnrollmentStartResult) LogValue() slog.Value {
	return slog.GroupValue(slog.String("enrollment_id", result.EnrollmentID), slog.String("status", result.Status), slog.Time("expires_at", result.ExpiresAt))
}

type EnrollmentPollResult struct {
	EnrollmentID string
	BindingID    string
	Status       string
	ExpiresAt    time.Time
}

func (result EnrollmentPollResult) String() string {
	return fmt.Sprintf("relay enrollment %s (%s)", result.EnrollmentID, result.Status)
}
func (result EnrollmentPollResult) GoString() string { return result.String() }
func (result EnrollmentPollResult) LogValue() slog.Value {
	return slog.GroupValue(slog.String("enrollment_id", result.EnrollmentID), slog.String("status", result.Status))
}

type EnrollmentError struct {
	Code       string
	RetryAfter time.Duration
}

func (err *EnrollmentError) Error() string {
	if err == nil {
		return "controller relay enrollment failed"
	}
	return "controller relay enrollment failed: " + err.Code
}
func (err *EnrollmentError) String() string   { return err.Error() }
func (err *EnrollmentError) GoString() string { return err.Error() }
func (err *EnrollmentError) LogValue() slog.Value {
	if err == nil {
		return slog.GroupValue(slog.String("code", "relay_unavailable"))
	}
	return slog.GroupValue(slog.String("code", err.Code), slog.Duration("retry_after", err.RetryAfter))
}

func IsEnrollmentCode(err error, code string) bool {
	var enrollmentErr *EnrollmentError
	return errors.As(err, &enrollmentErr) && enrollmentErr.Code == code
}

func (service *EnrollmentService) Start(ctx context.Context, input StartEnrollmentInput) (EnrollmentStartResult, error) {
	if service == nil || ctx == nil || !validOpaqueID(input.OwnerUserID) || !validOpaqueID(input.ConnectionID) || input.InstallationID <= 0 || input.RepositoryID <= 0 {
		return EnrollmentStartResult{}, enrollmentFailure("invalid_request")
	}
	repository, err := service.sources.Repository(ctx, input.OwnerUserID, input.ConnectionID, input.InstallationID, input.RepositoryID)
	if err != nil {
		return EnrollmentStartResult{}, sourceAccessError(err)
	}
	if repository.ID != input.RepositoryID || repository.Archived || repository.Disabled {
		return EnrollmentStartResult{}, enrollmentFailure("invalid_source")
	}

	identity, key, bundle, err := service.activeIdentity(ctx)
	if err != nil {
		return EnrollmentStartResult{}, err
	}
	defer bundle.Destroy()
	now := service.now().UTC()
	if now.IsZero() {
		return EnrollmentStartResult{}, enrollmentFailure("internal_error")
	}
	expiresAt := now.Add(enrollmentAttemptLifetime)
	requestNonce, err := protocol.NewNonce(service.entropy)
	if err != nil {
		return EnrollmentStartResult{}, enrollmentFailure("entropy_unavailable")
	}
	publicKey := base64.RawURLEncoding.EncodeToString(bundle.PublicKey)
	proof := protocol.EnrollmentProof{
		ControllerID: identity.ControllerID, KeyID: key.KeyID, PublicKey: publicKey,
		InstallationID: input.InstallationID, RepositoryID: input.RepositoryID,
		RequestNonce: requestNonce, IssuedAt: now, ExpiresAt: expiresAt,
	}
	transcript, err := protocol.EnrollmentTranscript(proof)
	if err != nil {
		return EnrollmentStartResult{}, enrollmentFailure("internal_error")
	}
	signature, err := protocol.Sign(bundle.PrivateKey, transcript)
	clear(transcript)
	if err != nil {
		return EnrollmentStartResult{}, enrollmentFailure("identity_unavailable")
	}
	started, err := service.client.Start(ctx, RelayEnrollmentRequest{
		ControllerID: proof.ControllerID, KeyID: proof.KeyID, PublicKey: proof.PublicKey,
		InstallationID: proof.InstallationID, RepositoryID: proof.RepositoryID,
		RequestNonce: proof.RequestNonce, IssuedAt: proof.IssuedAt, ExpiresAt: proof.ExpiresAt,
		Signature: signature,
	})
	if err != nil {
		return EnrollmentStartResult{}, relayClientError(err)
	}
	defer started.Destroy()
	if len(started.PollToken) != pollTokenBytes {
		return EnrollmentStartResult{}, enrollmentFailure("relay_invalid_response")
	}
	enrollmentID, err := randomUUID(service.entropy)
	if err != nil {
		return EnrollmentStartResult{}, enrollmentFailure("entropy_unavailable")
	}
	protectedRef, err := service.credentials.WriteEnrollmentPollToken(EnrollmentPollToken{
		Version: credentialVersion, ControllerID: identity.ControllerID, EnrollmentID: enrollmentID,
		OwnerUserID: input.OwnerUserID, Token: started.PollToken,
	})
	if err != nil {
		return EnrollmentStartResult{}, enrollmentFailure("credential_unavailable")
	}
	enrollment := Enrollment{
		EnrollmentID: enrollmentID, OwnerUserID: input.OwnerUserID, ConnectionID: input.ConnectionID,
		ControllerID: identity.ControllerID, KeyID: key.KeyID,
		InstallationID: input.InstallationID, RepositoryID: input.RepositoryID,
		Purpose: EnrollmentPurpose, ProtectedPollRef: protectedRef, State: EnrollmentPending,
		CreatedAt: now, ExpiresAt: expiresAt, StateChangedAt: now, UpdatedAt: now,
	}
	if err = service.repository.CreateEnrollment(ctx, enrollment); err != nil {
		persisted, readErr := service.repository.Enrollment(ctx, input.OwnerUserID, enrollmentID)
		if readErr != nil || !samePendingEnrollment(persisted, enrollment) {
			_ = service.credentials.RemoveEnrollmentPollToken(identity.ControllerID, enrollmentID)
			return EnrollmentStartResult{}, enrollmentFailure("persistence_unavailable")
		}
	}
	return EnrollmentStartResult{EnrollmentID: enrollmentID, AuthorizationURL: started.AuthorizationURL, Status: EnrollmentPending, ExpiresAt: expiresAt}, nil
}

func (service *EnrollmentService) Poll(ctx context.Context, ownerUserID, enrollmentID string) (EnrollmentPollResult, error) {
	if service == nil || ctx == nil || !validOpaqueID(ownerUserID) || !validCanonicalUUID(enrollmentID) {
		return EnrollmentPollResult{}, enrollmentFailure("invalid_request")
	}
	unlock := service.locks.lock(enrollmentID)
	defer unlock()
	enrollment, err := service.repository.Enrollment(ctx, ownerUserID, enrollmentID)
	if errors.Is(err, ErrNotFound) {
		return EnrollmentPollResult{}, enrollmentFailure("enrollment_not_found")
	}
	if err != nil {
		return EnrollmentPollResult{}, enrollmentFailure("persistence_unavailable")
	}
	if enrollment.State != EnrollmentPending {
		if err = service.cleanupTerminal(ctx, enrollment); err != nil {
			return pollResult(enrollment), err
		}
		return pollResult(enrollment), nil
	}
	now := service.now().UTC()
	if !now.Before(enrollment.ExpiresAt) {
		completed, completeErr := service.complete(ctx, enrollment, EnrollmentExpired, "", "", now)
		if completeErr != nil {
			return EnrollmentPollResult{}, completeErr
		}
		if err = service.cleanupTerminal(ctx, completed); err != nil {
			return pollResult(completed), err
		}
		return pollResult(completed), nil
	}
	token, err := service.credentials.ReadEnrollmentPollToken(enrollment.ControllerID, enrollment.EnrollmentID)
	if err != nil || token.OwnerUserID != ownerUserID {
		token.Destroy()
		completed, completeErr := service.complete(ctx, enrollment, EnrollmentFailed, "", ErrorEnrollmentFailed, now)
		if completeErr != nil {
			return EnrollmentPollResult{}, completeErr
		}
		if cleanupErr := service.cleanupTerminal(ctx, completed); cleanupErr != nil {
			return pollResult(completed), cleanupErr
		}
		return pollResult(completed), nil
	}
	defer token.Destroy()
	status, err := service.client.Poll(ctx, token.Token)
	if err != nil {
		if IsClientCode(err, "enrollment_not_found") {
			completed, completeErr := service.complete(ctx, enrollment, EnrollmentFailed, "", ErrorEnrollmentFailed, now)
			if completeErr != nil {
				return EnrollmentPollResult{}, completeErr
			}
			if cleanupErr := service.cleanupTerminal(ctx, completed); cleanupErr != nil {
				return pollResult(completed), cleanupErr
			}
			return pollResult(completed), nil
		}
		return EnrollmentPollResult{}, relayClientError(err)
	}
	if status.Status == EnrollmentPending {
		if err = service.repository.MarkEnrollmentPolled(ctx, ownerUserID, enrollmentID, now); err != nil {
			return EnrollmentPollResult{}, enrollmentFailure("persistence_unavailable")
		}
		enrollment.LastPolledAt = &now
		enrollment.UpdatedAt = now
		return pollResult(enrollment), nil
	}
	terminalState, bindingID, errorCode, err := service.mapTerminalStatus(status.Status)
	if err != nil {
		return EnrollmentPollResult{}, err
	}
	completed, err := service.complete(ctx, enrollment, terminalState, bindingID, errorCode, now)
	if err != nil {
		return EnrollmentPollResult{}, err
	}
	if err = service.cleanupTerminal(ctx, completed); err != nil {
		return pollResult(completed), err
	}
	return pollResult(completed), nil
}

type EnrollmentRecoveryPage struct {
	Cleaned         int
	Scanned         int
	NextCursor      EnrollmentRecoveryCursor
	Complete        bool
	NextRunAt       time.Time
	PostExpiryRunAt time.Time
}

type EnrollmentRecoveryCursor struct {
	CredentialCursor string
	PostExpiryRunAt  time.Time
}

// Recover advances one bounded page per invocation. It is the compatibility
// seam for startup and scheduled jobs that do not persist their own cursor.
// A process-local cursor prevents a healthy prefix from starving later paths.
func (service *EnrollmentService) Recover(ctx context.Context, limit int) (int, error) {
	if service == nil {
		return 0, enrollmentFailure("invalid_request")
	}
	service.recovery.mutex.Lock()
	defer service.recovery.mutex.Unlock()
	page, err := service.RecoverPage(ctx, service.recovery.cursor, limit)
	if err != nil {
		return page.Cleaned, err
	}
	service.recovery.cursor = page.NextCursor
	if page.Complete {
		service.recovery.cursor = EnrollmentRecoveryCursor{}
	}
	return page.Cleaned, nil
}

// RecoverPage is the explicit cursor and scheduling seam for durable startup
// or periodic runners. Callers continue immediately while Complete is false,
// persist NextCursor between invocations when needed, and schedule a new full
// pass at NextRunAt after the current pass completes. Ten-minute relay expiry
// remains authoritative; this method never extends an attempt.
func (service *EnrollmentService) RecoverPage(ctx context.Context, cursor EnrollmentRecoveryCursor, limit int) (EnrollmentRecoveryPage, error) {
	if service == nil || ctx == nil || limit < 1 || limit > 1000 {
		return EnrollmentRecoveryPage{}, enrollmentFailure("invalid_request")
	}
	if cursor.CredentialCursor == "" && !cursor.PostExpiryRunAt.IsZero() {
		return EnrollmentRecoveryPage{}, enrollmentFailure("invalid_request")
	}
	if _, _, err := parseEnrollmentPollCursor(cursor.CredentialCursor); err != nil {
		return EnrollmentRecoveryPage{}, enrollmentFailure("invalid_request")
	}
	now := service.now().UTC()
	rows, err := service.repository.RecoverEnrollments(ctx, now, limit)
	if err != nil {
		return EnrollmentRecoveryPage{}, enrollmentFailure("persistence_unavailable")
	}
	result := EnrollmentRecoveryPage{PostExpiryRunAt: cursor.PostExpiryRunAt}
	for _, enrollment := range rows {
		if err = service.cleanupTerminal(ctx, enrollment); err != nil {
			return result, err
		}
		result.Cleaned++
	}
	inventory, err := service.credentials.EnrollmentPollCredentials(cursor.CredentialCursor, limit)
	if err != nil {
		return result, enrollmentFailure("credential_unavailable")
	}
	result.Scanned = len(inventory.Credentials)
	result.Complete = inventory.Complete
	for _, metadata := range inventory.Credentials {
		enrollment, readErr := service.repository.Enrollment(ctx, metadata.OwnerUserID, metadata.EnrollmentID)
		if errors.Is(readErr, ErrNotFound) {
			if removeErr := service.credentials.RemoveEnrollmentPollToken(metadata.ControllerID, metadata.EnrollmentID); removeErr != nil {
				return result, enrollmentFailure("credential_cleanup_pending")
			}
			result.Cleaned++
			continue
		}
		if readErr != nil {
			return result, enrollmentFailure("persistence_unavailable")
		}
		if enrollment.ControllerID != metadata.ControllerID || enrollment.ProtectedPollRef != metadata.ProtectedRef {
			return result, enrollmentFailure("credential_unavailable")
		}
		if enrollment.State == EnrollmentPending {
			if result.PostExpiryRunAt.IsZero() || enrollment.ExpiresAt.Before(result.PostExpiryRunAt) {
				result.PostExpiryRunAt = enrollment.ExpiresAt
			}
		} else {
			if cleanupErr := service.cleanupTerminal(ctx, enrollment); cleanupErr != nil {
				return result, cleanupErr
			}
			result.Cleaned++
		}
	}
	if !result.Complete {
		result.NextRunAt = now
		result.NextCursor = EnrollmentRecoveryCursor{CredentialCursor: inventory.NextCursor, PostExpiryRunAt: result.PostExpiryRunAt}
	} else {
		result.NextRunAt = now.Add(enrollmentAttemptLifetime)
		if !result.PostExpiryRunAt.IsZero() && result.PostExpiryRunAt.Before(result.NextRunAt) {
			result.NextRunAt = result.PostExpiryRunAt
		}
	}
	return result, nil
}

func (service *EnrollmentService) activeIdentity(ctx context.Context) (ControllerIdentity, ControllerKey, ControllerKeyBundle, error) {
	identity, key, err := service.repository.ActiveIdentity(ctx)
	if err == nil {
		bundle, validateErr := service.validateActiveIdentity(identity, key)
		return identity, key, bundle, validateErr
	}
	if !errors.Is(err, ErrNotFound) {
		return ControllerIdentity{}, ControllerKey{}, ControllerKeyBundle{}, enrollmentFailure("persistence_unavailable")
	}
	controllerID, err := randomUUID(service.entropy)
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, ControllerKeyBundle{}, enrollmentFailure("entropy_unavailable")
	}
	keyID, err := randomUUID(service.entropy)
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, ControllerKeyBundle{}, enrollmentFailure("entropy_unavailable")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(service.entropy)
	if err != nil {
		return ControllerIdentity{}, ControllerKey{}, ControllerKeyBundle{}, enrollmentFailure("entropy_unavailable")
	}
	bundle := ControllerKeyBundle{Version: credentialVersion, ControllerID: controllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey}
	protectedRef, err := service.credentials.WriteControllerKey(bundle)
	if err != nil {
		bundle.Destroy()
		return ControllerIdentity{}, ControllerKey{}, ControllerKeyBundle{}, enrollmentFailure("credential_unavailable")
	}
	now := service.now().UTC()
	identity = ControllerIdentity{ControllerID: controllerID, State: ControllerActive, CreatedAt: now, UpdatedAt: now}
	activated := now
	key = ControllerKey{
		KeyID: keyID, ControllerID: controllerID, PublicKey: append([]byte(nil), publicKey...), Algorithm: KeyAlgorithmEd25519,
		State: KeyActive, ProtectedKeyRef: protectedRef, CreatedAt: now, UpdatedAt: now,
		ActivatedAt: &activated, PossessionConfirmedAt: &activated,
	}
	if err = service.repository.CreateIdentity(ctx, identity, key); err == nil {
		return identity, key, bundle, nil
	}
	storedIdentity, storedKey, readErr := service.repository.ActiveIdentity(ctx)
	if readErr == nil && storedIdentity.ControllerID == controllerID && storedKey.KeyID == keyID {
		return identity, key, bundle, nil
	}
	_ = service.credentials.RemoveControllerKey(controllerID, keyID)
	bundle.Destroy()
	if readErr == nil {
		storedBundle, validateErr := service.validateActiveIdentity(storedIdentity, storedKey)
		return storedIdentity, storedKey, storedBundle, validateErr
	}
	return ControllerIdentity{}, ControllerKey{}, ControllerKeyBundle{}, enrollmentFailure("persistence_unavailable")
}

func (service *EnrollmentService) validateActiveIdentity(identity ControllerIdentity, key ControllerKey) (ControllerKeyBundle, error) {
	if identity.State != ControllerActive || key.State != KeyActive || identity.ControllerID != key.ControllerID || key.Algorithm != KeyAlgorithmEd25519 || key.ProtectedKeyRef != ProtectedKeyRef(identity.ControllerID, key.KeyID) || len(key.PublicKey) != ed25519.PublicKeySize {
		return ControllerKeyBundle{}, enrollmentFailure("identity_unavailable")
	}
	bundle, err := service.credentials.ReadControllerKey(identity.ControllerID, key.KeyID, key.PublicKey)
	if err != nil {
		return ControllerKeyBundle{}, enrollmentFailure("identity_unavailable")
	}
	return bundle, nil
}

func (service *EnrollmentService) complete(ctx context.Context, enrollment Enrollment, state, bindingID, errorCode string, now time.Time) (Enrollment, error) {
	completed, err := service.repository.CompleteEnrollment(ctx, enrollment.OwnerUserID, enrollment.EnrollmentID, state, bindingID, errorCode, now)
	if err == nil {
		return completed, nil
	}
	stored, readErr := service.repository.Enrollment(ctx, enrollment.OwnerUserID, enrollment.EnrollmentID)
	if readErr == nil && stored.State == state && stored.BindingID == bindingID && stored.LastErrorCode == errorCode {
		return stored, nil
	}
	return Enrollment{}, enrollmentFailure("persistence_unavailable")
}

func (service *EnrollmentService) cleanupTerminal(ctx context.Context, enrollment Enrollment) error {
	if enrollment.State == EnrollmentPending {
		return enrollmentFailure("invalid_state")
	}
	if enrollment.ProtectedPollRef == "" {
		return nil
	}
	if enrollment.ProtectedPollRef != ProtectedEnrollmentPollRef(enrollment.ControllerID, enrollment.EnrollmentID) {
		return enrollmentFailure("credential_unavailable")
	}
	if err := service.credentials.RemoveEnrollmentPollToken(enrollment.ControllerID, enrollment.EnrollmentID); err != nil {
		return enrollmentFailure("credential_cleanup_pending")
	}
	if err := service.repository.ClearEnrollmentPollRef(ctx, enrollment.OwnerUserID, enrollment.EnrollmentID, service.now().UTC()); err != nil {
		return enrollmentFailure("persistence_unavailable")
	}
	return nil
}

func (service *EnrollmentService) mapTerminalStatus(status string) (string, string, string, error) {
	switch status {
	case EnrollmentAuthorized:
		bindingID, err := randomUUID(service.entropy)
		if err != nil {
			return "", "", "", enrollmentFailure("entropy_unavailable")
		}
		return EnrollmentAuthorized, bindingID, "", nil
	case EnrollmentDenied:
		return EnrollmentDenied, "", "", nil
	case EnrollmentExpired:
		return EnrollmentExpired, "", "", nil
	case EnrollmentFailed:
		return EnrollmentFailed, "", ErrorEnrollmentFailed, nil
	default:
		return "", "", "", enrollmentFailure("relay_invalid_response")
	}
}

func relayClientError(err error) error {
	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		return enrollmentFailure("relay_unavailable")
	}
	switch clientErr.Code {
	case "relay_rate_limited":
		return &EnrollmentError{Code: "relay_unavailable", RetryAfter: clientErr.RetryAfter}
	case "invalid_response":
		return enrollmentFailure("relay_invalid_response")
	case "relay_rejected":
		return enrollmentFailure("relay_rejected")
	default:
		return enrollmentFailure("relay_unavailable")
	}
}

func sourceAccessError(err error) error {
	for _, code := range []string{"source_access_lost", "invalid_source", "authentication_required"} {
		if sourceconnections.IsCode(err, code) {
			return enrollmentFailure(code)
		}
	}
	if sourceconnections.IsCode(err, "rate_limited") || sourceconnections.IsCode(err, "provider_unavailable") {
		return enrollmentFailure("provider_unavailable")
	}
	return enrollmentFailure("provider_unavailable")
}

func enrollmentFailure(code string) error { return &EnrollmentError{Code: code} }

func randomUUID(entropy io.Reader) (string, error) {
	id, err := uuid.NewRandomFromReader(entropy)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func samePendingEnrollment(actual, expected Enrollment) bool {
	return actual.EnrollmentID == expected.EnrollmentID && actual.OwnerUserID == expected.OwnerUserID && actual.ConnectionID == expected.ConnectionID && actual.ControllerID == expected.ControllerID && actual.KeyID == expected.KeyID && actual.InstallationID == expected.InstallationID && actual.RepositoryID == expected.RepositoryID && actual.Purpose == expected.Purpose && actual.ProtectedPollRef == expected.ProtectedPollRef && actual.State == EnrollmentPending && actual.ExpiresAt.Equal(expected.ExpiresAt)
}

func pollResult(enrollment Enrollment) EnrollmentPollResult {
	return EnrollmentPollResult{EnrollmentID: enrollment.EnrollmentID, BindingID: enrollment.BindingID, Status: enrollment.State, ExpiresAt: enrollment.ExpiresAt}
}

type enrollmentLockEntry struct {
	mutex sync.Mutex
	refs  int
}

type enrollmentLocks struct {
	mutex sync.Mutex
	items map[string]*enrollmentLockEntry
}

type enrollmentRecoveryCursor struct {
	mutex  sync.Mutex
	cursor EnrollmentRecoveryCursor
}

func (locks *enrollmentLocks) lock(key string) func() {
	locks.mutex.Lock()
	if locks.items == nil {
		locks.items = make(map[string]*enrollmentLockEntry)
	}
	entry := locks.items[key]
	if entry == nil {
		entry = &enrollmentLockEntry{}
		locks.items[key] = entry
	}
	entry.refs++
	locks.mutex.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		locks.mutex.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.items, key)
		}
		locks.mutex.Unlock()
	}
}
