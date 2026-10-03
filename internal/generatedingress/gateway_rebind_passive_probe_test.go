package generatedingress

import (
	"context"
	"reflect"
	"testing"
)

func TestInspectGatewayRebindDockerSelectsPassivePolicy(t *testing.T) {
	ctx := context.Background()
	source := routeState{Version: 1}
	state := gatewayV2RouteState{OperationID: "rebind-policy-state"}
	journal := gatewayMigrationJournal{OperationID: "rebind-policy-journal"}
	called := false
	_, err := inspectGatewayRebindDockerWith(ctx, source, state, journal,
		func(gotCtx context.Context, gotSource routeState, gotState gatewayV2RouteState,
			gotJournal gatewayMigrationJournal, requireStageConfig bool, policy gatewayV2DockerProbePolicy,
		) (gatewayV2DockerObservation, error) {
			called = true
			if gotCtx != ctx || !requireStageConfig || policy != gatewayV2DockerProbePassiveRebind ||
				!reflect.DeepEqual(gotSource, source) || !reflect.DeepEqual(gotState, state) ||
				!reflect.DeepEqual(gotJournal, journal) {
				t.Fatalf("rebind observer received wrong inputs: stage=%t policy=%d", requireStageConfig, policy)
			}
			return gatewayV2DockerObservation{}, nil
		})
	if err != nil || !called {
		t.Fatalf("passive rebind observer error=%v called=%t", err, called)
	}
}

func TestObserveGatewayV2ServingProbesHonorsPolicyAndContainerState(t *testing.T) {
	tests := []struct {
		name          string
		policy        gatewayV2DockerProbePolicy
		stageFound    bool
		stageRunning  bool
		stageRestart  bool
		finalFound    bool
		finalRunning  bool
		finalRestart  bool
		wantStageCall int
		wantFinalCall int
	}{
		{
			name: "passive running stage and final", policy: gatewayV2DockerProbePassiveRebind,
			stageFound: true, stageRunning: true, finalFound: true, finalRunning: true,
		},
		{
			name: "serving running stage and final", policy: gatewayV2DockerProbeServing,
			stageFound: true, stageRunning: true, finalFound: true, finalRunning: true,
			wantStageCall: 1, wantFinalCall: 1,
		},
		{
			name: "serving stopped stage and restarting final", policy: gatewayV2DockerProbeServing,
			stageFound: true, finalFound: true, finalRunning: true, finalRestart: true,
		},
		{
			name: "serving running stage and stopped final", policy: gatewayV2DockerProbeServing,
			stageFound: true, stageRunning: true, finalFound: true,
			wantStageCall: 1,
		},
		{
			name: "serving restarting stage and running final", policy: gatewayV2DockerProbeServing,
			stageFound: true, stageRunning: true, stageRestart: true, finalFound: true, finalRunning: true,
			wantFinalCall: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := gatewayV2DockerObservation{
				StageContainerFound: test.stageFound,
				StageContainer: caddyInspection{
					Running: test.stageRunning, Restarting: test.stageRestart,
				},
				FinalContainerFound: test.finalFound,
				FinalContainer: caddyInspection{
					Running: test.finalRunning, Restarting: test.finalRestart,
				},
			}
			var stage404, stageHost, final404, finalRoutes, finalHost int
			observeGatewayV2ServingProbes(test.policy, &observation, gatewayV2ServingProbeCallbacks{
				stage404:    func() bool { stage404++; return true },
				stageHost:   func() bool { stageHost++; return true },
				final404:    func() bool { final404++; return true },
				finalRoutes: func() bool { finalRoutes++; return true },
				finalHost:   func() bool { finalHost++; return true },
			})
			if stage404 != test.wantStageCall || stageHost != test.wantStageCall ||
				final404 != test.wantFinalCall || finalRoutes != test.wantFinalCall || finalHost != test.wantFinalCall {
				t.Fatalf("unexpected HTTP probe callbacks: stage404=%d stageHost=%d final404=%d finalRoutes=%d finalHost=%d",
					stage404, stageHost, final404, finalRoutes, finalHost)
			}
			if observation.Stage404Proven != (test.wantStageCall == 1) ||
				observation.StageHostPublicationProven != (test.wantStageCall == 1) ||
				observation.Final404Proven != (test.wantFinalCall == 1) ||
				observation.FinalRoutesProven != (test.wantFinalCall == 1) ||
				observation.FinalHostPublicationProven != (test.wantFinalCall == 1) {
				t.Fatalf("unexpected serving proof flags: stage404=%t stageHost=%t final404=%t finalRoutes=%t finalHost=%t",
					observation.Stage404Proven, observation.StageHostPublicationProven,
					observation.Final404Proven, observation.FinalRoutesProven, observation.FinalHostPublicationProven)
			}
		})
	}
}
