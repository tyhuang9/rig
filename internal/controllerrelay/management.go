package controllerrelay

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	ManagementInitializing = "initializing"
	ManagementUnavailable  = "unavailable"
	ManagementAvailable    = "available"

	ManagementErrorUnavailable       = "management_unavailable"
	ManagementErrorInvalidRequest    = "invalid_request"
	ManagementErrorEnrollmentMissing = "enrollment_not_found"
	ManagementErrorBindingMissing    = "binding_not_found"
	ManagementErrorBindingState      = "binding_state_conflict"
	ManagementErrorIdentity          = "identity_unavailable"
	ManagementErrorRotationConflict  = "rotation_conflict"
)

const maxManagementRetryAfter = 5 * time.Minute

// ManagementStatus is the controller-safe relay lifecycle view. It
// deliberately omits controller, key, session, endpoint, and credential data.
type ManagementStatus struct {
	Availability            string `json:"availability"`
	State                   string `json:"state,omitempty"`
	Paused                  bool   `json:"paused"`
	Outcome                 string `json:"outcome,omitempty"`
	DiagnosticsUnavailable  bool   `json:"diagnosticsUnavailable"`
	PendingCommands         int    `json:"pendingCommands"`
	ActiveLeases            int    `json:"activeLeases"`
	ExpiredLeases           int    `json:"expiredLeases"`
	OldestPendingAgeSeconds int64  `json:"oldestPendingAgeSeconds"`
	ObserverDropped         uint64 `json:"observerDropped"`
}

type ManagementBindingSummary struct {
	BindingID      string    `json:"bindingId"`
	ConnectionID   string    `json:"connectionId"`
	InstallationID int64     `json:"installationId"`
	RepositoryID   int64     `json:"repositoryId"`
	State          string    `json:"state"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ManagementKeyRotationSummary struct {
	InProgress bool      `json:"inProgress"`
	State      string    `json:"state,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
}

type ManagementReadModel struct {
	RemovableBindings []ManagementBindingSummary   `json:"removableBindings"`
	KeyRotation       ManagementKeyRotationSummary `json:"keyRotation"`
}

type ManagementEnrollmentInput struct {
	ConnectionID   string `json:"connectionId"`
	InstallationID int64  `json:"installationId"`
	RepositoryID   int64  `json:"repositoryId"`
}

type ManagementEnrollmentStart struct {
	EnrollmentID     string    `json:"enrollmentId"`
	AuthorizationURL string    `json:"authorizationUrl"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type ManagementEnrollmentStatus struct {
	EnrollmentID string     `json:"enrollmentId"`
	BindingID    string     `json:"bindingId,omitempty"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	ExpiresAt    time.Time  `json:"expiresAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

type ManagementBindingStatus struct {
	BindingID string    `json:"bindingId"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ManagementKeyRotationStatus struct {
	RotationID string    `json:"rotationId"`
	State      string    `json:"state"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ManagementError retains only an allow-listed code and bounded retry hint.
// It never wraps lower-layer errors because they may contain provider detail.
type ManagementError struct {
	Code       string        `json:"code"`
	RetryAfter time.Duration `json:"retryAfter,omitempty"`
}

func (err *ManagementError) Error() string {
	if err == nil {
		return "controller relay management failed"
	}
	return "controller relay management failed: " + safeManagementErrorCode(err.Code)
}
func (err *ManagementError) String() string   { return err.Error() }
func (err *ManagementError) GoString() string { return err.Error() }
func (err *ManagementError) LogValue() slog.Value {
	if err == nil {
		return slog.GroupValue(slog.String("code", ManagementErrorUnavailable))
	}
	return slog.GroupValue(slog.String("code", safeManagementErrorCode(err.Code)), slog.Duration("retry_after", boundedManagementRetryAfter(err.RetryAfter)))
}

func IsManagementCode(err error, code string) bool {
	var managementErr *ManagementError
	return errors.As(err, &managementErr) && managementErr.Code == safeManagementErrorCode(code)
}

type managementRepository interface {
	ActiveIdentity(context.Context) (ControllerIdentity, ControllerKey, error)
	Binding(context.Context, string, string) (InstallationBinding, error)
	Enrollment(context.Context, string, string) (Enrollment, error)
	ReadModel(context.Context, string) (RelayReadModel, error)
	Rotation(context.Context, string, string) (KeyRotation, error)
}

type managementEnrollmentService interface {
	Start(context.Context, StartEnrollmentInput) (EnrollmentStartResult, error)
	Poll(context.Context, string, string) (EnrollmentPollResult, error)
}

type managementControlService interface {
	RequestBindingRemoval(context.Context, string, string) (*protocol.BindingRemove, error)
	StartKeyRotation(context.Context, string) (*protocol.KeyRotationPropose, error)
}

type managementSupervisor interface {
	Snapshot() SupervisorSnapshot
	Reconcile()
}

// ManagementService composes the already-constructed relay graph. The host
// creates exactly one instance with the same repository, enrollment service,
// controls, and supervisor used by the relay lifecycle.
type ManagementService struct {
	repository managementRepository
	enrollment managementEnrollmentService
	controls   managementControlService
	supervisor managementSupervisor
}

func NewManagementService(repository *Repository, enrollment *EnrollmentService, controls *SessionControlService, supervisor *Supervisor) (*ManagementService, error) {
	if repository == nil || enrollment == nil || controls == nil || supervisor == nil {
		return nil, errors.New("controller relay management dependencies are required")
	}
	return newManagementService(repository, enrollment, controls, supervisor)
}

func newManagementService(repository managementRepository, enrollment managementEnrollmentService, controls managementControlService, supervisor managementSupervisor) (*ManagementService, error) {
	if repository == nil || enrollment == nil || controls == nil || supervisor == nil {
		return nil, errors.New("controller relay management dependencies are required")
	}
	return &ManagementService{repository: repository, enrollment: enrollment, controls: controls, supervisor: supervisor}, nil
}

func (service *ManagementService) Status() ManagementStatus {
	if service == nil || service.supervisor == nil {
		return ManagementStatus{Availability: ManagementUnavailable}
	}
	snapshot := service.supervisor.Snapshot()
	outcome := snapshot.Outcome
	if outcome != "" {
		outcome = safeSessionErrorCode(outcome)
	}
	return ManagementStatus{
		Availability:            ManagementAvailable,
		State:                   safeSupervisorStage(snapshot.State),
		Paused:                  snapshot.Paused,
		Outcome:                 outcome,
		DiagnosticsUnavailable:  snapshot.DiagnosticsUnavailable,
		PendingCommands:         nonnegativeManagementCount(snapshot.Diagnostics.PendingCommands),
		ActiveLeases:            nonnegativeManagementCount(snapshot.Diagnostics.ActiveLeases),
		ExpiredLeases:           nonnegativeManagementCount(snapshot.Diagnostics.ExpiredLeases),
		OldestPendingAgeSeconds: managementAgeSeconds(snapshot.Diagnostics.OldestPendingAge),
		ObserverDropped:         snapshot.ObserverDropped,
	}
}

func (service *ManagementService) ReadModel(ctx context.Context, owner string) (ManagementReadModel, error) {
	result := ManagementReadModel{RemovableBindings: make([]ManagementBindingSummary, 0)}
	if service == nil || service.repository == nil {
		return result, managementFailure(ManagementErrorUnavailable, 0)
	}
	if ctx == nil || !validOpaqueID(owner) {
		return result, managementFailure(ManagementErrorInvalidRequest, 0)
	}
	stored, err := service.repository.ReadModel(ctx, owner)
	if err != nil {
		return result, managementFailure("persistence_unavailable", 0)
	}
	if stored.RemovableBindings == nil || len(stored.RemovableBindings) > maxRelayReadModelBindings {
		return result, managementFailure("persistence_unavailable", 0)
	}
	for _, binding := range stored.RemovableBindings {
		if !validCanonicalUUID(binding.BindingID) || !validLowerHexID(binding.ConnectionID, 32) || binding.InstallationID < 1 || binding.RepositoryID < 1 || binding.UpdatedAt.IsZero() || !removableBindingState(binding.State) {
			return ManagementReadModel{RemovableBindings: make([]ManagementBindingSummary, 0)}, managementFailure("persistence_unavailable", 0)
		}
		result.RemovableBindings = append(result.RemovableBindings, ManagementBindingSummary{
			BindingID: binding.BindingID, ConnectionID: binding.ConnectionID,
			InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID,
			State: binding.State, UpdatedAt: binding.UpdatedAt,
		})
	}
	rotation := stored.KeyRotation
	if !rotation.InProgress {
		if rotation.State != "" || !rotation.ExpiresAt.IsZero() || !rotation.UpdatedAt.IsZero() {
			return ManagementReadModel{RemovableBindings: make([]ManagementBindingSummary, 0)}, managementFailure("persistence_unavailable", 0)
		}
		return result, nil
	}
	if !liveRotationState(rotation.State) || rotation.ExpiresAt.IsZero() || rotation.UpdatedAt.IsZero() {
		return ManagementReadModel{RemovableBindings: make([]ManagementBindingSummary, 0)}, managementFailure("persistence_unavailable", 0)
	}
	result.KeyRotation = ManagementKeyRotationSummary{
		InProgress: true, State: rotation.State, ExpiresAt: rotation.ExpiresAt, UpdatedAt: rotation.UpdatedAt,
	}
	return result, nil
}

func removableBindingState(value string) bool {
	return value == BindingAuthorized || value == BindingAccessLost || value == BindingRemovalPending
}

func liveRotationState(value string) bool {
	switch value {
	case RotationPrepare, RotationPropose, RotationConfirm, RotationNewKeyAuth, RotationFinalize:
		return true
	default:
		return false
	}
}

func validLowerHexID(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (service *ManagementService) StartEnrollment(ctx context.Context, owner string, input ManagementEnrollmentInput) (ManagementEnrollmentStart, error) {
	if service == nil || service.enrollment == nil {
		return ManagementEnrollmentStart{}, managementFailure(ManagementErrorUnavailable, 0)
	}
	if ctx == nil || !validOpaqueID(owner) || !validOpaqueID(input.ConnectionID) || input.InstallationID <= 0 || input.RepositoryID <= 0 {
		return ManagementEnrollmentStart{}, managementFailure(ManagementErrorInvalidRequest, 0)
	}
	result, err := service.enrollment.Start(ctx, StartEnrollmentInput{
		OwnerUserID: owner, ConnectionID: input.ConnectionID,
		InstallationID: input.InstallationID, RepositoryID: input.RepositoryID,
	})
	if err != nil {
		return ManagementEnrollmentStart{}, managementEnrollmentFailure(err)
	}
	return ManagementEnrollmentStart{
		EnrollmentID: result.EnrollmentID, AuthorizationURL: result.AuthorizationURL,
		State: result.Status, ExpiresAt: result.ExpiresAt,
	}, nil
}

func (service *ManagementService) PollEnrollment(ctx context.Context, owner, enrollmentID string) (ManagementEnrollmentStatus, error) {
	if service == nil || service.enrollment == nil {
		return ManagementEnrollmentStatus{}, managementFailure(ManagementErrorUnavailable, 0)
	}
	if ctx == nil || !validOpaqueID(owner) || !validCanonicalUUID(enrollmentID) {
		return ManagementEnrollmentStatus{}, managementFailure(ManagementErrorInvalidRequest, 0)
	}
	result, err := service.enrollment.Poll(ctx, owner, enrollmentID)
	if err != nil {
		return ManagementEnrollmentStatus{}, managementEnrollmentFailure(err)
	}
	if result.Status != EnrollmentPending {
		service.supervisor.Reconcile()
	}
	enrollment, err := service.repository.Enrollment(ctx, owner, enrollmentID)
	if err != nil {
		return ManagementEnrollmentStatus{}, managementFailure("persistence_unavailable", 0)
	}
	completedAt := enrollment.CompletedAt
	if completedAt != nil {
		completed := *completedAt
		completedAt = &completed
	}
	return ManagementEnrollmentStatus{
		EnrollmentID: enrollment.EnrollmentID, BindingID: enrollment.BindingID,
		State: enrollment.State, CreatedAt: enrollment.CreatedAt, UpdatedAt: enrollment.UpdatedAt,
		ExpiresAt: enrollment.ExpiresAt, CompletedAt: completedAt,
	}, nil
}

func (service *ManagementService) RemoveBinding(ctx context.Context, owner, bindingID string) (ManagementBindingStatus, error) {
	if service == nil || service.repository == nil || service.controls == nil || service.supervisor == nil {
		return ManagementBindingStatus{}, managementFailure(ManagementErrorUnavailable, 0)
	}
	if ctx == nil || !validOpaqueID(owner) || !validCanonicalUUID(bindingID) {
		return ManagementBindingStatus{}, managementFailure(ManagementErrorInvalidRequest, 0)
	}
	binding, err := service.repository.Binding(ctx, owner, bindingID)
	if errors.Is(err, ErrNotFound) {
		return ManagementBindingStatus{}, managementFailure(ManagementErrorBindingMissing, 0)
	}
	if err != nil {
		return ManagementBindingStatus{}, managementFailure("persistence_unavailable", 0)
	}
	state := binding.State
	updatedAt := binding.UpdatedAt
	binding = InstallationBinding{}
	switch state {
	case BindingAuthorized, BindingAccessLost, BindingRemovalPending:
	case BindingRemoved:
		return ManagementBindingStatus{BindingID: bindingID, State: BindingRemoved, UpdatedAt: updatedAt}, nil
	default:
		return ManagementBindingStatus{}, managementFailure(ManagementErrorBindingState, 0)
	}

	frame, err := service.controls.RequestBindingRemoval(ctx, owner, bindingID)
	destroyManagementBindingRemove(frame)
	if err != nil {
		return ManagementBindingStatus{}, managementControlFailure(err, ManagementErrorBindingState)
	}
	service.supervisor.Reconcile()

	binding, err = service.repository.Binding(ctx, owner, bindingID)
	if err != nil {
		return ManagementBindingStatus{}, managementFailure("persistence_unavailable", 0)
	}
	state = binding.State
	updatedAt = binding.UpdatedAt
	binding = InstallationBinding{}
	if state != BindingRemovalPending && state != BindingRemoved {
		return ManagementBindingStatus{}, managementFailure(ManagementErrorBindingState, 0)
	}
	return ManagementBindingStatus{BindingID: bindingID, State: state, UpdatedAt: updatedAt}, nil
}

func (service *ManagementService) RotateKey(ctx context.Context) (ManagementKeyRotationStatus, error) {
	if service == nil || service.repository == nil || service.controls == nil || service.supervisor == nil {
		return ManagementKeyRotationStatus{}, managementFailure(ManagementErrorUnavailable, 0)
	}
	if ctx == nil {
		return ManagementKeyRotationStatus{}, managementFailure(ManagementErrorInvalidRequest, 0)
	}
	identity, key, err := service.repository.ActiveIdentity(ctx)
	controllerID := identity.ControllerID
	activeIdentity := identity.State == ControllerActive && key.State == KeyActive && key.ControllerID == controllerID && validCanonicalUUID(key.KeyID)
	clear(key.PublicKey)
	identity = ControllerIdentity{}
	key = ControllerKey{}
	if errors.Is(err, ErrNotFound) {
		return ManagementKeyRotationStatus{}, managementFailure(ManagementErrorIdentity, 0)
	}
	if err != nil {
		return ManagementKeyRotationStatus{}, managementFailure("persistence_unavailable", 0)
	}
	if !validCanonicalUUID(controllerID) || !activeIdentity {
		return ManagementKeyRotationStatus{}, managementFailure(ManagementErrorIdentity, 0)
	}

	frame, err := service.controls.StartKeyRotation(ctx, controllerID)
	rotationID := ""
	if frame != nil {
		rotationID = frame.RotationID
	}
	destroyManagementKeyRotationPropose(frame)
	if err != nil {
		return ManagementKeyRotationStatus{}, managementControlFailure(err, ManagementErrorRotationConflict)
	}
	service.supervisor.Reconcile()
	if !validCanonicalUUID(rotationID) {
		return ManagementKeyRotationStatus{}, managementFailure("persistence_unavailable", 0)
	}

	rotation, err := service.repository.Rotation(ctx, controllerID, rotationID)
	if err != nil {
		return ManagementKeyRotationStatus{}, managementFailure("persistence_unavailable", 0)
	}
	if rotation.ControllerID != controllerID || rotation.RotationID != rotationID {
		return ManagementKeyRotationStatus{}, managementFailure("persistence_unavailable", 0)
	}
	return ManagementKeyRotationStatus{RotationID: rotation.RotationID, State: rotation.State, ExpiresAt: rotation.ExpiresAt}, nil
}

func destroyManagementBindingRemove(frame *protocol.BindingRemove) {
	if frame != nil {
		*frame = protocol.BindingRemove{}
	}
}

func destroyManagementKeyRotationPropose(frame *protocol.KeyRotationPropose) {
	if frame != nil {
		frame.NewPublicKey = ""
		*frame = protocol.KeyRotationPropose{}
	}
}

func managementEnrollmentFailure(err error) error {
	var enrollmentErr *EnrollmentError
	if !errors.As(err, &enrollmentErr) {
		return managementFailure(ManagementErrorUnavailable, 0)
	}
	code := safeManagementEnrollmentCode(enrollmentErr.Code)
	return managementFailure(code, enrollmentErr.RetryAfter)
}

func managementControlFailure(err error, stateCode string) error {
	var controlErr *SessionControlError
	if !errors.As(err, &controlErr) {
		return managementFailure(ManagementErrorUnavailable, 0)
	}
	switch controlErr.Code {
	case controlErrorInvalid:
		return managementFailure(ManagementErrorInvalidRequest, 0)
	case controlErrorPersistence:
		return managementFailure("persistence_unavailable", 0)
	case controlErrorCredential:
		return managementFailure("credential_unavailable", 0)
	case controlErrorRevoked:
		return managementFailure(ManagementErrorIdentity, 0)
	case controlErrorState, controlErrorExpired:
		return managementFailure(stateCode, 0)
	default:
		return managementFailure(ManagementErrorUnavailable, 0)
	}
}

func safeManagementEnrollmentCode(code string) string {
	switch code {
	case ManagementErrorInvalidRequest, ManagementErrorEnrollmentMissing,
		"enrollment_failed", "authorization_denied", "authorization_expired",
		"invalid_source", "authentication_required", "source_access_lost",
		"provider_unavailable", "relay_unavailable", "relay_invalid_response",
		"relay_rejected", "credential_unavailable", "credential_cleanup_pending",
		"persistence_unavailable":
		return code
	case "identity_unavailable":
		return ManagementErrorIdentity
	default:
		return ManagementErrorUnavailable
	}
}

func safeManagementErrorCode(code string) string {
	switch code {
	case ManagementErrorUnavailable, ManagementErrorInvalidRequest,
		ManagementErrorEnrollmentMissing, ManagementErrorBindingMissing,
		ManagementErrorBindingState, ManagementErrorIdentity,
		ManagementErrorRotationConflict, "enrollment_failed", "authorization_denied",
		"authorization_expired", "invalid_source", "authentication_required",
		"source_access_lost", "provider_unavailable", "relay_unavailable",
		"relay_invalid_response", "relay_rejected", "credential_unavailable",
		"credential_cleanup_pending", "persistence_unavailable":
		return code
	default:
		return ManagementErrorUnavailable
	}
}

func managementFailure(code string, retryAfter time.Duration) error {
	return &ManagementError{Code: safeManagementErrorCode(code), RetryAfter: boundedManagementRetryAfter(retryAfter)}
}

func boundedManagementRetryAfter(value time.Duration) time.Duration {
	if value <= 0 {
		return 0
	}
	if value > maxManagementRetryAfter {
		return maxManagementRetryAfter
	}
	return value
}

func nonnegativeManagementCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func managementAgeSeconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	seconds := math.Ceil(value.Seconds())
	if seconds >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(seconds)
}
