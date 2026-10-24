//go:build live_docker

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestLiveHostdRebindClosedDiagnosticControls(t *testing.T) {
	const secret = "token=do-not-disclose"
	if mode := os.Getenv("RIG_LIVE_CHILD_DIAGNOSTIC_CONTROL"); mode != "" {
		_, _ = os.Stdout.WriteString("/private/path " + secret + "\n")
		construction := fmt.Errorf("controlled private cause /private/path %s: %w", secret,
			errors.New("generated domain construction was refused: stage=empty_final_locked reason=docker_static_response"))
		switch mode {
		case "controller_restart":
			controllerChildStartupFailWithConstruction(t, controllerChildStartupControllerRestart,
				controllerChildStartupGeneratedDomain, construction, "controlled child failure /private/path "+secret)
		case "process_kill":
			controllerChildStartupFailWithConstruction(t, controllerChildStartupProcessKill,
				controllerChildStartupRuntimeComposition, construction, "controlled child failure /private/path "+secret)
		default:
			t.Fatal("unknown child diagnostic control")
		}
		return
	}
	knownRuntime := `{"time":"2026-10-15T00:00:00Z","level":"ERROR","msg":"runtime composition failed","error":"` + secret + `"}`
	knownGateway := `{"msg":"gateway startup inspection failed","error":"/private/path ` + secret + `"}`
	knownGatewayCheckpoint := `{"msg":"gateway startup inspection failed","startup_checkpoint":"upgrade_gateway_lock","error":"/private/path ` + secret + `"}`
	unknownGatewayCheckpoint := `{"msg":"gateway startup inspection failed","startup_checkpoint":"forged-checkpoint","error":"/private/path ` + secret + `"}`
	knownSnapshotCause := `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","startup_cause":"sqlite_locked","error":"/private/path ` + secret + `"}`
	unknownSnapshotCause := `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","startup_cause":"forged-cause","error":"/private/path ` + secret + `"}`
	secondary := `{"msg":"owned gateway emergency stop could not be verified","error":"` + secret + `"}`
	cases := []struct {
		name      string
		stderr    string
		wantCode  string
		wantEmpty bool
	}{
		{name: "known exact message", stderr: knownRuntime, wantCode: "runtime_composition"},
		{name: "primary precedes emergency stop", stderr: knownGateway + "\n" + secondary, wantCode: "gateway_inspection"},
		{name: "known gateway checkpoint", stderr: knownGatewayCheckpoint, wantCode: "upgrade_gateway_lock"},
		{name: "known snapshot stage and cause", stderr: knownSnapshotCause, wantCode: "startup_snapshot_grants cause=sqlite_locked"},
		{name: "unknown snapshot cause keeps stage", stderr: unknownSnapshotCause, wantCode: "startup_snapshot_grants"},
		{name: "numeric snapshot cause keeps stage", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","startup_cause":42,"error":"/private/path ` + secret + `"}`, wantCode: "startup_snapshot_grants"},
		{name: "object snapshot cause keeps stage", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","startup_cause":{"value":"sqlite_locked"},"error":"/private/path ` + secret + `"}`, wantCode: "startup_snapshot_grants"},
		{name: "array snapshot cause keeps stage", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","startup_cause":["sqlite_locked"],"error":"/private/path ` + secret + `"}`, wantCode: "startup_snapshot_grants"},
		{name: "null snapshot cause keeps stage", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","startup_cause":null,"error":"/private/path ` + secret + `"}`, wantCode: "startup_snapshot_grants"},
		{name: "snapshot cause ignored on non snapshot stage", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":"upgrade_gateway_lock","startup_cause":"sqlite_locked","error":"/private/path ` + secret + `"}`, wantCode: "upgrade_gateway_lock"},
		{name: "raw error cannot manufacture snapshot cause", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":"startup_snapshot_grants","error":"sqlite_locked ` + secret + `"}`, wantCode: "startup_snapshot_grants"},
		{name: "unknown gateway checkpoint keeps broad code", stderr: unknownGatewayCheckpoint, wantCode: "gateway_inspection"},
		{name: "numeric gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":42,"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "object gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":{"value":"upgrade_gateway_lock"},"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "array gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":["upgrade_gateway_lock"],"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "boolean gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":true,"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "null gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":null,"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "malformed gateway checkpoint retains first primary", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":42,"error":"/private/path ` + secret + `"}` + "\n" + knownRuntime, wantCode: "gateway_inspection"},
		{name: "gateway checkpoint retains first primary", stderr: knownGatewayCheckpoint + "\n" + knownRuntime, wantCode: "upgrade_gateway_lock"},
		{name: "primary follows emergency stop", stderr: secondary + "\n" + knownRuntime, wantCode: "runtime_composition"},
		{name: "first primary is retained", stderr: knownGateway + "\n" + knownRuntime, wantCode: "gateway_inspection"},
		{name: "checkpoint is ignored for another message", stderr: `{"msg":"runtime composition failed","startup_checkpoint":"upgrade_gateway_lock","error":"` + secret + `"}`, wantCode: "runtime_composition"},
		{name: "raw error cannot manufacture checkpoint", stderr: `{"msg":"gateway startup inspection failed","error":"upgrade_gateway_lock ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "unknown message", stderr: `{"msg":"custom startup failed ` + secret + `","error":"` + secret + `"}`},
		{name: "injected known message", stderr: `{"msg":"runtime composition failed; ` + secret + `","error":"` + secret + `"}`},
		{name: "malformed JSON", stderr: `{"msg":"runtime composition failed","error":"` + secret},
		{name: "empty stderr", stderr: " \n\t", wantEmpty: true},
		{
			name:   "known message after byte limit",
			stderr: strings.Repeat("x", liveHostdRebindStartupDiagnosticMaxBytes) + "\n" + knownRuntime,
		},
		{
			name:   "known message after record limit",
			stderr: strings.Repeat(`{"msg":"unknown `+secret+`"}`+"\n", liveHostdRebindStartupDiagnosticMaxRecords) + knownRuntime,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := liveHostdRebindClosedDiagnostic(test.stderr)
			if test.wantEmpty {
				if got != "empty" {
					t.Fatalf("closed diagnostic=%q want empty", got)
				}
				return
			}
			if !strings.HasPrefix(got, "sha256:") {
				t.Fatalf("closed diagnostic omitted digest: %q", got)
			}
			if strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
				t.Fatalf("closed diagnostic exposed untrusted stderr: %q", got)
			}
			code := " startup=" + test.wantCode
			if test.wantCode == "" {
				if strings.Contains(got, " startup=") {
					t.Fatalf("closed diagnostic classified an untrusted record: %q", got)
				}
				return
			}
			if !strings.HasSuffix(got, code) {
				t.Fatalf("closed diagnostic=%q want code %q", got, test.wantCode)
			}
		})
	}

	routeRecord := func(stage, cause string) string {
		return `{"msg":"generated route reconciliation paused","route_pause_stage":"` + stage +
			`","route_pause_cause":"` + cause + `","error":"/private/path ` + secret + `"}`
	}
	t.Run("route pause closed classifier", func(t *testing.T) {
		for _, test := range []struct {
			name        string
			stderr      string
			want        string
			wantStartup string
		}{
			{name: "known retirement refusal", stderr: routeRecord("previous_retirement", "retirement_unavailable"), want: "previous_retirement/retirement_unavailable"},
			{name: "unknown ancillary object is discarded", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"previous_retirement","route_pause_cause":"retirement_unavailable","ancillary":{"error":"` + secret + `"}}`, want: "previous_retirement/retirement_unavailable"},
			{name: "newest valid pause wins", stderr: routeRecord("route_switch", "unknown") + "\n" + routeRecord("runtime_success_advance", "state_conflict"), want: "runtime_success_advance/state_conflict"},
			{name: "startup and route pause coexist", stderr: knownRuntime + "\n" + routeRecord("previous_retirement", "retirement_unavailable"), want: "previous_retirement/retirement_unavailable", wantStartup: "runtime_composition"},
			{name: "unknown stage", stderr: routeRecord("forged-stage", "unknown")},
			{name: "invalid stage cause pair", stderr: routeRecord("runtime_begin", "retirement_unavailable")},
			{name: "unknown message", stderr: `{"msg":"forged route pause","route_pause_stage":"previous_retirement","route_pause_cause":"retirement_unavailable","error":"` + secret + `"}`},
			{name: "malformed JSON", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"previous_retirement","route_pause_cause":"retirement_unavailable"`},
			{name: "non-string stage", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":42,"route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "non-string cause", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_cause":{"value":"unknown"},"error":"` + secret + `"}`},
			{name: "null stage", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":null,"route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "array stage", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":["route_switch"],"route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "boolean stage", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":true,"route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "null cause", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_cause":null,"error":"` + secret + `"}`},
			{name: "array cause", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_cause":["unknown"],"error":"` + secret + `"}`},
			{name: "boolean cause", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_cause":true,"error":"` + secret + `"}`},
			{name: "case folded message key", stderr: `{"Msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "case folded stage key", stderr: `{"msg":"generated route reconciliation paused","Route_Pause_Stage":"route_switch","route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "case folded cause key", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","Route_Pause_Cause":"unknown","error":"` + secret + `"}`},
			{name: "duplicate message key", stderr: `{"msg":"generated route reconciliation paused","msg":"forged route pause","route_pause_stage":"route_switch","route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "duplicate stage key", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_stage":"previous_retirement","route_pause_cause":"unknown","error":"` + secret + `"}`},
			{name: "duplicate cause key", stderr: `{"msg":"generated route reconciliation paused","route_pause_stage":"route_switch","route_pause_cause":"unknown","route_pause_cause":"gateway_readiness_failed","error":"` + secret + `"}`},
			{name: "error cannot manufacture pair", stderr: `{"msg":"generated route reconciliation paused","error":"previous_retirement retirement_unavailable ` + secret + `"}`},
			{name: "known pause in bounded suffix", stderr: strings.Repeat("x", liveHostdRebindRoutePauseMaxBytes) + "\n" + routeRecord("route_switch", "gateway_readiness_failed"), want: "route_switch/gateway_readiness_failed"},
			{name: "known pause before byte bound", stderr: routeRecord("previous_retirement", "retirement_unavailable") + "\n" + strings.Repeat("x", liveHostdRebindRoutePauseMaxBytes)},
			{name: "known pause before record bound", stderr: routeRecord("previous_retirement", "retirement_unavailable") + "\n" + strings.Repeat(`{"msg":"untrusted `+secret+`"}`+"\n", liveHostdRebindRoutePauseMaxRecords)},
			{name: "known pause after older record bound", stderr: strings.Repeat(`{"msg":"untrusted `+secret+`"}`+"\n", liveHostdRebindRoutePauseMaxRecords) + routeRecord("runtime_success_head", "active_head_mismatch"), want: "runtime_success_head/active_head_mismatch"},
		} {
			t.Run(test.name, func(t *testing.T) {
				got := liveHostdRebindClosedDiagnostic(test.stderr)
				if !strings.HasPrefix(got, "sha256:") || strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
					t.Fatalf("closed diagnostic exposed untrusted stderr: %q", got)
				}
				if test.want == "" {
					if strings.Contains(got, " route_pause=") {
						t.Fatalf("closed diagnostic classified an untrusted route pause: %q", got)
					}
				} else if !strings.HasSuffix(got, " route_pause="+test.want) {
					t.Fatalf("closed diagnostic=%q want route pause %q", got, test.want)
				}
				if test.wantStartup != "" && !strings.Contains(got, " startup="+test.wantStartup+" ") {
					t.Fatalf("closed diagnostic=%q want startup %q", got, test.wantStartup)
				}
			})
		}
	})

	t.Run("route pause pair roster", func(t *testing.T) {
		pairs := map[string][]string{
			"runtime_begin":           {"unknown", "deployment_in_progress"},
			"runtime_success_head":    {"unknown", "active_head_mismatch", "canceled", "deadline_exceeded", "state_not_found", "invalid_state", "invalid_transition", "state_conflict"},
			"route_switch":            {"unknown", "gateway_readiness_failed", "canceled", "deadline_exceeded", "state_not_found", "invalid_state", "invalid_transition", "state_conflict"},
			"previous_retirement":     {"unknown", "retirement_unavailable", "candidate_cleanup_failed", "canceled", "deadline_exceeded", "state_not_found", "invalid_state", "invalid_transition", "state_conflict"},
			"runtime_success_advance": {"unknown", "canceled", "deadline_exceeded", "state_not_found", "invalid_state", "invalid_transition", "state_conflict"},
		}
		for stage, causes := range pairs {
			for _, cause := range causes {
				if got := liveHostdRebindRoutePauseDiagnosticCode(routeRecord(stage, cause)); got != stage+"/"+cause {
					t.Fatalf("route pause classifier=%q want %s/%s", got, stage, cause)
				}
			}
		}
	})

	t.Run("process kill capture waits for reap", func(t *testing.T) {
		command := &exec.Cmd{}
		child := controllerProcessKillCapture(command)
		if command.Stderr != &child.stderr {
			t.Fatal("process-kill child stderr is not privately captured")
		}
		if _, err := command.Stderr.Write([]byte(routeRecord("previous_retirement", "retirement_unavailable"))); err != nil {
			t.Fatal("write simulated process-kill child stderr")
		}
		if got, ok := controllerProcessKillClosedDiagnostic(child); ok || got != "" {
			t.Fatalf("process-kill diagnostic read before reap: %q", got)
		}
		close(child.done)
		got, ok := controllerProcessKillClosedDiagnostic(child)
		if !ok || !strings.HasPrefix(got, "sha256:") ||
			!strings.HasSuffix(got, " route_pause=previous_retirement/retirement_unavailable") {
			t.Fatalf("closed process-kill diagnostic=%q", got)
		}
		if strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
			t.Fatalf("closed process-kill diagnostic exposed untrusted stderr: %q", got)
		}
	})

	t.Run("child startup projection is closed", func(t *testing.T) {
		record := func(kind, stage, result string) string {
			return `{"msg":"controller child startup failed","child_kind":"` + kind +
				`","child_stage":"` + stage + `","child_result":"` + result + `"}`
		}
		constructionRecord := func(kind, stage, constructionStage, constructionReason string) string {
			return `{"msg":"controller child startup failed","child_kind":"` + kind +
				`","child_stage":"` + stage + `","child_result":"failed","construction_stage":"` +
				constructionStage + `","construction_reason":"` + constructionReason + `"}`
		}
		for _, test := range []struct {
			name, value, want string
		}{
			{name: "controller generated domain", value: record("controller_restart", "generated_domain", "failed"), want: "controller_restart/generated_domain/failed"},
			{name: "controller generated domain construction", value: constructionRecord("controller_restart", "generated_domain", "empty_final_locked", "docker_static_response"), want: "controller_restart/generated_domain/failed construction=empty_final_locked/docker_static_response"},
			{name: "process kill runtime construction", value: constructionRecord("process_kill", "runtime_composition", "startup_observation", "docker_network"), want: "process_kill/runtime_composition/failed construction=startup_observation/docker_network"},
			{name: "controller reservation input", value: constructionRecord("controller_restart", "generated_domain", "initial_classification", "input"), want: "controller_restart/generated_domain/failed construction=initial_classification/input"},
			{name: "controller stable construction", value: constructionRecord("controller_restart", "generated_domain", "stable_construction", "recovery"), want: "controller_restart/generated_domain/failed construction=stable_construction/recovery"},
			{name: "process kill empty construction", value: constructionRecord("process_kill", "runtime_composition", "empty_construction", "recovery"), want: "process_kill/runtime_composition/failed construction=empty_construction/recovery"},
			{name: "controller cold accounting", value: constructionRecord("controller_restart", "generated_domain", "cold_construction", "cold_accounting"), want: "controller_restart/generated_domain/failed construction=cold_construction/cold_accounting"},
			{name: "controller cold group reconstruction", value: constructionRecord("controller_restart", "generated_domain", "cold_construction", "cold_group_reconstruction"), want: "controller_restart/generated_domain/failed construction=cold_construction/cold_group_reconstruction"},
			{name: "process kill cold builder attestation", value: constructionRecord("process_kill", "runtime_composition", "cold_construction", "cold_builder_attestation"), want: "process_kill/runtime_composition/failed construction=cold_construction/cold_builder_attestation"},
			{name: "controller cold reservation", value: constructionRecord("controller_restart", "generated_domain", "cold_construction", "cold_reservation"), want: "controller_restart/generated_domain/failed construction=cold_construction/cold_reservation"},
			{name: "process kill cold ownership", value: constructionRecord("process_kill", "runtime_composition", "cold_construction", "cold_ownership"), want: "process_kill/runtime_composition/failed construction=cold_construction/cold_ownership"},
			{name: "controller cold head", value: constructionRecord("controller_restart", "generated_domain", "cold_construction", "cold_head"), want: "controller_restart/generated_domain/failed construction=cold_construction/cold_head"},
			{name: "process kill cold group attestation", value: constructionRecord("process_kill", "runtime_composition", "cold_construction", "cold_group_attestation"), want: "process_kill/runtime_composition/failed construction=cold_construction/cold_group_attestation"},
			{name: "controller cold manifest", value: constructionRecord("controller_restart", "generated_domain", "cold_construction", "cold_manifest"), want: "controller_restart/generated_domain/failed construction=cold_construction/cold_manifest"},
			{name: "process kill manifest fallback", value: constructionRecord("process_kill", "runtime_composition", "manifest", "manifest"), want: "process_kill/runtime_composition/failed construction=manifest/manifest"},
			{name: "process kill sealed database", value: record("process_kill", "sealed_database", "failed"), want: "process_kill/sealed_database/failed"},
			{name: "newest exact record wins", value: record("process_kill", "owner_lease", "failed") + "\n" + record("controller_restart", "runtime_composition", "failed"), want: "controller_restart/runtime_composition/failed"},
			{name: "unknown kind", value: record("forged", "generated_domain", "failed")},
			{name: "crossed stage", value: record("controller_restart", "local_machine", "failed")},
			{name: "unknown result", value: record("controller_restart", "generated_domain", "retrying")},
			{name: "unknown field", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":"generated_domain","child_result":"failed","error":"/private/path ` + secret + `"}`},
			{name: "duplicate stage", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":"generated_domain","child_stage":"manifest","child_result":"failed"}`},
			{name: "case folded stage", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","Child_Stage":"generated_domain","child_result":"failed"}`},
			{name: "non string stage", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":{"value":"generated_domain"},"child_result":"failed"}`},
			{name: "construction on ineligible child stage", value: constructionRecord("process_kill", "sealed_database", "startup_observation", "database")},
			{name: "construction missing reason", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":"generated_domain","child_result":"failed","construction_stage":"stable_final"}`},
			{name: "construction missing stage", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":"generated_domain","child_result":"failed","construction_reason":"state_store"}`},
			{name: "construction crossed pair", value: constructionRecord("controller_restart", "generated_domain", "join", "docker_network")},
			{name: "construction crossed cold pair", value: constructionRecord("controller_restart", "generated_domain", "cold_construction", "docker_network")},
			{name: "construction unknown reason", value: constructionRecord("controller_restart", "generated_domain", "stable_final", "private_path")},
			{name: "construction duplicate stage", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":"generated_domain","child_result":"failed","construction_stage":"stable_final","construction_stage":"join","construction_reason":"state_store"}`},
			{name: "construction case folded reason", value: `{"msg":"controller child startup failed","child_kind":"controller_restart","child_stage":"generated_domain","child_result":"failed","construction_stage":"stable_final","Construction_Reason":"state_store"}`},
			{name: "raw text cannot manufacture", value: `controller_restart/generated_domain/failed /private/path ` + secret},
			{name: "exact record in bounded suffix", value: strings.Repeat("x", controllerChildStartupDiagnosticMaxBytes) + "\n" + record("controller_restart", "generated_domain", "failed"), want: "controller_restart/generated_domain/failed"},
			{name: "exact record before byte bound", value: record("controller_restart", "generated_domain", "failed") + "\n" + strings.Repeat("x", controllerChildStartupDiagnosticMaxBytes)},
			{name: "exact record after older record bound", value: strings.Repeat(`{"msg":"untrusted"}`+"\n", controllerChildStartupDiagnosticMaxRecords) + record("process_kill", "worker_recovery", "failed"), want: "process_kill/worker_recovery/failed"},
			{name: "exact record before record bound", value: record("process_kill", "worker_recovery", "failed") + "\n" + strings.Repeat(`{"msg":"untrusted"}`+"\n", controllerChildStartupDiagnosticMaxRecords)},
		} {
			t.Run(test.name, func(t *testing.T) {
				if got := controllerChildStartupDiagnosticCode(test.value); got != test.want {
					t.Fatalf("child startup diagnostic=%q want %q", got, test.want)
				}
			})
		}

		t.Run("construction error traversal is closed and bounded", func(t *testing.T) {
			exact := errors.New("generated domain construction was refused: stage=stable_final reason=state_store")
			wrapped := fmt.Errorf("private /path %s: %w", secret, exact)
			stage, reason, ok := controllerChildConstructionDiagnostic(wrapped)
			if !ok || stage != "stable_final" || reason != "state_store" {
				t.Fatalf("closed construction diagnostic=%q/%q ok=%t", stage, reason, ok)
			}
			for _, rejected := range []error{
				errors.New("private prefix: generated domain construction was refused: stage=stable_final reason=state_store"),
				errors.New("generated domain construction was refused: stage=join reason=docker_network"),
				errors.New("generated domain construction was refused: stage=stable_final reason=state_store /private/path " + secret),
			} {
				if stage, reason, ok := controllerChildConstructionDiagnostic(rejected); ok || stage != "" || reason != "" {
					t.Fatalf("untrusted construction diagnostic=%q/%q ok=%t", stage, reason, ok)
				}
			}
			cycle := &controllerChildConstructionDiagnosticCycle{message: "/private/path " + secret}
			cycle.next = cycle
			if stage, reason, ok := controllerChildConstructionDiagnostic(cycle); ok || stage != "" || reason != "" {
				t.Fatalf("cyclic construction diagnostic=%q/%q ok=%t", stage, reason, ok)
			}
			beyondBound := error(exact)
			for index := 0; index < controllerChildConstructionMaxErrors; index++ {
				beyondBound = fmt.Errorf("private wrapper %d: %w", index, beyondBound)
			}
			if stage, reason, ok := controllerChildConstructionDiagnostic(beyondBound); ok || stage != "" || reason != "" {
				t.Fatalf("out-of-bound construction diagnostic=%q/%q ok=%t", stage, reason, ok)
			}
			wide := make([]error, controllerChildConstructionMaxErrors+1)
			for index := range wide[:len(wide)-1] {
				wide[index] = fmt.Errorf("private wide error %d /private/path %s", index, secret)
			}
			wide[len(wide)-1] = exact
			if stage, reason, ok := controllerChildConstructionDiagnostic(errors.Join(wide...)); ok || stage != "" || reason != "" {
				t.Fatalf("over-wide construction diagnostic=%q/%q ok=%t", stage, reason, ok)
			}
		})
		roster := map[string][]controllerChildStartupStage{
			"controller_restart": {
				controllerChildStartupManifest, controllerChildStartupDirectories, controllerChildStartupOwnerLease,
				controllerChildStartupSealedDatabase, controllerChildStartupSourceMaterializer, controllerChildStartupSourceRecovery,
				controllerChildStartupConfiguration, controllerChildStartupConfigurationRecovery,
				controllerChildStartupDeploymentPlans, controllerChildStartupPlanRecovery, controllerChildStartupDeploymentJobs,
				controllerChildStartupGeneratedDomain, controllerChildStartupRuntimeComposition,
				controllerChildStartupCapacityPressure, controllerChildStartupWorkerRecovery,
				controllerChildStartupAPIListener, controllerChildStartupReadyProtocol,
			},
			"process_kill": {
				controllerChildStartupManifest, controllerChildStartupAddress, controllerChildStartupLocalDocker,
				controllerChildStartupDirectories, controllerChildStartupOwnerLease, controllerChildStartupSealedDatabase,
				controllerChildStartupSourceMaterializer, controllerChildStartupSourceRecovery,
				controllerChildStartupConfiguration, controllerChildStartupConfigurationRecovery,
				controllerChildStartupDeploymentPlans, controllerChildStartupPlanRecovery,
				controllerChildStartupDeploymentJobs, controllerChildStartupLocalMachine,
				controllerChildStartupRuntimeComposition, controllerChildStartupWorkerRecovery,
				controllerChildStartupAPIListener, controllerChildStartupReadyProtocol,
			},
		}
		for kind, stages := range roster {
			for _, stage := range stages {
				name := kind + "/" + string(stage)
				t.Run(name, func(t *testing.T) {
					if got := controllerChildStartupDiagnosticCode(record(kind, string(stage), "failed")); got != name+"/failed" {
						t.Fatalf("child startup roster diagnostic=%q want %q", got, name+"/failed")
					}
				})
			}
		}
	})

	t.Run("actual failing child exposes only closed stage", func(t *testing.T) {
		for _, mode := range []string{"controller_restart", "process_kill"} {
			t.Run(mode, func(t *testing.T) {
				command := exec.Command(os.Args[0], "-test.run=^TestLiveHostdRebindClosedDiagnosticControls$", "-test.timeout=30s")
				for _, value := range os.Environ() {
					if !strings.HasPrefix(value, "RIG_LIVE_CHILD_DIAGNOSTIC_CONTROL=") {
						command.Env = append(command.Env, value)
					}
				}
				command.Env = append(command.Env, "RIG_LIVE_CHILD_DIAGNOSTIC_CONTROL="+mode)
				child := controllerProcessKillCapture(command)
				if command.Stdout != nil {
					t.Fatal("child diagnostic control captured arbitrary stdout")
				}
				child.err = command.Run()
				close(child.done)
				if child.err == nil {
					t.Fatal("controlled failing child unexpectedly succeeded")
				}
				if strings.Contains(child.stderr.String(), secret) || strings.Contains(child.stderr.String(), "/private/path") {
					t.Fatal("child stderr exposed the raw controlled failure")
				}
				got, ok := controllerProcessKillClosedDiagnostic(child)
				want := " child=" + mode + "/"
				if mode == "controller_restart" {
					want += "generated_domain/failed"
				} else {
					want += "runtime_composition/failed"
				}
				want += " construction=empty_final_locked/docker_static_response"
				if !ok || !strings.HasPrefix(got, "sha256:") || !strings.HasSuffix(got, want) {
					t.Fatalf("actual child closed diagnostic=%q want suffix %q", got, want)
				}
				if strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
					t.Fatalf("actual child diagnostic exposed private output: %q", got)
				}
			})
		}
	})

}

type controllerChildConstructionDiagnosticCycle struct {
	message string
	next    error
}

func (e *controllerChildConstructionDiagnosticCycle) Error() string { return e.message }
func (e *controllerChildConstructionDiagnosticCycle) Unwrap() error { return e.next }

func TestLiveHostdRebindClosedDiagnosticCheckpointRoster(t *testing.T) {
	const secret = "token=do-not-disclose"
	for _, checkpoint := range gatewayStartupDiagnosticExpectedCheckpoints {
		t.Run(string(checkpoint), func(t *testing.T) {
			stderr := `{"msg":"gateway startup inspection failed","startup_checkpoint":"` + string(checkpoint) +
				`","error":"/private/path ` + secret + `"}`
			got := liveHostdRebindClosedDiagnostic(stderr)
			if !strings.HasPrefix(got, "sha256:") || strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
				t.Fatalf("closed diagnostic exposed untrusted stderr: %q", got)
			}
			if want := " startup=" + string(checkpoint); !strings.HasSuffix(got, want) {
				t.Fatalf("closed diagnostic=%q want code %q", got, checkpoint)
			}
		})
	}
}

func TestLiveHostdRebindClosedDiagnosticSnapshotCauseRoster(t *testing.T) {
	const secret = "token=do-not-disclose"
	cases := []struct {
		checkpoint gatewayStartupDiagnosticCheckpoint
		cause      gatewayStartupDiagnosticCause
	}{
		{gatewayStartupDiagnosticStartupSnapshotBegin, gatewayStartupDiagnosticCauseInvalidStoredState},
		{gatewayStartupDiagnosticStartupSnapshotUpgrade, gatewayStartupDiagnosticCauseCanceled},
		{gatewayStartupDiagnosticStartupSnapshotGrants, gatewayStartupDiagnosticCauseDeadlineExceeded},
		{gatewayStartupDiagnosticStartupSnapshotDisables, gatewayStartupDiagnosticCauseSQLiteBusy},
		{gatewayStartupDiagnosticStartupSnapshotRebind, gatewayStartupDiagnosticCauseSQLiteLocked},
		{gatewayStartupDiagnosticStartupSnapshotRuntimeHeads, gatewayStartupDiagnosticCauseSQLiteError},
		{gatewayStartupDiagnosticStartupSnapshotRuntimeComponents, gatewayStartupDiagnosticCauseSQLiteCorrupt},
		{gatewayStartupDiagnosticStartupSnapshotApprovalAuthority, gatewayStartupDiagnosticCauseSQLiteIO},
		{gatewayStartupDiagnosticStartupSnapshotCommit, gatewayStartupDiagnosticCauseSQLiteOther},
		{gatewayStartupDiagnosticStartupSnapshotBegin, gatewayStartupDiagnosticCauseUnknown},
	}
	for _, test := range cases {
		name := string(test.checkpoint) + "/" + string(test.cause)
		t.Run(name, func(t *testing.T) {
			stderr := `{"msg":"gateway startup inspection failed","startup_checkpoint":"` + string(test.checkpoint) +
				`","startup_cause":"` + string(test.cause) + `","error":"/private/path ` + secret + `"}`
			got := liveHostdRebindClosedDiagnostic(stderr)
			if !strings.HasPrefix(got, "sha256:") || strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
				t.Fatalf("closed diagnostic exposed untrusted stderr: %q", got)
			}
			want := " startup=" + string(test.checkpoint)
			if test.cause != gatewayStartupDiagnosticCauseUnknown {
				want += " cause=" + string(test.cause)
			}
			if !strings.HasSuffix(got, want) {
				t.Fatalf("closed diagnostic=%q want %q", got, want)
			}
		})
	}
}

func TestLiveHostdRebindLegacyV1OwnershipControls(t *testing.T) {
	resources := liveHostdRebindLegacyV1Resources{
		containerID:      strings.Repeat("a", 64),
		networkID:        strings.Repeat("b", 64),
		configVolumeName: liveHostdRebindLegacyV1ConfigVolume,
	}
	containerLabels := map[string]string{
		"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.listener-isolation": "v1",
	}
	networkLabels := map[string]string{
		"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v1",
	}
	volumeLabels := map[string]string{
		"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1",
	}
	cases := []struct {
		name       string
		kind       string
		id         string
		nameID     string
		labels     map[string]string
		want       bool
		uncaptured bool
	}{
		{
			name: "exact captured container", kind: "container", id: resources.containerID,
			nameID: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, want: true,
		},
		{
			name: "exact captured network", kind: "network", id: resources.networkID,
			nameID: liveHostdRebindLegacyV1NetworkName, labels: networkLabels, want: true,
		},
		{
			name: "exact captured config volume", kind: "volume", id: liveHostdRebindLegacyV1ConfigVolume,
			nameID: liveHostdRebindLegacyV1ConfigVolume, labels: volumeLabels, want: true,
		},
		{
			name: "changed container identity", kind: "container", id: strings.Repeat("c", 64),
			nameID: liveHostdRebindLegacyV1ContainerName, labels: containerLabels,
		},
		{
			name: "foreign canonical lookalike", kind: "network", id: strings.Repeat("d", 64),
			nameID: liveHostdRebindLegacyV1NetworkName, labels: networkLabels,
		},
		{
			name: "foreign name with captured identity", kind: "container", id: resources.containerID,
			nameID: "foreign-caddy", labels: containerLabels,
		},
		{
			name: "malformed identity label", kind: "network", id: resources.networkID,
			nameID: liveHostdRebindLegacyV1NetworkName,
			labels: map[string]string{"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v2"},
		},
		{
			name: "missing container listener label", kind: "container", id: resources.containerID,
			nameID: liveHostdRebindLegacyV1ContainerName,
			labels: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"},
		},
		{
			name: "changed config volume name", kind: "volume", id: "other-volume",
			nameID: "other-volume", labels: volumeLabels,
		},
		{
			name: "zero capture denies removal", kind: "container", id: resources.containerID,
			nameID: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, uncaptured: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			captured := resources
			if test.uncaptured {
				captured = liveHostdRebindLegacyV1Resources{}
			}
			if got := captured.matches(test.kind, test.id, test.nameID, test.labels); got != test.want {
				t.Fatalf("legacy v1 ownership=%t want=%t", got, test.want)
			}
			identity, allowed := captured.removalIdentityFromInspection(test.kind, test.id, test.nameID, test.labels, test.labels, nil)
			wantIdentity := ""
			if test.want {
				wantIdentity = test.id
			}
			if allowed != test.want || identity != wantIdentity {
				t.Fatalf("removal authority=(%q, %t) want=(%q, %t)", identity, allowed, wantIdentity, test.want)
			}
		})
	}

	for _, test := range []struct {
		name       string
		kind       string
		id         string
		resource   string
		labels     map[string]string
		inspected  map[string]string
		inspectErr error
		want       liveHostdRebindLegacyV1CleanupDecision
	}{
		{
			name: "delegate exact captured resource", kind: "container", id: resources.containerID,
			resource: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, inspected: containerLabels,
			want: liveHostdRebindLegacyV1Delegate,
		},
		{
			name: "retain uncaptured legacy resource", kind: "container", id: strings.Repeat("e", 64),
			resource: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, inspected: containerLabels,
			want: liveHostdRebindLegacyV1Retain,
		},
		{
			name: "retain replaced legacy network", kind: "network", id: strings.Repeat("f", 64),
			resource: liveHostdRebindLegacyV1NetworkName, labels: networkLabels, inspected: networkLabels,
			want: liveHostdRebindLegacyV1Retain,
		},
		{
			name: "retain missing inspected identity", kind: "volume", labels: volumeLabels,
			inspectErr: errors.New("identity unavailable"), want: liveHostdRebindLegacyV1Retain,
		},
		{
			name: "retain label inspection disagreement", kind: "container", id: resources.containerID,
			resource: liveHostdRebindLegacyV1ContainerName, labels: containerLabels,
			inspected: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"},
			want:      liveHostdRebindLegacyV1Retain,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resources.cleanupDecisionFromInspection(test.kind, test.id, test.resource, test.labels, test.inspected, test.inspectErr)
			if got != test.want {
				t.Fatalf("legacy cleanup decision=%d want=%d", got, test.want)
			}
			identity, allowed := resources.removalIdentityFromInspection(test.kind, test.id, test.resource, test.labels, test.inspected, test.inspectErr)
			wantAllowed := test.want == liveHostdRebindLegacyV1Delegate
			wantIdentity := ""
			if wantAllowed {
				wantIdentity = test.id
			}
			if allowed != wantAllowed || identity != wantIdentity {
				t.Fatalf("removal authority=(%q, %t) want=(%q, %t)", identity, allowed, wantIdentity, wantAllowed)
			}
		})
	}
}
