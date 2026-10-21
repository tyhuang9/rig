package applifecycle

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

type operationFixture struct {
	db       *sql.DB
	repo     *Repository
	jobs     *jobs.Service
	dataRoot string
	appID    string
	actorID  string
	pins     GeneratedLifecyclePinsV1
}

func TestApplicationLifecycleOperationAcceptanceHistoricalReplayAndLegacyRefusal(t *testing.T) {
	fixture := newOperationFixture(t, 2)
	request := fixture.request(Start, BaselineRunning, "start-key", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil || !accepted.Created || accepted.Operation.JobID == "" || accepted.Operation.DesiredRevision != 1 {
		t.Fatalf("accept=%#v error=%v", accepted, err)
	}
	if string(mustJob(t, fixture.jobs, accepted.Operation.JobID).Input) != "{}" {
		t.Fatal("lifecycle job input changed from the sealed empty object")
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',type='stop',resource_type='machine',resource_id='mutated',requested_by='mutated' WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	laterRequest := fixture.request(Restart, BaselineRunning, "later-revision", 1)
	later, err := fixture.repo.Accept(context.Background(), fixture.jobs, laterRequest, fixture.pins)
	if err != nil || !later.Created || later.Operation.DesiredRevision != 2 {
		t.Fatalf("later acceptance=%#v error=%v", later, err)
	}
	replayed, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, GeneratedLifecyclePinsV1{})
	if err != nil || replayed.Created || replayed.Operation.JobID != accepted.Operation.JobID || replayed.Operation.PinsDigest != accepted.Operation.PinsDigest {
		t.Fatalf("historical replay=%#v error=%v", replayed, err)
	}
	changedActor := request
	changedActor.ActorID = uuid.NewString()
	if _, err := fixture.repo.Accept(context.Background(), fixture.jobs, changedActor, fixture.pins); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed actor replay error=%v", err)
	}
	changedRevision := request
	changedRevision.ExpectedDesiredRevision = 1
	if _, err := fixture.repo.Accept(context.Background(), fixture.jobs, changedRevision, fixture.pins); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed CAS replay error=%v", err)
	}

	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded' WHERE id=?`, later.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	legacyRequest := fixture.request(Stop, BaselineRunning, "legacy-key", 2)
	legacyJob, created, err := fixture.jobs.CreateWithInput(jobs.CreateRequest{
		Type: "stop", ResourceType: "application", ResourceID: fixture.appID,
		IdempotencyKey: legacyRequest.IdempotencyKey, RequestedBy: fixture.actorID, Input: jobs.NoInput{},
	})
	if err != nil || !created || legacyJob.ID == "" {
		t.Fatalf("legacy job=%#v created=%t error=%v", legacyJob, created, err)
	}
	if _, err := fixture.repo.Accept(context.Background(), fixture.jobs, legacyRequest, GeneratedLifecyclePinsV1{}); !errors.Is(err, ErrMissingOperation) {
		t.Fatalf("legacy job adoption error=%v", err)
	}
}

func TestApplicationLifecycleOperationCaughtFailuresLeaveNoPartialAuthority(t *testing.T) {
	t.Run("job event transition and pins", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		request := fixture.request(Start, BaselineRunning, "atomic-composition", 0)
		bad := fixture.pins
		bad.ActiveGeneration++
		if _, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, bad); err == nil {
			t.Fatal("invalid current pins accepted")
		}
		assertOperationCounts(t, fixture.db, fixture.appID, 0, 0, 0)
		var jobsCount, eventsCount int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&jobsCount); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM job_events e JOIN jobs j ON j.id=e.job_id WHERE j.idempotency_key=?`, request.IdempotencyKey).Scan(&eventsCount); err != nil {
			t.Fatal(err)
		}
		if jobsCount != 0 || eventsCount != 0 {
			t.Fatalf("partial acceptance jobs=%d events=%d", jobsCount, eventsCount)
		}
	})

	t.Run("after transition SQL refusal", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		request := fixture.request(Start, BaselineRunning, "bad-pins", 0)
		job := insertOperationJob(t, fixture, request)
		tx, err := fixture.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at)
			VALUES(?,'caller.work','application',?,'{}',?)`, fixture.actorID, fixture.appID, operationStamp()); err != nil {
			t.Fatal(err)
		}
		bad := fixture.pins
		bad.ActiveGeneration++
		if _, _, err := fixture.repo.AcceptOperationTx(context.Background(), tx, job, request, bad); err == nil {
			_ = tx.Rollback()
			t.Fatal("SQL pin mismatch accepted")
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("caller commit after refusal: %v", err)
		}
		assertOperationCounts(t, fixture.db, fixture.appID, 0, 0, 0)
		var audit int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='caller.work' AND resource_id=?`, fixture.appID).Scan(&audit); err != nil || audit != 1 {
			t.Fatalf("caller work=%d error=%v", audit, err)
		}
	})

	t.Run("post write decode failure", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		request := fixture.request(Restart, BaselineStopped, "decode-failure", 0)
		job := insertOperationJob(t, fixture, request)
		calls := 0
		fixture.repo.now = func() time.Time {
			calls++
			if calls == 1 {
				return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			}
			return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		tx, _ := fixture.db.Begin()
		if _, _, err := fixture.repo.AcceptOperationTx(context.Background(), tx, job, request, fixture.pins); err == nil {
			_ = tx.Rollback()
			t.Fatal("unparseable operation timestamp returned success")
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit after caught decode failure: %v", err)
		}
		assertOperationCounts(t, fixture.db, fixture.appID, 0, 0, 0)
	})

	t.Run("cancellation after transition", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		request := fixture.request(Stop, BaselineRunning, "cancel-after-transition", 0)
		job := insertOperationJob(t, fixture, request)
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		fixture.repo.now = func() time.Time {
			calls++
			if calls == 2 {
				cancel()
			}
			return time.Date(2026, 10, 18, 12, 0, 0, calls, time.UTC)
		}
		tx, _ := fixture.db.Begin()
		if _, _, err := fixture.repo.AcceptOperationTx(ctx, tx, job, request, fixture.pins); !errors.Is(err, context.Canceled) {
			_ = tx.Rollback()
			t.Fatalf("cancellation error=%v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit after caught cancellation: %v", err)
		}
		assertOperationCounts(t, fixture.db, fixture.appID, 0, 0, 0)
	})
}

func TestApplicationLifecycleOperationConcurrentAcceptanceHasOneWinner(t *testing.T) {
	fixture := newOperationFixture(t, 2)
	secondDB, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	request := fixture.request(Restart, BaselineRunning, "concurrent", 0)
	type result struct {
		accepted AcceptedOperation
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, pair := range []struct {
		repo *Repository
		jobs *jobs.Service
	}{{fixture.repo, fixture.jobs}, {New(secondDB), jobs.New(secondDB)}} {
		pair := pair
		go func() {
			<-start
			accepted, acceptErr := pair.repo.Accept(context.Background(), pair.jobs, request, fixture.pins)
			results <- result{accepted: accepted, err: acceptErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.accepted.Operation.JobID == "" ||
		first.accepted.Operation.JobID != second.accepted.Operation.JobID || first.accepted.Created == second.accepted.Created {
		t.Fatalf("concurrent acceptance first=%#v second=%#v", first, second)
	}
	assertOperationCounts(t, fixture.db, fixture.appID, 1, 1, 2)
}

func TestApplicationLifecycleOperationInvalidRequestsWriteNothing(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	valid := fixture.request(Start, BaselineRunning, "valid-key", 0)
	tests := map[string]LifecycleRequest{}
	tests["empty application"] = func() LifecycleRequest { value := valid; value.ApplicationID = ""; return value }()
	tests["empty actor"] = func() LifecycleRequest { value := valid; value.ActorID = ""; return value }()
	tests["empty key"] = func() LifecycleRequest { value := valid; value.IdempotencyKey = ""; return value }()
	tests["negative revision"] = func() LifecycleRequest { value := valid; value.ExpectedDesiredRevision = -1; return value }()
	tests["invalid action"] = func() LifecycleRequest { value := valid; value.Action = "pause"; return value }()
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins); err == nil {
				t.Fatal("invalid acceptance succeeded")
			}
			assertOperationCounts(t, fixture.db, fixture.appID, 0, 0, 0)
			var lifecycleJobs, lifecycleEvents int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type IN ('start','stop','restart')`).Scan(&lifecycleJobs); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM job_events e JOIN jobs j ON j.id=e.job_id WHERE j.type IN ('start','stop','restart')`).Scan(&lifecycleEvents); err != nil {
				t.Fatal(err)
			}
			if lifecycleJobs != 0 || lifecycleEvents != 0 {
				t.Fatalf("invalid request wrote jobs=%d events=%d", lifecycleJobs, lifecycleEvents)
			}
		})
	}
}

func TestApplicationLifecycleJournalAllGraphsAndComponentBounds(t *testing.T) {
	graphs := []struct {
		name     string
		action   Action
		baseline BaselineDisposition
	}{
		{"stop-running", Stop, BaselineRunning}, {"stop-stopped", Stop, BaselineStopped},
		{"start-running", Start, BaselineRunning}, {"start-stopped", Start, BaselineStopped},
		{"restart-running", Restart, BaselineRunning}, {"restart-stopped", Restart, BaselineStopped},
	}
	for _, components := range []int{1, 2} {
		for _, graph := range graphs {
			t.Run(fmtName(graph.name, components), func(t *testing.T) {
				fixture := newOperationFixture(t, components)
				request := fixture.request(graph.action, graph.baseline, graph.name, 0)
				accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
				if err != nil {
					t.Fatal(err)
				}
				claim := runningClaim(t, fixture, accepted.Operation.JobID)
				runCompleteGraph(t, fixture, claim, accepted.Operation)
				wantPairs := expectedGraphPairs(graph.action, graph.baseline, components)
				assertJournalCounts(t, fixture.db, accepted.Operation.JobID, wantPairs, wantPairs)
				unresolved, err := fixture.repo.ApplicationUnresolved(context.Background(), fixture.appID)
				if err != nil || unresolved {
					t.Fatalf("complete graph unresolved=%t error=%v", unresolved, err)
				}
				if excluded, err := fixture.repo.RebindExcluded(context.Background()); err != nil || excluded {
					t.Fatalf("complete graph rebind excluded=%t error=%v", excluded, err)
				}
			})
		}
	}
}

func expectedGraphPairs(action Action, baseline BaselineDisposition, components int) int {
	switch {
	case action == Stop:
		return components + 2
	case action == Start && baseline == BaselineRunning:
		return 3
	case action == Start && baseline == BaselineStopped:
		return components + 4
	case action == Restart && baseline == BaselineRunning:
		return 2*components + 4
	case action == Restart && baseline == BaselineStopped:
		return components + 4
	default:
		return -1
	}
}

func TestApplicationLifecycleJournalBarriersHistoricalReplayAndPendingFence(t *testing.T) {
	fixture := newOperationFixture(t, 2)
	request := fixture.request(Restart, BaselineRunning, "barriers", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	withdraw := writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectCommand, OutcomeRoutesAbsent)
	firstStop := writeEffect(t, fixture, claim, accepted.Operation, BoundaryStop, fixture.pins.Components[1].Name, EffectCommand, OutcomeContainerStopped)
	if _, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand); err == nil {
		t.Fatal("start passed before every stop sibling")
	}
	writeEffect(t, fixture, claim, accepted.Operation, BoundaryStop, fixture.pins.Components[0].Name, EffectCommand, OutcomeContainerStopped)
	firstStart := writeEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[1].Name, EffectCommand, OutcomeContainerRunning)
	if _, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryHealth, "", EffectObservationOnly); err == nil {
		t.Fatal("health passed before every start sibling")
	}
	writeEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand, OutcomeContainerRunning)

	if _, err := fixture.db.Exec(`UPDATE jobs SET status='failed',type='stop',resource_type='machine',resource_id='mutated',requested_by='mutated' WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
		VALUES(?, 'start','application',?,'succeeded','done',?,'{}',?,?)`, uuid.NewString(), fixture.appID, fixture.actorID, operationStamp(), operationStamp()); err != nil {
		t.Fatal(err)
	}

	tx, _ := fixture.db.Begin()
	replayedIntent, created, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, WorkerClaim{}, accepted.Operation, withdraw.intent.Request)
	if err != nil || created || replayedIntent.RequestDigest != withdraw.intent.RequestDigest {
		_ = tx.Rollback()
		t.Fatalf("historical intent=%#v created=%t error=%v", replayedIntent, created, err)
	}
	replayedReceipt, created, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, WorkerClaim{}, firstStop.intent, firstStop.receipt.Proof)
	if err != nil || created || replayedReceipt.ProofDigest != firstStop.receipt.ProofDigest {
		_ = tx.Rollback()
		t.Fatalf("historical receipt=%#v created=%t error=%v", replayedReceipt, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	changedProof := firstStop.receipt.Proof
	changedProof.EvidenceDigest = strings.Repeat("9", 64)
	tx, _ = fixture.db.Begin()
	if _, _, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, WorkerClaim{}, firstStop.intent, changedProof); !errors.Is(err, ErrEffectConflict) {
		_ = tx.Rollback()
		t.Fatalf("changed proof error=%v", err)
	}
	_ = tx.Commit()
	changedRequest := withdraw.intent.Request
	changedRequest.Mode = EffectObservationOnly
	tx, _ = fixture.db.Begin()
	if _, _, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, WorkerClaim{}, accepted.Operation, changedRequest); !errors.Is(err, ErrEffectConflict) {
		_ = tx.Rollback()
		t.Fatalf("changed request error=%v", err)
	}
	_ = tx.Commit()
	if _, err := fixture.repo.AuthorizePending(context.Background(), claim, firstStart.intent); !errors.Is(err, ErrEffectNotAuthorized) {
		t.Fatalf("receipted key authorized error=%v", err)
	}
}

func TestApplicationLifecyclePendingStartPersistsStatusPauseAndReopenUntilReceipt(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	request := fixture.request(Start, BaselineStopped, "pending-start", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
	intent, created, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand)
	if err != nil || !created {
		t.Fatalf("pending start=%#v created=%t error=%v", intent, created, err)
	}
	if pending, err := fixture.repo.PendingStartExists(context.Background()); err != nil || !pending {
		t.Fatalf("initial pending=%t error=%v", pending, err)
	}
	wrongProof := EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: fixture.appID,
		DesiredRevision: accepted.Operation.DesiredRevision, Boundary: BoundaryStart, ComponentName: intent.Key.ComponentName,
		Outcome: OutcomeContainerStopped, IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("c", 64), PinsDigest: accepted.Operation.PinsDigest}
	tx, _ := fixture.db.Begin()
	if _, _, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, intent, wrongProof); err == nil {
		_ = tx.Rollback()
		t.Fatal("wrong start outcome accepted")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after caught wrong outcome: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='paused',pause_disposition='insufficient_replacement_capacity' WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fixture.db, fixture.repo, fixture.jobs = reopened, New(reopened), jobs.New(reopened)
	if pending, err := fixture.repo.PendingStartExists(context.Background()); err != nil || !pending {
		t.Fatalf("reopened paused pending=%t error=%v", pending, err)
	}
	if _, err := reopened.Exec(`UPDATE jobs SET status='running',phase='running',pause_disposition=NULL WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	claim, err = WorkerClaimFor(mustJob(t, fixture.jobs, accepted.Operation.JobID))
	if err != nil {
		t.Fatal(err)
	}
	proof := EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: fixture.appID,
		DesiredRevision: accepted.Operation.DesiredRevision, Boundary: BoundaryStart, ComponentName: intent.Key.ComponentName,
		Outcome: OutcomeContainerRunning, IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("d", 64), PinsDigest: accepted.Operation.PinsDigest}
	tx, _ = reopened.Begin()
	if _, created, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("running receipt created=%t error=%v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if pending, err := fixture.repo.PendingStartExists(context.Background()); err != nil || pending {
		t.Fatalf("receipted pending=%t error=%v", pending, err)
	}
}

func TestApplicationLifecycleJournalPostWriteDecodeFailuresRollbackOnlyJournal(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Stop, BaselineRunning, "intent-decode-failure", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		fixture.repo.now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
		tx, _ := fixture.db.Begin()
		if _, err := tx.Exec(`INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at)
			VALUES(?,'caller.intent','application',?,'{}',?)`, fixture.actorID, fixture.appID, operationStamp()); err != nil {
			t.Fatal(err)
		}
		request := effectRequest(accepted.Operation, BoundaryWithdraw, "", EffectCommand)
		if _, _, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request); err == nil {
			_ = tx.Rollback()
			t.Fatal("unparseable intent timestamp returned success")
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("caller commit after intent decode failure: %v", err)
		}
		assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 0, 0)
		assertAuditCount(t, fixture.db, fixture.appID, "caller.intent", 1)
	})

	t.Run("receipt", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Start, BaselineStopped, "receipt-decode-failure", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
		intent, created, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand)
		if err != nil || !created {
			t.Fatalf("pending start created=%t error=%v", created, err)
		}
		fixture.repo.now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
		tx, _ := fixture.db.Begin()
		if _, err := tx.Exec(`INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at)
			VALUES(?,'caller.receipt','application',?,'{}',?)`, fixture.actorID, fixture.appID, operationStamp()); err != nil {
			t.Fatal(err)
		}
		proof := effectProof(accepted.Operation, intent, OutcomeContainerRunning)
		if _, _, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err == nil {
			_ = tx.Rollback()
			t.Fatal("unparseable receipt timestamp returned success")
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("caller commit after receipt decode failure: %v", err)
		}
		assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 2, 1)
		assertAuditCount(t, fixture.db, fixture.appID, "caller.receipt", 1)
		if pending, err := fixture.repo.PendingStartExists(context.Background()); err != nil || !pending {
			t.Fatalf("pending start after receipt rollback=%t error=%v", pending, err)
		}
	})
}

func TestApplicationLifecycleNewReceiptRequiresFreshRunningClaim(t *testing.T) {
	t.Run("stale attempt then fresh attempt", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Start, BaselineStopped, "stale-receipt", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		stale := runningClaim(t, fixture, accepted.Operation.JobID)
		writeEffect(t, fixture, stale, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
		intent, _, err := beginEffect(t, fixture, stale, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, accepted.Operation.JobID); err != nil {
			t.Fatal(err)
		}
		proof := effectProof(accepted.Operation, intent, OutcomeContainerRunning)
		tx, _ := fixture.db.Begin()
		if _, err := tx.Exec(`INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at)
			VALUES(?,'caller.stale','application',?,'{}',?)`, fixture.actorID, fixture.appID, operationStamp()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, stale, intent, proof); err == nil {
			_ = tx.Rollback()
			t.Fatal("stale worker attempt retained receipt")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 2, 1)
		assertAuditCount(t, fixture.db, fixture.appID, "caller.stale", 1)

		fresh, err := WorkerClaimFor(mustJob(t, fixture.jobs, accepted.Operation.JobID))
		if err != nil {
			t.Fatal(err)
		}
		tx, _ = fixture.db.Begin()
		if _, created, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, fresh, intent, proof); err != nil || !created {
			_ = tx.Rollback()
			t.Fatalf("fresh attempt could not receipt old intent created=%t error=%v", created, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 2, 2)
	})

	statuses := []string{"queued", "assigned", "waiting_external", "waiting_user", "succeeded", "failed", "cancelled", "interrupted", "needs_attention"}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			fixture := newOperationFixture(t, 1)
			accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
				fixture.request(Start, BaselineStopped, "status-"+status, 0), fixture.pins)
			if err != nil {
				t.Fatal(err)
			}
			claim := runningClaim(t, fixture, accepted.Operation.JobID)
			writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
			intent, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand)
			if err != nil {
				t.Fatal(err)
			}
			var pause any
			if status == "waiting_user" {
				pause = "route_reconciliation_required"
			}
			if _, err := fixture.db.Exec(`UPDATE jobs SET status=?,phase=?,pause_disposition=? WHERE id=?`, status, status, pause, accepted.Operation.JobID); err != nil {
				t.Fatal(err)
			}
			tx, _ := fixture.db.Begin()
			if _, _, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, intent,
				effectProof(accepted.Operation, intent, OutcomeContainerRunning)); err == nil {
				_ = tx.Rollback()
				t.Fatal("non-running job retained receipt")
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 2, 1)
		})
	}
}

func TestApplicationLifecycleClosedGraphsRejectExcludedKeys(t *testing.T) {
	t.Run("start running", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Start, BaselineRunning, "closed-start-running", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		cases := []EffectRequestV1{
			effectRequest(accepted.Operation, BoundaryWithdraw, "", EffectCommand),
			effectRequest(accepted.Operation, BoundaryStop, fixture.pins.Components[0].Name, EffectCommand),
			effectRequest(accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand),
		}
		for _, request := range cases {
			tx, _ := fixture.db.Begin()
			if _, _, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request); err == nil {
				_ = tx.Rollback()
				t.Fatalf("repository authored excluded key %s/%s", request.Boundary, request.ComponentName)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			raw, digest, _ := canonicalEffectRequest(request)
			if err := insertRawEffectIntent(fixture.db, accepted.Operation.JobID, claim.Attempt(), request, raw, digest); err == nil {
				t.Fatalf("SQL authored excluded key %s/%s", request.Boundary, request.ComponentName)
			}
		}
		assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 0, 0)
	})

	t.Run("restart stopped", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Restart, BaselineStopped, "closed-restart-stopped", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
		request := effectRequest(accepted.Operation, BoundaryStop, fixture.pins.Components[0].Name, EffectObservationOnly)
		tx, _ := fixture.db.Begin()
		if _, _, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request); err == nil {
			_ = tx.Rollback()
			t.Fatal("repository authored excluded stopped-baseline stop")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		raw, digest, _ := canonicalEffectRequest(request)
		if err := insertRawEffectIntent(fixture.db, accepted.Operation.JobID, claim.Attempt(), request, raw, digest); err == nil {
			t.Fatal("SQL authored excluded stopped-baseline stop")
		}
		assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 1, 1)
	})
}

func TestApplicationLifecycleRawReceiptAndCanonicalPayloadGuards(t *testing.T) {
	t.Run("receipt binding", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Start, BaselineStopped, "raw-receipt-binding", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
		intent, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand)
		if err != nil {
			t.Fatal(err)
		}
		valid := effectProof(accepted.Operation, intent, OutcomeContainerRunning)
		cases := map[string]EffectProofV1{
			"wrong outcome":        func() EffectProofV1 { value := valid; value.Outcome = OutcomeContainerStopped; return value }(),
			"foreign operation":    func() EffectProofV1 { value := valid; value.OperationID = uuid.NewString(); return value }(),
			"foreign application":  func() EffectProofV1 { value := valid; value.ApplicationID = uuid.NewString(); return value }(),
			"wrong intent binding": func() EffectProofV1 { value := valid; value.IntentDigest = strings.Repeat("0", 64); return value }(),
		}
		for name, proof := range cases {
			t.Run(name, func(t *testing.T) {
				raw, digest, err := canonicalEffectProof(proof)
				if err != nil {
					t.Fatal(err)
				}
				if err := insertRawEffectReceipt(fixture.db, intent, claim.Attempt(), proof, raw, digest); err == nil {
					t.Fatal("invalid raw proof retained")
				}
				assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 2, 1)
			})
		}
		validJSON, validDigest, _ := canonicalEffectProof(valid)
		for name, raw := range map[string][]byte{
			"unknown version": bytesReplace(validJSON, `"version":1`, `"version":2`),
			"duplicate key":   bytesReplace(validJSON, `"version":1`, `"version":1,"version":1`),
		} {
			t.Run(name, func(t *testing.T) {
				if err := insertRawEffectReceipt(fixture.db, intent, claim.Attempt(), valid, raw, validDigest); err == nil {
					t.Fatal("non-closed raw proof retained")
				}
				assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 2, 1)
			})
		}
	})

	t.Run("request version and duplicate key", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Stop, BaselineRunning, "raw-request-canonical", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectCommand, OutcomeRoutesAbsent)
		request := effectRequest(accepted.Operation, BoundaryStop, fixture.pins.Components[0].Name, EffectCommand)
		raw, digest, _ := canonicalEffectRequest(request)
		for name, malformed := range map[string][]byte{
			"unknown version": bytesReplace(raw, `"version":1`, `"version":2`),
			"duplicate key":   bytesReplace(raw, `"version":1`, `"version":1,"version":1`),
		} {
			t.Run(name, func(t *testing.T) {
				if err := insertRawEffectIntent(fixture.db, accepted.Operation.JobID, claim.Attempt(), request, malformed, digest); err == nil {
					t.Fatal("non-closed raw request retained")
				}
				assertJournalCounts(t, fixture.db, accepted.Operation.JobID, 1, 1)
			})
		}
	})
}

func TestApplicationLifecycleHistoricalUnresolvedOperationSurvivesNewerCompletion(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	first, err := fixture.repo.Accept(context.Background(), fixture.jobs,
		fixture.request(Start, BaselineRunning, "older-unresolved", 0), fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, first.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	second, err := fixture.repo.Accept(context.Background(), fixture.jobs,
		fixture.request(Restart, BaselineRunning, "newer-completed", 1), fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	runCompleteGraph(t, fixture, runningClaim(t, fixture, second.Operation.JobID), second.Operation)
	if unresolved, err := fixture.repo.ApplicationUnresolved(context.Background(), fixture.appID); err != nil || !unresolved {
		t.Fatalf("older operation unresolved=%t error=%v", unresolved, err)
	}
	if excluded, err := fixture.repo.RebindExcluded(context.Background()); err != nil || !excluded {
		t.Fatalf("older operation rebind excluded=%t error=%v", excluded, err)
	}
}

func TestApplicationLifecycleAuthorityRejectsNoncanonicalStoredIntent(t *testing.T) {
	for name, mutate := range map[string]func([]byte, string) ([]byte, string){
		"wrong digest": func(raw []byte, _ string) ([]byte, string) { return raw, strings.Repeat("0", 64) },
		"noncanonical bytes": func(raw []byte, digest string) ([]byte, string) {
			return append([]byte(" "), raw...), digest
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newOperationFixture(t, 1)
			accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
				fixture.request(Stop, BaselineRunning, "authority-"+name, 0), fixture.pins)
			if err != nil {
				t.Fatal(err)
			}
			claim := runningClaim(t, fixture, accepted.Operation.JobID)
			request := effectRequest(accepted.Operation, BoundaryWithdraw, "", EffectCommand)
			raw, canonicalDigest, _ := canonicalEffectRequest(request)
			raw, storedDigest := mutate(raw, canonicalDigest)
			if err := insertRawEffectIntent(fixture.db, accepted.Operation.JobID, claim.Attempt(), request, raw, storedDigest); err != nil {
				t.Fatalf("shape-valid raw intent setup: %v", err)
			}
			intent := Intent{JobID: accepted.Operation.JobID, Key: EffectKey{Boundary: BoundaryWithdraw},
				Request: request, RequestDigest: storedDigest}
			if _, err := fixture.repo.AuthorizePending(context.Background(), claim, intent); err == nil {
				t.Fatal("noncanonical persisted intent authorized")
			}
		})
	}
}

func TestApplicationLifecyclePendingStartRejectsMalformedMatchingReceipt(t *testing.T) {
	for name, mutate := range map[string]func([]byte, string) ([]byte, string){
		"wrong digest": func(raw []byte, _ string) ([]byte, string) { return raw, strings.Repeat("0", 64) },
		"noncanonical bytes": func(raw []byte, digest string) ([]byte, string) {
			return append([]byte(" "), raw...), digest
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newOperationFixture(t, 1)
			accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
				fixture.request(Start, BaselineStopped, "pending-malformed-"+name, 0), fixture.pins)
			if err != nil {
				t.Fatal(err)
			}
			claim := runningClaim(t, fixture, accepted.Operation.JobID)
			writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
			intent, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, fixture.pins.Components[0].Name, EffectCommand)
			if err != nil {
				t.Fatal(err)
			}
			proof := effectProof(accepted.Operation, intent, OutcomeContainerRunning)
			raw, canonicalDigest, _ := canonicalEffectProof(proof)
			raw, storedDigest := mutate(raw, canonicalDigest)
			if err := insertRawEffectReceipt(fixture.db, intent, claim.Attempt(), proof, raw, storedDigest); err != nil {
				t.Fatalf("shape-valid raw receipt setup: %v", err)
			}
			if pending, err := fixture.repo.PendingStartExists(context.Background()); err == nil || !pending {
				t.Fatalf("malformed matching receipt pending=%t error=%v", pending, err)
			}
		})
	}
}

func TestApplicationLifecycleResolutionValidatesCompletionAndRequiredGraph(t *testing.T) {
	t.Run("noncanonical completion", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Stop, BaselineRunning, "bad-completion", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectCommand, OutcomeRoutesAbsent)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryStop, fixture.pins.Components[0].Name, EffectCommand, OutcomeContainerStopped)
		intent, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryComplete, "", EffectObservationOnly)
		if err != nil {
			t.Fatal(err)
		}
		proof := effectProof(accepted.Operation, intent, OutcomeCompleteStopped)
		raw, digest, _ := canonicalEffectProof(proof)
		if err := insertRawEffectReceipt(fixture.db, intent, claim.Attempt(), proof, append([]byte(" "), raw...), digest); err != nil {
			t.Fatalf("shape-valid raw completion setup: %v", err)
		}
		assertResolutionFailsClosed(t, fixture)
	})

	t.Run("malformed required graph proof", func(t *testing.T) {
		fixture := newOperationFixture(t, 1)
		accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs,
			fixture.request(Stop, BaselineRunning, "bad-required-proof", 0), fixture.pins)
		if err != nil {
			t.Fatal(err)
		}
		claim := runningClaim(t, fixture, accepted.Operation.JobID)
		withdraw, _, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectCommand)
		if err != nil {
			t.Fatal(err)
		}
		withdrawProof := effectProof(accepted.Operation, withdraw, OutcomeRoutesAbsent)
		raw, _, _ := canonicalEffectProof(withdrawProof)
		if err := insertRawEffectReceipt(fixture.db, withdraw, claim.Attempt(), withdrawProof, raw, strings.Repeat("0", 64)); err != nil {
			t.Fatalf("shape-valid required proof setup: %v", err)
		}
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryStop, fixture.pins.Components[0].Name, EffectCommand, OutcomeContainerStopped)
		writeEffect(t, fixture, claim, accepted.Operation, BoundaryComplete, "", EffectObservationOnly, OutcomeCompleteStopped)
		assertResolutionFailsClosed(t, fixture)
	})
}

func TestApplicationLifecycleSQLGuardsRetainOperationsAndJournal(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	request := fixture.request(Stop, BaselineRunning, "sql-guards", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"operation update":  `UPDATE application_lifecycle_operations SET actor_id='changed' WHERE job_id='` + accepted.Operation.JobID + `'`,
		"operation delete":  `DELETE FROM application_lifecycle_operations WHERE job_id='` + accepted.Operation.JobID + `'`,
		"operation replace": `INSERT OR REPLACE INTO application_lifecycle_operations SELECT * FROM application_lifecycle_operations WHERE job_id='` + accepted.Operation.JobID + `'`,
		"component insert":  `INSERT INTO application_lifecycle_operation_components(job_id,ordinal,component_name) VALUES('` + accepted.Operation.JobID + `',1,'extra')`,
		"component update":  `UPDATE application_lifecycle_operation_components SET ordinal=1 WHERE job_id='` + accepted.Operation.JobID + `'`,
		"component delete":  `DELETE FROM application_lifecycle_operation_components WHERE job_id='` + accepted.Operation.JobID + `'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.db.Exec(statement); err == nil {
				t.Fatal("immutable SQL mutation succeeded")
			}
		})
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	withdraw := writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectCommand, OutcomeRoutesAbsent)
	component := fixture.pins.Components[0].Name
	stopRequest := EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: fixture.appID,
		DesiredRevision: accepted.Operation.DesiredRevision, Boundary: BoundaryStop, ComponentName: component,
		Mode: EffectCommand, PinsDigest: accepted.Operation.PinsDigest}
	stopJSON, stopDigest, err := canonicalEffectRequest(stopRequest)
	if err != nil {
		t.Fatal(err)
	}
	insertRawIntent := func(attempt int, boundary Boundary, componentName string, raw []byte, digest string) error {
		_, err := fixture.db.Exec(`INSERT INTO application_lifecycle_effect_intents(
			job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
		) VALUES(?,?,?,?,1,?,?,?)`, accepted.Operation.JobID, string(boundary), componentName, attempt, string(raw), digest, operationStamp())
		return err
	}
	if _, err := fixture.db.Exec(`INSERT INTO application_lifecycle_effect_intents(
		job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
	) VALUES(?,?,?,?,1,?,?,?)`, uuid.NewString(), string(BoundaryStop), component, claim.Attempt(), string(stopJSON), stopDigest, operationStamp()); err == nil {
		t.Fatal("missing foreign operation authored intent")
	}
	if err := insertRawIntent(claim.Attempt()+1, BoundaryStop, component, stopJSON, stopDigest); err == nil {
		t.Fatal("stale writer attempt authored intent")
	}
	malformed := bytesReplace(stopJSON, `"version":1`, `"unknown":1,"version":1`)
	if err := insertRawIntent(claim.Attempt(), BoundaryStop, component, malformed, stopDigest); err == nil {
		t.Fatal("unknown request field authored intent")
	}
	wrongKey := stopRequest
	wrongKey.ComponentName = "foreign-component"
	wrongJSON, wrongDigest, _ := canonicalEffectRequest(wrongKey)
	if err := insertRawIntent(claim.Attempt(), BoundaryStop, wrongKey.ComponentName, wrongJSON, wrongDigest); err == nil {
		t.Fatal("foreign component authored intent")
	}
	completeRequest := stopRequest
	completeRequest.Boundary, completeRequest.ComponentName, completeRequest.Mode = BoundaryComplete, "", EffectObservationOnly
	completeJSON, completeDigest, _ := canonicalEffectRequest(completeRequest)
	if err := insertRawIntent(claim.Attempt(), BoundaryComplete, "", completeJSON, completeDigest); err == nil {
		t.Fatal("out-of-order completion intent succeeded")
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='paused',pause_disposition='route_reconciliation_required' WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	if err := insertRawIntent(claim.Attempt(), BoundaryStop, component, stopJSON, stopDigest); err == nil {
		t.Fatal("non-running job authored intent")
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='running',pause_disposition=NULL WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"intent update":   `UPDATE application_lifecycle_effect_intents SET request_digest='` + strings.Repeat("a", 64) + `' WHERE job_id='` + accepted.Operation.JobID + `'`,
		"intent delete":   `DELETE FROM application_lifecycle_effect_intents WHERE job_id='` + accepted.Operation.JobID + `'`,
		"intent replace":  `INSERT OR REPLACE INTO application_lifecycle_effect_intents SELECT * FROM application_lifecycle_effect_intents WHERE job_id='` + accepted.Operation.JobID + `'`,
		"receipt update":  `UPDATE application_lifecycle_effect_receipts SET proof_digest='` + strings.Repeat("a", 64) + `' WHERE job_id='` + accepted.Operation.JobID + `'`,
		"receipt delete":  `DELETE FROM application_lifecycle_effect_receipts WHERE job_id='` + accepted.Operation.JobID + `'`,
		"receipt replace": `INSERT OR REPLACE INTO application_lifecycle_effect_receipts SELECT * FROM application_lifecycle_effect_receipts WHERE job_id='` + accepted.Operation.JobID + `'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.db.Exec(statement); err == nil {
				t.Fatal("immutable journal SQL mutation succeeded")
			}
		})
	}
	if _, err := fixture.repo.AuthorizePending(context.Background(), claim, withdraw.intent); !errors.Is(err, ErrEffectNotAuthorized) {
		t.Fatalf("receipted withdraw authorized error=%v", err)
	}
}

func TestApplicationLifecycleCanonicalPayloadsRejectUnknownMissingAndDuplicatePins(t *testing.T) {
	fixture := newOperationFixture(t, 2)
	encoded, digest, err := canonicalPins(fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown field":           bytesReplace(encoded, `"version":1`, `"unknown":1,"version":1`),
		"missing field":           bytesReplace(encoded, `"version":1,`, ``),
		"noncanonical whitespace": append([]byte(" "), encoded...),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var decoded GeneratedLifecyclePinsV1
			if err := decodeCanonical(raw, &decoded, func() ([]byte, string, error) { return canonicalPins(decoded) }, digest); err == nil {
				t.Fatal("noncanonical pins decoded")
			}
		})
	}
	duplicate := fixture.pins
	duplicate.Components[1].Name = duplicate.Components[0].Name
	duplicate.Configuration.RuntimeComponents[1] = duplicate.Components[0].Name
	if _, _, err := canonicalPins(duplicate); !errors.Is(err, ErrInvalidPins) {
		t.Fatalf("duplicate components error=%v", err)
	}
	var decoded GeneratedLifecyclePinsV1
	if err := decodeCanonical(encoded, &decoded, func() ([]byte, string, error) { return canonicalPins(decoded) }, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong pins digest decoded")
	}
}

type writtenEffect struct {
	intent  Intent
	receipt Receipt
}

func runCompleteGraph(t *testing.T, fixture *operationFixture, claim WorkerClaim, operation Operation) {
	t.Helper()
	action, baseline := operation.Action, operation.Pins.BaselineDisposition
	if !(action == Start && baseline == BaselineRunning) {
		writeEffect(t, fixture, claim, operation, BoundaryWithdraw, "", modeFor(operation, BoundaryWithdraw), OutcomeRoutesAbsent)
	}
	if action == Stop || (action == Restart && baseline == BaselineRunning) {
		for index := len(operation.Pins.Components) - 1; index >= 0; index-- {
			writeEffect(t, fixture, claim, operation, BoundaryStop, operation.Pins.Components[index].Name, modeFor(operation, BoundaryStop), OutcomeContainerStopped)
		}
	}
	if (action == Start && baseline == BaselineStopped) || action == Restart {
		for index := len(operation.Pins.Components) - 1; index >= 0; index-- {
			writeEffect(t, fixture, claim, operation, BoundaryStart, operation.Pins.Components[index].Name, EffectCommand, OutcomeContainerRunning)
		}
	}
	if action == Start || action == Restart {
		writeEffect(t, fixture, claim, operation, BoundaryHealth, "", EffectObservationOnly, OutcomeHealthy)
		writeEffect(t, fixture, claim, operation, BoundaryPublish, "", EffectCommand, OutcomeServing)
	}
	complete := OutcomeCompleteRunning
	if action == Stop {
		complete = OutcomeCompleteStopped
	}
	writeEffect(t, fixture, claim, operation, BoundaryComplete, "", EffectObservationOnly, complete)
}

func beginEffect(t *testing.T, fixture *operationFixture, claim WorkerClaim, operation Operation, boundary Boundary, component string, mode EffectMode) (Intent, bool, error) {
	t.Helper()
	request := EffectRequestV1{Version: 1, OperationID: operation.JobID, ApplicationID: operation.ApplicationID,
		DesiredRevision: operation.DesiredRevision, Boundary: boundary, ComponentName: component, Mode: mode, PinsDigest: operation.PinsDigest}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	intent, created, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, operation, request)
	if err != nil {
		_ = tx.Commit()
		return Intent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return intent, created, nil
}

func writeEffect(t *testing.T, fixture *operationFixture, claim WorkerClaim, operation Operation, boundary Boundary, component string, mode EffectMode, outcome Outcome) writtenEffect {
	t.Helper()
	intent, created, err := beginEffect(t, fixture, claim, operation, boundary, component, mode)
	if err != nil || !created {
		t.Fatalf("begin %s/%s created=%t error=%v", boundary, component, created, err)
	}
	proof := EffectProofV1{Version: 1, OperationID: operation.JobID, ApplicationID: operation.ApplicationID,
		DesiredRevision: operation.DesiredRevision, Boundary: boundary, ComponentName: component, Outcome: outcome,
		IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("e", 64), PinsDigest: operation.PinsDigest}
	tx, _ := fixture.db.Begin()
	receipt, observed, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof)
	if err != nil || !observed {
		_ = tx.Rollback()
		t.Fatalf("observe %s/%s created=%t error=%v", boundary, component, observed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return writtenEffect{intent: intent, receipt: receipt}
}

func modeFor(operation Operation, boundary Boundary) EffectMode {
	if boundary == BoundaryHealth || boundary == BoundaryComplete || operation.Pins.BaselineDisposition == BaselineStopped && (boundary == BoundaryWithdraw || boundary == BoundaryStop) {
		return EffectObservationOnly
	}
	return EffectCommand
}

func effectRequest(operation Operation, boundary Boundary, component string, mode EffectMode) EffectRequestV1 {
	return EffectRequestV1{Version: 1, OperationID: operation.JobID, ApplicationID: operation.ApplicationID,
		DesiredRevision: operation.DesiredRevision, Boundary: boundary, ComponentName: component,
		Mode: mode, PinsDigest: operation.PinsDigest}
}

func effectProof(operation Operation, intent Intent, outcome Outcome) EffectProofV1 {
	return EffectProofV1{Version: 1, OperationID: operation.JobID, ApplicationID: operation.ApplicationID,
		DesiredRevision: operation.DesiredRevision, Boundary: intent.Key.Boundary, ComponentName: intent.Key.ComponentName,
		Outcome: outcome, IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("e", 64), PinsDigest: operation.PinsDigest}
}

func insertRawEffectIntent(db *sql.DB, jobID string, attempt int, request EffectRequestV1, raw []byte, digest string) error {
	_, err := db.Exec(`INSERT INTO application_lifecycle_effect_intents(
		job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
	) VALUES(?,?,?,?,1,?,?,?)`, jobID, string(request.Boundary), request.ComponentName, attempt, string(raw), digest, operationStamp())
	return err
}

func insertRawEffectReceipt(db *sql.DB, intent Intent, attempt int, proof EffectProofV1, raw []byte, digest string) error {
	_, err := db.Exec(`INSERT INTO application_lifecycle_effect_receipts(
		job_id,boundary,component_name,writer_attempt,outcome,proof_version,proof_json,proof_digest,observed_at
	) VALUES(?,?,?,?,?,1,?,?,?)`, intent.JobID, string(intent.Key.Boundary), intent.Key.ComponentName,
		attempt, string(proof.Outcome), string(raw), digest, operationStamp())
	return err
}

func assertJournalCounts(t *testing.T, db *sql.DB, jobID string, wantIntents, wantReceipts int) {
	t.Helper()
	var intents, receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_lifecycle_effect_intents WHERE job_id=?`, jobID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_lifecycle_effect_receipts WHERE job_id=?`, jobID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if intents != wantIntents || receipts != wantReceipts {
		t.Fatalf("journal counts intents=%d receipts=%d want=%d/%d", intents, receipts, wantIntents, wantReceipts)
	}
}

func assertAuditCount(t *testing.T, db *sql.DB, appID, action string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_id=? AND action=?`, appID, action).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("audit %s count=%d want=%d", action, count, want)
	}
}

func assertResolutionFailsClosed(t *testing.T, fixture *operationFixture) {
	t.Helper()
	if unresolved, err := fixture.repo.ApplicationUnresolved(context.Background(), fixture.appID); err == nil || !unresolved {
		t.Fatalf("malformed graph unresolved=%t error=%v", unresolved, err)
	}
	if excluded, err := fixture.repo.RebindExcluded(context.Background()); err == nil || !excluded {
		t.Fatalf("malformed graph rebind excluded=%t error=%v", excluded, err)
	}
}

func newOperationFixture(t *testing.T, componentCount int) *operationFixture {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := databasefixture.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &operationFixture{db: db, repo: New(db), jobs: jobs.New(db), dataRoot: dataRoot, appID: uuid.NewString(), actorID: uuid.NewString()}
	planID, releaseID, deploymentID, deployJobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := operationStamp()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`, f.actorID, f.actorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, f.appID, f.appID, f.appID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,?,0,'',?,?,'accepted',?,?)`,
		planID, f.appID, "apps/"+f.appID+"/plan.json", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), componentCount, f.actorID, stamp, f.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, f.appID, stamp, strings.Repeat("d", 64), strings.Repeat("e", 64), planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,'{}',?,?)`, deployJobID, f.appID, f.actorID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, f.appID, releaseID, deployJobID, planID); err != nil {
		t.Fatal(err)
	}
	names := []string{"api"}
	if componentCount == 2 {
		names = append(names, "web")
	}
	runtime := generatedruntimestate.New(db)
	value, created, err := runtime.Begin(context.Background(), generatedruntimestate.BeginInput{DeploymentID: deploymentID, AppID: f.appID, ReleaseID: releaseID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: names})
	if err != nil || !created {
		t.Fatalf("runtime begin=%#v created=%t error=%v", value, created, err)
	}
	components := make([]RetainedComponent, 0, len(names))
	for _, name := range names {
		artifactID, containerID, containerName := uuid.NewString(), digestFor(name+"-container"), "rig-"+name+"-retained"
		definition, image := digestFor(name+"-definition"), "sha256:"+digestFor(name+"-image")
		if _, err := db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at)
			VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, name, definition, image, stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.SetImageReady(context.Background(), f.appID, deploymentID, name, artifactID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.SetContainerStarting(context.Background(), f.appID, deploymentID, name, containerName); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.SetContainerRunning(context.Background(), f.appID, deploymentID, name, containerID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.AdvanceComponent(context.Background(), f.appID, deploymentID, name, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
			t.Fatal(err)
		}
		components = append(components, RetainedComponent{Name: name, Role: "service", Technology: "node", ContainerID: containerID,
			ContainerName: containerName, ArtifactID: artifactID, ImageContentID: image, BuildDefinitionDigest: definition,
			NetworkName: "rig", NetworkAlias: name, InternalPort: 8080, WorkingDirectory: "/app", RunCommandDigest: digestFor(name + "-command"),
			Limits: RetainedLimits{MemoryBytes: 128 << 20, MemorySwapBytes: 256 << 20, CPUQuota: 50000, CPUPeriod: 100000, PIDsLimit: 128, TmpfsBytes: 16 << 20, LogDriver: "local", LogMaxSize: "10m", LogMaxFiles: 3, HardeningPolicyVersion: "v1"}})
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute} {
		value, err = runtime.Advance(context.Background(), f.appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtime.SwitchActive(context.Background(), f.appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("active head=%#v changed=%t error=%v", head, changed, err)
	}
	value, err = runtime.Advance(context.Background(), f.appID, deploymentID, value.Phase, generatedruntimestate.PhaseDraining, "")
	if err != nil {
		t.Fatal(err)
	}
	value, err = runtime.Advance(context.Background(), f.appID, deploymentID, value.Phase, generatedruntimestate.PhaseSucceeded, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE deployments SET status='succeeded',started_at=?,finished_at=? WHERE id=?`, stamp, stamp, deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, deployJobID); err != nil {
		t.Fatal(err)
	}
	f.pins = GeneratedLifecyclePinsV1{Version: 1, DeploymentID: deploymentID, ReleaseID: releaseID, Slot: head.Slot, ActiveGeneration: head.Generation,
		PlanRevision: RevisionRef{ID: planID, Number: 1}, Configuration: ConfigurationRef{FormatVersion: 0, PlanRevision: RevisionRef{ID: planID, Number: 1}, RuntimeComponents: names},
		BaselineDisposition: BaselineRunning, Components: components, Route: AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind", SelectedLineageGeneration: 1,
			SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("f", 64), ProtectedFormatVersion: 2, TargetRecordDigest: strings.Repeat("a", 64)}}
	return f
}

func (f *operationFixture) request(action Action, baseline BaselineDisposition, key string, expected int64) LifecycleRequest {
	f.pins.BaselineDisposition = baseline
	return LifecycleRequest{ApplicationID: f.appID, ActorID: f.actorID, IdempotencyKey: key, Action: action, ExpectedDesiredRevision: expected}
}

func runningClaim(t *testing.T, fixture *operationFixture, jobID string) WorkerClaim {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=attempt+1 WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	claim, err := WorkerClaimFor(mustJob(t, fixture.jobs, jobID))
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func insertOperationJob(t *testing.T, fixture *operationFixture, request LifecycleRequest) jobs.Job {
	t.Helper()
	job, created, err := fixture.jobs.CreateWithInput(jobs.CreateRequest{Type: string(request.Action), ResourceType: "application", ResourceID: request.ApplicationID,
		IdempotencyKey: request.IdempotencyKey, RequestedBy: request.ActorID, Input: jobs.NoInput{}})
	if err != nil || !created {
		t.Fatalf("insert operation job=%#v created=%t error=%v", job, created, err)
	}
	return job
}

func mustJob(t *testing.T, service *jobs.Service, id string) jobs.Job {
	t.Helper()
	job, err := service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func assertOperationCounts(t *testing.T, db *sql.DB, appID string, revisions, operations, components int) {
	t.Helper()
	var gotRevisions, gotOperations, gotComponents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_transitions WHERE app_id=?`, appID).Scan(&gotRevisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_lifecycle_operations WHERE app_id=?`, appID).Scan(&gotOperations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_lifecycle_operation_components c JOIN application_lifecycle_operations o ON o.job_id=c.job_id WHERE o.app_id=?`, appID).Scan(&gotComponents); err != nil {
		t.Fatal(err)
	}
	if gotRevisions != revisions || gotOperations != operations || gotComponents != components {
		t.Fatalf("counts transitions=%d operations=%d components=%d", gotRevisions, gotOperations, gotComponents)
	}
	var head int64
	if err := db.QueryRow(`SELECT revision_number FROM application_desired_lifecycle_heads WHERE app_id=?`, appID).Scan(&head); err != nil || head != int64(revisions) {
		t.Fatalf("head=%d error=%v", head, err)
	}
}

func digestFor(seed string) string {
	value := []byte(seed)
	const hex = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hex[int(value[i%len(value)])%len(hex)]
	}
	return string(out)
}
func operationStamp() string {
	return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
}
func bytesReplace(value []byte, old, replacement string) []byte {
	return bytes.Replace(value, []byte(old), []byte(replacement), 1)
}
func fmtName(name string, components int) string {
	return name + "-" + string(rune('0'+components)) + "-components"
}
