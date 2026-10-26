package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedRouteAuthority owns route pinning and atomic activation for the
// exact Manager and candidate owner published by one GeneratedDomain.
type GeneratedRouteAuthority struct {
	self         *GeneratedRouteAuthority
	manager      *Manager
	candidates   *GeneratedCandidateAuthority
	stores       *deployments.GeneratedOwnerStores
	runtime      *generatedruntimestate.Repository
	construction *generatedDomainConstruction
}

func newGeneratedRouteAuthority(manager *Manager, candidates *GeneratedCandidateAuthority,
	stores *deployments.GeneratedOwnerStores,
) (*GeneratedRouteAuthority, error) {
	if manager == nil || candidates == nil || stores == nil || !stores.Configured() ||
		candidates.stores != stores || candidates.ledger == nil || candidates.heads == nil {
		return nil, errors.New("generated route authority configuration is invalid")
	}
	result := &GeneratedRouteAuthority{manager: manager, candidates: candidates, stores: stores,
		runtime: generatedruntimestate.New(stores.Database())}
	result.self = result
	return result, nil
}

func (a *GeneratedRouteAuthority) configuredFor(construction *generatedDomainConstruction) bool {
	return a.configured() && construction != nil && a.manager == construction.manager &&
		a.candidates == construction.candidates && a.stores == construction.candidates.stores &&
		a == construction.routes && a.construction == construction
}

func (a *GeneratedRouteAuthority) configured() bool {
	return a != nil && a.self == a && a.manager != nil && a.candidates != nil && a.stores != nil &&
		a.stores.Configured() && a.candidates.stores == a.stores &&
		a.candidates.ledger != nil && a.candidates.heads != nil &&
		a.runtime != nil && a.runtime.UsesDatabase(a.stores.Database()) &&
		a.manager.domainView != nil && a.manager.domainView.manager == a.manager &&
		(a.construction == nil || (a.construction.routes == a &&
			a.construction.manager == a.manager && a.construction.candidates == a.candidates &&
			a.construction.reservation != nil && a.construction.domain != nil &&
			a.construction.domain.construction == a.construction))
}

// GeneratedRouteExecution is a one-shot sealed route and activation
// operation. It retains the original candidate execution and Claim; callers
// cannot supply an activation receipt, digest, or writer success assertion.
type GeneratedRouteExecution struct {
	self              *GeneratedRouteExecution
	liveness          *generatedRouteExecutionLiveness
	authority         *GeneratedRouteAuthority
	candidate         *GeneratedCandidateExecution
	request           generatedroute.RouteSwitchRequest
	expectedHead      generatedruntimestate.ActiveHead
	candidateRoster   []generatedruntimestate.RouteActivationComponent
	predecessorRoster []generatedruntimestate.RouteActivationComponent
	previous          routeRecord
	hadPrevious       bool
	receiptID         string
	intent            []byte
}

type generatedRouteExecutionLiveness struct {
	mu         sync.Mutex
	entry      generatedRouteActivationEntry
	completed  bool
	deployment generatedruntimestate.Deployment
	retirement *generatedPredecessorRetirementState
}

type generatedRouteActivationEntry uint8

const (
	generatedRouteActivationLegacy generatedRouteActivationEntry = iota + 1
	generatedRouteActivationRetirement
)

type generatedRouteIntentDocument struct {
	Version           int                                              `json:"version"`
	ReceiptID         string                                           `json:"receiptId"`
	Request           generatedroute.RouteSwitchRequest                `json:"request"`
	ExpectedHead      generatedruntimestate.ActiveHead                 `json:"expectedHead"`
	CandidateRoster   []generatedruntimestate.RouteActivationComponent `json:"candidateRoster"`
	PredecessorRoster []generatedruntimestate.RouteActivationComponent `json:"predecessorRoster"`
}

type generatedRouteProofDocument struct {
	Version   int          `json:"version"`
	ReceiptID string       `json:"receiptId"`
	Before    *routeRecord `json:"before,omitempty"`
	After     routeRecord  `json:"after"`
}

// Pin binds an exact candidate execution, complete rosters, predecessor head,
// and protected route before any route mutation.
func (a *GeneratedRouteAuthority) Pin(ctx context.Context, candidate *GeneratedCandidateExecution,
	request generatedroute.RouteSwitchRequest,
) (*GeneratedRouteExecution, error) {
	if !a.configured() || candidate == nil ||
		ctx == nil || !validSwitchRequest(request) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	s, done, err := candidate.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	if s.authority != a.candidates || request.AppID != s.pins.claim.ApplicationID() ||
		string(request.ToSlot) != s.pins.candidateSlot {
		return nil, deploymentexecution.ErrIdentityLost
	}
	current, err := s.observeCandidateState(true)
	if err != nil {
		return nil, err
	}
	if current.runtime.phase != generatedruntimestate.PhaseSwitchingRoute || current.main.status != WaitingHealth {
		return nil, deploymentexecution.ErrStageInvalid
	}
	identities := make([]CandidateIdentity, 0, len(current.components))
	for _, member := range current.components {
		if member.state != generatedruntimestate.ComponentHealthy || member.receipt == nil {
			return nil, deploymentexecution.ErrStageInvalid
		}
		permit, err := s.describeDurableCandidate(member)
		if err != nil {
			return nil, err
		}
		observed, found, err := s.authority.inspectCandidate(s.ctx, permit.containerID)
		if err != nil || !found || !permit.matchesRunning(observed) || observed.Health != "healthy" {
			return nil, errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
		identities = append(identities, permit.candidateIdentity())
	}
	if !routeRequestMatchesCandidateIdentities(request, identities) {
		return nil, deploymentexecution.ErrIdentityLost
	}
	candidateRoster, err := a.runtime.RouteActivationRoster(ctx, request.AppID,
		s.pins.deploymentID, generatedruntimestate.ComponentHealthy)
	if err != nil || !routeRosterMatchesCandidateSnapshot(candidateRoster, current) {
		return nil, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	expectedHead := generatedruntimestate.ActiveHead{
		AppID: current.observedHead.appID, DeploymentID: current.observedHead.deploymentID,
		ReleaseID: current.observedHead.releaseID, Slot: current.observedHead.slot,
		Generation: current.observedHead.generation, ActivationReceiptID: current.observedHead.activationReceiptID,
	}
	predecessorRoster := []generatedruntimestate.RouteActivationComponent{}
	var previous routeRecord
	hadPrevious := expectedHead.DeploymentID != ""
	if hadPrevious {
		predecessorRoster, err = a.runtime.RouteActivationRoster(ctx, request.AppID,
			expectedHead.DeploymentID, generatedruntimestate.ComponentActive)
		if err != nil {
			return nil, err
		}
		observation, err := a.manager.Observe(ctx, request.AppID)
		if err != nil || string(observation.Slot) != expectedHead.Slot {
			return nil, errors.Join(deploymentexecution.ErrAuthorityStale, err)
		}
		previous = routeRecord{Slot: observation.Slot, Endpoints: append([]generatedroute.RouteEndpoint(nil), observation.Endpoints...)}
		if request.FromSlot != previous.Slot {
			return nil, deploymentexecution.ErrAuthorityStale
		}
	} else if request.FromSlot != "" {
		return nil, deploymentexecution.ErrAuthorityStale
	}
	receiptID := uuid.NewString()
	intent, err := marshalCanonicalGeneratedRouteDocument(generatedRouteIntentDocument{Version: 1, ReceiptID: receiptID,
		Request: canonicalGeneratedRouteRequest(request), ExpectedHead: expectedHead,
		CandidateRoster: candidateRoster, PredecessorRoster: predecessorRoster})
	if err != nil {
		return nil, err
	}
	result := &GeneratedRouteExecution{authority: a, candidate: candidate,
		request: canonicalGeneratedRouteRequest(request), expectedHead: expectedHead,
		candidateRoster: candidateRoster, predecessorRoster: predecessorRoster,
		previous: previous, hadPrevious: hadPrevious, receiptID: receiptID, intent: intent,
		liveness: &generatedRouteExecutionLiveness{}}
	result.self = result
	return result, nil
}

// Activate switches the exact pinned route and commits the durable activation
// receipt/head/rosters/phase in one original-Claim writer transaction.
func (e *GeneratedRouteExecution) Activate(ctx context.Context) (generatedruntimestate.Deployment, error) {
	deployment, _, err := e.activateEntry(ctx, generatedRouteActivationLegacy)
	return deployment, err
}

// ActivateWithPredecessorRetirement is the only activation entry point that
// may return a receipt-owned retirement handle. The handle is minted from the
// internally committed activation result and accepts no caller proof input.
func (e *GeneratedRouteExecution) ActivateWithPredecessorRetirement(ctx context.Context) (
	generatedruntimestate.Deployment, *GeneratedPredecessorRetirement, error,
) {
	return e.activateEntry(ctx, generatedRouteActivationRetirement)
}

func (e *GeneratedRouteExecution) activateEntry(ctx context.Context, entry generatedRouteActivationEntry) (
	generatedruntimestate.Deployment, *GeneratedPredecessorRetirement, error,
) {
	if e == nil || e.self != e || e.liveness == nil || ctx == nil {
		return generatedruntimestate.Deployment{}, nil, deploymentexecution.ErrInvalidConfiguration
	}
	e.liveness.mu.Lock()
	if e.liveness.entry != 0 {
		if entry == generatedRouteActivationRetirement && e.liveness.entry == entry &&
			e.liveness.completed && e.liveness.retirement != nil {
			deployment := e.liveness.deployment
			retirement := &GeneratedPredecessorRetirement{state: e.liveness.retirement}
			e.liveness.mu.Unlock()
			if err := retirement.current(); err != nil {
				return generatedruntimestate.Deployment{}, nil, err
			}
			return deployment, retirement, nil
		}
		e.liveness.mu.Unlock()
		return generatedruntimestate.Deployment{}, nil, deploymentexecution.ErrScopeUnavailable
	}
	e.liveness.entry = entry
	e.liveness.mu.Unlock()
	var successorPolicies map[string]candidatePolicy
	var err error
	if entry == generatedRouteActivationRetirement {
		successorPolicies, err = e.capturePredecessorRetirementSuccessor()
		if err != nil {
			return generatedruntimestate.Deployment{}, nil, err
		}
	}
	deployment, activation, err := e.activateCore(ctx)
	if err != nil {
		return generatedruntimestate.Deployment{}, nil, err
	}
	if entry == generatedRouteActivationLegacy {
		e.liveness.mu.Lock()
		e.liveness.completed = true
		e.liveness.deployment = deployment
		e.liveness.mu.Unlock()
		return deployment, nil, nil
	}
	retirement, err := e.mintPredecessorRetirement(ctx, activation, successorPolicies)
	if err != nil {
		return generatedruntimestate.Deployment{}, nil, err
	}
	e.liveness.mu.Lock()
	e.liveness.completed = true
	e.liveness.deployment = deployment
	e.liveness.retirement = retirement.state
	e.liveness.mu.Unlock()
	return deployment, retirement, nil
}

func (e *GeneratedRouteExecution) activateCore(ctx context.Context) (
	generatedruntimestate.Deployment, generatedRouteActivationResult, error,
) {
	a := e.authority
	if !a.configured() || e.candidate == nil {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, deploymentexecution.ErrInvalidConfiguration
	}
	s, done, err := e.candidate.begin(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, err
	}
	defer done()
	current, err := s.observeCandidateState(true)
	expectedCandidateHead := candidateHeadSnapshot{
		appID: e.expectedHead.AppID, deploymentID: e.expectedHead.DeploymentID,
		releaseID: e.expectedHead.ReleaseID, slot: e.expectedHead.Slot,
		generation: e.expectedHead.Generation, activationReceiptID: e.expectedHead.ActivationReceiptID,
	}
	if err != nil || !routeRosterMatchesCandidateSnapshot(e.candidateRoster, current) ||
		current.observedHead != expectedCandidateHead {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, errors.Join(deploymentexecution.ErrAuthorityStale, err)
	}
	if e.hadPrevious {
		observation, observeErr := a.manager.Observe(ctx, e.request.AppID)
		if observeErr != nil || !sameRoute(e.previous, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
			return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, errors.Join(deploymentexecution.ErrAuthorityStale, observeErr)
		}
	}
	if err := a.manager.switchGeneratedDomainRoute(ctx, e.request); err != nil {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, err
	}
	afterObservation, err := a.manager.Observe(ctx, e.request.AppID)
	after := routeRecord{Slot: afterObservation.Slot, Endpoints: append([]generatedroute.RouteEndpoint(nil), afterObservation.Endpoints...)}
	proposed := routeRecord{Slot: e.request.ToSlot, Endpoints: append([]generatedroute.RouteEndpoint(nil), e.request.Endpoints...)}
	if err != nil || !sameRoute(after, proposed) {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	proofDocument := generatedRouteProofDocument{Version: 1, ReceiptID: e.receiptID, After: after}
	if e.hadPrevious {
		copy := e.previous
		proofDocument.Before = &copy
	}
	proof, err := marshalCanonicalGeneratedRouteDocument(proofDocument)
	if err != nil {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, err
	}
	input := generatedRouteActivationInput{
		receiptID: e.receiptID, appID: e.request.AppID,
		successorDeploymentID: s.pins.deploymentID, successorReleaseID: s.pins.releaseID,
		successorSlot: s.pins.candidateSlot, expectedHead: e.expectedHead,
		candidateRoster: e.candidateRoster, predecessorRoster: e.predecessorRoster,
		routeIntent: e.intent, committedRouteProof: proof,
	}
	result, activateErr := a.activateRoute(ctx, s.pinned, s.pins.claim, input)
	if activateErr != nil {
		// A lost commit reply is resolved only by replaying the same receipt and
		// exact state. No new receipt or generic self-head success is accepted.
		result, err = a.activateRoute(ctx, s.pinned, s.pins.claim, input)
		if err != nil {
			return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, e.resolveActivationFailure(ctx,
				errors.Join(activateErr, err))
		}
	}
	self := candidateHeadSnapshot{appID: result.head.AppID, deploymentID: result.head.DeploymentID,
		releaseID: result.head.ReleaseID, slot: result.head.Slot, generation: result.head.Generation,
		activationReceiptID: result.head.ActivationReceiptID}
	if _, err := a.candidates.heads.bindSelf(current.headPins, self); err != nil {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, err
	}
	deployment, err := a.runtime.Get(ctx, e.request.AppID, s.pins.deploymentID)
	if err != nil {
		return generatedruntimestate.Deployment{}, generatedRouteActivationResult{}, err
	}
	return deployment, result, nil
}

// resolveActivationFailure restores a predecessor route only after a fresh
// database observation proves that the fixed activation transaction did not
// commit. A self, crossed, or unreadable head is left untouched because the
// physical route may already be backed by a durable activation receipt.
func (e *GeneratedRouteExecution) resolveActivationFailure(ctx context.Context, activationErr error) error {
	a := e.authority
	observeCtx := context.WithoutCancel(ctx)
	head, err := a.runtime.Active(observeCtx, e.request.AppID)
	if err != nil || !sameActivationHeadForRouteAuthority(head, e.expectedHead) || !e.hadPrevious {
		return errors.Join(activationErr, err)
	}
	restore := generatedroute.RouteSwitchRequest{AppID: e.request.AppID,
		FromSlot: e.request.ToSlot, ToSlot: e.previous.Slot,
		Endpoints:   append([]generatedroute.RouteEndpoint(nil), e.previous.Endpoints...),
		DrainPeriod: e.request.DrainPeriod}
	if err := a.manager.switchGeneratedDomainRoute(observeCtx, restore); err != nil {
		return errors.Join(activationErr, err)
	}
	observed, err := a.manager.Observe(observeCtx, e.request.AppID)
	if err != nil || !sameRoute(e.previous, routeRecord{Slot: observed.Slot, Endpoints: observed.Endpoints}) {
		return errors.Join(activationErr, deploymentexecution.ErrIdentityLost, err)
	}
	return activationErr
}

func sameActivationHeadForRouteAuthority(left, right generatedruntimestate.ActiveHead) bool {
	return left.AppID == right.AppID && left.DeploymentID == right.DeploymentID &&
		left.ReleaseID == right.ReleaseID && left.Slot == right.Slot &&
		left.Generation == right.Generation && left.ActivationReceiptID == right.ActivationReceiptID
}

func routeRequestMatchesCandidateIdentities(request generatedroute.RouteSwitchRequest, identities []CandidateIdentity) bool {
	want := make([]generatedroute.RouteEndpoint, 0, len(identities))
	for _, identity := range identities {
		if identity.Slot != string(request.ToSlot) || identity.InternalPort == 0 {
			return false
		}
		want = append(want, generatedroute.RouteEndpoint{Component: identity.ComponentName, Role: identity.Role,
			ContainerID: identity.ContainerID, NetworkName: identity.NetworkName,
			NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort})
	}
	got := append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)
	sort.Slice(want, func(i, j int) bool { return want[i].Component < want[j].Component })
	sort.Slice(got, func(i, j int) bool { return got[i].Component < got[j].Component })
	return reflect.DeepEqual(got, want)
}

func routeRosterMatchesCandidateSnapshot(roster []generatedruntimestate.RouteActivationComponent,
	snapshot candidateAdmissionSnapshot,
) bool {
	if len(roster) != len(snapshot.components) || len(roster) == 0 {
		return false
	}
	for index, member := range snapshot.components {
		if member.receipt == nil || member.artifact == nil || index >= len(roster) {
			return false
		}
		value := roster[index]
		if value.DeploymentID != snapshot.headPins.deploymentID || value.ComponentName != member.plan.Name ||
			value.Slot != snapshot.headPins.slot || value.ImageArtifactID != member.artifact.id ||
			value.ContainerName != member.containerName || value.ContainerID != member.containerID ||
			value.CandidateReceiptID != member.receipt.id || value.CandidatePolicyDigest != member.receipt.policyDigest {
			return false
		}
	}
	return true
}

func canonicalGeneratedRouteRequest(request generatedroute.RouteSwitchRequest) generatedroute.RouteSwitchRequest {
	result := request
	result.Endpoints = append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)
	sort.Slice(result.Endpoints, func(i, j int) bool { return result.Endpoints[i].Component < result.Endpoints[j].Component })
	return result
}

func marshalCanonicalGeneratedRouteDocument(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}
