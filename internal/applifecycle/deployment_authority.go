package applifecycle

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrDeploymentAuthorityInvalid     = errors.New("deployment desired lifecycle authority is invalid")
	ErrDeploymentAuthorityUnavailable = errors.New("deployment desired lifecycle authority is unavailable")
	ErrDeploymentNotAuthorized        = errors.New("deployment is not authorized by desired lifecycle")
	ErrDeploymentAuthorityChanged     = errors.New("deployment desired lifecycle authority changed")
)

type deploymentAuthorityMode uint8

const (
	deploymentAuthorityLegacyUninitialized deploymentAuthorityMode = iota + 1
	deploymentAuthorityRunning
	deploymentAuthorityStopped
)

// DeploymentAuthoritySnapshot is an immutable value read from one exact
// application's desired lifecycle head. It is a read guard only: callers still
// need shared exclusion and a durable per-effect journal before external
// runtime effects can be authorized across crashes or concurrent writers.
type DeploymentAuthoritySnapshot struct {
	applicationID string
	revision      int64
	mode          deploymentAuthorityMode
}

func (s DeploymentAuthoritySnapshot) ApplicationID() string { return s.applicationID }
func (s DeploymentAuthoritySnapshot) Revision() int64       { return s.revision }

func (s DeploymentAuthoritySnapshot) IsLegacyUninitialized() bool {
	return s.mode == deploymentAuthorityLegacyUninitialized
}

func (s DeploymentAuthoritySnapshot) DesiredState() (State, bool) {
	if s.mode == deploymentAuthorityRunning {
		return Running, true
	}
	return "", false
}

type deploymentAuthorityHeadReader interface {
	Head(context.Context, string) (Head, error)
}

type DeploymentAuthority struct {
	heads deploymentAuthorityHeadReader
}

func NewDeploymentAuthority(heads deploymentAuthorityHeadReader) (*DeploymentAuthority, error) {
	if heads == nil {
		return nil, errors.New("deployment desired lifecycle head reader is required")
	}
	return &DeploymentAuthority{heads: heads}, nil
}

func (a *DeploymentAuthority) Snapshot(ctx context.Context, applicationID string) (DeploymentAuthoritySnapshot, error) {
	head, err := a.read(ctx, applicationID)
	if err != nil {
		return DeploymentAuthoritySnapshot{}, err
	}
	if head.mode == deploymentAuthorityStopped {
		return DeploymentAuthoritySnapshot{}, ErrDeploymentNotAuthorized
	}
	return head, nil
}

func (a *DeploymentAuthority) Revalidate(ctx context.Context, snapshot DeploymentAuthoritySnapshot) error {
	if !validDeploymentAuthoritySnapshot(snapshot) {
		return ErrDeploymentAuthorityInvalid
	}
	current, err := a.read(ctx, snapshot.applicationID)
	if err != nil {
		return err
	}
	if current != snapshot {
		return ErrDeploymentAuthorityChanged
	}
	return nil
}

func (a *DeploymentAuthority) read(ctx context.Context, applicationID string) (DeploymentAuthoritySnapshot, error) {
	if a == nil || a.heads == nil || ctx == nil || applicationID == "" {
		return DeploymentAuthoritySnapshot{}, ErrDeploymentAuthorityInvalid
	}
	if canceled := ctx.Err(); canceled != nil {
		return DeploymentAuthoritySnapshot{}, canceled
	}
	head, err := a.heads.Head(ctx, applicationID)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return DeploymentAuthoritySnapshot{}, canceled
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return DeploymentAuthoritySnapshot{}, err
		}
		if errors.Is(err, ErrApplicationNotFound) {
			return DeploymentAuthoritySnapshot{}, fmt.Errorf("%w: desired lifecycle head is missing", ErrDeploymentAuthorityInvalid)
		}
		return DeploymentAuthoritySnapshot{}, fmt.Errorf("%w: read desired lifecycle head: %v", ErrDeploymentAuthorityUnavailable, err)
	}
	if canceled := ctx.Err(); canceled != nil {
		return DeploymentAuthoritySnapshot{}, canceled
	}
	if head.ApplicationID != applicationID || head.Revision < 0 {
		return DeploymentAuthoritySnapshot{}, ErrDeploymentAuthorityInvalid
	}
	if head.DesiredState == nil {
		if head.Revision != 0 || head.UpdatedAt != nil {
			return DeploymentAuthoritySnapshot{}, ErrDeploymentAuthorityInvalid
		}
		return DeploymentAuthoritySnapshot{
			applicationID: applicationID,
			mode:          deploymentAuthorityLegacyUninitialized,
		}, nil
	}
	if head.Revision < 1 || head.UpdatedAt == nil {
		return DeploymentAuthoritySnapshot{}, ErrDeploymentAuthorityInvalid
	}
	switch *head.DesiredState {
	case Running:
		return DeploymentAuthoritySnapshot{
			applicationID: applicationID,
			revision:      head.Revision,
			mode:          deploymentAuthorityRunning,
		}, nil
	case Stopped:
		return DeploymentAuthoritySnapshot{
			applicationID: applicationID,
			revision:      head.Revision,
			mode:          deploymentAuthorityStopped,
		}, nil
	default:
		return DeploymentAuthoritySnapshot{}, ErrDeploymentAuthorityInvalid
	}
}

func validDeploymentAuthoritySnapshot(snapshot DeploymentAuthoritySnapshot) bool {
	switch snapshot.mode {
	case deploymentAuthorityLegacyUninitialized:
		return snapshot.applicationID != "" && snapshot.revision == 0
	case deploymentAuthorityRunning:
		return snapshot.applicationID != "" && snapshot.revision > 0
	default:
		return false
	}
}
