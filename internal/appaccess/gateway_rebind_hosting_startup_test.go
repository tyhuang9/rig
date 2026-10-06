package appaccess

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

func TestHostingGatewayStartupRebindCensusUsesOneReadTransaction(t *testing.T) {
	dataRoot := t.TempDir()
	readerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })
	addUsers(t, readerDB)
	f := newGatewayRebindFixtureOnDB(t, readerDB, true)
	writerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })
	writer := New(writerDB)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	receipt := strings.Repeat("7", 64)
	transfer := gatewayRebindTransferForFixture(t, f, receipt)
	ready := gatewayRebindProofForFixture(t, f, GatewayRebindPrepared, 1, GatewayRebindSuccessorReady, receipt, nil)
	if _, err := f.repository.ApplyGatewayRebindTransition(ctx, ready); err != nil {
		t.Fatal(err)
	}
	committed := gatewayRebindProofForFixture(t, f, GatewayRebindSuccessorReady, 2, GatewayRebindDatabaseCommitted, receipt, []GatewayRebindAllocationTransfer{transfer})
	if _, err := f.repository.ApplyGatewayRebindTransition(ctx, committed); err != nil {
		t.Fatal(err)
	}
	before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || before.Active == nil || before.Phase != GatewayRebindDatabaseCommitted {
		t.Fatalf("database commit: %v", err)
	}
	called := false
	f.repository.afterGrantStartupClaimsRead = func() {
		f.repository.afterGrantStartupClaimsRead = nil
		called = true
		release := gatewayRebindProofForFixture(t, f, GatewayRebindDatabaseCommitted, 3, GatewayRebindCommitted, receipt, nil)
		release.LocalAttestationDigest = strings.Repeat("8", 64)
		if _, err := writer.ApplyGatewayRebindTransition(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !called || !reflect.DeepEqual(snapshot.Rebind, before) || !reflect.DeepEqual(snapshot.Upgrades.CurrentProfile, before.CurrentProfile) {
		t.Fatalf("startup mixed SQL transactions: hook=%t phase=%s error=%v", called, snapshot.Rebind.Phase, err)
	}
	after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || after.Rebind.Active != nil || after.Rebind.Phase != "" || reflect.DeepEqual(after.Rebind, before) ||
		!reflect.DeepEqual(after.Rebind.CurrentSource, before.CurrentSource) || !reflect.DeepEqual(after.Rebind.CurrentTransfers, before.CurrentTransfers) {
		t.Fatalf("fresh startup failed to observe release with immutable transfer history: %v", err)
	}
}
