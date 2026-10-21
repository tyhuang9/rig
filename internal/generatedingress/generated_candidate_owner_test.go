package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type embeddedGeneratedReplacementReservation struct {
	GeneratedReplacementReservation
}
type embeddedGeneratedCandidateInvocation struct{ GeneratedCandidateInvocation }

func TestGeneratedCandidateOwnerCreatesStartsAndHandsOffExactCandidate(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	defer reservation.Release()
	execution := fixture.pin(t, reservation)
	defer execution.Leave()
	invocation := fixture.beginCreate(t, execution)
	permit := fixture.bindInvocation(t, invocation)

	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ReceiptID != permit.receipt.id || identity.ContainerID != fixture.runner.containerID ||
		identity.ContainerName != permit.policy.identity.ContainerName || identity.NetworkName != permit.policy.identity.NetworkName ||
		identity.NetworkAlias != permit.policy.identity.NetworkAlias || identity.InternalPort != permit.policy.internalPort {
		t.Fatalf("created identity = %#v", identity)
	}
	environment := fixture.environment.snapshot()
	if environment.stageCalls != 1 || environment.cleanupCalls != 1 || environment.operationID != permit.receipt.environmentOperationID ||
		environment.attempt != permit.receipt.environmentStagingAttempt || len(environment.captured) == 0 ||
		!bytes.Contains(environment.captured, []byte("OWNER_SECRET=")) {
		t.Fatalf("environment lifecycle calls=%d/%d operation=%q attempt=%d captured=%d",
			environment.stageCalls, environment.cleanupCalls, environment.operationID, environment.attempt, len(environment.captured))
	}
	if !candidateOwnerAllZero(environment.raw) {
		t.Fatal("staged environment input was retained after staging")
	}
	for _, request := range fixture.runner.requests() {
		material := strings.Join(append(append([]string(nil), request.Args...), request.Env...), "\n")
		if strings.Contains(material, "candidate-owner-runtime-secret") || strings.Contains(material, "OWNER_SECRET=") {
			t.Fatal("candidate secret appeared in process request")
		}
	}

	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	state, err := execution.FinishHealth()
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != generatedruntimestate.PhaseSwitchingRoute || len(state.Components) != 1 ||
		state.Components[0].State != generatedruntimestate.ComponentHealthy {
		t.Fatalf("handoff state = %#v", state)
	}
	observed, err := execution.ObserveRouteCandidate("api")
	if err != nil {
		t.Fatal(err)
	}
	if observed != identity {
		t.Fatalf("route identity = %#v, want %#v", observed, identity)
	}
	if fixture.runner.count(candidateOwnerCreate) != 1 || fixture.runner.count(candidateOwnerStart) != 1 ||
		fixture.runner.count(candidateOwnerStop) != 0 || fixture.runner.count(candidateOwnerRemove) != 0 {
		t.Fatalf("physical mutations create=%d start=%d stop=%d remove=%d",
			fixture.runner.count(candidateOwnerCreate), fixture.runner.count(candidateOwnerStart),
			fixture.runner.count(candidateOwnerStop), fixture.runner.count(candidateOwnerRemove))
	}
}

func TestGeneratedCandidateOwnerRejectsEmbeddedAndCrossScopeHandles(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	defer reservation.Release()
	wrappedReservation := embeddedGeneratedReplacementReservation{GeneratedReplacementReservation: reservation}
	if err := fixture.authority.RetainReplacementCapacity(wrappedReservation); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("embedded reservation retain error = %v", err)
	}
	if _, err := fixture.authority.PinCandidates(fixture.validation.migration.guarded.context, fixture.validation.request, wrappedReservation); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("embedded reservation pin error = %v", err)
	}

	execution := fixture.pin(t, reservation)
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)
	wrappedInvocation := embeddedGeneratedCandidateInvocation{GeneratedCandidateInvocation: invocation}
	if _, err := execution.CreateAndRecord(wrappedInvocation); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("embedded invocation create error = %v", err)
	}

	aliasReservation := fixture.reserve(t)
	defer aliasReservation.Release()
	alias := fixture.pin(t, aliasReservation)
	defer alias.Leave()
	if _, err := alias.CreateAndRecord(invocation); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("cross-scope invocation create error = %v", err)
	}
	execution.Leave()
	if _, err := execution.BeginCreate("api"); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("left execution error = %v", err)
	}
	if fixture.runner.count(candidateOwnerCreate) != 0 {
		t.Fatalf("sealed-handle refusals ran %d creates", fixture.runner.count(candidateOwnerCreate))
	}
}

func TestGeneratedCandidateOwnerCoalescesAliasObservationsWithoutDuplicateCommands(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	defer reservation.Release()
	execution := fixture.pin(t, reservation)
	defer execution.Leave()
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)
	if _, err := execution.BeginCreate("api"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate BeginCreate error = %v", err)
	}
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.CreateAndRecord(invocation); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate CreateAndRecord error = %v", err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}

	aliasReservation := fixture.reserve(t)
	defer aliasReservation.Release()
	alias := fixture.pin(t, aliasReservation)
	defer alias.Leave()
	if _, err := alias.BeginCreate("api"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("alias BeginCreate error = %v", err)
	}
	adopted, err := alias.AdoptCandidate("api")
	if err != nil {
		t.Fatal(err)
	}
	if err := alias.StartCandidate(adopted); err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	if fixture.runner.count(candidateOwnerCreate) != 1 || fixture.runner.count(candidateOwnerStart) != 1 {
		t.Fatalf("duplicate physical commands create=%d start=%d",
			fixture.runner.count(candidateOwnerCreate), fixture.runner.count(candidateOwnerStart))
	}
}

func TestGeneratedCandidateOwnerRechecksAuthorityAfterEnvironmentStaging(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	defer reservation.Release()
	execution := fixture.pin(t, reservation)
	defer execution.Leave()
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)

	var revokeErr error
	fixture.environment.setAfterStage(func([]byte) {
		_, revokeErr = fixture.validation.migration.repository.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`,
			fixture.validation.pins.claim.JobID())
	})
	if _, err := execution.CreateAndRecord(invocation); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("create after authority revocation error = %v", err)
	}
	if revokeErr != nil {
		t.Fatalf("revoke authority: %v", revokeErr)
	}
	if fixture.runner.count(candidateOwnerCreate) != 0 {
		t.Fatalf("revoked create ran %d physical commands", fixture.runner.count(candidateOwnerCreate))
	}
	environment := fixture.environment.snapshot()
	if environment.stageCalls != 1 || environment.cleanupCalls != 1 || len(environment.captured) == 0 || !candidateOwnerAllZero(environment.raw) {
		t.Fatalf("revoked create environment lifecycle calls=%d/%d captured=%d zeroed=%t",
			environment.stageCalls, environment.cleanupCalls, len(environment.captured), candidateOwnerAllZero(environment.raw))
	}
}

func TestGeneratedCandidateOwnerDoesNotHoldSQLiteWriterDuringProcessRun(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	db := fixture.validation.migration.repository.db
	if _, err := db.Exec(`CREATE TABLE candidate_owner_writer_probe(value INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	reservation := fixture.reserve(t)
	defer reservation.Release()
	execution := fixture.pin(t, reservation)
	defer execution.Leave()
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)

	var writerErr error
	fixture.runner.setBefore(candidateOwnerCreate, func(runtimeprocess.CommandRequest) {
		_, writerErr = db.ExecContext(context.Background(), `INSERT INTO candidate_owner_writer_probe(value) VALUES(1)`)
	})
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if writerErr != nil {
		t.Fatalf("independent writer during create: %v", writerErr)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM candidate_owner_writer_probe`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("writer probe count=%d error=%v", count, err)
	}
}

func candidateOwnerAllZero(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
