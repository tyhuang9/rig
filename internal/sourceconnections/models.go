package sourceconnections

import (
	"fmt"
	"log/slog"
	"time"
)

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
	UserCode        string `json:"-"`
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

type SourceRepository struct {
	ID            int64
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	Archived      bool
	Disabled      bool
}

type RepositoryPage struct {
	Page         int
	PerPage      int
	TotalCount   int
	Repositories []SourceRepository
}

type Branch struct {
	Name      string
	SHA       string
	Protected bool
}

type BranchPage struct {
	Page     int
	PerPage  int
	Branches []Branch
}

type TokenBundle struct {
	Version          int       `json:"version"`
	Generation       int64     `json:"generation"`
	AccessToken      string    `json:"-"`
	RefreshToken     string    `json:"-"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	ProviderUserID   string    `json:"providerUserId"`
	ProviderLogin    string    `json:"providerLogin"`
}

type TokenExchange struct {
	Version          int       `json:"version"`
	AccessToken      string    `json:"-"`
	RefreshToken     string    `json:"-"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
}

func (start DeviceStart) String() string {
	return fmt.Sprintf("GitHub device connection %s (expires %s)", start.ConnectionID, start.ExpiresAt.Format(time.RFC3339))
}

func (start DeviceStart) GoString() string { return start.String() }

func (start DeviceStart) LogValue() slog.Value {
	return slog.GroupValue(slog.String("connection_id", start.ConnectionID), slog.Time("expires_at", start.ExpiresAt))
}

func (exchange TokenExchange) String() string   { return "GitHub protected token exchange" }
func (exchange TokenExchange) GoString() string { return exchange.String() }
func (exchange TokenExchange) LogValue() slog.Value {
	return slog.GroupValue(slog.String("state", "protected"))
}
