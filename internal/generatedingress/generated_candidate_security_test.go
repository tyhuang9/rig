package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedCandidateRejectedDiscoveryCannotRedirectCleanup(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := fixture.beginCreate(t, execution)
	permit := fixture.bindInvocation(t, invocation)
	lostAcknowledgement := errors.New("test create acknowledgement lost")
	fixture.runner.setResult("create", func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{}, lostAcknowledgement
	})
	if _, err := execution.CreateAndRecord(invocation); !errors.Is(err, lostAcknowledgement) {
		t.Fatalf("ambiguous create error = %v", err)
	}

	foreignID := strings.Repeat("f", 64)
	foreign := fixture.runner.configuredSnapshot(permit.policy, foreignID, uuid.NewString())
	foreignJSON, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	completedExit := generatedMigrationCompletedExitOne(t)
	var inspectedTargets []string
	fixture.runner.setResult("inspect", func(request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		target := request.Args[len(request.Args)-1]
		inspectedTargets = append(inspectedTargets, target)
		if target == permit.policy.identity.ContainerName {
			return runtimeprocess.CommandResult{Stdout: append([]byte(nil), foreignJSON...)}, nil
		}
		if target == foreignID {
			return runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + foreignID)}, completedExit
		}
		return runtimeprocess.CommandResult{}, errors.New("unexpected cleanup inspection target")
	})
	for attempt := 0; attempt < 2; attempt++ {
		err := execution.RemoveInactive(invocation)
		var diagnostic *GeneratedCandidateError
		if !errors.As(err, &diagnostic) || diagnostic.Code != "candidate_hardening_failed" {
			t.Fatalf("foreign discovery cleanup attempt %d = %v", attempt+1, err)
		}
	}
	if len(inspectedTargets) != 2 || inspectedTargets[0] != permit.policy.identity.ContainerName ||
		inspectedTargets[1] != permit.policy.identity.ContainerName {
		t.Fatal("rejected discovery redirected a later cleanup inspection")
	}
	if fixture.runner.count("stop") != 0 || fixture.runner.count("remove") != 0 {
		t.Fatal("foreign discovery authorized a destructive command")
	}
	current := fixture.validation.read(t)
	if len(current.components) != 1 || current.components[0].state != generatedruntimestate.ComponentStarting ||
		current.components[0].containerID != "" {
		t.Fatal("rejected discovery changed the durable candidate identity")
	}
	reservation.Release()
	assertCandidateSecurityCapacityRetained(t, fixture)
}

func TestGeneratedCandidateUnquiescedCleanupBlocksHealthAcrossPins(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	firstReservation := fixture.reserve(t)
	t.Cleanup(firstReservation.Release)
	first := fixture.pin(t, firstReservation)
	t.Cleanup(first.Leave)
	invocation := fixture.beginCreate(t, first)
	fixture.bindInvocation(t, invocation)
	if _, err := first.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if err := first.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, first)

	secondReservation := fixture.reserve(t)
	t.Cleanup(secondReservation.Release)
	second := fixture.pin(t, secondReservation)
	t.Cleanup(second.Leave)
	adopted, err := second.AdoptCandidate("api")
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner.setResult("stop", func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{}, runtimeprocess.ErrTerminationFailed
	})
	if err := first.RemoveInactive(invocation); !errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		t.Fatalf("uncertain cleanup error = %v", err)
	}
	if fixture.runner.count("stop") != 1 || fixture.runner.count("remove") != 0 {
		t.Fatal("uncertain stop did not prevent removal")
	}
	if err := second.WaitHealthyAndRecord(adopted); !errors.Is(err, errCandidateMutationUnquiesced) {
		t.Fatalf("health after another pin's uncertain mutation = %v", err)
	}
	current := fixture.validation.read(t)
	if current.runtime.phase != generatedruntimestate.PhaseWaitingHealth || len(current.components) != 1 ||
		current.components[0].state != generatedruntimestate.ComponentRunning {
		t.Fatal("uncertain mutation was followed by a positive health state change")
	}
	firstReservation.Release()
	secondReservation.Release()
	assertCandidateSecurityCapacityRetained(t, fixture)
}

func TestGeneratedCandidateCreatePreparationCleansStagedEnvironment(t *testing.T) {
	for _, test := range []struct {
		name          string
		stageFails    bool
		authorityLost bool
		cleanupFails  bool
	}{
		{name: "stager_returns_lease_and_error", stageFails: true},
		{name: "authority_changes_after_staging", authorityLost: true},
		{name: "stage_and_cleanup_errors_preserve_both_causes", stageFails: true, cleanupFails: true},
		{name: "authority_and_cleanup_errors_preserve_both_causes", authorityLost: true, cleanupFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCandidateOwnerFixture(t)
			reservation := fixture.reserve(t)
			t.Cleanup(reservation.Release)
			execution := fixture.pin(t, reservation)
			t.Cleanup(execution.Leave)
			invocation := fixture.beginCreate(t, execution)
			fixture.bindInvocation(t, invocation)
			stageFailure := errors.New("test staging failure")
			cleanupFailure := errors.New("test staged-file cleanup failure")
			if test.stageFails {
				fixture.environment.setStageError(stageFailure)
			}
			if test.cleanupFails {
				fixture.environment.setCleanupError(cleanupFailure)
			}
			sawProtectedBytes := false
			fixture.environment.setAfterStage(func(raw []byte) {
				for _, value := range raw {
					if value != 0 {
						sawProtectedBytes = true
						break
					}
				}
				if test.authorityLost {
					if _, err := fixture.validation.migration.repository.db.Exec(
						`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, fixture.validation.request.Claim.JobID()); err != nil {
						t.Fatal(err)
					}
				}
			})
			identity, err := execution.CreateAndRecord(invocation)
			want := error(deploymentexecution.ErrIdentityLost)
			if test.stageFails {
				want = stageFailure
			}
			if !errors.Is(err, want) {
				t.Fatalf("primary preparation failure = %v", err)
			}
			if test.cleanupFails && !errors.Is(err, cleanupFailure) {
				t.Fatalf("cleanup failure cause was lost: %v", err)
			}
			if identity != (CandidateIdentity{}) || fixture.runner.count("create") != 0 {
				t.Fatal("failed preparation granted a candidate result or entered create")
			}
			snapshot := fixture.environment.snapshot()
			if !sawProtectedBytes || snapshot.stageCalls != 1 || snapshot.cleanupCalls != 1 {
				t.Fatal("failed preparation did not attempt immediate staged-file cleanup")
			}
			assertCandidateSecurityStagingCleared(t, snapshot)
			if test.cleanupFails {
				fixture.environment.setCleanupError(nil)
				execution.Leave()
				if fixture.environment.snapshot().cleanupCalls != 2 {
					t.Fatal("failed cleanup lease was not retained for Leave to retry")
				}
			} else {
				execution.Leave()
				if fixture.environment.snapshot().cleanupCalls != 1 {
					t.Fatal("Leave repeated successful secret-file cleanup")
				}
			}
		})
	}
}

func TestGeneratedCandidateUnquiescedCreateRetainsStagedEnvironment(t *testing.T) {
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)
	fixture.runner.setResult("create", func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{}, runtimeprocess.ErrTerminationFailed
	})
	identity, err := execution.CreateAndRecord(invocation)
	if !errors.Is(err, runtimeprocess.ErrTerminationFailed) || identity != (CandidateIdentity{}) {
		t.Fatalf("unquiesced create outcome = %v", err)
	}
	snapshot := fixture.environment.snapshot()
	if snapshot.stageCalls != 1 || snapshot.cleanupCalls != 0 {
		t.Fatal("unjoined create did not retain its staged environment")
	}
	assertCandidateSecurityStagingCleared(t, snapshot)
	if err := execution.RemoveInactive(invocation); err == nil {
		t.Fatal("unjoined create allowed explicit cleanup")
	}
	execution.Leave()
	if fixture.environment.snapshot().cleanupCalls != 0 || fixture.runner.count("stop") != 0 || fixture.runner.count("remove") != 0 {
		t.Fatal("Leave or cleanup deleted resources needed by an unjoined create")
	}
	reservation.Release()
	assertCandidateSecurityCapacityRetained(t, fixture)
}

func assertCandidateSecurityStagingCleared(t *testing.T, snapshot candidateOwnerEnvironmentSnapshot) {
	t.Helper()
	defer clear(snapshot.captured)
	if len(snapshot.captured) == 0 || len(snapshot.raw) == 0 {
		t.Fatal("fixture did not stage protected runtime bytes")
	}
	for _, value := range snapshot.raw {
		if value != 0 {
			t.Fatal("staged input bytes were not cleared")
		}
	}
}

func assertCandidateSecurityCapacityRetained(t *testing.T, fixture *candidateOwnerFixture) {
	t.Helper()
	leases, err := fixture.authority.budget.Acquire(context.Background(), generatedcapacity.Limits{
		MemoryBytes: uint64(fixture.authority.options.MemoryBytes),
		DiskBytes:   fixture.authority.options.ReplacementDiskBytes,
	}, 1)
	for _, lease := range leases {
		lease.Release()
	}
	if !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("competing admission after retained candidate = %v", err)
	}
}
