package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	ControlContinue SessionControlAction = iota
	ControlReconnect
	ControlStop

	controlErrorInvalid     = "invalid_control"
	controlErrorState       = "control_state"
	controlErrorPersistence = "persistence_unavailable"
	controlErrorCredential  = "credential_unavailable"
	controlErrorExpired     = "rotation_expired"
	controlErrorRevoked     = "identity_revoked"
)

type SessionControlAction uint8

type SessionControlContext struct {
	ControllerID string
	KeyID        string
	Epoch        uint64
	Fence        uint64
	SessionID    string
	ExpiresAt    time.Time
}

func (SessionControlContext) String() string         { return "controller relay control session" }
func (value SessionControlContext) GoString() string { return value.String() }
func (SessionControlContext) LogValue() slog.Value {
	return slog.GroupValue(slog.String("kind", "controller_relay_control_session"))
}

type SessionControlResult struct {
	Response protocol.Frame
	Action   SessionControlAction
}

type RevokedKeyCleanupCandidate struct {
	ControllerID    string
	KeyID           string
	ProtectedKeyRef string
}

type RevokedKeyCleanupPage struct {
	Candidates []RevokedKeyCleanupCandidate
	Cleaned    int
	NextCursor string
	Complete   bool
}

type ControllerKeyRecoveryCursor struct {
	LeaseCursor         string
	RevokedCursor       string
	CredentialCursor    string
	TemporaryCursor     string
	LeasesComplete      bool
	RevokedComplete     bool
	CredentialsComplete bool
	TemporaryComplete   bool
}

type ControllerKeyRecoveryPage struct {
	Cleaned           int
	Scanned           int
	LeaseScanned      int
	RevokedScanned    int
	CredentialScanned int
	TemporaryScanned  int
	NeedsAttention    []ControllerKeyCredentialIssue
	NextCursor        ControllerKeyRecoveryCursor
	Complete          bool
}

func (SessionControlResult) String() string         { return "controller relay control result" }
func (value SessionControlResult) GoString() string { return value.String() }
func (value SessionControlResult) LogValue() slog.Value {
	return slog.GroupValue(slog.Uint64("action", uint64(value.Action)))
}

type SessionControlError struct {
	Code  string
	cause error
}

func (err *SessionControlError) Error() string {
	if err == nil {
		return "controller relay control failed"
	}
	return "controller relay control failed: " + safeControlErrorCode(err.Code)
}
func (err *SessionControlError) String() string   { return err.Error() }
func (err *SessionControlError) GoString() string { return err.Error() }
func (err *SessionControlError) LogValue() slog.Value {
	if err == nil {
		return slog.GroupValue(slog.String("code", controlErrorState))
	}
	return slog.GroupValue(slog.String("code", safeControlErrorCode(err.Code)))
}

func safeControlErrorCode(code string) string {
	switch code {
	case controlErrorInvalid, controlErrorState, controlErrorPersistence, controlErrorCredential, controlErrorExpired, controlErrorRevoked:
		return code
	default:
		return controlErrorState
	}
}

func controlFailure(code string) error { return &SessionControlError{Code: safeControlErrorCode(code)} }

func canceledControlFailure(code string) error {
	return &SessionControlError{Code: safeControlErrorCode(code), cause: context.Canceled}
}

func controlFailureWasCanceled(err error) bool {
	var controlErr *SessionControlError
	return errors.As(err, &controlErr) && errors.Is(controlErr.cause, context.Canceled)
}

type sessionControlRepository interface {
	ActiveIdentity(context.Context) (ControllerIdentity, ControllerKey, error)
	Binding(context.Context, string, string) (InstallationBinding, error)
	BindingForController(context.Context, string, string) (InstallationBinding, error)
	Key(context.Context, string, string) (ControllerKey, error)
	Rotation(context.Context, string, string) (KeyRotation, error)
	RotationByNewKey(context.Context, string, string) (KeyRotation, error)
	LiveKeyRotation(context.Context, string) (KeyRotation, ControllerKey, error)
	SessionStatus(context.Context, string) (SessionStatus, error)
	BeginControllerKeyWrite(context.Context, ControllerKeyIOLease) error
	MaterializePendingKeyAndRotation(context.Context, ControllerKeyIOLease, ControllerKey, KeyRotation, time.Time) error
	ExpiredControllerKeyIOLeases(context.Context, string, time.Time, int) (ControllerKeyIOLeasePage, error)
	ClaimExpiredControllerKeyIOLease(context.Context, ControllerKeyIOLease, string, time.Time, time.Time) (ControllerKeyIOLease, error)
	AcquireControllerKeyCleanupLease(context.Context, ControllerKeyIOLease) error
	FinishControllerKeyIOLease(context.Context, ControllerKeyIOLease) error
	PrepareBindingRemoval(context.Context, string, string, OutboundCommand, time.Time) (InstallationBinding, OutboundCommand, error)
	CompleteBindingRemoval(context.Context, string, protocol.BindingRemoved, time.Time) error
	PendingControlCommands(context.Context, string, int) ([]OutboundCommand, error)
	ControlCommandForAggregate(context.Context, string, string, string, string) (OutboundCommand, error)
	LoadControlCommand(context.Context, string, string) (OutboundCommand, error)
	PrepareRotationProposal(context.Context, OutboundCommand, time.Time) (KeyRotation, OutboundCommand, error)
	PrepareRotationConfirmation(context.Context, string, OutboundCommand, time.Time) (KeyRotation, OutboundCommand, error)
	ConfirmRotationAndPrepareFinalize(context.Context, string, string, string, OutboundCommand, time.Time) (KeyRotation, OutboundCommand, error)
	CompleteRotationFinalized(context.Context, string, protocol.KeyRotationFinalized, time.Time) error
	CompleteRotationAfterReady(context.Context, string, string, uint64, uint64, time.Time) error
	FailExpiredRotation(context.Context, string, string, time.Time) error
	RevokedRotationKeyCleanupCandidate(context.Context, RevokedKeyCleanupCandidate) (RevokedKeyCleanupCandidate, error)
	RevokedRotationKeyCleanupCandidates(context.Context, string, int) (RevokedKeyCleanupPage, error)
	MarkRevokedControllerKeyCleared(context.Context, RevokedKeyCleanupCandidate, time.Time) error
}

// fencedSessionControlRepository is implemented by the durable repository.
// Test-only repositories may omit it, but a production inbound control frame
// can never mutate durable state without its active Ready epoch and fence.
type fencedSessionControlRepository interface {
	CompleteBindingRemovalFenced(context.Context, string, uint64, uint64, protocol.BindingRemoved, time.Time) error
	PrepareRotationConfirmationFenced(context.Context, string, uint64, uint64, OutboundCommand, time.Time) (KeyRotation, OutboundCommand, error)
	ConfirmRotationAndPrepareFinalizeFenced(context.Context, string, string, string, uint64, uint64, OutboundCommand, time.Time) (KeyRotation, OutboundCommand, error)
	CompleteRotationFinalizedFenced(context.Context, string, uint64, uint64, protocol.KeyRotationFinalized, time.Time) error
	FailExpiredRotationFenced(context.Context, string, string, uint64, uint64, time.Time) error
}

type sessionControlCredentials interface {
	WriteControllerKey(ControllerKeyBundle) (string, error)
	ReadControllerKey(controllerID, keyID string, expectedPublicKey []byte) (ControllerKeyBundle, error)
	RemoveControllerKeyWithResult(controllerID, keyID string) (bool, error)
	ControllerKeyCredentials(string, int) (ControllerKeyCredentialPage, error)
	ControllerKeyTemporaryArtifacts(string, int) (ControllerKeyTemporaryArtifactPage, error)
	RemoveControllerKeyTemporaryArtifact(string, string) (bool, error)
}

type SessionControlConfig struct {
	RotationLifetime     time.Duration
	MaxChallengeLifetime time.Duration
	MaxPending           int
	KeyWriteLease        time.Duration
	KeyRecoveryLease     time.Duration
	KeyWriteContention   time.Duration
	Now                  func() time.Time
	Entropy              io.Reader
	GenerateKey          func(io.Reader) (ed25519.PublicKey, ed25519.PrivateKey, error)
	NewID                func(io.Reader) (string, error)
}

func DefaultSessionControlConfig() SessionControlConfig {
	return SessionControlConfig{
		RotationLifetime:     15 * time.Minute,
		MaxChallengeLifetime: 5 * time.Minute,
		MaxPending:           256,
		KeyWriteLease:        5 * time.Minute,
		KeyRecoveryLease:     2 * time.Minute,
		KeyWriteContention:   2 * time.Second,
		Now:                  time.Now,
		Entropy:              rand.Reader,
		GenerateKey:          ed25519.GenerateKey,
		NewID: func(entropy io.Reader) (string, error) {
			value, err := uuid.NewRandomFromReader(entropy)
			return value.String(), err
		},
	}
}

type SessionControlService struct {
	repository  sessionControlRepository
	credentials sessionControlCredentials
	config      SessionControlConfig
	entropyMu   sync.Mutex
	recoveryMu  sync.Mutex
	recovery    ControllerKeyRecoveryCursor
}

func (service *SessionControlService) requiresSessionFence() bool {
	if service == nil {
		return false
	}
	_, ok := service.repository.(fencedSessionControlRepository)
	return ok
}

func NewSessionControlService(repository sessionControlRepository, credentials sessionControlCredentials, config SessionControlConfig) (*SessionControlService, error) {
	if repository == nil || credentials == nil || !validSessionControlConfig(config) {
		return nil, errors.New("invalid controller relay control configuration")
	}
	return &SessionControlService{repository: repository, credentials: credentials, config: config}, nil
}

func (service *SessionControlService) RequestBindingRemoval(ctx context.Context, owner, bindingID string) (*protocol.BindingRemove, error) {
	if service == nil || ctx == nil {
		return nil, controlFailure(controlErrorInvalid)
	}
	binding, err := service.repository.Binding(ctx, owner, bindingID)
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	frame, command, err := service.newBindingRemoval(binding)
	if err != nil {
		return nil, err
	}
	persistedBinding, persisted, err := service.repository.PrepareBindingRemoval(ctx, owner, bindingID, command, service.now())
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	if persisted.MessageID == command.MessageID {
		return frame, nil
	}
	return service.bindingRemovalFrame(persistedBinding, persisted)
}

func (service *SessionControlService) StartKeyRotation(ctx context.Context, controllerID string) (*protocol.KeyRotationPropose, error) {
	if service == nil || ctx == nil || !canonicalUUID(controllerID) {
		return nil, controlFailure(controlErrorInvalid)
	}
	if frame, found, err := service.replayLiveKeyRotation(ctx, controllerID); found || err != nil {
		return frame, err
	}
	identity, active, err := service.repository.ActiveIdentity(ctx)
	if err != nil || identity.ControllerID != controllerID || active.ControllerID != controllerID || active.State != KeyActive {
		return nil, service.repositoryFailure(err)
	}
	service.entropyMu.Lock()
	publicKey, privateKey, err := service.config.GenerateKey(service.config.Entropy)
	service.entropyMu.Unlock()
	if err != nil || len(publicKey) != ed25519.PublicKeySize || len(privateKey) != ed25519.PrivateKeySize {
		clear(publicKey)
		clear(privateKey)
		return nil, controlFailure(controlErrorCredential)
	}
	defer clear(publicKey)
	defer clear(privateKey)
	keyID, err := service.newID()
	if err != nil || !canonicalUUID(keyID) {
		return nil, controlFailure(controlErrorCredential)
	}
	rotationID, err := service.newID()
	if err != nil || !canonicalUUID(rotationID) || rotationID == keyID {
		return nil, controlFailure(controlErrorCredential)
	}
	now := service.now()
	leaseID, err := service.newID()
	if err != nil || !canonicalUUID(leaseID) || leaseID == keyID || leaseID == rotationID {
		return nil, controlFailure(controlErrorCredential)
	}
	lease := ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(controllerID), ControllerID: controllerID, LeaseID: leaseID, Operation: ControllerKeyIOWrite, Phase: ControllerKeyIOActive, Fence: 1,
		LeaseExpiresAt: now.Add(service.config.KeyWriteLease), KeyID: keyID, RotationID: rotationID, OldKeyID: active.KeyID,
		PublicKey: append([]byte(nil), publicKey...), ProtectedKeyRef: ProtectedKeyRef(controllerID, keyID), CreatedAt: now, UpdatedAt: now,
	}
	defer clear(lease.PublicKey)
	if err = service.repository.BeginControllerKeyWrite(ctx, lease); err != nil {
		if errors.Is(err, ErrConflict) {
			frame, waitErr := service.waitForLiveKeyRotationOrWriteLease(ctx, lease)
			if frame != nil || waitErr != nil {
				return frame, waitErr
			}
		} else {
			return nil, service.repositoryFailure(err)
		}
	}
	bundle := ControllerKeyBundle{Version: credentialVersion, ControllerID: controllerID, KeyID: keyID, PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), PublicKey: append(ed25519.PublicKey(nil), publicKey...)}
	defer bundle.Destroy()
	protectedRef, err := service.credentials.WriteControllerKey(bundle)
	bundle.Destroy()
	clear(privateKey)
	if err != nil || protectedRef != ProtectedKeyRef(controllerID, keyID) {
		return nil, controlFailure(controlErrorCredential)
	}
	key := ControllerKey{KeyID: keyID, ControllerID: controllerID, PublicKey: append([]byte(nil), publicKey...), Algorithm: KeyAlgorithmEd25519, State: KeyPending, ProtectedKeyRef: protectedRef, CreatedAt: now, UpdatedAt: now}
	defer clear(key.PublicKey)
	rotation := KeyRotation{RotationID: rotationID, ControllerID: controllerID, OldKeyID: active.KeyID, NewKeyID: keyID, State: RotationPrepare, ExpiresAt: now.Add(service.config.RotationLifetime), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err = service.repository.MaterializePendingKeyAndRotation(ctx, lease, key, rotation, service.now()); err != nil {
		// Materialization errors can be commit-ambiguous. The protected file is
		// retained; either authoritative metadata exists or the durable lease is
		// the only recovery owner.
		return nil, service.repositoryFailure(err)
	}
	persistedKey, persistedRotation := key, rotation
	expirationNow := service.now()
	if !persistedRotation.ExpiresAt.After(expirationNow) && persistedRotation.State != RotationFinalize {
		if failErr := service.failExpiredRotation(ctx, persistedRotation, expirationNow); failErr != nil {
			return nil, failErr
		}
		return nil, controlFailure(controlErrorExpired)
	}
	frame, command, err := service.newRotationProposal(persistedRotation, persistedKey)
	if err != nil {
		return nil, err
	}
	_, persisted, err := service.repository.PrepareRotationProposal(ctx, command, service.now())
	if err != nil {
		frame.NewPublicKey = ""
		return nil, service.repositoryFailure(err)
	}
	if persisted.MessageID == command.MessageID {
		return frame, nil
	}
	frame.NewPublicKey = ""
	return service.rotationProposalFrame(persistedRotation, persistedKey, persisted)
}

func (service *SessionControlService) Pending(ctx context.Context, session SessionControlContext, limit int) ([]protocol.Frame, error) {
	if service == nil || ctx == nil || !validSessionControlContext(session) || limit < 1 || limit > service.config.MaxPending {
		return nil, controlFailure(controlErrorInvalid)
	}
	commands, err := service.repository.PendingControlCommands(ctx, session.ControllerID, limit)
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	frames := make([]protocol.Frame, 0, len(commands))
	for _, command := range commands {
		// A confirmation depends on the relay challenge. Replaying Propose causes
		// the relay to return its durable challenge, from which Confirm is
		// deterministically reconstructed without persisting nonce/signature data.
		if command.CommandType == CommandRotationConfirm {
			continue
		}
		frame, buildErr := service.frameForCommand(ctx, session, command)
		if buildErr != nil {
			destroySessionControlFrames(frames)
			return nil, buildErr
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func (service *SessionControlService) Handle(ctx context.Context, session SessionControlContext, frame protocol.Frame) (SessionControlResult, error) {
	if service == nil || ctx == nil || !validSessionControlContext(session) || protocol.Validate(frame) != nil {
		destroyInboundSessionControlFrame(frame)
		return SessionControlResult{}, controlFailure(controlErrorInvalid)
	}
	switch value := frame.(type) {
	case *protocol.BindingRemoved:
		if err := service.completeBindingRemoval(ctx, session, *value); err != nil {
			return SessionControlResult{}, service.repositoryFailure(err)
		}
		return SessionControlResult{Action: ControlContinue}, nil
	case *protocol.KeyRotationChallenge:
		response, err := service.handleRotationChallenge(ctx, session, value)
		if err != nil {
			return SessionControlResult{Action: ControlContinue}, err
		}
		return SessionControlResult{Response: response, Action: ControlContinue}, nil
	case *protocol.KeyRotationConfirmed:
		response, err := service.handleRotationConfirmed(ctx, session, value)
		if err != nil {
			return SessionControlResult{Action: ControlContinue}, err
		}
		return SessionControlResult{Response: response, Action: ControlContinue}, nil
	case *protocol.KeyRotationFinalized:
		if err := service.completeRotationFinalized(ctx, session, *value); err != nil {
			return SessionControlResult{}, service.repositoryFailure(err)
		}
		return SessionControlResult{Action: ControlReconnect}, nil
	case *protocol.KeyRevoked:
		if value.ControllerID != session.ControllerID || value.KeyID != session.KeyID {
			return SessionControlResult{}, controlFailure(controlErrorInvalid)
		}
		return SessionControlResult{Action: ControlStop}, controlFailure(controlErrorRevoked)
	case *protocol.ControllerRevoked:
		if value.ControllerID != session.ControllerID {
			return SessionControlResult{}, controlFailure(controlErrorInvalid)
		}
		return SessionControlResult{Action: ControlStop}, controlFailure(controlErrorRevoked)
	default:
		return SessionControlResult{}, controlFailure(controlErrorInvalid)
	}
}

func (service *SessionControlService) completeBindingRemoval(ctx context.Context, session SessionControlContext, response protocol.BindingRemoved) error {
	if repository, ok := service.repository.(fencedSessionControlRepository); ok {
		if session.Epoch == 0 || session.Fence == 0 {
			return ErrState
		}
		return repository.CompleteBindingRemovalFenced(ctx, session.ControllerID, session.Epoch, session.Fence, response, service.now())
	}
	return service.repository.CompleteBindingRemoval(ctx, session.ControllerID, response, service.now())
}

func (service *SessionControlService) completeRotationFinalized(ctx context.Context, session SessionControlContext, response protocol.KeyRotationFinalized) error {
	if repository, ok := service.repository.(fencedSessionControlRepository); ok {
		if session.Epoch == 0 || session.Fence == 0 {
			return ErrState
		}
		return repository.CompleteRotationFinalizedFenced(ctx, session.ControllerID, session.Epoch, session.Fence, response, service.now())
	}
	return service.repository.CompleteRotationFinalized(ctx, session.ControllerID, response, service.now())
}

func destroyInboundSessionControlFrame(frame protocol.Frame) {
	if challenge, ok := frame.(*protocol.KeyRotationChallenge); ok && challenge != nil {
		challenge.ServerNonce = ""
	}
}

// CompleteRotationAfterFencedReady never manufactures session evidence. It
// requires the lifecycle owner to have already persisted an exact Ready row.
func (service *SessionControlService) CompleteRotationAfterFencedReady(ctx context.Context, controllerID, keyID string, epoch, fence uint64) error {
	if service == nil || ctx == nil || !canonicalUUID(controllerID) || !canonicalUUID(keyID) || epoch == 0 || fence == 0 {
		return controlFailure(controlErrorInvalid)
	}
	status, err := service.repository.SessionStatus(ctx, controllerID)
	if err != nil {
		return service.repositoryFailure(err)
	}
	if status.State != SessionReady || status.KeyID != keyID || status.Epoch != epoch || status.Fence != fence || status.LastReadyAt == nil {
		return controlFailure(controlErrorState)
	}
	rotation, err := service.repository.RotationByNewKey(ctx, controllerID, keyID)
	if err != nil {
		return service.repositoryFailure(err)
	}
	if rotation.State == RotationCompleted {
		candidate := RevokedKeyCleanupCandidate{ControllerID: controllerID, KeyID: rotation.OldKeyID, ProtectedKeyRef: ProtectedKeyRef(controllerID, rotation.OldKeyID)}
		_, err = service.cleanupRevokedControllerKey(ctx, candidate)
		return err
	}
	if rotation.State != RotationFinalize {
		return controlFailure(controlErrorState)
	}
	finalize, err := service.repository.ControlCommandForAggregate(ctx, controllerID, "", rotation.RotationID, "finalize")
	if err != nil {
		return service.repositoryFailure(err)
	}
	if finalize.State != CommandCompleted || finalize.CompletedAt == nil {
		return controlFailure(controlErrorState)
	}
	if err = service.repository.CompleteRotationAfterReady(ctx, controllerID, rotation.RotationID, epoch, fence, service.now()); err != nil {
		return service.repositoryFailure(err)
	}
	candidate := RevokedKeyCleanupCandidate{ControllerID: controllerID, KeyID: rotation.OldKeyID, ProtectedKeyRef: ProtectedKeyRef(controllerID, rotation.OldKeyID)}
	_, err = service.cleanupRevokedControllerKey(ctx, candidate)
	return err
}

// RecoverRevokedControllerKeys performs bounded, cursor-based best-effort
// cleanup. It continues through the page after an individual deletion failure
// and returns the cursor with a sanitized error so later candidates cannot be
// permanently starved by one damaged file.
func (service *SessionControlService) RecoverRevokedControllerKeys(ctx context.Context, cursor string, limit int) (RevokedKeyCleanupPage, error) {
	if service == nil || ctx == nil || limit < 1 || limit > service.config.MaxPending {
		return RevokedKeyCleanupPage{}, controlFailure(controlErrorInvalid)
	}
	page, err := service.repository.RevokedRotationKeyCleanupCandidates(ctx, cursor, limit)
	if err != nil {
		return RevokedKeyCleanupPage{}, service.repositoryFailure(err)
	}
	var cleanupErr error
	for _, candidate := range page.Candidates {
		removed, removeErr := service.cleanupRevokedControllerKey(ctx, candidate)
		if removed {
			page.Cleaned++
		}
		if removeErr != nil && cleanupErr == nil {
			cleanupErr = removeErr
		}
	}
	return page, cleanupErr
}

// RecoverControllerKeys is the process-local startup/scheduled seam. Persistent
// runners should use RecoverControllerKeysPage and store its explicit cursor.
func (service *SessionControlService) RecoverControllerKeys(ctx context.Context, limit int) (ControllerKeyRecoveryPage, error) {
	if service == nil {
		return ControllerKeyRecoveryPage{}, controlFailure(controlErrorInvalid)
	}
	service.recoveryMu.Lock()
	defer service.recoveryMu.Unlock()
	page, err := service.RecoverControllerKeysPage(ctx, service.recovery, limit)
	service.recovery = page.NextCursor
	if page.Complete {
		service.recovery = ControllerKeyRecoveryCursor{}
	}
	return page, err
}

// RecoverControllerKeysPage reconciles two bounded durable views: revoked-key
// cleanup intents in SQLite and purpose-bound protected key files. Exact public
// identity agreement preserves every known key; only a definite DB not-found
// makes an inventoried crash orphan delete-eligible.
func (service *SessionControlService) RecoverControllerKeysPage(ctx context.Context, cursor ControllerKeyRecoveryCursor, limit int) (ControllerKeyRecoveryPage, error) {
	if service == nil || ctx == nil || limit < 1 || limit > service.config.MaxPending || !validControllerKeyRecoveryCursor(cursor) {
		return ControllerKeyRecoveryPage{}, controlFailure(controlErrorInvalid)
	}
	result := ControllerKeyRecoveryPage{NextCursor: cursor}
	var leaseErr, cleanupErr, inventoryErr, reconcileErr, temporaryErr error

	if !cursor.LeasesComplete {
		leasing, err := service.recoverExpiredControllerKeyIOLeases(ctx, cursor.LeaseCursor, limit)
		leaseErr = err
		result.Scanned += len(leasing.Leases)
		result.LeaseScanned += len(leasing.Leases)
		result.Cleaned += leasing.Cleaned
		if leasing.Complete {
			result.NextCursor.LeasesComplete = true
			result.NextCursor.LeaseCursor = ""
		} else if leasing.NextCursor != "" {
			result.NextCursor.LeaseCursor = leasing.NextCursor
		}
	}

	if !cursor.RevokedComplete {
		revoked, err := service.RecoverRevokedControllerKeys(ctx, cursor.RevokedCursor, limit)
		cleanupErr = err
		result.Scanned += len(revoked.Candidates)
		result.RevokedScanned += len(revoked.Candidates)
		result.Cleaned += revoked.Cleaned
		if revoked.Complete {
			result.NextCursor.RevokedComplete = true
			result.NextCursor.RevokedCursor = ""
		} else if revoked.NextCursor != "" {
			result.NextCursor.RevokedCursor = revoked.NextCursor
		}
	}

	if !cursor.CredentialsComplete {
		inventory, err := service.credentials.ControllerKeyCredentials(cursor.CredentialCursor, limit)
		if err != nil {
			inventoryErr = controlFailure(controlErrorCredential)
		} else {
			if inventory.Complete {
				result.NextCursor.CredentialsComplete = true
				result.NextCursor.CredentialCursor = ""
			} else if inventory.NextCursor != "" {
				result.NextCursor.CredentialCursor = inventory.NextCursor
			}
			result.Scanned += len(inventory.Credentials) + len(inventory.Issues)
			result.CredentialScanned += len(inventory.Credentials) + len(inventory.Issues)
			result.NeedsAttention = append(result.NeedsAttention, inventory.Issues...)
			for index := range inventory.Credentials {
				metadata := &inventory.Credentials[index]
				key, keyErr := service.repository.Key(ctx, metadata.ControllerID, metadata.KeyID)
				if errors.Is(keyErr, ErrNotFound) {
					lease, newErr := service.newCleanupLease(metadata.ControllerID, ControllerKeyIOKeyCleanup, metadata.KeyID, "")
					if newErr != nil {
						if reconcileErr == nil {
							reconcileErr = newErr
						}
						clear(metadata.PublicKey)
						continue
					}
					if acquireErr := service.repository.AcquireControllerKeyCleanupLease(ctx, lease); acquireErr != nil {
						if !errors.Is(acquireErr, ErrConflict) && reconcileErr == nil {
							reconcileErr = service.repositoryFailure(acquireErr)
						}
						clear(metadata.PublicKey)
						continue
					}
					key, keyErr = service.repository.Key(ctx, metadata.ControllerID, metadata.KeyID)
					if keyErr == nil {
						clear(key.PublicKey)
						if finishErr := service.repository.FinishControllerKeyIOLease(ctx, lease); finishErr != nil && reconcileErr == nil {
							reconcileErr = service.repositoryFailure(finishErr)
						}
						clear(metadata.PublicKey)
						continue
					}
					if !errors.Is(keyErr, ErrNotFound) {
						if reconcileErr == nil {
							reconcileErr = service.repositoryFailure(keyErr)
						}
						clear(metadata.PublicKey)
						continue
					}
					removed, removeErr := service.credentials.RemoveControllerKeyWithResult(metadata.ControllerID, metadata.KeyID)
					if removeErr != nil {
						if reconcileErr == nil {
							reconcileErr = controlFailure(controlErrorCredential)
						}
					} else if finishErr := service.repository.FinishControllerKeyIOLease(ctx, lease); finishErr != nil {
						if reconcileErr == nil {
							reconcileErr = service.repositoryFailure(finishErr)
						}
					} else if removed {
						result.Cleaned++
					}
					clear(metadata.PublicKey)
					continue
				}
				if keyErr != nil {
					if reconcileErr == nil {
						reconcileErr = service.repositoryFailure(keyErr)
					}
					clear(metadata.PublicKey)
					continue
				}
				if key.ControllerID != metadata.ControllerID || key.KeyID != metadata.KeyID || key.ProtectedKeyRef != metadata.ProtectedRef || !bytes.Equal(key.PublicKey, metadata.PublicKey) {
					if reconcileErr == nil {
						reconcileErr = controlFailure(controlErrorState)
					}
				}
				clear(key.PublicKey)
				clear(metadata.PublicKey)
			}
			if len(inventory.Issues) != 0 && reconcileErr == nil {
				reconcileErr = controlFailure(controlErrorCredential)
			}
		}
	}

	if !cursor.TemporaryComplete {
		temporary, err := service.credentials.ControllerKeyTemporaryArtifacts(cursor.TemporaryCursor, limit)
		if err != nil {
			temporaryErr = controlFailure(controlErrorCredential)
		} else {
			if temporary.Complete {
				result.NextCursor.TemporaryComplete = true
				result.NextCursor.TemporaryCursor = ""
			} else if temporary.NextCursor != "" {
				result.NextCursor.TemporaryCursor = temporary.NextCursor
			}
			result.Scanned += len(temporary.Artifacts)
			result.TemporaryScanned += len(temporary.Artifacts)
			for _, artifact := range temporary.Artifacts {
				lease, newErr := service.newCleanupLease(artifact.ControllerID, ControllerKeyIOTempCleanup, "", artifact.Name)
				if newErr != nil {
					if reconcileErr == nil {
						reconcileErr = newErr
					}
					continue
				}
				if acquireErr := service.repository.AcquireControllerKeyCleanupLease(ctx, lease); acquireErr != nil {
					if !errors.Is(acquireErr, ErrConflict) && reconcileErr == nil {
						reconcileErr = service.repositoryFailure(acquireErr)
					}
					continue
				}
				removed, removeErr := service.credentials.RemoveControllerKeyTemporaryArtifact(artifact.ControllerID, artifact.Name)
				if removeErr != nil {
					if reconcileErr == nil {
						reconcileErr = controlFailure(controlErrorCredential)
					}
					continue
				}
				if finishErr := service.repository.FinishControllerKeyIOLease(ctx, lease); finishErr != nil {
					if reconcileErr == nil {
						reconcileErr = service.repositoryFailure(finishErr)
					}
					continue
				}
				if removed {
					result.Cleaned++
				}
			}
		}
	}

	result.Complete = result.NextCursor.LeasesComplete && result.NextCursor.RevokedComplete && result.NextCursor.CredentialsComplete && result.NextCursor.TemporaryComplete
	for _, err := range []error{leaseErr, cleanupErr, inventoryErr, reconcileErr, temporaryErr} {
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func validControllerKeyRecoveryCursor(cursor ControllerKeyRecoveryCursor) bool {
	return !(cursor.LeasesComplete && cursor.LeaseCursor != "") &&
		!(cursor.RevokedComplete && cursor.RevokedCursor != "") &&
		!(cursor.CredentialsComplete && cursor.CredentialCursor != "") &&
		!(cursor.TemporaryComplete && cursor.TemporaryCursor != "")
}

func (service *SessionControlService) replayLiveKeyRotation(ctx context.Context, controllerID string) (*protocol.KeyRotationPropose, bool, error) {
	rotation, key, err := service.repository.LiveKeyRotation(ctx, controllerID)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, service.repositoryFailure(err)
	}
	defer clear(key.PublicKey)
	now := service.now()
	if !rotation.ExpiresAt.After(now) && rotation.State != RotationFinalize {
		if err = service.failExpiredRotation(ctx, rotation, now); err != nil {
			return nil, true, err
		}
		return nil, true, controlFailure(controlErrorExpired)
	}
	command, err := service.repository.ControlCommandForAggregate(ctx, controllerID, "", rotation.RotationID, "propose")
	if errors.Is(err, ErrNotFound) && rotation.State == RotationPrepare {
		frame, candidate, buildErr := service.newRotationProposal(rotation, key)
		if buildErr != nil {
			return nil, true, buildErr
		}
		_, persisted, prepareErr := service.repository.PrepareRotationProposal(ctx, candidate, service.now())
		if prepareErr != nil {
			frame.NewPublicKey = ""
			return nil, true, service.repositoryFailure(prepareErr)
		}
		if persisted.MessageID == candidate.MessageID {
			return frame, true, nil
		}
		frame.NewPublicKey = ""
		replayed, replayErr := service.rotationProposalFrame(rotation, key, persisted)
		return replayed, true, replayErr
	}
	if err != nil {
		return nil, true, service.repositoryFailure(err)
	}
	frame, err := service.rotationProposalFrame(rotation, key, command)
	return frame, true, err
}

// waitForLiveKeyRotationOrWriteLease distinguishes a competing writer from a
// short-lived cleanup owner. It converges to the writer's live rotation or
// retries this exact pre-generated lease after cleanup releases the scope.
func (service *SessionControlService) waitForLiveKeyRotationOrWriteLease(ctx context.Context, lease ControllerKeyIOLease) (*protocol.KeyRotationPropose, error) {
	deadline := time.NewTimer(service.config.KeyWriteContention)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		frame, found, err := service.replayLiveKeyRotation(ctx, lease.ControllerID)
		if found && err == nil {
			return frame, nil
		}
		if err != nil {
			return nil, err
		}
		if err = service.repository.BeginControllerKeyWrite(ctx, lease); err == nil {
			return nil, nil
		} else if !errors.Is(err, ErrConflict) {
			return nil, service.repositoryFailure(err)
		}
		select {
		case <-ctx.Done():
			return nil, controlFailure(controlErrorPersistence)
		case <-deadline.C:
			return nil, controlFailure(controlErrorState)
		case <-ticker.C:
		}
	}
}

// cleanupRevokedControllerKey serializes every known-key deletion against
// writers and other cleanup workers. A conflicting owner defers this durable
// candidate to recovery; it never authorizes an unleased delete.
func (service *SessionControlService) cleanupRevokedControllerKey(ctx context.Context, candidate RevokedKeyCleanupCandidate) (bool, error) {
	lease, err := service.newCleanupLease(candidate.ControllerID, ControllerKeyIORevokedCleanup, candidate.KeyID, "")
	if err != nil {
		return false, err
	}
	if err = service.repository.AcquireControllerKeyCleanupLease(ctx, lease); err != nil {
		if errors.Is(err, ErrConflict) {
			return false, nil
		}
		return false, service.repositoryFailure(err)
	}
	return service.reconcileOwnedRevokedControllerKey(ctx, lease, candidate)
}

func (service *SessionControlService) reconcileOwnedRevokedControllerKey(ctx context.Context, lease ControllerKeyIOLease, candidate RevokedKeyCleanupCandidate) (bool, error) {
	if lease.Operation != ControllerKeyIORevokedCleanup || lease.ControllerID != candidate.ControllerID || lease.KeyID != candidate.KeyID || lease.ProtectedKeyRef != candidate.ProtectedKeyRef {
		return false, controlFailure(controlErrorState)
	}
	persisted, err := service.repository.RevokedRotationKeyCleanupCandidate(ctx, candidate)
	if errors.Is(err, ErrNotFound) {
		if finishErr := service.repository.FinishControllerKeyIOLease(ctx, lease); finishErr != nil {
			return false, service.repositoryFailure(finishErr)
		}
		return false, nil
	}
	if err != nil {
		return false, service.repositoryFailure(err)
	}
	if persisted != candidate {
		return false, controlFailure(controlErrorState)
	}
	removed, err := service.credentials.RemoveControllerKeyWithResult(candidate.ControllerID, candidate.KeyID)
	if err != nil {
		// No deletion was confirmed, so release this exact attempt without
		// setting the cleared marker. The durable revoked row remains eligible
		// next sweep and later keys on the controller are not starved.
		if finishErr := service.repository.FinishControllerKeyIOLease(ctx, lease); finishErr != nil {
			return false, service.repositoryFailure(finishErr)
		}
		return false, controlFailure(controlErrorCredential)
	}
	if err = service.repository.MarkRevokedControllerKeyCleared(ctx, candidate, service.now()); err != nil {
		return removed, service.repositoryFailure(err)
	}
	if err = service.repository.FinishControllerKeyIOLease(ctx, lease); err != nil {
		return removed, service.repositoryFailure(err)
	}
	return removed, nil
}

func (service *SessionControlService) newCleanupLease(controllerID, operation, keyID, artifactName string) (ControllerKeyIOLease, error) {
	leaseID, err := service.newID()
	if err != nil || !canonicalUUID(leaseID) {
		return ControllerKeyIOLease{}, controlFailure(controlErrorCredential)
	}
	now := service.now()
	lease := ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(controllerID), ControllerID: controllerID, LeaseID: leaseID, Operation: operation, Phase: ControllerKeyIORecovery, Fence: 1,
		LeaseExpiresAt: now.Add(service.config.KeyRecoveryLease), KeyID: keyID, ArtifactName: artifactName, CreatedAt: now, UpdatedAt: now,
	}
	if operation == ControllerKeyIOKeyCleanup || operation == ControllerKeyIORevokedCleanup {
		lease.ProtectedKeyRef = ProtectedKeyRef(controllerID, keyID)
	}
	if !validControllerKeyIOLease(lease) {
		return ControllerKeyIOLease{}, controlFailure(controlErrorInvalid)
	}
	return lease, nil
}

func (service *SessionControlService) recoverExpiredControllerKeyIOLeases(ctx context.Context, cursor string, limit int) (ControllerKeyIOLeasePage, error) {
	page, err := service.repository.ExpiredControllerKeyIOLeases(ctx, cursor, service.now(), limit)
	if err != nil {
		return ControllerKeyIOLeasePage{}, service.repositoryFailure(err)
	}
	var recoveryErr error
	for index := range page.Leases {
		candidate := &page.Leases[index]
		recoveryID, idErr := service.newID()
		if idErr != nil || !canonicalUUID(recoveryID) {
			if recoveryErr == nil {
				recoveryErr = controlFailure(controlErrorCredential)
			}
			clear(candidate.PublicKey)
			continue
		}
		now := service.now()
		claimed, claimErr := service.repository.ClaimExpiredControllerKeyIOLease(ctx, *candidate, recoveryID, now, now.Add(service.config.KeyRecoveryLease))
		clear(candidate.PublicKey)
		if claimErr != nil {
			if !errors.Is(claimErr, ErrConflict) && recoveryErr == nil {
				recoveryErr = service.repositoryFailure(claimErr)
			}
			continue
		}
		removed, reconcileErr := service.reconcileClaimedControllerKeyIOLease(ctx, claimed)
		clear(claimed.PublicKey)
		if reconcileErr != nil {
			if recoveryErr == nil {
				recoveryErr = reconcileErr
			}
			continue
		}
		if removed {
			page.Cleaned++
		}
	}
	return page, recoveryErr
}

func (service *SessionControlService) reconcileClaimedControllerKeyIOLease(ctx context.Context, lease ControllerKeyIOLease) (bool, error) {
	if lease.Operation == ControllerKeyIOTempCleanup {
		removed, err := service.credentials.RemoveControllerKeyTemporaryArtifact(lease.ControllerID, lease.ArtifactName)
		if err != nil {
			return false, controlFailure(controlErrorCredential)
		}
		if err = service.repository.FinishControllerKeyIOLease(ctx, lease); err != nil {
			return false, service.repositoryFailure(err)
		}
		return removed, nil
	}
	if lease.Operation == ControllerKeyIORevokedCleanup {
		candidate := RevokedKeyCleanupCandidate{ControllerID: lease.ControllerID, KeyID: lease.KeyID, ProtectedKeyRef: lease.ProtectedKeyRef}
		return service.reconcileOwnedRevokedControllerKey(ctx, lease, candidate)
	}
	key, err := service.repository.Key(ctx, lease.ControllerID, lease.KeyID)
	if err == nil {
		defer clear(key.PublicKey)
		if (lease.Operation == ControllerKeyIOWrite || lease.Operation == ControllerKeyIOIdentityWrite) && (key.ProtectedKeyRef != lease.ProtectedKeyRef || !bytes.Equal(key.PublicKey, lease.PublicKey)) {
			return false, controlFailure(controlErrorState)
		}
		if err = service.repository.FinishControllerKeyIOLease(ctx, lease); err != nil {
			return false, service.repositoryFailure(err)
		}
		return false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return false, service.repositoryFailure(err)
	}
	removed, err := service.credentials.RemoveControllerKeyWithResult(lease.ControllerID, lease.KeyID)
	if err != nil {
		return false, controlFailure(controlErrorCredential)
	}
	if err = service.repository.FinishControllerKeyIOLease(ctx, lease); err != nil {
		return false, service.repositoryFailure(err)
	}
	return removed, nil
}

func (service *SessionControlService) handleRotationChallenge(ctx context.Context, session SessionControlContext, challenge *protocol.KeyRotationChallenge) (_ *protocol.KeyRotationConfirm, resultErr error) {
	defer func() { challenge.ServerNonce = "" }()
	propose, err := service.repository.LoadControlCommand(ctx, session.ControllerID, challenge.TargetMessageID)
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	if propose.CommandType != CommandRotationPropose || propose.Stage != "propose" || propose.RotationID != challenge.RotationID || propose.State != CommandPrepared {
		return nil, controlFailure(controlErrorState)
	}
	rotation, err := service.repository.Rotation(ctx, session.ControllerID, challenge.RotationID)
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	now := service.now()
	if rotation.OldKeyID != session.KeyID || (rotation.State != RotationPropose && rotation.State != RotationConfirm) || !challenge.ExpiresAt.After(now) || challenge.ExpiresAt.After(rotation.ExpiresAt) || challenge.ExpiresAt.Sub(now) > service.config.MaxChallengeLifetime {
		if !rotation.ExpiresAt.After(now) && rotation.State != RotationFinalize {
			if failErr := service.failExpiredRotationFenced(ctx, session, rotation, now); failErr != nil {
				return nil, failErr
			}
		}
		return nil, controlFailure(controlErrorExpired)
	}
	key, err := service.repository.Key(ctx, session.ControllerID, rotation.NewKeyID)
	if err != nil || key.State != KeyPending {
		return nil, service.repositoryFailure(err)
	}
	defer clear(key.PublicKey)
	bundle, err := service.credentials.ReadControllerKey(session.ControllerID, key.KeyID, key.PublicKey)
	if err != nil {
		return nil, controlFailure(controlErrorCredential)
	}
	defer bundle.Destroy()
	publicKey := base64.RawURLEncoding.EncodeToString(key.PublicKey)
	transcript, err := protocol.KeyRotationTranscript(protocol.RotationProof{RotationID: rotation.RotationID, ControllerID: rotation.ControllerID, OldKeyID: rotation.OldKeyID, NewKeyID: rotation.NewKeyID, NewPublicKey: publicKey, SessionID: session.SessionID, ServerNonce: challenge.ServerNonce, ExpiresAt: challenge.ExpiresAt})
	publicKey = ""
	if err != nil {
		return nil, controlFailure(controlErrorInvalid)
	}
	signature, err := protocol.Sign(bundle.PrivateKey, transcript)
	bundle.Destroy()
	clear(transcript)
	if err != nil {
		return nil, controlFailure(controlErrorCredential)
	}
	envelope, err := service.envelope(protocol.TypeKeyRotationConfirm)
	if err != nil {
		signature = ""
		return nil, err
	}
	candidate := &protocol.KeyRotationConfirm{Envelope: envelope, RotationID: rotation.RotationID, Signature: signature}
	command, err := outboundCommand(session.ControllerID, "", rotation.RotationID, "confirm", candidate)
	if err != nil {
		candidate.Signature = ""
		signature = ""
		return nil, err
	}
	var persisted OutboundCommand
	if repository, ok := service.repository.(fencedSessionControlRepository); ok {
		if session.Epoch == 0 || session.Fence == 0 {
			return nil, controlFailure(controlErrorState)
		}
		_, persisted, err = repository.PrepareRotationConfirmationFenced(ctx, challenge.TargetMessageID, session.Epoch, session.Fence, command, now)
	} else {
		_, persisted, err = service.repository.PrepareRotationConfirmation(ctx, challenge.TargetMessageID, command, now)
	}
	if err != nil {
		candidate.Signature = ""
		signature = ""
		return nil, service.repositoryFailure(err)
	}
	if persisted.MessageID != command.MessageID {
		candidate.Signature = ""
		candidate = &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, persisted.MessageID, persisted.SentAt), RotationID: rotation.RotationID, Signature: signature}
		if !controlFrameMatches(candidate, persisted) {
			candidate.Signature = ""
			signature = ""
			return nil, controlFailure(controlErrorState)
		}
	}
	signature = ""
	return candidate, nil
}

func (service *SessionControlService) failExpiredRotation(ctx context.Context, rotation KeyRotation, at time.Time) error {
	if err := service.repository.FailExpiredRotation(ctx, rotation.ControllerID, rotation.RotationID, at); err != nil {
		return service.repositoryFailure(err)
	}
	candidate := RevokedKeyCleanupCandidate{ControllerID: rotation.ControllerID, KeyID: rotation.NewKeyID, ProtectedKeyRef: ProtectedKeyRef(rotation.ControllerID, rotation.NewKeyID)}
	_, err := service.cleanupRevokedControllerKey(ctx, candidate)
	return err
}

func (service *SessionControlService) failExpiredRotationFenced(ctx context.Context, session SessionControlContext, rotation KeyRotation, at time.Time) error {
	if repository, ok := service.repository.(fencedSessionControlRepository); ok {
		if session.Epoch == 0 || session.Fence == 0 {
			return controlFailure(controlErrorState)
		}
		if err := repository.FailExpiredRotationFenced(ctx, rotation.ControllerID, rotation.RotationID, session.Epoch, session.Fence, at); err != nil {
			return service.repositoryFailure(err)
		}
	} else if err := service.repository.FailExpiredRotation(ctx, rotation.ControllerID, rotation.RotationID, at); err != nil {
		return service.repositoryFailure(err)
	}
	candidate := RevokedKeyCleanupCandidate{ControllerID: rotation.ControllerID, KeyID: rotation.NewKeyID, ProtectedKeyRef: ProtectedKeyRef(rotation.ControllerID, rotation.NewKeyID)}
	_, err := service.cleanupRevokedControllerKey(ctx, candidate)
	return err
}

func (service *SessionControlService) handleRotationConfirmed(ctx context.Context, session SessionControlContext, confirmed *protocol.KeyRotationConfirmed) (*protocol.KeyRotationFinalize, error) {
	confirm, err := service.repository.LoadControlCommand(ctx, session.ControllerID, confirmed.TargetMessageID)
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	if confirm.CommandType != CommandRotationConfirm || confirm.RotationID != confirmed.RotationID || confirm.Stage != "confirm" {
		return nil, controlFailure(controlErrorState)
	}
	envelope, err := service.envelope(protocol.TypeKeyRotationFinalize)
	if err != nil {
		return nil, err
	}
	candidate := &protocol.KeyRotationFinalize{Envelope: envelope, RotationID: confirmed.RotationID, RetireOldKey: true}
	command, err := outboundCommand(session.ControllerID, "", confirmed.RotationID, "finalize", candidate)
	if err != nil {
		return nil, err
	}
	var persisted OutboundCommand
	if repository, ok := service.repository.(fencedSessionControlRepository); ok {
		if session.Epoch == 0 || session.Fence == 0 {
			return nil, controlFailure(controlErrorState)
		}
		_, persisted, err = repository.ConfirmRotationAndPrepareFinalizeFenced(ctx, confirmed.TargetMessageID, confirmed.RotationID, session.KeyID, session.Epoch, session.Fence, command, service.now())
	} else {
		_, persisted, err = service.repository.ConfirmRotationAndPrepareFinalize(ctx, confirmed.TargetMessageID, confirmed.RotationID, session.KeyID, command, service.now())
	}
	if err != nil {
		return nil, service.repositoryFailure(err)
	}
	if persisted.MessageID != command.MessageID {
		candidate = &protocol.KeyRotationFinalize{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalize, persisted.MessageID, persisted.SentAt), RotationID: confirmed.RotationID, RetireOldKey: true}
		if !controlFrameMatches(candidate, persisted) {
			return nil, controlFailure(controlErrorState)
		}
	}
	return candidate, nil
}

func (service *SessionControlService) frameForCommand(ctx context.Context, session SessionControlContext, command OutboundCommand) (protocol.Frame, error) {
	switch command.CommandType {
	case CommandBindingRemove:
		binding, err := service.repository.BindingForController(ctx, session.ControllerID, command.BindingID)
		if err != nil || binding.State != BindingRemovalPending {
			return nil, service.repositoryFailure(err)
		}
		return service.bindingRemovalFrame(binding, command)
	case CommandRotationPropose:
		rotation, err := service.repository.Rotation(ctx, session.ControllerID, command.RotationID)
		if err != nil || rotation.OldKeyID != session.KeyID {
			return nil, service.repositoryFailure(err)
		}
		key, err := service.repository.Key(ctx, session.ControllerID, rotation.NewKeyID)
		if err != nil {
			return nil, service.repositoryFailure(err)
		}
		defer clear(key.PublicKey)
		return service.rotationProposalFrame(rotation, key, command)
	case CommandRotationFinalize:
		rotation, err := service.repository.Rotation(ctx, session.ControllerID, command.RotationID)
		if err != nil || rotation.State != RotationFinalize || (rotation.OldKeyID != session.KeyID && rotation.NewKeyID != session.KeyID) {
			return nil, service.repositoryFailure(err)
		}
		frame := &protocol.KeyRotationFinalize{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalize, command.MessageID, command.SentAt), RotationID: rotation.RotationID, RetireOldKey: true}
		if !controlFrameMatches(frame, command) {
			return nil, controlFailure(controlErrorState)
		}
		return frame, nil
	default:
		return nil, controlFailure(controlErrorState)
	}
}

func (service *SessionControlService) newBindingRemoval(binding InstallationBinding) (*protocol.BindingRemove, OutboundCommand, error) {
	envelope, err := service.envelope(protocol.TypeBindingRemove)
	if err != nil {
		return nil, OutboundCommand{}, err
	}
	frame := &protocol.BindingRemove{Envelope: envelope, InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID}
	command, err := outboundCommand(binding.ControllerID, binding.BindingID, "", "remove", frame)
	return frame, command, err
}

func (service *SessionControlService) bindingRemovalFrame(binding InstallationBinding, command OutboundCommand) (*protocol.BindingRemove, error) {
	frame := &protocol.BindingRemove{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemove, command.MessageID, command.SentAt), InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID}
	if !controlFrameMatches(frame, command) {
		return nil, controlFailure(controlErrorState)
	}
	return frame, nil
}

func (service *SessionControlService) newRotationProposal(rotation KeyRotation, key ControllerKey) (*protocol.KeyRotationPropose, OutboundCommand, error) {
	envelope, err := service.envelope(protocol.TypeKeyRotationPropose)
	if err != nil {
		return nil, OutboundCommand{}, err
	}
	frame := &protocol.KeyRotationPropose{Envelope: envelope, RotationID: rotation.RotationID, ControllerID: rotation.ControllerID, OldKeyID: rotation.OldKeyID, NewKeyID: rotation.NewKeyID, NewPublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey)}
	command, err := outboundCommand(rotation.ControllerID, "", rotation.RotationID, "propose", frame)
	return frame, command, err
}

func (service *SessionControlService) rotationProposalFrame(rotation KeyRotation, key ControllerKey, command OutboundCommand) (*protocol.KeyRotationPropose, error) {
	frame := &protocol.KeyRotationPropose{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationPropose, command.MessageID, command.SentAt), RotationID: rotation.RotationID, ControllerID: rotation.ControllerID, OldKeyID: rotation.OldKeyID, NewKeyID: rotation.NewKeyID, NewPublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey)}
	if !controlFrameMatches(frame, command) {
		frame.NewPublicKey = ""
		return nil, controlFailure(controlErrorState)
	}
	return frame, nil
}

func (service *SessionControlService) envelope(messageType protocol.MessageType) (protocol.Envelope, error) {
	messageID, err := service.newID()
	if err != nil || !canonicalUUID(messageID) {
		return protocol.Envelope{}, controlFailure(controlErrorCredential)
	}
	return protocol.NewEnvelope(messageType, messageID, service.now()), nil
}

func (service *SessionControlService) newID() (string, error) {
	service.entropyMu.Lock()
	defer service.entropyMu.Unlock()
	return service.config.NewID(service.config.Entropy)
}

func outboundCommand(controllerID, bindingID, rotationID, stage string, frame protocol.Frame) (OutboundCommand, error) {
	digest, err := controlFrameDigest(frame)
	if err != nil {
		return OutboundCommand{}, controlFailure(controlErrorInvalid)
	}
	base := frameBase(frame)
	if base == nil {
		return OutboundCommand{}, controlFailure(controlErrorInvalid)
	}
	commandType := string(base.Type)
	return OutboundCommand{ControllerID: controllerID, MessageID: base.MessageID, CommandType: commandType, BindingID: bindingID, RotationID: rotationID, Stage: stage, SentAt: base.SentAt, Digest: digest, State: CommandPrepared}, nil
}

func controlFrameDigest(frame protocol.Frame) ([sha256.Size]byte, error) {
	encoded, err := protocol.Encode(frame, protocol.DefaultMaxEnvelopeBytes)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer clear(encoded)
	return sha256.Sum256(encoded), nil
}

func controlFrameMatches(frame protocol.Frame, command OutboundCommand) bool {
	digest, err := controlFrameDigest(frame)
	return err == nil && digest == command.Digest
}

func frameBase(frame protocol.Frame) *protocol.Envelope {
	switch value := frame.(type) {
	case *protocol.BindingRemove:
		return &value.Envelope
	case *protocol.KeyRotationPropose:
		return &value.Envelope
	case *protocol.KeyRotationConfirm:
		return &value.Envelope
	case *protocol.KeyRotationFinalize:
		return &value.Envelope
	default:
		return nil
	}
}

func destroySessionControlFrames(frames []protocol.Frame) {
	for _, frame := range frames {
		destroySessionControlFrame(frame)
	}
}

func destroySessionControlFrame(frame protocol.Frame) {
	switch value := frame.(type) {
	case *protocol.KeyRotationPropose:
		value.NewPublicKey = ""
	case *protocol.KeyRotationConfirm:
		value.Signature = ""
	}
}

func (service *SessionControlService) repositoryFailure(err error) error {
	if err == nil {
		return controlFailure(controlErrorState)
	}
	switch {
	case errors.Is(err, context.Canceled):
		// Retain only the standard cancellation sentinel, never the repository
		// error. The transport uses it to distinguish its own session cancel
		// from a genuine persistence failure without exposing provider detail.
		return canceledControlFailure(controlErrorPersistence)
	case errors.Is(err, ErrInvalid):
		return controlFailure(controlErrorInvalid)
	case errors.Is(err, ErrState), errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound):
		return controlFailure(controlErrorState)
	default:
		return controlFailure(controlErrorPersistence)
	}
}

func (service *SessionControlService) now() time.Time { return service.config.Now().UTC() }

func validSessionControlConfig(config SessionControlConfig) bool {
	return config.RotationLifetime >= time.Minute && config.RotationLifetime <= 24*time.Hour &&
		config.MaxChallengeLifetime >= time.Second && config.MaxChallengeLifetime <= 5*time.Minute &&
		config.KeyWriteLease >= time.Minute && config.KeyWriteLease <= 30*time.Minute &&
		config.KeyRecoveryLease >= time.Minute && config.KeyRecoveryLease <= 30*time.Minute &&
		config.KeyWriteContention >= 10*time.Millisecond && config.KeyWriteContention <= 10*time.Second &&
		config.MaxPending >= 1 && config.MaxPending <= protocol.MaxArrayItems && config.Now != nil && config.Entropy != nil && config.GenerateKey != nil && config.NewID != nil
}

func validSessionControlContext(value SessionControlContext) bool {
	return canonicalUUID(value.ControllerID) && canonicalUUID(value.KeyID) && canonicalUUID(value.SessionID) && !value.ExpiresAt.IsZero()
}
