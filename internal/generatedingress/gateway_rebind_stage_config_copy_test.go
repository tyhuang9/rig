package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindStageConfigCopyFake struct {
	*gatewayRebindStageConfigIntentFake
	expected          []byte
	inventory         gatewayRebindStageConfigInventory
	inventoryErr      error
	inventoryErrAfter int
	inventoryCalls    int
	copyErr           error
	copyMakesExact    bool
	copyCalls         int
	onCopy            func()
}

func (f *gatewayRebindStageConfigCopyFake) configVolumeInventory(_ context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	f.inventoryCalls++
	if !reflect.DeepEqual(intent, f.intent) || stage.StageContainer == nil || stage.StageConfigIntent == nil ||
		stage.StageContainer.ID != f.containerID || !bytes.Equal(expected, f.expected) {
		return 0, errors.New("unexpected stage config inventory input")
	}
	if f.inventoryErr != nil && (f.inventoryErrAfter == 0 || f.inventoryCalls >= f.inventoryErrAfter) {
		return 0, f.inventoryErr
	}
	return f.inventory, nil
}

func (f *gatewayRebindStageConfigCopyFake) copyStageConfig(_ context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, contents []byte,
) error {
	f.copyCalls++
	if !reflect.DeepEqual(intent, f.intent) || stage.StageContainer == nil ||
		stage.StageContainer.ID != f.containerID || !bytes.Equal(contents, f.expected) {
		return errors.New("unexpected stage config copy input")
	}
	if f.copyMakesExact {
		f.inventory = gatewayRebindStageConfigInventoryExact
	}
	if f.onCopy != nil {
		f.onCopy()
	}
	return f.copyErr
}

func TestGatewayRebindExactStageConfigVolumeArchive(t *testing.T) {
	expected := []byte(`{"admin":{"listen":"localhost:2019"}}`)
	validEmpty := gatewayRebindStageConfigCopyTestTar(t, expected, false)
	validExact := gatewayRebindStageConfigCopyTestTar(t, expected, true)
	nonzeroPadding := append([]byte(nil), validExact...)
	nonzeroPadding[2*512+len(expected)] = 1
	payloadEnd := 2*512 + ((len(expected)+511)/512)*512
	oneBlockTerminator := append([]byte(nil), validExact[:payloadEnd+512]...)
	mismatched := append([]byte(nil), expected...)
	mismatched[0] ^= 1
	for _, test := range []struct {
		name      string
		archive   []byte
		expected  []byte
		inventory gatewayRebindStageConfigInventory
	}{
		{name: "empty", archive: validEmpty, expected: expected, inventory: gatewayRebindStageConfigInventoryEmpty},
		{name: "exact stage config", archive: validExact, expected: expected, inventory: gatewayRebindStageConfigInventoryExact},
		{name: "exact dot slash stage config", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			{Name: "./" + gatewayV2StageConfigFilename, Typeflag: tar.TypeReg, Size: int64(len(expected))},
		}, expected), expected: expected, inventory: gatewayRebindStageConfigInventoryExact},
		{name: "mismatched bytes", archive: validExact, expected: append([]byte(nil), expected[:len(expected)-1]...)},
		{name: "truncated", archive: append([]byte(nil), validExact[:len(validExact)-512]...), expected: expected},
		{name: "one block terminator", archive: oneBlockTerminator, expected: expected},
		{name: "non block aligned", archive: append(append([]byte(nil), validExact...), 0), expected: expected},
		{name: "nonzero file padding", archive: nonzeroPadding, expected: expected},
		{name: "trailing content", archive: append(append([]byte(nil), validExact...), bytes.Repeat([]byte{1}, 512)...), expected: expected},
		{name: "symlink", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			{Name: gatewayV2StageConfigFilename, Typeflag: tar.TypeSymlink, Linkname: "elsewhere"},
		}, nil), expected: expected},
		{name: "extra file", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			{Name: gatewayV2StageConfigFilename, Typeflag: tar.TypeReg, Size: int64(len(expected))},
			{Name: "foreign.json", Typeflag: tar.TypeReg, Size: 1},
		}, append(append([]byte(nil), expected...), 'x')), expected: expected},
		{name: "duplicate stage config", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			{Name: gatewayV2StageConfigFilename, Typeflag: tar.TypeReg, Size: int64(len(expected))},
			{Name: gatewayV2StageConfigFilename, Typeflag: tar.TypeReg, Size: int64(len(expected))},
		}, append(append([]byte(nil), expected...), expected...)), expected: expected},
		{name: "PAX metadata", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir, PAXRecords: map[string]string{"comment": "unexpected"}},
		}, nil), expected: expected},
		{name: "foreign filename", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir}, {Name: "active.json", Typeflag: tar.TypeReg, Size: int64(len(expected))},
		}, expected), expected: expected},
		{name: "same length mismatched content", archive: gatewayRebindStageConfigCopyTestTar(t, mismatched, true), expected: expected},
	} {
		t.Run(test.name, func(t *testing.T) {
			inventory, err := gatewayRebindExactStageConfigVolumeArchive(test.archive, test.expected)
			if test.inventory == 0 {
				if err == nil {
					t.Fatalf("invalid archive accepted as inventory %d", inventory)
				}
				return
			}
			if err != nil || inventory != test.inventory {
				t.Fatalf("inventory=%d want=%d error=%v", inventory, test.inventory, err)
			}
		})
	}
}

func TestGatewayRebindStageConfigCopyDriverUsesPinnedContainerAndRejectsCommandUncertainty(t *testing.T) {
	_, intent, records := gatewayRebindProgressThroughContainer(t)
	intentBinding, err := gatewayRebindStageConfigIntentBindingFor(intent, records[5])
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(intent, records[5], intentBinding,
		gatewayRebindProgressTimestamp(7))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(expected)
	validArchive := gatewayRebindStageConfigCopyTestTar(t, expected, true)
	mismatched := append([]byte(nil), expected...)
	mismatched[len(mismatched)-1] ^= 1
	foreignArchive := gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
		{Name: ".", Typeflag: tar.TypeDir},
		{Name: "foreign.json", Typeflag: tar.TypeReg, Size: int64(len(expected))},
	}, expected)
	for _, test := range []struct {
		name      string
		result    runtimeprocess.CommandResult
		err       error
		inventory gatewayRebindStageConfigInventory
	}{
		{name: "exact", result: runtimeprocess.CommandResult{Stdout: validArchive}, inventory: gatewayRebindStageConfigInventoryExact},
		{name: "empty", result: runtimeprocess.CommandResult{Stdout: gatewayRebindStageConfigCopyTestTar(t, expected, false)}, inventory: gatewayRebindStageConfigInventoryEmpty},
		{name: "stdout truncated", result: runtimeprocess.CommandResult{Stdout: validArchive, StdoutTruncated: true}},
		{name: "stderr truncated", result: runtimeprocess.CommandResult{Stdout: validArchive, StderrTruncated: true}},
		{name: "stderr", result: runtimeprocess.CommandResult{Stdout: validArchive, Stderr: []byte("warning")}},
		{name: "Docker error", err: errors.New("Docker unavailable")},
		{name: "foreign archive", result: runtimeprocess.CommandResult{Stdout: foreignArchive}},
		{name: "mismatched archive", result: runtimeprocess.CommandResult{
			Stdout: gatewayRebindStageConfigCopyTestTar(t, mismatched, true),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &gatewayRebindStageConfigIntentRunner{result: test.result, err: test.err}
			manager := &Manager{runner: runner, options: Options{
				DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Second,
				OutputLimit: defaultOutputLimit,
			}}
			driver := managerGatewayRebindStageConfigCopyDriver{manager: manager}
			inventory, err := driver.configVolumeInventory(context.Background(), intent, *seventh.Stage, expected)
			if test.inventory == 0 {
				if err == nil {
					t.Fatalf("uncertain command accepted as inventory %d", inventory)
				}
			} else if err != nil || inventory != test.inventory {
				t.Fatalf("inventory=%d want=%d error=%v", inventory, test.inventory, err)
			}
			if len(runner.requests) != 1 || !reflect.DeepEqual(runner.requests[0].Args, []string{
				"container", "cp", seventh.Stage.StageContainer.ID + ":/config/.", "-",
			}) {
				t.Fatalf("inventory command=%#v", runner.requests)
			}
		})
	}
}

func TestGatewayRebindStageConfigCopyDriverUsesGuardedCopyWithoutClearingCallerBytes(t *testing.T) {
	_, intent, records := gatewayRebindProgressThroughContainer(t)
	intentBinding, err := gatewayRebindStageConfigIntentBindingFor(intent, records[5])
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(intent, records[5], intentBinding,
		gatewayRebindProgressTimestamp(7))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(expected)
	want := append([]byte(nil), expected...)
	runner := &gatewayRebindStageConfigIntentRunner{}
	manager, _ := newManagerFixture(t, false)
	manager.runner = runner
	driver := managerGatewayRebindStageConfigCopyDriver{manager: manager}
	if err := driver.copyStageConfig(context.Background(), intent, *seventh.Stage, expected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, want) {
		t.Fatal("guarded config copy cleared the caller's readback bytes")
	}
	if len(runner.requests) != 1 || len(runner.requests[0].Args) != 4 ||
		runner.requests[0].Args[0] != "container" || runner.requests[0].Args[1] != "cp" ||
		runner.requests[0].Args[3] != seventh.Stage.StageContainer.ID+":/config/"+gatewayV2StageConfigFilename {
		t.Fatalf("copy command=%#v", runner.requests)
	}
	mutated := append([]byte(nil), expected...)
	mutated[len(mutated)/2] ^= 1
	if err := driver.copyStageConfig(context.Background(), intent, *seventh.Stage, mutated); err == nil {
		t.Fatal("same-length mutated stage config reached guarded copy")
	}
	if len(runner.requests) != 1 {
		t.Fatalf("mutated stage config reached Docker: requests=%d", len(runner.requests))
	}
}

func TestGatewayRebindStageConfigCopyAppendsAndFreshManagerReplays(t *testing.T) {
	fixture, fake, reads, records := installGatewayRebindStageConfigCopyIntentProgress(t)
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 7 {
		t.Fatalf("base history=%d error=%v", len(history.Progress), err)
	}
	baseBytes := make([][]byte, 7)
	for index, entry := range history.Progress {
		baseBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := false
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8),
		func() { checkpoint = true }); err != nil {
		t.Fatalf("copy stage config: %v", err)
	}
	if !checkpoint || fake.copyCalls != 1 || fake.inventory != gatewayRebindStageConfigInventoryExact {
		t.Fatalf("checkpoint=%t copies=%d inventory=%d", checkpoint, fake.copyCalls, fake.inventory)
	}
	history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 {
		t.Fatalf("sequence-eight history=%d error=%v", len(history.Progress), err)
	}
	for index := range baseBytes {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !bytes.Equal(body, baseBytes[index]) ||
			history.Progress[index].Record.Digest != records[index].Digest {
			t.Fatalf("sequence %d changed: %v", index+1, readErr)
		}
	}
	copyBinding, err := gatewayRebindStageConfigCopyBindingFor(fixture.intent, records[6])
	if err != nil || history.Progress[7].Record.Stage == nil ||
		history.Progress[7].Record.Stage.StageConfigCopy == nil ||
		*history.Progress[7].Record.Stage.StageConfigCopy != copyBinding {
		t.Fatalf("copy receipt=%#v error=%v", history.Progress[7].Record, err)
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite changed: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(9), nil); err != nil {
		t.Fatalf("fresh Manager replay: %v", err)
	}
	if fake.copyCalls != 1 {
		t.Fatalf("exact replay copied again: %d", fake.copyCalls)
	}
}

func TestGatewayRebindStageConfigCopyAdoptsOnlyExactExistingFile(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
	fake.inventory = gatewayRebindStageConfigInventoryExact
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil); err != nil {
		t.Fatalf("adopt exact stage config: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 || fake.copyCalls != 0 {
		t.Fatalf("exact adoption: progress=%d copies=%d error=%v", len(history.Progress), fake.copyCalls, err)
	}
}

func TestGatewayRebindStageConfigCopyReceiptReplayRejectsInventoryAndRuntimeDriftWithoutRecopy(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageConfigCopyFake)
	}{
		{name: "missing stage config", mutate: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.inventory = gatewayRebindStageConfigInventoryEmpty
		}},
		{name: "foreign config content", mutate: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.inventoryErr = errors.New("foreign config inventory")
		}},
		{name: "mismatched stage config", mutate: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.inventoryErr = errors.New("stage config content mismatch")
		}},
		{name: "stopped container drift", mutate: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.running = true
		}},
		{name: "pinned image drift", mutate: func(fake *gatewayRebindStageConfigCopyFake) {
			fake.imageDrift = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
			if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil); err != nil {
				t.Fatalf("install sequence eight: %v", err)
			}
			test.mutate(fake)
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			err := restarted.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(9), nil)
			history, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 8 || fake.copyCalls != 1 {
				t.Fatalf("receipt replay accepted drift: err=%v scan=%v progress=%d copies=%d",
					err, scanErr, len(history.Progress), fake.copyCalls)
			}
		})
	}
}

func TestGatewayRebindStageConfigCopyPostInstallAttestationFailureKeepsReceiptAndReplays(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
	fake.inventoryErr = errors.New("post-install inventory unavailable")
	fake.inventoryErrAfter = 7
	err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil)
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil || scanErr != nil || len(history.Progress) != 8 || fake.copyCalls != 1 {
		t.Fatalf("post-install failure lost receipt: err=%v scan=%v progress=%d copies=%d",
			err, scanErr, len(history.Progress), fake.copyCalls)
	}
	fake.inventoryErr = nil
	fake.inventoryErrAfter = 0
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(9), nil); err != nil {
		t.Fatalf("fresh exact receipt replay failed: %v", err)
	}
	if fake.copyCalls != 1 {
		t.Fatalf("fresh receipt replay recopied config: %d", fake.copyCalls)
	}
}

func TestGatewayRebindStageConfigCopyFailedEmptyCopyRetriesGuardedCopyOnce(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
	fake.copyMakesExact = false
	fake.copyErr = errors.New("copy failed before installing content")
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil); err == nil {
		t.Fatal("failed empty copy reported success")
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 7 || fake.inventory != gatewayRebindStageConfigInventoryEmpty ||
		fake.copyCalls != 1 {
		t.Fatalf("failed empty copy: progress=%d inventory=%d copies=%d error=%v",
			len(history.Progress), fake.inventory, fake.copyCalls, err)
	}
	fake.copyErr = nil
	fake.copyMakesExact = true
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(8), nil); err != nil {
		t.Fatalf("guarded empty retry failed: %v", err)
	}
	history, err = restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 || fake.copyCalls != 2 {
		t.Fatalf("guarded retry: progress=%d copies=%d error=%v", len(history.Progress), fake.copyCalls, err)
	}
}

func TestGatewayRebindStageConfigCopyFailuresRemainAtSequenceSeven(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*gatewayRebindEffectBoundaryFixture, *gatewayRebindStageConfigCopyFake,
			*gatewayRebindSuccessorPreflightReads)
		wantCopy int
	}{
		{name: "unexpected inventory", configure: func(_ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigCopyFake, _ *gatewayRebindSuccessorPreflightReads,
		) {
			fake.inventory = 0
		}},
		{name: "inventory error", configure: func(_ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigCopyFake, _ *gatewayRebindSuccessorPreflightReads,
		) {
			fake.inventoryErr = errors.New("inventory unavailable")
		}},
		{name: "container drift before copy", configure: func(_ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigCopyFake, _ *gatewayRebindSuccessorPreflightReads,
		) {
			fake.running = true
		}},
		{name: "host topology drift", configure: func(_ *gatewayRebindEffectBoundaryFixture,
			_ *gatewayRebindStageConfigCopyFake, reads *gatewayRebindSuccessorPreflightReads,
		) {
			original := reads.network.host
			reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
				value, err := original()
				value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("10.99.0.0/16"))
				return value, err
			}
		}},
		{name: "ambiguous copy", wantCopy: 1, configure: func(_ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigCopyFake, _ *gatewayRebindSuccessorPreflightReads,
		) {
			fake.copyMakesExact = true
			fake.copyErr = errors.New("copy result uncertain")
		}},
		{name: "container drift after copy", wantCopy: 1, configure: func(_ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigCopyFake, _ *gatewayRebindSuccessorPreflightReads,
		) {
			fake.onCopy = func() { fake.running = true }
		}},
		{name: "failed readback", wantCopy: 1, configure: func(_ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigCopyFake, _ *gatewayRebindSuccessorPreflightReads,
		) {
			fake.inventoryErr = errors.New("readback unavailable")
			fake.inventoryErrAfter = 5
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
			test.configure(&fixture, fake, &reads)
			err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil)
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err == nil || scanErr != nil || len(history.Progress) != 7 || fake.copyCalls != test.wantCopy {
				t.Fatalf("failure escaped fence: err=%v scan=%v progress=%d copies=%d",
					err, scanErr, len(history.Progress), fake.copyCalls)
			}
		})
	}
}

func TestGatewayRebindStageConfigCopyRejectsDriftAdjacentToCopy(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
	err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8),
		func() { fake.running = true })
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err == nil || scanErr != nil || len(history.Progress) != 7 || fake.copyCalls != 0 {
		t.Fatalf("adjacent drift escaped fence: err=%v scan=%v progress=%d copies=%d",
			err, scanErr, len(history.Progress), fake.copyCalls)
	}
}

func TestGatewayRebindStageConfigCopyRetryAdoptsAmbiguousExactCopy(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageConfigCopyIntentProgress(t)
	fake.copyMakesExact = true
	fake.copyErr = errors.New("copy result uncertain")
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorStageConfigWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil); err == nil {
		t.Fatal("ambiguous copy reported success")
	}
	fake.copyErr = nil
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.copyGatewayRebindSuccessorStageConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(8), nil); err != nil {
		t.Fatalf("retry did not adopt exact file: %v", err)
	}
	history, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 8 || fake.copyCalls != 1 {
		t.Fatalf("ambiguous retry: progress=%d copies=%d error=%v", len(history.Progress), fake.copyCalls, err)
	}
}

func installGatewayRebindStageConfigCopyIntentProgress(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindStageConfigCopyFake, gatewayRebindSuccessorPreflightReads, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture, intentFake, reads, records := installGatewayRebindStageConfigContainerProgress(t)
	binding, err := gatewayRebindStageConfigIntentBindingFor(fixture.intent, records[5])
	if err != nil {
		t.Fatal(err)
	}
	seventh, err := newGatewayRebindStageConfigIntentProgress(fixture.intent, records[5], binding,
		gatewayRebindProgressTimestamp(7))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 7)
	if err != nil || store.installExact(context.Background(), seventh) != nil {
		t.Fatalf("install config intent progress: %v", err)
	}
	intentFake.stage = *seventh.Stage
	expected, err := gatewayRebindStageConfigBytes(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	fake := &gatewayRebindStageConfigCopyFake{
		gatewayRebindStageConfigIntentFake: intentFake, expected: expected,
		inventory: gatewayRebindStageConfigInventoryEmpty, copyMakesExact: true,
	}
	t.Cleanup(func() { clear(fake.expected) })
	return fixture, fake, reads, append(records, seventh)
}

func gatewayRebindStageConfigCopyTestTar(t *testing.T, body []byte, includeConfig bool) []byte {
	t.Helper()
	headers := []tar.Header{{Name: ".", Typeflag: tar.TypeDir, Mode: 0o755}}
	if includeConfig {
		headers = append(headers, tar.Header{Name: gatewayV2StageConfigFilename, Typeflag: tar.TypeReg,
			Mode: 0o644, Size: int64(len(body))})
	}
	return gatewayRebindStageConfigCopyHeadersTar(t, headers, body)
}

func gatewayRebindStageConfigCopyHeadersTar(t *testing.T, headers []tar.Header, body []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	offset := 0
	for index := range headers {
		header := headers[index]
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg && header.Size > 0 {
			end := offset + int(header.Size)
			if end > len(body) {
				t.Fatal("insufficient TAR test body")
			}
			if _, err := writer.Write(body[offset:end]); err != nil {
				t.Fatal(err)
			}
			offset = end
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

var _ gatewayRebindStageConfigCopyDriver = (*gatewayRebindStageConfigCopyFake)(nil)
