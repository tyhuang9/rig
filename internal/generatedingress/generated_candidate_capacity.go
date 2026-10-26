package generatedingress

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/hostd/hostd/internal/generatedcapacity"
)

var (
	errCandidateCapacityInvalid     = errors.New("generated candidate capacity request is invalid")
	errCandidateCapacityPinsChanged = errors.New("generated candidate capacity pins changed")
	errCandidateCapacityBusy        = errors.New("generated candidate capacity operation is busy")
	errCandidateCapacityClosing     = errors.New("generated candidate capacity reservation is closing")
	errCandidateCapacityStale       = errors.New("generated candidate capacity binding is stale")
	errCandidateCleanupClosed       = errors.New("generated candidate cleanup is closed")
	errCandidateReceiptMismatch     = errors.New("generated candidate receipt does not match")
	errCandidateCommandEntered      = errors.New("generated candidate command was already entered")
	errCandidateMutationUnquiesced  = errors.New("generated candidate mutation is not quiesced")
	errCandidatePhysicalTransition  = errors.New("generated candidate physical transition is invalid")
	errCandidateConfirmedAbsent     = errors.New("generated candidate is confirmed absent")
	errCandidateOperationKind       = errors.New("generated candidate operation token has the wrong kind")
)

// candidateCapacityLedger is the one process-local candidate accounting
// registry associated with an exact raw host budget. Its groups are never
// removed: receipt, command, cleanup and quiescence tombstones must outlive
// every replaceable raw-charge epoch.
type candidateCapacityLedger struct {
	budget      *generatedcapacity.Budget
	coordinator *generatedDomainCapacityCoordinator

	mu     sync.Mutex
	groups map[candidateCapacityGroupKey]*candidateCapacityGroup
}

type candidateCapacityGroupKey struct {
	appID        string
	deploymentID string
}

type candidateGroupSpec struct {
	appID        string
	deploymentID string
	pins         candidateImmutablePins
	components   []string
	limits       generatedcapacity.Limits
}

type candidateImmutablePins struct {
	jobID                        string
	releaseID                    string
	configurationRevisionID      string
	configurationRevisionNumber  int64
	deploymentPlanRevisionID     string
	deploymentPlanRevisionNumber int64
	candidateSlot                string
	previousDeploymentID         string
	previousSlot                 string
}

type candidateCapacityGroup struct {
	mu      sync.Mutex
	changed *sync.Cond
	ledger  *candidateCapacityLedger
	key     candidateCapacityGroupKey

	pins       candidateImmutablePins
	components []string
	limits     generatedcapacity.Limits
	byName     map[string]*candidateCapacityComponent

	reserving bool
	nextEpoch uint64

	cleanupClosed bool
	activeCleanup int
}

type candidateCapacityComponent struct {
	name string

	charge  *candidateCapacityCharge
	receipt *candidateReceiptTombstone

	tokenHeld       bool
	tokenGeneration uint64
}

type candidateChargeState uint8

const (
	candidateChargeActive candidateChargeState = iota
	candidateChargeRetiring
	candidateChargeReleased
)

type candidateAccountingOwner uint8

const (
	candidateAccountingCandidate candidateAccountingOwner = iota
	candidateAccountingTransferring
	candidateAccountingRouteActive
	candidateAccountingPendingPublication
	candidateAccountingPredecessorRetiring
	candidateAccountingPredecessorRetired
)

type candidateCapacityCharge struct {
	epoch uint64
	token candidateCapacityChargeToken
	// lease remains the exact raw-token projection used by existing internal
	// accounting proofs. Restored debt never populates it.
	lease *generatedcapacity.Lease
	refs  int
	state candidateChargeState
	owner candidateAccountingOwner

	receiptRetained  bool
	explicitRetained bool
	logicallyRetired bool
}

type candidateCapacityChargeToken interface {
	configured() bool
	published() bool
	release()
}

type candidateRawCapacityChargeToken struct {
	lease *generatedcapacity.Lease
}

func (t *candidateRawCapacityChargeToken) configured() bool { return t != nil && t.lease != nil }
func (t *candidateRawCapacityChargeToken) published() bool  { return t.configured() }
func (t *candidateRawCapacityChargeToken) release() {
	if t != nil && t.lease != nil {
		t.lease.Release()
		t.lease = nil
	}
}

func candidateCapacityChargeTokenConfigured(token candidateCapacityChargeToken) bool {
	return token != nil && token.configured()
}

func candidateCapacityChargeTokenPublished(token candidateCapacityChargeToken) bool {
	return token != nil && token.published()
}

type candidateCapacityReservation struct {
	ledger *candidateCapacityLedger
	group  *candidateCapacityGroup

	mu         sync.Mutex
	changed    *sync.Cond
	closing    bool
	released   bool
	releaseErr error
	active     int

	bindings map[string]*candidateCapacityBinding
}

type candidateCapacityBinding struct {
	component *candidateCapacityComponent
	charge    *candidateCapacityCharge
	epoch     uint64
	attached  bool

	creatorReceipt    *candidateReceiptTombstone
	knownNeverEntered atomic.Bool
}

type candidateReceiptCommit uint8

const (
	candidateReceiptCommitAcknowledged candidateReceiptCommit = iota + 1
	candidateReceiptCommitUnknown
)

type candidatePhysicalState uint8

const (
	candidatePhysicalUnused candidatePhysicalState = iota
	candidatePhysicalAttemptedUnknown
	candidatePhysicalPresent
	candidatePhysicalConfirmedAbsent
	candidatePhysicalHandedToRoute
)

type candidateCommand uint8

const (
	candidateCommandCreate candidateCommand = iota + 1
	candidateCommandStart
	candidateCommandStop
	candidateCommandRemove
)

type candidateOperationMode uint8

const (
	candidateOperationObserve candidateOperationMode = iota + 1
	candidateOperationMutate
)

const (
	candidateReceiptPhysicalMask uint64 = 0x7

	candidateReceiptAcknowledged uint64 = 1 << 3
	candidateReceiptUnknown      uint64 = 1 << 4
	candidateReceiptObserved     uint64 = 1 << 5
	candidateReceiptCreate       uint64 = 1 << 6
	candidateReceiptStart        uint64 = 1 << 7
	candidateReceiptStop         uint64 = 1 << 8
	candidateReceiptRemove       uint64 = 1 << 9
	candidateReceiptUnquiesced   uint64 = 1 << 10
)

type candidateReceiptTombstone struct {
	id    string
	state atomic.Uint64
}

// candidateColdReceiptSeed is private reconstruction evidence. It installs no
// command token and grants no cleanup or release authority.
type candidateColdReceiptSeed struct {
	id       string
	physical candidatePhysicalState
	unknown  bool
}

type candidateOperationLifecycle struct {
	done atomic.Bool
}

type candidateComponentOperation struct {
	reservation *candidateCapacityReservation
	group       *candidateCapacityGroup
	binding     *candidateCapacityBinding
	component   *candidateCapacityComponent
	receipt     *candidateReceiptTombstone
	lifecycle   *candidateOperationLifecycle

	tokenGeneration uint64
	mode            candidateOperationMode
	attachment      bool
	cleanup         bool
}

type candidateRosterToken struct {
	component  *candidateCapacityComponent
	generation uint64
}

type candidateRosterOperation struct {
	reservation *candidateCapacityReservation
	group       *candidateCapacityGroup
	lifecycle   *candidateOperationLifecycle
	tokens      []candidateRosterToken
}

// candidateRetirementRosterOperation holds the complete fixed predecessor
// roster. It retains all raw charges and never performs the later accounting
// handoff or release.
type candidateRetirementRosterOperation struct {
	group     *candidateCapacityGroup
	lifecycle *candidateOperationLifecycle
	tokens    []candidateRosterToken
	byName    map[string]*candidateCapacityComponent
}

type candidateAccountingHandoffEntry struct {
	component *candidateCapacityComponent
	charge    *candidateCapacityCharge
	receipt   *candidateReceiptTombstone
	epoch     uint64
	token     candidateCapacityChargeToken
	lease     *generatedcapacity.Lease
	release   candidateAccountingReleaseState
}

type candidateAccountingReleaseState uint8

const (
	candidateAccountingReleasePending candidateAccountingReleaseState = iota
	candidateAccountingReleaseStarted
	candidateAccountingReleaseComplete
)

// candidateAccountingHandoffOperation owns no release authority. Unit 2B
// records only the complete in-process ownership transfer while every exact
// raw lease remains live. The later opaque Unit 2C operation owns gateway
// unlock, predecessor once-release and terminal durable readback.
type candidateAccountingHandoffOperation struct {
	mu sync.Mutex

	ledger           *candidateCapacityLedger
	successorGroup   *candidateCapacityGroup
	predecessorGroup *candidateCapacityGroup
	successor        []candidateAccountingHandoffEntry
	predecessor      []candidateAccountingHandoffEntry
	gatewayUnlocked  bool
	closed           bool
}

// candidateReceiptSnapshot is read-only evidence. It exposes no transition or
// release capability and its fields remain private to the concrete owner.
type candidateReceiptSnapshot struct {
	receiptID string
	physical  candidatePhysicalState

	commitAcknowledged bool
	commitUnknown      bool
	observed           bool
	createEntered      bool
	startEntered       bool
	stopEntered        bool
	removeEntered      bool
	mutationUnquiesced bool

	chargeEpoch        uint64
	chargeState        candidateChargeState
	accountingOwner    candidateAccountingOwner
	accountingRetained bool
	receiptRetained    bool
	explicitRetained   bool
	logicallyRetired   bool
}

type candidateRetiringCharge struct {
	component *candidateCapacityComponent
	charge    *candidateCapacityCharge
	token     candidateCapacityChargeToken
}

// candidateRecoveredCompletionHold pins the complete restored RouteActive
// roster while a cold-only completion transaction is in flight. It owns no
// capacity and cannot retire, release, or replace a charge.
type candidateRecoveredCompletionHold struct {
	group       *candidateCapacityGroup
	coordinator *generatedDomainCapacityCoordinator
	spec        candidateGroupSpec
	receipts    map[string]string
	tokens      []candidateRosterToken
	entries     []candidateAccountingHandoffEntry
	lifecycle   *candidateOperationLifecycle
}

func newCandidateCapacityLedger(budget *generatedcapacity.Budget,
	coordinators ...*generatedDomainCapacityCoordinator,
) (*candidateCapacityLedger, error) {
	if budget == nil {
		return nil, errCandidateCapacityInvalid
	}
	var coordinator *generatedDomainCapacityCoordinator
	if len(coordinators) > 1 {
		return nil, errCandidateCapacityInvalid
	}
	if len(coordinators) == 1 {
		coordinator = coordinators[0]
	}
	return &candidateCapacityLedger{
		budget: budget, coordinator: coordinator,
		groups: make(map[candidateCapacityGroupKey]*candidateCapacityGroup),
	}, nil
}

func (l *candidateCapacityLedger) reserve(
	ctx context.Context,
	spec candidateGroupSpec,
) (*candidateCapacityReservation, error) {
	if l == nil || l.budget == nil {
		return nil, errCandidateCapacityInvalid
	}
	normalized, err := normalizeCandidateGroupSpec(spec)
	if err != nil {
		return nil, err
	}
	key := candidateCapacityGroupKey{appID: normalized.appID, deploymentID: normalized.deploymentID}

	l.mu.Lock()
	group := l.groups[key]
	if group == nil {
		group = newCandidateCapacityGroup(l, key, normalized)
		l.groups[key] = group
	}
	l.mu.Unlock()

	return group.reserve(ctx, l, normalized)
}

func (l *candidateCapacityLedger) enterRetirementRoster(appID, deploymentID string,
	receipts map[string]string,
) (*candidateRetirementRosterOperation, error) {
	if l == nil || l.budget == nil || appID == "" || deploymentID == "" || len(receipts) == 0 {
		return nil, errCandidateCapacityInvalid
	}
	l.mu.Lock()
	group := l.groups[candidateCapacityGroupKey{appID: appID, deploymentID: deploymentID}]
	l.mu.Unlock()
	if group == nil {
		return nil, errCandidateCapacityStale
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.reserving || len(receipts) != len(group.components) {
		return nil, errCandidateCapacityBusy
	}
	for _, name := range group.components {
		component := group.byName[name]
		if component == nil || component.charge == nil || component.charge.state != candidateChargeActive ||
			!candidateAccountingOwnerCanBecomePredecessor(component.charge.owner) ||
			!candidateCapacityChargeTokenPublished(component.charge.token) ||
			component.charge.logicallyRetired || component.receipt == nil || component.receipt.id != receipts[name] {
			return nil, errCandidateReceiptMismatch
		}
		word := component.receipt.state.Load()
		if word&candidateReceiptUnquiesced != 0 {
			return nil, errCandidateMutationUnquiesced
		}
		physical := candidatePhysicalFromWord(word)
		if physical != candidatePhysicalPresent && physical != candidatePhysicalHandedToRoute &&
			physical != candidatePhysicalConfirmedAbsent {
			return nil, errCandidatePhysicalTransition
		}
		if component.tokenHeld {
			return nil, errCandidateCapacityBusy
		}
	}
	operation := &candidateRetirementRosterOperation{
		group: group, lifecycle: &candidateOperationLifecycle{},
		tokens: make([]candidateRosterToken, 0, len(group.components)),
		byName: make(map[string]*candidateCapacityComponent, len(group.components)),
	}
	for _, name := range group.components {
		component := group.byName[name]
		component.tokenHeld = true
		component.tokenGeneration++
		if component.tokenGeneration == 0 {
			component.tokenGeneration++
		}
		operation.tokens = append(operation.tokens, candidateRosterToken{component: component, generation: component.tokenGeneration})
		operation.byName[name] = component
	}
	return operation, nil
}

func (l *candidateCapacityLedger) holdRecoveredCompletionRoster(spec candidateGroupSpec,
	receipts map[string]string,
) (*candidateRecoveredCompletionHold, error) {
	if l == nil || l.budget == nil || l.coordinator == nil {
		return nil, errCandidateCapacityInvalid
	}
	normalized, err := normalizeCandidateGroupSpec(spec)
	if err != nil || len(receipts) != len(normalized.components) {
		return nil, errCandidateCapacityInvalid
	}
	key := candidateCapacityGroupKey{appID: normalized.appID, deploymentID: normalized.deploymentID}
	l.mu.Lock()
	group := l.groups[key]
	l.mu.Unlock()
	if group == nil {
		return nil, errCandidateCapacityStale
	}
	hold := &candidateRecoveredCompletionHold{
		group: group, coordinator: l.coordinator, spec: normalized,
		receipts:  make(map[string]string, len(receipts)),
		tokens:    make([]candidateRosterToken, 0, len(normalized.components)),
		entries:   make([]candidateAccountingHandoffEntry, 0, len(normalized.components)),
		lifecycle: &candidateOperationLifecycle{},
	}
	group.mu.Lock()
	if group.reserving || group.pins != normalized.pins || group.limits != normalized.limits ||
		!equalCandidateRoster(group.components, normalized.components) {
		group.mu.Unlock()
		return nil, errCandidateCapacityPinsChanged
	}
	for _, name := range group.components {
		component := group.byName[name]
		receiptID := receipts[name]
		if component == nil || receiptID == "" || component.receipt == nil || component.receipt.id != receiptID ||
			component.charge == nil || component.charge.state != candidateChargeActive ||
			component.charge.owner != candidateAccountingRouteActive || !component.charge.receiptRetained ||
			component.charge.explicitRetained ||
			component.charge.logicallyRetired || component.tokenHeld {
			group.mu.Unlock()
			return nil, errCandidateCapacityStale
		}
		word := component.receipt.state.Load()
		debt, restored := component.charge.token.(*generatedDomainCapacityDebtToken)
		if !restored || debt == nil || debt.owner != l.coordinator ||
			!candidateCapacityChargeTokenPublished(component.charge.token) ||
			word&candidateReceiptUnquiesced != 0 ||
			candidatePhysicalFromWord(word) != candidatePhysicalHandedToRoute {
			group.mu.Unlock()
			return nil, errCandidateCapacityStale
		}
	}
	for _, name := range group.components {
		component := group.byName[name]
		component.tokenHeld = true
		component.tokenGeneration++
		if component.tokenGeneration == 0 {
			component.tokenGeneration++
		}
		hold.receipts[name] = receipts[name]
		hold.tokens = append(hold.tokens, candidateRosterToken{
			component: component, generation: component.tokenGeneration,
		})
		hold.entries = append(hold.entries, candidateAccountingHandoffEntry{
			component: component, charge: component.charge, receipt: component.receipt,
			epoch: component.charge.epoch, token: component.charge.token, lease: component.charge.lease,
		})
	}
	group.mu.Unlock()
	if !hold.valid() {
		hold.leave()
		return nil, errCandidateCapacityStale
	}
	return hold, nil
}

func (h *candidateRecoveredCompletionHold) valid() bool {
	if h == nil || h.lifecycle == nil || h.lifecycle.done.Load() || h.group == nil || h.coordinator == nil ||
		len(h.tokens) == 0 || len(h.tokens) != len(h.entries) || len(h.tokens) != len(h.receipts) {
		return false
	}
	h.group.mu.Lock()
	if h.group.reserving || h.group.pins != h.spec.pins || h.group.limits != h.spec.limits ||
		!equalCandidateRoster(h.group.components, h.spec.components) {
		h.group.mu.Unlock()
		return false
	}
	tokens, ok := candidateAccountingTokensLocked(h.group, h.tokens)
	if !ok || len(tokens) != len(h.entries) {
		h.group.mu.Unlock()
		return false
	}
	debts := make([]*generatedDomainCapacityDebtToken, 0, len(h.entries))
	for _, entry := range h.entries {
		if entry.component == nil || entry.receipt == nil {
			h.group.mu.Unlock()
			return false
		}
		debt, restored := entry.token.(*generatedDomainCapacityDebtToken)
		if !restored || debt == nil || debt.owner != h.coordinator ||
			h.receipts[entry.component.name] != entry.receipt.id ||
			!validCandidateRecoveredCompletionEntryLocked(entry, h.group) {
			h.group.mu.Unlock()
			return false
		}
		debts = append(debts, debt)
	}
	h.group.mu.Unlock()

	h.coordinator.mu.Lock()
	defer h.coordinator.mu.Unlock()
	if h.coordinator.phase != generatedDomainCapacityPublished {
		return false
	}
	seen := make(map[*generatedDomainCapacityDebtToken]struct{}, len(debts))
	for _, debt := range debts {
		if debt.owner != h.coordinator || !debt.active || debt.limits != h.spec.limits {
			return false
		}
		if _, found := h.coordinator.tokens[debt]; !found {
			return false
		}
		if _, duplicate := seen[debt]; duplicate {
			return false
		}
		seen[debt] = struct{}{}
	}
	return true
}

func validCandidateRecoveredCompletionEntryLocked(entry candidateAccountingHandoffEntry,
	group *candidateCapacityGroup,
) bool {
	if group == nil || entry.component == nil || group.byName[entry.component.name] != entry.component ||
		entry.component.charge != entry.charge || entry.charge == nil || entry.charge.epoch != entry.epoch ||
		entry.charge.token != entry.token || !candidateCapacityChargeTokenPublished(entry.token) ||
		!candidateAccountingTokenLeaseMatches(entry.token, entry.charge.lease, entry.lease, true) ||
		entry.charge.state != candidateChargeActive ||
		entry.charge.owner != candidateAccountingRouteActive || !entry.charge.receiptRetained ||
		entry.charge.explicitRetained || entry.charge.logicallyRetired ||
		entry.component.receipt != entry.receipt || entry.receipt == nil {
		return false
	}
	word := entry.receipt.state.Load()
	return word&candidateReceiptUnquiesced == 0 &&
		candidatePhysicalFromWord(word) == candidatePhysicalHandedToRoute
}

func (h *candidateRecoveredCompletionHold) leave() {
	if h == nil || h.lifecycle == nil || h.lifecycle.done.Swap(true) || h.group == nil {
		return
	}
	h.group.mu.Lock()
	for _, token := range h.tokens {
		if token.component != nil && token.component.tokenHeld &&
			token.component.tokenGeneration == token.generation {
			token.component.tokenHeld = false
		}
	}
	h.group.changed.Broadcast()
	h.group.mu.Unlock()
}

func candidateAccountingOwnerCanBecomePredecessor(owner candidateAccountingOwner) bool {
	return owner == candidateAccountingCandidate || owner == candidateAccountingRouteActive ||
		owner == candidateAccountingPendingPublication
}

func (o *candidateRetirementRosterOperation) enterCommand(componentName string, command candidateCommand) error {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() ||
		(command != candidateCommandStop && command != candidateCommandRemove) {
		return errCandidateCapacityInvalid
	}
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	component := o.byName[componentName]
	if !o.validComponentLocked(component) || component.receipt == nil {
		return errCandidateCapacityStale
	}
	marker, _ := candidateCommandMarker(command)
	for {
		word := component.receipt.state.Load()
		if word&candidateReceiptUnquiesced != 0 {
			return errCandidateMutationUnquiesced
		}
		if word&marker != 0 {
			return errCandidateCommandEntered
		}
		physical := candidatePhysicalFromWord(word)
		if physical != candidatePhysicalPresent && physical != candidatePhysicalHandedToRoute {
			return errCandidatePhysicalTransition
		}
		if component.receipt.state.CompareAndSwap(word, word|marker) {
			return nil
		}
	}
}

func (o *candidateRetirementRosterOperation) markConfirmedAbsentRetained(componentName string) error {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() {
		return errCandidateCapacityStale
	}
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	component := o.byName[componentName]
	if !o.validComponentLocked(component) || component.receipt == nil || component.charge == nil {
		return errCandidateCapacityStale
	}
	if component.receipt.state.Load()&candidateReceiptUnquiesced != 0 {
		return errCandidateMutationUnquiesced
	}
	if err := candidateTransitionPhysical(&component.receipt.state, candidatePhysicalConfirmedAbsent,
		candidatePhysicalPresent, candidatePhysicalHandedToRoute, candidatePhysicalConfirmedAbsent); err != nil {
		return err
	}
	component.charge.explicitRetained = true
	return nil
}

func (o *candidateRetirementRosterOperation) latchMutationUnquiesced(componentName string) {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() {
		return
	}
	o.group.mu.Lock()
	defer o.group.mu.Unlock()
	component := o.byName[componentName]
	if !o.validComponentLocked(component) || component.receipt == nil || component.charge == nil {
		return
	}
	word := component.receipt.state.Load()
	if word&(candidateReceiptStop|candidateReceiptRemove) == 0 {
		return
	}
	candidateAtomicSet(&component.receipt.state, candidateReceiptUnquiesced)
	component.charge.explicitRetained = true
}

func (o *candidateRetirementRosterOperation) validComponentLocked(component *candidateCapacityComponent) bool {
	if component == nil || component.charge == nil || component.charge.state != candidateChargeActive ||
		!candidateAccountingOwnerCanBecomePredecessor(component.charge.owner) || component.charge.logicallyRetired {
		return false
	}
	for _, token := range o.tokens {
		if token.component == component {
			return component.tokenHeld && component.tokenGeneration == token.generation
		}
	}
	return false
}

func (o *candidateRetirementRosterOperation) leave() {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Swap(true) {
		return
	}
	o.group.mu.Lock()
	for _, token := range o.tokens {
		if token.component.tokenHeld && token.component.tokenGeneration == token.generation {
			token.component.tokenHeld = false
		}
	}
	o.group.changed.Broadcast()
	o.group.mu.Unlock()
}

// beginAccountingHandoff changes only private process-local ownership. Both
// complete rosters are validated before any member changes, and every raw
// lease remains held. A nil predecessor is the exact empty-predecessor case.
func (l *candidateCapacityLedger) beginAccountingHandoff(
	successor *candidateRosterOperation,
	predecessor *candidateRetirementRosterOperation,
) (*candidateAccountingHandoffOperation, error) {
	if l == nil || l.budget == nil || successor == nil || successor.group == nil ||
		successor.reservation == nil || successor.reservation.ledger != l ||
		successor.group.ledger != l || successor.lifecycle == nil || successor.lifecycle.done.Load() {
		return nil, errCandidateCapacityInvalid
	}
	var predecessorGroup *candidateCapacityGroup
	if predecessor != nil {
		predecessorGroup = predecessor.group
		if predecessorGroup == nil || predecessorGroup.ledger != l || predecessor.lifecycle == nil ||
			predecessor.lifecycle.done.Load() || predecessorGroup == successor.group {
			return nil, errCandidateCapacityInvalid
		}
	}

	unlock := lockCandidateAccountingGroups(successor.group, predecessorGroup)
	defer unlock()
	successorEntries, err := validateCandidateAccountingSuccessorLocked(successor)
	if err != nil {
		return nil, err
	}
	predecessorEntries, err := validateCandidateAccountingPredecessorLocked(predecessor)
	if err != nil {
		return nil, err
	}

	// The complete successor batch is detached from this reservation while
	// both group locks are held. Other aliases may detach later, but their
	// release path cannot retire a transferring or route-active charge.
	for _, entry := range successorEntries {
		binding := successor.reservation.bindings[entry.component.name]
		binding.charge.refs--
		binding.attached = false
		entry.charge.explicitRetained = true
		entry.charge.owner = candidateAccountingTransferring
	}
	for _, entry := range predecessorEntries {
		entry.charge.explicitRetained = true
		entry.charge.owner = candidateAccountingPredecessorRetiring
	}
	return &candidateAccountingHandoffOperation{
		ledger:           l,
		successorGroup:   successor.group,
		predecessorGroup: predecessorGroup,
		successor:        successorEntries,
		predecessor:      predecessorEntries,
	}, nil
}

// markGatewayUnlocked is called only by the later opaque handoff owner after
// its gateway unlock succeeds. It promotes the entire exact successor roster
// to route-active ownership without releasing any lease. Predecessor leases
// remain live and predecessor-retiring for Unit 2C's finite once-release.
func (o *candidateAccountingHandoffOperation) markGatewayUnlocked() error {
	if o == nil || o.ledger == nil || o.successorGroup == nil || len(o.successor) == 0 {
		return errCandidateCapacityInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errCandidateCapacityStale
	}
	unlock := lockCandidateAccountingGroups(o.successorGroup, o.predecessorGroup)
	defer unlock()

	expectedSuccessorOwner := candidateAccountingTransferring
	if o.gatewayUnlocked {
		expectedSuccessorOwner = candidateAccountingRouteActive
	}
	for _, entry := range o.successor {
		if !validCandidateAccountingEntryLocked(entry, o.successorGroup, expectedSuccessorOwner,
			candidatePhysicalHandedToRoute) {
			return errCandidateCapacityStale
		}
	}
	for _, entry := range o.predecessor {
		if !validCandidateAccountingEntryLocked(entry, o.predecessorGroup,
			candidateAccountingPredecessorRetiring, candidatePhysicalConfirmedAbsent) {
			return errCandidateCapacityStale
		}
	}
	if o.gatewayUnlocked {
		return nil
	}
	for _, entry := range o.successor {
		entry.charge.owner = candidateAccountingRouteActive
	}
	o.gatewayUnlocked = true
	return nil
}

// releasePredecessorOnce returns only the fixed predecessor shares after the
// owning route operation has proved and released its first gateway exclusion.
// Progress is retained on each exact entry so a retry can never infer release
// from a nil lease or invoke the opaque raw lease twice.
func (o *candidateAccountingHandoffOperation) releasePredecessorOnce() error {
	if o == nil || o.ledger == nil || o.successorGroup == nil || len(o.successor) == 0 {
		return errCandidateCapacityInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.gatewayUnlocked {
		return errCandidateCapacityStale
	}

	unlock := lockCandidateAccountingGroups(o.successorGroup, o.predecessorGroup)
	for _, entry := range o.successor {
		if !validCandidateAccountingEntryLocked(entry, o.successorGroup,
			candidateAccountingRouteActive, candidatePhysicalHandedToRoute) {
			unlock()
			o.closed = true
			return errCandidateCapacityStale
		}
	}
	pending := make([]int, 0, len(o.predecessor))
	for index := range o.predecessor {
		entry := &o.predecessor[index]
		switch entry.release {
		case candidateAccountingReleasePending:
			if !validCandidateAccountingEntryLocked(*entry, o.predecessorGroup,
				candidateAccountingPredecessorRetiring, candidatePhysicalConfirmedAbsent) {
				unlock()
				o.closed = true
				return errCandidateCapacityStale
			}
			entry.release = candidateAccountingReleaseStarted
			pending = append(pending, index)
		case candidateAccountingReleaseComplete:
			if !validCandidateAccountingReleasedEntryLocked(*entry, o.predecessorGroup) {
				unlock()
				o.closed = true
				return errCandidateCapacityStale
			}
		default:
			unlock()
			o.closed = true
			return errCandidateCapacityStale
		}
	}
	unlock()

	for _, index := range pending {
		entry := &o.predecessor[index]
		entry.token.release()
		o.predecessorGroup.mu.Lock()
		if !validCandidateAccountingEntryAfterTokenReleaseLocked(*entry, o.predecessorGroup,
			candidateAccountingPredecessorRetiring, candidatePhysicalConfirmedAbsent) {
			o.predecessorGroup.mu.Unlock()
			o.closed = true
			return errCandidateCapacityStale
		}
		entry.charge.state = candidateChargeReleased
		entry.charge.owner = candidateAccountingPredecessorRetired
		entry.charge.refs = 0
		entry.charge.logicallyRetired = true
		entry.charge.token = nil
		entry.charge.lease = nil
		entry.release = candidateAccountingReleaseComplete
		o.predecessorGroup.changed.Broadcast()
		o.predecessorGroup.mu.Unlock()
	}
	return nil
}

// validateFinalState proves the exact terminal accounting projection without
// acquiring capacity or deriving authority from tombstones.
func (o *candidateAccountingHandoffOperation) validateFinalState() error {
	if o == nil || o.ledger == nil || o.successorGroup == nil || len(o.successor) == 0 {
		return errCandidateCapacityInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.gatewayUnlocked {
		return errCandidateCapacityStale
	}
	unlock := lockCandidateAccountingGroups(o.successorGroup, o.predecessorGroup)
	defer unlock()
	for _, entry := range o.successor {
		if !validCandidateAccountingEntryLocked(entry, o.successorGroup,
			candidateAccountingRouteActive, candidatePhysicalHandedToRoute) {
			return errCandidateCapacityStale
		}
	}
	for _, entry := range o.predecessor {
		if entry.release != candidateAccountingReleaseComplete ||
			!validCandidateAccountingReleasedEntryLocked(entry, o.predecessorGroup) {
			return errCandidateCapacityStale
		}
	}
	return nil
}

func (o *candidateAccountingHandoffOperation) close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

func validateCandidateAccountingSuccessorLocked(
	operation *candidateRosterOperation,
) ([]candidateAccountingHandoffEntry, error) {
	group := operation.group
	if operation.lifecycle.done.Load() || operation.reservation.group != group || group.reserving ||
		len(operation.tokens) != len(group.components) || len(operation.reservation.bindings) != len(group.components) {
		return nil, errCandidateCapacityStale
	}
	tokens, ok := candidateAccountingTokensLocked(group, operation.tokens)
	if !ok {
		return nil, errCandidateCapacityStale
	}
	entries := make([]candidateAccountingHandoffEntry, 0, len(group.components))
	for _, name := range group.components {
		component := group.byName[name]
		binding := operation.reservation.bindings[name]
		_, found := tokens[component]
		if component == nil || !found || !validCandidateBindingLocked(binding) ||
			binding.component != component || !candidateCapacityChargeTokenPublished(binding.charge.token) ||
			binding.charge.owner != candidateAccountingCandidate || component.receipt == nil {
			return nil, errCandidateCapacityStale
		}
		word := component.receipt.state.Load()
		if word&candidateReceiptUnquiesced != 0 {
			return nil, errCandidateMutationUnquiesced
		}
		if candidatePhysicalFromWord(word) != candidatePhysicalHandedToRoute {
			return nil, errCandidatePhysicalTransition
		}
		entries = append(entries, candidateAccountingHandoffEntry{
			component: component, charge: binding.charge, receipt: component.receipt,
			epoch: binding.charge.epoch, token: binding.charge.token, lease: binding.charge.lease,
		})
	}
	return entries, nil
}

func validateCandidateAccountingPredecessorLocked(
	operation *candidateRetirementRosterOperation,
) ([]candidateAccountingHandoffEntry, error) {
	if operation == nil {
		return nil, nil
	}
	group := operation.group
	if operation.lifecycle.done.Load() || group.reserving || len(operation.tokens) != len(group.components) ||
		len(operation.byName) != len(group.components) {
		return nil, errCandidateCapacityStale
	}
	tokens, ok := candidateAccountingTokensLocked(group, operation.tokens)
	if !ok {
		return nil, errCandidateCapacityStale
	}
	entries := make([]candidateAccountingHandoffEntry, 0, len(group.components))
	for _, name := range group.components {
		component := group.byName[name]
		_, found := tokens[component]
		if component == nil || operation.byName[name] != component || !found || component.charge == nil ||
			component.charge.state != candidateChargeActive || !candidateCapacityChargeTokenPublished(component.charge.token) ||
			!candidateAccountingOwnerCanBecomePredecessor(component.charge.owner) ||
			component.charge.logicallyRetired || !component.charge.explicitRetained || component.receipt == nil {
			return nil, errCandidateCapacityStale
		}
		word := component.receipt.state.Load()
		if word&candidateReceiptUnquiesced != 0 {
			return nil, errCandidateMutationUnquiesced
		}
		if candidatePhysicalFromWord(word) != candidatePhysicalConfirmedAbsent {
			return nil, errCandidatePhysicalTransition
		}
		entries = append(entries, candidateAccountingHandoffEntry{
			component: component, charge: component.charge, receipt: component.receipt,
			epoch: component.charge.epoch, token: component.charge.token, lease: component.charge.lease,
		})
	}
	return entries, nil
}

func candidateAccountingTokensLocked(group *candidateCapacityGroup,
	tokens []candidateRosterToken,
) (map[*candidateCapacityComponent]uint64, bool) {
	values := make(map[*candidateCapacityComponent]uint64, len(tokens))
	for _, token := range tokens {
		if token.component == nil || group.byName[token.component.name] != token.component ||
			!token.component.tokenHeld || token.component.tokenGeneration != token.generation {
			return nil, false
		}
		if _, exists := values[token.component]; exists {
			return nil, false
		}
		values[token.component] = token.generation
	}
	return values, len(values) == len(group.components)
}

func validCandidateAccountingEntryLocked(entry candidateAccountingHandoffEntry,
	group *candidateCapacityGroup, owner candidateAccountingOwner, physical candidatePhysicalState,
) bool {
	if group == nil || entry.component == nil || group.byName[entry.component.name] != entry.component ||
		entry.component.charge != entry.charge || entry.charge == nil || entry.charge.epoch != entry.epoch ||
		entry.charge.token != entry.token || !candidateCapacityChargeTokenPublished(entry.token) ||
		!candidateAccountingTokenLeaseMatches(entry.token, entry.charge.lease, entry.lease, true) ||
		entry.charge.state != candidateChargeActive ||
		entry.charge.owner != owner || !entry.charge.explicitRetained || entry.charge.logicallyRetired ||
		entry.component.receipt != entry.receipt ||
		entry.receipt == nil {
		return false
	}
	word := entry.receipt.state.Load()
	return word&candidateReceiptUnquiesced == 0 && candidatePhysicalFromWord(word) == physical
}

func validCandidateAccountingEntryAfterTokenReleaseLocked(entry candidateAccountingHandoffEntry,
	group *candidateCapacityGroup, owner candidateAccountingOwner, physical candidatePhysicalState,
) bool {
	if group == nil || entry.component == nil || group.byName[entry.component.name] != entry.component ||
		entry.component.charge != entry.charge || entry.charge == nil || entry.charge.epoch != entry.epoch ||
		entry.charge.token != entry.token || entry.token == nil || candidateCapacityChargeTokenConfigured(entry.token) ||
		!candidateAccountingTokenLeaseMatches(entry.token, entry.charge.lease, entry.lease, false) ||
		entry.charge.state != candidateChargeActive || entry.charge.owner != owner ||
		!entry.charge.explicitRetained || entry.charge.logicallyRetired ||
		entry.component.receipt != entry.receipt || entry.receipt == nil {
		return false
	}
	word := entry.receipt.state.Load()
	return word&candidateReceiptUnquiesced == 0 && candidatePhysicalFromWord(word) == physical
}

func candidateAccountingTokenLeaseMatches(token candidateCapacityChargeToken, chargeLease,
	capturedLease *generatedcapacity.Lease, published bool,
) bool {
	switch value := token.(type) {
	case *candidateRawCapacityChargeToken:
		if value == nil || capturedLease == nil || chargeLease != capturedLease {
			return false
		}
		if published {
			return value.lease == capturedLease
		}
		return value.lease == nil
	case *generatedDomainCapacityDebtToken:
		if value == nil || capturedLease != nil || chargeLease != nil {
			return false
		}
		if published {
			return value.published()
		}
		return !value.configured()
	default:
		return false
	}
}

func validCandidateAccountingReleasedEntryLocked(entry candidateAccountingHandoffEntry,
	group *candidateCapacityGroup,
) bool {
	if group == nil || entry.component == nil || group.byName[entry.component.name] != entry.component ||
		entry.component.charge != entry.charge || entry.charge == nil || entry.charge.epoch != entry.epoch ||
		entry.token == nil || entry.charge.token != nil || entry.charge.state != candidateChargeReleased ||
		entry.charge.owner != candidateAccountingPredecessorRetired || !entry.charge.explicitRetained ||
		!entry.charge.logicallyRetired || entry.component.receipt != entry.receipt || entry.receipt == nil {
		return false
	}
	word := entry.receipt.state.Load()
	return word&candidateReceiptUnquiesced == 0 &&
		candidatePhysicalFromWord(word) == candidatePhysicalConfirmedAbsent
}

func lockCandidateAccountingGroups(successor, predecessor *candidateCapacityGroup) func() {
	if predecessor == nil {
		successor.mu.Lock()
		return successor.mu.Unlock
	}
	first, second := successor, predecessor
	if candidateCapacityGroupKeyLess(predecessor.key, successor.key) {
		first, second = predecessor, successor
	}
	first.mu.Lock()
	second.mu.Lock()
	return func() {
		second.mu.Unlock()
		first.mu.Unlock()
	}
}

func candidateCapacityGroupKeyLess(left, right candidateCapacityGroupKey) bool {
	return left.appID < right.appID || left.appID == right.appID && left.deploymentID < right.deploymentID
}

func normalizeCandidateGroupSpec(spec candidateGroupSpec) (candidateGroupSpec, error) {
	if spec.appID == "" || spec.deploymentID == "" || len(spec.components) == 0 ||
		spec.limits.MemoryBytes == 0 || spec.limits.DiskBytes == 0 {
		return candidateGroupSpec{}, errCandidateCapacityInvalid
	}
	components := append([]string(nil), spec.components...)
	sort.Strings(components)
	for index, component := range components {
		if component == "" || index > 0 && components[index-1] == component {
			return candidateGroupSpec{}, errCandidateCapacityInvalid
		}
	}
	spec.components = components
	return spec, nil
}

func newCandidateCapacityGroup(ledger *candidateCapacityLedger, key candidateCapacityGroupKey,
	spec candidateGroupSpec,
) *candidateCapacityGroup {
	group := &candidateCapacityGroup{
		ledger:     ledger,
		key:        key,
		pins:       spec.pins,
		components: append([]string(nil), spec.components...),
		limits:     spec.limits,
		byName:     make(map[string]*candidateCapacityComponent, len(spec.components)),
	}
	group.changed = sync.NewCond(&group.mu)
	for _, name := range group.components {
		group.byName[name] = &candidateCapacityComponent{name: name}
	}
	return group
}

// seedColdOwnership records exact immutable group and receipt identity after a
// complete startup read. It may populate a charge-free group for a quiescent or
// proved-absent deployment. Risky groups are reserved through reserve first;
// this method only makes their already-retained receipts exact.
func (l *candidateCapacityLedger) seedColdOwnership(spec candidateGroupSpec,
	receipts map[string]candidateColdReceiptSeed,
) error {
	if l == nil || l.budget == nil {
		return errCandidateCapacityInvalid
	}
	normalized, err := normalizeCandidateGroupSpec(spec)
	if err != nil {
		return err
	}
	key := candidateCapacityGroupKey{appID: normalized.appID, deploymentID: normalized.deploymentID}
	l.mu.Lock()
	group := l.groups[key]
	if group == nil {
		group = newCandidateCapacityGroup(l, key, normalized)
		l.groups[key] = group
	}
	l.mu.Unlock()

	group.mu.Lock()
	defer group.mu.Unlock()
	if group.reserving || group.pins != normalized.pins || group.limits != normalized.limits ||
		!equalCandidateRoster(group.components, normalized.components) || len(receipts) > len(group.components) {
		return errCandidateCapacityPinsChanged
	}
	for name, seed := range receipts {
		component := group.byName[name]
		if component == nil || seed.id == "" ||
			(seed.physical != candidatePhysicalAttemptedUnknown && seed.physical != candidatePhysicalPresent &&
				seed.physical != candidatePhysicalConfirmedAbsent && seed.physical != candidatePhysicalHandedToRoute) {
			return errCandidateCapacityInvalid
		}
		if component.receipt == nil {
			component.receipt = &candidateReceiptTombstone{id: seed.id}
		} else if component.receipt.id != seed.id {
			return errCandidateReceiptMismatch
		}
		word := uint64(seed.physical) | candidateReceiptAcknowledged | candidateReceiptObserved
		if seed.unknown {
			word |= candidateReceiptUnknown | candidateReceiptUnquiesced
		}
		component.receipt.state.Store(word)
		if component.charge != nil {
			if component.charge.state != candidateChargeActive || component.charge.logicallyRetired {
				return errCandidateCapacityStale
			}
			component.charge.receiptRetained = true
		}
	}
	return nil
}

// seedColdOwnershipWithDebt installs one complete authenticated Current roster
// while bootstrap capacity is still closed. The tokens are pending publication:
// no ordinary alias, retirement or cleanup path may consume them until their
// common coordinator is published.
func (l *candidateCapacityLedger) seedColdOwnershipWithDebt(spec candidateGroupSpec,
	receipts map[string]candidateColdReceiptSeed, tokens []*generatedDomainCapacityDebtToken,
) error {
	if l == nil || l.budget == nil || l.coordinator == nil {
		return errCandidateCapacityInvalid
	}
	normalized, err := normalizeCandidateGroupSpec(spec)
	if err != nil || len(receipts) != len(normalized.components) || len(tokens) != len(normalized.components) {
		return errCandidateCapacityInvalid
	}
	words := make([]uint64, len(normalized.components))
	seenTokens := make(map[*generatedDomainCapacityDebtToken]struct{}, len(tokens))
	for index, name := range normalized.components {
		seed, found := receipts[name]
		if !found || seed.id == "" || tokens[index] == nil || tokens[index].owner != l.coordinator ||
			!tokens[index].configured() ||
			(seed.physical != candidatePhysicalPresent && seed.physical != candidatePhysicalHandedToRoute) {
			return errCandidateCapacityInvalid
		}
		if _, duplicate := seenTokens[tokens[index]]; duplicate {
			return errCandidateCapacityInvalid
		}
		seenTokens[tokens[index]] = struct{}{}
		word := uint64(seed.physical) | candidateReceiptAcknowledged | candidateReceiptObserved
		if seed.unknown {
			return errCandidateCapacityInvalid
		}
		words[index] = word
	}
	key := candidateCapacityGroupKey{appID: normalized.appID, deploymentID: normalized.deploymentID}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.groups[key] != nil {
		return errCandidateCapacityPinsChanged
	}
	group := newCandidateCapacityGroup(l, key, normalized)
	for index, name := range normalized.components {
		seed := receipts[name]
		component := group.byName[name]
		group.nextEpoch++
		component.receipt = &candidateReceiptTombstone{id: seed.id}
		component.receipt.state.Store(words[index])
		component.charge = &candidateCapacityCharge{epoch: group.nextEpoch, token: tokens[index],
			state: candidateChargeActive, owner: candidateAccountingPendingPublication, receiptRetained: true}
	}
	l.groups[key] = group
	return nil
}

// abandonUnpublished detaches all raw tokens in a graph that registry state
// still proves unreachable. It returns no authority and performs token release
// only after dropping ledger and group locks.
func (l *candidateCapacityLedger) abandonUnpublished() {
	if l == nil {
		return
	}
	l.mu.Lock()
	groups := make([]*candidateCapacityGroup, 0, len(l.groups))
	for _, group := range l.groups {
		groups = append(groups, group)
	}
	l.mu.Unlock()
	var tokens []candidateCapacityChargeToken
	for _, group := range groups {
		group.mu.Lock()
		for _, component := range group.byName {
			if component == nil || component.charge == nil || component.charge.token == nil {
				continue
			}
			tokens = append(tokens, component.charge.token)
			component.charge.token = nil
			component.charge.lease = nil
			component.charge.state = candidateChargeReleased
			component.charge.refs = 0
			component.charge.logicallyRetired = true
		}
		group.changed.Broadcast()
		group.mu.Unlock()
	}
	for _, token := range tokens {
		token.release()
	}
}

// publishRestoredOwnership is called only while the registry still excludes
// every consumer of this construction. It first validates the complete
// pending graph, then publishes the shared coordinator and promotes the exact
// restored entries without reminting tokens.
func (l *candidateCapacityLedger) publishRestoredOwnership() error {
	if l == nil || l.coordinator == nil || !l.coordinator.readyToPublish() {
		return errCandidateCapacityInvalid
	}
	l.mu.Lock()
	groups := make([]*candidateCapacityGroup, 0, len(l.groups))
	for _, group := range l.groups {
		groups = append(groups, group)
	}
	l.mu.Unlock()
	type pendingCharge struct {
		group  *candidateCapacityGroup
		charge *candidateCapacityCharge
		token  *generatedDomainCapacityDebtToken
	}
	var pending []pendingCharge
	pendingTokens := make(map[*generatedDomainCapacityDebtToken]struct{})
	for _, group := range groups {
		group.mu.Lock()
		for _, component := range group.byName {
			if component == nil || component.charge == nil ||
				component.charge.owner != candidateAccountingPendingPublication {
				continue
			}
			charge := component.charge
			token, restored := charge.token.(*generatedDomainCapacityDebtToken)
			if charge.state != candidateChargeActive || charge.logicallyRetired || !restored ||
				token.owner != l.coordinator ||
				!candidateCapacityChargeTokenConfigured(charge.token) ||
				candidateCapacityChargeTokenPublished(charge.token) {
				group.mu.Unlock()
				return errCandidateCapacityInvalid
			}
			if _, duplicate := pendingTokens[token]; duplicate {
				group.mu.Unlock()
				return errCandidateCapacityInvalid
			}
			pendingTokens[token] = struct{}{}
			pending = append(pending, pendingCharge{group: group, charge: charge, token: token})
		}
		group.mu.Unlock()
	}
	l.coordinator.mu.Lock()
	expected := len(l.coordinator.tokens)
	exactTokens := len(pendingTokens) == expected
	if exactTokens {
		for token := range l.coordinator.tokens {
			if _, found := pendingTokens[token]; !found {
				exactTokens = false
				break
			}
		}
	}
	l.coordinator.mu.Unlock()
	if !exactTokens || len(pending) != expected || l.coordinator.publish() != nil {
		return errCandidateCapacityInvalid
	}
	for _, entry := range pending {
		entry.group.mu.Lock()
		if entry.charge.owner != candidateAccountingPendingPublication ||
			!candidateCapacityChargeTokenPublished(entry.charge.token) {
			entry.group.mu.Unlock()
			return errCandidateCapacityInvalid
		}
		entry.charge.owner = candidateAccountingRouteActive
		entry.group.mu.Unlock()
	}
	return nil
}

func (g *candidateCapacityGroup) reserve(
	ctx context.Context,
	ledger *candidateCapacityLedger,
	spec candidateGroupSpec,
) (*candidateCapacityReservation, error) {
	g.mu.Lock()
	if g.pins != spec.pins || g.limits != spec.limits || !equalCandidateRoster(g.components, spec.components) {
		g.mu.Unlock()
		return nil, errCandidateCapacityPinsChanged
	}
	if g.reserving {
		g.mu.Unlock()
		return nil, errCandidateCapacityBusy
	}

	missing := make([]*candidateCapacityComponent, 0, len(g.components))
	for _, name := range g.components {
		component := g.byName[name]
		if component.charge != nil && component.charge.owner != candidateAccountingCandidate {
			g.mu.Unlock()
			return nil, errCandidateCapacityStale
		}
		if component.charge != nil && component.charge.logicallyRetired {
			err := candidateComponentAllowsFreshCharge(component)
			if err == nil {
				err = errCandidateCapacityStale
			}
			g.mu.Unlock()
			return nil, err
		}
		switch {
		case component.charge == nil, component.charge.state == candidateChargeReleased:
			if err := candidateComponentAllowsFreshCharge(component); err != nil {
				g.mu.Unlock()
				return nil, err
			}
			missing = append(missing, component)
		case component.charge.state == candidateChargeRetiring:
			g.mu.Unlock()
			return nil, errCandidateCapacityBusy
		case component.charge.state != candidateChargeActive:
			g.mu.Unlock()
			return nil, errCandidateCapacityStale
		}
	}
	if len(missing) == 0 {
		reservation := g.attachReservationLocked(ledger)
		g.mu.Unlock()
		return reservation, nil
	}

	if uint64(len(missing)) > math.MaxUint64-g.nextEpoch {
		g.mu.Unlock()
		return nil, errCandidateCapacityInvalid
	}
	g.reserving = true
	g.mu.Unlock()

	var borrow *generatedDomainCapacityBorrow
	if ledger.coordinator != nil {
		var borrowErr error
		borrow, borrowErr = ledger.coordinator.borrowAcquire()
		if borrowErr != nil {
			g.mu.Lock()
			g.reserving = false
			g.changed.Broadcast()
			g.mu.Unlock()
			return nil, borrowErr
		}
		defer borrow.release()
	}
	leases, acquireErr := ledger.budget.Acquire(ctx, spec.limits, len(missing))
	if acquireErr != nil {
		releaseCandidateAdmissionLeases(leases)
		g.mu.Lock()
		g.reserving = false
		g.changed.Broadcast()
		g.mu.Unlock()
		return nil, acquireErr
	}

	g.mu.Lock()
	valid := g.reserving && len(leases) == len(missing)
	var driftErr error
	if valid {
		for _, component := range missing {
			if component.charge != nil && component.charge.state != candidateChargeReleased {
				valid = false
				driftErr = errCandidateCapacityStale
				break
			}
			if err := candidateComponentAllowsFreshCharge(component); err != nil {
				valid = false
				driftErr = err
				break
			}
		}
	}
	if !valid {
		g.mu.Unlock()
		releaseCandidateAdmissionLeases(leases)
		g.mu.Lock()
		g.reserving = false
		g.changed.Broadcast()
		g.mu.Unlock()
		if driftErr == nil {
			driftErr = errCandidateCapacityStale
		}
		return nil, driftErr
	}
	for index, component := range missing {
		g.nextEpoch++
		component.charge = &candidateCapacityCharge{
			epoch: g.nextEpoch,
			token: &candidateRawCapacityChargeToken{lease: leases[index]}, lease: leases[index],
			state:           candidateChargeActive,
			receiptRetained: component.receipt != nil,
		}
	}
	reservation := g.attachReservationLocked(ledger)
	g.reserving = false
	g.changed.Broadcast()
	g.mu.Unlock()
	return reservation, nil
}

func releaseCandidateAdmissionLeases(leases []*generatedcapacity.Lease) {
	for _, lease := range leases {
		if lease != nil {
			lease.Release()
		}
	}
}

func candidateComponentAllowsFreshCharge(component *candidateCapacityComponent) error {
	if component == nil || component.receipt == nil {
		return nil
	}
	word := component.receipt.state.Load()
	if candidatePhysicalFromWord(word) == candidatePhysicalConfirmedAbsent {
		return errCandidateConfirmedAbsent
	}
	// A released charge is legitimate only for an original receipt whose
	// current invocation proved that the runner was never entered. Every risky
	// physical state and unquiesced mutation must still own its prior charge.
	if word&candidateReceiptUnquiesced != 0 || candidatePhysicalFromWord(word) != candidatePhysicalUnused {
		return errCandidateCapacityStale
	}
	return nil
}

func (g *candidateCapacityGroup) attachReservationLocked(
	ledger *candidateCapacityLedger,
) *candidateCapacityReservation {
	reservation := &candidateCapacityReservation{
		ledger:   ledger,
		group:    g,
		bindings: make(map[string]*candidateCapacityBinding, len(g.components)),
	}
	reservation.changed = sync.NewCond(&reservation.mu)
	for _, name := range g.components {
		component := g.byName[name]
		charge := component.charge
		// Any alias that sees an existing receipt is not the unique creator
		// with exact never-entered proof. Retention is monotonic.
		if component.receipt != nil {
			charge.receiptRetained = true
		}
		charge.refs++
		reservation.bindings[name] = &candidateCapacityBinding{
			component: component,
			charge:    charge,
			epoch:     charge.epoch,
			attached:  true,
		}
	}
	return reservation
}

func equalCandidateRoster(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (r *candidateCapacityReservation) retainAll() error {
	if err := r.enterActivity(); err != nil {
		return err
	}
	defer r.leaveActivity()

	g := r.group
	g.mu.Lock()
	for g.reserving {
		g.changed.Wait()
	}
	for _, name := range g.components {
		binding := r.bindings[name]
		if !validCandidateBindingLocked(binding) {
			g.mu.Unlock()
			return errCandidateCapacityStale
		}
	}
	for _, name := range g.components {
		r.bindings[name].charge.explicitRetained = true
	}
	g.mu.Unlock()
	return nil
}

func (r *candidateCapacityReservation) release() error {
	if r == nil || r.group == nil {
		return errCandidateCapacityInvalid
	}
	r.mu.Lock()
	if r.released {
		err := r.releaseErr
		r.mu.Unlock()
		return err
	}
	if r.closing {
		for !r.released {
			r.changed.Wait()
		}
		err := r.releaseErr
		r.mu.Unlock()
		return err
	}
	r.closing = true
	for r.active > 0 {
		r.changed.Wait()
	}
	r.mu.Unlock()

	releaseErr := r.releaseBindings()

	r.mu.Lock()
	r.releaseErr = releaseErr
	r.released = true
	r.changed.Broadcast()
	r.mu.Unlock()
	return releaseErr
}

func (r *candidateCapacityReservation) releaseBindings() error {
	g := r.group
	g.mu.Lock()
	for g.reserving {
		g.changed.Wait()
	}
	for _, name := range g.components {
		binding := r.bindings[name]
		if binding == nil {
			g.mu.Unlock()
			return errCandidateCapacityStale
		}
		if binding.attached && !validCandidateBindingLocked(binding) &&
			!transferredCandidateBindingLocked(binding) && !terminalCandidateBindingLocked(binding) {
			g.mu.Unlock()
			return errCandidateCapacityStale
		}
	}

	retiring := make([]candidateRetiringCharge, 0, len(g.components))
	for _, name := range g.components {
		binding := r.bindings[name]
		if !binding.attached {
			continue
		}
		if terminalCandidateBindingLocked(binding) {
			binding.attached = false
			continue
		}
		if transferredCandidateBindingLocked(binding) {
			binding.charge.refs--
			binding.attached = false
			continue
		}
		binding.charge.refs--
		binding.attached = false
		if candidateChargeCanRetire(binding.component, binding.charge, binding) {
			binding.charge.state = candidateChargeRetiring
			retiring = append(retiring, candidateRetiringCharge{
				component: binding.component,
				charge:    binding.charge,
				token:     binding.charge.token,
			})
		}
	}
	g.mu.Unlock()

	releaseRetiringCandidateCharges(g, retiring)
	return nil
}

func candidateChargeCanRetire(
	component *candidateCapacityComponent,
	charge *candidateCapacityCharge,
	binding *candidateCapacityBinding,
) bool {
	if component == nil || charge == nil || binding == nil || charge.state != candidateChargeActive ||
		charge.owner != candidateAccountingCandidate ||
		charge.refs != 0 || charge.receiptRetained || charge.explicitRetained ||
		charge.logicallyRetired || !candidateCapacityChargeTokenConfigured(charge.token) {
		return false
	}
	receipt := component.receipt
	if receipt == nil {
		return true
	}
	word := receipt.state.Load()
	if word&candidateReceiptUnquiesced != 0 {
		return false
	}
	switch candidatePhysicalFromWord(word) {
	case candidatePhysicalConfirmedAbsent:
		return true
	case candidatePhysicalUnused:
		return binding.creatorReceipt == receipt && binding.knownNeverEntered.Load()
	default:
		return false
	}
}

func releaseRetiringCandidateCharges(
	group *candidateCapacityGroup,
	retiring []candidateRetiringCharge,
) {
	for _, entry := range retiring {
		entry.token.release()
	}
	if len(retiring) == 0 {
		return
	}
	group.mu.Lock()
	for _, entry := range retiring {
		if entry.component.charge == entry.charge && entry.charge.state == candidateChargeRetiring {
			entry.charge.state = candidateChargeReleased
			entry.charge.token = nil
			entry.charge.lease = nil
		}
	}
	group.changed.Broadcast()
	group.mu.Unlock()
}

func validCandidateBindingLocked(binding *candidateCapacityBinding) bool {
	return exactCandidateBindingLocked(binding) && binding.attached &&
		binding.charge.state == candidateChargeActive && binding.charge.owner == candidateAccountingCandidate &&
		!binding.charge.logicallyRetired &&
		binding.charge.refs > 0
}

func transferredCandidateBindingLocked(binding *candidateCapacityBinding) bool {
	return exactCandidateBindingLocked(binding) && binding.attached &&
		binding.charge.state == candidateChargeActive &&
		(binding.charge.owner == candidateAccountingTransferring ||
			binding.charge.owner == candidateAccountingRouteActive ||
			binding.charge.owner == candidateAccountingPredecessorRetiring)
}

func terminalCandidateBindingLocked(binding *candidateCapacityBinding) bool {
	return exactCandidateBindingLocked(binding) && binding.attached && binding.charge.logicallyRetired
}

func exactCandidateBindingLocked(binding *candidateCapacityBinding) bool {
	return binding != nil && binding.component != nil && binding.charge != nil &&
		binding.component.charge == binding.charge && binding.charge.epoch == binding.epoch
}

func (r *candidateCapacityReservation) enterActivity() error {
	if r == nil || r.group == nil {
		return errCandidateCapacityInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.released {
		return errCandidateCapacityClosing
	}
	r.active++
	return nil
}

func (r *candidateCapacityReservation) leaveActivity() {
	r.mu.Lock()
	if r.active > 0 {
		r.active--
	}
	if r.active == 0 {
		r.changed.Broadcast()
	}
	r.mu.Unlock()
}

func (r *candidateCapacityReservation) enterReceiptAttachment(
	component string,
) (*candidateComponentOperation, error) {
	return r.enterComponentToken(component, "", candidateOperationObserve, true, false)
}

func (r *candidateCapacityReservation) enterComponent(
	component string,
	receiptID string,
	mode candidateOperationMode,
) (*candidateComponentOperation, error) {
	if receiptID == "" || mode != candidateOperationObserve && mode != candidateOperationMutate {
		return nil, errCandidateCapacityInvalid
	}
	return r.enterComponentToken(component, receiptID, mode, false, false)
}

func (r *candidateCapacityReservation) enterCleanup(
	component string,
	receiptID string,
) (*candidateComponentOperation, error) {
	if receiptID == "" {
		return nil, errCandidateCapacityInvalid
	}
	return r.enterComponentToken(component, receiptID, candidateOperationMutate, false, true)
}

func (r *candidateCapacityReservation) enterComponentToken(
	componentName string,
	receiptID string,
	mode candidateOperationMode,
	attachment bool,
	cleanup bool,
) (*candidateComponentOperation, error) {
	if err := r.enterActivity(); err != nil {
		return nil, err
	}

	g := r.group
	g.mu.Lock()
	binding := r.bindings[componentName]
	if !validCandidateBindingLocked(binding) {
		g.mu.Unlock()
		r.leaveActivity()
		return nil, errCandidateCapacityStale
	}
	component := binding.component
	if cleanup {
		if g.cleanupClosed {
			g.mu.Unlock()
			r.leaveActivity()
			return nil, errCandidateCleanupClosed
		}
		g.activeCleanup++
	}
	receipt := component.receipt
	if !attachment && (receipt == nil || receipt.id != receiptID) {
		if cleanup {
			g.activeCleanup--
			g.changed.Broadcast()
		}
		g.mu.Unlock()
		r.leaveActivity()
		return nil, errCandidateReceiptMismatch
	}
	if !attachment && mode == candidateOperationMutate && receipt.state.Load()&candidateReceiptUnquiesced != 0 {
		if cleanup {
			g.activeCleanup--
			g.changed.Broadcast()
		}
		g.mu.Unlock()
		r.leaveActivity()
		return nil, errCandidateMutationUnquiesced
	}
	if component.tokenHeld {
		if cleanup {
			g.activeCleanup--
			g.changed.Broadcast()
		}
		g.mu.Unlock()
		r.leaveActivity()
		return nil, errCandidateCapacityBusy
	}
	component.tokenHeld = true
	component.tokenGeneration++
	if component.tokenGeneration == 0 {
		component.tokenGeneration++
	}
	operation := &candidateComponentOperation{
		reservation:     r,
		group:           g,
		binding:         binding,
		component:       component,
		receipt:         receipt,
		lifecycle:       &candidateOperationLifecycle{},
		tokenGeneration: component.tokenGeneration,
		mode:            mode,
		attachment:      attachment,
		cleanup:         cleanup,
	}
	g.mu.Unlock()
	return operation, nil
}

func (r *candidateCapacityReservation) closeCleanupAndEnterRoster(
	receipts map[string]string,
) (*candidateRosterOperation, error) {
	if len(receipts) == 0 {
		return nil, errCandidateCapacityInvalid
	}
	expected := make(map[string]string, len(receipts))
	for component, receiptID := range receipts {
		if component == "" || receiptID == "" {
			return nil, errCandidateCapacityInvalid
		}
		expected[component] = receiptID
	}
	if err := r.enterActivity(); err != nil {
		return nil, err
	}

	g := r.group
	g.mu.Lock()
	g.cleanupClosed = true
	g.changed.Broadcast()
	for g.activeCleanup > 0 {
		g.changed.Wait()
	}
	if len(expected) != len(g.components) {
		g.mu.Unlock()
		r.leaveActivity()
		return nil, errCandidateReceiptMismatch
	}
	for _, name := range g.components {
		binding := r.bindings[name]
		if !validCandidateBindingLocked(binding) {
			g.mu.Unlock()
			r.leaveActivity()
			return nil, errCandidateCapacityStale
		}
		receipt := binding.component.receipt
		if receipt == nil || expected[name] != receipt.id {
			g.mu.Unlock()
			r.leaveActivity()
			return nil, errCandidateReceiptMismatch
		}
		if receipt.state.Load()&candidateReceiptUnquiesced != 0 {
			g.mu.Unlock()
			r.leaveActivity()
			return nil, errCandidateMutationUnquiesced
		}
		if binding.component.tokenHeld {
			g.mu.Unlock()
			r.leaveActivity()
			return nil, errCandidateCapacityBusy
		}
	}

	tokens := make([]candidateRosterToken, 0, len(g.components))
	for _, name := range g.components {
		component := r.bindings[name].component
		component.tokenHeld = true
		component.tokenGeneration++
		if component.tokenGeneration == 0 {
			component.tokenGeneration++
		}
		tokens = append(tokens, candidateRosterToken{
			component:  component,
			generation: component.tokenGeneration,
		})
	}
	operation := &candidateRosterOperation{
		reservation: r,
		group:       g,
		lifecycle:   &candidateOperationLifecycle{},
		tokens:      tokens,
	}
	g.mu.Unlock()
	return operation, nil
}

func (o *candidateComponentOperation) recordNewReceipt(
	receiptID string,
	commit candidateReceiptCommit,
) error {
	if receiptID == "" || commit != candidateReceiptCommitAcknowledged && commit != candidateReceiptCommitUnknown {
		return errCandidateCapacityInvalid
	}
	if o == nil || !o.attachment || o.lifecycle == nil || o.lifecycle.done.Load() {
		return errCandidateCapacityStale
	}

	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if !o.validTokenLocked() || o.component.receipt != nil {
		return errCandidateReceiptMismatch
	}
	word := candidateReceiptAcknowledged
	if commit == candidateReceiptCommitUnknown {
		word = candidateReceiptUnknown
	}
	receipt := &candidateReceiptTombstone{id: receiptID}
	receipt.state.Store(word)
	o.component.receipt = receipt
	o.receipt = receipt
	o.binding.creatorReceipt = receipt
	o.binding.knownNeverEntered.Store(true)
	// An already-shared epoch no longer has one exclusive creator proof.
	if o.binding.charge.refs > 1 {
		o.binding.charge.receiptRetained = true
	}
	return nil
}

func (o *candidateComponentOperation) attachExistingReceipt(receiptID string) error {
	if receiptID == "" || o == nil || !o.attachment || o.lifecycle == nil || o.lifecycle.done.Load() {
		return errCandidateCapacityInvalid
	}

	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if !o.validTokenLocked() {
		return errCandidateCapacityStale
	}
	receipt := o.component.receipt
	if receipt == nil {
		receipt = &candidateReceiptTombstone{id: receiptID}
		receipt.state.Store(candidateReceiptObserved)
		o.component.receipt = receipt
	} else if receipt.id != receiptID {
		return errCandidateReceiptMismatch
	} else {
		candidateAtomicSet(&receipt.state, candidateReceiptObserved)
	}
	o.receipt = receipt
	o.binding.creatorReceipt = nil
	o.binding.knownNeverEntered.Store(false)
	o.binding.charge.receiptRetained = true
	return nil
}

// enterCommand is the only command-entry transition. It is deliberately
// atomic-only so the concrete owner may place it immediately after the final
// committed writer guard and immediately before the fixed runner call.
func (o *candidateComponentOperation) enterCommand(command candidateCommand) error {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() || o.receipt == nil {
		return errCandidateCapacityStale
	}
	if o.mode != candidateOperationMutate {
		return errCandidateOperationKind
	}
	switch command {
	case candidateCommandCreate, candidateCommandStart:
		if o.cleanup || o.attachment {
			return errCandidateOperationKind
		}
	case candidateCommandStop, candidateCommandRemove:
		if !o.cleanup || o.attachment {
			return errCandidateOperationKind
		}
	default:
		return errCandidateCapacityInvalid
	}
	marker, ok := candidateCommandMarker(command)
	if !ok {
		return errCandidateCapacityInvalid
	}
	if command == candidateCommandCreate {
		// Losing this proof is conservative even when the subsequent CAS finds
		// a prior entry; it can never make capacity releasable.
		o.binding.knownNeverEntered.Store(false)
	}
	for {
		word := o.receipt.state.Load()
		if word&candidateReceiptUnquiesced != 0 {
			return errCandidateMutationUnquiesced
		}
		if word&marker != 0 {
			return errCandidateCommandEntered
		}
		next := word | marker
		physical := candidatePhysicalFromWord(word)
		switch command {
		case candidateCommandCreate:
			if physical != candidatePhysicalUnused {
				return errCandidatePhysicalTransition
			}
			next = candidateWordWithPhysical(next, candidatePhysicalAttemptedUnknown)
		case candidateCommandStart:
			if physical != candidatePhysicalPresent {
				return errCandidatePhysicalTransition
			}
		case candidateCommandStop, candidateCommandRemove:
			if physical != candidatePhysicalAttemptedUnknown && physical != candidatePhysicalPresent {
				return errCandidatePhysicalTransition
			}
		}
		if o.receipt.state.CompareAndSwap(word, next) {
			return nil
		}
	}
}

func candidateCommandMarker(command candidateCommand) (uint64, bool) {
	switch command {
	case candidateCommandCreate:
		return candidateReceiptCreate, true
	case candidateCommandStart:
		return candidateReceiptStart, true
	case candidateCommandStop:
		return candidateReceiptStop, true
	case candidateCommandRemove:
		return candidateReceiptRemove, true
	default:
		return 0, false
	}
}

func (o *candidateComponentOperation) markPresent() error {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() || o.receipt == nil {
		return errCandidateCapacityStale
	}
	if o.mode != candidateOperationMutate || o.cleanup || o.attachment {
		return errCandidateOperationKind
	}
	if o.receipt.state.Load()&candidateReceiptUnquiesced != 0 {
		return errCandidateMutationUnquiesced
	}
	return candidateTransitionPhysical(
		&o.receipt.state,
		candidatePhysicalPresent,
		candidatePhysicalUnused,
		candidatePhysicalAttemptedUnknown,
		candidatePhysicalPresent,
	)
}

func (o *candidateComponentOperation) retireConfirmedAbsent() error {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() || o.receipt == nil {
		return errCandidateCapacityStale
	}
	if o.mode != candidateOperationMutate || !o.cleanup || o.attachment {
		return errCandidateOperationKind
	}

	g := o.group
	g.mu.Lock()
	for g.reserving {
		g.changed.Wait()
	}
	if !o.validTokenLocked() || !validCandidateBindingLocked(o.binding) {
		g.mu.Unlock()
		return errCandidateCapacityStale
	}
	if o.receipt.state.Load()&candidateReceiptUnquiesced != 0 {
		g.mu.Unlock()
		return errCandidateMutationUnquiesced
	}
	if err := candidateTransitionPhysical(
		&o.receipt.state,
		candidatePhysicalConfirmedAbsent,
		candidatePhysicalUnused,
		candidatePhysicalAttemptedUnknown,
		candidatePhysicalPresent,
		candidatePhysicalConfirmedAbsent,
	); err != nil {
		g.mu.Unlock()
		return err
	}
	charge := o.binding.charge
	charge.logicallyRetired = true
	charge.refs = 0
	o.binding.attached = false
	var retiring []candidateRetiringCharge
	if !charge.explicitRetained {
		charge.state = candidateChargeRetiring
		retiring = []candidateRetiringCharge{{
			component: o.component,
			charge:    charge,
			token:     charge.token,
		}}
	}
	g.mu.Unlock()

	releaseRetiringCandidateCharges(g, retiring)
	return nil
}

// latchMutationUnquiesced is a monotonic post-run transition. It also retains
// the current exact charge under the group lock, so a concurrent absence fact
// can never release capacity after an unjoined mutation result.
func (o *candidateComponentOperation) latchMutationUnquiesced() {
	if o == nil || o.receipt == nil || o.lifecycle == nil || o.lifecycle.done.Load() ||
		o.mode != candidateOperationMutate || o.attachment {
		return
	}
	g := o.group
	g.mu.Lock()
	if !o.validTokenLocked() {
		g.mu.Unlock()
		return
	}
	word := o.receipt.state.Load()
	if o.cleanup {
		if word&(candidateReceiptStop|candidateReceiptRemove) == 0 {
			g.mu.Unlock()
			return
		}
	} else if word&(candidateReceiptCreate|candidateReceiptStart) == 0 {
		g.mu.Unlock()
		return
	}
	candidateAtomicSet(&o.receipt.state, candidateReceiptUnquiesced)
	if o.binding != nil && o.binding.component == o.component &&
		o.component.charge == o.binding.charge && o.binding.charge.epoch == o.binding.epoch &&
		o.binding.charge.state == candidateChargeActive {
		o.binding.charge.explicitRetained = true
	}
	g.mu.Unlock()
}

func (o *candidateComponentOperation) snapshot() (candidateReceiptSnapshot, error) {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() || o.receipt == nil {
		return candidateReceiptSnapshot{}, errCandidateCapacityStale
	}
	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	if !o.validTokenLocked() {
		return candidateReceiptSnapshot{}, errCandidateCapacityStale
	}
	return candidateSnapshotLocked(o.receipt, o.binding.charge), nil
}

// markHandedToRoute records the whole exact roster while its tokens are held.
// Validation completes for every member before any state changes.
func (o *candidateRosterOperation) markHandedToRoute() error {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Load() {
		return errCandidateCapacityStale
	}
	g := o.group
	g.mu.Lock()
	defer g.mu.Unlock()
	words := make([]uint64, len(o.tokens))
	for index, token := range o.tokens {
		if !token.component.tokenHeld || token.component.tokenGeneration != token.generation ||
			token.component.receipt == nil {
			return errCandidateCapacityStale
		}
		word := token.component.receipt.state.Load()
		if word&candidateReceiptUnquiesced != 0 {
			return errCandidateMutationUnquiesced
		}
		physical := candidatePhysicalFromWord(word)
		if physical != candidatePhysicalPresent && physical != candidatePhysicalHandedToRoute {
			return errCandidatePhysicalTransition
		}
		words[index] = word
	}
	for index, token := range o.tokens {
		if candidatePhysicalFromWord(words[index]) == candidatePhysicalHandedToRoute {
			continue
		}
		token.component.receipt.state.Store(
			candidateWordWithPhysical(words[index], candidatePhysicalHandedToRoute),
		)
	}
	return nil
}

func candidateSnapshotLocked(
	receipt *candidateReceiptTombstone,
	charge *candidateCapacityCharge,
) candidateReceiptSnapshot {
	word := receipt.state.Load()
	return candidateReceiptSnapshot{
		receiptID:          receipt.id,
		physical:           candidatePhysicalFromWord(word),
		commitAcknowledged: word&candidateReceiptAcknowledged != 0,
		commitUnknown:      word&candidateReceiptUnknown != 0,
		observed:           word&candidateReceiptObserved != 0,
		createEntered:      word&candidateReceiptCreate != 0,
		startEntered:       word&candidateReceiptStart != 0,
		stopEntered:        word&candidateReceiptStop != 0,
		removeEntered:      word&candidateReceiptRemove != 0,
		mutationUnquiesced: word&candidateReceiptUnquiesced != 0,
		chargeEpoch:        charge.epoch,
		chargeState:        charge.state,
		accountingOwner:    charge.owner,
		accountingRetained: charge.receiptRetained || charge.explicitRetained,
		receiptRetained:    charge.receiptRetained,
		explicitRetained:   charge.explicitRetained,
		logicallyRetired:   charge.logicallyRetired,
	}
}

func (o *candidateComponentOperation) validTokenLocked() bool {
	return o != nil && o.lifecycle != nil && !o.lifecycle.done.Load() &&
		o.binding != nil && o.binding.component == o.component &&
		validCandidateBindingLocked(o.binding) && o.component.tokenHeld &&
		o.component.tokenGeneration == o.tokenGeneration
}

func (o *candidateComponentOperation) leave() {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Swap(true) {
		return
	}
	g := o.group
	g.mu.Lock()
	if o.component != nil && o.component.tokenHeld && o.component.tokenGeneration == o.tokenGeneration {
		o.component.tokenHeld = false
	}
	if o.cleanup && g.activeCleanup > 0 {
		g.activeCleanup--
	}
	g.changed.Broadcast()
	g.mu.Unlock()
	o.reservation.leaveActivity()
}

func (o *candidateRosterOperation) leave() {
	if o == nil || o.lifecycle == nil || o.lifecycle.done.Swap(true) {
		return
	}
	g := o.group
	g.mu.Lock()
	for _, token := range o.tokens {
		if token.component.tokenHeld && token.component.tokenGeneration == token.generation {
			token.component.tokenHeld = false
		}
	}
	g.changed.Broadcast()
	g.mu.Unlock()
	o.reservation.leaveActivity()
}

func candidatePhysicalFromWord(word uint64) candidatePhysicalState {
	return candidatePhysicalState(word & candidateReceiptPhysicalMask)
}

func candidateWordWithPhysical(word uint64, physical candidatePhysicalState) uint64 {
	return word&^candidateReceiptPhysicalMask | uint64(physical)
}

func candidateTransitionPhysical(
	state *atomic.Uint64,
	target candidatePhysicalState,
	allowed ...candidatePhysicalState,
) error {
	for {
		word := state.Load()
		current := candidatePhysicalFromWord(word)
		accepted := false
		for _, candidate := range allowed {
			if current == candidate {
				accepted = true
				break
			}
		}
		if !accepted {
			return errCandidatePhysicalTransition
		}
		if current == target {
			return nil
		}
		if state.CompareAndSwap(word, candidateWordWithPhysical(word, target)) {
			return nil
		}
	}
}

func candidateAtomicSet(state *atomic.Uint64, flag uint64) {
	for {
		word := state.Load()
		if word&flag != 0 || state.CompareAndSwap(word, word|flag) {
			return
		}
	}
}
