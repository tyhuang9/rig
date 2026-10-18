package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	composeLiveCommandUnknown = "unknown"
	composeLiveCommandConfig  = "config"
	composeLiveCommandUp      = "up"
)

var composeLiveRunToken = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

// composeLiveManifest deliberately retains only the immutable ownership facts
// that both the test and the CI cleanup step need. It must never carry a
// rendered Compose model, environment, source path, or secret.
type composeLiveManifest struct {
	Version     int    `json:"version"`
	RunToken    string `json:"runToken"`
	Case        string `json:"case"`
	AppID       string `json:"appID"`
	Project     string `json:"project"`
	Service     string `json:"service"`
	NetworkName string `json:"networkName"`
}

type composeLiveCommandEvidence struct {
	Kind            string `json:"kind"`
	ExitCode        int    `json:"exitCode,omitempty"`
	Exited          bool   `json:"exited"`
	TimedOut        bool   `json:"timedOut"`
	OutputTruncated bool   `json:"outputTruncated"`
}

func composeLiveClassifyCommand(args []string) string {
	if len(args) == 13 && args[0] == "compose" && args[1] == "--project-name" && args[2] != "" &&
		args[3] == "--project-directory" && args[4] != "" && args[5] == "--env-file" && args[6] != "" &&
		args[7] == "-f" && args[8] != "" && args[9] == "config" && args[10] == "--format" &&
		args[11] == "json" && args[12] == "--no-env-resolution" {
		return composeLiveCommandConfig
	}
	if len(args) == 14 && args[0] == "compose" && args[1] == "--project-name" && args[2] != "" &&
		args[3] == "--project-directory" && args[4] != "" && args[5] == "--env-file" && args[6] != "" &&
		args[7] == "-f" && args[8] != "" && args[9] == "up" && args[10] == "-d" &&
		args[11] == "--wait" && args[12] == "--wait-timeout" && args[13] != "" {
		return composeLiveCommandUp
	}
	return composeLiveCommandUnknown
}

func (e composeLiveCommandEvidence) marshal() ([]byte, error) {
	if e.Kind != composeLiveCommandConfig && e.Kind != composeLiveCommandUp && e.Kind != composeLiveCommandUnknown {
		return nil, errors.New("unknown compose command evidence kind")
	}
	return json.Marshal(e)
}

// composeLiveObservedEvidence is the sole conversion from an observed process
// request/result into uploadable evidence. The inputs which could contain
// protected paths, environment entries, or process output are deliberately
// consumed only to enforce the observed command shape; none are serialized.
func composeLiveObservedEvidence(kind string, args, environment []string, stdout, stderr []byte, evidence composeLiveCommandEvidence) ([]byte, error) {
	if kind != composeLiveClassifyCommand(args) || kind == composeLiveCommandUnknown {
		return nil, errors.New("invalid compose live command observation")
	}
	// Deliberately consume untrusted process details without retaining them.
	_ = environment
	_ = stdout
	_ = stderr
	evidence.Kind = kind
	return evidence.marshal()
}

func composeLiveValidateManifest(value composeLiveManifest) error {
	if value.Version != 1 || !composeLiveRunToken.MatchString(value.RunToken) ||
		!regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`).MatchString(value.Case) ||
		!regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(value.AppID) ||
		value.Project != "rig-"+strings.ReplaceAll(value.AppID, "-", "") ||
		value.Service != "app" || value.NetworkName != value.Project+"_default" {
		return errors.New("invalid compose live ownership manifest")
	}
	return nil
}

func composeLiveWriteManifest(evidenceDirectory string, value composeLiveManifest) (string, error) {
	if err := composeLiveValidateManifest(value); err != nil {
		return "", err
	}
	directory, err := filepath.Abs(evidenceDirectory)
	if err != nil || filepath.Clean(directory) != directory {
		return "", errors.New("compose live evidence directory is invalid")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("compose live evidence directory is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return "", errors.New("compose live evidence directory resolves through a link")
	}
	name := "manifest-" + strings.ToLower(value.Case) + ".json"
	path := filepath.Join(directory, name)
	if filepath.Dir(path) != directory {
		return "", errors.New("compose live manifest escaped evidence directory")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	defer clear(body)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("create compose live ownership manifest")
	}
	if _, err = file.Write(append(body, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return path, nil
}

func TestHostdComposeLiveObserverClassification(t *testing.T) {
	config := []string{"compose", "--project-name", "rig-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--project-directory", "/workspace", "--env-file", "/protected/env", "-f", "/workspace/compose.yaml", "config", "--format", "json", "--no-env-resolution"}
	up := []string{"compose", "--project-name", "rig-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--project-directory", "/workspace", "--env-file", "/protected/env", "-f", "/protected/effective.json", "up", "-d", "--wait", "--wait-timeout", "30"}
	if got := composeLiveClassifyCommand(config); got != composeLiveCommandConfig {
		t.Fatalf("config classification=%q", got)
	}
	if got := composeLiveClassifyCommand(up); got != composeLiveCommandUp {
		t.Fatalf("up classification=%q", got)
	}
	if got := composeLiveClassifyCommand([]string{"container", "exec", "fixture", "echo", "up"}); got != composeLiveCommandUnknown {
		t.Fatalf("decoy classification=%q", got)
	}
}

func TestHostdComposeLiveEvidenceRedaction(t *testing.T) {
	secret := "compose-live-test-secret"
	path := "/private/fixture/compose.yaml"
	args := []string{"compose", "--project-name", "rig-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--project-directory", "/private/workspace", "--env-file", "/private/runtime.env", "-f", path, "up", "-d", "--wait", "--wait-timeout", "30"}
	body, err := composeLiveObservedEvidence(composeLiveCommandUp, args, []string{"COMPOSE_LIVE_SECRET=" + secret}, []byte("stdout "+secret), []byte("stderr "+secret), composeLiveCommandEvidence{Exited: true, ExitCode: 1})
	if err != nil {
		t.Fatal("convert sanitized command evidence")
	}
	defer clear(body)
	encoded := string(body)
	if strings.Contains(encoded, secret) || strings.Contains(encoded, path) || strings.Contains(encoded, "stdout") || strings.Contains(encoded, "stderr") || strings.Contains(encoded, "env") {
		t.Fatal("sanitized command evidence serialized protected data")
	}
	if _, err := composeLiveObservedEvidence("nonfinite", args, []string{"COMPOSE_LIVE_SECRET=" + secret}, nil, nil, composeLiveCommandEvidence{}); err == nil {
		t.Fatal("nonfinite command kind was accepted")
	}
}

func TestHostdComposeLiveOwnershipManifest(t *testing.T) {
	directory := t.TempDir()
	value := composeLiveManifest{
		Version: 1, RunToken: "123-4", Case: "Success",
		AppID:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Project: "rig-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Service: "app",
		NetworkName: "rig-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_default",
	}
	path, err := composeLiveWriteManifest(directory, value)
	if err != nil {
		t.Fatal("write ownership manifest")
	}
	if _, err := composeLiveWriteManifest(directory, value); err == nil {
		t.Fatal("manifest reuse was accepted")
	}
	for _, invalid := range []composeLiveManifest{
		{Version: 1, RunToken: "bad", Case: value.Case, AppID: value.AppID, Project: value.Project, Service: value.Service, NetworkName: value.NetworkName},
		{Version: 1, RunToken: value.RunToken, Case: "bad-case", AppID: value.AppID, Project: value.Project, Service: value.Service, NetworkName: value.NetworkName},
		{Version: 1, RunToken: value.RunToken, Case: value.Case, AppID: value.AppID, Project: "rig-unrelated", Service: value.Service, NetworkName: value.NetworkName},
		{Version: 1, RunToken: value.RunToken, Case: value.Case, AppID: value.AppID, Project: value.Project, Service: "other", NetworkName: value.NetworkName},
		{Version: 1, RunToken: value.RunToken, Case: value.Case, AppID: value.AppID, Project: value.Project, Service: value.Service, NetworkName: "rig-unrelated_default"},
	} {
		if err := composeLiveValidateManifest(invalid); err == nil {
			t.Fatal("invalid ownership manifest was accepted")
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read ownership manifest")
	}
	defer clear(body)
	var found composeLiveManifest
	if err := json.Unmarshal(body, &found); err != nil || found != value {
		t.Fatalf("manifest decode valid=%t", err == nil)
	}
	linked := filepath.Join(t.TempDir(), "linked-evidence")
	if err := os.Symlink(directory, linked); err == nil {
		if _, err := composeLiveWriteManifest(linked, value); err == nil {
			t.Fatal("symlinked evidence directory was accepted")
		}
	}
}
