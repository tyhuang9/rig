package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type finalConfigAutosavePolicyCase struct {
	name           string
	phase, from    gatewayRebindProgressPhase
	final, running bool
	want           gatewayRebindFinalConfigAutosaveMode
}

func finalConfigAutosavePolicyCases() []finalConfigAutosavePolicyCase {
	return []finalConfigAutosavePolicyCase{
		{"seq12 stage", gatewayRebindProgressFinalConfigCopied, "", false, true, gatewayRebindFinalConfigStageAutosave},
		{"seq13 stage", gatewayRebindProgressFinalHandoverIntent, "", false, false, gatewayRebindFinalConfigStageAutosave},
		{"seq13 stopped final", gatewayRebindProgressFinalHandoverIntent, "", true, false, gatewayRebindFinalConfigStageAutosave},
		{"seq14 stopped final", gatewayRebindProgressFinalContainerBound, "", true, false, gatewayRebindFinalConfigStageAutosave},
		{"seq15 stopped final", gatewayRebindProgressCutoverIntent, "", true, false, gatewayRebindFinalConfigEitherAutosave},
		{"seq15 running final", gatewayRebindProgressCutoverIntent, "", true, true, gatewayRebindFinalConfigActiveAutosave},
		{"seq16 running final", gatewayRebindProgressSuccessorServing, "", true, true, gatewayRebindFinalConfigActiveAutosave},
		{"seq16 stopped final", gatewayRebindProgressSuccessorServing, "", true, false, gatewayRebindFinalConfigActiveAutosave},
		{"seq17 final", gatewayRebindProgressHandoverCommitted, "", true, false, gatewayRebindFinalConfigActiveAutosave},
		{"rollback stage", gatewayRebindProgressRollbackIntent, gatewayRebindProgressFinalHandoverIntent, false, true, gatewayRebindFinalConfigStageAutosave},
		{"rollback before cutover", gatewayRebindProgressRollbackIntent, gatewayRebindProgressFinalContainerBound, true, false, gatewayRebindFinalConfigStageAutosave},
		{"rollback uncertain stopped", gatewayRebindProgressRollbackIntent, gatewayRebindProgressCutoverIntent, true, false, gatewayRebindFinalConfigEitherAutosave},
		{"rollback uncertain running", gatewayRebindProgressRollbackIntent, gatewayRebindProgressCutoverIntent, true, true, gatewayRebindFinalConfigActiveAutosave},
		{"rollback after serving", gatewayRebindProgressRollbackIntent, gatewayRebindProgressSuccessorServing, true, false, gatewayRebindFinalConfigActiveAutosave},
		{"unknown phase", "unknown", "", true, false, 0},
		{"old phase", gatewayRebindProgressStageServing, "", false, true, 0},
		{"final before intent", gatewayRebindProgressFinalConfigCopied, "", true, false, 0},
		{"running final before cutover", gatewayRebindProgressFinalContainerBound, "", true, true, 0},
		{"stage after cutover", gatewayRebindProgressCutoverIntent, "", false, false, 0},
		{"abort with final", gatewayRebindProgressHandoverRolledBack, "", true, false, 0},
		{"rollback missing origin", gatewayRebindProgressRollbackIntent, "", true, false, 0},
		{"rollback terminal origin", gatewayRebindProgressRollbackIntent, gatewayRebindProgressHandoverCommitted, true, false, 0},
	}
}

func TestGatewayRebindFinalHandoverAutosavePolicyTracksDurableCutoverAndRollbackOrigin(t *testing.T) {
	for _, test := range finalConfigAutosavePolicyCases() {
		t.Run(test.name, func(t *testing.T) {
			if got := gatewayRebindFinalHandoverAutosaveMode(test.phase, test.from, test.final, test.running); got != test.want {
				t.Fatalf("policy=%d want=%d", got, test.want)
			}
		})
	}
}

func TestGatewayRebindFinalHandoverConfigPairUsesPhasePolicyAndBoundedRead(t *testing.T) {
	value, _ := newGatewayRebindFinalHandoverPlanTestContext(t)
	stage, err := gatewayRebindStageConfigBytes(value.Intent)
	if err != nil {
		t.Fatal(err)
	}
	active, err := gatewayRebindFinalConfigBytes(value.Intent, value.SequenceTwelve.Stage.FinalConfigIntent.RoutePlan)
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(body []byte) []byte {
		var object any
		if json.Unmarshal(body, &object) != nil {
			t.Fatal("invalid renderer JSON")
		}
		result, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, test := range finalConfigAutosavePolicyCases() {
		t.Run(test.name, func(t *testing.T) {
			for _, save := range []struct {
				name    string
				body    []byte
				allowed bool
			}{
				{"stage", canonical(stage), test.want == gatewayRebindFinalConfigStageAutosave || test.want == gatewayRebindFinalConfigEitherAutosave},
				{"active", canonical(active), test.want == gatewayRebindFinalConfigActiveAutosave || test.want == gatewayRebindFinalConfigEitherAutosave},
			} {
				t.Run(save.name, func(t *testing.T) {
					current := value
					current.Phase, current.RollbackFromPhase = test.phase, test.from
					id := value.SequenceTwelve.Stage.StageContainer.ID
					if test.final {
						id = strings.Repeat("9", 64)
					}
					archive := finalConfigAutosaveArchive(t, stage, active, save.body, "stage.json", "active.json", "caddy/", "caddy/autosave.json")
					runner := &finalConfigDriverRunner{result: runtimeprocess.CommandResult{Stdout: archive}}
					manager := &Manager{runner: runner, dockerEnv: []string{"DOCKER_CONFIG=controlled-test-only"}, options: Options{DockerExecutable: "pinned-docker", WorkingDirectory: t.TempDir(), CommandTimeout: 7 * time.Second, OutputLimit: 4096}}
					driver := newManagerGatewayRebindFinalHandoverDriver(manager, gatewayRebindSuccessorPreflightReads{}, nil)
					if got := driver.handoverConfigPair(context.Background(), current, id, test.running); got != save.allowed {
						t.Fatalf("pair=%t want=%t", got, save.allowed)
					}
					if test.want == 0 {
						if len(runner.requests) != 0 {
							t.Fatal("invalid phase dispatched Docker inventory")
						}
						return
					}
					if len(runner.requests) != 1 {
						t.Fatalf("inventory requests=%d", len(runner.requests))
					}
					request := runner.requests[0]
					if request.Executable != "pinned-docker" || !reflect.DeepEqual(request.Args, []string{"container", "cp", id + ":/config/.", "-"}) || request.Directory != manager.options.WorkingDirectory || request.Timeout != 7*time.Second || request.OutputLimit != 192<<10 || !reflect.DeepEqual(request.Env, manager.dockerEnv) || manager.options.OutputLimit != 4096 {
						t.Fatal("phase inventory changed command scope or generic output bound")
					}
					if !allGatewayRebindTarZero(archive) {
						t.Fatal("Docker inventory bytes were not cleared")
					}
				})
			}
		})
	}
}

type finalHandoverAutosaveRead struct {
	context     gatewayRebindFinalHandoverContext
	observation gatewayRebindFinalHandoverObservation
}

type finalHandoverAutosaveRecordingDriver struct {
	gatewayRebindFinalHandoverDriver
	reads []finalHandoverAutosaveRead
}

func (d *finalHandoverAutosaveRecordingDriver) observeHandover(ctx context.Context, value gatewayRebindFinalHandoverContext) (gatewayRebindFinalHandoverObservation, error) {
	observation, err := d.gatewayRebindFinalHandoverDriver.observeHandover(ctx, value)
	if err == nil {
		d.reads = append(d.reads, finalHandoverAutosaveRead{value, observation})
	}
	return observation, err
}

func TestGatewayRebindFinalHandoverRollbackAutosaveContextComesFromDurableOrigin(t *testing.T) {
	for _, test := range []struct {
		name                string
		origin              gatewayRebindProgressPhase
		sequence, failWrite uint64
		failEffect          string
		afterEffect         bool
		final               gatewayRebindHandoverContainerState
	}{
		{"sequence thirteen", gatewayRebindProgressFinalHandoverIntent, 13, 0, "stop_stage", false, gatewayRebindHandoverContainerAbsent},
		{"sequence fourteen", gatewayRebindProgressFinalContainerBound, 14, 15, "", false, gatewayRebindHandoverContainerStopped},
		{"uncertain start stopped", gatewayRebindProgressCutoverIntent, 15, 0, "start_final", false, gatewayRebindHandoverContainerStopped},
		{"uncertain start running", gatewayRebindProgressCutoverIntent, 15, 0, "start_final", true, gatewayRebindHandoverContainerRunning},
		{"sequence sixteen", gatewayRebindProgressSuccessorServing, 16, 17, "", false, gatewayRebindHandoverContainerRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake := installGatewayRebindFinalHandoverFixture(t)
			fake.failAt, fake.failAfter = test.failEffect, test.afterEffect
			original := upgradeProtectedWriteNew
			writeFailed := false
			if test.failWrite != 0 {
				name, _ := gatewayRebindProgressName(fixture.intent.Generation, fixture.intent.OperationID, test.failWrite)
				upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
					if filepath.Base(path) == name && !writeFailed {
						writeFailed = true
						return errors.New("injected pause before next immutable record")
					}
					return original(path, purpose, body)
				}
			}
			t.Cleanup(func() { upgradeProtectedWriteNew = original })
			driver := &finalHandoverAutosaveRecordingDriver{gatewayRebindFinalHandoverDriver: fake}
			err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, driver, gatewayRebindProgressTimestamp(13), nil)
			upgradeProtectedWriteNew = original
			if err == nil || (test.failWrite != 0 && !writeFailed) || (test.failEffect != "" && !fake.failed) {
				t.Fatal("required durable origin was not reached")
			}
			paused, err := fake.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(paused.Progress) != int(test.sequence) || paused.Progress[len(paused.Progress)-1].Record.Phase != test.origin || fake.observation.Final != test.final {
				t.Fatal("fault left an unexpected handover phase or final state")
			}
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			driver.reads = nil
			if err := fake.manager.rollbackGatewayRebindFinalHandoverWithDriver(context.Background(), fixture.predecessor.repository, driver, gatewayRebindProgressTimestamp(40), nil); err != nil {
				t.Fatalf("rollback from actual durable origin: %v", err)
			}
			history := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalAbort)
			last := history.Progress[len(history.Progress)-1].Record
			if last.Handover == nil || last.Handover.Rollback == nil || last.Handover.Rollback.FromPhase != test.origin {
				t.Fatal("terminal history lost the actual rollback origin")
			}
			var rollbackReads, containerReads int
			for _, read := range driver.reads {
				if read.context.Phase != gatewayRebindProgressRollbackIntent {
					continue
				}
				rollbackReads++
				if read.context.RollbackFromPhase != last.Handover.Rollback.FromPhase {
					t.Fatal("coordinator supplied autosave authority other than the retained rollback origin")
				}
				stagePresent := read.observation.Stage != gatewayRebindHandoverContainerAbsent
				finalPresent := read.observation.Final != gatewayRebindHandoverContainerAbsent
				if !stagePresent && !finalPresent {
					continue
				}
				containerReads++
				running := read.observation.Final == gatewayRebindHandoverContainerRunning
				want := gatewayRebindFinalConfigStageAutosave
				if finalPresent && test.origin == gatewayRebindProgressCutoverIntent {
					want = gatewayRebindFinalConfigEitherAutosave
					if running {
						want = gatewayRebindFinalConfigActiveAutosave
					}
				} else if finalPresent && test.origin == gatewayRebindProgressSuccessorServing {
					want = gatewayRebindFinalConfigActiveAutosave
				}
				if got := gatewayRebindFinalHandoverAutosaveMode(read.context.Phase, read.context.RollbackFromPhase, finalPresent, running); got != want {
					t.Fatalf("durable rollback selected policy=%d want=%d", got, want)
				}
			}
			if rollbackReads == 0 || containerReads == 0 {
				t.Fatal("test did not observe the actual rollback/config policy boundary")
			}
			before := liveGatewayRebindProgressBytes(t, history)
			effects := append([]string(nil), fake.effects...)
			fake.manager = newGatewayRebindStageContainerManager(t, fixture)
			if err := fake.manager.handoverGatewayRebindFinalWithDriver(context.Background(), fixture.predecessor.repository, driver, gatewayRebindProgressTimestamp(60), nil); err != nil {
				t.Fatal(err)
			}
			replayed := assertGatewayRebindFinalHandoverTerminal(t, fixture, gatewayRebindFinalHandoverTerminalAbort)
			if !reflect.DeepEqual(effects, fake.effects) || !reflect.DeepEqual(before, liveGatewayRebindProgressBytes(t, replayed)) {
				t.Fatal("terminal replay changed effects or retained history")
			}
		})
	}
}
