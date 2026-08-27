package controllerrelay

import "time"

const (
	ControllerActive  = "active"
	ControllerRevoked = "revoked"

	KeyPending = "pending"
	KeyActive  = "active"
	KeyRevoked = "revoked"

	RotationPrepare    = "prepare"
	RotationPropose    = "propose"
	RotationConfirm    = "confirm"
	RotationNewKeyAuth = "new_key_auth"
	RotationFinalize   = "finalize"
	RotationCompleted  = "completed"
	RotationFailed     = "failed"

	BindingPending        = "pending"
	BindingAuthorized     = "authorized"
	BindingDenied         = "denied"
	BindingExpired        = "expired"
	BindingRemovalPending = "removal_pending"
	BindingRemoved        = "removed"
	BindingAccessLost     = "access_lost"
	BindingFailed         = "failed"

	EnrollmentPending    = "pending"
	EnrollmentAuthorized = "authorized"
	EnrollmentDenied     = "denied"
	EnrollmentExpired    = "expired"
	EnrollmentFailed     = "failed"

	KeyAlgorithmEd25519 = "ed25519"
	EnrollmentPurpose   = "controller-relay-enrollment-poll"

	ErrorAuthorizationDenied  = "authorization_denied"
	ErrorAuthorizationExpired = "authorization_expired"
	ErrorEnrollmentFailed     = "enrollment_failed"
	ErrorKeyRevoked           = "key_revoked"
	ErrorProtocol             = "protocol_error"
	ErrorProviderUnavailable  = "provider_unavailable"
	ErrorRelayUnavailable     = "relay_unavailable"
	ErrorRemovalFailed        = "removal_failed"
	ErrorRotationFailed       = "rotation_failed"
	ErrorSourceAccessLost     = "source_access_lost"
)

type ControllerIdentity struct {
	ControllerID  string
	State         string
	LastErrorCode string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RevokedAt     *time.Time
}

type ControllerKey struct {
	KeyID                 string
	ControllerID          string
	PublicKey             []byte
	Algorithm             string
	State                 string
	ProtectedKeyRef       string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ActivatedAt           *time.Time
	PossessionConfirmedAt *time.Time
	RevokedAt             *time.Time
}

type KeyRotation struct {
	RotationID     string
	ControllerID   string
	OldKeyID       string
	NewKeyID       string
	State          string
	ExpiresAt      time.Time
	StateChangedAt time.Time
	LastErrorCode  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

type InstallationBinding struct {
	BindingID      string
	OwnerUserID    string
	ConnectionID   string
	ControllerID   string
	InstallationID int64
	RepositoryID   int64
	State          string
	StateChangedAt time.Time
	LastErrorCode  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

// RelayBindingReadModel is the narrow durable projection exposed to relay
// management. It intentionally excludes controller, provider, error, and
// protocol fields.
type RelayBindingReadModel struct {
	BindingID      string
	ConnectionID   string
	InstallationID int64
	RepositoryID   int64
	State          string
	UpdatedAt      time.Time
}

// RelayKeyRotationReadModel is the narrow durable projection for the one live
// singleton-controller rotation. A zero value means no rotation is active.
type RelayKeyRotationReadModel struct {
	InProgress bool
	State      string
	ExpiresAt  time.Time
	UpdatedAt  time.Time
}

type RelayReadModel struct {
	RemovableBindings []RelayBindingReadModel
	KeyRotation       RelayKeyRotationReadModel
}

type Enrollment struct {
	EnrollmentID     string
	OwnerUserID      string
	ConnectionID     string
	ControllerID     string
	KeyID            string
	BindingID        string
	InstallationID   int64
	RepositoryID     int64
	Purpose          string
	ProtectedPollRef string
	State            string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	StateChangedAt   time.Time
	UpdatedAt        time.Time
	LastPolledAt     *time.Time
	CompletedAt      *time.Time
	PollRefClearedAt *time.Time
	LastErrorCode    string
}

func ProtectedKeyRef(controllerID, keyID string) string {
	return "relay/controllers/" + controllerID + "/keys/" + keyID + ".key"
}

func ProtectedEnrollmentPollRef(controllerID, enrollmentID string) string {
	return "relay/controllers/" + controllerID + "/enrollments/" + enrollmentID + "/poll"
}
