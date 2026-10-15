package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

// SQL claim creation and protected admission can read the same wall-clock tick.
// Pin that boundary without sleeps; recovery must also tolerate a clock that
// moved backwards while retaining the exact SQL claim and existing history.
func TestGatewayRebindPreparedProgressTimeFollowsSQLClaim(t *testing.T) {
	for _, path := range []string{"private", "strict", "recovery"} {
		for _, clock := range []struct {
			name  string
			delta time.Duration
		}{
			{name: "equal"},
			{name: "later", delta: time.Second},
			{name: "backwards", delta: -time.Second},
		} {
			t.Run(path+"/"+clock.name, func(t *testing.T) {
				ctx := context.Background()
				f, input, strictProof := gatewayRebindStrictNativeFixture(t)
				installCrossStoreFixtureNetworkObserver(f.manager, f.state.Network.Subnet)
				beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				admissionProof := gatewayRebindAdmissionFixtureProof(t, f.manager, beforeSQL, input.Inspection,
					gatewayRebindNativeFixtureInspector(t, f),
					gatewayRebindCrossStoreAdmissionTestReads(input.Inspection.Spec, f.state.Network.Subnet))
				beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
				if err != nil {
					t.Fatal(err)
				}
				beforeCommands := len(f.runner.commands)
				var claimed appaccess.GatewayRebindClaimV2
				claimCalls, clockCalls, sessionCalls := 0, 0, 0
				readClock := func() time.Time {
					clockCalls++
					return claimed.CreatedAt.Add(clock.delta)
				}
				f.manager.gatewayRebindAfterClaim = func(_ context.Context, claim appaccess.GatewayRebindClaimV2) error {
					claimCalls++
					claimed = claim
					f.manager.gatewayRebindClock = readClock
					if path == "recovery" {
						return errors.New("injected stop after the SQL claim")
					}
					return nil
				}
				attempt, prepareErr := withCrossStoreFixtureEffectLocks(t, f.manager,
					func() (gatewayRebindPreparedAttempt, error) {
						if path == "strict" {
							return f.manager.prepareGatewayRebindStrictLocked(ctx, f.repository, input, strictProof,
								func(context.Context) error { sessionCalls++; return nil })
						}
						return f.manager.prepareGatewayRebindLocked(ctx, f.repository, input)
					})
				if claimCalls != 1 || claimed.CreatedAt.IsZero() {
					t.Fatalf("did not reach the real SQL claim: calls=%d error=%v", claimCalls, prepareErr)
				}
				preparedSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
				if err != nil || preparedSQL.Active == nil || preparedSQL.Active.Claim.V2 == nil ||
					!reflect.DeepEqual(*preparedSQL.Active.Claim.V2, claimed) ||
					preparedSQL.Phase != appaccess.GatewayRebindPrepared || !preparedSQL.RollbackAllowed ||
					preparedSQL.DatabaseCommitObserved ||
					!reflect.DeepEqual(beforeSQL.CurrentSource, preparedSQL.CurrentSource) ||
					!reflect.DeepEqual(beforeSQL.CurrentProfile, preparedSQL.CurrentProfile) ||
					!errors.Is(f.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
					t.Fatalf("prepared time changed SQL authority or released its fence: %v", err)
				}
				manager := f.manager
				if path == "recovery" {
					interruptedFiles, readErr := readGatewayHistorySnapshotMode(f.manager.store, true)
					if prepareErr == nil || clockCalls != 0 || readErr != nil ||
						!sameGatewayHistorySnapshot(beforeFiles, interruptedFiles) {
						t.Fatalf("post-claim interruption installed protected evidence: clockCalls=%d prepare=%v read=%v",
							clockCalls, prepareErr, readErr)
					}
					manager, err = New(f.runner, f.manager.options)
					if err != nil {
						t.Fatal(err)
					}
					manager.gatewayRebindClock = readClock
					attempt, prepareErr = withCrossStoreFixtureEffectLocks(t, manager,
						func() (gatewayRebindPreparedAttempt, error) {
							return manager.recoverGatewayRebindPreparedAdmissionWithProofLocked(ctx, f.repository,
								preparedSQL, gatewayRebindImmutableFixtureProofReader(admissionProof))
						})
				}
				if prepareErr != nil {
					t.Fatalf("post-claim clock prevented prepared progress: claim=%s candidate=%s error=%v",
						claimed.CreatedAt.Format(time.RFC3339Nano), claimed.CreatedAt.Add(clock.delta).Format(time.RFC3339Nano), prepareErr)
				}
				want := claimed.CreatedAt.Add(clock.delta)
				if clock.delta <= 0 {
					want = claimed.CreatedAt.Add(time.Nanosecond)
				}
				occurredAt, parseErr := parseGatewayRebindProgressTime(attempt.Progress.OccurredAt)
				if parseErr != nil || !occurredAt.Equal(want) || !occurredAt.After(claimed.CreatedAt) ||
					clockCalls != 1 || (path == "strict" && sessionCalls == 0) ||
					attempt.Progress.Sequence != 1 || attempt.Progress.Phase != gatewayRebindProgressSuccessorIntent ||
					!reflect.DeepEqual(attempt.Claim, claimed) ||
					attempt.Checkpoint.Digest != input.Inspection.PredecessorCheckpointDigest ||
					attempt.Intent.Predecessor != input.Inspection.Spec.Predecessor {
					t.Fatalf("prepared progress lost its ordered time or claim: got=%s want=%s clocks=%d sessions=%d error=%v",
						attempt.Progress.OccurredAt, want.Format(time.RFC3339Nano), clockCalls, sessionCalls, parseErr)
				}
				preparedFiles, err := readGatewayHistorySnapshotMode(manager.store, true)
				if err != nil || !gatewayRebindAdmissionHistoryRetained(beforeFiles, preparedFiles) {
					t.Fatalf("prepared time rewrote predecessor protected history: %v", err)
				}
				if path == "private" && clock.name == "equal" {
					// Time normalization belongs to the coordinator that constructs a
					// new record. The installer must still refuse explicitly bad input.
					for _, invalid := range []time.Time{claimed.CreatedAt, claimed.CreatedAt.Add(-time.Second)} {
						_, invalidErr := withCrossStoreFixtureEffectLocks(t, manager,
							func() (gatewayRebindPreparedAttempt, error) {
								return manager.installGatewayRebindPreparedProtectedWithNetworkLocked(ctx, claimed,
									input.Inspection.Roster, input.Inspection.RuntimeHeads, attempt.Checkpoint,
									attempt.Intent.NetworkObservation, invalid)
							})
						if invalidErr == nil {
							t.Fatal("explicit unordered prepared time bypassed the installer validator")
						}
					}
				}
				replayManager, err := New(f.runner, f.manager.options)
				if err != nil {
					t.Fatal(err)
				}
				replayClockCalls, replayProofCalls := 0, 0
				replayManager.gatewayRebindClock = func() time.Time {
					replayClockCalls++
					return claimed.CreatedAt.Add(-time.Hour)
				}
				replayed, replayErr := withCrossStoreFixtureEffectLocks(t, replayManager,
					func() (gatewayRebindPreparedAttempt, error) {
						return replayManager.recoverGatewayRebindPreparedAdmissionWithProofLocked(ctx, f.repository,
							preparedSQL, func(context.Context, gatewayRebindAdmissionProofTarget, gatewayCurrentSelection) (gatewayRebindAdmissionPhysicalProof, error) {
								replayProofCalls++
								return gatewayRebindAdmissionPhysicalProof{}, errors.New("unexpected proof read during exact replay")
							})
					})
				finalFiles, fileErr := readGatewayHistorySnapshotMode(replayManager.store, true)
				finalSQL, sqlErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
				if replayErr != nil || !reflect.DeepEqual(attempt, replayed) || replayClockCalls != 0 || replayProofCalls != 0 ||
					fileErr != nil || !sameGatewayHistorySnapshot(preparedFiles, finalFiles) ||
					sqlErr != nil || !reflect.DeepEqual(preparedSQL, finalSQL) ||
					!errors.Is(f.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) ||
					len(f.runner.commands) != beforeCommands {
					t.Fatalf("clock-independent replay changed history, SQL, fence or external effects: replay=%v files=%v SQL=%v clocks=%d proofs=%d",
						replayErr, fileErr, sqlErr, replayClockCalls, replayProofCalls)
				}
			})
		}
	}
}
