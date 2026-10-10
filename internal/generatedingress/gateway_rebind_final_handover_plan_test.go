package generatedingress

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayRebindFinalHandoverPlanPinsCompleteRoutesAndResourceIdentities(t *testing.T) {
	value, observed := newGatewayRebindFinalHandoverPlanTestContext(t)
	plan, err := gatewayRebindFinalHandoverPlanFor(value, observed)
	if err != nil || !validGatewayRebindFinalHandoverPlan(value, plan) {
		t.Fatalf("plan: %v", err)
	}
	value.Plan = &plan
	args, err := gatewayRebindFinalContainerCreateArgs(value)
	if err != nil {
		t.Fatal(err)
	}
	options := make(map[string][]string)
	for index := 2; index < len(args)-4; index++ {
		if strings.HasPrefix(args[index], "--") && index+1 < len(args) && !strings.HasPrefix(args[index+1], "--") {
			options[args[index]] = append(options[args[index]], args[index+1])
		}
	}
	for option, expected := range map[string][]string{
		"--name":     {value.Intent.Intent.Identity.FinalContainer},
		"--hostname": {value.Intent.Intent.Identity.FinalHostname},
		"--user":     {"1000:1000"}, "--cap-drop": {"ALL"}, "--security-opt": {"no-new-privileges"},
		"--restart": {gatewayV2FinalRestartPolicy}, "--entrypoint": {caddyExecutable},
	} {
		if !reflect.DeepEqual(options[option], expected) {
			t.Fatalf("%s=%v want %v", option, options[option], expected)
		}
	}
	if len(options["--network"]) != len(observed.ApplicationNetworks)+1 {
		t.Fatal("final command did not retain the complete application network roster")
	}
	for _, network := range observed.ApplicationNetworks {
		if !containsString(options["--network"], "name="+network.Name) {
			t.Fatal("final command omitted a route's application network")
		}
	}
	loopback := "127.0.0.1:" + strconv.FormatUint(uint64(value.Predecessor.Journal.Source.LocalHostPort), 10) + ":8080/tcp"
	if !containsString(options["--publish"], loopback) ||
		args[len(args)-1] != "/config/active.json" || args[len(args)-4] != "sha256:"+value.SequenceTwelve.Stage.ObservedDockerImageID {
		t.Fatal("final command lost its pinned image, inactive config, or predecessor loopback publication")
	}
	if plan.SequenceTwelveDigest != value.SequenceTwelve.Digest ||
		plan.PredecessorObservationDigest != observed.PredecessorObservationDigest ||
		plan.PredecessorInitiallyRunning != observed.PredecessorRunning ||
		plan.FinalConfigDigest != value.SequenceTwelve.Stage.FinalConfigIntent.ContentDigest {
		t.Fatal("handover plan lost immutable or physical predecessor evidence")
	}
	for _, mutate := range []func(*gatewayRebindFinalHandoverPlan){
		func(p *gatewayRebindFinalHandoverPlan) { p.FinalConfigDigest = strings.Repeat("a", 64) },
		func(p *gatewayRebindFinalHandoverPlan) { p.LocalHostPort++ },
		func(p *gatewayRebindFinalHandoverPlan) { p.FinalOwnershipDigest = strings.Repeat("b", 64) },
		func(p *gatewayRebindFinalHandoverPlan) { p.ApplicationNetworks = nil },
	} {
		forged := plan
		mutate(&forged)
		forged.Digest, err = gatewayRebindFinalHandoverPlanDigest(forged)
		if err != nil {
			t.Fatal(err)
		}
		value.Plan = &forged
		if _, err := gatewayRebindFinalContainerCreateArgs(value); err == nil {
			t.Fatal("recomputed plan forgery authorized a final create command")
		}
	}
	value.Plan = &plan
	binding, err := gatewayRebindFinalContainerBindingFor(value, strings.Repeat("9", 64))
	if err != nil || !validGatewayRebindFinalContainerBinding(value, binding) {
		t.Fatalf("binding: %v", err)
	}
	for _, id := range []string{"", "short", value.SequenceTwelve.Stage.StageContainer.ID,
		value.Predecessor.Journal.Resources.FinalContainerID} {
		if _, err := gatewayRebindFinalContainerBindingFor(value, id); err == nil {
			t.Fatal("invalid or existing gateway identity accepted as the successor final")
		}
	}
}

func TestGatewayRebindFinalHandoverPlanRejectsUnprovenAndAmbiguousAdmission(t *testing.T) {
	value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
	for _, mutate := range []func(*gatewayRebindFinalHandoverObservation){
		func(o *gatewayRebindFinalHandoverObservation) { o.Stage = gatewayRebindHandoverContainerStopped },
		func(o *gatewayRebindFinalHandoverObservation) {
			o.Final = gatewayRebindHandoverContainerStopped
			o.FinalID = strings.Repeat("9", 64)
		},
		func(o *gatewayRebindFinalHandoverObservation) { o.ConfigVolumePresent = false },
		func(o *gatewayRebindFinalHandoverObservation) { o.ConfigDigest = strings.Repeat("a", 64) },
		func(o *gatewayRebindFinalHandoverObservation) { o.PredecessorObservationDigest = "" },
		func(o *gatewayRebindFinalHandoverObservation) {
			o.ApplicationNetworks = append(o.ApplicationNetworks, o.ApplicationNetworks[0])
		},
	} {
		observed := initial
		observed.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork{}, initial.ApplicationNetworks...)
		mutate(&observed)
		observed.Digest, _ = gatewayRebindFinalHandoverObservationDigest(observed)
		if _, err := gatewayRebindFinalHandoverPlanFor(value, observed); err == nil {
			t.Fatal("unproven or ambiguous admission observation authorized handover")
		}
	}
	stopped := initial
	stopped.PredecessorRunning = false
	stopped.PredecessorAddress = gatewayRebindPredecessorAddressAbsent
	stopped.PredecessorStopDigest = stopped.PredecessorObservationDigest
	stopped.Digest, _ = gatewayRebindFinalHandoverObservationDigest(stopped)
	plan, err := gatewayRebindFinalHandoverPlanFor(value, stopped)
	if err != nil || plan.PredecessorInitiallyRunning {
		t.Fatalf("already-stopped exact predecessor could not be represented truthfully: %v", err)
	}
	stopped.PredecessorStopDigest = strings.Repeat("e", 64)
	stopped.Digest, _ = gatewayRebindFinalHandoverObservationDigest(stopped)
	if validGatewayRebindFinalHandoverObservationValue(stopped) {
		t.Fatal("unrelated stopped predecessor proof accepted after recomputing observation digest")
	}
	if _, err := gatewayRebindFinalHandoverPlanFor(value, stopped); err == nil {
		t.Fatal("unrelated stopped predecessor proof authorized handover")
	}
}

func newGatewayRebindFinalHandoverPlanTestContext(t *testing.T) (gatewayRebindFinalHandoverContext, gatewayRebindFinalHandoverObservation) {
	t.Helper()
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 {
		t.Fatalf("history: %v", err)
	}
	source, err := fixture.predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	value := gatewayRebindFinalHandoverContext{Intent: fixture.intent, SequenceTwelve: history.Progress[11].Record,
		Predecessor: history.Predecessor, Source: source, Phase: gatewayRebindProgressFinalConfigCopied}
	docker, err := gatewayRebindStageNetworkInspect(t, fixture)(context.Background(), source, history.Predecessor.State, history.Predecessor.Journal)
	if err != nil {
		t.Fatal(err)
	}
	defer clearGatewayV2DockerObservation(&docker)
	predDigest, err := gatewayRebindEffectBoundaryDockerDigest(docker, history.Predecessor.State.Identity)
	if err != nil {
		t.Fatal(err)
	}
	observed := gatewayRebindFinalHandoverObservation{
		Stage: gatewayRebindHandoverContainerRunning, Final: gatewayRebindHandoverContainerAbsent,
		PredecessorRunning: docker.FinalContainer.Running, PredecessorAddress: gatewayRebindPredecessorAddressPresent,
		PredecessorObservationDigest: predDigest, ConfigVolumePresent: true, DataVolumePresent: true, IngressNetworkPresent: true,
		ConfigDigest: value.SequenceTwelve.Stage.FinalConfigIntent.ContentDigest,
		HostDigest:   strings.Repeat("a", 64), InventoryDigest: strings.Repeat("b", 64), ApplicationEndpointsDigest: strings.Repeat("c", 64),
	}
	if !observed.PredecessorRunning {
		observed.PredecessorStopDigest = observed.PredecessorObservationDigest
	}
	for name, id := range docker.ApplicationNetworkIDs {
		observed.ApplicationNetworks = append(observed.ApplicationNetworks, gatewayRebindHandoverApplicationNetwork{Name: name, ID: normalizeID(id)})
	}
	sort.Slice(observed.ApplicationNetworks, func(i, j int) bool {
		return observed.ApplicationNetworks[i].Name < observed.ApplicationNetworks[j].Name
	})
	observed.Digest, err = gatewayRebindFinalHandoverObservationDigest(observed)
	if err != nil {
		t.Fatal(err)
	}
	return value, observed
}
