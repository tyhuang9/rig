package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This control is intentionally outside the live_docker build tag. It makes a
// private Manager call or handcrafted controller mux an invalid replacement
// for the supported child process, even on hosts that cannot execute Docker.
func TestGatewayRebindLiveHarnessUsesSupportedDaemonAssembly(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	process := readGatewayRebindHarnessSource(t, root, "cmd/hostd/gateway_rebind_process_live_test.go")
	matrix := readGatewayRebindHarnessSource(t, root, "cmd/hostd/gateway_rebind_matrix_live_test.go")
	scenarios := readGatewayRebindHarnessSource(t, root, "cmd/hostd/gateway_rebind_scenarios_live_test.go")
	fixture := readGatewayRebindHarnessSource(t, root, "cmd/hostd/gateway_rebind_fixture_live_test.go")
	supported := strings.Join([]string{process, matrix, scenarios}, "\n")
	// gofmt aligns adjacent assembly fields; whitespace does not change wiring.
	supported = strings.Join(strings.Fields(supported), " ")

	for _, required := range []string{
		"exec.Command(os.Args[0]", "runServerWithAssembly(", "hostdDaemonAssemblyOptions{",
		"runner: runner", "ready: func(value hostdDaemonReady)",
		"http.MethodPost", "/api/v1/system/lan-gateway-rebind/proposal",
		"/api/v1/system/lan-gateway-rebind\"", "auth.SessionCookie", "X-CSRF-Token",
		"Process.Kill()", ".Wait()",
	} {
		if !strings.Contains(supported, required) {
			t.Errorf("supported full-hostd process harness lost %q", required)
		}
	}
	for _, forbidden := range []string{
		"httptest.NewServer", "controller.Server{", "generatedingress.New(",
		"CommitGatewayRebind(", "RecoverGatewayRebind(",
	} {
		if strings.Contains(process, forbidden) || strings.Contains(matrix, forbidden) ||
			strings.Contains(scenarios, forbidden) {
			t.Errorf("full-hostd acceptance harness contains forbidden private substitute %q", forbidden)
		}
	}
	for _, required := range []string{
		"for _, entry := range gatewayRebindLiveBoundaries",
		"for _, entry := range gatewayRebindLiveScenarios",
		"liveHostdRebindExecutableDigest(t)",
	} {
		if !strings.Contains(matrix, required) {
			t.Errorf("closed full-hostd matrix lost %q", required)
		}
	}
	for _, required := range []string{
		"database.OpenForGeneratedDomain(", "prepareRuntimeCompositionForTest(", "liveHostdRebindDeployLocal(",
		"SourcePath: source",
		"lanTwoAppGrant(", "runtimeprocess.ExecRunner{}", "fixture.cleanup(t)",
	} {
		if !strings.Contains(fixture, required) {
			t.Errorf("real fixture lost %q", required)
		}
	}
}

func readGatewayRebindHarnessSource(t *testing.T, root, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
