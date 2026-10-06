package appaccess

import (
	"math"
	"strings"
	"testing"
	"time"
)

const (
	rebindTypeTestOperation  = "11111111-1111-4111-8111-111111111111"
	rebindTypeTestProfileOne = "22222222-2222-4222-8222-222222222222"
	rebindTypeTestProfileTwo = "33333333-3333-4333-8333-333333333333"
	rebindTypeTestConfigure  = "44444444-4444-4444-8444-444444444444"
	rebindTypeTestApp        = "55555555-5555-4555-8555-555555555555"
	rebindTypeTestAllocation = "66666666-6666-4666-8666-666666666666"
	rebindTypeTestOwner      = "77777777-7777-4777-8777-777777777777"
	rebindTypeTestRevision   = "88888888-8888-4888-8888-888888888888"
	rebindTypeTestGrant      = "99999999-9999-4999-8999-999999999999"
	rebindTypeTestDeployment = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	rebindTypeTestRelease    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	rebindTypeTestUpgrade    = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func TestGatewayRebindV1CanonicalDigestsRemainStable(t *testing.T) {
	entry := gatewayRebindV1GoldenEntry()
	entryDigest, err := GatewayRebindRosterEntryDigest(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.EntryDigest = entryDigest
	rosterDigest, err := GatewayRebindRosterDigest([]GatewayRebindRosterEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	spec := GatewayRebindSpec{
		OperationID:                        rebindTypeTestOperation,
		PredecessorProfileRevisionID:       rebindTypeTestProfileOne,
		PredecessorProfileRevisionNumber:   1,
		PredecessorProfileSpecDigest:       strings.Repeat("1", 64),
		PredecessorUpgradeOperationID:      rebindTypeTestUpgrade,
		PredecessorProtectedIdentityDigest: strings.Repeat("2", 64),
		SuccessorProfileRevisionID:         rebindTypeTestProfileTwo,
		SuccessorProfileRevisionNumber:     2,
		SuccessorProfileOperationID:        rebindTypeTestConfigure,
		SuccessorProfile: GatewayProfileSpec{
			SelectedIPv4: "192.168.50.8", InterfaceID: "adapter-v2", PortStart: 8100, PortEnd: 8119,
		},
		RosterDigest: rosterDigest,
		RosterCount:  1,
	}
	specDigest, err := GatewayRebindSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	const wantEntry = "18db84ab0b00512ae5d1c83dea016290e8b8c09cd3d164a6e962883a0def5c98"
	const wantRoster = "97ad7bd29453332db65a082e0ebf0537f7c53ba4f9d5adda57067b513618a4e4"
	const wantSpec = "8b7e52e4365d7e6fa35aa0f2eea067ed832b4ba69ffb84e8305f20615bb6dd1e"
	if entryDigest != wantEntry || rosterDigest != wantRoster || specDigest != wantSpec {
		t.Fatalf("v1 canonical digests changed: entry=%s roster=%s spec=%s", entryDigest, rosterDigest, specDigest)
	}
}

func TestGatewayRebindV2CanonicalTypesBindTypedSourceAndTransferChain(t *testing.T) {
	entry := gatewayRebindV2GoldenEntry(nil)
	entryDigest, err := GatewayRebindRosterEntryV2Digest(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.EntryDigest = entryDigest
	rosterDigest, err := GatewayRebindRosterV2Digest([]GatewayRebindRosterEntryV2{entry})
	if err != nil {
		t.Fatal(err)
	}
	spec := gatewayRebindV2GoldenSpec(rosterDigest)
	specDigest, err := GatewayRebindSpecV2Digest(spec)
	if err != nil {
		t.Fatal(err)
	}
	transfer := gatewayRebindV2GoldenTransfer(nil, entryDigest)
	transferDigest, err := GatewayRebindAllocationTransferDigest(transfer)
	if err != nil {
		t.Fatal(err)
	}
	transfer.TransferDigest = transferDigest
	manifestDigest, err := GatewayRebindTransferManifestDigest([]GatewayRebindAllocationTransfer{transfer})
	if err != nil {
		t.Fatal(err)
	}
	const wantEntry = "8f2301c26fca2c6cb0f0ec47a73f6e574ee1fb0dc9d28350b5134826a4c66455"
	const wantRoster = "7aa081010a2340db2069c16b6614b567354ea312781f736d068e5f7a8b99a56b"
	const wantSpec = "21a56b79e6f4215efec0403bb9b2da002382c42037043ca1a9a4d6298a5088f7"
	const wantTransfer = "37e44731dc1ab634116a834e43f744b28e1570a0a2161f0f69a522390b742e13"
	const wantManifest = "b5621d06785d826a517ea7367cfbbfad9f91cb98ab9bc33be119ef206025b0eb"
	if entryDigest != wantEntry || rosterDigest != wantRoster || specDigest != wantSpec ||
		transferDigest != wantTransfer || manifestDigest != wantManifest {
		t.Fatalf("v2 canonical digest mismatch: entry=%s roster=%s spec=%s transfer=%s manifest=%s",
			entryDigest, rosterDigest, specDigest, transferDigest, manifestDigest)
	}
}

func TestGatewayRebindRuntimeHeadsV2DigestBindsCompleteCanonicalCensus(t *testing.T) {
	heads := gatewayRebindV2GoldenRuntimeHeads()
	digest, err := GatewayRebindRuntimeHeadsV2Digest(rebindTypeTestOperation, heads)
	if err != nil {
		t.Fatal(err)
	}
	const want = "1da2d4b9584b0905b8f8abfd139501c42f73ec074ad4b7ec8cbfd61c9a436e18"
	if digest != want {
		t.Fatalf("runtime-head digest changed: got %s want %s", digest, want)
	}

	sameInstant := append([]GatewayRebindRuntimeHead(nil), heads...)
	sameInstant[0].UpdatedAt = sameInstant[0].UpdatedAt.In(time.FixedZone("test-offset", -6*60*60))
	if got, err := GatewayRebindRuntimeHeadsV2Digest(rebindTypeTestOperation, sameInstant); err != nil || got != digest {
		t.Fatalf("equivalent timestamp instant was not canonical: digest=%s error=%v", got, err)
	}
	changed := append([]GatewayRebindRuntimeHead(nil), heads...)
	changed[1].UpdatedAt = changed[1].UpdatedAt.Add(time.Nanosecond)
	if got, err := GatewayRebindRuntimeHeadsV2Digest(rebindTypeTestOperation, changed); err != nil || got == digest {
		t.Fatalf("runtime-head update time was not bound: digest=%s error=%v", got, err)
	}

	for name, mutate := range map[string]func([]GatewayRebindRuntimeHead){
		"unsorted":      func(values []GatewayRebindRuntimeHead) { values[0], values[1] = values[1], values[0] },
		"duplicate app": func(values []GatewayRebindRuntimeHead) { values[1].AppID = values[0].AppID },
		"noncanonical deployment": func(values []GatewayRebindRuntimeHead) {
			values[0].DeploymentID = strings.ToUpper(values[0].DeploymentID)
		},
		"inactive generation": func(values []GatewayRebindRuntimeHead) { values[0].Generation = 0 },
		"missing timestamp":   func(values []GatewayRebindRuntimeHead) { values[0].UpdatedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := append([]GatewayRebindRuntimeHead(nil), heads...)
			mutate(invalid)
			if _, err := GatewayRebindRuntimeHeadsV2Digest(rebindTypeTestOperation, invalid); err == nil {
				t.Fatal("invalid runtime-head census was accepted")
			}
		})
	}
	if _, err := GatewayRebindRuntimeHeadsV2Digest("not-an-operation", heads); err == nil {
		t.Fatal("invalid runtime-head operation was accepted")
	}
}

func TestGatewayRebindV2CanonicalTypesRejectCrossVersionAndBrokenChains(t *testing.T) {
	entry := gatewayRebindV2GoldenEntry(nil)
	entry.EntryDigest, _ = GatewayRebindRosterEntryV2Digest(entry)
	rosterDigest, _ := GatewayRebindRosterV2Digest([]GatewayRebindRosterEntryV2{entry})
	spec := gatewayRebindV2GoldenSpec(rosterDigest)

	wrongSpecVersion := spec
	wrongSpecVersion.Version = 1
	if _, err := GatewayRebindSpecV2Digest(wrongSpecVersion); err == nil {
		t.Fatal("v1 spec version was accepted as v2")
	}
	wrongRuntimeVersion := spec
	wrongRuntimeVersion.RuntimeHeadsVersion = 0
	if _, err := GatewayRebindSpecV2Digest(wrongRuntimeVersion); err == nil {
		t.Fatal("absent runtime-head format was accepted as v2")
	}
	wrongRuntimeDigest := spec
	wrongRuntimeDigest.RuntimeHeadsDigest = ""
	if _, err := GatewayRebindSpecV2Digest(wrongRuntimeDigest); err == nil {
		t.Fatal("missing runtime-head digest was accepted as v2")
	}
	wrongRosterVersion := entry
	wrongRosterVersion.Version = 1
	if _, err := GatewayRebindRosterEntryV2Digest(wrongRosterVersion); err == nil {
		t.Fatal("v1 roster version was accepted as v2")
	}
	wrongSourceVariant := spec
	wrongSourceVariant.Predecessor.Lineage.ProtectedIntentDigest = strings.Repeat("9", 64)
	if _, err := GatewayRebindSpecV2Digest(wrongSourceVariant); err == nil {
		t.Fatal("upgrade source carrying a rebind intent was accepted")
	}
	wrongUpgradeRevision := spec
	wrongUpgradeRevision.Predecessor.SourceStateRevision = 1
	if _, err := GatewayRebindSpecV2Digest(wrongUpgradeRevision); err == nil {
		t.Fatal("legacy upgrade source with a mutable revision was accepted")
	}
	wrongGeneration := spec
	wrongGeneration.Predecessor.Lineage.ProtectedGeneration = math.MaxUint64
	if _, err := GatewayRebindSpecV2Digest(wrongGeneration); err == nil {
		t.Fatal("source without a reservable successor generation was accepted")
	}
	storageOverflow := spec
	storageOverflow.Predecessor.Lineage.ProtectedGeneration = uint64(math.MaxInt64) + 1
	if _, err := GatewayRebindSpecV2Digest(storageOverflow); err == nil {
		t.Fatal("source generation outside signed SQL storage was accepted")
	}
	noSuccessorGeneration := spec
	noSuccessorGeneration.Predecessor.Lineage.ProtectedGeneration = math.MaxInt64
	if _, err := GatewayRebindSpecV2Digest(noSuccessorGeneration); err == nil {
		t.Fatal("source without a representable successor SQL generation was accepted")
	}
	rebindSource := spec
	rebindSource.Predecessor.Lineage.Kind = GatewayRebindSourceGatewayRebind
	rebindSource.Predecessor.Lineage.ProtectedGeneration = 1
	rebindSource.Predecessor.Lineage.ProtectedJournalDigest = ""
	rebindSource.Predecessor.Lineage.ProtectedIntentDigest = strings.Repeat("8", 64)
	rebindSource.Predecessor.Lineage.TerminalReceiptDigest = strings.Repeat("9", 64)
	rebindSource.Predecessor.SourceStateVersion = 1
	rebindSource.SuccessorProtectedGeneration = 2
	if _, err := GatewayRebindSpecV2Digest(rebindSource); err == nil {
		t.Fatal("rebind source revision zero was accepted")
	}
	rebindSource.Predecessor.SourceStateRevision = 1
	if _, err := GatewayRebindSpecV2Digest(rebindSource); err != nil {
		t.Fatalf("valid rebind source was rejected: %v", err)
	}
	reusedGeneration := rebindSource
	reusedGeneration.SuccessorProtectedGeneration = reusedGeneration.Predecessor.Lineage.ProtectedGeneration
	if _, err := GatewayRebindSpecV2Digest(reusedGeneration); err == nil {
		t.Fatal("successor protected generation reuse was accepted")
	}

	first := gatewayRebindV2GoldenTransfer(nil, entry.EntryDigest)
	first.TransferDigest, _ = GatewayRebindAllocationTransferDigest(first)
	second := first
	second.Ordinal = 3
	second.AppID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	second.AllocationID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	second.GrantAttemptID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	second.PredecessorTransferDigest = digestPointer(first.TransferDigest)
	second.TransferDigest, _ = GatewayRebindAllocationTransferDigest(second)
	if _, err := GatewayRebindTransferManifestDigest([]GatewayRebindAllocationTransfer{first, second}); err == nil {
		t.Fatal("gapped transfer manifest was accepted")
	}
	second.Ordinal = 2
	second.AppID = first.AppID
	second.TransferDigest, _ = GatewayRebindAllocationTransferDigest(second)
	if _, err := GatewayRebindTransferManifestDigest([]GatewayRebindAllocationTransfer{first, second}); err == nil {
		t.Fatal("duplicate transfer app was accepted")
	}
}

func gatewayRebindV1GoldenEntry() GatewayRebindRosterEntry {
	return GatewayRebindRosterEntry{
		OperationID: rebindTypeTestOperation, Ordinal: 1, AppID: rebindTypeTestApp,
		AllocationID: rebindTypeTestAllocation, Port: 8105,
		AllocationOwnerOperationID: rebindTypeTestOwner, AllocationState: AllocationActive,
		AccessRevisionID: rebindTypeTestRevision, AccessRevisionNumber: 1,
		AccessSpecDigest: strings.Repeat("3", 64), GrantAttemptID: rebindTypeTestGrant,
		GrantStateSequence: 4, GrantProtectedStateDigest: strings.Repeat("4", 64),
		ServingDeploymentID: rebindTypeTestDeployment, ServingReleaseID: rebindTypeTestRelease,
		ServingSlot: "blue", RouteGeneration: 7,
	}
}

func gatewayRebindV2GoldenEntry(predecessor *string) GatewayRebindRosterEntryV2 {
	v1 := gatewayRebindV1GoldenEntry()
	return GatewayRebindRosterEntryV2{
		Version:     GatewayRebindRosterVersionV2,
		OperationID: v1.OperationID, Ordinal: v1.Ordinal, AppID: v1.AppID,
		AllocationID: v1.AllocationID, Port: v1.Port,
		AllocationOwnerOperationID: v1.AllocationOwnerOperationID, AllocationState: v1.AllocationState,
		AccessRevisionID: v1.AccessRevisionID, AccessRevisionNumber: v1.AccessRevisionNumber,
		AccessSpecDigest: v1.AccessSpecDigest, GrantAttemptID: v1.GrantAttemptID,
		GrantStateSequence: v1.GrantStateSequence, GrantProtectedStateDigest: v1.GrantProtectedStateDigest,
		SourceProfileRevisionID: rebindTypeTestProfileOne, SourceProfileRevisionNumber: 1,
		SourceProfileSpecDigest: strings.Repeat("1", 64), PredecessorTransferDigest: predecessor,
		ServingDeploymentID: v1.ServingDeploymentID, ServingReleaseID: v1.ServingReleaseID,
		ServingSlot: v1.ServingSlot, RouteGeneration: v1.RouteGeneration,
	}
}

func gatewayRebindV2GoldenSpec(rosterDigest string) GatewayRebindSpecV2 {
	runtimeHeads := gatewayRebindV2GoldenRuntimeHeads()
	runtimeHeadsDigest, _ := GatewayRebindRuntimeHeadsV2Digest(rebindTypeTestOperation, runtimeHeads)
	return GatewayRebindSpecV2{
		Version: GatewayRebindSpecVersionV2, OperationID: rebindTypeTestOperation,
		Predecessor: GatewayRebindSourceRef{
			Lineage: GatewayCurrentLineageRef{
				Kind: GatewayRebindSourceGatewayUpgrade, OperationID: rebindTypeTestUpgrade,
				ProfileRevisionID: rebindTypeTestProfileOne, ProfileRevisionNumber: 1,
				ProfileSpecDigest: strings.Repeat("1", 64), ProtectedGeneration: 0,
				ProtectedIdentityDigest: strings.Repeat("2", 64), ProtectedJournalDigest: strings.Repeat("5", 64),
			},
			SourceStateVersion: 2, SourceStateRevision: 0, SourceStateDigest: strings.Repeat("6", 64),
			PredecessorCheckpointDigest: strings.Repeat("7", 64),
		},
		SuccessorProtectedGeneration: 1,
		SuccessorProfileRevisionID:   rebindTypeTestProfileTwo, SuccessorProfileRevisionNumber: 2,
		SuccessorProfileOperationID: rebindTypeTestConfigure,
		SuccessorProfile: GatewayProfileSpec{
			SelectedIPv4: "192.168.50.8", InterfaceID: "adapter-v2", PortStart: 8100, PortEnd: 8119,
		},
		RosterVersion: GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: 1,
		RuntimeHeadsVersion: GatewayRebindRuntimeHeadsVersionV1,
		RuntimeHeadsDigest:  runtimeHeadsDigest, RuntimeHeadsCount: int64(len(runtimeHeads)),
	}
}

func gatewayRebindV2GoldenRuntimeHeads() []GatewayRebindRuntimeHead {
	return []GatewayRebindRuntimeHead{
		{AppID: rebindTypeTestApp, DeploymentID: rebindTypeTestDeployment,
			ReleaseID: rebindTypeTestRelease, Slot: "blue", Generation: 7,
			UpdatedAt: time.Date(2026, time.October, 5, 12, 34, 56, 123456789, time.UTC)},
		{AppID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
			DeploymentID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
			ReleaseID:    "ffffffff-ffff-4fff-8fff-ffffffffffff", Slot: "green", Generation: 8,
			UpdatedAt: time.Date(2026, time.October, 5, 12, 35, 57, 987654321, time.UTC)},
	}
}

func gatewayRebindV2GoldenTransfer(predecessor *string, entryDigest string) GatewayRebindAllocationTransfer {
	return GatewayRebindAllocationTransfer{
		Version: GatewayRebindTransferVersionV1, OperationID: rebindTypeTestOperation,
		Ordinal: 1, AppID: rebindTypeTestApp, AllocationID: rebindTypeTestAllocation,
		GrantAttemptID: rebindTypeTestGrant, SourceBindingDigest: strings.Repeat("8", 64),
		RosterEntryDigest:       entryDigest,
		SourceProfileRevisionID: rebindTypeTestProfileOne, SourceProfileRevisionNumber: 1,
		SourceProfileSpecDigest: strings.Repeat("1", 64), PredecessorTransferDigest: predecessor,
		SuccessorProfileRevisionID: rebindTypeTestProfileTwo, SuccessorProfileRevisionNumber: 2,
		SuccessorProfileSpecDigest: strings.Repeat("9", 64), TerminalReceiptDigest: strings.Repeat("a", 64),
	}
}

func digestPointer(value string) *string { return &value }
