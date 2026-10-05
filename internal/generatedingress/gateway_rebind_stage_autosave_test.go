package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindStageStartInventoryAcceptsExactAutosaveAfterDurableIntent(t *testing.T) {
	fixture, _, _, records := installGatewayRebindStageStartIntentProgress(t)
	binding := mustGatewayRebindStageStartIntentBinding(t, fixture.intent, records[7])
	ninth, err := newGatewayRebindStageStartIntentProgress(fixture.intent, records[7], binding, gatewayRebindProgressTimestamp(9))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot, ninth.Generation, ninth.OperationID, ninth.Sequence)
	if err != nil || store.installExact(context.Background(), ninth) != nil {
		t.Fatal("install exact durable start intent")
	}
	expected, err := gatewayRebindStageConfigBytes(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(expected)
	var decoded any
	if err := json.Unmarshal(expected, &decoded); err != nil {
		t.Fatal(err)
	}
	autosave, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(autosave)
	archive := stageAutosaveTestArchive(t, expected, autosave)
	runner := &gatewayRebindStageConfigIntentRunner{result: runtimeprocess.CommandResult{Stdout: archive}}
	fixture.predecessor.manager.runner = runner
	driver := managerGatewayRebindStageStartIntentDriver{manager: fixture.predecessor.manager}
	got, err := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, expected)
	if err != nil || got != gatewayRebindStageConfigInventoryExact {
		t.Fatalf("exact post-start autosave rejected: inventory=%d error=%v", got, err)
	}
	if len(runner.requests) != 1 || runner.requests[0].OutputLimit != 128<<10 {
		t.Fatalf("post-start archive did not use bounded 128KiB read: %#v", runner.requests)
	}
	if !bytes.Equal(expected, mustStageAutosaveConfig(t, fixture.intent)) {
		t.Fatal("reader changed approved stage bytes")
	}
}

func mustStageAutosaveConfig(t *testing.T, intent gatewayRebindProtectedIntent) []byte {
	t.Helper()
	value, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(value) })
	return value
}

func TestGatewayRebindStageAutosaveCanonicalConfigMatchesPinnedCaddy(t *testing.T) {
	input := []byte(" {\"z\": [1.0, \"<tag>\"], \"a\": {\"y\":true,\"x\":null}} ")
	want := []byte(`{"a":{"x":null,"y":true},"z":[1,"\u003ctag\u003e"]}`)
	got, err := gatewayRebindCanonicalAutosaveConfig(input)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("canonical snapshot mismatch: %v", err)
	}
	clear(got)
	for _, input := range [][]byte{nil, []byte("null"), []byte("[]"), []byte("1"), []byte(`"text"`), []byte("{bad}"), []byte("{} {}"), bytes.Repeat([]byte{' '}, gatewayV2MaxConfigBytes+1), []byte(`{"x":"` + strings.Repeat("<", gatewayV2MaxConfigBytes/2) + `"}`)} {
		if value, err := gatewayRebindCanonicalAutosaveConfig(input); err == nil {
			clear(value)
			t.Fatal("invalid, nonobject or oversized snapshot accepted")
		}
	}
}

func TestGatewayRebindStartedStageArchiveRequiresExactOptionalSnapshot(t *testing.T) {
	stage := []byte(`{"z":1,"a":2}`)
	autosave := []byte(`{"a":2,"z":1}`)
	root := tar.Header{Name: ".", Typeflag: tar.TypeDir}
	seed := gatewayRebindPinnedImageConfigDirectoryTestHeader()
	file := tar.Header{Name: "stage.json", Typeflag: tar.TypeReg, Size: int64(len(stage))}
	snapshot := tar.Header{Name: "caddy/autosave.json", Typeflag: tar.TypeReg, Size: int64(len(autosave)), Mode: 0600, Uid: 1000, Gid: 1000}
	archiveFor := func(headers []tar.Header, snapshotBytes []byte) []byte {
		var body []byte
		for _, header := range headers {
			if header.Typeflag != tar.TypeReg || header.Size == 0 {
				continue
			}
			if header.Name == "stage.json" || header.Name == "./stage.json" {
				body = append(body, stage...)
			} else {
				body = append(body, snapshotBytes...)
			}
		}
		return gatewayRebindStageConfigCopyHeadersTar(t, headers, body)
	}
	for _, name := range []string{"caddy/autosave.json", "./caddy/autosave.json"} {
		snapshot.Name = name
		for _, headers := range [][]tar.Header{{root, seed, file, snapshot}, {root, seed, snapshot, file}, {root, file, seed, snapshot}} {
			archive := archiveFor(headers, autosave)
			if got, err := gatewayRebindExactStartedStageConfigVolumeArchive(archive, stage); err != nil || got != gatewayRebindStageConfigInventoryExact {
				t.Fatalf("exact started snapshot rejected: %v", err)
			}
			if _, err := gatewayRebindExactStageConfigVolumeArchive(archive, stage); err == nil {
				t.Fatal("pre-start archive admitted autosave")
			}
		}
	}
	snapshot.Name = "caddy/autosave.json"
	for _, headers := range [][]tar.Header{{root, file}, {root, seed, file}} {
		if got, err := gatewayRebindExactStartedStageConfigVolumeArchive(archiveFor(headers, nil), stage); err != nil || got != gatewayRebindStageConfigInventoryExact {
			t.Fatalf("absent optional autosave rejected: %v", err)
		}
	}
	for name, mutate := range map[string]func(*tar.Header){
		"wrong uid": func(h *tar.Header) { h.Uid = 0 }, "wrong gid": func(h *tar.Header) { h.Gid = 0 },
		"wrong mode": func(h *tar.Header) { h.Mode = 0644 }, "setuid": func(h *tar.Header) { h.Mode = 04600 },
		"nested path": func(h *tar.Header) { h.Name = "caddy/other/autosave.json" }, "path alias": func(h *tar.Header) { h.Name = "././caddy/autosave.json" },
		"traversal": func(h *tar.Header) { h.Name = "caddy/../autosave.json" }, "absolute": func(h *tar.Header) { h.Name = "/config/caddy/autosave.json" },
		"directory": func(h *tar.Header) { h.Typeflag, h.Size = tar.TypeDir, 0 },
		"symlink":   func(h *tar.Header) { h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, "../stage.json", 0 },
		"hardlink":  func(h *tar.Header) { h.Typeflag, h.Linkname, h.Size = tar.TypeLink, "stage.json", 0 },
		"PAX":       func(h *tar.Header) { h.PAXRecords = map[string]string{"comment": "unexpected"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := snapshot
			mutate(&changed)
			if _, err := gatewayRebindExactStartedStageConfigVolumeArchive(archiveFor([]tar.Header{root, seed, file, changed}, autosave), stage); err == nil {
				t.Fatal("invalid autosave header accepted")
			}
		})
	}
	for name, headers := range map[string][]tar.Header{
		"missing seed": {root, file, snapshot}, "missing stage": {root, seed, snapshot},
		"duplicate snapshot": {root, seed, file, snapshot, snapshot}, "duplicate stage": {root, seed, file, snapshot, file},
		"foreign child":    {root, seed, file, snapshot, {Name: "caddy/foreign", Typeflag: tar.TypeReg}},
		"nested directory": {root, seed, file, snapshot, {Name: "caddy/nested/", Typeflag: tar.TypeDir}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gatewayRebindExactStartedStageConfigVolumeArchive(archiveFor(headers, autosave), stage); err == nil {
				t.Fatal("incomplete or extra inventory accepted")
			}
		})
	}
	if _, err := gatewayRebindExactStartedStageConfigVolumeArchive(archiveFor([]tar.Header{root, seed, file, snapshot}, stage), stage); err == nil {
		t.Fatal("noncanonical but equivalent observed JSON accepted")
	}
	valid := archiveFor([]tar.Header{root, seed, file, snapshot}, autosave)
	padding := append([]byte(nil), valid...)
	padding[5*512+len(autosave)] = 1
	for name, value := range map[string][]byte{
		"missing terminator": valid[:len(valid)-512], "partial block": valid[:len(valid)-1],
		"nonzero padding": padding, "trailing data": append(append([]byte(nil), valid...), bytes.Repeat([]byte{1}, 512)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gatewayRebindExactStartedStageConfigVolumeArchive(value, stage); err == nil {
				t.Fatal("invalid TAR framing accepted")
			}
		})
	}
	long := snapshot
	long.Name, long.Format = strings.Repeat("a", 110), tar.FormatGNU
	gnu := archiveFor([]tar.Header{root, seed, file, long}, autosave)
	if gnu[4*512+156] != tar.TypeGNULongName {
		t.Fatal("GNU test header missing")
	}
	clear(gnu[5*512 : 6*512])
	copy(gnu[5*512:], "caddy/autosave.json\x00")
	if _, err := gatewayRebindExactStartedStageConfigVolumeArchive(gnu, stage); err == nil {
		t.Fatal("hidden GNU metadata accepted")
	}
}

func TestGatewayRebindStartedStageArchiveBoundsTwoMaximumConfigs(t *testing.T) {
	stage := []byte(`{"x":"` + strings.Repeat("a", gatewayV2MaxConfigBytes-8) + `"}`)
	if len(stage) != gatewayV2MaxConfigBytes {
		t.Fatal("wrong maximum fixture size")
	}
	archive := stageAutosaveTestArchive(t, stage, stage)
	if len(archive) <= defaultOutputLimit || len(archive) > gatewayRebindStartedStageConfigArchiveLimit {
		t.Fatal("archive did not exercise dedicated bound")
	}
	if got, err := gatewayRebindExactStartedStageConfigVolumeArchive(archive, stage); err != nil || got != gatewayRebindStageConfigInventoryExact {
		t.Fatalf("bounded maximum archive rejected: %v", err)
	}
	if _, err := gatewayRebindExactStageConfigVolumeArchive(archive, stage); err == nil {
		t.Fatal("pre-start limit was broadened")
	}
	over := append(append([]byte(nil), archive...), make([]byte, gatewayRebindStartedStageConfigArchiveLimit-len(archive)+512)...)
	if _, err := gatewayRebindExactStartedStageConfigVolumeArchive(over, stage); err == nil {
		t.Fatal("oversized archive accepted")
	}
}

func TestGatewayRebindStageStartInventoryRequiresDurableUnchangedIntent(t *testing.T) {
	fixture, _, _, records := installGatewayRebindStageStartIntentProgress(t)
	manager := fixture.predecessor.manager
	binding := mustGatewayRebindStageStartIntentBinding(t, fixture.intent, records[7])
	ninth, err := newGatewayRebindStageStartIntentProgress(fixture.intent, records[7], binding, gatewayRebindProgressTimestamp(9))
	if err != nil {
		t.Fatal(err)
	}
	expected := mustStageAutosaveConfig(t, fixture.intent)
	autosave, err := gatewayRebindCanonicalAutosaveConfig(expected)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(autosave)
	archive := stageAutosaveTestArchive(t, expected, autosave)
	runner := &gatewayRebindStageAutosaveRunner{result: runtimeprocess.CommandResult{Stdout: archive}}
	manager.runner = runner
	driver := managerGatewayRebindStageStartIntentDriver{manager: manager}
	if _, err := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, expected); err == nil || len(runner.requests) != 0 {
		t.Fatal("undurable start binding reached Docker")
	}
	if _, err := driver.configVolumeInventory(context.Background(), fixture.intent, *records[7].Stage, expected); err == nil || len(runner.requests) != 1 || runner.requests[0].OutputLimit != defaultOutputLimit {
		t.Fatal("pre-start driver no longer uses strict original path")
	}
	store, err := newGatewayRebindProgressStore(manager.options.DataRoot, ninth.Generation, ninth.OperationID, 9)
	if err != nil || store.installExact(context.Background(), ninth) != nil {
		t.Fatal("install start receipt")
	}
	before, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*gatewayRebindStageIntent){
		"image drift": func(s *gatewayRebindStageIntent) { s.ObservedDockerImageID = strings.Repeat("f", 64) },
		"binding drift": func(s *gatewayRebindStageIntent) {
			v := *s.StageStartIntent
			v.ProbeToken = strings.Repeat("f", 64)
			s.StageStartIntent = &v
		},
	} {
		t.Run(name, func(t *testing.T) {
			runner.requests = nil
			stage := *ninth.Stage
			mutate(&stage)
			if _, err := driver.configVolumeInventory(context.Background(), fixture.intent, stage, expected); err == nil || len(runner.requests) != 0 {
				t.Fatal("unbound input reached Docker")
			}
		})
	}
	for name, result := range map[string]runtimeprocess.CommandResult{
		"truncated stdout": {Stdout: archive, StdoutTruncated: true}, "truncated stderr": {Stdout: archive, StderrTruncated: true}, "warning": {Stdout: archive, Stderr: []byte("warning")},
	} {
		t.Run(name, func(t *testing.T) {
			runner.result = result
			if _, err := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, expected); err == nil {
				t.Fatal("incomplete read accepted")
			}
		})
	}
	runner.result = runtimeprocess.CommandResult{Stdout: archive}
	runner.err = errors.New("Docker read failed")
	if _, err := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, expected); err == nil {
		t.Fatal("Docker failure accepted")
	}
	runner.err = nil
	runner.requests = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := driver.configVolumeInventory(ctx, fixture.intent, *ninth.Stage, expected); err == nil || len(runner.requests) != 0 {
		t.Fatal("canceled read reached Docker")
	}
	wrong := append([]byte(nil), expected...)
	wrong[0] = '['
	if _, err := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, wrong); err == nil || len(runner.requests) != 0 {
		t.Fatal("wrong expected bytes reached Docker")
	}
	backup := store.path + ".test-backup"
	runner.hook = func() {
		if err := os.Rename(store.path, backup); err != nil {
			t.Fatal(err)
		}
	}
	_, readErr := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, expected)
	if err := os.Rename(backup, store.path); err != nil {
		t.Fatal(err)
	}
	runner.hook = nil
	if readErr == nil {
		t.Fatal("changed protected history across read accepted")
	}
	if got, err := driver.configVolumeInventory(context.Background(), fixture.intent, *ninth.Stage, expected); err != nil || got != gatewayRebindStageConfigInventoryExact {
		t.Fatalf("fresh stopped-stage retry failed: %v", err)
	}
	after, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("inventory reader changed protected history")
	}
}

type gatewayRebindStageAutosaveRunner struct {
	requests []runtimeprocess.CommandRequest
	result   runtimeprocess.CommandResult
	err      error
	hook     func()
}

func (r *gatewayRebindStageAutosaveRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	result := r.result
	result.Stdout = append([]byte(nil), result.Stdout...)
	result.Stderr = append([]byte(nil), result.Stderr...)
	if r.hook != nil {
		r.hook()
	}
	return result, r.err
}

func stageAutosaveTestArchive(t *testing.T, stage, autosave []byte) []byte {
	t.Helper()
	headers := []tar.Header{
		{Name: ".", Typeflag: tar.TypeDir},
		{Name: "caddy/", Typeflag: tar.TypeDir, Mode: 01777},
		{Name: "stage.json", Typeflag: tar.TypeReg, Size: int64(len(stage))},
		{Name: "caddy/autosave.json", Typeflag: tar.TypeReg, Size: int64(len(autosave)), Mode: 0600, Uid: 1000, Gid: 1000},
	}
	return gatewayRebindStageConfigCopyHeadersTar(t, headers, append(append([]byte(nil), stage...), autosave...))
}
