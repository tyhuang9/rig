package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type liveGatewayRebindFinalConfigCopyDriver struct {
	managerGatewayRebindFinalConfigCopyDriver
	copyCalls int
	loseAck   bool
}

func (d *liveGatewayRebindFinalConfigCopyDriver) copyFinalConfig(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, contents []byte,
) error {
	d.copyCalls++
	if err := d.managerGatewayRebindFinalConfigCopyDriver.copyFinalConfig(ctx, intent, stage, contents); err != nil {
		return err
	}
	if d.loseAck {
		return errors.New("injected acknowledgment loss after actual Docker final config copy")
	}
	return nil
}

func TestLiveGatewayRebindSuccessorFinalConfigCopy(t *testing.T) {
	liveGatewayRebindSuccessorFinalConfigCopy(t, false)
}

func TestLiveGatewayRebindSuccessorFinalConfigCopyLostAcknowledgmentAdopts(t *testing.T) {
	liveGatewayRebindSuccessorFinalConfigCopy(t, true)
}

func liveGatewayRebindSuccessorFinalConfigCopy(t *testing.T, loseAck bool) {
	t.Helper()
	mode := "final-copy"
	if loseAck {
		mode += "-lost-ack"
	}
	// The shared journey retains its stage-start checks, immutable predecessor,
	// SQLite/route comparisons, application-request counter, and exact cleanup.
	liveGatewayRebindSuccessorStageStart(t, mode, func(fixture *liveGatewayV2Fixture,
		repository *appaccess.Repository, intent gatewayRebindProtectedIntent, baseTime time.Time,
	) {
		manager := fixture.ingress
		ten, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(ten.Progress) != 10 {
			t.Fatal("final config live journey lacks verified serving sequence ten")
		}
		tenBytes := liveGatewayRebindProgressBytes(t, ten)
		reads := liveGatewayRebindStageProductionReads(manager)
		if err := manager.prepareGatewayRebindSuccessorFinalConfigIntent(fixture.ctx, repository,
			reads, manager.inspectGatewayRebindDocker, baseTime.Add(11*time.Nanosecond), nil); err != nil {
			failLiveIngress(t, "prepare exact final configuration intent", err)
		}
		eleven, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(eleven.Progress) != 11 || eleven.Progress[10].Record.Stage == nil ||
			eleven.Progress[10].Record.Stage.FinalConfigIntent == nil {
			t.Fatal("final config live journey did not retain sequence eleven")
		}
		elevenBytes := liveGatewayRebindProgressBytes(t, eleven)
		for index := range tenBytes {
			if !bytes.Equal(tenBytes[index], elevenBytes[index]) {
				t.Fatalf("final config intent rewrote sequence %d", index+1)
			}
		}
		stage := *eleven.Progress[10].Record.Stage
		stageBytes, err := gatewayRebindStageConfigBytes(intent)
		if err != nil {
			t.Fatal("regenerate original stage configuration")
		}
		defer clear(stageBytes)
		activeBytes, err := gatewayRebindFinalConfigBytes(intent, stage.FinalConfigIntent.RoutePlan)
		if err != nil {
			t.Fatal("regenerate authorized final application configuration")
		}
		defer clear(activeBytes)
		driver := &liveGatewayRebindFinalConfigCopyDriver{
			managerGatewayRebindFinalConfigCopyDriver: managerGatewayRebindFinalConfigCopyDriver{manager: manager},
			loseAck: loseAck,
		}
		inventory, err := driver.finalConfigVolumeInventory(fixture.ctx, intent, stage, stageBytes, activeBytes)
		if err != nil || inventory != gatewayRebindFinalConfigInventoryStageOnly {
			t.Fatal("sequence eleven did not start with the exact stage-only volume")
		}
		copyErr := manager.copyGatewayRebindSuccessorFinalConfigWithDriver(fixture.ctx, repository,
			reads, manager.inspectGatewayRebindDocker, driver, baseTime.Add(12*time.Nanosecond), nil)
		if driver.copyCalls != 1 {
			t.Fatalf("initial final configuration copy calls=%d", driver.copyCalls)
		}
		if loseAck {
			if copyErr == nil {
				t.Fatal("lost Docker copy acknowledgment was reported as successful")
			}
			uncertain, scanErr := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || !reflect.DeepEqual(elevenBytes, liveGatewayRebindProgressBytes(t, uncertain)) {
				t.Fatal("lost acknowledgment advanced or changed protected sequence eleven")
			}
			inventory, err = driver.finalConfigVolumeInventory(fixture.ctx, intent, stage, stageBytes, activeBytes)
			if err != nil || inventory != gatewayRebindFinalConfigInventoryExactPair {
				t.Fatal("real copy did not leave the exact authorized pair before adoption")
			}
			if err := manager.prepareGatewayRebindSuccessorFinalConfigIntent(fixture.ctx, repository,
				reads, manager.inspectGatewayRebindDocker, baseTime.Add(13*time.Nanosecond), nil); err == nil {
				t.Fatal("old sequence-eleven replay accepted an unexpected active configuration")
			}
			manager, err = New(fixture.runner, fixture.ingress.options)
			if err != nil {
				t.Fatal("restart Manager after uncertain final configuration copy")
			}
			adopter := &liveGatewayRebindFinalConfigCopyDriver{
				managerGatewayRebindFinalConfigCopyDriver: managerGatewayRebindFinalConfigCopyDriver{manager: manager},
			}
			copyErr = manager.copyGatewayRebindSuccessorFinalConfigWithDriver(fixture.ctx, repository,
				liveGatewayRebindStageProductionReads(manager), manager.inspectGatewayRebindDocker,
				adopter, baseTime.Add(14*time.Nanosecond), nil)
			if adopter.copyCalls != 0 {
				t.Fatal("fresh Manager overwrote the exact previously copied final configuration")
			}
		}
		if copyErr != nil {
			failLiveIngress(t, "copy or adopt exact final configuration", copyErr)
		}
		twelve, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(twelve.Progress) != 12 || twelve.Progress[11].Record.Stage == nil ||
			twelve.Progress[11].Record.Stage.FinalConfigCopy == nil {
			t.Fatal("final configuration copy did not retain sequence twelve")
		}
		twelveBytes := liveGatewayRebindProgressBytes(t, twelve)
		for index := range elevenBytes {
			if !bytes.Equal(elevenBytes[index], twelveBytes[index]) {
				t.Fatalf("final configuration copy rewrote sequence %d", index+1)
			}
		}
		if !liveGatewayRebindFinalConfigCleanupInventory(t, fixture, fixture.ctx, intent, twelve) {
			t.Fatal("verified sequence twelve did not retain its exact file pair")
		}
		live, err := driver.liveConfig(fixture.ctx, stage.StageContainer.ID)
		if err != nil || !sameCaddyConfig(stageBytes, live) {
			clear(live)
			t.Fatal("copy activated final application configuration or changed live stage configuration")
		}
		clear(live)
		replayed, err := New(fixture.runner, fixture.ingress.options)
		if err != nil {
			t.Fatal("restart Manager before final configuration receipt replay")
		}
		replayDriver := &liveGatewayRebindFinalConfigCopyDriver{
			managerGatewayRebindFinalConfigCopyDriver: managerGatewayRebindFinalConfigCopyDriver{manager: replayed},
		}
		if err := replayed.copyGatewayRebindSuccessorFinalConfigWithDriver(fixture.ctx, repository,
			liveGatewayRebindStageProductionReads(replayed), replayed.inspectGatewayRebindDocker,
			replayDriver, baseTime.Add(15*time.Nanosecond), nil); err != nil {
			failLiveIngress(t, "fresh Manager final configuration receipt replay", err)
		}
		replayedHistory, err := replayed.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || replayDriver.copyCalls != 0 ||
			!reflect.DeepEqual(twelveBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) {
			t.Fatal("final configuration replay copied again or changed protected history")
		}
	})
}

// Used only by disposable live-fixture cleanup after full protected lineage
// validation. Sequence eleven permits the two certain physical outcomes of an
// uncertain copy; sequence twelve must prove the complete authorized pair.
func liveGatewayRebindFinalConfigCleanupInventory(t *testing.T, fixture *liveGatewayV2Fixture,
	ctx context.Context, intent gatewayRebindProtectedIntent, history gatewayRebindProtectedIntentHistory,
) bool {
	t.Helper()
	if len(history.Progress) != 11 && len(history.Progress) != 12 {
		t.Error("live final config cleanup has no exact authorized phase; retaining resources")
		return false
	}
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.FinalConfigIntent == nil {
		t.Error("live final config cleanup has no protected final configuration; retaining resources")
		return false
	}
	stageBytes, stageErr := gatewayRebindStageConfigBytes(intent)
	defer clear(stageBytes)
	activeBytes, activeErr := gatewayRebindFinalConfigBytes(intent, stage.FinalConfigIntent.RoutePlan)
	defer clear(activeBytes)
	if stageErr != nil || activeErr != nil {
		t.Error("live final config cleanup cannot regenerate the authorized files; retaining resources")
		return false
	}
	driver := managerGatewayRebindFinalConfigCopyDriver{manager: fixture.ingress}
	inventory, err := driver.finalConfigVolumeInventory(ctx, intent, *stage, stageBytes, activeBytes)
	if err != nil || (inventory != gatewayRebindFinalConfigInventoryStageOnly && inventory != gatewayRebindFinalConfigInventoryExactPair) ||
		(len(history.Progress) == 12 && inventory != gatewayRebindFinalConfigInventoryExactPair) {
		t.Error("live final config cleanup cannot prove the exact authorized inventory; retaining resources")
		return false
	}
	return true
}

var _ gatewayRebindFinalConfigCopyDriver = (*liveGatewayRebindFinalConfigCopyDriver)(nil)
