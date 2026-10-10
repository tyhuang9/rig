package generatedingress

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

// Test teardown is deliberately separate from production recovery. A committed
// successor cannot be rolled back by the production entrypoint. This helper may
// remove only this disposable fixture's fully attested, terminal-bound resources;
// it never changes the terminal decision, source history, SQL or app networks.
func cleanupLiveGatewayRebindFinalHandover(t *testing.T, fixture *liveGatewayV2Fixture,
	repository *appaccess.Repository, intent gatewayRebindProtectedIntent,
) {
	t.Helper()
	if fixture == nil || fixture.ingress == nil || repository == nil || !validGatewayRebindProtectedIntent(intent) {
		t.Error("live handover cleanup lacks exact fixture authority; retaining resources")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || !reflect.DeepEqual(history.Intents[0].Intent, intent) {
		t.Error("live handover cleanup lacks exact protected lineage; retaining resources")
		return
	}
	if len(history.Progress) <= 12 {
		var networkID string
		var config *gatewayRebindStageConfigVolumeBinding
		var data *gatewayRebindStageDataVolumeBinding
		var container *gatewayRebindStageContainerBinding
		if len(history.Progress) != 0 {
			stage := history.Progress[len(history.Progress)-1].Record.Stage
			if stage != nil {
				if stage.Network != nil {
					networkID = stage.Network.ID
				}
				config, data, container = stage.ConfigVolume, stage.DataVolume, stage.StageContainer
			}
		}
		if networkID == "" {
			// No create response or durable binding may be recovered by name.
			observed, inspectErr := (managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}).inspect(ctx, intent)
			if inspectErr != nil || !validGatewayRebindStageNetworkAbsentObservation(intent, observed) {
				t.Error("live handover cleanup found an unbound network or unknown resources; retaining them")
			}
			return
		}
		cleanupLiveGatewayRebindStageStartChain(t, fixture, intent, networkID, config, data, container)
		return
	}
	last := history.Progress[len(history.Progress)-1].Record
	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	driver := newManagerGatewayRebindFinalHandoverDriver(fixture.ingress, reads, fixture.ingress.inspectGatewayRebindDocker)
	if last.Phase != gatewayRebindProgressHandoverCommitted && last.Phase != gatewayRebindProgressHandoverRolledBack {
		// Pending operations use the same explicit, durable rollback path as
		// recovery. An unbound create or unavailable original address stays fenced.
		if err := fixture.ingress.rollbackGatewayRebindFinalHandoverWithDriver(ctx, repository, driver, time.Now().UTC(), nil); err != nil {
			t.Error("live handover cleanup could not prove explicit rollback; retaining resources")
			return
		}
		history, err = fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(history.Progress) == 0 {
			t.Error("live handover cleanup cannot read rollback outcome; retaining resources")
			return
		}
		last = history.Progress[len(history.Progress)-1].Record
	}
	if len(history.Terminals) == 0 {
		if err := fixture.ingress.handoverGatewayRebindFinalWithDriver(ctx, repository, driver, time.Now().UTC(), nil); err != nil {
			t.Error("live handover cleanup cannot finish the exact terminal receipt; retaining resources")
			return
		}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, fixture.ingress.options.WorkingDirectory)
	if err != nil {
		t.Error("live handover cleanup cannot acquire effects lease; retaining resources")
		return
	}
	defer func() {
		if err := releaseEffects(); err != nil {
			t.Error("live handover cleanup effects lease release failed")
		}
	}()
	releaseGateway, err := fixture.ingress.lockGatewayRaw(ctx)
	if err != nil {
		t.Error("live handover cleanup cannot acquire gateway lock; retaining resources")
		return
	}
	defer func() {
		if err := releaseGateway(); err != nil {
			t.Error("live handover cleanup gateway lock release failed")
		}
	}()
	run := gatewayRebindFinalHandoverRun{manager: fixture.ingress, repository: repository, driver: driver}
	initial, err := run.attestStable(ctx, last, "", false)
	if err != nil || len(initial.History.Terminals) != 1 || last.Handover == nil || last.Handover.Outcome == nil ||
		!reflect.DeepEqual(initial.Observation, last.Handover.Outcome.Observation) {
		t.Error("live handover cleanup lacks fresh exact terminal proof; retaining resources")
		return
	}
	if last.Phase == gatewayRebindProgressHandoverRolledBack {
		return
	}
	if last.Phase != gatewayRebindProgressHandoverCommitted || last.Handover.Final == nil || last.Handover.Plan == nil {
		t.Error("live handover cleanup has no exact committed final binding; retaining resources")
		return
	}
	for step := 0; step < 6; step++ {
		current, err := run.attestStable(ctx, last, "", false)
		if err != nil || !reflect.DeepEqual(current.Anchor.database, initial.Anchor.database) ||
			!reflect.DeepEqual(current.RuntimeHeads, initial.RuntimeHeads) ||
			!reflect.DeepEqual(current.History.Terminals, initial.History.Terminals) ||
			!reflect.DeepEqual(current.History.Progress, initial.History.Progress) {
			t.Error("live handover terminal cleanup lost stable ownership or history; retaining resources")
			return
		}
		proof := current.Observation
		if proof.Stage != gatewayRebindHandoverContainerAbsent || proof.PredecessorRunning ||
			proof.PredecessorObservationDigest != initial.Observation.PredecessorObservationDigest {
			t.Error("live handover terminal cleanup found changed gateway ownership; retaining resources")
			return
		}
		var args []string
		switch {
		case proof.Final == gatewayRebindHandoverContainerRunning:
			if proof.FinalID != last.Handover.Final.ID {
				t.Error("live handover cleanup final ID drift; retaining resources")
				return
			}
			args = []string{"container", "stop", "--time", "10", last.Handover.Final.ID}
		case proof.Final == gatewayRebindHandoverContainerStopped:
			if proof.FinalID != last.Handover.Final.ID {
				t.Error("live handover cleanup final ID drift; retaining resources")
				return
			}
			args = []string{"container", "rm", last.Handover.Final.ID}
		case proof.ConfigVolumePresent:
			args = []string{"volume", "rm", intent.Intent.Identity.ConfigVolume}
		case proof.DataVolumePresent:
			args = []string{"volume", "rm", intent.Intent.Identity.DataVolume}
		case proof.IngressNetworkPresent:
			args = []string{"network", "rm", history.Progress[11].Record.Stage.Network.ID}
		default:
			return
		}
		if err := fixture.ingress.runDiscard(ctx, fixture.ingress.options.CommandTimeout, args...); err != nil {
			t.Error("live handover terminal cleanup effect is uncertain; retaining remaining resources")
			return
		}
	}
	t.Error("live handover terminal cleanup did not finish its bounded exact resource sequence")
}
