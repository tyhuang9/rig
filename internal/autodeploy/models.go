package autodeploy

import (
	"errors"
	"fmt"
	"time"
)

const (
	StateDisabled    = "disabled"
	StateIdle        = "idle"
	StateDispatching = "dispatching"
	StateDeploying   = "deploying"
	StatePaused      = "paused"
	StateRetryWait   = "retry_wait"

	PauseApprovalRequired                = "approval_required"
	PauseMigrationApprovalRequired       = "migration_approval_required"
	PauseInsufficientReplacementCapacity = "insufficient_replacement_capacity"
	PauseDeploymentPlanReviewRequired    = "deployment_plan_review_required"
	PauseDeploymentFailed                = "deployment_failed"
	PauseMissingConfig                   = "missing_configuration"
	PauseSourceAccessLost                = "source_access_lost"
	PauseInvalidSource                   = "invalid_source"
	PauseProviderUnavailable             = "provider_unavailable"
	PauseRelayUnavailable                = "relay_unavailable"
)

var (
	ErrNotFound                 = errors.New("auto-deploy configuration not found")
	ErrInvalid                  = errors.New("invalid auto-deploy request")
	ErrConflict                 = errors.New("auto-deploy revision conflict")
	ErrState                    = errors.New("invalid auto-deploy state transition")
	ErrUnauthorized             = errors.New("auto-deploy actor is not authorized")
	ErrApplicationBusy          = errors.New("application has an active auto-deploy job")
	ErrSourceAccessLost         = errors.New("auto-deploy source access lost")
	ErrDispatchPreflightChanged = errors.New("auto-deploy dispatch preflight changed")
)

type ConfigureRequest struct {
	ApplicationID    string
	ActorUserID      string
	ExpectedRevision uint64
	Enabled          bool
}

type Status struct {
	ApplicationID              string
	Revision                   uint64
	Enabled                    bool
	SourceOwnerUserID          string
	ConfiguredByUserID         string
	ControllerID               string
	BindingID                  string
	SubscriptionID             string
	SourceConnectionID         string
	InstallationID             int64
	RepositoryID               int64
	TrackedBranch              string
	TrackedRef                 string
	SourceScopeActive          bool
	State                      string
	LastConsumedGeneration     uint64
	LatestResolvedGeneration   uint64
	LatestResolvedSHA          string
	DispatchSequence           uint64
	PreparedDispatchSequence   uint64
	PreparedDispatchGeneration uint64
	PreparedDispatchSHA        string
	ActiveJobID                string
	ActiveDispatchSequence     uint64
	ActiveGeneration           uint64
	ActiveSHA                  string
	LastSuccessfulDeployedSHA  string
	PauseCode                  string
	PausedSHA                  string
	RetryAttempt               uint32
	NextRetryAt                *time.Time
	NextJobPollAt              *time.Time
	LastReconciledAt           *time.Time
	NextReconcileAt            *time.Time
	LeaseFence                 uint64
	LeaseExpiresAt             *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type WorkLease struct {
	ApplicationID  string
	ConfigRevision uint64
	Fence          uint64
	Token          string
	ExpiresAt      time.Time
}

type SourceACKHead struct {
	ControllerID   string
	SubscriptionID string
	DeliveryID     string
	Generation     uint64
	InstallationID int64
	RepositoryID   int64
	Ref            string
	ObservedSHA    string
	ObservedAt     time.Time
	ReceivedAt     time.Time
}

type PreparedDispatch struct {
	ApplicationID string
	Sequence      uint64
	Generation    uint64
	SHA           string
}

// DispatchIdempotencyKey is stable across coordinator crashes. Jobs already
// scope idempotency by application and type, so the configuration revision and
// dispatch sequence are the complete replay identity.
func DispatchIdempotencyKey(configRevision, sequence uint64) string {
	return fmt.Sprintf("auto-deploy:%d:%d", configRevision, sequence)
}
