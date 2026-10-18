// Package deploymentbinding stores the immutable desired-lifecycle authority
// accepted with an ordinary application deployment.
package deploymentbinding

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Mode string

const (
	ModeLegacyUninitialized Mode = "legacy_uninitialized"
	ModeRunning             Mode = "running"
)

type Classification uint8

const (
	ClassificationCurrent Classification = iota + 1
	ClassificationStale
)

type Completion uint8

const (
	CompletionIncomplete Completion = iota + 1
	CompletionComplete
)

var (
	ErrBindingNotFound      = errors.New("application deployment lifecycle binding not found")
	ErrInvalidBinding       = errors.New("invalid application deployment lifecycle binding")
	ErrLifecycleUnresolved  = errors.New("application lifecycle authority is unresolved")
	ErrUnavailable          = errors.New("application deployment lifecycle binding storage unavailable")
	ErrInvalidConfiguration = errors.New("invalid application deployment lifecycle binding configuration")
	ErrCanceled             = errors.New("application deployment lifecycle binding operation canceled")
)

// OperationRef identifies an immutable lifecycle operation selected from
// retained database state. It is never caller-proposed effect permission.
type OperationRef struct {
	ApplicationID   string
	OperationID     string
	DesiredRevision int64
}

// LifecycleTxValidator is implemented by the canonical lifecycle repository.
// Every method must use the supplied transaction for all nested reads.
type LifecycleTxValidator interface {
	ConfiguredFor(*sql.DB) bool
	HistoricalBindingTx(context.Context, *sql.Tx, OperationRef) error
	CurrentOperationTx(context.Context, *sql.Tx, OperationRef) (Completion, error)
	UnresolvedHistoryTx(context.Context, *sql.Tx, string) (bool, error)
}

// Binding is immutable authority identity. Its fields are deliberately private
// so a caller cannot construct a revision permit.
type Binding struct {
	repository                *Repository
	jobID                     string
	applicationID             string
	desiredRevision           int64
	mode                      Mode
	authorizingLifecycleJobID string
	createdAt                 time.Time
}

func (b Binding) JobID() string                     { return b.jobID }
func (b Binding) ApplicationID() string             { return b.applicationID }
func (b Binding) DesiredRevision() int64            { return b.desiredRevision }
func (b Binding) Mode() Mode                        { return b.mode }
func (b Binding) AuthorizingLifecycleJobID() string { return b.authorizingLifecycleJobID }
func (b Binding) CreatedAt() time.Time              { return b.createdAt }

// Equal reports equality of immutable persisted identity. Repository ownership
// is intentionally included so a value read from another database cannot be
// used as a permit in this repository.
func (b Binding) Equal(other Binding) bool {
	return b.repository == other.repository && b.jobID == other.jobID &&
		b.applicationID == other.applicationID && b.desiredRevision == other.desiredRevision &&
		b.mode == other.mode && b.authorizingLifecycleJobID == other.authorizingLifecycleJobID &&
		b.createdAt.Equal(other.createdAt)
}

func (b Binding) validFor(repository *Repository) bool {
	if repository == nil || b.repository != repository || b.jobID == "" || b.applicationID == "" || b.createdAt.IsZero() {
		return false
	}
	switch b.mode {
	case ModeLegacyUninitialized:
		return b.desiredRevision == 0 && b.authorizingLifecycleJobID == ""
	case ModeRunning:
		return b.desiredRevision > 0 && b.authorizingLifecycleJobID != ""
	default:
		return false
	}
}
