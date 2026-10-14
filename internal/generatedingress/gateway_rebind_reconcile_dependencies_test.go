package generatedingress

import (
	"context"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindReconcileRefusesMissingDependenciesBeforeAudit(t *testing.T) {
	fixture, driver, _ := newGatewayRebindTypedForwardOnlyFixture(t)
	manager := fixture.predecessor.manager
	readCurrent := gatewayRebindReconcileControlledCurrentReader(t, fixture)
	review := gatewayRebindReconcileForwardReview(t, fixture, fixture.predecessor.repository, readCurrent)
	request := GatewayRebindReconcileRequest{
		Version: GatewayRebindReconcileVersion, OperationID: fixture.attempt.Claim.Spec.OperationID,
		RecoveryDigest:         review.AuthorizationReview.RecoveryDigest,
		RecoveryApprovalDigest: review.AuthorizationReview.RecoveryApprovalDigest,
	}
	repository := &gatewayRebindReconcileTestRepository{Repository: fixture.predecessor.repository}
	before, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner.effects = nil

	for _, missing := range []string{
		"manager", "context", "repository", "session", "current proof reader",
		"driver manager", "driver from another manager",
	} {
		t.Run(missing, func(t *testing.T) {
			var callContext context.Context = context.Background()
			callManager, callDriver := manager, driver
			var callRepository GatewayRebindRecoveryRepository = repository
			sessionCalls, proofCalls := 0, 0
			session := func(context.Context) error {
				sessionCalls++
				return nil
			}
			var current gatewayRebindRecoveryCurrentProofReader = func(ctx context.Context,
				repository GatewayRebindRecoveryReadRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
				liveSession func(context.Context) error,
			) (gatewayRebindCommitCurrentProof, error) {
				proofCalls++
				return readCurrent(ctx, repository, snapshot, liveSession)
			}
			switch missing {
			case "manager":
				callManager = nil
			case "context":
				callContext = nil
			case "repository":
				callRepository = nil
			case "session":
				session = nil
			case "current proof reader":
				current = nil
			case "driver manager":
				callDriver.manager = nil
			case "driver from another manager":
				callDriver.manager = &Manager{}
			}
			response, reconcileErr := callManager.reconcileGatewayRebindWithRuntime(callContext,
				callRepository, request, gatewayRebindTestAdministrator, session, callDriver, current)
			if !IsCode(reconcileErr, DiagnosticValidationFailed) ||
				!reflect.DeepEqual(response, GatewayRebindReconcileResponse{}) ||
				repository.authorizeCall != 0 || sessionCalls != 0 || proofCalls != 0 || len(fixture.runner.effects) != 0 {
				t.Fatalf("dependency=%s validation=%t audit=%d sessions=%d proofs=%d effects=%d",
					missing, IsCode(reconcileErr, DiagnosticValidationFailed), repository.authorizeCall,
					sessionCalls, proofCalls, len(fixture.runner.effects))
			}
			after, snapshotErr := repository.GatewayRebindRecoverySnapshot(context.Background())
			afterFiles, filesErr := readGatewayHistorySnapshotMode(manager.store, true)
			if snapshotErr != nil || filesErr != nil || !reflect.DeepEqual(before, after) ||
				!sameGatewayHistorySnapshot(files, afterFiles) || manager.gatewayRebindAdmissionBlocked() {
				t.Fatalf("dependency=%s changed recovery state: snapshot_error=%v files_error=%v",
					missing, snapshotErr, filesErr)
			}
		})
	}
}
