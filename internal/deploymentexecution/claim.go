// Package deploymentexecution defines dormant, mandatory authority identity
// for ordinary deployment effects. It does not execute commands.
package deploymentexecution

import (
	"errors"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

var (
	ErrInvalidClaim         = errors.New("deployment execution claim is invalid")
	ErrInvalidConfiguration = errors.New("deployment execution authority configuration is invalid")
	ErrScopeUnavailable     = errors.New("deployment execution scope is unavailable")
	ErrAuthorityStale       = errors.New("deployment execution authority is stale")
	ErrAuthorityInvalid     = errors.New("deployment execution authority history is invalid")
	ErrAuthorityUnresolved  = errors.New("deployment execution authority is unresolved")
	ErrIdentityLost         = errors.New("deployment execution identity is no longer current")
	ErrStageInvalid         = errors.New("deployment execution stage is not authorized")
	ErrPositiveFence        = errors.New("deployment execution is blocked by pending lifecycle start")
	ErrUnavailable          = errors.New("deployment execution authority storage is unavailable")
	ErrCanceled             = errors.New("deployment execution authority check was canceled")
)

// Claim retains the original immutable binding and worker identity. Possession
// alone grants no permission; every effect boundary must check it again.
type Claim struct {
	binding       deploymentbinding.Binding
	jobID         string
	applicationID string
	attempt       int
}

func NewClaim(original deploymentbinding.Binding, jobID, applicationID string, attempt int) (Claim, error) {
	if jobID == "" || applicationID == "" || attempt < 1 || original.JobID() != jobID || original.ApplicationID() != applicationID {
		return Claim{}, ErrInvalidClaim
	}
	return Claim{binding: original, jobID: jobID, applicationID: applicationID, attempt: attempt}, nil
}

func (c Claim) JobID() string         { return c.jobID }
func (c Claim) ApplicationID() string { return c.applicationID }
func (c Claim) Attempt() int          { return c.attempt }

func (c Claim) valid() bool {
	return c.jobID != "" && c.applicationID != "" && c.attempt > 0 &&
		c.binding.JobID() == c.jobID && c.binding.ApplicationID() == c.applicationID
}
