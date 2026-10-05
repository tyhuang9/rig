package generatedingress

import (
	"context"
	"database/sql"
	"encoding/binary"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

// TestLiveGatewayRebindSuccessorNetworkStage exercises the private first
// successor effect against a disposable default-local Linux Docker daemon.
// The successor deliberately reuses the selected, currently present host
// interface while the private Docker subnet remains generation scoped.
func TestLiveGatewayRebindSuccessorNetworkStage(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "a1111111-1111-4111-8111-111111111111",
		planID:           "a2222222-2222-4222-8222-222222222222",
		operationID:      "a3333333-3333-4333-8333-333333333333",
		profileRevision:  "a4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-network-stage",
		applicationReply: "gateway-v2-rebind-network-stage",
		countRequests:    true,
	})
	db, err := database.Open(fixture.stateRoot)
	if err != nil {
		t.Fatal("open live rebind network SQLite fixture")
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := appaccess.New(db)
	profile := prepareLiveGatewayRebindStagePredecessor(t, fixture, db, repository)

	successor := appaccess.GatewayProfileSpec{
		SelectedIPv4: fixture.request.Profile.SelectedIPv4,
		InterfaceID:  fixture.request.Profile.InterfaceID,
		PortStart:    fixture.port,
		PortEnd:      fixture.port,
	}
	proposal := seedLiveGatewayRebindPublicPassiveLineage(t, fixture, db, repository, profile, successor)
	predecessorState, predecessorJournal, predecessorStore := liveGatewayV2LoadDurableOperation(t, fixture)
	preclaim, err := repository.GatewayRebindPreclaimSnapshot(fixture.ctx, proposal)
	if err != nil || !gatewayRebindPreclaimMatches(preclaim, predecessorState, predecessorJournal) {
		t.Fatal("live successor did not pass complete zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("live prepared claim did not bind the committed predecessor")
	}

	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("production successor preflight rejected the present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("protected-intent observation did not match the stable production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve live successor intent generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact live successor protected intent")
	}

	driver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	image, found, err := driver.inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned live gateway image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct live successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install exact live successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt:            baseTime.Add(time.Nanosecond),
		ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct live successor stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install exact live successor stage intent progress")
	}

	var cleanupID string
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, cleanupID)
	})
	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before successor stage")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before successor stage")
	}
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("counted predecessor requests did not settle before successor stage")
	}

	stageTime := baseTime.Add(2 * time.Nanosecond)
	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, stageTime, nil); err != nil {
		logLiveGatewayRebindStageNetworkFailure(t, fixture, repository, reads, intent,
			beforeRoute, predecessorState, predecessorJournal)
		clearGatewayV2DockerObservation(&beforeDocker)
		failLiveIngress(t, "stage live rebind successor network", err)
	}
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 3 {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor stage did not install complete protected progress")
	}
	bound := history.Progress[2].Record
	ownershipDigest, digestErr := gatewayRebindStageNetworkOwnershipDigest(intent)
	if digestErr != nil || bound.PreviousDigest != second.Digest || bound.Stage == nil || bound.Stage.Network == nil ||
		bound.Stage.Network.OwnershipDigest != ownershipDigest {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor stage installed an invalid network binding")
	}
	stageWithoutNetwork := *bound.Stage
	stageWithoutNetwork.Network = nil
	if !reflect.DeepEqual(stageWithoutNetwork, *second.Stage) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor network binding changed the prepared stage intent")
	}
	cleanupID = bound.Stage.Network.ID
	physical, err := driver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, physical, cleanupID) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("production Docker adapter did not observe the exact bound successor network")
	}
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil || physical.Network.Options[gatewayRebindBridgeNameOptionKey] != bridgeName ||
		len(physical.Network.Containers) != 0 {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor network did not retain its exact private bridge shape")
	}
	createdDelta, err := readGatewayRebindStageNetworkPhysicalObservation(
		fixture.ctx, reads, driver, intent, cleanupID)
	if err != nil || !validLiveGatewayRebindStagePhysicalDelta(intent, createdDelta, cleanupID) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor network did not produce the exact Linux bridge host delta")
	}

	beforeReplay := history
	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, stageTime.Add(time.Nanosecond), nil); err != nil {
		clearGatewayV2DockerObservation(&beforeDocker)
		failLiveIngress(t, "replay live rebind successor network", err)
	}
	afterReplay, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(beforeReplay, afterReplay) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor replay changed protected progress")
	}
	replayed, err := driver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, replayed, cleanupID) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor replay did not preserve the exact bound Docker network")
	}
	replayedDelta, err := readGatewayRebindStageNetworkPhysicalObservation(
		fixture.ctx, reads, driver, intent, cleanupID)
	if err != nil || !validLiveGatewayRebindStagePhysicalDelta(intent, replayedDelta, cleanupID) ||
		!reflect.DeepEqual(createdDelta, replayedDelta) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("live successor replay changed the exact Linux bridge host delta")
	}

	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("successor network stage changed the prepared SQLite claim")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("successor network stage changed protected predecessor state")
	}
	afterRoute, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("successor network stage changed the active application route")
	}
	afterDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) ||
		!reflect.DeepEqual(beforeDocker, afterDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("successor network stage changed the committed predecessor Docker resources")
	}
	clearGatewayV2DockerObservation(&beforeDocker)
	clearGatewayV2DockerObservation(&afterDocker)
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("successor network stage or replay forwarded an application request")
	}
}

// Log only predicates and counts so a failed hosted run identifies which
// post-create proof failed without printing Docker IDs or protected contents.
func logLiveGatewayRebindStageNetworkFailure(t *testing.T, fixture *liveGatewayV2Fixture,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	intent gatewayRebindProtectedIntent, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) {
	t.Helper()
	ctx := fixture.ctx
	history, historyErr := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	progressCount := -1
	if historyErr == nil {
		progressCount = len(history.Progress)
	}
	anchor, anchorErr := fixture.ingress.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	t.Logf("rebind network failure: progress_scan_ok=%t progress_count=%d anchor_ok=%t anchor_progress_count=%d",
		historyErr == nil, progressCount, anchorErr == nil, anchor.progressCount)

	driver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	observed, observeErr := driver.inspect(ctx, intent)
	exactNetwork := observeErr == nil && observed.Found &&
		validGatewayRebindStageNetworkObservation(intent, observed, observed.ID)
	t.Logf("rebind network failure: network_inspect_ok=%t network_found=%t network_exact=%t",
		observeErr == nil, observed.Found, exactNetwork)

	candidates, candidatesErr := reads.network.candidates()
	host, hostErr := reads.network.host()
	ids, idsErr := reads.dockerIDs(ctx)
	prefixes, prefixesErr := reads.network.docker(ctx)
	projected := gatewayRebindCandidateProjection(candidates)
	candidatesExact := candidatesErr == nil && validGatewayRebindStageNetworkHostDelta(intent,
		projected, intent.NetworkObservation.HostRoutes, intent.NetworkObservation.HostInterfaces)
	routes, routeErr := canonicalGatewayRebindPrefixes(host.Routes)
	interfaces, interfaceErr := canonicalGatewayRebindPrefixes(host.Interfaces)
	routesExact := hostErr == nil && routeErr == nil && gatewayRebindPrefixesMatchBaselineOrPlan(
		routes, intent.NetworkObservation.HostRoutes, intent.Intent.Network.Subnet)
	interfacesExact := hostErr == nil && interfaceErr == nil && gatewayRebindPrefixesMatchBaselineOrPlan(
		interfaces, intent.NetworkObservation.HostInterfaces, intent.Intent.Network.Subnet)
	expectedIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedIDs = append(expectedIDs, observed.ID)
	sort.Strings(expectedIDs)
	idsExact := idsErr == nil && validContainerID(observed.ID) &&
		validGatewayRebindSuccessorDockerIDs(ids) &&
		equalStrings(ids, expectedIDs)
	dockerPrefixes, prefixErr := canonicalGatewayRebindPrefixes(prefixes)
	expectedPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedPrefixes = append(expectedPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedPrefixes)
	prefixesExact := prefixesErr == nil && prefixErr == nil && equalStrings(dockerPrefixes, expectedPrefixes)
	physicalReadOK := false
	if validContainerID(observed.ID) {
		_, physicalErr := readGatewayRebindStageNetworkPhysicalObservation(ctx, reads, driver, intent, observed.ID)
		physicalReadOK = physicalErr == nil
	}
	t.Logf("rebind network failure: candidates_read_ok=%t bridge_candidate_exact=%t host_read_ok=%t routes_exact=%t interfaces_exact=%t docker_ids_read_ok=%t docker_ids_exact=%t docker_prefixes_read_ok=%t docker_prefixes_exact=%t physical_read_ok=%t",
		candidatesErr == nil, candidatesExact, hostErr == nil, routesExact, interfacesExact,
		idsErr == nil, idsExact, prefixesErr == nil, prefixesExact, physicalReadOK)
	if hostErr == nil && routeErr == nil {
		plan, planErr := netip.ParsePrefix(intent.Intent.Network.Subnet)
		gateway, gatewayErr := netip.ParseAddr(intent.Intent.Network.GatewayIPv4)
		if planErr == nil && plan.Addr().Is4() && gatewayErr == nil && gateway.Is4() {
			lastOctets := plan.Masked().Addr().As4()
			last := binary.BigEndian.Uint32(lastOctets[:]) |
				uint32((uint64(1)<<uint(32-plan.Bits()))-1)
			binary.BigEndian.PutUint32(lastOctets[:], last)
			broadcast32 := netip.AddrFrom4(lastOctets).String() + "/32"
			baseline := make(map[string]int, len(intent.NetworkObservation.HostRoutes))
			for _, prefix := range intent.NetworkObservation.HostRoutes {
				baseline[prefix]++
			}
			added := make(map[string]int)
			addedPlan, addedGateway, addedBroadcast := 0, 0, 0
			addedInside32, addedInsideOther, addedOutside := 0, 0, 0
			for _, prefix := range routes {
				if baseline[prefix] > 0 {
					baseline[prefix]--
					continue
				}
				added[prefix]++
				switch {
				case prefix == plan.String():
					addedPlan++
				case prefix == gateway.String()+"/32":
					addedGateway++
				case prefix == broadcast32:
					addedBroadcast++
				default:
					parsed, parseErr := netip.ParsePrefix(prefix)
					if parseErr == nil && parsed.Bits() >= plan.Bits() && plan.Contains(parsed.Addr()) {
						if parsed.Bits() == 32 {
							addedInside32++
						} else {
							addedInsideOther++
						}
					} else {
						addedOutside++
					}
				}
			}
			removed, duplicateAdded := 0, 0
			for _, count := range baseline {
				removed += count
			}
			for _, count := range added {
				if count > 1 {
					duplicateAdded += count - 1
				}
			}
			t.Logf("rebind network failure: route_delta baseline=%d observed=%d removed=%d added_plan=%d added_gateway32=%d added_broadcast32=%d added_other_inside32=%d added_other_inside_non32=%d added_outside=%d duplicate_added=%d",
				len(intent.NetworkObservation.HostRoutes), len(routes), removed, addedPlan,
				addedGateway, addedBroadcast, addedInside32, addedInsideOther,
				addedOutside, duplicateAdded)
		}
	}

	first, firstErr := fixture.ingress.inspectGatewayRebindDocker(ctx, source, state, journal)
	second, secondErr := fixture.ingress.inspectGatewayRebindDocker(ctx, source, state, journal)
	firstValid := firstErr == nil && validGatewayRebindPredecessorDocker(source, state, journal, first)
	secondValid := secondErr == nil && validGatewayRebindPredecessorDocker(source, state, journal, second)
	semanticEqual := firstValid && secondValid && sameGatewayRebindPredecessorDockerObservation(
		first, second, state.Identity)
	firstDigest, firstDigestErr := canonicalDigest(first)
	secondDigest, secondDigestErr := canonicalDigest(second)
	rawDigestEqual := firstValid && secondValid && firstDigestErr == nil && secondDigestErr == nil &&
		firstDigest == secondDigest
	clearGatewayV2DockerObservation(&first)
	clearGatewayV2DockerObservation(&second)
	t.Logf("rebind network failure: predecessor_first_valid=%t predecessor_second_valid=%t predecessor_semantic_equal=%t predecessor_raw_digest_equal=%t",
		firstValid, secondValid, semanticEqual, rawDigestEqual)
}

func prepareLiveGatewayRebindStagePredecessor(t *testing.T, fixture *liveGatewayV2Fixture,
	db *sql.DB, repository *appaccess.Repository,
) appaccess.GatewayProfileRevision {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'rebind-network-live-admin','hash','administrator',datetime('now'),datetime('now'))`,
		gatewayRebindTestAdministrator); err != nil {
		t.Fatal("seed live rebind network administrator")
	}
	profileSpec := appaccess.GatewayProfileSpec{
		SelectedIPv4: fixture.request.Profile.SelectedIPv4,
		InterfaceID:  fixture.request.Profile.InterfaceID,
		PortStart:    fixture.port,
		PortEnd:      fixture.port,
	}
	profile, _, err := repository.ConfigureGatewayProfile(fixture.ctx, appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), Spec: profileSpec,
		Approval: appaccess.Approval{
			Action: appaccess.ActionConfigureGateway, SpecDigest: fixture.request.Profile.SpecDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil || profile.SpecDigest != fixture.request.Profile.SpecDigest {
		t.Fatal("seed exact live rebind network predecessor profile")
	}
	fixture.request.Profile.RevisionID = profile.ID
	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
		ProfileSpecDigest: profile.SpecDigest,
	}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal("digest live rebind network predecessor upgrade")
	}
	fixture.request.ApprovedActionDigest = upgradeDigest
	liveGatewayV2AssertSourceAttestation(t, fixture)
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(fixture.ctx, appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: fixture.request.OperationID, Spec: upgradeSpec,
		Approval: appaccess.Approval{
			Action: appaccess.ActionUpgradeGateway, SpecDigest: upgradeDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatal("seed live rebind network predecessor upgrade claim")
	}
	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request,
		liveGatewayV2Authorizer(t, fixture.request))
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		liveGatewayV2LogOperationDiagnostic(t, fixture)
		failLiveIngress(t, "commit live rebind network predecessor", err)
	}
	owner := appaccess.GatewayProfileUpgradeClaimOwner{
		OperationID: upgrade.OperationID, ProfileRevisionID: profile.ID,
		ProfileRevisionNumber: profile.RevisionNumber,
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(fixture.ctx, owner,
		appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeServing); err != nil {
		t.Fatal("advance live rebind network predecessor to serving")
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(fixture.ctx, owner,
		appaccess.GatewayProfileUpgradeServing, appaccess.GatewayProfileUpgradeCommitted); err != nil {
		t.Fatal("commit live rebind network predecessor claim")
	}
	return profile
}

func liveGatewayRebindStageProductionReads(manager *Manager) gatewayRebindSuccessorPreflightReads {
	inventory := managerGatewayV2DockerInventory{manager: manager}
	return gatewayRebindSuccessorPreflightReads{
		network:   gatewayV2ProductionNetworkPlanReads(manager),
		dockerIDs: inventory.listNetworkIDs,
	}
}

func validLiveGatewayRebindStagePhysicalDelta(intent gatewayRebindProtectedIntent,
	observed gatewayRebindStageNetworkAttestation, expectedID string,
) bool {
	if !validGatewayRebindProtectedIntent(intent) || !validContainerID(expectedID) ||
		normalizeID(expectedID) != expectedID || observed.NetworkID != expectedID {
		return false
	}
	ownershipDigest, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil || observed.OwnershipDigest != ownershipDigest {
		return false
	}
	plan, planErr := netip.ParsePrefix(intent.Intent.Network.Subnet)
	gateway, gatewayErr := netip.ParseAddr(intent.Intent.Network.GatewayIPv4)
	if planErr != nil || gatewayErr != nil || !plan.Addr().Is4() || !gateway.Is4() {
		return false
	}
	expectedRoutes := append([]string{}, intent.NetworkObservation.HostRoutes...)
	expectedRoutes = append(expectedRoutes, intent.Intent.Network.Subnet,
		netip.PrefixFrom(gateway, 32).String(),
		netip.PrefixFrom(lastIPv4Address(plan), 32).String())
	sort.Strings(expectedRoutes)
	expectedInterfaces := append([]string{}, intent.NetworkObservation.HostInterfaces...)
	expectedInterfaces = append(expectedInterfaces, intent.Intent.Network.Subnet)
	sort.Strings(expectedInterfaces)
	expectedDockerIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedDockerIDs = append(expectedDockerIDs, expectedID)
	sort.Strings(expectedDockerIDs)
	expectedDockerPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedDockerPrefixes = append(expectedDockerPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedDockerPrefixes)
	if !equalStrings(observed.HostRoutes, expectedRoutes) ||
		!equalStrings(observed.HostInterfaces, expectedInterfaces) ||
		!equalStrings(observed.DockerIDs, expectedDockerIDs) ||
		!equalStrings(observed.DockerPrefixes, expectedDockerPrefixes) ||
		len(observed.Candidates) != len(intent.NetworkObservation.Candidates)+1 {
		return false
	}
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil {
		return false
	}
	withoutBridge := make([]gatewayRebindSuccessorNetworkCandidate, 0, len(observed.Candidates)-1)
	foundBridge := false
	for _, candidate := range observed.Candidates {
		index, name, ok := strings.Cut(candidate.InterfaceID, "/")
		parsedIndex, parseErr := strconv.Atoi(index)
		isBridge := ok && parseErr == nil && parsedIndex > 0 && strconv.Itoa(parsedIndex) == index &&
			name == bridgeName && candidate.IPv4 == intent.Intent.Network.GatewayIPv4 &&
			candidate.Prefix == intent.Intent.Network.Subnet
		if isBridge {
			if foundBridge {
				return false
			}
			foundBridge = true
			continue
		}
		withoutBridge = append(withoutBridge, candidate)
	}
	return foundBridge && reflect.DeepEqual(withoutBridge, intent.NetworkObservation.Candidates)
}

func TestLiveGatewayRebindStagePhysicalDeltaRequiresExactLinuxRoutes(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	intent := fixture.intent
	const networkID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ownershipDigest, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil {
		t.Fatal(err)
	}
	plan := netip.MustParsePrefix(intent.Intent.Network.Subnet)
	gateway := netip.MustParseAddr(intent.Intent.Network.GatewayIPv4)
	routes := append([]string{}, intent.NetworkObservation.HostRoutes...)
	routes = append(routes, plan.String(), netip.PrefixFrom(gateway, 32).String(),
		netip.PrefixFrom(lastIPv4Address(plan), 32).String())
	sort.Strings(routes)
	interfaces := append([]string{}, intent.NetworkObservation.HostInterfaces...)
	interfaces = append(interfaces, plan.String())
	sort.Strings(interfaces)
	dockerIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	dockerIDs = append(dockerIDs, networkID)
	sort.Strings(dockerIDs)
	dockerPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	dockerPrefixes = append(dockerPrefixes, plan.String())
	sort.Strings(dockerPrefixes)
	candidates := append([]gatewayRebindSuccessorNetworkCandidate{}, intent.NetworkObservation.Candidates...)
	candidates = append(candidates, gatewayRebindSuccessorNetworkCandidate{
		InterfaceID: "42/" + bridgeName, IPv4: gateway.String(), Prefix: plan.String(),
	})
	observed := gatewayRebindStageNetworkAttestation{
		Candidates: candidates, HostRoutes: routes, HostInterfaces: interfaces,
		DockerIDs: dockerIDs, DockerPrefixes: dockerPrefixes,
		NetworkID: networkID, OwnershipDigest: ownershipDigest,
	}
	if !validLiveGatewayRebindStagePhysicalDelta(intent, observed, networkID) {
		t.Fatal("exact hosted Linux bridge route delta was rejected")
	}
	observed.HostRoutes = make([]string, 0, len(routes)-1)
	for _, route := range routes {
		if route != netip.PrefixFrom(gateway, 32).String() {
			observed.HostRoutes = append(observed.HostRoutes, route)
		}
	}
	if validLiveGatewayRebindStagePhysicalDelta(intent, observed, networkID) {
		t.Fatal("live physical gate accepted an incomplete route delta")
	}
	observed.HostRoutes = append([]string{}, routes...)
	observed.HostRoutes = append(observed.HostRoutes, "10.241.0.0/16")
	sort.Strings(observed.HostRoutes)
	if validLiveGatewayRebindStagePhysicalDelta(intent, observed, networkID) {
		t.Fatal("live physical gate accepted an unrelated route")
	}
}

func cleanupLiveGatewayRebindStageNetwork(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, expectedID string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	driver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil {
		t.Error("inspect live successor network for exact cleanup")
		return
	}
	if !observed.Found {
		if !validGatewayRebindStageNetworkAbsentObservation(intent, observed) {
			t.Error("live successor cleanup found uncertain owned residue")
		}
		return
	}
	if !validContainerID(expectedID) || normalizeID(expectedID) != expectedID {
		t.Error("live successor cleanup has no protected bound identity; retaining network")
		return
	}
	if !validGatewayRebindStageNetworkObservation(intent, observed, expectedID) {
		t.Error("live successor cleanup ownership is uncertain; retaining network")
		return
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "network", "rm", observed.ID)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor network")
		return
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkAbsentObservation(intent, residue) {
		t.Error("exact live successor network cleanup left owned residue")
	}
}
