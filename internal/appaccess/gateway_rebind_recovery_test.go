package appaccess

import (
	"context"
	"strings"
	"testing"
)

func TestGatewayRebindRecoverySnapshotSeparatesActiveAndCurrentPhases(t *testing.T) {
	t.Run("prepared active over upgrade current", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Active == nil || snapshot.Phase != GatewayRebindPrepared ||
			snapshot.DatabaseCommittedEvent != nil || snapshot.DatabaseCommitObserved ||
			!snapshot.RollbackAllowed || snapshot.CurrentSource == nil ||
			snapshot.CurrentSource.Kind != GatewayRebindSourceGatewayUpgrade ||
			snapshot.CurrentDatabaseCommittedEvent != nil || len(snapshot.CurrentTransfers) != 0 {
			t.Fatalf("prepared snapshot=%#v", snapshot)
		}
	})

	t.Run("committed current has no active recovery phase", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		receipt := strings.Repeat("7", 64)
		commitGatewayRebindFixture(t, fixture, receipt)
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Active != nil || snapshot.Phase != "" || snapshot.DatabaseCommittedEvent != nil ||
			snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed || snapshot.CurrentSource == nil ||
			snapshot.CurrentSource.OperationID != fixture.claim.Spec.OperationID ||
			snapshot.CurrentDatabaseCommittedEvent == nil ||
			snapshot.CurrentDatabaseCommittedEvent.OperationID != fixture.claim.Spec.OperationID ||
			len(snapshot.CurrentTransfers) != 1 || len(snapshot.History) != 1 {
			t.Fatalf("committed snapshot=%#v", snapshot)
		}
	})

	t.Run("unresolved after database commit is roll forward only", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		receipt := strings.Repeat("7", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, receipt, nil)); err != nil {
			t.Fatal(err)
		}
		transfer := gatewayRebindTransferForFixture(t, fixture, receipt)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
				GatewayRebindDatabaseCommitted, receipt, []GatewayRebindAllocationTransfer{transfer})); err != nil {
			t.Fatal(err)
		}
		unresolved := gatewayRebindProofForFixture(t, fixture, GatewayRebindDatabaseCommitted, 3,
			GatewayRebindUnresolved, receipt, nil)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), unresolved); err != nil {
			t.Fatal(err)
		}
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Active == nil || snapshot.Phase != GatewayRebindUnresolved ||
			snapshot.DatabaseCommittedEvent == nil || !snapshot.DatabaseCommitObserved ||
			snapshot.RollbackAllowed || snapshot.CurrentSource == nil ||
			snapshot.CurrentSource.OperationID != fixture.claim.Spec.OperationID ||
			snapshot.CurrentDatabaseCommittedEvent == nil || len(snapshot.CurrentTransfers) != 1 {
			t.Fatalf("unresolved snapshot=%#v", snapshot)
		}
	})
}
