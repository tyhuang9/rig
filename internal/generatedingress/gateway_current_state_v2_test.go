package generatedingress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

// This mirrors the accepted pre-origin-union wire type. Keep the value
// sourceV1 field and field order independent from the production serializer so
// pointer/omitempty changes cannot make the compatibility oracle tautological.
type gatewayCurrentRouteStateV2PreOriginOracle struct {
	Version                uint64                              `json:"version"`
	Revision               uint64                              `json:"revision"`
	Lineage                appaccess.GatewayCurrentLineageRef  `json:"lineage"`
	Profile                gatewayProfileBinding               `json:"profile"`
	Identity               gatewayCurrentIdentity              `json:"identity"`
	Network                gatewayV2NetworkPlan                `json:"network"`
	TransferManifestDigest string                              `json:"transferManifestDigest"`
	SourceV1               gatewayCurrentRouteSourceV1         `json:"sourceV1"`
	Apps                   map[string]gatewayCurrentAppRouteV2 `json:"apps"`
	Digest                 string                              `json:"digest"`
}

type gatewayCurrentRouteStateDigestEnvelopeV2PreOriginOracle struct {
	Purpose string                                    `json:"purpose"`
	State   gatewayCurrentRouteStateV2PreOriginOracle `json:"state"`
}

func gatewayCurrentRouteStateV2ForTest(t *testing.T, fixture gatewayCurrentStateFixture) gatewayCurrentRouteStateV2 {
	t.Helper()
	apps := make(map[string]gatewayCurrentAppRouteV2, len(fixture.baseline.Apps))
	generation := int64(1)
	for appID, app := range fixture.baseline.Apps {
		apps[appID] = gatewayCurrentAppRouteV2{
			Route:   cloneGatewayCurrentAppRoute(app).Route,
			Runtime: gatewayCurrentRuntimePinV2{DeploymentID: uuid.NewString(), ReleaseID: uuid.NewString(), Slot: app.Route.Slot, ActiveGeneration: generation},
			Traffic: gatewayCurrentTrafficServingV2,
			Authority: gatewayCurrentAppliedAuthorityV2{
				DesiredMode: gatewayCurrentDesiredUninitializedV2,
			},
			LAN: cloneGatewayCurrentAppRoute(app).LAN,
		}
		generation++
	}
	value := gatewayCurrentRouteStateV2{
		Version: gatewayCurrentRouteStateVersionV2, Revision: 1,
		Lineage: fixture.baseline.Lineage, Profile: fixture.baseline.Profile,
		Identity: cloneGatewayCurrentRouteState(fixture.baseline).Identity,
		Network:  fixture.baseline.Network, TransferManifestDigest: fixture.baseline.TransferManifestDigest,
		SourceV1: &gatewayCurrentRouteSourceV1{Revision: fixture.baseline.Revision, Digest: fixture.baseline.Digest},
		Apps:     apps,
	}
	var err error
	value.Digest, err = gatewayCurrentRouteStateDigestV2(value)
	if err != nil || !validGatewayCurrentRouteStateV2(value) {
		t.Fatalf("build current V2 fixture: %v", err)
	}
	return value
}

func gatewayCurrentRouteStateV2FirstApp(t *testing.T, value gatewayCurrentRouteStateV2) string {
	t.Helper()
	ids := make([]string, 0, len(value.Apps))
	for appID := range value.Apps {
		ids = append(ids, appID)
	}
	if len(ids) == 0 {
		t.Fatal("current V2 fixture has no applications")
	}
	sort.Strings(ids)
	return ids[0]
}

func gatewayCurrentRouteStateV2AuthoredAuthority(mode gatewayCurrentDesiredModeV2,
	boundary gatewayCurrentLifecycleBoundaryV2,
) gatewayCurrentAppliedAuthorityV2 {
	return gatewayCurrentAppliedAuthorityV2{
		DesiredMode: mode, DesiredRevision: 1,
		Lifecycle: &gatewayCurrentLifecycleAuthorityV2{
			OperationID: uuid.NewString(), PinsDigest: strings.Repeat("d", 64),
			Boundary: boundary, IntentDigest: strings.Repeat("e", 64),
		},
	}
}

func gatewayCurrentRouteStateV2WithDormantApp(t *testing.T, value gatewayCurrentRouteStateV2,
	mode gatewayCurrentDesiredModeV2,
) gatewayCurrentRouteStateV2 {
	t.Helper()
	result := cloneGatewayCurrentRouteStateV2(value)
	appID := gatewayCurrentRouteStateV2FirstApp(t, result)
	app := result.Apps[appID]
	app.Traffic = gatewayCurrentTrafficDormantV2
	app.Authority = gatewayCurrentRouteStateV2AuthoredAuthority(mode, gatewayCurrentLifecycleWithdrawV2)
	result.Apps[appID] = app
	result.Digest, _ = gatewayCurrentRouteStateDigestV2(result)
	return result
}

func gatewayCurrentRouteStateV2RawBody(t *testing.T, value gatewayCurrentRouteStateV2) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func gatewayCurrentRouteStateV2ReplaceOnce(t *testing.T, body, old, replacement []byte) []byte {
	t.Helper()
	if bytes.Count(body, old) != 1 {
		t.Fatalf("canonical V2 fixture occurrence count=%d want=1", bytes.Count(body, old))
	}
	return bytes.Replace(body, old, replacement, 1)
}

func gatewayCurrentRouteStateV2WriteRaw(t *testing.T, store *gatewayCurrentRouteStateV2Store, body []byte) {
	t.Helper()
	if err := upgradeProtectedWriteNew(store.path, store.purpose, body); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayCurrentRouteStateV2Storage(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	baseline := gatewayCurrentRouteStateV2ForTest(t, fixture)

	t.Run("closed_authority_matrix", func(t *testing.T) {
		legacy := gatewayCurrentAppliedAuthorityV2{DesiredMode: gatewayCurrentDesiredUninitializedV2}
		runningPublish := gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredRunningV2, gatewayCurrentLifecyclePublishV2)
		runningWithdraw := gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredRunningV2, gatewayCurrentLifecycleWithdrawV2)
		stoppedWithdraw := gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredStoppedV2, gatewayCurrentLifecycleWithdrawV2)
		stoppedPublish := gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredStoppedV2, gatewayCurrentLifecyclePublishV2)
		cases := []struct {
			name      string
			traffic   gatewayCurrentTrafficV2
			authority gatewayCurrentAppliedAuthorityV2
			want      bool
		}{
			{name: "legacy_serving", traffic: gatewayCurrentTrafficServingV2, authority: legacy, want: true},
			{name: "legacy_dormant", traffic: gatewayCurrentTrafficDormantV2, authority: legacy},
			{name: "running_published_serving", traffic: gatewayCurrentTrafficServingV2, authority: runningPublish, want: true},
			{name: "running_withdrawn_dormant", traffic: gatewayCurrentTrafficDormantV2, authority: runningWithdraw, want: true},
			{name: "stopped_withdrawn_dormant", traffic: gatewayCurrentTrafficDormantV2, authority: stoppedWithdraw, want: true},
			{name: "running_withdrawn_serving", traffic: gatewayCurrentTrafficServingV2, authority: runningWithdraw},
			{name: "running_published_dormant", traffic: gatewayCurrentTrafficDormantV2, authority: runningPublish},
			{name: "stopped_published_serving", traffic: gatewayCurrentTrafficServingV2, authority: stoppedPublish},
			{name: "unknown_traffic", traffic: "future", authority: runningPublish},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				if got := validGatewayCurrentAppliedAuthorityV2(test.traffic, test.authority); got != test.want {
					t.Fatalf("valid=%v want=%v", got, test.want)
				}
			})
		}
		invalid := runningPublish
		invalid.DesiredRevision = 0
		if validGatewayCurrentAppliedAuthorityV2(gatewayCurrentTrafficServingV2, invalid) {
			t.Fatal("authored revision zero was accepted")
		}
		invalid = runningPublish
		invalid.Lifecycle.Boundary = "future"
		if validGatewayCurrentAppliedAuthorityV2(gatewayCurrentTrafficServingV2, invalid) {
			t.Fatal("unknown lifecycle boundary was accepted")
		}
	})

	t.Run("fixed_shape_and_canonical_digests", func(t *testing.T) {
		body, err := json.Marshal(baseline)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		wantKeys := []string{"apps", "digest", "identity", "lineage", "network", "profile", "revision", "sourceV1", "transferManifestDigest", "version"}
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Fatalf("current V2 JSON shape changed: got=%v want=%v", keys, wantKeys)
		}
		appID := gatewayCurrentRouteStateV2FirstApp(t, baseline)
		first, err := gatewayCurrentAppRouteDigestV2(appID, baseline.Profile, baseline.Apps[appID])
		second, secondErr := gatewayCurrentAppRouteDigestV2(appID, baseline.Profile, cloneGatewayCurrentAppRouteV2(baseline.Apps[appID]))
		if err != nil || secondErr != nil || first != second || !validSHA256(first) {
			t.Fatalf("canonical target digest mismatch: first=%s second=%s errors=%v/%v", first, second, err, secondErr)
		}
		changed := cloneGatewayCurrentAppRouteV2(baseline.Apps[appID])
		changed.Runtime.ActiveGeneration++
		changedDigest, err := gatewayCurrentAppRouteDigestV2(appID, baseline.Profile, changed)
		if err != nil || changedDigest == first {
			t.Fatalf("runtime pin was not bound into target digest: digest=%s error=%v", changedDigest, err)
		}
		wrongSlot := cloneGatewayCurrentRouteStateV2(baseline)
		app := wrongSlot.Apps[appID]
		if app.Runtime.Slot == "blue" {
			app.Runtime.Slot = "green"
		} else {
			app.Runtime.Slot = "blue"
		}
		wrongSlot.Apps[appID] = app
		wrongSlot.Digest, _ = gatewayCurrentRouteStateDigestV2(wrongSlot)
		if validGatewayCurrentRouteStateV2(wrongSlot) {
			t.Fatal("route/runtime slot mismatch was accepted")
		}
	})

	t.Run("pre_origin_union_rebind_wire_digest_load_and_save_compatibility", func(t *testing.T) {
		const literalDigest = "81bc41cdb37d51c1b7e577d28e37543feb7271f8f037ad519c3223beaeac7ce4"
		const literalWire = `{"version":2,"revision":1,"lineage":{"kind":"","operationId":"","profileRevisionId":"","profileRevisionNumber":0,"profileSpecDigest":"","protectedGeneration":0,"protectedIdentityDigest":""},"profile":{"revisionId":"","revisionNumber":0,"specDigest":"","selectedIpv4":"","interfaceId":"","portStart":0,"portEnd":0},"identity":{"kind":""},"network":{"subnet":"","gatewayIpv4":"","containerIpv4":""},"transferManifestDigest":"","sourceV1":{"revision":7,"digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"apps":{},"digest":"81bc41cdb37d51c1b7e577d28e37543feb7271f8f037ad519c3223beaeac7ce4"}`
		literal := gatewayCurrentRouteStateV2{
			Version: 2, Revision: 1,
			SourceV1: &gatewayCurrentRouteSourceV1{
				Revision: 7, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			Apps:   map[string]gatewayCurrentAppRouteV2{},
			Digest: literalDigest,
		}
		literalBytes, err := json.Marshal(literal)
		if err != nil || string(literalBytes) != literalWire {
			t.Fatalf("pre-union literal wire changed: got=%s want=%s error=%v", literalBytes, literalWire, err)
		}
		literalComputed, err := gatewayCurrentRouteStateDigestV2(literal)
		if err != nil || literalComputed != literalDigest {
			t.Fatalf("pre-union literal digest changed: got=%s want=%s error=%v", literalComputed, literalDigest, err)
		}

		oracle := gatewayCurrentRouteStateV2PreOriginOracle{
			Version: baseline.Version, Revision: baseline.Revision, Lineage: baseline.Lineage,
			Profile: baseline.Profile, Identity: baseline.Identity, Network: baseline.Network,
			TransferManifestDigest: baseline.TransferManifestDigest, SourceV1: *baseline.SourceV1,
			Apps: baseline.Apps,
		}
		oracleDigest, err := canonicalDigest(gatewayCurrentRouteStateDigestEnvelopeV2PreOriginOracle{
			Purpose: "hostd/generated-ingress/routes/current/v2/state-digest/v1", State: oracle,
		})
		if err != nil {
			t.Fatal(err)
		}
		oracle.Digest = oracleDigest
		wantBytes, err := json.Marshal(oracle)
		if err != nil {
			t.Fatal(err)
		}
		gotBytes, err := json.Marshal(baseline)
		if err != nil || !bytes.Equal(gotBytes, wantBytes) || baseline.Digest != oracleDigest {
			t.Fatalf("prechange rebind bytes/digest changed: bytesEqual=%v gotDigest=%s wantDigest=%s error=%v",
				bytes.Equal(gotBytes, wantBytes), baseline.Digest, oracleDigest, err)
		}

		store, err := newGatewayCurrentRouteStateV2Store(t.TempDir(), baseline.Lineage, *baseline.SourceV1)
		if err != nil {
			t.Fatal(err)
		}
		gatewayCurrentRouteStateV2WriteRaw(t, store, wantBytes)
		loaded, err := store.load()
		if err != nil || !reflect.DeepEqual(loaded, baseline) {
			t.Fatalf("load prechange rebind bytes: %v", err)
		}
		next := gatewayCurrentRouteStateV2WithDormantApp(t, loaded, gatewayCurrentDesiredStoppedV2)
		next.Revision++
		next.Digest, _ = gatewayCurrentRouteStateDigestV2(next)
		if err := store.saveNext(loaded, next); err != nil {
			t.Fatalf("save from prechange rebind bytes: %v", err)
		}
		confirmed, err := store.load()
		if err != nil || !reflect.DeepEqual(confirmed, next) {
			t.Fatalf("reload after compatible save: %v", err)
		}
	})

	t.Run("exclusive_source_origin_union_and_upgrade_store", func(t *testing.T) {
		upgrade := gatewayCurrentProjectionV2UpgradeFixture(t, fixture, baseline)
		if upgrade.SourceV1 != nil || upgrade.SourceUpgrade == nil || !validGatewayCurrentRouteStateV2(upgrade) {
			t.Fatalf("genuine upgrade origin is invalid: %#v", gatewayCurrentRouteOriginForStateV2(upgrade))
		}
		for _, generation := range []uint64{0, 7} {
			value := cloneGatewayCurrentRouteStateV2(upgrade)
			value.Lineage.ProtectedGeneration = generation
			value.Digest, _ = gatewayCurrentRouteStateDigestV2(value)
			store, err := newGatewayCurrentRouteStateV2StoreForOrigin(t.TempDir(), value.Lineage,
				gatewayCurrentRouteOriginForStateV2(value))
			if err != nil || store.installBaseline(value) != nil {
				t.Fatalf("install upgrade generation %d: %v", generation, err)
			}
			loaded, err := store.load()
			if err != nil || !reflect.DeepEqual(value, loaded) {
				t.Fatalf("load upgrade generation %d: %v", generation, err)
			}
		}

		both := cloneGatewayCurrentRouteStateV2(upgrade)
		sourceV1 := *baseline.SourceV1
		both.SourceV1 = &sourceV1
		both.Digest, _ = gatewayCurrentRouteStateDigestV2(both)
		neither := cloneGatewayCurrentRouteStateV2(upgrade)
		neither.SourceUpgrade = nil
		neither.Digest, _ = gatewayCurrentRouteStateDigestV2(neither)
		wrong := cloneGatewayCurrentRouteStateV2(upgrade)
		wrong.SourceUpgrade.StateDigest = strings.Repeat("z", 64)
		wrong.Digest, _ = gatewayCurrentRouteStateDigestV2(wrong)
		wrongLineage := cloneGatewayCurrentRouteStateV2(upgrade)
		wrongLineage.Lineage.ProtectedJournalDigest = ""
		wrongLineage.Digest, _ = gatewayCurrentRouteStateDigestV2(wrongLineage)
		for name, value := range map[string]gatewayCurrentRouteStateV2{
			"both": both, "neither": neither, "wrong_origin": wrong, "wrong_lineage": wrongLineage,
		} {
			t.Run(name, func(t *testing.T) {
				if validGatewayCurrentRouteStateV2(value) {
					t.Fatal("invalid origin union was accepted")
				}
			})
		}

		copy := cloneGatewayCurrentRouteStateV2(upgrade)
		copy.SourceUpgrade.StateDigest = strings.Repeat("e", 64)
		if copy.SourceUpgrade.StateDigest == upgrade.SourceUpgrade.StateDigest {
			t.Fatal("upgrade origin clone aliases source")
		}
	})

	t.Run("dormant_retains_runtime_and_optional_lan", func(t *testing.T) {
		value := gatewayCurrentRouteStateV2WithDormantApp(t, baseline, gatewayCurrentDesiredStoppedV2)
		appID := gatewayCurrentRouteStateV2FirstApp(t, value)
		beforeRuntime := baseline.Apps[appID].Runtime
		app := value.Apps[appID]
		app.LAN = nil
		value.Apps[appID] = app
		value.Digest, _ = gatewayCurrentRouteStateDigestV2(value)
		if !validGatewayCurrentRouteStateV2(value) || value.Apps[appID].Runtime != beforeRuntime {
			t.Fatalf("local-only dormant record lost its retained runtime: %#v", value.Apps[appID])
		}
	})

	t.Run("runtime_and_cross_app_identity_refusals", func(t *testing.T) {
		appID := gatewayCurrentRouteStateV2FirstApp(t, baseline)
		for _, test := range []struct {
			name   string
			mutate func(*gatewayCurrentRuntimePinV2)
		}{
			{name: "missing_deployment", mutate: func(value *gatewayCurrentRuntimePinV2) { value.DeploymentID = "" }},
			{name: "uppercase_release", mutate: func(value *gatewayCurrentRuntimePinV2) { value.ReleaseID = strings.Repeat("A", 32) }},
			{name: "unknown_slot", mutate: func(value *gatewayCurrentRuntimePinV2) { value.Slot = "future" }},
			{name: "zero_generation", mutate: func(value *gatewayCurrentRuntimePinV2) { value.ActiveGeneration = 0 }},
		} {
			t.Run(test.name, func(t *testing.T) {
				value := cloneGatewayCurrentRouteStateV2(baseline)
				app := value.Apps[appID]
				test.mutate(&app.Runtime)
				value.Apps[appID] = app
				value.Digest, _ = gatewayCurrentRouteStateDigestV2(value)
				if validGatewayCurrentRouteStateV2(value) {
					t.Fatal("invalid runtime pin was accepted")
				}
			})
		}
		twoApps := cloneGatewayCurrentRouteStateV2(baseline)
		secondID := uuid.NewString()
		second := cloneGatewayCurrentAppRouteV2(twoApps.Apps[appID])
		second.LAN = nil
		second.Runtime.DeploymentID = uuid.NewString()
		twoApps.Apps[secondID] = second
		twoApps.Digest, _ = gatewayCurrentRouteStateDigestV2(twoApps)
		if !validGatewayCurrentRouteStateV2(twoApps) {
			t.Fatal("valid two-app identity fixture was rejected")
		}
		duplicate := cloneGatewayCurrentRouteStateV2(twoApps)
		second = duplicate.Apps[secondID]
		second.Runtime.DeploymentID = duplicate.Apps[appID].Runtime.DeploymentID
		duplicate.Apps[secondID] = second
		duplicate.Digest, _ = gatewayCurrentRouteStateDigestV2(duplicate)
		if validGatewayCurrentRouteStateV2(duplicate) {
			t.Fatal("one deployment identity was accepted for multiple applications")
		}
	})

	t.Run("application_bound_rejects_otherwise_valid_unique_local_records", func(t *testing.T) {
		templateID := gatewayCurrentRouteStateV2FirstApp(t, baseline)
		template := cloneGatewayCurrentAppRouteV2(baseline.Apps[templateID])
		template.LAN = nil
		value := cloneGatewayCurrentRouteStateV2(baseline)
		value.Apps = make(map[string]gatewayCurrentAppRouteV2, maxStateApps+1)
		add := func(index int) {
			appID := uuid.NewString()
			app := cloneGatewayCurrentAppRouteV2(template)
			app.Runtime.DeploymentID = uuid.NewString()
			network := fmt.Sprintf("lifecycle-app-network-%d", index)
			for endpoint := range app.Route.Endpoints {
				app.Route.Endpoints[endpoint].ContainerID = fmt.Sprintf("%064x", index*len(app.Route.Endpoints)+endpoint+1)
				app.Route.Endpoints[endpoint].NetworkName = network
			}
			value.Apps[appID] = app
		}
		for index := 0; index < maxStateApps; index++ {
			add(index)
		}
		value.Digest, _ = gatewayCurrentRouteStateDigestV2(value)
		if !validGatewayCurrentRouteStateV2(value) {
			t.Fatal("maximum bounded set of unique local-only records was rejected")
		}
		add(maxStateApps)
		value.Digest, _ = gatewayCurrentRouteStateDigestV2(value)
		if validGatewayCurrentRouteStateV2(value) {
			t.Fatal("application set above the fixed bound was accepted")
		}
	})

	t.Run("exclusive_baseline_and_revision_cas", func(t *testing.T) {
		dataRoot := t.TempDir()
		store, err := newGatewayCurrentRouteStateV2Store(dataRoot, baseline.Lineage, *baseline.SourceV1)
		if err != nil || store.installBaseline(baseline) != nil || store.installBaseline(baseline) != nil {
			t.Fatalf("install or exact replay: %v", err)
		}
		changedBaseline := gatewayCurrentRouteStateV2WithDormantApp(t, baseline, gatewayCurrentDesiredStoppedV2)
		if err := store.installBaseline(changedBaseline); err == nil {
			t.Fatal("changed create-exclusive replay was accepted")
		}
		loaded, err := store.load()
		if err != nil || !reflect.DeepEqual(loaded, baseline) {
			t.Fatalf("load baseline: %v", err)
		}
		next := gatewayCurrentRouteStateV2WithDormantApp(t, baseline, gatewayCurrentDesiredStoppedV2)
		next.Revision++
		next.Digest, _ = gatewayCurrentRouteStateDigestV2(next)
		if err := store.saveNext(baseline, next); err != nil {
			t.Fatalf("save next: %v", err)
		}
		staleNext := cloneGatewayCurrentRouteStateV2(next)
		if err := store.saveNext(baseline, staleNext); err == nil {
			t.Fatal("stale previous revision was accepted")
		}
		skipped := cloneGatewayCurrentRouteStateV2(next)
		skipped.Revision += 2
		skipped.Digest, _ = gatewayCurrentRouteStateDigestV2(skipped)
		if err := store.saveNext(next, skipped); err == nil {
			t.Fatal("skipped revision was accepted")
		}
		changedOrigin := cloneGatewayCurrentRouteStateV2(next)
		changedOrigin.Revision++
		changedOrigin.SourceV1.Revision++
		changedOrigin.Digest, _ = gatewayCurrentRouteStateDigestV2(changedOrigin)
		if err := store.saveNext(next, changedOrigin); err == nil {
			t.Fatal("changed immutable V1 origin was accepted")
		}
		wrongSource := *baseline.SourceV1
		wrongSource.Revision++
		wrongStore, err := newGatewayCurrentRouteStateV2Store(dataRoot, baseline.Lineage, wrongSource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = wrongStore.load(); err == nil {
			t.Fatal("wrong source V1 revision loaded existing V2 state")
		}
		wrongLineage := baseline.Lineage
		wrongLineage.TerminalReceiptDigest = strings.Repeat("f", 64)
		if wrongLineage.TerminalReceiptDigest == baseline.Lineage.TerminalReceiptDigest {
			wrongLineage.TerminalReceiptDigest = strings.Repeat("e", 64)
		}
		wrongLineageStore, err := newGatewayCurrentRouteStateV2Store(dataRoot, wrongLineage, *baseline.SourceV1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = wrongLineageStore.load(); err == nil {
			t.Fatal("different full lineage sharing the scoped path loaded existing V2 state")
		}
	})

	t.Run("canonical_decode_rejects_duplicate_alias_and_unknown_keys", func(t *testing.T) {
		canonical := gatewayCurrentRouteStateV2RawBody(t, baseline)
		var top map[string]json.RawMessage
		if err := json.Unmarshal(canonical, &top); err != nil {
			t.Fatal(err)
		}
		appsField := append([]byte(`"apps":`), top["apps"]...)
		duplicateTopApps := append(append(append([]byte{}, appsField...), ','), appsField...)
		duplicateTopApps = gatewayCurrentRouteStateV2ReplaceOnce(t, canonical, appsField, duplicateTopApps)

		single := cloneGatewayCurrentRouteStateV2(baseline)
		appID := gatewayCurrentRouteStateV2FirstApp(t, single)
		app := single.Apps[appID]
		single.Apps = map[string]gatewayCurrentAppRouteV2{appID: app}
		single.Digest, _ = gatewayCurrentRouteStateDigestV2(single)
		singleBody := gatewayCurrentRouteStateV2RawBody(t, single)
		var singleTop map[string]json.RawMessage
		if err := json.Unmarshal(singleBody, &singleTop); err != nil {
			t.Fatal(err)
		}
		appBody, err := json.Marshal(app)
		if err != nil {
			t.Fatal(err)
		}
		oneAppsField := append([]byte(`"apps":`), singleTop["apps"]...)
		duplicateAppMap := []byte(fmt.Sprintf(`"apps":{"%s":%s,"%s":%s}`, appID, appBody, appID, appBody))
		duplicateAppMap = gatewayCurrentRouteStateV2ReplaceOnce(t, singleBody, oneAppsField, duplicateAppMap)

		caseAliasedTop := gatewayCurrentRouteStateV2ReplaceOnce(t, canonical,
			[]byte(`"sourceV1":`), []byte(`"SourceV1":`))
		caseAliasedNested := gatewayCurrentRouteStateV2ReplaceOnce(t, canonical,
			[]byte(`"activeGeneration":`), []byte(`"ActiveGeneration":`))
		sourceField := append([]byte(`"sourceV1":`), top["sourceV1"]...)
		sourceWithUnknown := append(append([]byte{}, top["sourceV1"][:len(top["sourceV1"])-1]...), []byte(`,"future":true}`)...)
		nestedUnknown := gatewayCurrentRouteStateV2ReplaceOnce(t, canonical, sourceField,
			append([]byte(`"sourceV1":`), sourceWithUnknown...))
		withPending := gatewayCurrentRouteStateV2ReplaceOnce(t, canonical, appsField,
			append(append([]byte{}, appsField...), []byte(`,"pending":{"kind":"lifecycle_withdraw"}`)...))
		withLANRecovery := gatewayCurrentRouteStateV2ReplaceOnce(t, canonical, appsField,
			append(append([]byte{}, appsField...), []byte(`,"lanRecovery":{"version":1}`)...))

		cases := []struct {
			name string
			body []byte
		}{
			{name: "duplicate_top_level_apps", body: duplicateTopApps},
			{name: "duplicate_app_id_map_key", body: duplicateAppMap},
			{name: "case_aliased_top_level_field", body: caseAliasedTop},
			{name: "case_aliased_nested_field", body: caseAliasedNested},
			{name: "nested_unknown_field", body: nestedUnknown},
			{name: "unimplemented_pending", body: withPending},
			{name: "unimplemented_lan_recovery", body: withLANRecovery},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				var normalized gatewayCurrentRouteStateV2
				if err := json.Unmarshal(test.body, &normalized); err != nil || !validGatewayCurrentRouteStateV2(normalized) {
					t.Fatalf("malformed control did not normalize to the valid signed state: %v", err)
				}
				store, err := newGatewayCurrentRouteStateV2Store(t.TempDir(), baseline.Lineage, *baseline.SourceV1)
				if err != nil {
					t.Fatal(err)
				}
				gatewayCurrentRouteStateV2WriteRaw(t, store, test.body)
				if _, err := store.load(); err == nil {
					t.Fatal("noncanonical protected shape was accepted")
				}
			})
		}

		future := cloneGatewayCurrentRouteStateV2(baseline)
		future.Version++
		future.Digest, _ = gatewayCurrentRouteStateDigestV2(future)
		store, err := newGatewayCurrentRouteStateV2Store(t.TempDir(), baseline.Lineage, *baseline.SourceV1)
		if err != nil {
			t.Fatal(err)
		}
		protected := gatewayUpgradeStateStore{directory: store.directory}
		if err := protected.writeExact(store.path, store.purpose, future, true, maxGatewayCurrentRouteStateBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(); err == nil {
			t.Fatal("unknown V2 state version was accepted")
		}
	})

	t.Run("bounded_protected_payload", func(t *testing.T) {
		store, err := newGatewayCurrentRouteStateV2Store(t.TempDir(), baseline.Lineage, *baseline.SourceV1)
		if err != nil {
			t.Fatal(err)
		}
		protected := gatewayUpgradeStateStore{directory: store.directory}
		oversized := map[string]string{"payload": strings.Repeat("x", maxGatewayCurrentRouteStateBytes)}
		if err := protected.writeExact(store.path, store.purpose, oversized, true, maxGatewayCurrentRouteStateBytes); err == nil {
			t.Fatal("oversized V2 protected state was written")
		}
		if err := upgradeProtectedWriteNew(store.path, store.purpose, bytes.Repeat([]byte{'x'}, maxGatewayCurrentRouteStateBytes+1)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(); err == nil {
			t.Fatal("oversized V2 protected state was loaded")
		}
	})

	t.Run("v1_bytes_digest_and_store_are_unchanged", func(t *testing.T) {
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(fixture.store.path)
		if err != nil {
			t.Fatal(err)
		}
		beforeDigest, err := gatewayCurrentRouteStateDigest(fixture.baseline)
		if err != nil || beforeDigest != fixture.baseline.Digest {
			t.Fatalf("V1 digest mismatch before V2 install: digest=%s error=%v", beforeDigest, err)
		}
		var v1Object map[string]json.RawMessage
		body, _ := json.Marshal(fixture.baseline)
		if err := json.Unmarshal(body, &v1Object); err != nil {
			t.Fatal(err)
		}
		v1Keys := make([]string, 0, len(v1Object))
		for key := range v1Object {
			v1Keys = append(v1Keys, key)
		}
		sort.Strings(v1Keys)
		wantV1Keys := []string{"apps", "digest", "identity", "lineage", "network", "profile", "revision", "transferManifestDigest", "version"}
		if !reflect.DeepEqual(v1Keys, wantV1Keys) {
			t.Fatalf("V1 canonical shape changed: got=%v want=%v", v1Keys, wantV1Keys)
		}
		v2Store, err := newGatewayCurrentRouteStateV2Store(fixture.dataRoot, baseline.Lineage, *baseline.SourceV1)
		if err != nil || v2Store.installBaseline(baseline) != nil {
			t.Fatalf("install adjacent V2: %v", err)
		}
		after, err := os.ReadFile(fixture.store.path)
		loaded, loadErr := fixture.store.load()
		if err != nil || loadErr != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(loaded, fixture.baseline) {
			t.Fatalf("V2 install changed V1: read=%v load=%v bytesEqual=%v", err, loadErr, bytes.Equal(before, after))
		}
		v1Name, v1Purpose := gatewayCurrentRouteStateName(baseline.Lineage.ProtectedGeneration, baseline.Lineage.OperationID)
		v2Name, v2Purpose := gatewayCurrentRouteStateNameV2(baseline.Lineage.ProtectedGeneration, baseline.Lineage.OperationID)
		if v1Name == v2Name || v1Purpose == v2Purpose || filepathBase(v2Store.path) != v2Name {
			t.Fatalf("V1/V2 storage scopes overlap: v1=%s/%s v2=%s/%s", v1Name, v1Purpose, v2Name, v2Purpose)
		}
	})

	t.Run("clone_isolation", func(t *testing.T) {
		copy := cloneGatewayCurrentRouteStateV2(baseline)
		appID := gatewayCurrentRouteStateV2FirstApp(t, copy)
		app := copy.Apps[appID]
		originalContainer := baseline.Apps[appID].Route.Endpoints[0].ContainerID
		app.Route.Endpoints[0].ContainerID = strings.Repeat("f", 64)
		app.Authority = gatewayCurrentRouteStateV2AuthoredAuthority(gatewayCurrentDesiredRunningV2, gatewayCurrentLifecyclePublishV2)
		copy.Apps[appID] = app
		if baseline.Apps[appID].Route.Endpoints[0].ContainerID != originalContainer || baseline.Apps[appID].Authority.Lifecycle != nil {
			t.Fatal("V2 clone mutation changed the source state")
		}
	})
}
