package generatedingress

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/generatedroute"
)

func gatewayCurrentRebindSnapshotForV2Test(fixture gatewayCurrentStateFixture) appaccess.GatewayRebindRecoverySnapshot {
	profile := gatewayCurrentFixtureProfile(fixture.baseline.Lineage, fixture.baseline.Profile)
	authority := gatewayCurrentAuthority(fixture.baseline.Lineage)
	event := appaccess.GatewayRebindEvent{OperationID: fixture.receipt.OperationID,
		Sequence: 4, State: appaccess.GatewayRebindDatabaseCommitted}
	return appaccess.GatewayRebindRecoverySnapshot{
		History: []appaccess.GatewayRebindHistoryEntry{{
			Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: 1, Legacy: &appaccess.GatewayRebindClaim{
				Spec: appaccess.GatewayRebindSpec{OperationID: fixture.receipt.OperationID}}},
			Events: []appaccess.GatewayRebindEvent{event}, Transfers: fixture.transfers,
		}},
		CurrentProfile: &profile, CurrentSource: &authority, CurrentDatabaseCommittedEvent: &event,
		CurrentTransfers: append([]appaccess.GatewayRebindAllocationTransfer(nil), fixture.transfers...),
	}
}

func gatewayCurrentUpgradeSnapshotForV2Test(t *testing.T,
	fixture gatewayCurrentStateFixture,
) appaccess.GatewayRebindRecoverySnapshot {
	t.Helper()
	lineage, err := gatewayUpgradeCurrentLineage(fixture.history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	profile := gatewayCurrentFixtureProfile(lineage, fixture.history.Predecessor.State.Profile)
	authority := gatewayCurrentAuthority(lineage)
	return appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: &profile, CurrentSource: &authority}
}

func gatewayCurrentInstallV2TestState(t *testing.T, dataRoot string, value gatewayCurrentRouteStateV2) {
	t.Helper()
	store, err := newGatewayCurrentRouteStateV2StoreForOrigin(dataRoot, value.Lineage,
		gatewayCurrentRouteOriginForStateV2(value))
	if err != nil || store.installBaseline(value) != nil {
		t.Fatalf("install current V2 test state: %v", err)
	}
}

func TestGatewayCurrentProtectedSelectionV2(t *testing.T) {
	t.Run("proved_absence_returns_legacy_and_valid_rebind_V2_wins", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayCurrentRebindSnapshotForV2Test(fixture)
		fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
			func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) { return snapshot, nil })
		selected, gotSQL, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
		if err != nil || selected.Legacy == nil || selected.V2 != nil || !reflect.DeepEqual(snapshot, gotSQL) {
			t.Fatalf("absent V2 selection=%#v error=%v", selected, err)
		}

		baseline := gatewayCurrentRouteStateV2ForTest(t, fixture)
		gatewayCurrentInstallV2TestState(t, fixture.dataRoot, baseline)
		selected, gotSQL, err = fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
		if err != nil || selected.Legacy != nil || selected.V2 == nil ||
			!reflect.DeepEqual(selected.V2.State, baseline) || !reflect.DeepEqual(snapshot, gotSQL) {
			t.Fatalf("V2 selection=%#v error=%v", selected, err)
		}
		name, _ := gatewayCurrentRouteStateNameV2(baseline.Lineage.ProtectedGeneration, baseline.Lineage.OperationID)
		history, historyErr := readGatewayHistorySnapshotPolicy(fixture.manager.store, true, true)
		fingerprint, fingerprinted := history.files[name]
		if historyErr != nil || !fingerprinted || fingerprint.info == nil {
			t.Fatalf("aware history omitted current V2 fingerprint: error=%v files=%#v", historyErr, history.files)
		}
		if _, err := fixture.manager.selectGatewayCurrentLocked(context.Background(), snapshot); err == nil {
			t.Fatal("legacy selector ignored a current V2 sidecar")
		}
		if _, err := readGatewayHistorySnapshotMode(fixture.manager.store, true); err == nil {
			t.Fatal("legacy history scanner ignored a current V2 sidecar")
		}
		if _, err := readGatewayRebindProtectedPresenceReadOnly(fixture.dataRoot); err == nil {
			t.Fatal("legacy startup presence accepted a current V2 sidecar")
		}

		selected.V2.State.Apps = map[string]gatewayCurrentAppRouteV2{}
		selected.V2.Projection.RetainedApps = map[string]gatewayCurrentAppRouteV2{}
		selected.V2.Source.State.Apps = map[string]gatewayCurrentAppRoute{}
		fresh, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
		if err != nil || fresh.V2 == nil || !reflect.DeepEqual(fresh.V2.State, baseline) ||
			len(fresh.V2.Projection.RetainedApps) != len(baseline.Apps) ||
			fresh.V2.Source.State == nil || len(fresh.V2.Source.State.Apps) != len(fixture.baseline.Apps) {
			t.Fatalf("mutated selection aliased protected source: selection=%#v error=%v", fresh, err)
		}
	})

	t.Run("upgrade_generation_zero_selects_and_foreign_positive_refuses", func(t *testing.T) {
		for _, generation := range []uint64{0, 4} {
			name := "zero"
			if generation > 0 {
				name = "positive"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newGatewayCurrentStateFixture(t)
				snapshot := gatewayCurrentUpgradeSnapshotForV2Test(t, fixture)
				fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
					func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) { return snapshot, nil })
				baseline := gatewayCurrentProjectionV2UpgradeFixture(t, fixture,
					gatewayCurrentRouteStateV2ForTest(t, fixture))
				baseline.Lineage.ProtectedGeneration = generation
				baseline.Digest, _ = gatewayCurrentRouteStateDigestV2(baseline)
				if generation != fixture.history.Predecessor.Generation {
					// The retained history selects its actual generation; a foreign filename
					// must refuse instead of being chosen by numeric order.
					gatewayCurrentInstallV2TestState(t, fixture.dataRoot, baseline)
					if selected, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background()); err == nil ||
						!reflect.DeepEqual(selected, gatewayCurrentProtectedSelection{}) {
						t.Fatalf("foreign generation selected: %#v error=%v", selected, err)
					}
					return
				}
				gatewayCurrentInstallV2TestState(t, fixture.dataRoot, baseline)
				selected, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
				if err != nil || selected.V2 == nil || selected.V2.State.SourceUpgrade == nil ||
					selected.V2.State.SourceV1 != nil {
					t.Fatalf("upgrade V2 selection=%#v error=%v", selected, err)
				}
			})
		}
	})

	t.Run("malformed_duplicate_and_wrong_origin_never_fall_back", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*testing.T, gatewayCurrentStateFixture, gatewayCurrentRouteStateV2)
		}{
			{name: "case_variant", mutate: func(t *testing.T, f gatewayCurrentStateFixture, _ gatewayCurrentRouteStateV2) {
				name, _ := gatewayCurrentRouteStateNameV2(f.baseline.Lineage.ProtectedGeneration, f.baseline.Lineage.OperationID)
				if err := os.WriteFile(filepath.Join(f.manager.store.root, strings.ToUpper(name)), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "partial", mutate: func(t *testing.T, f gatewayCurrentStateFixture, _ gatewayCurrentRouteStateV2) {
				name, _ := gatewayCurrentRouteStateNameV2(f.baseline.Lineage.ProtectedGeneration, f.baseline.Lineage.OperationID)
				if err := os.WriteFile(filepath.Join(f.manager.store.root, name), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "current_v2_namespace_unknown", mutate: func(t *testing.T, f gatewayCurrentStateFixture, _ gatewayCurrentRouteStateV2) {
				if err := os.WriteFile(filepath.Join(f.manager.store.root, "gateway-current-routes.v2-unknown"),
					[]byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "directory", mutate: func(t *testing.T, f gatewayCurrentStateFixture, _ gatewayCurrentRouteStateV2) {
				name, _ := gatewayCurrentRouteStateNameV2(f.baseline.Lineage.ProtectedGeneration, f.baseline.Lineage.OperationID)
				if err := os.Mkdir(filepath.Join(f.manager.store.root, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}},
			{name: "wrong_origin", mutate: func(t *testing.T, f gatewayCurrentStateFixture, value gatewayCurrentRouteStateV2) {
				value.SourceV1.Digest = strings.Repeat("f", 64)
				value.Digest, _ = gatewayCurrentRouteStateDigestV2(value)
				gatewayCurrentInstallV2TestState(t, f.dataRoot, value)
			}},
			{name: "duplicate", mutate: func(t *testing.T, f gatewayCurrentStateFixture, value gatewayCurrentRouteStateV2) {
				gatewayCurrentInstallV2TestState(t, f.dataRoot, value)
				other := cloneGatewayCurrentRouteStateV2(value)
				other.Lineage.OperationID = "33333333-3333-4333-8333-333333333333"
				other.Lineage.ProtectedIdentityDigest = other.Identity.Rebind.Digest
				other.Digest, _ = gatewayCurrentRouteStateDigestV2(other)
				name, purpose := gatewayCurrentRouteStateNameV2(other.Lineage.ProtectedGeneration, other.Lineage.OperationID)
				store := gatewayUpgradeStateStore{directory: f.manager.store}
				if err := store.writeExact(filepath.Join(f.manager.store.root, name), purpose, other, true,
					maxGatewayCurrentRouteStateBytes); err != nil {
					t.Fatal(err)
				}
			}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				fixture := newGatewayCurrentStateFixture(t)
				if err := fixture.store.installBaseline(fixture.baseline); err != nil {
					t.Fatal(err)
				}
				snapshot := gatewayCurrentRebindSnapshotForV2Test(fixture)
				fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
					func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) { return snapshot, nil })
				test.mutate(t, fixture, gatewayCurrentRouteStateV2ForTest(t, fixture))
				selected, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
				if err == nil || !reflect.DeepEqual(selected, gatewayCurrentProtectedSelection{}) {
					t.Fatalf("unsafe V2 fell back: selection=%#v error=%v", selected, err)
				}
			})
		}
	})

	t.Run("storage_valid_projection_conflict_refuses_without_fallback", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		v1, v2, appA, appB := gatewayCurrentProjectionV2TwoAppFixture(t, fixture.baseline,
			gatewayCurrentRouteStateV2ForTest(t, fixture))
		conflict := v2.Apps[appB]
		conflict.Route = cloneGatewayCurrentAppRoute(gatewayCurrentAppRoute{Route: conflict.Route}).Route
		for index := range conflict.Route.Endpoints {
			conflict.Route.Endpoints[index].NetworkName = v2.Apps[appA].Route.Endpoints[0].NetworkName
		}
		v2.Apps[appB] = conflict
		gatewayCurrentProjectionV2RefreshDigest(t, &v2)
		if !validGatewayCurrentRouteStateV2(v2) {
			t.Fatal("projection conflict fixture is not storage valid")
		}
		fixture.baseline = v1
		if err := fixture.store.installBaseline(v1); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayCurrentRebindSnapshotForV2Test(fixture)
		fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
			func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) { return snapshot, nil })
		gatewayCurrentInstallV2TestState(t, fixture.dataRoot, v2)
		selected, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
		if err == nil || !reflect.DeepEqual(selected, gatewayCurrentProtectedSelection{}) {
			t.Fatalf("unprojectable V2 selected: selection=%#v error=%v", selected, err)
		}
	})

	t.Run("cancellation_after_successful_SQL_callback_refuses", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayCurrentRebindSnapshotForV2Test(fixture)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
			func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				calls++
				if calls == 2 {
					cancel()
				}
				return snapshot, nil
			})
		selected, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(ctx)
		if err == nil || calls != 2 || !reflect.DeepEqual(selected, gatewayCurrentProtectedSelection{}) {
			t.Fatalf("cancelled selection=%#v calls=%d error=%v", selected, calls, err)
		}
	})

	t.Run("SQL_callback_protected_mutation_refuses_without_fallback", func(t *testing.T) {
		for _, target := range []string{"source_V1", "current_V2"} {
			t.Run(target, func(t *testing.T) {
				fixture := newGatewayCurrentStateFixture(t)
				if err := fixture.store.installBaseline(fixture.baseline); err != nil {
					t.Fatal(err)
				}
				snapshot := gatewayCurrentRebindSnapshotForV2Test(fixture)
				var mutate func() error
				if target == "source_V1" {
					next := cloneGatewayCurrentRouteState(fixture.baseline)
					next.Revision++
					next.Digest, _ = gatewayCurrentRouteStateDigest(next)
					mutate = func() error { return fixture.store.saveNext(fixture.baseline, next) }
				} else {
					baseline := gatewayCurrentRouteStateV2ForTest(t, fixture)
					store, err := newGatewayCurrentRouteStateV2StoreForOrigin(fixture.dataRoot,
						baseline.Lineage, gatewayCurrentRouteOriginForStateV2(baseline))
					if err != nil || store.installBaseline(baseline) != nil {
						t.Fatalf("install current V2 baseline: %v", err)
					}
					next := cloneGatewayCurrentRouteStateV2(baseline)
					next.Revision++
					next.Digest, _ = gatewayCurrentRouteStateDigestV2(next)
					mutate = func() error { return store.saveNext(baseline, next) }
				}
				calls := 0
				fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(
					func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
						calls++
						if calls == 2 {
							if err := mutate(); err != nil {
								return appaccess.GatewayRebindRecoverySnapshot{}, err
							}
						}
						return snapshot, nil
					})
				selected, _, err := fixture.manager.readGatewayCurrentProtectedSelectionLocked(context.Background())
				if err == nil || calls != 2 || !reflect.DeepEqual(selected, gatewayCurrentProtectedSelection{}) {
					t.Fatalf("protected mutation selected=%#v calls=%d error=%v", selected, calls, err)
				}
			})
		}
	})

	t.Run("legacy_entries_refuse_sidecar_before_mutation", func(t *testing.T) {
		fixture, source, driver := gatewayCurrentServingRestoreSQLFixture(t)
		fixture.manager.options.RebindCurrentStateRepository = fixture.repository
		repository := gatewayCurrentV2SQLitePromotionRepository{
			access: fixture.repository, lifecycle: applifecycle.New(fixture.db),
		}
		authority, err := readGatewayCurrentV2PromotionAuthority(context.Background(), repository, source)
		if err != nil {
			t.Fatal(err)
		}
		baseline, err := gatewayCurrentV2BaselineFor(source, authority)
		if err != nil {
			t.Fatal(err)
		}
		gatewayCurrentInstallV2TestState(t, fixture.manager.options.DataRoot, baseline)
		beforeFiles, err := readGatewayHistorySnapshotPolicy(fixture.manager.store, true, true)
		if err != nil {
			t.Fatal(err)
		}
		beforeSQL, err := fixture.repository.HostingGatewayStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		beforeCommands := len(fixture.runner.commands)
		appID := gatewayCurrentRouteStateV2FirstApp(t, baseline)
		app := baseline.Apps[appID]
		request := generatedroute.RouteSwitchRequest{AppID: appID, ToSlot: app.Route.Slot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), app.Route.Endpoints...)}
		if err := exerciseManagerSwitch(fixture.manager, context.Background(), request); err == nil {
			t.Fatal("ordinary route entry accepted a current V2 sidecar")
		}
		if store, _, _, committed, err := fixture.manager.committedV2Locked(context.Background()); err == nil || store != nil || committed {
			t.Fatalf("committed upgrade fallback accepted sidecar: store=%#v committed=%t error=%v", store, committed, err)
		}
		lan, err := fixture.manager.InspectGatewayV2LANAccessStartup(context.Background(),
			GatewayLANGrantStartupClaims(beforeSQL.Grants), GatewayLANDisableStartupClaims(beforeSQL.Disables))
		if err == nil || !reflect.DeepEqual(lan, GatewayV2LANAccessStartupInspection{}) {
			t.Fatalf("LAN startup accepted sidecar: inspection=%#v error=%v", lan, err)
		}
		proposal, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), fixture.repository,
			GatewayRebindProposalInput{OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
				SuccessorProfileRevisionNumber: baseline.Profile.RevisionNumber + 1,
				SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: appaccess.GatewayProfileSpec{
					SelectedIPv4: "192.168.98.8", InterfaceID: "v2-refusal-successor",
					PortStart: baseline.Profile.PortStart, PortEnd: baseline.Profile.PortEnd,
				}})
		if err == nil || !reflect.DeepEqual(proposal, GatewayRebindProposalInspection{}) {
			t.Fatalf("rebind proposal accepted sidecar: inspection=%#v error=%v", proposal, err)
		}
		handled, err := fixture.manager.RestoreGatewayCurrentServingStartup(context.Background(), fixture.repository)
		if err == nil || handled || driver.calls != 0 || driver.effects != 0 || driver.retirementCalls != 0 {
			t.Fatalf("current restore accepted sidecar: handled=%t calls=%d effects=%d retirement=%d error=%v",
				handled, driver.calls, driver.effects, driver.retirementCalls, err)
		}
		afterFiles, filesErr := readGatewayHistorySnapshotPolicy(fixture.manager.store, true, true)
		afterSQL, sqlErr := fixture.repository.HostingGatewayStartupSnapshot(context.Background())
		store, storeErr := newGatewayCurrentRouteStateV2StoreForOrigin(fixture.manager.options.DataRoot,
			baseline.Lineage, gatewayCurrentRouteOriginForStateV2(baseline))
		installed, loadErr := store.load()
		if filesErr != nil || sqlErr != nil || storeErr != nil || loadErr != nil ||
			!sameGatewayHistorySnapshot(beforeFiles, afterFiles) || !reflect.DeepEqual(beforeSQL, afterSQL) ||
			!reflect.DeepEqual(installed, baseline) || len(fixture.runner.commands) != beforeCommands {
			t.Fatalf("legacy refusal mutated state: files=%v SQL=%v store=%v load=%v commands=%d",
				filesErr, sqlErr, storeErr, loadErr, len(fixture.runner.commands)-beforeCommands)
		}
	})
}
