package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// generatedDomainReservationInput is immutable construction provenance. It
// carries no route, deployment, candidate or effect authority.
type generatedDomainReservationInput struct {
	stores        *deployments.GeneratedOwnerStores
	repository    *deployments.Repository
	database      *sql.DB
	guard         *deploymentexecution.Guard
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	runner        runtimeprocess.CommandRunner
	environment   GeneratedCandidateEnvironmentStager
	options       generatedDomainOptions
	physical      generatedDomainPhysicalIdentity
	proof         generatedDomainPathProof
}

type generatedDomainState struct {
	done       chan struct{}
	failed     bool
	fenceReady bool
	err        error

	constructionPhase    generatedDomainConstructionPhase
	constructionDone     chan struct{}
	construction         *generatedDomainConstruction
	constructionErr      error
	constructionView     *generatedManagerView
	constructionActivity generatedDomainConstructionActivityKind
	transition           *generatedDomainConstructionTransition
}

type generatedDomainConstructionActivityKind uint8

const (
	generatedDomainConstructionActivityNone generatedDomainConstructionActivityKind = iota
	generatedDomainConstructionActivityEmpty
	generatedDomainConstructionActivityCold
)

type generatedDomainConstructionPhase uint8

const (
	generatedDomainConstructionUnstarted generatedDomainConstructionPhase = iota
	generatedDomainConstructionRunning
	generatedDomainConstructionComplete
	generatedDomainConstructionFailed
)

type generatedDomainConstructionTransition struct {
	physical generatedDomainPhysicalIdentity
	proof    generatedDomainPathProof
	owned    generatedDomainOwnedPathTransition
}

type generatedManagerView struct {
	mu sync.Mutex

	manager *Manager
	root    generatedLegacyRootObservation

	identity      generatedManagerPhysicalIdentity
	identityBound bool
	pairing       bool
	stale         bool
	generation    uint64

	typedActive      int
	typedIdle        chan struct{}
	inspectionActive int
	inspectionIdle   chan struct{}
}

type generatedLegacyManagerRecord struct {
	view *generatedManagerView
}

type generatedLegacyConstructionTicket struct {
	view       *generatedManagerView
	done       chan struct{}
	completed  bool
	authorized bool
}

type generatedDomainReservation struct {
	input generatedDomainReservationInput
	state *generatedDomainState
}

type generatedDomainProcessRegistry struct {
	mu sync.Mutex

	reservations []*generatedDomainReservation
	records      []*generatedLegacyManagerRecord
	tickets      map[*generatedLegacyConstructionTicket]struct{}
	pairingEpoch int
	pairingIdle  chan struct{}
}

var generatedDomains = generatedDomainProcessRegistry{
	tickets: make(map[*generatedLegacyConstructionTicket]struct{}), pairingIdle: generatedClosedSignal(),
}

func newGeneratedDomainReservationInput(
	stores *deployments.GeneratedOwnerStores,
	runner runtimeprocess.CommandRunner,
	environment GeneratedCandidateEnvironmentStager,
	options generatedDomainOptions,
	physical generatedDomainPhysicalIdentity,
) (generatedDomainReservationInput, error) {
	options, valid := normalizeGeneratedDomainOptions(options)
	if stores == nil || !valid || !generatedCandidatePointerIdentity(runner) ||
		!generatedCandidatePointerIdentity(environment) {
		return generatedDomainReservationInput{}, deploymentexecution.ErrInvalidConfiguration
	}
	snapshot := *stores
	repository := snapshot.Repository()
	if repository == nil {
		return generatedDomainReservationInput{}, deploymentexecution.ErrInvalidConfiguration
	}
	canonical, err := repository.GeneratedOwnerStores()
	if err != nil || canonical == nil || !snapshot.ConfiguredForDomain(options.DataRoot, options.WorkingDirectory) ||
		!canonical.ConfiguredForDomain(options.DataRoot, options.WorkingDirectory) {
		return generatedDomainReservationInput{}, deploymentexecution.ErrInvalidConfiguration
	}
	input := generatedDomainReservationInput{
		stores: canonical, repository: repository, database: canonical.Database(),
		guard: canonical.ExecutionGuard(), plans: canonical.Plans(), configuration: canonical.Configuration(),
		runner: runner, environment: environment, options: options, physical: physical,
	}
	proof, err := inspectGeneratedDomainPaths(options.DataRoot)
	if err != nil || !sameGeneratedDomainFileIdentity(proof.dataRoot, physical.manager.dataRoot) ||
		!sameGeneratedDomainFileIdentity(proof.databaseFile, physical.databaseFile) ||
		!sameGeneratedDomainOptionalFileIdentity(proof.ingressRoot, physical.manager.ingressRoot) {
		return generatedDomainReservationInput{}, deploymentexecution.ErrInvalidConfiguration
	}
	input.proof = proof
	if !input.configured() || !input.current() {
		return generatedDomainReservationInput{}, deploymentexecution.ErrInvalidConfiguration
	}
	return input, nil
}

func (i generatedDomainReservationInput) configured() bool {
	if i.stores == nil || i.repository == nil || i.database == nil || i.guard == nil ||
		i.plans == nil || i.configuration == nil || !generatedCandidatePointerIdentity(i.runner) ||
		!generatedCandidatePointerIdentity(i.environment) || i.stores.Repository() != i.repository ||
		i.stores.Database() != i.database || i.stores.ExecutionGuard() != i.guard ||
		i.stores.Plans() != i.plans || i.stores.Configuration() != i.configuration ||
		!i.stores.ConfiguredForDomain(i.options.DataRoot, i.options.WorkingDirectory) ||
		i.physical.database != i.database || i.physical.databaseFile == nil ||
		i.physical.manager.dataRoot == nil || !validGeneratedDaemonID(i.physical.manager.daemonID) ||
		i.physical.manager.namespace != fixedGeneratedGatewayNamespace() {
		return false
	}
	return true
}

func (i generatedDomainReservationInput) current() bool {
	ingress, err := i.proof.recheck(i.physical.manager.ingressRoot != nil)
	return err == nil && sameGeneratedDomainFileIdentity(i.proof.dataRoot, i.physical.manager.dataRoot) &&
		sameGeneratedDomainFileIdentity(i.proof.databaseFile, i.physical.databaseFile) &&
		sameGeneratedDomainOptionalFileIdentity(ingress, i.physical.manager.ingressRoot)
}

func sameGeneratedDomainReservationInput(left, right generatedDomainReservationInput) bool {
	return sameGeneratedDomainReservationProvenance(left, right) &&
		sameGeneratedDomainPhysicalIdentity(left.physical, right.physical)
}

func sameGeneratedDomainReservationProvenance(left, right generatedDomainReservationInput) bool {
	return left.stores == right.stores && left.repository == right.repository &&
		left.database == right.database && left.guard == right.guard && left.plans == right.plans &&
		left.configuration == right.configuration && sameGeneratedPointer(left.runner, right.runner) &&
		sameGeneratedPointer(left.environment, right.environment) && left.options == right.options
}

func sameGeneratedPointer(left, right any) bool {
	if !generatedCandidatePointerIdentity(left) || !generatedCandidatePointerIdentity(right) ||
		reflect.TypeOf(left) != reflect.TypeOf(right) {
		return false
	}
	return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
}

func sameGeneratedDomainPhysicalIdentity(left, right generatedDomainPhysicalIdentity) bool {
	return left.database == right.database &&
		sameGeneratedDomainFileIdentity(left.databaseFile, right.databaseFile) &&
		sameGeneratedManagerPhysicalIdentity(left.manager, right.manager)
}

func sameGeneratedManagerPhysicalIdentity(left, right generatedManagerPhysicalIdentity) bool {
	return sameGeneratedDomainFileIdentity(left.dataRoot, right.dataRoot) &&
		sameGeneratedDomainOptionalFileIdentity(left.ingressRoot, right.ingressRoot) &&
		left.daemonID == right.daemonID && left.namespace == right.namespace
}

func generatedClosedSignal() chan struct{} {
	result := make(chan struct{})
	close(result)
	return result
}

func newGeneratedManagerView(root generatedLegacyRootObservation) *generatedManagerView {
	return &generatedManagerView{
		root: root, typedIdle: generatedClosedSignal(), inspectionIdle: generatedClosedSignal(),
	}
}

func admitGeneratedLegacyConstruction(
	view *generatedManagerView,
	root generatedLegacyRootObservation,
) (*generatedLegacyConstructionTicket, error) {
	if view == nil || root.dataRootPath == "" || view.root.dataRootPath != root.dataRootPath {
		return nil, errors.New("generated ingress construction admission is invalid")
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	if generatedDomains.pairingEpoch != 0 {
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	ticket := &generatedLegacyConstructionTicket{view: view, done: make(chan struct{})}
	generatedDomains.tickets[ticket] = struct{}{}
	return ticket, nil
}

func (t *generatedLegacyConstructionTicket) authorizeStateCreation(ctx context.Context) error {
	if t == nil || t.view == nil || ctx == nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	for {
		generatedDomains.mu.Lock()
		if _, present := generatedDomains.tickets[t]; !present || t.completed || generatedDomains.pairingEpoch != 0 {
			generatedDomains.mu.Unlock()
			return &Error{Code: DiagnosticRouteUnresolved}
		}
		needsDaemon := len(generatedDomains.reservations) != 0
		for _, reservation := range generatedDomains.reservations {
			if generatedLegacyObservationRootConflict(t.view.root, reservation.input) {
				generatedDomains.mu.Unlock()
				return &Error{Code: DiagnosticRouteUnresolved}
			}
		}
		generatedDomains.mu.Unlock()

		current, err := t.view.root.recheck(false)
		if err != nil {
			return err
		}
		var identity generatedManagerPhysicalIdentity
		if needsDaemon {
			identity, err = inspectGeneratedProspectiveManagerIdentity(ctx, current)
			if err != nil {
				return err
			}
		}

		generatedDomains.mu.Lock()
		if _, present := generatedDomains.tickets[t]; !present || t.completed || generatedDomains.pairingEpoch != 0 {
			generatedDomains.mu.Unlock()
			return &Error{Code: DiagnosticRouteUnresolved}
		}
		if !needsDaemon && len(generatedDomains.reservations) != 0 {
			generatedDomains.mu.Unlock()
			continue
		}
		conflict := false
		for _, reservation := range generatedDomains.reservations {
			if generatedLegacyObservationRootConflict(current, reservation.input) ||
				generatedLegacyDaemonNamespaceConflict(identity, reservation.input.physical) {
				conflict = true
				break
			}
		}
		if conflict {
			generatedDomains.mu.Unlock()
			return &Error{Code: DiagnosticRouteUnresolved}
		}
		t.view.mu.Lock()
		t.view.root = current
		t.view.mu.Unlock()
		t.authorized = true
		generatedDomains.mu.Unlock()
		return nil
	}
}

func inspectGeneratedProspectiveManagerIdentity(
	ctx context.Context,
	root generatedLegacyRootObservation,
) (generatedManagerPhysicalIdentity, error) {
	if ctx == nil || !generatedCandidatePointerIdentity(root.runner) {
		return generatedManagerPhysicalIdentity{}, errors.New("generated ingress construction identity is unavailable")
	}
	first, err := root.recheck(false)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, err
	}
	dockerEnv, err := dockerEnvironment(root.options.DockerEndpoint, root.options.DockerConfigDirectory)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, err
	}
	daemonID, err := inspectGeneratedDockerDaemonIdentity(ctx, root.runner, root.options.DockerExecutable,
		dockerEnv, root.options.CommandTimeout)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, err
	}
	second, err := first.recheck(false)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, err
	}
	return generatedManagerPhysicalIdentity{dataRoot: second.dataRoot, ingressRoot: second.ingressRoot,
		daemonID: daemonID, namespace: second.namespace}, nil
}

func (t *generatedLegacyConstructionTicket) publish(manager *Manager) error {
	if t == nil || manager == nil || manager.domainView != t.view {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	root, err := t.view.root.recheck(true)
	if err != nil || manager.store == nil || manager.store.root != root.ingressPath {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	root.dockerEnv = append([]string(nil), manager.dockerEnv...)
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	if _, present := generatedDomains.tickets[t]; !present || t.completed || !t.authorized {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	t.view.mu.Lock()
	t.view.manager = manager
	t.view.root = root
	t.view.generation++
	t.view.mu.Unlock()
	generatedDomains.records = append(generatedDomains.records, &generatedLegacyManagerRecord{view: t.view})
	t.completeLocked()
	return nil
}

func (t *generatedLegacyConstructionTicket) fail(_ error) {
	if t == nil {
		return
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	if _, present := generatedDomains.tickets[t]; present && !t.completed {
		t.completeLocked()
	}
}

func (t *generatedLegacyConstructionTicket) completeLocked() {
	delete(generatedDomains.tickets, t)
	t.completed = true
	close(t.done)
}

func reserveGeneratedDomain(ctx context.Context, input generatedDomainReservationInput) (*generatedDomainState, error) {
	if ctx == nil || !input.configured() {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return reserveGeneratedDomainAfterPairing(ctx, input, false, false)
}

func reserveGeneratedDomainAfterPairing(
	ctx context.Context,
	input generatedDomainReservationInput,
	current bool,
	allowConstructionTransition bool,
) (*generatedDomainState, error) {
	generatedDomains.mu.Lock()
	for _, existing := range generatedDomains.reservations {
		transitionMatch := allowConstructionTransition &&
			generatedDomainConstructionTransitionMatchesLocked(existing, input)
		if sameGeneratedDomainReservationInput(existing.input, input) || transitionMatch {
			state := existing.state
			generatedDomains.mu.Unlock()
			return waitGeneratedDomainReservation(ctx, state)
		}
		if generatedDomainRootOrDatabaseConflict(existing.input.physical, input.physical) ||
			generatedDomainDaemonNamespaceConflict(existing.input.physical, input.physical) {
			generatedDomains.mu.Unlock()
			return nil, deploymentexecution.ErrInvalidConfiguration
		}
	}
	if generatedDomains.pairingEpoch != 0 {
		idle := generatedDomains.pairingIdle
		generatedDomains.mu.Unlock()
		if err := waitGeneratedSignal(ctx, idle); err != nil {
			return nil, err
		}
		return reserveGeneratedDomainAfterPairing(ctx, input, false, allowConstructionTransition)
	}
	if !current {
		generatedDomains.mu.Unlock()
		if !input.current() {
			return nil, deploymentexecution.ErrInvalidConfiguration
		}
		return reserveGeneratedDomainAfterPairing(ctx, input, true, allowConstructionTransition)
	}
	state := &generatedDomainState{done: make(chan struct{}), constructionDone: make(chan struct{})}
	reservation := &generatedDomainReservation{input: input, state: state}
	generatedDomains.reservations = append(generatedDomains.reservations, reservation)
	generatedDomains.pairingEpoch = 1
	generatedDomains.pairingIdle = make(chan struct{})
	tickets := make([]*generatedLegacyConstructionTicket, 0, len(generatedDomains.tickets))
	for ticket := range generatedDomains.tickets {
		tickets = append(tickets, ticket)
	}
	generatedDomains.mu.Unlock()
	if err := ctx.Err(); err != nil {
		failGeneratedDomainReservation(reservation, err, true)
		return state, err
	}

	if err := waitGeneratedConstructionTickets(ctx, tickets); err != nil {
		failGeneratedDomainReservation(reservation, err, true)
		return state, err
	}

	generatedDomains.mu.Lock()
	records := append([]*generatedLegacyManagerRecord(nil), generatedDomains.records...)
	generatedDomains.mu.Unlock()

	observed := make(map[*generatedManagerView]generatedManagerPhysicalIdentity, len(records))
	for _, record := range records {
		if record == nil || record.view == nil {
			failGeneratedDomainReservation(reservation, errors.New("generated ingress legacy identity is unavailable"), true)
			return state, state.err
		}
		record.view.mu.Lock()
		manager := record.view.manager
		alreadyStale := record.view.stale
		record.view.mu.Unlock()
		if alreadyStale {
			continue
		}
		identity, _, err := inspectGeneratedLegacyManagerIdentity(ctx, manager, record.view)
		if err != nil {
			failGeneratedDomainReservation(reservation, err, true)
			return state, err
		}
		observed[record.view] = identity
	}

	type generatedActivityWait struct {
		view       *generatedManagerView
		generation uint64
		inspection <-chan struct{}
		typed      <-chan struct{}
	}
	waits := make([]generatedActivityWait, 0, len(records))
	generatedDomains.mu.Lock()
	if state.failed {
		generatedDomains.mu.Unlock()
		return state, state.err
	}
	for _, record := range generatedDomains.records {
		identity, present := observed[record.view]
		if !present {
			continue
		}
		if !generatedLegacyRootConflict(identity, input.physical) &&
			!generatedLegacyDaemonNamespaceConflict(identity, input.physical) {
			continue
		}
		record.view.mu.Lock()
		record.view.identity = identity
		record.view.identityBound = true
		record.view.pairing = true
		record.view.generation++
		waits = append(waits, generatedActivityWait{view: record.view, generation: record.view.generation,
			inspection: record.view.inspectionIdle, typed: record.view.typedIdle})
		record.view.mu.Unlock()
	}
	generatedDomains.mu.Unlock()

	for _, wait := range waits {
		if err := waitGeneratedSignal(ctx, wait.inspection); err != nil {
			failGeneratedDomainReservation(reservation, err, true)
			return state, err
		}
		if err := waitGeneratedSignal(ctx, wait.typed); err != nil {
			failGeneratedDomainReservation(reservation, err, true)
			return state, err
		}
	}
	if err := ctx.Err(); err != nil {
		failGeneratedDomainReservation(reservation, err, true)
		return state, err
	}

	generatedDomains.mu.Lock()
	if state.failed {
		generatedDomains.mu.Unlock()
		return state, state.err
	}
	for _, wait := range waits {
		wait.view.mu.Lock()
		if wait.view.generation != wait.generation || !wait.view.pairing {
			wait.view.mu.Unlock()
			generatedDomains.mu.Unlock()
			err := errors.New("generated ingress legacy activity changed during pairing")
			failGeneratedDomainReservation(reservation, err, true)
			return state, err
		}
		wait.view.pairing = false
		wait.view.stale = true
		wait.view.generation++
		wait.view.mu.Unlock()
	}
	state.fenceReady = true
	close(state.done)
	closeGeneratedPairingEpochLocked()
	generatedDomains.mu.Unlock()
	return state, nil
}

func generatedDomainConstructionTransitionMatchesLocked(
	reservation *generatedDomainReservation,
	input generatedDomainReservationInput,
) bool {
	if reservation == nil || reservation.state == nil ||
		!sameGeneratedDomainReservationProvenance(reservation.input, input) {
		return false
	}
	if transition := reservation.state.transition; transition != nil {
		return sameGeneratedDomainPhysicalIdentity(transition.physical, input.physical) &&
			sameGeneratedDomainPathObservation(transition.proof, input.proof, false)
	}
	// A caller may observe the constructor-owned directory in the narrow span
	// between create-only Mkdir and recording its final identity. It may join
	// the running attempt, but receives no result or authority unless that
	// attempt later records the exact transition and completes. This is not an
	// adoption rule for generic reservation callers.
	left, right := reservation.input.physical, input.physical
	return reservation.state.constructionPhase == generatedDomainConstructionRunning &&
		left.manager.ingressRoot == nil && right.manager.ingressRoot != nil &&
		left.database == right.database && sameGeneratedDomainFileIdentity(left.databaseFile, right.databaseFile) &&
		sameGeneratedDomainFileIdentity(left.manager.dataRoot, right.manager.dataRoot) &&
		left.manager.daemonID == right.manager.daemonID && left.manager.namespace == right.manager.namespace
}

// reserveGeneratedDomainConstruction joins the irreversible domain
// reservation and elects exactly one construction owner after the legacy
// fence is ready. Waiters never hold a registry or view lock. Their own
// cancellation does not alter the owner's sticky result.
func reserveGeneratedDomainConstruction(
	ctx context.Context,
	input generatedDomainReservationInput,
) (*generatedDomainReservation, bool, *generatedDomainConstruction, error) {
	if ctx == nil || !input.configured() {
		return nil, false, nil, deploymentexecution.ErrInvalidConfiguration
	}
	state, err := reserveGeneratedDomainAfterPairing(ctx, input, false, true)
	if err != nil {
		return nil, false, nil, err
	}
	generatedDomains.mu.Lock()
	var reservation *generatedDomainReservation
	for _, candidate := range generatedDomains.reservations {
		if candidate != nil && candidate.state == state {
			reservation = candidate
			break
		}
	}
	if reservation == nil || !state.fenceReady || state.failed {
		generatedDomains.mu.Unlock()
		return nil, false, nil, deploymentexecution.ErrInvalidConfiguration
	}
	switch state.constructionPhase {
	case generatedDomainConstructionUnstarted:
		state.constructionPhase = generatedDomainConstructionRunning
		generatedDomains.mu.Unlock()
		return reservation, true, nil, nil
	case generatedDomainConstructionRunning:
		done := state.constructionDone
		generatedDomains.mu.Unlock()
		if err := waitGeneratedSignal(ctx, done); err != nil {
			return reservation, false, nil, err
		}
	case generatedDomainConstructionComplete, generatedDomainConstructionFailed:
		generatedDomains.mu.Unlock()
	default:
		generatedDomains.mu.Unlock()
		return nil, false, nil, deploymentexecution.ErrInvalidConfiguration
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	if state.constructionPhase == generatedDomainConstructionComplete && state.construction != nil {
		return reservation, false, state.construction, nil
	}
	if state.constructionErr != nil {
		return reservation, false, nil, state.constructionErr
	}
	return reservation, false, nil, deploymentexecution.ErrInvalidConfiguration
}

func recordGeneratedDomainConstructionTransition(
	reservation *generatedDomainReservation,
	physical generatedDomainPhysicalIdentity,
	proof generatedDomainPathProof,
	owned generatedDomainOwnedPathTransition,
) error {
	if reservation == nil || !owned.current() || proof.ingressRoot == nil ||
		!sameGeneratedDomainPathObservation(proof, owned.final, false) {
		return deploymentexecution.ErrInvalidConfiguration
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	state := reservation.state
	viewActive := false
	if state != nil && state.constructionView != nil {
		state.constructionView.mu.Lock()
		viewActive = state.constructionView.typedActive == 1
		state.constructionView.mu.Unlock()
	}
	if state == nil || state.constructionPhase != generatedDomainConstructionRunning || state.transition != nil ||
		state.constructionView == nil || !viewActive ||
		state.constructionActivity != generatedDomainConstructionActivityEmpty ||
		!sameGeneratedDomainPhysicalIdentity(physical, generatedDomainPhysicalIdentity{
			manager: generatedManagerPhysicalIdentity{dataRoot: proof.dataRoot, ingressRoot: proof.ingressRoot,
				daemonID: reservation.input.physical.manager.daemonID, namespace: fixedGeneratedGatewayNamespace()},
			databaseFile: proof.databaseFile, database: reservation.input.database,
		}) {
		return deploymentexecution.ErrInvalidConfiguration
	}
	state.transition = &generatedDomainConstructionTransition{physical: physical, proof: proof, owned: owned}
	return nil
}

// beginGeneratedDomainConstructionActivity binds the empty constructor to one
// unpublished view before it acquires the Manager-local exclusion. The view is
// retained by the completed Manager, so construction and later typed activity
// use the same latch family. The returned release never waits or performs I/O.
func beginGeneratedDomainConstructionActivity(
	reservation *generatedDomainReservation,
	view *generatedManagerView,
) (func() error, error) {
	return beginGeneratedDomainConstructionActivityMode(reservation, view, generatedDomainConstructionActivityEmpty)
}

func beginGeneratedDomainColdConstructionActivity(
	reservation *generatedDomainReservation,
	view *generatedManagerView,
) (func() error, error) {
	return beginGeneratedDomainConstructionActivityMode(reservation, view, generatedDomainConstructionActivityCold)
}

func beginGeneratedDomainConstructionActivityMode(
	reservation *generatedDomainReservation,
	view *generatedManagerView,
	kind generatedDomainConstructionActivityKind,
) (func() error, error) {
	if reservation == nil || view == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	state := reservation.state
	if state == nil || state.constructionPhase != generatedDomainConstructionRunning ||
		state.constructionView != nil || state.constructionActivity != generatedDomainConstructionActivityNone ||
		state.transition != nil ||
		(kind != generatedDomainConstructionActivityEmpty && kind != generatedDomainConstructionActivityCold) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	view.mu.Lock()
	root := view.root
	if view.manager != nil || view.stale || view.pairing || view.typedActive != 0 || view.inspectionActive != 0 ||
		root.dataRootPath != reservation.input.options.DataRoot ||
		root.ingressPath != reservation.input.proof.ingressPath ||
		!sameGeneratedDomainFileIdentity(root.dataRoot, reservation.input.proof.dataRoot) ||
		!sameGeneratedDomainPathIdentityMap(root.ancestors, reservation.input.proof.ancestors, false) ||
		!sameGeneratedPointer(root.runner, reservation.input.runner) ||
		!sameGeneratedManagerIdentityOptions(root.options, generatedDomainManagerOptions(reservation.input)) ||
		root.namespace != fixedGeneratedGatewayNamespace() {
		view.mu.Unlock()
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	if kind == generatedDomainConstructionActivityEmpty && root.ingressRoot != nil ||
		kind == generatedDomainConstructionActivityCold && (root.ingressRoot == nil ||
			reservation.input.proof.ingressRoot == nil ||
			!sameGeneratedDomainFileIdentity(root.ingressRoot, reservation.input.proof.ingressRoot)) {
		view.mu.Unlock()
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	beginGeneratedTypedActivityLocked(view)
	state.constructionView = view
	state.constructionActivity = kind
	view.mu.Unlock()
	var once sync.Once
	return func() (releaseErr error) {
		once.Do(func() {
			generatedDomains.mu.Lock()
			defer generatedDomains.mu.Unlock()
			view.mu.Lock()
			defer view.mu.Unlock()
			if reservation.state != state || state.constructionPhase != generatedDomainConstructionRunning ||
				state.constructionView != view || state.constructionActivity != kind || view.typedActive != 1 {
				releaseErr = deploymentexecution.ErrInvalidConfiguration
				return
			}
			view.typedActive = 0
			close(view.typedIdle)
		})
		return releaseErr
	}, nil
}

func completeGeneratedDomainConstruction(reservation *generatedDomainReservation, result *generatedDomainConstruction) error {
	if !validGeneratedDomainConstructionResult(reservation, result) {
		return deploymentexecution.ErrInvalidConfiguration
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	state := reservation.state
	viewReleased := false
	if state != nil && state.constructionView != nil {
		state.constructionView.mu.Lock()
		viewReleased = state.constructionView.typedActive == 0
		state.constructionView.mu.Unlock()
	}
	validActivity := state != nil && ((state.constructionActivity == generatedDomainConstructionActivityNone &&
		state.constructionView == nil && state.transition == nil) ||
		(state.constructionActivity == generatedDomainConstructionActivityEmpty && state.transition != nil &&
			state.constructionView == result.manager.domainView && viewReleased) ||
		(state.constructionActivity == generatedDomainConstructionActivityCold && state.transition == nil &&
			state.constructionView == result.manager.domainView && viewReleased))
	if state == nil || state.constructionPhase != generatedDomainConstructionRunning || state.construction != nil ||
		!validActivity {
		return deploymentexecution.ErrInvalidConfiguration
	}
	state.construction = result
	state.constructionPhase = generatedDomainConstructionComplete
	close(state.constructionDone)
	return nil
}

func failGeneratedDomainConstruction(reservation *generatedDomainReservation, err error) {
	if reservation == nil {
		return
	}
	if err == nil {
		err = deploymentexecution.ErrInvalidConfiguration
	}
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	state := reservation.state
	if state == nil || state.constructionPhase != generatedDomainConstructionRunning {
		return
	}
	state.constructionErr = err
	state.constructionPhase = generatedDomainConstructionFailed
	close(state.constructionDone)
}

func waitGeneratedConstructionTickets(ctx context.Context, tickets []*generatedLegacyConstructionTicket) error {
	for _, ticket := range tickets {
		if ticket == nil {
			return errors.New("generated ingress construction activity is invalid")
		}
		if err := waitGeneratedSignal(ctx, ticket.done); err != nil {
			return err
		}
	}
	return nil
}

func waitGeneratedSignal(ctx context.Context, signal <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-signal:
		return nil
	}
}

func waitGeneratedDomainReservation(ctx context.Context, state *generatedDomainState) (*generatedDomainState, error) {
	if state == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	if err := waitGeneratedSignal(ctx, state.done); err != nil {
		return state, err
	}
	generatedDomains.mu.Lock()
	err := state.err
	generatedDomains.mu.Unlock()
	return state, err
}

func failGeneratedDomainReservation(reservation *generatedDomainReservation, err error, closeEpoch bool) {
	generatedDomains.mu.Lock()
	defer generatedDomains.mu.Unlock()
	state := reservation.state
	if state.failed || state.fenceReady {
		return
	}
	state.failed = true
	state.err = err
	if closeEpoch {
		closeGeneratedPairingEpochLocked()
	}
	close(state.done)
}

func closeGeneratedPairingEpochLocked() {
	if generatedDomains.pairingEpoch == 0 {
		return
	}
	generatedDomains.pairingEpoch = 0
	close(generatedDomains.pairingIdle)
}

func (m *Manager) enterGeneratedDomainTypedMutation(ctx context.Context) (func(), error) {
	if m == nil || m.domainView == nil || ctx == nil {
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	view := m.domainView
	generatedDomains.mu.Lock()
	view.mu.Lock()
	if view.manager == nil || view.stale || view.pairing || generatedDomains.pairingEpoch != 0 {
		view.mu.Unlock()
		generatedDomains.mu.Unlock()
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	if len(generatedDomains.reservations) == 0 {
		beginGeneratedTypedActivityLocked(view)
		view.mu.Unlock()
		generatedDomains.mu.Unlock()
		return generatedTypedActivityLeave(view), nil
	}
	beginGeneratedInspectionActivityLocked(view)
	view.mu.Unlock()
	generatedDomains.mu.Unlock()

	identity, root, err := inspectGeneratedLegacyManagerIdentity(ctx, m, view)

	generatedDomains.mu.Lock()
	view.mu.Lock()
	endGeneratedInspectionActivityLocked(view)
	if err != nil || view.manager == nil || view.stale || view.pairing || generatedDomains.pairingEpoch != 0 {
		view.mu.Unlock()
		generatedDomains.mu.Unlock()
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	if view.identityBound && !sameGeneratedManagerPhysicalIdentity(view.identity, identity) {
		view.stale = true
		view.generation++
		view.mu.Unlock()
		generatedDomains.mu.Unlock()
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	owned := false
	ownedView := false
	for _, reservation := range generatedDomains.reservations {
		reservationOwnsView := generatedDomainReservationOwnsViewLocked(reservation, view)
		if reservationOwnsView {
			ownedView = true
		}
		if generatedDomainReservationOwnsManagerLocked(reservation, m, view, identity) {
			owned = true
			continue
		}
		// A forged reference family that retained the exact private view has
		// no authority, but it also cannot make the anchored owner conflict
		// with its own reservation. Physical drift is rejected above against
		// the view's bound identity before this ownership decision.
		if reservationOwnsView {
			continue
		}
		if generatedLegacyRootConflict(identity, reservation.input.physical) ||
			generatedLegacyDaemonNamespaceConflict(identity, reservation.input.physical) {
			view.stale = true
			view.generation++
			view.mu.Unlock()
			generatedDomains.mu.Unlock()
			return nil, &Error{Code: DiagnosticRouteUnresolved}
		}
	}
	if ownedView && !owned {
		// A caller that retained the exact private view but not its complete
		// Manager reference family has no authority. Do not let that forged
		// alias poison the anchored owner merely by presenting the shared view.
		view.mu.Unlock()
		generatedDomains.mu.Unlock()
		return nil, &Error{Code: DiagnosticRouteUnresolved}
	}
	view.root = root
	view.identity = identity
	view.identityBound = true
	beginGeneratedTypedActivityLocked(view)
	view.mu.Unlock()
	generatedDomains.mu.Unlock()
	return generatedTypedActivityLeave(view), nil
}

func generatedDomainReservationOwnsManagerLocked(
	reservation *generatedDomainReservation,
	manager *Manager,
	view *generatedManagerView,
	identity generatedManagerPhysicalIdentity,
) bool {
	if reservation == nil || reservation.state == nil ||
		reservation.state.constructionPhase != generatedDomainConstructionComplete ||
		reservation.state.construction == nil || manager == nil || manager.domainView != view {
		return false
	}
	owner := reservation.state.construction.manager
	if owner == nil || view.manager != owner || owner.domainView != view ||
		!sameGeneratedDomainManagerReferenceFamily(owner, manager) {
		return false
	}
	expected := reservation.input.physical.manager
	if reservation.state.transition != nil {
		expected = reservation.state.transition.physical.manager
	}
	return sameGeneratedManagerPhysicalIdentity(expected, identity)
}

func generatedDomainReservationOwnsViewLocked(reservation *generatedDomainReservation,
	view *generatedManagerView,
) bool {
	return reservation != nil && reservation.state != nil &&
		reservation.state.constructionPhase == generatedDomainConstructionComplete &&
		reservation.state.construction != nil && reservation.state.construction.manager != nil &&
		view != nil && view.manager == reservation.state.construction.manager &&
		reservation.state.construction.manager.domainView == view
}

func sameGeneratedDomainManagerReferenceFamily(owner, alias *Manager) bool {
	if owner == nil || alias == nil || owner.domainView == nil || owner.domainView != alias.domainView ||
		!sameGeneratedPointer(owner.runner, alias.runner) || owner.store == nil || owner.store != alias.store ||
		!sameGeneratedManagerIdentityOptions(owner.options, alias.options) ||
		owner.options.RebindFenceCheck == nil || alias.options.RebindFenceCheck == nil ||
		owner.options.generatedDomainRebindRepository == nil ||
		owner.options.generatedDomainRebindRepository != alias.options.generatedDomainRebindRepository ||
		!sameGeneratedPointer(owner.options.RebindCurrentStateRepository,
			alias.options.RebindCurrentStateRepository) ||
		!reflect.DeepEqual(owner.dockerEnv, alias.dockerEnv) ||
		!sameGeneratedDomainFileIdentity(owner.workingDirectoryIdentity, alias.workingDirectoryIdentity) ||
		owner.gatewayRebindFailStop == nil || owner.gatewayRebindFailStop != alias.gatewayRebindFailStop ||
		owner.gatewayRebindCommitBarrier == nil || owner.gatewayRebindCommitBarrier != alias.gatewayRebindCommitBarrier ||
		!sameGeneratedDomainManagerHooks(owner, alias) ||
		!sameGeneratedDomainManagerDriverFamily(owner, alias) {
		return false
	}
	return reflect.DeepEqual(owner.gatewayRebindTerminalReleaseEvent, alias.gatewayRebindTerminalReleaseEvent)
}

func sameGeneratedDomainManagerHooks(owner, alias *Manager) bool {
	return owner.gatewayRebindV2NetworkObserver == nil && alias.gatewayRebindV2NetworkObserver == nil &&
		owner.gatewayRebindAfterClaim == nil && alias.gatewayRebindAfterClaim == nil &&
		owner.gatewayRebindAfterPreparedCheckpoint == nil && alias.gatewayRebindAfterPreparedCheckpoint == nil &&
		owner.gatewayRebindAfterPreparedIntent == nil && alias.gatewayRebindAfterPreparedIntent == nil &&
		owner.gatewayRebindClock == nil && alias.gatewayRebindClock == nil &&
		owner.gatewayTopologyObserver == nil && alias.gatewayTopologyObserver == nil &&
		owner.gatewayCandidateObserver == nil && alias.gatewayCandidateObserver == nil &&
		owner.gatewayMixedRestartObserver == nil && alias.gatewayMixedRestartObserver == nil &&
		owner.gatewayV2UpgradeDriver == nil && alias.gatewayV2UpgradeDriver == nil &&
		owner.gatewayV2TransferDriver == nil && alias.gatewayV2TransferDriver == nil &&
		owner.gatewayV2CoordinatorDriver == nil && alias.gatewayV2CoordinatorDriver == nil &&
		owner.gatewayV2LANGrantDriver == nil && alias.gatewayV2LANGrantDriver == nil
}

func sameGeneratedDomainManagerDriverFamily(owner, alias *Manager) bool {
	ownerRebind, ownerOK := owner.gatewayRebindCrossStoreDriver.(managerGatewayRebindCrossStoreDriver)
	aliasRebind, aliasOK := alias.gatewayRebindCrossStoreDriver.(managerGatewayRebindCrossStoreDriver)
	if !ownerOK || !aliasOK || !generatedDomainRebindDriverAnchored(ownerRebind, owner) ||
		!generatedDomainRebindDriverAnchored(aliasRebind, owner) {
		return false
	}
	ownerCurrent, ownerOK := owner.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	aliasCurrent, aliasOK := alias.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	return ownerOK && aliasOK && generatedDomainCurrentDriverAnchored(ownerCurrent, owner) &&
		generatedDomainCurrentDriverAnchored(aliasCurrent, owner)
}

func generatedDomainRebindDriverAnchored(driver managerGatewayRebindCrossStoreDriver, owner *Manager) bool {
	stage, stageOK := driver.stage.(gatewayRebindTypedStageRuntime)
	handover, handoverOK := driver.handover.(gatewayRebindTypedHandoverRuntime)
	return driver.manager == owner && driver.repository == nil && driver.forwardSessionGuard == nil &&
		driver.rollbackStartGuard == nil && stageOK && handoverOK && stage.manager == owner &&
		handover.manager == owner && handover.stage.manager == owner
}

func generatedDomainCurrentDriverAnchored(driver managedGatewayCurrentPhysicalDriver, owner *Manager) bool {
	runtime, ok := driver.runtime.(managerGatewayCurrentPhysicalRuntime)
	return ok && driver.managerGatewayCurrentPhysicalDriver.manager == owner && runtime.manager == owner
}

func beginGeneratedTypedActivityLocked(view *generatedManagerView) {
	if view.typedActive == 0 {
		view.typedIdle = make(chan struct{})
	}
	view.typedActive++
}

func beginGeneratedInspectionActivityLocked(view *generatedManagerView) {
	if view.inspectionActive == 0 {
		view.inspectionIdle = make(chan struct{})
	}
	view.inspectionActive++
}

func endGeneratedInspectionActivityLocked(view *generatedManagerView) {
	view.inspectionActive--
	if view.inspectionActive == 0 {
		close(view.inspectionIdle)
	}
}

func generatedTypedActivityLeave(view *generatedManagerView) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			view.mu.Lock()
			view.typedActive--
			if view.typedActive == 0 {
				close(view.typedIdle)
			}
			view.mu.Unlock()
		})
	}
}
