package deployments

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
	budget *generatedcapacity.Budget

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
	previousGeneration           int64
}

type candidateCapacityGroup struct {
	mu      sync.Mutex
	changed *sync.Cond

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

type candidateCapacityCharge struct {
	epoch uint64
	lease *generatedcapacity.Lease
	refs  int
	state candidateChargeState

	receiptRetained  bool
	explicitRetained bool
	logicallyRetired bool
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
	accountingRetained bool
	receiptRetained    bool
	explicitRetained   bool
	logicallyRetired   bool
}

type candidateRetiringCharge struct {
	component *candidateCapacityComponent
	charge    *candidateCapacityCharge
	lease     *generatedcapacity.Lease
}

func newCandidateCapacityLedger(budget *generatedcapacity.Budget) (*candidateCapacityLedger, error) {
	if budget == nil {
		return nil, errCandidateCapacityInvalid
	}
	return &candidateCapacityLedger{
		budget: budget,
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
		group = newCandidateCapacityGroup(normalized)
		l.groups[key] = group
	}
	l.mu.Unlock()

	return group.reserve(ctx, l, normalized)
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

func newCandidateCapacityGroup(spec candidateGroupSpec) *candidateCapacityGroup {
	group := &candidateCapacityGroup{
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
			epoch:           g.nextEpoch,
			lease:           leases[index],
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
			!terminalCandidateBindingLocked(binding) {
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
		binding.charge.refs--
		binding.attached = false
		if candidateChargeCanRetire(binding.component, binding.charge, binding) {
			binding.charge.state = candidateChargeRetiring
			retiring = append(retiring, candidateRetiringCharge{
				component: binding.component,
				charge:    binding.charge,
				lease:     binding.charge.lease,
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
		charge.refs != 0 || charge.receiptRetained || charge.explicitRetained ||
		charge.logicallyRetired || charge.lease == nil {
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
		entry.lease.Release()
	}
	if len(retiring) == 0 {
		return
	}
	group.mu.Lock()
	for _, entry := range retiring {
		if entry.component.charge == entry.charge && entry.charge.state == candidateChargeRetiring {
			entry.charge.state = candidateChargeReleased
			entry.charge.lease = nil
		}
	}
	group.changed.Broadcast()
	group.mu.Unlock()
}

func validCandidateBindingLocked(binding *candidateCapacityBinding) bool {
	return exactCandidateBindingLocked(binding) && binding.attached &&
		binding.charge.state == candidateChargeActive && !binding.charge.logicallyRetired &&
		binding.charge.refs > 0
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
			lease:     charge.lease,
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
