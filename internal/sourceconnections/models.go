package sourceconnections

import "time"

const (
	StatusPending      = "pending"
	StatusConnected    = "connected"
	StatusDenied       = "denied"
	StatusExpired      = "expired"
	StatusDisconnected = "disconnected"
	StatusAccessLost   = "access_lost"
)

type Connection struct {
	ID                   string
	OwnerUserID          string
	Status               string
	ProviderUserID       string
	ProviderLogin        string
	CredentialGeneration int64
	PendingExpiresAt     *time.Time
	PollInterval         time.Duration
	NextPollAt           *time.Time
	AccessExpiresAt      *time.Time
	RefreshExpiresAt     *time.Time
	LastErrorCode        string
	ConnectedAt          *time.Time
	DisconnectedAt       *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type DeviceStart struct {
	ConnectionID    string
	UserCode        string
	VerificationURI string
	InstallURL      string
	ExpiresAt       time.Time
	PollInterval    time.Duration
}

type Installation struct {
	ID                  int64
	AccountLogin        string
	AccountType         string
	TargetType          string
	RepositorySelection string
	SuspendedAt         *time.Time
	CachedAt            time.Time
}

type InstallationPage struct {
	Page          int
	PerPage       int
	TotalCount    int
	Installations []Installation
}

type TokenBundle struct {
	Version          int       `json:"version"`
	Generation       int64     `json:"generation"`
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	ProviderUserID   string    `json:"providerUserId"`
	ProviderLogin    string    `json:"providerLogin"`
}
