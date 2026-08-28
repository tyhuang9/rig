package controllerrelay

import (
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	SessionDisconnected   = "disconnected"
	SessionConnecting     = "connecting"
	SessionAuthenticating = "authenticating"
	SessionReady          = "ready"
	SessionBackoff        = "backoff"
	SessionNeedsAttention = "needs_attention"
	SessionStopped        = "stopped"

	SubscriptionActive  = "active"
	SubscriptionRetired = "retired"

	SyncInflight = "inflight"
	SyncAcked    = "acked"

	DecisionAck    = "ack"
	DecisionReject = "reject"

	RejectUnknownSubscription = "source.unknown_subscription"
	RejectScopeMismatch       = "source.scope_mismatch"
	RejectGenerationConflict  = "source.generation_conflict"
	RejectInvalidEvent        = "access.invalid_event"

	CommandBindingRemove    = "binding.remove"
	CommandRotationPropose  = "key.rotation.propose"
	CommandRotationConfirm  = "key.rotation.confirm"
	CommandRotationFinalize = "key.rotation.finalize"

	CommandPrepared  = "prepared"
	CommandCompleted = "completed"
)

type SessionStatus struct {
	ControllerID   string
	Epoch          uint64
	Fence          uint64
	State          string
	KeyID          string
	ErrorCode      string
	Attempt        uint32
	NextAttemptAt  *time.Time
	LastReadyAt    *time.Time
	LastSeenAt     *time.Time
	StateChangedAt time.Time
	UpdatedAt      time.Time
}

type RelaySubscription struct {
	SubscriptionID string
	OwnerUserID    string
	BindingID      string
	ControllerID   string
	InstallationID int64
	RepositoryID   int64
	Ref            string
	State          string
	CreatedAt      time.Time
	RetiredAt      *time.Time
}

func (s RelaySubscription) Protocol() protocol.Subscription {
	return protocol.Subscription{SubscriptionID: s.SubscriptionID, InstallationID: s.InstallationID, RepositoryID: s.RepositoryID, Ref: s.Ref}
}

type SyncSnapshot struct {
	ControllerID string
	Generation   uint64
	MessageID    string
	SentAt       time.Time
	Digest       [32]byte
	State        string
	Items        []protocol.Subscription
	AckedAt      *time.Time
}

type InboxDecision struct {
	Kind string
	Code string
}

// SourceACKHead is the compact, authoritative identity of the newest durable
// source.desired envelope for one subscription. Raw inbox rows are bounded
// audit history and are not used to determine acknowledgement state.
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

func AckDecision() InboxDecision { return InboxDecision{Kind: DecisionAck} }

func RejectDecision(code string) InboxDecision {
	return InboxDecision{Kind: DecisionReject, Code: code}
}

type OutboundCommand struct {
	ControllerID string
	MessageID    string
	CommandType  string
	BindingID    string
	RotationID   string
	Stage        string
	SentAt       time.Time
	Digest       [32]byte
	State        string
	CompletedAt  *time.Time
}
