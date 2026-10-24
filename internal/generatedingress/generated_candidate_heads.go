package generatedingress

import (
	"math"
	"sync"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
)

// Head generations are process-local authority observations, not durable
// candidate accounting pins: Begin persists only predecessor identity/slot.
// This registry is shared by every alias of the concrete command owner and
// outlives reservation charge epochs. No caller may reset or refresh an anchor.
type candidateHeadRegistry struct {
	mu     sync.Mutex
	groups map[candidateCapacityGroupKey]*candidateHeadGroup
}

type candidateHeadPins struct {
	appID, deploymentID, releaseID, slot                  string
	previousDeploymentID, previousReleaseID, previousSlot string
}

type candidateHeadSnapshot struct {
	appID, deploymentID, releaseID, slot, activationReceiptID string
	generation                                                int64
}

type candidateHeadGroup struct {
	pins        candidateHeadPins
	predecessor *candidateHeadSnapshot
	self        *candidateHeadSnapshot
}

type candidateHeadMode uint8

const (
	candidatePredecessorHead candidateHeadMode = iota + 1
	candidateReadOnlySelfHead
)

// An anchor is an immutable expected head for a later guarded SQL check. It
// is not a physical observation, mutation permit, or authorization result.
type candidateHeadAnchor struct {
	snapshot candidateHeadSnapshot
	mode     candidateHeadMode
}

func newCandidateHeadRegistry() *candidateHeadRegistry {
	return &candidateHeadRegistry{groups: make(map[candidateCapacityGroupKey]*candidateHeadGroup)}
}

func validCandidateHeadPins(p candidateHeadPins) bool {
	if uuid.Validate(p.appID) != nil || uuid.Validate(p.deploymentID) != nil || !validReleaseID(p.releaseID) ||
		!validGeneratedSlotRelationship(p.slot, p.previousDeploymentID, p.previousSlot) {
		return false
	}
	if p.previousDeploymentID == "" {
		return p.previousReleaseID == ""
	}
	return p.previousDeploymentID != p.deploymentID && uuid.Validate(p.previousDeploymentID) == nil && validReleaseID(p.previousReleaseID)
}

// groupLocked permanently records immutable identities even if the subsequent
// head check refuses. Call only after exact risky capacity has been attached.
func (r *candidateHeadRegistry) groupLocked(pins candidateHeadPins) (*candidateHeadGroup, error) {
	key := candidateCapacityGroupKey{appID: pins.appID, deploymentID: pins.deploymentID}
	group := r.groups[key]
	if group == nil {
		group = &candidateHeadGroup{pins: pins}
		r.groups[key] = group
	} else if group.pins != pins {
		return nil, deploymentexecution.ErrIdentityLost
	}
	return group, nil
}

// bindPredecessor must run with no SQL transaction or other owner lock held.
// The owner first seeds accounting from durable records, reads an exact head,
// binds here, then rechecks this immutable snapshot in every final writer Tx.
func (r *candidateHeadRegistry) bindPredecessor(pins candidateHeadPins, observed candidateHeadSnapshot) (candidateHeadAnchor, error) {
	if r == nil || r.groups == nil || !validCandidateHeadPins(pins) {
		return candidateHeadAnchor{}, deploymentexecution.ErrInvalidConfiguration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	group, err := r.groupLocked(pins)
	if err != nil {
		return candidateHeadAnchor{}, err
	}
	if group.self != nil || observed.appID != pins.appID || observed.deploymentID != pins.previousDeploymentID ||
		observed.releaseID != pins.previousReleaseID || observed.slot != pins.previousSlot ||
		(pins.previousDeploymentID == "" && observed.generation != 0) || (pins.previousDeploymentID != "" && observed.generation < 1) {
		return candidateHeadAnchor{}, deploymentexecution.ErrAuthorityStale
	}
	if group.predecessor == nil {
		copy := observed
		group.predecessor = &copy
	} else if *group.predecessor != observed {
		return candidateHeadAnchor{}, deploymentexecution.ErrAuthorityStale
	}
	return candidateHeadAnchor{snapshot: *group.predecessor, mode: candidatePredecessorHead}, nil
}

// bindSelf is exclusively for route observation after the owner validates
// SwitchingRoute and the complete Active roster. It never derives a historical
// predecessor generation or grants positive start/cleanup/transfer authority.
func (r *candidateHeadRegistry) bindSelf(pins candidateHeadPins, observed candidateHeadSnapshot) (candidateHeadAnchor, error) {
	return r.bindColdSelf(pins, observed, false)
}

// bindColdSelf records a read-only current head reconstructed from the sealed
// startup census. A null receipt is accepted only for an explicitly selected
// quiescent pre-042 head; it never authorizes a switching or draining replay.
func (r *candidateHeadRegistry) bindColdSelf(pins candidateHeadPins, observed candidateHeadSnapshot,
	allowLegacyReceipt bool,
) (candidateHeadAnchor, error) {
	if r == nil || r.groups == nil || !validCandidateHeadPins(pins) {
		return candidateHeadAnchor{}, deploymentexecution.ErrInvalidConfiguration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	group, err := r.groupLocked(pins)
	if err != nil {
		return candidateHeadAnchor{}, err
	}
	if observed.appID != pins.appID || observed.deploymentID != pins.deploymentID || observed.releaseID != pins.releaseID ||
		observed.slot != pins.slot || observed.generation < 1 ||
		(observed.activationReceiptID == "" && !allowLegacyReceipt) ||
		(observed.activationReceiptID != "" && !validCanonicalUUID(observed.activationReceiptID)) {
		return candidateHeadAnchor{}, deploymentexecution.ErrAuthorityStale
	}
	if group.predecessor != nil && (group.predecessor.generation == math.MaxInt64 || observed.generation != group.predecessor.generation+1) {
		return candidateHeadAnchor{}, deploymentexecution.ErrAuthorityStale
	}
	if group.self == nil {
		copy := observed
		group.self = &copy
	} else if *group.self != observed {
		return candidateHeadAnchor{}, deploymentexecution.ErrAuthorityStale
	}
	return candidateHeadAnchor{snapshot: *group.self, mode: candidateReadOnlySelfHead}, nil
}

func (a candidateHeadAnchor) checkPredecessor(observed candidateHeadSnapshot) error {
	if a.mode != candidatePredecessorHead || a.snapshot != observed {
		return deploymentexecution.ErrAuthorityStale
	}
	return nil
}

func (a candidateHeadAnchor) checkRouteObservation(observed candidateHeadSnapshot) error {
	if (a.mode != candidatePredecessorHead && a.mode != candidateReadOnlySelfHead) || a.snapshot != observed {
		return deploymentexecution.ErrAuthorityStale
	}
	return nil
}
