package generatedingress

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGatewayRebindStageContainerSnapshotComparisonPreservesExactObservation(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	fake, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	if err := fixture.predecessor.manager.stageGatewayRebindSuccessorContainerWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil); err != nil {
		t.Fatalf("bind stopped stage container through its effect path: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 6 || history.Progress[5].Record.Stage == nil ||
		history.Progress[5].Record.Stage.StageContainer == nil {
		t.Fatalf("read bound stopped stage progress: history=%#v error=%v", history.Progress, err)
	}
	stage := *history.Progress[5].Record.Stage
	binding := stage.StageContainer
	observed, err := fake.inspect(context.Background(), fixture.intent)
	if err != nil || !validGatewayRebindStageContainerObservation(fixture.intent, stage, observed, binding) {
		t.Fatalf("inspect exact bound stopped stage container: observation=%#v error=%v", observed, err)
	}
	snapshot, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(t *testing.T) (gatewayRebindStageContainerObservation, gatewayRebindStageContainerObservation) {
		t.Helper()
		var physical, replayed gatewayRebindStageContainerObservation
		if err := json.Unmarshal(snapshot, &physical); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(snapshot, &replayed); err != nil {
			t.Fatal(err)
		}
		return physical, replayed
	}
	compare := func(t *testing.T, physical, replayed gatewayRebindStageContainerObservation, want bool) {
		t.Helper()
		physicalBefore, err := json.Marshal(physical)
		if err != nil {
			t.Fatal(err)
		}
		replayedBefore, err := json.Marshal(replayed)
		if err != nil {
			t.Fatal(err)
		}
		got := equalGatewayRebindStageContainerSnapshot(fixture.intent, stage, binding, physical, replayed)
		physicalAfter, err := json.Marshal(physical)
		if err != nil {
			t.Fatal(err)
		}
		replayedAfter, err := json.Marshal(replayed)
		if err != nil {
			t.Fatal(err)
		}
		if string(physicalBefore) != string(physicalAfter) || string(replayedBefore) != string(replayedAfter) {
			t.Fatal("stopped-stage snapshot comparison mutated its inputs")
		}
		if got != want {
			t.Fatalf("snapshot equality=%t, want %t", got, want)
		}
	}
	valid := func(value gatewayRebindStageContainerObservation) bool {
		return validGatewayRebindStageContainerObservation(fixture.intent, stage, value, binding)
	}

	t.Run("valid reversed mounts", func(t *testing.T) {
		physical, replayed := decode(t)
		if len(replayed.StageContainer.Mounts) != 2 {
			t.Fatalf("bound stopped-stage fixture mounts=%#v", replayed.StageContainer.Mounts)
		}
		replayed.StageContainer.Mounts[0], replayed.StageContainer.Mounts[1] =
			replayed.StageContainer.Mounts[1], replayed.StageContainer.Mounts[0]
		if !valid(physical) || !valid(replayed) {
			t.Fatal("reversed exact stopped-stage mounts were not independently valid")
		}
		compare(t, physical, replayed, true)
	})

	mountControls := []struct {
		name   string
		mutate func(*gatewayRebindStageContainerObservation)
	}{
		{name: "changed name", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts[0].Name += "-changed"
		}},
		{name: "changed type", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts[0].Type = "bind"
		}},
		{name: "changed RW", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts[0].RW = false
		}},
		{name: "changed destination", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts[0].Destination = "/unexpected"
		}},
		{name: "duplicate", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts[1] = value.StageContainer.Mounts[0]
		}},
		{name: "foreign", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts[1].Name = "rig-rebind-foreign-volume"
		}},
		{name: "missing", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts = value.StageContainer.Mounts[:1]
		}},
		{name: "extra", mutate: func(value *gatewayRebindStageContainerObservation) {
			value.StageContainer.Mounts = append(value.StageContainer.Mounts, mountInspection{
				Type: "volume", Name: "rig-rebind-extra-volume", Destination: "/extra", RW: true,
			})
		}},
	}
	for _, side := range []struct {
		name      string
		pick      func(*gatewayRebindStageContainerObservation, *gatewayRebindStageContainerObservation) *gatewayRebindStageContainerObservation
		unchanged func(*gatewayRebindStageContainerObservation, *gatewayRebindStageContainerObservation) *gatewayRebindStageContainerObservation
	}{
		{
			name: "physical",
			pick: func(physical, _ *gatewayRebindStageContainerObservation) *gatewayRebindStageContainerObservation {
				return physical
			},
			unchanged: func(_, replayed *gatewayRebindStageContainerObservation) *gatewayRebindStageContainerObservation {
				return replayed
			},
		},
		{
			name: "replayed",
			pick: func(_, replayed *gatewayRebindStageContainerObservation) *gatewayRebindStageContainerObservation {
				return replayed
			},
			unchanged: func(physical, _ *gatewayRebindStageContainerObservation) *gatewayRebindStageContainerObservation {
				return physical
			},
		},
	} {
		for _, control := range mountControls {
			t.Run(side.name+"/"+control.name, func(t *testing.T) {
				physical, replayed := decode(t)
				mutated := side.pick(&physical, &replayed)
				control.mutate(mutated)
				if valid(*mutated) {
					t.Fatal("malformed stopped-stage mount snapshot remained valid")
				}
				if !valid(*side.unchanged(&physical, &replayed)) {
					t.Fatal("unchanged stopped-stage mount snapshot became invalid")
				}
				compare(t, physical, replayed, false)
			})
		}
	}

	t.Run("identically malformed duplicates", func(t *testing.T) {
		physical, replayed := decode(t)
		physical.StageContainer.Mounts[1] = physical.StageContainer.Mounts[0]
		replayed.StageContainer.Mounts[1] = replayed.StageContainer.Mounts[0]
		if valid(physical) || valid(replayed) {
			t.Fatal("identically malformed stopped-stage snapshots remained valid")
		}
		compare(t, physical, replayed, false)
	})

	t.Run("valid configured-network identity drift", func(t *testing.T) {
		physical, replayed := decode(t)
		network := replayed.StageRuntime.ConfiguredNetworks[fixture.intent.Intent.Identity.IngressNetwork]
		network.NetworkID = ""
		replayed.StageRuntime.ConfiguredNetworks[fixture.intent.Intent.Identity.IngressNetwork] = network
		if !valid(physical) || !valid(replayed) {
			t.Fatal("configured-network identity control was not individually valid")
		}
		compare(t, physical, replayed, false)
	})

	t.Run("ordered command drift", func(t *testing.T) {
		physical, replayed := decode(t)
		replayed.StageContainer.Cmd[0], replayed.StageContainer.Cmd[1] =
			replayed.StageContainer.Cmd[1], replayed.StageContainer.Cmd[0]
		if valid(replayed) {
			t.Fatal("ordered command drift remained valid")
		}
		compare(t, physical, replayed, false)
	})
}
