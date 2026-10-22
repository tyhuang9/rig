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
	return left.stores == right.stores && left.repository == right.repository &&
		left.database == right.database && left.guard == right.guard && left.plans == right.plans &&
		left.configuration == right.configuration && sameGeneratedPointer(left.runner, right.runner) &&
		sameGeneratedPointer(left.environment, right.environment) && left.options == right.options &&
		sameGeneratedDomainPhysicalIdentity(left.physical, right.physical)
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
	return reserveGeneratedDomainAfterPairing(ctx, input, false)
}

func reserveGeneratedDomainAfterPairing(
	ctx context.Context,
	input generatedDomainReservationInput,
	current bool,
) (*generatedDomainState, error) {
	generatedDomains.mu.Lock()
	for _, existing := range generatedDomains.reservations {
		if sameGeneratedDomainReservationInput(existing.input, input) {
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
		return reserveGeneratedDomainAfterPairing(ctx, input, false)
	}
	if !current {
		generatedDomains.mu.Unlock()
		if !input.current() {
			return nil, deploymentexecution.ErrInvalidConfiguration
		}
		return reserveGeneratedDomainAfterPairing(ctx, input, true)
	}
	state := &generatedDomainState{done: make(chan struct{})}
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
	for _, reservation := range generatedDomains.reservations {
		if generatedLegacyRootConflict(identity, reservation.input.physical) ||
			generatedLegacyDaemonNamespaceConflict(identity, reservation.input.physical) {
			view.stale = true
			view.generation++
			view.mu.Unlock()
			generatedDomains.mu.Unlock()
			return nil, &Error{Code: DiagnosticRouteUnresolved}
		}
	}
	view.root = root
	view.identity = identity
	view.identityBound = true
	beginGeneratedTypedActivityLocked(view)
	view.mu.Unlock()
	generatedDomains.mu.Unlock()
	return generatedTypedActivityLeave(view), nil
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
