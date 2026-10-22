package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type generatedDomainDecodeSentinel struct {
	called *bool
}

func (s *generatedDomainDecodeSentinel) UnmarshalJSON([]byte) error {
	*s.called = true
	return nil
}

func TestGeneratedDomainProtectedStartupCoversEveryArtifactFamily(t *testing.T) {
	t.Run("absent_and_lock_only_are_read_only", func(t *testing.T) {
		dataRoot := t.TempDir()
		missingPath := filepath.Join(dataRoot, "runtime", "generated-ingress")
		got, err := readGeneratedDomainProtectedStartup(context.Background(), dataRoot)
		if err != nil || got.directoryPresent || !got.effectEmpty {
			t.Fatalf("absent namespace = %#v, %v", got, err)
		}
		if _, statErr := os.Lstat(missingPath); !os.IsNotExist(statErr) {
			t.Fatalf("read-only absence created %s: %v", missingPath, statErr)
		}
		if err := os.MkdirAll(missingPath, 0o700); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(missingPath, gatewayLockFilename)
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		got, err = readGeneratedDomainProtectedStartup(context.Background(), dataRoot)
		if err != nil || !got.directoryPresent || !got.effectEmpty || len(got.artifacts) != 1 ||
			got.artifacts[0].name != gatewayLockFilename {
			t.Fatalf("lock-only namespace = %#v, %v", got, err)
		}
		after, err := os.Lstat(lockPath)
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
			t.Fatalf("lock-only read changed the artifact: %v", err)
		}
	})

	t.Run("closed_family_name_inventory", func(t *testing.T) {
		operationID := "11111111-1111-4111-8111-111111111111"
		stateName, journalName, retirementName, abortName, _, _, _, _ := gatewayUpgradeGenerationNames(7, operationID)
		intentV1, _ := gatewayRebindProtectedIntentName(7, operationID)
		intentV2, _ := gatewayRebindProtectedIntentV2Name(7, operationID)
		progress, _ := gatewayRebindProgressName(7, operationID, 1)
		terminalV1, _ := gatewayRebindFinalHandoverTerminalName(7, operationID)
		terminalV2, _ := gatewayRebindTerminalNameV2(7, operationID)
		checkpoint, _ := gatewayRebindPredecessorCheckpointName(7, operationID)
		currentV1, _ := gatewayCurrentRouteStateName(7, operationID)
		currentV2, _ := gatewayCurrentRouteStateNameV2(7, operationID)

		for _, name := range []string{
			v2RouteStateFilename, gatewayMigrationFilename, gatewayRollbackRetirementFilename, gatewayPreJournalAbortFilename,
			stateName, journalName, retirementName, abortName,
			intentV1, intentV2, progress, terminalV1, terminalV2, checkpoint,
		} {
			_, _, _, _, relevant, err := parseGatewayHistoryArtifactName(name)
			if err != nil || !relevant {
				t.Fatalf("protected artifact family %q is not in the closed parser: %v", name, err)
			}
		}
		if _, _, relevant, err := parseGatewayCurrentRoutePresenceName(currentV1); err != nil || !relevant {
			t.Fatalf("current V1 family is not in the closed parser: %v", err)
		}
		if !gatewayCurrentRouteV2NamespaceName(currentV2) {
			t.Fatal("current V2 family is not in the closed parser")
		}
		if _, _, _, _, relevant, err := parseGatewayHistoryArtifactName("Gateway-rebind-progress.v1.g0000000007." + operationID + ".s0000000001.bundle"); err == nil {
			t.Fatalf("case alias was accepted: relevant=%v error=%v", relevant, err)
		}
		if stateFilename != "routes.bundle" || gatewayLockFilename != "gateway.lock" {
			t.Fatal("native V1 or lock family name changed")
		}
	})

	t.Run("strict_v1_upgrade_rebind_and_current_decode", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		got, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		if err != nil || !got.upgrade.committed || !got.hasRebind || got.effectEmpty ||
			len(got.rebind.Intents) != 1 || len(got.rebind.Progress) == 0 || len(got.rebind.Terminals) != 1 ||
			len(got.currentV1) != 1 || len(got.terminals) != 1 || len(got.rebindArtifacts) == 0 {
			t.Fatalf("strict V1 protected decode = %#v, %v", got, err)
		}
		name, _ := gatewayCurrentRouteStateName(fixture.receipt.Generation, fixture.receipt.OperationID)
		if current, exists := got.currentV1[name]; !exists || current.Digest != fixture.baseline.Digest {
			t.Fatalf("decoded current V1 = %#v exists=%t", current, exists)
		}
	})

	t.Run("strict_v2_current_decode", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		value := gatewayCurrentRouteStateV2ForTest(t, fixture)
		gatewayCurrentInstallV2TestState(t, fixture.dataRoot, value)
		got, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		if err != nil || !got.upgrade.committed || !got.hasRebind || got.effectEmpty || len(got.currentV1) != 0 ||
			len(got.currentV2.files) != 1 || len(got.rebind.Intents) != 1 || len(got.rebind.Terminals) != 1 {
			t.Fatalf("strict V2 current decode = %#v, %v", got, err)
		}
		name, _ := gatewayCurrentRouteStateNameV2(value.Lineage.ProtectedGeneration, value.Lineage.OperationID)
		if current, exists := got.currentV2.files[name]; !exists || current.State.Digest != value.Digest {
			t.Fatalf("decoded current V2 = %#v exists=%t", current, exists)
		}
	})

	t.Run("strict_v2_current_decode_preserves_bounded_256k_read", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		value := gatewayCurrentRouteStateV2ForTest(t, fixture)
		var template gatewayCurrentAppRouteV2
		for _, app := range value.Apps {
			template = cloneGatewayCurrentAppRouteV2(app)
			break
		}
		value.Apps = make(map[string]gatewayCurrentAppRouteV2, maxStateApps)
		for index := 1; index <= maxStateApps; index++ {
			app := cloneGatewayCurrentAppRouteV2(template)
			server := app.Route.Endpoints[0]
			server.Component = strings.Repeat("s", 64)
			server.Role = "server"
			server.NetworkName = strings.Repeat("n", 96)
			server.NetworkAlias = strings.Repeat("a", 96)
			static := server
			static.Component = strings.Repeat("t", 64)
			static.Role = "static"
			app.Route.Endpoints = append(app.Route.Endpoints[:0], server, static)
			app.Runtime.DeploymentID = fmt.Sprintf("10000000-0000-4000-8000-%012d", index)
			app.Runtime.ReleaseID = fmt.Sprintf("20000000-0000-4000-8000-%012d", index)
			app.Runtime.ActiveGeneration = int64(index)
			app.LAN = nil
			appID := fmt.Sprintf("30000000-0000-4000-8000-%012d", index)
			value.Apps[appID] = app
		}
		var err error
		value.Digest, err = gatewayCurrentRouteStateDigestV2(value)
		body, marshalErr := json.Marshal(value)
		if err != nil || marshalErr != nil || !validGatewayCurrentRouteStateV2(value) ||
			len(body) <= 64<<10 || len(body) > maxGatewayCurrentRouteStateBytes {
			t.Fatalf("large current V2 fixture bytes=%d valid=%t digest=%v marshal=%v",
				len(body), validGatewayCurrentRouteStateV2(value), err, marshalErr)
		}
		gatewayCurrentInstallV2TestState(t, fixture.dataRoot, value)
		got, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		if err != nil || len(got.currentV2.files) != 1 {
			t.Fatalf("large current V2 protected decode = %d, %v", len(got.currentV2.files), err)
		}
	})

	t.Run("unknown_and_nested_entries_refuse", func(t *testing.T) {
		for _, nested := range []bool{false, true} {
			dataRoot := t.TempDir()
			root := filepath.Join(dataRoot, "runtime", "generated-ingress")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if nested {
				if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(root, "unknown.bundle"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := readGeneratedDomainProtectedStartup(context.Background(), dataRoot); err == nil || got.directoryPresent {
				t.Fatalf("unknown protected entry accepted: %#v, %v", got, err)
			}
		}
	})

	t.Run("entry_bound_applies_before_namespace_decode", func(t *testing.T) {
		dataRoot := t.TempDir()
		root := filepath.Join(dataRoot, "runtime", "generated-ingress")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < generatedDomainStartupMaximumEntries; index++ {
			name := filepath.Join(root, "unknown-"+formatGeneratedDomainProtectedIndex(index)+".bundle")
			file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := readGeneratedDomainProtectedDirectoryBounded(root)
		if err != nil || len(entries) != generatedDomainStartupMaximumEntries {
			t.Fatalf("exact entry bound = %d, %v", len(entries), err)
		}
		store := &stateStore{root: root, path: filepath.Join(root, stateFilename), readOnly: true}
		if _, err := scanGatewayCurrentV2CensusBounded(store); err != nil {
			t.Fatalf("bounded current census at exact entry limit: %v", err)
		}
		name := filepath.Join(root, "unknown-"+
			formatGeneratedDomainProtectedIndex(generatedDomainStartupMaximumEntries)+".bundle")
		file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if entries, err := readGeneratedDomainProtectedDirectoryBounded(root); err == nil || entries != nil {
			t.Fatalf("oversized namespace was materialized: entries=%d error=%v", len(entries), err)
		}
		if _, err := readGatewayHistorySnapshotPolicyBounded(store, true, true); err == nil {
			t.Fatal("bounded history accepted an oversized namespace")
		}
		if _, err := scanGatewayCurrentV2CensusBounded(store); err == nil {
			t.Fatal("bounded current census accepted an oversized namespace")
		}
	})

	t.Run("aggregate_byte_bound_precedes_canonical_decode", func(t *testing.T) {
		dataRoot := t.TempDir()
		root := filepath.Join(dataRoot, "runtime", "generated-ingress")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		payload := []byte(strings.Repeat("x", maxGatewayCurrentRouteStateBytes))
		writeCurrent := func(index int) {
			t.Helper()
			operationID := fmt.Sprintf("00000000-0000-4000-8000-%012d", index)
			name, _ := gatewayCurrentRouteStateNameV2(uint64(index), operationID)
			if err := os.WriteFile(filepath.Join(root, name), payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for index := 1; index <= generatedDomainStartupMaximumBytes/maxGatewayCurrentRouteStateBytes; index++ {
			writeCurrent(index)
		}
		entries, err := readGeneratedDomainProtectedDirectoryBounded(root)
		if err != nil {
			t.Fatal(err)
		}
		store := &stateStore{root: root, path: filepath.Join(root, stateFilename), readOnly: true}
		if scanned, err := fingerprintGeneratedDomainProtectedEntries(store, entries); err != nil || len(scanned) != len(entries) {
			t.Fatalf("exact aggregate byte bound = %d/%d, %v", len(scanned), len(entries), err)
		}
		writeCurrent(len(entries) + 1)
		if _, err := readGatewayHistorySnapshotPolicyBounded(store, true, true); err == nil ||
			!strings.Contains(err.Error(), "aggregate byte bound") {
			t.Fatalf("fresh bounded history did not reject aggregate growth: %v", err)
		}
		if _, err := scanGatewayCurrentV2CensusBounded(store); err == nil ||
			!strings.Contains(err.Error(), "aggregate byte bound") {
			t.Fatalf("fresh bounded current census did not reject aggregate growth: %v", err)
		}
		if got, err := readGeneratedDomainProtectedStartup(context.Background(), dataRoot); err == nil ||
			got.directoryPresent || !strings.Contains(err.Error(), "exceeds its bound") {
			t.Fatalf("outer protected reader accepted aggregate growth: %#v, %v", got, err)
		}
	})

	t.Run("decode_time_growth_and_actual_budget_refuse_before_json_decode", func(t *testing.T) {
		dataRoot := t.TempDir()
		root := filepath.Join(dataRoot, "runtime", "generated-ingress")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		store := &stateStore{root: root, path: filepath.Join(root, stateFilename), readOnly: true}
		operationID := "11111111-1111-4111-8111-111111111111"
		name, purpose := gatewayCurrentRouteStateNameV2(1, operationID)
		path := filepath.Join(root, name)
		initial := []byte(`{"value":"small"}`)
		if err := upgradeProtectedWriteNew(path, purpose, initial); err != nil {
			t.Fatal(err)
		}
		beforeDirectory, err := store.directoryIdentity()
		if err != nil {
			t.Fatal(err)
		}
		captured, err := fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
		if err != nil {
			t.Fatal(err)
		}
		grown := []byte(`{"value":"` + strings.Repeat("x", 32<<10) + `"}`)
		if err := upgradeProtectedWrite(path, purpose, grown); err != nil {
			t.Fatal(err)
		}
		reader, err := newGeneratedDomainProtectedReader(store, beforeDirectory,
			map[string]gatewayHistoryFileFingerprint{path: captured})
		if err != nil {
			t.Fatal(err)
		}
		decoded := false
		if _, err := reader.readStrict(path, purpose, maxGatewayCurrentRouteStateBytes,
			&generatedDomainDecodeSentinel{called: &decoded}); err == nil || decoded {
			t.Fatalf("decode-time replacement reached JSON decode: decoded=%t error=%v", decoded, err)
		}
		reader.clear()

		captured, err = fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
		if err != nil {
			t.Fatal(err)
		}
		beforeDirectory, err = store.directoryIdentity()
		if err != nil {
			t.Fatal(err)
		}
		reader, err = newGeneratedDomainProtectedReader(store, beforeDirectory,
			map[string]gatewayHistoryFileFingerprint{path: captured})
		if err != nil {
			t.Fatal(err)
		}
		reader.usedBytes = generatedDomainStartupMaximumBytes - int64(len(grown)) + 1
		decoded = false
		if _, err := reader.readStrict(path, purpose, maxGatewayCurrentRouteStateBytes,
			&generatedDomainDecodeSentinel{called: &decoded}); err == nil || decoded ||
			!strings.Contains(err.Error(), "decoded bytes exceed") {
			t.Fatalf("actual plaintext budget reached JSON decode: decoded=%t error=%v", decoded, err)
		}
		reader.clear()
	})

	t.Run("read_only_rebind_final_scan_is_fresh_and_bounded", func(t *testing.T) {
		fixture := newGatewayCurrentStateFixture(t)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.dataRoot)
		if err != nil || !protected.upgrade.committed {
			t.Fatalf("rebind bound fixture = %#v, %v", protected, err)
		}
		store, present, _, err := inspectStateStoreReadOnly(fixture.dataRoot)
		if err != nil || !present {
			t.Fatalf("rebind bound store = %t, %v", present, err)
		}
		before, err := readGatewayHistorySnapshotPolicyBounded(store, true, true)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := func() {
			for index := 0; index <= generatedDomainStartupMaximumEntries; index++ {
				name := filepath.Join(store.root, "late-"+formatGeneratedDomainProtectedIndex(index)+".bundle")
				file, createErr := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if createErr != nil {
					t.Fatal(createErr)
				}
				if closeErr := file.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
			}
		}
		if _, err := scanGatewayRebindProtectedIntentHistoryReadOnly(
			store, protected.upgrade, before, checkpoint, true); err == nil ||
			!strings.Contains(err.Error(), "changed during inspection") {
			t.Fatalf("read-only rebind final scan accepted late namespace growth: %v", err)
		}
	})

	t.Run("outer_inspection_refuses_oversized_namespace_before_docker", func(t *testing.T) {
		fixture := newGeneratedDomainIdentityFixture(t)
		root := filepath.Join(fixture.dataRoot, "runtime", "generated-ingress")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index <= generatedDomainStartupMaximumEntries; index++ {
			name := filepath.Join(root, "outer-"+formatGeneratedDomainProtectedIndex(index)+".bundle")
			file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
		runner := &generatedDomainOuterRunner{}
		got, err := inspectGeneratedDomainStartup(context.Background(), fixture.stores, runner, fixture.options)
		if !errors.Is(err, errGeneratedDomainStartupRead) || !reflect.DeepEqual(got, generatedDomainStartupClassification{}) ||
			!reflect.DeepEqual(runner.commands, [][]string{{"info", "--format", "{{json .ID}}"}}) {
			t.Fatalf("outer oversized namespace = %#v, %v commands=%#v", got, err, runner.commands)
		}
	})
}

func formatGeneratedDomainProtectedIndex(value int) string {
	const digits = "0123456789"
	var buffer [5]byte
	for index := len(buffer) - 1; index >= 0; index-- {
		buffer[index] = digits[value%10]
		value /= 10
	}
	return string(buffer[:])
}
