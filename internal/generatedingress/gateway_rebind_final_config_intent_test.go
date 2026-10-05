package generatedingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestGatewayRebindFinalConfigRoutePlanPreservesAllAppsAndRawBindings(t *testing.T) {
	intent, predecessor, heads, loopbackID := gatewayRebindFinalConfigLoopbackFixture(t)
	plan, err := gatewayRebindFinalConfigRoutePlanFor(intent, predecessor, heads)
	if err != nil || !validGatewayRebindFinalConfigRoutePlanValue(intent, plan) || len(plan.Routes) != 2 {
		t.Fatalf("route plan=%#v error=%v", plan, err)
	}
	if plan.EffectiveSuccessorProfile != intent.Intent.SuccessorProfile ||
		plan.SuccessorIdentity != intent.Intent.Identity || plan.Predecessor != intent.Intent.Predecessor {
		t.Fatal("route plan did not retain exact source and effective successor identities")
	}
	foundLoopback := false
	for _, binding := range plan.Routes {
		source := predecessor.State.Apps[binding.AppID]
		if !reflect.DeepEqual(binding.Route, source.Route) || !reflect.DeepEqual(binding.SourceLAN, source.LAN) {
			t.Fatalf("source binding was rewritten for %s", binding.AppID)
		}
		if binding.AppID == loopbackID {
			foundLoopback = true
			if binding.SourceLAN != nil {
				t.Fatal("loopback-only application acquired a LAN binding")
			}
		}
	}
	if !foundLoopback {
		t.Fatal("complete predecessor route plan omitted the loopback-only application")
	}
	lan := plan.Routes[0]
	if lan.SourceLAN == nil {
		lan = plan.Routes[1]
	}
	if lan.SourceLAN == nil || lan.SourceLAN.ProfileRevisionID != predecessor.State.Profile.RevisionID ||
		lan.SourceLAN.ProfileRevisionNumber != predecessor.State.Profile.RevisionNumber ||
		lan.SourceLAN.ProfileSpecDigest != predecessor.State.Profile.SpecDigest ||
		lan.SourceLAN.ProfileRevisionID == plan.EffectiveSuccessorProfile.RevisionID {
		t.Fatalf("raw predecessor profile was not preserved separately from successor: %#v", lan)
	}
	body, err := gatewayRebindFinalConfigBytes(intent, plan)
	if err != nil || len(body) == 0 || !bytes.Contains(body, []byte(loopbackID+".rig.localhost")) {
		t.Fatalf("complete active config bytes missing loopback app: error=%v", err)
	}
	repeated, err := gatewayRebindFinalConfigBytes(intent, plan)
	if err != nil || !bytes.Equal(body, repeated) {
		t.Fatalf("final active config is not deterministic: error=%v", err)
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	local := config.Apps.HTTP.Servers["generated"]
	if !reflect.DeepEqual(local.Listen, []string{net.JoinHostPort(intent.Intent.Network.ContainerIPv4,
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10))}) {
		t.Fatalf("local listener=%#v", local.Listen)
	}
	hosts := make(map[string]bool)
	for _, route := range local.Routes {
		for _, match := range route.Match {
			for _, host := range match.Host {
				hosts[host] = true
			}
		}
	}
	if !hosts[loopbackID+".rig.localhost"] || !hosts[lan.AppID+".rig.localhost"] {
		t.Fatalf("local routes omit predecessor apps: %#v", hosts)
	}
	lanServer := config.Apps.HTTP.Servers["lan-"+strconv.FormatUint(uint64(lan.SourceLAN.Port), 10)]
	if !reflect.DeepEqual(lanServer.Listen, []string{net.JoinHostPort(intent.Intent.Network.ContainerIPv4,
		strconv.FormatUint(uint64(lan.SourceLAN.Port), 10))}) {
		t.Fatalf("LAN listener=%#v container=%q port=%d", lanServer.Listen,
			intent.Intent.Network.ContainerIPv4, lan.SourceLAN.Port)
	}
	selectedHost := false
	for _, route := range lanServer.Routes {
		for _, match := range route.Match {
			for _, host := range match.Host {
				selectedHost = selectedHost || host == plan.EffectiveSuccessorProfile.SelectedIPv4
			}
		}
	}
	if !selectedHost {
		t.Fatalf("LAN routes do not bind approved host %q", plan.EffectiveSuccessorProfile.SelectedIPv4)
	}
}

func TestGatewayRebindFinalConfigRoutePlanRejectsCompletenessAndSourceDrift(t *testing.T) {
	intent, predecessor, heads, loopbackID := gatewayRebindFinalConfigLoopbackFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*gatewayUpgradeGenerationSelection, *[]appaccess.GatewayRebindRuntimeHead)
	}{
		{name: "omitted runtime head", mutate: func(_ *gatewayUpgradeGenerationSelection, values *[]appaccess.GatewayRebindRuntimeHead) {
			*values = (*values)[:len(*values)-1]
		}},
		{name: "extra runtime head", mutate: func(_ *gatewayUpgradeGenerationSelection, values *[]appaccess.GatewayRebindRuntimeHead) {
			*values = append(*values, appaccess.GatewayRebindRuntimeHead{AppID: uuid.NewString()})
		}},
		{name: "loopback slot drift", mutate: func(_ *gatewayUpgradeGenerationSelection, values *[]appaccess.GatewayRebindRuntimeHead) {
			for index := range *values {
				if (*values)[index].AppID == loopbackID {
					(*values)[index].Slot = oppositeGatewayRebindTestSlot((*values)[index].Slot)
				}
			}
		}},
		{name: "wrong generation", mutate: func(value *gatewayUpgradeGenerationSelection, _ *[]appaccess.GatewayRebindRuntimeHead) {
			value.Generation++
		}},
		{name: "predecessor route drift", mutate: func(value *gatewayUpgradeGenerationSelection, _ *[]appaccess.GatewayRebindRuntimeHead) {
			app := value.State.Apps[loopbackID]
			app.Route.Endpoints[0].InternalPort++
			value.State.Apps[loopbackID] = app
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := predecessor
			candidate.State = cloneGatewayV2RouteState(predecessor.State)
			candidateHeads := append([]appaccess.GatewayRebindRuntimeHead(nil), heads...)
			test.mutate(&candidate, &candidateHeads)
			if plan, err := gatewayRebindFinalConfigRoutePlanFor(intent, candidate, candidateHeads); err == nil ||
				!reflect.DeepEqual(plan, gatewayRebindFinalConfigRoutePlan{}) {
				t.Fatalf("drift accepted: plan=%#v error=%v", plan, err)
			}
		})
	}
}

func TestGatewayRebindFinalConfigRoutePlanPinsFreshLoopbackRuntimeHead(t *testing.T) {
	intent, predecessor, heads, loopbackID := gatewayRebindFinalConfigLoopbackFixture(t)
	initial, err := gatewayRebindFinalConfigRoutePlanFor(intent, predecessor, heads)
	if err != nil {
		t.Fatal(err)
	}
	for index := range heads {
		if heads[index].AppID == loopbackID {
			heads[index].Generation++
			heads[index].UpdatedAt = heads[index].UpdatedAt.Add(time.Second)
		}
	}
	fresh, err := gatewayRebindFinalConfigRoutePlanFor(intent, predecessor, heads)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(initial, fresh) || initial.Digest == fresh.Digest ||
		initial.RouteMapDigest == fresh.RouteMapDigest {
		t.Fatal("fresh loopback runtime head did not change the pinned route plan")
	}
}

func TestGatewayRebindFinalConfigIntentRejectsDurablePlanOmittingLoopbackRoute(t *testing.T) {
	fixture, intent, predecessor, heads, loopbackID := gatewayRebindFinalConfigLoopbackFixtureWithStore(t)
	persistGatewayRebindFinalConfigLoopbackFixture(t, fixture, intent, predecessor)
	records := gatewayRebindFinalConfigProgressRecords(t, intent)
	for _, record := range records {
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
			intent.Generation, intent.OperationID, record.Sequence)
		if err != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, err)
		}
	}
	binding, err := gatewayRebindFinalConfigIntentBindingFor(intent, predecessor, heads, records[9])
	if err != nil {
		t.Fatal(err)
	}
	forgedRoutes := make([]gatewayRebindFinalConfigRouteBinding, 0, len(binding.RoutePlan.Routes)-1)
	for _, route := range binding.RoutePlan.Routes {
		if route.AppID != loopbackID {
			forgedRoutes = append(forgedRoutes, route)
		}
	}
	if len(forgedRoutes) != len(intent.Intent.Roster) {
		t.Fatalf("forged route count=%d roster=%d", len(forgedRoutes), len(intent.Intent.Roster))
	}
	binding.RoutePlan.Routes = cloneGatewayRebindFinalConfigRoutes(forgedRoutes)
	binding.RoutePlan.RouteMapDigest, err = gatewayRebindFinalConfigRouteMapDigest(binding.RoutePlan.Routes)
	if err != nil {
		t.Fatal(err)
	}
	binding.RoutePlan.Digest, err = gatewayRebindFinalConfigRoutePlanDigest(binding.RoutePlan)
	if err != nil {
		t.Fatal(err)
	}
	body, err := gatewayRebindFinalConfigBytes(intent, binding.RoutePlan)
	if err != nil {
		t.Fatalf("forged plan should remain standalone-valid: %v", err)
	}
	digest := sha256.Sum256(body)
	binding.ContentDigest = hex.EncodeToString(digest[:])
	binding.ContentLength = int64(len(body))
	clear(body)
	if !validGatewayRebindFinalConfigIntentBinding(intent, records[9], binding) {
		t.Fatal("forged omitted-loopback binding was not independently self-consistent")
	}
	forged, err := newGatewayRebindFinalConfigIntentProgress(intent, records[9], binding,
		gatewayRebindProgressTimestamp(11))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
		intent.Generation, intent.OperationID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.installExact(context.Background(), forged); err == nil {
		t.Fatal("pre-write validation accepted a final plan omitting the loopback route")
	}
	if _, err := os.Stat(store.path); !os.IsNotExist(err) {
		t.Fatalf("rejected final plan created sequence eleven: %v", err)
	}
	state := gatewayUpgradeStateStore{directory: store.directory}
	if err := state.writeExact(store.path, store.purpose, forged, true,
		gatewayRebindProgressMaxBytes(11)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("history scanner accepted durable final plan omitting the loopback route")
	}
	fresh, err := New(fixture.runner, fixture.manager.options)
	if err != nil {
		t.Fatal(err)
	}
	reads := gatewayRebindSuccessorPreflightReads{
		network: gatewayV2NetworkPlanReads{
			candidates: func() ([]hostNetworkCandidate, error) { return nil, nil },
			host:       func() (gatewayV2HostNetworkSnapshot, error) { return gatewayV2HostNetworkSnapshot{}, nil },
			docker:     func(context.Context) ([]netip.Prefix, error) { return nil, nil },
		},
		dockerIDs: func(context.Context) ([]string, error) { return nil, nil },
	}
	inspect := func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) (gatewayV2DockerObservation, error) {
		return gatewayV2DockerObservation{}, nil
	}
	if err := fresh.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(context.Background(),
		fixture.repository, reads, inspect, &gatewayRebindStageStartFake{},
		gatewayRebindProgressTimestamp(12), nil); err == nil {
		t.Fatal("fresh Manager accepted durable final plan omitting the loopback route")
	}
}

func TestGatewayRebindFinalConfigRoutePlanRejectsRosterPortsAndBindingForgeries(t *testing.T) {
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentTwoAppFixture(t)
	intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	heads := gatewayRebindFinalConfigHeadsForState(t, predecessor.State, intent.Intent.Roster)
	plan, err := gatewayRebindFinalConfigRoutePlanFor(intent, predecessor, heads)
	if err != nil || len(plan.Routes) != 2 {
		t.Fatalf("two-app plan: %#v error=%v", plan, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindFinalConfigRoutePlan)
	}{
		{name: "omitted app", mutate: func(value *gatewayRebindFinalConfigRoutePlan) {
			value.Routes = value.Routes[:1]
		}},
		{name: "extra app", mutate: func(value *gatewayRebindFinalConfigRoutePlan) {
			extra := value.Routes[0]
			extra.AppID = uuid.NewString()
			extra.RuntimeHead.AppID = extra.AppID
			value.Routes = append(value.Routes, extra)
			sort.Slice(value.Routes, func(i, j int) bool { return value.Routes[i].AppID < value.Routes[j].AppID })
		}},
		{name: "duplicate port", mutate: func(value *gatewayRebindFinalConfigRoutePlan) {
			value.Routes[1].SourceLAN.Port = value.Routes[0].SourceLAN.Port
		}},
		{name: "out of successor range", mutate: func(value *gatewayRebindFinalConfigRoutePlan) {
			value.Routes[0].SourceLAN.Port = value.EffectiveSuccessorProfile.PortEnd + 1
		}},
		{name: "raw profile rewrite", mutate: func(value *gatewayRebindFinalConfigRoutePlan) {
			value.Routes[0].SourceLAN.ProfileRevisionID = value.EffectiveSuccessorProfile.RevisionID
		}},
		{name: "source digest", mutate: func(value *gatewayRebindFinalConfigRoutePlan) {
			value.Routes[0].SourceBindingDigest = strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := plan
			forged.Routes = cloneGatewayRebindFinalConfigRoutes(plan.Routes)
			test.mutate(&forged)
			for index := range forged.Routes {
				if test.name != "source digest" || index != 0 {
					forged.Routes[index].SourceBindingDigest, _ = gatewayRebindFinalConfigSourceBindingDigest(forged.Routes[index])
				}
			}
			forged.RouteMapDigest, _ = gatewayRebindFinalConfigRouteMapDigest(forged.Routes)
			forged.Digest, _ = gatewayRebindFinalConfigRoutePlanDigest(forged)
			if validGatewayRebindFinalConfigRoutePlanValue(intent, forged) {
				t.Fatal("forged final route plan was accepted")
			}
		})
	}
	_ = fixture
}

func TestGatewayRebindFinalConfigIntentAppendsSequenceElevenAndReplaysReadOnly(t *testing.T) {
	fixture, fake, reads, records := installGatewayRebindFinalConfigServingProgress(t)
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 10 {
		t.Fatalf("sequence-ten history=%d error=%v", len(history.Progress), err)
	}
	before := make([][]byte, len(history.Progress))
	for index := range history.Progress {
		before[index], err = os.ReadFile(history.Progress[index].Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	beforeDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeRoute, err := fixture.predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := false
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(11), func() { checkpoint = true }); err != nil {
		after, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		t.Fatalf("append final config intent after checkpoint=%t history=%d scan=%v: %v",
			checkpoint, len(after.Progress), scanErr, err)
	}
	if !checkpoint || fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("final intent checkpoint=%t starts=%d stops=%d", checkpoint, fake.startCalls, fake.stopCalls)
	}
	history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 11 {
		t.Fatalf("sequence-eleven history=%d error=%v", len(history.Progress), err)
	}
	for index := range before {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !bytes.Equal(body, before[index]) || history.Progress[index].Record.Digest != records[index].Digest {
			t.Fatalf("sequence %d changed: %v", index+1, readErr)
		}
	}
	record := history.Progress[10].Record
	if record.Sequence != 11 || record.Phase != gatewayRebindProgressFinalConfigIntent || record.Stage == nil ||
		record.Stage.FinalConfigIntent == nil {
		t.Fatalf("invalid final config intent record: %#v", record)
	}
	binding := record.Stage.FinalConfigIntent
	body, err := gatewayRebindFinalConfigBytes(fixture.intent, binding.RoutePlan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	if binding.ContentDigest != hex.EncodeToString(digest[:]) || binding.ContentLength != int64(len(body)) ||
		binding.Destination != "/config/active.json" || binding.SequenceTenDigest != records[9].Digest ||
		binding.PriorProgressDigest != records[9].Digest || binding.RoutePlan.RouteMapDigest == "" {
		t.Fatalf("final config binding=%#v", binding)
	}
	afterDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) {
		t.Fatalf("final intent changed SQLite: equal=%t error=%v", reflect.DeepEqual(beforeDatabase, afterDatabase), err)
	}
	afterRoute, err := fixture.predecessor.manager.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatalf("final intent changed route state: equal=%t error=%v", reflect.DeepEqual(beforeRoute, afterRoute), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatalf("fresh Manager replay: %v", err)
	}
	if fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("final intent replay mutated Docker: starts=%d stops=%d", fake.startCalls, fake.stopCalls)
	}
}

func TestGatewayRebindFinalConfigIntentRejectsFreshDriftAndHistoryGaps(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigServingProgress(t)
	for _, test := range []struct {
		name   string
		mutate func()
		reset  func()
	}{
		{name: "live stage config", mutate: func() { fake.liveConfigDrift = true }, reset: func() { fake.liveConfigDrift = false }},
		{name: "host publication", mutate: func() { fake.hostProbeDrift = true }, reset: func() { fake.hostProbeDrift = false }},
		{name: "container publication", mutate: func() { fake.containerDrift = true }, reset: func() { fake.containerDrift = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.mutate()
			defer test.reset()
			err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
				context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
				fake, gatewayRebindProgressTimestamp(11), nil)
			if err == nil {
				t.Fatal("fresh physical drift was accepted")
			}
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 10 {
				t.Fatalf("drift appended progress=%d error=%v", len(history.Progress), scanErr)
			}
		})
	}

	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := fixture.predecessor.repository.GatewayRebindRuntimeHeads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding, err := gatewayRebindFinalConfigIntentBindingFor(fixture.intent, history.Predecessor, heads,
		history.Progress[9].Record)
	if err != nil {
		t.Fatal(err)
	}
	record, err := newGatewayRebindFinalConfigIntentProgress(fixture.intent, history.Progress[9].Record,
		binding, gatewayRebindProgressTimestamp(11))
	if err != nil {
		t.Fatal(err)
	}
	gapRoot := t.TempDir()
	store, err := newGatewayRebindProgressStore(gapRoot, fixture.intent.Generation, fixture.intent.OperationID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.installExact(context.Background(), record); err == nil {
		t.Fatal("sequence eleven installed without sequences one through ten")
	}
}

func TestGatewayRebindFinalConfigIntentRejectsLoopbackRuntimeHeadCheckpointDrift(t *testing.T) {
	fixture, fake, reads, loopbackID := installGatewayRebindFinalConfigLoopbackServingProgress(t)
	for _, trigger := range []string{
		"lan_gateway_rebind_fence_runtime_head_update", "generated_runtime_active_head_valid_update",
	} {
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	checkpointCalls := 0
	err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(11), func() {
			checkpointCalls++
			if _, updateErr := fixture.predecessor.db.Exec(`UPDATE generated_runtime_active_heads
				SET generation=generation+1,updated_at=? WHERE app_id=?`,
				gatewayRebindProgressTimestamp(13).Format(time.RFC3339Nano), loopbackID); updateErr != nil {
				t.Fatal(updateErr)
			}
		})
	if err == nil || checkpointCalls != 1 {
		t.Fatalf("loopback head checkpoint drift accepted: calls=%d error=%v", checkpointCalls, err)
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 10 || fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("checkpoint rejection history=%d starts=%d stops=%d scan=%v",
			len(history.Progress), fake.startCalls, fake.stopCalls, scanErr)
	}
}

func TestGatewayRebindFinalConfigIntentAmbiguousWriteRequiresFreshReplay(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigServingProgress(t)
	originalWrite := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if err := originalWrite(path, purpose, body); err != nil {
			return err
		}
		return errors.New("injected protected write uncertainty")
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = originalWrite })
	err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(11), nil)
	if err == nil {
		t.Fatal("ambiguous protected write reported success")
	}
	upgradeProtectedWriteNew = originalWrite
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatalf("fresh Manager did not adopt exact immutable readback: %v", err)
	}
	history, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 11 || fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("ambiguous replay history=%d starts=%d stops=%d error=%v",
			len(history.Progress), fake.startCalls, fake.stopCalls, scanErr)
	}
}

func TestGatewayRebindStageStartStillRejectsLaterPhaseHistory(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigServingProgress(t)
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatal(err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil); err == nil {
		t.Fatal("sequence-ten operation accepted later-phase history")
	}
}

func installGatewayRebindFinalConfigServingProgress(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindStageStartFake, gatewayRebindSuccessorPreflightReads, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, fake, reads, _ := installGatewayRebindStageServingIntent(t)
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("install sequence-ten serving receipt: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 10 {
		t.Fatalf("sequence-ten history=%d error=%v", len(history.Progress), err)
	}
	fake.stage = *history.Progress[9].Record.Stage
	records := make([]gatewayRebindProgressRecord, len(history.Progress))
	for index := range history.Progress {
		records[index] = history.Progress[index].Record
	}
	return fixture, fake, reads, records
}

func installGatewayRebindFinalConfigLoopbackServingProgress(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindStageStartFake, gatewayRebindSuccessorPreflightReads, string,
) {
	t.Helper()
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	loopbackID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	for _, trigger := range []string{
		"lan_gateway_rebind_fence_runtime_head_insert", "lan_gateway_rebind_fence_runtime_head_update",
		"lan_gateway_rebind_fence_runtime_head_delete", "lan_gateway_rebind_fence_access_head_insert",
	} {
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.predecessor.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`,
		loopbackID, "rebind-loopback-"+loopbackID, "Loopback-only rebind app"); err != nil {
		t.Fatal(err)
	}
	head := seedGatewayRebindPredecessorRuntime(t, fixture.predecessor.db, loopbackID)
	for _, statement := range []string{
		`CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_update
			BEFORE UPDATE ON generated_runtime_active_heads
			WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
			BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END`,
		`CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_insert
			BEFORE INSERT ON generated_runtime_active_heads
			WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
			BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END`,
		`CREATE TRIGGER lan_gateway_rebind_fence_runtime_head_delete
			BEFORE DELETE ON generated_runtime_active_heads
			WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
			BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END`,
		`CREATE TRIGGER lan_gateway_rebind_fence_access_head_insert
			BEFORE INSERT ON lan_app_access_heads
			WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)
			BEGIN SELECT RAISE(ABORT, 'LAN gateway rebind fence is active'); END`,
	} {
		if _, err := fixture.predecessor.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	state := cloneGatewayV2RouteState(fixture.predecessor.state)
	var source gatewayV2AppRoute
	for _, app := range state.Apps {
		source = cloneGatewayV2AppRoute(app)
		break
	}
	source.LAN = nil
	source.Route.Slot = generatedruntime.Slot(head.Slot)
	source.Route.Endpoints = []generatedruntime.RouteEndpoint{
		endpoint("api", "server", "rebind-loopback-network", "rebind-loopback-"+head.Slot, 3000, '7'),
	}
	state.Apps[loopbackID] = source
	predecessor := gatewayUpgradeGenerationSelection{
		Store: fixture.predecessor.store, Generation: fixture.predecessor.store.generation,
		State: state, Journal: fixture.predecessor.journal, Existing: true,
		operationID: fixture.predecessor.journal.OperationID,
	}
	intent := fixture.intent
	var err error
	intent.Intent.Predecessor.StateDigest, err = canonicalDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	intent.Intent.Digest, err = gatewayRebindInitialSuccessorIntentDigest(intent.Intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.Digest, err = gatewayRebindProtectedIntentDigest(intent)
	if err != nil || !validGatewayRebindProtectedIntent(intent) {
		t.Fatalf("loopback protected intent: %v", err)
	}
	persistGatewayRebindFinalConfigLoopbackFixture(t, fixture.predecessor, intent, predecessor)
	fixture.predecessor.state = state
	fixture.intent = intent

	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	container, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	container.found = true
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, *records[4].Stage)
	if err != nil {
		t.Fatal(err)
	}
	sixth, err := newGatewayRebindStageContainerProgress(intent, records[4], gatewayRebindStageContainerBinding{
		ID: container.containerID, OwnershipDigest: ownership, ConfigurationDigest: configuration,
	}, gatewayRebindProgressTimestamp(6))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		intent.Generation, intent.OperationID, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.installExact(context.Background(), sixth); err != nil {
		t.Fatal(err)
	}
	container.stage = *sixth.Stage
	intentFake := &gatewayRebindStageConfigIntentFake{
		gatewayRebindStageContainerFake: container, empty: true,
	}
	configIntent, err := gatewayRebindStageConfigIntentBindingFor(intent, sixth)
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(intent, sixth, configIntent,
		gatewayRebindProgressTimestamp(7))
	if err != nil {
		t.Fatal(err)
	}
	store, err = newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		intent.Generation, intent.OperationID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.installExact(context.Background(), seventh); err != nil {
		t.Fatal(err)
	}
	intentFake.stage = *seventh.Stage
	expected, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		t.Fatal(err)
	}
	configFake := &gatewayRebindStageConfigCopyFake{
		gatewayRebindStageConfigIntentFake: intentFake, expected: expected,
		inventory: gatewayRebindStageConfigInventoryEmpty, copyMakesExact: true,
	}
	t.Cleanup(func() { clear(configFake.expected) })
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), configFake,
		gatewayRebindProgressTimestamp(8), nil); err != nil {
		t.Fatalf("install loopback sequence eight: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 {
		t.Fatalf("loopback sequence-eight history=%d error=%v", len(history.Progress), err)
	}
	configFake.stage = *history.Progress[7].Record.Stage
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageStartIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		configFake, gatewayRebindProgressTimestamp(9), nil); err != nil {
		t.Fatalf("install loopback sequence nine: %v", err)
	}
	history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 9 {
		t.Fatalf("loopback sequence-nine history=%d error=%v", len(history.Progress), err)
	}
	configFake.stage = *history.Progress[8].Record.Stage
	fake := &gatewayRebindStageStartFake{
		gatewayRebindStageConfigCopyFake: configFake, endpointID: strings.Repeat("e", 64),
	}
	if err := fixture.predecessor.manager.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(10), nil); err != nil {
		t.Fatalf("install loopback sequence ten: %v", err)
	}
	history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 10 {
		t.Fatalf("loopback sequence-ten history=%d error=%v", len(history.Progress), err)
	}
	fake.stage = *history.Progress[9].Record.Stage
	return fixture, fake, reads, loopbackID
}

func gatewayRebindFinalConfigLoopbackFixture(t *testing.T) (gatewayRebindProtectedIntent,
	gatewayUpgradeGenerationSelection, []appaccess.GatewayRebindRuntimeHead, string,
) {
	t.Helper()
	_, intent, predecessor, heads, loopbackID := gatewayRebindFinalConfigLoopbackFixtureWithStore(t)
	return intent, predecessor, heads, loopbackID
}

func gatewayRebindFinalConfigLoopbackFixtureWithStore(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindProtectedIntent, gatewayUpgradeGenerationSelection, []appaccess.GatewayRebindRuntimeHead, string,
) {
	t.Helper()
	fixture, intent := gatewayRebindProgressFixture(t)
	predecessor := gatewayUpgradeGenerationSelection{
		Store: fixture.store, Generation: fixture.store.generation, State: cloneGatewayV2RouteState(fixture.state),
		Journal: fixture.journal, Existing: true, operationID: fixture.journal.OperationID,
	}
	loopbackID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	var source gatewayV2AppRoute
	for _, app := range predecessor.State.Apps {
		source = cloneGatewayV2AppRoute(app)
		break
	}
	source.LAN = nil
	predecessor.State.Apps[loopbackID] = source
	stateDigest, err := canonicalDigest(predecessor.State)
	if err != nil {
		t.Fatal(err)
	}
	intent.Intent.Predecessor.StateDigest = stateDigest
	intent.Intent.Digest, err = gatewayRebindInitialSuccessorIntentDigest(intent.Intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.Digest, err = gatewayRebindProtectedIntentDigest(intent)
	if err != nil || !validGatewayRebindProtectedIntent(intent) {
		t.Fatalf("loopback intent invalid: %v", err)
	}
	heads := gatewayRebindFinalConfigHeadsForState(t, predecessor.State, intent.Intent.Roster)
	return fixture, intent, predecessor, heads, loopbackID
}

func persistGatewayRebindFinalConfigLoopbackFixture(t *testing.T, fixture gatewayRebindPredecessorFixture,
	intent gatewayRebindProtectedIntent, predecessor gatewayUpgradeGenerationSelection,
) {
	t.Helper()
	if err := fixture.store.writeExact(fixture.store.v2Path, fixture.store.v2Purpose,
		predecessor.State, false, maxV2RouteStateBytes); err != nil {
		t.Fatal(err)
	}
	intentStore, err := newGatewayRebindProtectedIntentStore(fixture.manager.options.DataRoot,
		intent.Generation, intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	state := gatewayUpgradeStateStore{directory: intentStore.directory}
	if err := state.writeExact(intentStore.path, intentStore.purpose, intent, false,
		maxGatewayRebindProtectedIntentBytes); err != nil {
		t.Fatal(err)
	}
}

func gatewayRebindFinalConfigProgressRecords(t *testing.T,
	intent gatewayRebindProtectedIntent,
) []gatewayRebindProgressRecord {
	t.Helper()
	records := make([]gatewayRebindProgressRecord, 0, 10)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, first)
	second, err := newGatewayRebindStageIntentProgress(intent, first,
		gatewayRebindProgressStageObservation(intent, 2))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, second)
	networkOwnership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	third, err := newGatewayRebindStageNetworkProgress(intent, second, strings.Repeat("a", 64),
		networkOwnership, gatewayRebindProgressTimestamp(3))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, third)
	configOwnership, err := gatewayRebindStageConfigVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := newGatewayRebindStageConfigVolumeProgress(intent, third,
		gatewayRebindStageConfigVolumeBinding{
			Name:       intent.Intent.Identity.ConfigVolume,
			Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.ConfigVolume + "/_data",
			CreatedAt:  "2026-10-03T12:00:03Z", OwnershipDigest: configOwnership,
		}, gatewayRebindProgressTimestamp(4))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, fourth)
	dataOwnership, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	fifth, err := newGatewayRebindStageDataVolumeProgress(intent, fourth,
		gatewayRebindStageDataVolumeBinding{
			Name:       intent.Intent.Identity.DataVolume,
			Mountpoint: "/var/lib/docker/volumes/" + intent.Intent.Identity.DataVolume + "/_data",
			CreatedAt:  "2026-10-03T12:00:04Z", OwnershipDigest: dataOwnership,
		}, gatewayRebindProgressTimestamp(5))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, fifth)
	containerOwnership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, *fifth.Stage)
	if err != nil {
		t.Fatal(err)
	}
	sixth, err := newGatewayRebindStageContainerProgress(intent, fifth, gatewayRebindStageContainerBinding{
		ID: strings.Repeat("c", 64), OwnershipDigest: containerOwnership, ConfigurationDigest: configuration,
	}, gatewayRebindProgressTimestamp(6))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, sixth)
	configIntent, err := gatewayRebindStageConfigIntentBindingFor(intent, sixth)
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(intent, sixth, configIntent,
		gatewayRebindProgressTimestamp(7))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, seventh)
	configCopy, err := gatewayRebindStageConfigCopyBindingFor(intent, seventh)
	if err != nil {
		t.Fatal(err)
	}
	eighth, err := newGatewayRebindStageConfigCopyProgress(intent, seventh, configCopy,
		gatewayRebindProgressTimestamp(8))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, eighth)
	start, err := gatewayRebindStageStartIntentBindingFor(intent, eighth)
	if err != nil {
		t.Fatal(err)
	}
	ninth, err := newGatewayRebindStageStartIntentProgress(intent, eighth, start,
		gatewayRebindProgressTimestamp(9))
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, ninth)
	serving, err := gatewayRebindStageServingBindingFor(intent, ninth, strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	tenth, err := newGatewayRebindStageServingProgress(intent, ninth, serving,
		gatewayRebindProgressTimestamp(10))
	if err != nil {
		t.Fatal(err)
	}
	return append(records, tenth)
}

func gatewayRebindFinalConfigHeadsForState(t *testing.T, state gatewayV2RouteState,
	roster []appaccess.GatewayRebindRosterEntry,
) []appaccess.GatewayRebindRuntimeHead {
	t.Helper()
	byApp := make(map[string]appaccess.GatewayRebindRosterEntry, len(roster))
	for _, entry := range roster {
		byApp[entry.AppID] = entry
	}
	appIDs := make([]string, 0, len(state.Apps))
	for appID := range state.Apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	result := make([]appaccess.GatewayRebindRuntimeHead, 0, len(appIDs))
	for index, appID := range appIDs {
		app := state.Apps[appID]
		head := appaccess.GatewayRebindRuntimeHead{
			AppID: appID, DeploymentID: uuid.NewString(), ReleaseID: strings.Repeat("a", 32),
			Slot: string(app.Route.Slot), Generation: int64(index + 1),
			UpdatedAt: time.Date(2026, time.October, 4, 12, 0, index, 0, time.UTC),
		}
		if entry, ok := byApp[appID]; ok {
			head.DeploymentID, head.ReleaseID = entry.ServingDeploymentID, entry.ServingReleaseID
			head.Slot, head.Generation = entry.ServingSlot, entry.RouteGeneration
		}
		result = append(result, head)
	}
	return result
}

func cloneGatewayRebindFinalConfigRoutes(values []gatewayRebindFinalConfigRouteBinding) []gatewayRebindFinalConfigRouteBinding {
	result := make([]gatewayRebindFinalConfigRouteBinding, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Route.Endpoints = append(result[index].Route.Endpoints[:0:0], value.Route.Endpoints...)
		if value.SourceLAN != nil {
			copyLAN := *value.SourceLAN
			result[index].SourceLAN = &copyLAN
		}
	}
	return result
}

func oppositeGatewayRebindTestSlot(value string) string {
	if value == "blue" {
		return "green"
	}
	return "blue"
}
