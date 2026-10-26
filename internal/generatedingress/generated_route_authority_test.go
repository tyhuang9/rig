package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const generatedRouteActivationImmutableTriggerTestSQL = `CREATE TRIGGER generated_route_activation_immutable
BEFORE UPDATE ON generated_route_activations
BEGIN SELECT RAISE(ABORT, 'generated route activations are immutable'); END`

func TestGeneratedRouteAuthorityCommitsReceiptHeadAndRosterWithOriginalClaim(t *testing.T) {
	fixture, execution, identity := generatedRouteCandidateFixture(t)
	manager, runner := newManagerFixture(t, false)
	runner.appID = fixture.validation.migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
	authority, err := newGeneratedRouteAuthority(manager, fixture.authority, fixture.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	authorityCopy := *authority
	if copied, err := authorityCopy.Pin(fixture.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity)); copied != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("copied route authority result=%#v error=%v", copied, err)
	}
	sealed, err := authority.Pin(fixture.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	state, done, err := execution.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	probe := generatedRouteActivationInput{receiptID: sealed.receiptID, appID: runner.appID,
		successorDeploymentID: state.pins.deploymentID, successorReleaseID: state.pins.releaseID,
		successorSlot: state.pins.candidateSlot, expectedHead: sealed.expectedHead,
		candidateRoster: sealed.candidateRoster, predecessorRoster: sealed.predecessorRoster,
		routeIntent: sealed.intent, committedRouteProof: []byte(`{}`)}
	if !validGeneratedRouteActivationInput(probe, state.pins.claim) {
		done()
		t.Fatalf("sealed activation input is invalid: %#v", probe)
	}
	if _, valid := canonicalGeneratedRouteActivationRoster(probe.candidateRoster); !valid {
		done()
		t.Fatalf("sealed candidate roster is invalid: %#v", probe.candidateRoster)
	}
	done()
	sealedCopy := *sealed
	if copied, err := sealedCopy.Activate(fixture.validation.migration.guarded.context); copied.DeploymentID != "" ||
		!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("copied sealed activation result=%#v error=%v", copied, err)
	}
	result, err := sealed.Activate(fixture.validation.migration.guarded.context)
	if err != nil {
		current, _ := generatedruntimestate.New(fixture.validation.migration.repository.db).
			Get(context.Background(), runner.appID, fixture.validation.request.DeploymentID)
		t.Fatalf("activate: %v expected=%#v candidate=%#v runtime=%#v", err,
			sealed.expectedHead, sealed.candidateRoster, current)
	}
	if result.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activated deployment phase = %q", result.Phase)
	}
	repository := generatedruntimestate.New(fixture.validation.migration.repository.db)
	head, err := repository.Active(context.Background(), runner.appID)
	if err != nil {
		t.Fatal(err)
	}
	if head.DeploymentID != fixture.validation.request.DeploymentID || head.Generation != 1 ||
		head.ActivationReceiptID == "" {
		t.Fatalf("activated head = %#v", head)
	}
	var receiptCount, activeCount int
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
		WHERE id=? AND app_id=? AND successor_deployment_id=?`, head.ActivationReceiptID, runner.appID,
		fixture.validation.request.DeploymentID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components
		WHERE deployment_id=? AND state='active'`, fixture.validation.request.DeploymentID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || activeCount != 1 {
		t.Fatalf("durable activation receipt=%d active-components=%d", receiptCount, activeCount)
	}
	if _, err := sealed.Activate(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("sealed activation replay error = %v", err)
	}
	if err := manager.Switch(context.Background(), generatedRouteRequest(runner.appID, identity)); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ordinary Manager.Switch error = %v", err)
	}
	if err := manager.Provision(context.Background()); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ordinary Manager.Provision error = %v", err)
	}
	if err := manager.Recover(context.Background()); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ordinary Manager.Recover error = %v", err)
	}
}

func TestGeneratedRouteAuthorityRejectsChangedOriginalClaimWithoutPartialSQLCommit(t *testing.T) {
	fixture, execution, identity := generatedRouteCandidateFixture(t)
	manager, runner := newManagerFixture(t, false)
	runner.appID = fixture.validation.migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
	authority, err := newGeneratedRouteAuthority(manager, fixture.authority, fixture.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := authority.Pin(fixture.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	sealedCopy := *sealed
	changed := false
	runner.gatewayProbeMutate = func(*ingressRunner) {
		if changed {
			return
		}
		changed = true
		if _, mutateErr := fixture.validation.migration.repository.db.Exec(`UPDATE jobs SET attempt=attempt+1
			WHERE id=?`, fixture.validation.migration.claim.JobID()); mutateErr != nil {
			t.Fatalf("change original claim: %v", mutateErr)
		}
	}
	if _, err := sealed.Activate(fixture.validation.migration.guarded.context); err == nil {
		t.Fatal("activation with a changed original Claim succeeded")
	}
	commandsAfterFailure := len(runner.commands)
	if copied, err := sealedCopy.Activate(fixture.validation.migration.guarded.context); copied.DeploymentID != "" ||
		!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("copied failed activation result=%#v error=%v", copied, err)
	}
	if len(runner.commands) != commandsAfterFailure {
		t.Fatalf("copied failed activation issued commands: before=%d after=%d", commandsAfterFailure, len(runner.commands))
	}
	if !changed {
		t.Fatal("claim did not change after sealed physical proof")
	}
	var receiptCount int
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).
		Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	head, err := generatedruntimestate.New(fixture.validation.migration.repository.db).
		Active(context.Background(), runner.appID)
	if err != nil {
		t.Fatal(err)
	}
	if receiptCount != 0 || head.Generation != 0 || head.DeploymentID != "" || head.ActivationReceiptID != "" {
		t.Fatalf("failed activation committed SQL receipt=%d head=%#v", receiptCount, head)
	}
	var phase generatedruntimestate.Phase
	var state generatedruntimestate.ComponentState
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT phase FROM generated_runtime_deployments
		WHERE deployment_id=?`, fixture.validation.request.DeploymentID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name='api'`, fixture.validation.request.DeploymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if phase != generatedruntimestate.PhaseSwitchingRoute || state != generatedruntimestate.ComponentHealthy {
		t.Fatalf("failed activation phase/state = %q/%q", phase, state)
	}
	stored, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stored.Active[runner.appID]; !exists {
		t.Fatal("first physical route was discarded without a durable predecessor")
	}
}

func TestGeneratedRouteAuthorityAttestPublishedAcceptsCurrentRouteAndRejectsPhysicalDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*generatedRouteAttestationFixture)
		check  func(*testing.T, error)
	}{
		{name: "current_claim", check: func(t *testing.T, err error) {
			if err != nil {
				t.Fatalf("current published route attestation: %v", err)
			}
		}},
		{name: "route_config_drift", mutate: func(f *generatedRouteAttestationFixture) {
			f.runner.endpointRoleLabel = "crossed-role"
		}, check: func(t *testing.T, err error) {
			if !IsCode(err, DiagnosticIngressDrift) {
				t.Fatalf("physical route drift error = %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedRouteAttestationFixture(t)
			if test.mutate != nil {
				test.mutate(fixture)
			}
			before := fixture.snapshot(t)
			commandCount := len(fixture.runner.commands)
			err := fixture.authority.AttestPublished(fixture.context, fixture.claim, fixture.deploymentID)
			test.check(t, err)
			fixture.assertUnchanged(t, before, commandCount)
		})
	}
}

func TestGeneratedRouteAuthorityAttestPublishedRejectsCurrentClaimAttemptDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		claim  func(*testing.T, *generatedRouteAttestationFixture) deploymentexecution.Claim
		mutate func(*testing.T, *generatedRouteAttestationFixture)
	}{
		{name: "future_claim", claim: func(t *testing.T, fixture *generatedRouteAttestationFixture) deploymentexecution.Claim {
			claim, err := deploymentexecution.NewClaim(fixture.candidate.validation.migration.binding,
				fixture.claim.JobID(), fixture.claim.ApplicationID(), fixture.claim.Attempt()+1)
			if err != nil {
				t.Fatal(err)
			}
			return claim
		}},
		{name: "stale_claim", mutate: func(t *testing.T, fixture *generatedRouteAttestationFixture) {
			if _, err := fixture.database.Exec(`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`,
				time.Now().UTC().Format(time.RFC3339Nano), fixture.claim.JobID()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedRouteAttestationFixture(t)
			claim := fixture.claim
			if test.mutate != nil {
				test.mutate(t, fixture)
			}
			if test.claim != nil {
				claim = test.claim(t, fixture)
			}
			before := fixture.snapshot(t)
			commandCount := len(fixture.runner.commands)
			err := fixture.authority.AttestPublished(fixture.context, claim, fixture.deploymentID)
			if !errors.Is(err, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("%s error = %v", test.name, err)
			}
			fixture.assertUnchanged(t, before, commandCount)
		})
	}
}

func TestGeneratedRouteAuthorityAttestPublishedRejectsReceiptCreatorLineage(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *generatedRouteAttestationFixture, string)
	}{
		{name: "crossed_creator_job", mutate: func(t *testing.T, fixture *generatedRouteAttestationFixture, receiptID string) {
			crossedJobID := uuid.NewString()
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := fixture.database.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,
				requested_by,input_json,created_at,updated_at)
				VALUES(?,'deploy','application',?,'running','running',1,?,'{}',?,?)`, crossedJobID,
				uuid.NewString(), fixture.candidate.validation.migration.guarded.actorID, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			generatedRouteMutateActivationReceiptForTest(t, fixture.database, receiptID,
				`UPDATE generated_route_activations SET creator_job_id=? WHERE id=?`, crossedJobID, receiptID)
		}},
		{name: "future_creator_attempt", mutate: func(t *testing.T, fixture *generatedRouteAttestationFixture, receiptID string) {
			generatedRouteMutateActivationReceiptForTest(t, fixture.database, receiptID,
				`UPDATE generated_route_activations SET creator_worker_attempt=? WHERE id=?`, fixture.claim.Attempt()+1, receiptID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedRouteAttestationFixture(t)
			head, err := generatedruntimestate.New(fixture.database).Active(context.Background(), fixture.claim.ApplicationID())
			if err != nil || head.ActivationReceiptID == "" {
				t.Fatalf("active route head = %#v, %v", head, err)
			}
			test.mutate(t, fixture, head.ActivationReceiptID)
			before := fixture.snapshot(t)
			commandCount := len(fixture.runner.commands)
			err = fixture.authority.AttestPublished(fixture.context, fixture.claim, fixture.deploymentID)
			if !errors.Is(err, deploymentexecution.ErrAuthorityStale) || !errors.Is(err, generatedruntimestate.ErrConflict) {
				t.Fatalf("%s error = %v", test.name, err)
			}
			fixture.assertUnchanged(t, before, commandCount)
		})
	}
}

type generatedRouteAttestationFixture struct {
	candidate    *candidateOwnerFixture
	authority    *GeneratedRouteAuthority
	runner       *ingressRunner
	database     *sql.DB
	context      context.Context
	claim        deploymentexecution.Claim
	deploymentID string
}

func newGeneratedRouteAttestationFixture(t *testing.T) *generatedRouteAttestationFixture {
	t.Helper()
	candidate, execution, identity := generatedRouteCandidateFixture(t)
	manager, runner := newManagerFixture(t, false)
	runner.appID = candidate.validation.migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
	authority, err := newGeneratedRouteAuthority(manager, candidate.authority, candidate.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := authority.Pin(candidate.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	result, err := sealed.Activate(candidate.validation.migration.guarded.context)
	if err != nil || result.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activate published route = %#v, %v", result, err)
	}
	return &generatedRouteAttestationFixture{candidate: candidate, authority: authority, runner: runner,
		database: candidate.validation.migration.repository.db,
		context:  candidate.validation.migration.guarded.context, claim: candidate.validation.migration.claim,
		deploymentID: candidate.validation.request.DeploymentID}
}

type generatedRouteAttestationSnapshot struct {
	receipt generatedruntimestate.RouteActivationReceipt
	head    generatedruntimestate.ActiveHead
	roster  []generatedruntimestate.RouteActivationComponent
	runtime generatedruntimestate.Deployment
	job     struct {
		status, phase string
		attempt       int
	}
	runner generatedRouteAttestationRunnerSnapshot
}

type generatedRouteAttestationRunnerSnapshot struct {
	liveConfig, startConfig []byte
	files                   map[string][]byte
	caddyNetworks           map[string]*networkAttachment
	ingressContainers       map[string]caddyNetworkContainerInspection
	stopped                 bool
	caddyMissing            bool
	ingressMissing          bool
	restartInstalls         int
}

func (fixture *generatedRouteAttestationFixture) snapshot(t *testing.T) generatedRouteAttestationSnapshot {
	t.Helper()
	tx, err := fixture.database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value := generatedRouteAttestationSnapshot{}
	value.head, err = readGeneratedRouteActivationHeadTx(context.Background(), tx, fixture.claim.ApplicationID())
	if err == nil {
		value.receipt, err = readGeneratedRouteActivationReceiptTx(context.Background(), tx, value.head.ActivationReceiptID)
	}
	if err == nil {
		value.roster, err = readGeneratedRouteActivationRosterTx(context.Background(), tx, fixture.deploymentID,
			generatedruntimestate.ComponentActive)
	}
	if err == nil {
		err = tx.QueryRow(`SELECT status,phase,attempt FROM jobs WHERE id=?`, fixture.claim.JobID()).
			Scan(&value.job.status, &value.job.phase, &value.job.attempt)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	value.runtime, err = generatedruntimestate.New(fixture.database).
		Get(context.Background(), fixture.claim.ApplicationID(), fixture.deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	value.runner = generatedRouteAttestationRunnerState(fixture.runner)
	return value
}

func (fixture *generatedRouteAttestationFixture) assertUnchanged(t *testing.T,
	before generatedRouteAttestationSnapshot, commandCount int,
) {
	t.Helper()
	after := fixture.snapshot(t)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("published route attestation mutated state:\nbefore=%#v\nafter=%#v", before, after)
	}
	for _, command := range fixture.runner.commands[commandCount:] {
		if !fixture.generatedRouteAttestationCommandReadOnly(command) {
			t.Fatalf("published route attestation dispatched a command outside the read-only allowlist: %#v", command)
		}
	}
}

func generatedRouteAttestationRunnerState(runner *ingressRunner) generatedRouteAttestationRunnerSnapshot {
	result := generatedRouteAttestationRunnerSnapshot{
		liveConfig: append([]byte(nil), runner.liveConfig...), startConfig: append([]byte(nil), runner.startConfig...),
		files: make(map[string][]byte, len(runner.files)), caddyNetworks: make(map[string]*networkAttachment, len(runner.caddyNetworks)),
		ingressContainers: make(map[string]caddyNetworkContainerInspection, len(runner.ingressContainers)),
		stopped:           runner.stopped, caddyMissing: runner.caddyMissing, ingressMissing: runner.ingressMissing,
		restartInstalls: runner.restartInstalls,
	}
	for path, contents := range runner.files {
		result.files[path] = append([]byte(nil), contents...)
	}
	for name, attachment := range runner.caddyNetworks {
		if attachment == nil {
			result.caddyNetworks[name] = nil
			continue
		}
		clone := *attachment
		clone.Aliases = append([]string(nil), attachment.Aliases...)
		result.caddyNetworks[name] = &clone
	}
	for id, container := range runner.ingressContainers {
		result.ingressContainers[id] = container
	}
	return result
}

func (fixture *generatedRouteAttestationFixture) generatedRouteAttestationCommandReadOnly(command []string) bool {
	caddyID := fixture.runner.caddyInspection().ID
	endpoint := fixture.runner.endpoint
	allowed := [][]string{
		{"image", "inspect", "--format", imageInspectFormat, caddyImage},
		{"container", "inspect", "--format", caddyInspectFormat, caddyContainerName},
		{"network", "inspect", "--format", networkInspectFormat, endpoint.NetworkName},
		{"network", "inspect", "--format", "json", caddyNetworkName},
		{"container", "exec", caddyID, "curl", "--silent", "--show-error", "--fail", "--max-time", "10", "http://127.0.0.1:2019/config/"},
		{"container", "inspect", "--format", endpointInspectFormat, endpoint.ContainerID},
		{"container", "exec", caddyID,
			"curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}",
			"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
			"http://" + net.JoinHostPort(endpoint.NetworkAlias+"."+endpoint.NetworkName,
				strconv.FormatUint(uint64(endpoint.InternalPort), 10)) + "/"},
	}
	for _, expected := range allowed {
		if reflect.DeepEqual(command, expected) {
			return true
		}
	}
	return false
}

func generatedRouteMutateActivationReceiptForTest(t *testing.T, database *sql.DB, receiptID, statement string,
	arguments ...any,
) {
	t.Helper()
	if _, err := database.Exec(`DROP TRIGGER generated_route_activation_immutable`); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			if _, err := database.Exec(generatedRouteActivationImmutableTriggerTestSQL); err != nil {
				t.Errorf("restore generated route activation trigger: %v", err)
			}
		}
	}()
	if _, err := database.Exec(statement, arguments...); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(generatedRouteActivationImmutableTriggerTestSQL); err != nil {
		t.Fatal(err)
	}
	restored = true
	var triggerSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='generated_route_activation_immutable'`).
		Scan(&triggerSQL); err != nil || strings.Join(strings.Fields(triggerSQL), " ") !=
		strings.Join(strings.Fields(generatedRouteActivationImmutableTriggerTestSQL), " ") {
		t.Fatalf("restored generated route activation trigger = %q, %v", triggerSQL, err)
	}
	if _, err := database.Exec(`UPDATE generated_route_activations SET creator_worker_attempt=creator_worker_attempt WHERE id=?`, receiptID); err == nil {
		t.Fatal("restored generated route activation trigger accepted an update")
	}
}

func generatedRouteCandidateFixture(t *testing.T) (*candidateOwnerFixture, *GeneratedCandidateExecution, CandidateIdentity) {
	t.Helper()
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	return fixture, execution, identity
}

func generatedRouteEndpoint(identity CandidateIdentity) generatedroute.RouteEndpoint {
	return generatedroute.RouteEndpoint{Component: identity.ComponentName, Role: identity.Role,
		ContainerID: identity.ContainerID, NetworkName: identity.NetworkName,
		NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort}
}

func generatedRouteRequest(appID string, identity CandidateIdentity) generatedroute.RouteSwitchRequest {
	return generatedroute.RouteSwitchRequest{AppID: appID, ToSlot: generatedidentity.Slot(identity.Slot),
		Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(identity)}}
}
