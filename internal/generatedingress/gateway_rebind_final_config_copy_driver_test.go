package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindExactFinalConfigVolumeArchiveAcceptsOnlyStageOrExactPair(t *testing.T) {
	stage, active := []byte(`{"stage":true}`), []byte(`{"active":true}`)
	for _, test := range []struct {
		name  string
		files []string
		want  gatewayRebindFinalConfigInventory
	}{
		{name: "stage only", files: []string{"stage.json"}, want: gatewayRebindFinalConfigInventoryStageOnly},
		{name: "stage then active", files: []string{"stage.json", "active.json"}, want: gatewayRebindFinalConfigInventoryExactPair},
		{name: "active then stage", files: []string{"active.json", "stage.json"}, want: gatewayRebindFinalConfigInventoryExactPair},
		{name: "dot slash names", files: []string{"./active.json", "./stage.json"}, want: gatewayRebindFinalConfigInventoryExactPair},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := finalConfigDriverArchive(t, stage, active, test.files...)
			got, err := gatewayRebindExactFinalConfigVolumeArchive(archive, stage, active)
			if err != nil || got != test.want {
				t.Fatalf("inventory=%d want=%d error=%v", got, test.want, err)
			}
			if test.want == gatewayRebindFinalConfigInventoryExactPair {
				if _, err := gatewayRebindExactStageConfigVolumeArchive(archive, stage); err == nil {
					t.Fatal("old stage-only parser accepted final config pair")
				}
			}
		})
	}
}

func TestGatewayRebindExactFinalConfigVolumeArchiveRejectsAmbiguousInventory(t *testing.T) {
	stage, active := []byte(`{"stage":true}`), []byte(`{"active":true}`)
	pair := finalConfigDriverArchive(t, stage, active, "stage.json", "active.json")
	stageOnly := finalConfigDriverArchive(t, stage, active, "stage.json")
	root := tar.Header{Name: ".", Typeflag: tar.TypeDir}
	stageHeader := tar.Header{Name: "stage.json", Typeflag: tar.TypeReg, Size: int64(len(stage))}
	activeHeader := tar.Header{Name: "active.json", Typeflag: tar.TypeReg, Size: int64(len(active))}
	join := func(values ...[]byte) []byte { return bytes.Join(values, nil) }
	padding := append([]byte(nil), pair...)
	padding[1024+len(stage)] = 1
	activePadding := append([]byte(nil), pair...)
	activePadding[2048+len(active)] = 1
	gnuBody := []byte("stage.json\x00")
	gnu := gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
		root, {Name: "././@LongLink", Typeflag: tar.TypeReg, Size: int64(len(gnuBody))}, stageHeader, activeHeader,
	}, join(gnuBody, stage, active))
	finalConfigDriverSetTarType(gnu[512:1024], tar.TypeGNULongName)
	for _, test := range []struct {
		name    string
		archive []byte
	}{
		{name: "empty bytes"},
		{name: "empty volume", archive: finalConfigDriverArchive(t, stage, active)},
		{name: "active without stage", archive: finalConfigDriverArchive(t, stage, active, "active.json")},
		{name: "missing root", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{stageHeader, activeHeader}, join(stage, active))},
		{name: "root is regular file", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeReg}, stageHeader}, stage)},
		{name: "foreign root", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{{Name: "config", Typeflag: tar.TypeDir}, stageHeader}, stage)},
		{name: "duplicate stage", archive: finalConfigDriverArchive(t, stage, active, "stage.json", "stage.json")},
		{name: "duplicate active", archive: finalConfigDriverArchive(t, stage, active, "stage.json", "active.json", "active.json")},
		{name: "duplicate alias", archive: finalConfigDriverArchive(t, stage, active, "stage.json", "./stage.json")},
		{name: "extra file", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, stageHeader, activeHeader, {Name: "unexpected", Typeflag: tar.TypeReg}}, join(stage, active))},
		{name: "extra directory", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, stageHeader, {Name: "other", Typeflag: tar.TypeDir}}, stage)},
		{name: "symlink", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, stageHeader, {Name: "active.json", Typeflag: tar.TypeSymlink, Linkname: "stage.json"}}, stage)},
		{name: "hardlink", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, stageHeader, {Name: "active.json", Typeflag: tar.TypeLink, Linkname: "stage.json"}}, stage)},
		{name: "PAX root metadata", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir, PAXRecords: map[string]string{"comment": "extra"}}, stageHeader}, stage)},
		{name: "PAX file metadata", archive: gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, {Name: "stage.json", Typeflag: tar.TypeReg, Size: int64(len(stage)), PAXRecords: map[string]string{"comment": "extra"}}}, stage)},
		{name: "GNU long-name metadata", archive: gnu},
		{name: "same-length wrong stage", archive: finalConfigDriverArchive(t, bytes.Repeat([]byte{'x'}, len(stage)), active, "stage.json", "active.json")},
		{name: "same-length wrong active", archive: finalConfigDriverArchive(t, stage, bytes.Repeat([]byte{'x'}, len(active)), "stage.json", "active.json")},
		{name: "partial active", archive: finalConfigDriverArchive(t, stage, active[:len(active)-1], "stage.json", "active.json")},
		{name: "oversized active content", archive: finalConfigDriverArchive(t, stage, append(append([]byte(nil), active...), 'x'), "stage.json", "active.json")},
		{name: "truncated header", archive: pair[:1800]},
		{name: "truncated active payload", archive: pair[:2048+len(active)-1]},
		{name: "one terminator block", archive: pair[:len(pair)-512]},
		{name: "non-block-aligned padding", archive: append(append([]byte(nil), stageOnly...), 0)},
		{name: "nonzero stage padding", archive: padding},
		{name: "nonzero active padding", archive: activePadding},
		{name: "nonzero trailing block", archive: append(append([]byte(nil), pair...), bytes.Repeat([]byte{1}, 512)...)},
		{name: "second archive after terminator", archive: append(append([]byte(nil), stageOnly...), stageOnly...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := gatewayRebindExactFinalConfigVolumeArchive(test.archive, stage, active); err == nil || got != 0 {
				t.Fatalf("ambiguous archive accepted: inventory=%d error=%v", got, err)
			}
		})
	}
	for _, name := range []string{"/stage.json", "../stage.json", "sub/../stage.json", "././stage.json", "stage.json/", `config\stage.json`} {
		t.Run("path "+name, func(t *testing.T) {
			archive := append([]byte(nil), stageOnly...)
			header := archive[512:1024]
			clear(header[:100])
			copy(header[:100], name)
			finalConfigDriverSetTarType(header, tar.TypeReg)
			if got, err := gatewayRebindExactFinalConfigVolumeArchive(archive, stage, active); err == nil || got != 0 {
				t.Fatalf("path alias accepted: inventory=%d error=%v", got, err)
			}
		})
	}
}

func TestGatewayRebindExactFinalConfigVolumeArchiveBoundsBothConfigsAndArchive(t *testing.T) {
	stage := bytes.Repeat([]byte{'s'}, gatewayV2MaxConfigBytes)
	active := bytes.Repeat([]byte{'a'}, gatewayV2MaxConfigBytes)
	pair := finalConfigDriverArchive(t, stage, active, "active.json", "stage.json")
	if len(pair) <= 64<<10 || len(pair) > gatewayRebindFinalConfigArchiveLimit {
		t.Fatalf("max config pair archive has unexpected size %d", len(pair))
	}
	for _, archive := range [][]byte{pair, append(append([]byte(nil), pair...), make([]byte, gatewayRebindFinalConfigArchiveLimit-len(pair))...)} {
		if got, err := gatewayRebindExactFinalConfigVolumeArchive(archive, stage, active); err != nil || got != gatewayRebindFinalConfigInventoryExactPair {
			t.Fatalf("bounded max-size pair rejected: bytes=%d inventory=%d error=%v", len(archive), got, err)
		}
	}
	oversizedArchive := append(append([]byte(nil), pair...), make([]byte, gatewayRebindFinalConfigArchiveLimit-len(pair)+512)...)
	for _, test := range []struct {
		name                   string
		archive, stage, active []byte
	}{
		{name: "archive over bound", archive: oversizedArchive, stage: stage, active: active},
		{name: "stage over bound", archive: pair, stage: append(append([]byte(nil), stage...), 's'), active: active},
		{name: "active over bound", archive: pair, stage: stage, active: append(append([]byte(nil), active...), 'a')},
		{name: "empty expected stage", archive: pair, active: active},
		{name: "empty expected active", archive: finalConfigDriverArchive(t, stage, active, "stage.json"), stage: stage},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := gatewayRebindExactFinalConfigVolumeArchive(test.archive, test.stage, test.active); err == nil || got != 0 {
				t.Fatalf("invalid bound accepted: inventory=%d error=%v", got, err)
			}
		})
	}
}

type finalConfigDriverRunner struct {
	requests []runtimeprocess.CommandRequest
	contexts []context.Context
	result   runtimeprocess.CommandResult
	err      error
	onRun    func(runtimeprocess.CommandRequest)
}

func (r *finalConfigDriverRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	r.contexts = append(r.contexts, ctx)
	if r.onRun != nil {
		r.onRun(request)
	}
	return r.result, r.err
}

func TestGatewayRebindFinalConfigCopyDriverInventoryUsesBoundedPinnedReadAndClearsResults(t *testing.T) {
	intent, stage, stageBytes, activeBytes := finalConfigCopyDriverFixture(t)
	pair := finalConfigDriverArchive(t, stageBytes, activeBytes, "stage.json", "active.json")
	marker := "opaque-docker-output-must-not-escape"
	for _, test := range []struct {
		name                             string
		stdout, stderr                   []byte
		stdoutTruncated, stderrTruncated bool
		err                              error
		want                             gatewayRebindFinalConfigInventory
	}{
		{name: "exact pair", stdout: pair, want: gatewayRebindFinalConfigInventoryExactPair},
		{name: "stage only", stdout: finalConfigDriverArchive(t, stageBytes, activeBytes, "stage.json"), want: gatewayRebindFinalConfigInventoryStageOnly},
		{name: "stdout truncated", stdout: pair, stdoutTruncated: true},
		{name: "stderr truncated", stdout: pair, stderrTruncated: true},
		{name: "stderr warning", stdout: pair, stderr: []byte(marker)},
		{name: "runner error", stdout: pair, stderr: []byte(marker), err: errors.New(marker)},
		{name: "runner cancellation", stdout: pair, stderr: []byte(marker), err: context.Canceled},
		{name: "runner deadline", stdout: pair, stderr: []byte(marker), err: context.DeadlineExceeded},
		{name: "invalid archive", stdout: []byte(marker)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &finalConfigDriverRunner{result: runtimeprocess.CommandResult{
				Stdout: append([]byte(nil), test.stdout...), Stderr: append([]byte(nil), test.stderr...),
				StdoutTruncated: test.stdoutTruncated, StderrTruncated: test.stderrTruncated,
			}, err: test.err}
			manager := &Manager{runner: runner, dockerEnv: []string{"DOCKER_HOST=local-test-only"}, options: Options{
				DockerExecutable: "pinned-docker", WorkingDirectory: t.TempDir(), CommandTimeout: 7 * time.Second, OutputLimit: 4096,
			}}
			driver := managerGatewayRebindFinalConfigCopyDriver{manager: manager}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, err := driver.finalConfigVolumeInventory(ctx, intent, stage, stageBytes, activeBytes)
			if (test.want == 0 && (err == nil || got != 0)) || (test.want != 0 && (err != nil || got != test.want)) {
				t.Fatalf("inventory=%d want=%d error=%v", got, test.want, err)
			}
			if err != nil && strings.Contains(err.Error(), marker) {
				t.Fatal("opaque runner output escaped in error")
			}
			if len(runner.requests) != 1 || runner.contexts[0] != ctx {
				t.Fatalf("read requests=%#v", runner.requests)
			}
			request := runner.requests[0]
			if !reflect.DeepEqual(request.Args, []string{"container", "cp", stage.StageContainer.ID + ":/config/.", "-"}) ||
				request.Executable != manager.options.DockerExecutable || request.Directory != manager.options.WorkingDirectory ||
				!reflect.DeepEqual(request.Env, manager.dockerEnv) || request.Timeout != 7*time.Second || request.OutputLimit != 128<<10 ||
				manager.options.OutputLimit != 4096 {
				t.Fatalf("unexpected bounded inventory request: %#v", request)
			}
			if !finalConfigDriverZeroed(runner.result.Stdout) || !finalConfigDriverZeroed(runner.result.Stderr) {
				t.Fatal("inventory command buffers were retained")
			}
		})
	}
}

func TestGatewayRebindFinalConfigCopyDriverRejectsUnboundInputsBeforeDocker(t *testing.T) {
	intent, stage, stageBytes, activeBytes := finalConfigCopyDriverFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindProtectedIntent, *gatewayRebindStageIntent, *[]byte, *[]byte)
	}{
		{name: "missing final intent", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.FinalConfigIntent = nil
		}},
		{name: "missing stage intent", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.StageConfigIntent = nil
		}},
		{name: "changed container identity", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.StageContainer.ID = strings.Repeat("9", 64)
		}},
		{name: "changed successor identity", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.Identity.Digest = strings.Repeat("9", 64)
		}},
		{name: "changed observed topology", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.NetworkTopologyDigest = strings.Repeat("9", 64)
		}},
		{name: "changed destination", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.FinalConfigIntent.Destination = "/config/stage.json"
		}},
		{name: "changed content digest", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.FinalConfigIntent.ContentDigest = strings.Repeat("9", 64)
		}},
		{name: "changed protected digest", mutate: func(_ *gatewayRebindProtectedIntent, s *gatewayRebindStageIntent, _, _ *[]byte) {
			s.FinalConfigIntent.ProtectedIntentDigest = strings.Repeat("9", 64)
		}},
		{name: "changed stage bytes", mutate: func(_ *gatewayRebindProtectedIntent, _ *gatewayRebindStageIntent, b, _ *[]byte) { (*b)[0] ^= 1 }},
		{name: "changed active bytes", mutate: func(_ *gatewayRebindProtectedIntent, _ *gatewayRebindStageIntent, _, b *[]byte) { (*b)[0] ^= 1 }},
		{name: "empty active bytes", mutate: func(_ *gatewayRebindProtectedIntent, _ *gatewayRebindStageIntent, _, b *[]byte) { *b = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := finalConfigDriverCloneStage(t, stage)
			candidateIntent := intent
			stageInput, activeInput := append([]byte(nil), stageBytes...), append([]byte(nil), activeBytes...)
			test.mutate(&candidateIntent, &candidate, &stageInput, &activeInput)
			runner := &finalConfigDriverRunner{}
			driver := managerGatewayRebindFinalConfigCopyDriver{manager: &Manager{runner: runner}}
			if got, err := driver.finalConfigVolumeInventory(context.Background(), candidateIntent, candidate, stageInput, activeInput); err == nil || got != 0 {
				t.Fatalf("unbound inventory accepted: inventory=%d error=%v", got, err)
			}
			if test.name != "changed stage bytes" {
				if err := driver.copyFinalConfig(context.Background(), candidateIntent, candidate, activeInput); err == nil {
					t.Fatal("unbound copy accepted")
				}
			}
			if len(runner.requests) != 0 {
				t.Fatalf("unbound input reached Docker: %#v", runner.requests)
			}
		})
	}
	for _, ctx := range []context.Context{nil, canceledFinalConfigDriverContext()} {
		runner := &finalConfigDriverRunner{}
		driver := managerGatewayRebindFinalConfigCopyDriver{manager: &Manager{runner: runner}}
		if _, err := driver.finalConfigVolumeInventory(ctx, intent, stage, stageBytes, activeBytes); err == nil {
			t.Fatal("invalid context accepted by inventory")
		}
		if err := driver.copyFinalConfig(ctx, intent, stage, activeBytes); err == nil {
			t.Fatal("invalid context accepted by copy")
		}
		if len(runner.requests) != 0 {
			t.Fatal("invalid context reached Docker")
		}
	}
	nilDriver := managerGatewayRebindFinalConfigCopyDriver{}
	if _, err := nilDriver.finalConfigVolumeInventory(context.Background(), intent, stage, stageBytes, activeBytes); err == nil {
		t.Fatal("nil manager accepted by inventory")
	}
	if err := nilDriver.copyFinalConfig(context.Background(), intent, stage, activeBytes); err == nil {
		t.Fatal("nil manager accepted by copy")
	}
}

func TestGatewayRebindFinalConfigCopyDriverCopiesOnlyActiveAndPreservesCallerBytes(t *testing.T) {
	intent, stage, _, active := finalConfigCopyDriverFixture(t)
	want := append([]byte(nil), active...)
	manager, _ := newManagerFixture(t, false)
	runner := &finalConfigDriverRunner{}
	var copiedPath string
	runner.onRun = func(request runtimeprocess.CommandRequest) {
		if len(request.Args) != 4 || request.Args[0] != "container" || request.Args[1] != "cp" || request.Args[3] != stage.StageContainer.ID+":/config/active.json" {
			t.Fatalf("unexpected config mutation: %#v", request.Args)
		}
		copiedPath = request.Args[2]
		body, err := os.ReadFile(copiedPath)
		if err != nil || !bytes.Equal(body, want) {
			t.Fatalf("copied contents differ: %v", err)
		}
	}
	manager.runner = runner
	driver := managerGatewayRebindFinalConfigCopyDriver{manager: manager}
	for _, runErr := range []error{nil, errors.New("opaque-copy-error")} {
		runner.err = runErr
		err := driver.copyFinalConfig(context.Background(), intent, stage, active)
		if (err != nil) != (runErr != nil) {
			t.Fatalf("copy error=%v runner error=%v", err, runErr)
		}
		if err != nil && strings.Contains(err.Error(), "opaque-copy-error") {
			t.Fatal("opaque copy error escaped")
		}
		if !bytes.Equal(active, want) {
			t.Fatal("copy cleared caller bytes")
		}
		if _, err := os.Stat(copiedPath); !os.IsNotExist(err) {
			t.Fatalf("temporary config retained: %v", err)
		}
	}
	if len(runner.requests) != 2 {
		t.Fatalf("copy request count=%d", len(runner.requests))
	}
}

func finalConfigCopyDriverFixture(t *testing.T) (gatewayRebindProtectedIntent, gatewayRebindStageIntent, []byte, []byte) {
	t.Helper()
	intent, predecessor, heads, _ := gatewayRebindFinalConfigLoopbackFixture(t)
	records := gatewayRebindFinalConfigProgressRecords(t, intent)
	binding, err := gatewayRebindFinalConfigIntentBindingFor(intent, predecessor, heads, records[9])
	if err != nil {
		t.Fatal(err)
	}
	record, err := newGatewayRebindFinalConfigIntentProgress(intent, records[9], binding, gatewayRebindProgressTimestamp(11))
	if err != nil {
		t.Fatal(err)
	}
	stageBytes, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		t.Fatal(err)
	}
	activeBytes, err := gatewayRebindFinalConfigBytes(intent, binding.RoutePlan)
	if err != nil {
		t.Fatal(err)
	}
	return intent, *record.Stage, stageBytes, activeBytes
}

func finalConfigDriverArchive(t *testing.T, stage, active []byte, names ...string) []byte {
	t.Helper()
	headers := []tar.Header{{Name: ".", Typeflag: tar.TypeDir}}
	var contents []byte
	for _, name := range names {
		body := stage
		if strings.TrimPrefix(name, "./") == "active.json" {
			body = active
		}
		headers = append(headers, tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(body))})
		contents = append(contents, body...)
	}
	return gatewayRebindStageConfigCopyHeadersTar(t, headers, contents)
}

func finalConfigDriverSetTarType(header []byte, kind byte) {
	header[156] = kind
	for index := 148; index < 156; index++ {
		header[index] = ' '
	}
	var checksum int
	for _, value := range header {
		checksum += int(value)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", checksum))
}

func finalConfigDriverZeroed(value []byte) bool {
	for _, octet := range value {
		if octet != 0 {
			return false
		}
	}
	return true
}

func finalConfigDriverCloneStage(t *testing.T, stage gatewayRebindStageIntent) gatewayRebindStageIntent {
	t.Helper()
	body, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	var cloned gatewayRebindStageIntent
	if err := json.Unmarshal(body, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func canceledFinalConfigDriverContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
