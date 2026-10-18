package applifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
)

func TestApplicationDesiredLifecycleTransitionsAndHistoricalReplay(t *testing.T) {
	fixture := newLifecycleFixture(t)
	fixture.repo.now = func() time.Time {
		return time.Date(2026, 10, 18, 1, 2, 3, 4, time.UTC)
	}
	head, err := fixture.repo.Head(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	if head.DesiredState != nil || head.Revision != 0 || head.UpdatedAt != nil {
		t.Fatalf("initial head=%#v", head)
	}
	if history, err := fixture.repo.History(context.Background(), fixture.appID); err != nil || len(history) != 0 {
		t.Fatalf("initial history=%#v error=%v", history, err)
	}

	steps := []struct {
		jobID        string
		action       Action
		expected     int64
		wantState    State
		wantPrevious *State
	}{
		{"job-start", Start, 0, Running, nil},
		{"job-restart", Restart, 1, Running, statePointer(Running)},
		{"job-stop", Stop, 2, Stopped, statePointer(Running)},
		{"job-start-again", Start, 3, Running, statePointer(Stopped)},
	}
	var first Transition
	for index, step := range steps {
		insertLifecycleJob(t, fixture.db, step.jobID, string(step.action), fixture.appID, fixture.actorID)
		transition, created, err := fixture.authorAndCommit(AuthorRequest{
			ApplicationID: fixture.appID, JobID: step.jobID, Action: step.action,
			ActorID: fixture.actorID, ExpectedRevision: step.expected,
		})
		if err != nil {
			t.Fatalf("author %s: %v", step.action, err)
		}
		if !created || transition.Revision != int64(index+1) || transition.ExpectedRevision != step.expected || transition.DesiredState != step.wantState || !sameState(transition.PreviousState, step.wantPrevious) {
			t.Fatalf("transition %d=%#v created=%t", index+1, transition, created)
		}
		if index == 0 {
			first = transition
		}
	}

	if _, err := fixture.db.Exec(`UPDATE jobs
		SET type='stop',resource_type='machine',resource_id='mutated-resource',requested_by='mutated-actor'
		WHERE id='job-start'`); err != nil {
		t.Fatal(err)
	}
	replayed, created, err := fixture.authorAndCommit(AuthorRequest{
		ApplicationID: fixture.appID, JobID: "job-start", Action: Start,
		ActorID: fixture.actorID, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || !reflect.DeepEqual(replayed, first) || replayed.Revision != 1 {
		t.Fatalf("historical replay=%#v created=%t first=%#v", replayed, created, first)
	}
	if _, _, err := fixture.authorAndCommit(AuthorRequest{
		ApplicationID: fixture.appID, JobID: "job-start", Action: Start,
		ActorID: fixture.actorID, ExpectedRevision: 1,
	}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting historical replay error=%v", err)
	}
	head, err = fixture.repo.Head(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	if head.DesiredState == nil || *head.DesiredState != Running || head.Revision != 4 {
		t.Fatalf("final head=%#v", head)
	}
	history, err := fixture.repo.History(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history length=%d", len(history))
	}
	for index, transition := range history {
		if transition.Revision != int64(index+1) || transition.ExpectedRevision != int64(index) {
			t.Fatalf("history gap at %d: %#v", index, transition)
		}
	}
	var observedStatus string
	if err := fixture.db.QueryRow(`SELECT status FROM applications WHERE id=?`, fixture.appID).Scan(&observedStatus); err != nil {
		t.Fatal(err)
	}
	if observedStatus != "deployed" {
		t.Fatalf("desired lifecycle changed observed application status to %q", observedStatus)
	}
}

func TestApplicationDesiredLifecycleAuthorityFailuresRemainUninitialized(t *testing.T) {
	fixture := newLifecycleFixture(t)
	insertLifecycleApplication(t, fixture.db, "other-app", "failed")
	insertLifecycleJob(t, fixture.db, "foreign-job", "start", "other-app", fixture.actorID)
	insertLifecycleJob(t, fixture.db, "wrong-resource-job", "start", fixture.appID, fixture.actorID)
	if _, err := fixture.db.Exec(`UPDATE jobs SET resource_type='machine' WHERE id='wrong-resource-job'`); err != nil {
		t.Fatal(err)
	}
	insertLifecycleJob(t, fixture.db, "actor-job", "start", fixture.appID, fixture.actorID)
	insertLifecycleJob(t, fixture.db, "action-job", "start", fixture.appID, fixture.actorID)
	insertLifecycleJob(t, fixture.db, "missing-app-job", "start", "missing-app", fixture.actorID)

	tests := []struct {
		name    string
		request AuthorRequest
		want    error
	}{
		{"nil transaction", AuthorRequest{ApplicationID: fixture.appID, JobID: "actor-job", Action: Start, ActorID: fixture.actorID}, ErrInvalidRequest},
		{"empty application", AuthorRequest{JobID: "actor-job", Action: Start, ActorID: fixture.actorID}, ErrInvalidRequest},
		{"empty job", AuthorRequest{ApplicationID: fixture.appID, Action: Start, ActorID: fixture.actorID}, ErrInvalidRequest},
		{"empty actor", AuthorRequest{ApplicationID: fixture.appID, JobID: "actor-job", Action: Start}, ErrInvalidRequest},
		{"negative expected revision", AuthorRequest{ApplicationID: fixture.appID, JobID: "actor-job", Action: Start, ActorID: fixture.actorID, ExpectedRevision: -1}, ErrInvalidRequest},
		{"invalid action", AuthorRequest{ApplicationID: fixture.appID, JobID: "actor-job", Action: "pause", ActorID: fixture.actorID}, ErrInvalidAction},
		{"missing job", AuthorRequest{ApplicationID: fixture.appID, JobID: "missing-job", Action: Start, ActorID: fixture.actorID}, ErrJobNotFound},
		{"foreign application job", AuthorRequest{ApplicationID: fixture.appID, JobID: "foreign-job", Action: Start, ActorID: fixture.actorID}, ErrJobMismatch},
		{"wrong resource type", AuthorRequest{ApplicationID: fixture.appID, JobID: "wrong-resource-job", Action: Start, ActorID: fixture.actorID}, ErrJobMismatch},
		{"actor mismatch", AuthorRequest{ApplicationID: fixture.appID, JobID: "actor-job", Action: Start, ActorID: "other-actor"}, ErrJobMismatch},
		{"action mismatch", AuthorRequest{ApplicationID: fixture.appID, JobID: "action-job", Action: Stop, ActorID: fixture.actorID}, ErrJobMismatch},
		{"missing application", AuthorRequest{ApplicationID: "missing-app", JobID: "missing-app-job", Action: Start, ActorID: fixture.actorID}, ErrApplicationNotFound},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.name == "nil transaction" {
				_, _, err := fixture.repo.AuthorTransition(context.Background(), nil, testCase.request)
				if !errors.Is(err, testCase.want) {
					t.Fatalf("error=%v want=%v", err, testCase.want)
				}
				return
			}
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			_, _, got := fixture.repo.AuthorTransition(context.Background(), tx, testCase.request)
			if !errors.Is(got, testCase.want) {
				_ = tx.Rollback()
				t.Fatalf("error=%v want=%v", got, testCase.want)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit after rejected authoring: %v", err)
			}
		})
	}
	assertLifecycleHeadAndHistory(t, fixture.db, fixture.appID, 0, 0)

	insertLifecycleJob(t, fixture.db, "valid-job", "start", fixture.appID, fixture.actorID)
	if _, _, err := fixture.authorAndCommit(AuthorRequest{
		ApplicationID: fixture.appID, JobID: "valid-job", Action: Start,
		ActorID: fixture.actorID, ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	insertLifecycleJob(t, fixture.db, "stale-job", "stop", fixture.appID, fixture.actorID)
	if _, _, err := fixture.authorAndCommit(AuthorRequest{
		ApplicationID: fixture.appID, JobID: "stale-job", Action: Stop,
		ActorID: fixture.actorID, ExpectedRevision: 0,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale error=%v", err)
	}
	if _, _, err := fixture.authorAndCommit(AuthorRequest{
		ApplicationID: fixture.appID, JobID: "valid-job", Action: Start,
		ActorID: fixture.actorID, ExpectedRevision: 1,
	}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	assertLifecycleHeadAndHistory(t, fixture.db, fixture.appID, 1, 1)
}

func TestApplicationDesiredLifecyclePostWriteFailureRollsBackOnlyAuthoring(t *testing.T) {
	fixture := newLifecycleFixture(t)
	fixture.repo.now = func() time.Time {
		return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	insertLifecycleJob(t, fixture.db, "post-write-job", "start", fixture.appID, fixture.actorID)
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,'caller.work','application',?,'{}','2026-10-18T01:00:00Z')`, fixture.actorID, fixture.appID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, _, err = fixture.repo.AuthorTransition(context.Background(), tx, AuthorRequest{
		ApplicationID: fixture.appID, JobID: "post-write-job", Action: Start,
		ActorID: fixture.actorID, ExpectedRevision: 0,
	})
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("unparseable post-write timestamp returned success")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("caller transaction was not preserved after ordinary authoring failure: %v", err)
	}
	var callerRows int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE actor_id=? AND action='caller.work' AND resource_id=?`, fixture.actorID, fixture.appID).Scan(&callerRows); err != nil {
		t.Fatal(err)
	}
	if callerRows != 1 {
		t.Fatalf("caller-owned rows after authoring rollback=%d", callerRows)
	}
	assertLifecycleHeadAndHistory(t, fixture.db, fixture.appID, 0, 0)
}

func TestApplicationDesiredLifecycleCanceledOperationRollsBackOnlyAuthoring(t *testing.T) {
	fixture := newLifecycleFixture(t)
	insertLifecycleJob(t, fixture.db, "canceled-job", "start", fixture.appID, fixture.actorID)
	operationContext, cancelOperation := context.WithCancel(context.Background())
	fixture.repo.now = func() time.Time {
		cancelOperation()
		return time.Date(2026, 10, 18, 1, 2, 3, 4, time.UTC)
	}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,'caller.canceled','application',?,'{}','2026-10-18T01:00:00Z')`, fixture.actorID, fixture.appID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, _, err = fixture.repo.AuthorTransition(operationContext, tx, AuthorRequest{
		ApplicationID: fixture.appID, JobID: "canceled-job", Action: Start,
		ActorID: fixture.actorID, ExpectedRevision: 0,
	})
	if !errors.Is(err, context.Canceled) {
		_ = tx.Rollback()
		t.Fatalf("canceled authoring error=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("caller transaction was not preserved after cancellation: %v", err)
	}
	var callerRows int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE actor_id=? AND action='caller.canceled' AND resource_id=?`, fixture.actorID, fixture.appID).Scan(&callerRows); err != nil {
		t.Fatal(err)
	}
	if callerRows != 1 {
		t.Fatalf("caller-owned rows after canceled authoring=%d", callerRows)
	}
	assertLifecycleHeadAndHistory(t, fixture.db, fixture.appID, 0, 0)
}

func TestApplicationDesiredLifecycleConcurrentCASHasOneWinnerAndNoGap(t *testing.T) {
	dataRoot := t.TempDir()
	dbOne, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer dbOne.Close()
	dbTwo, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer dbTwo.Close()
	insertLifecycleApplication(t, dbOne, "concurrent-app", "deployed")
	insertLifecycleJob(t, dbOne, "concurrent-start-a", "start", "concurrent-app", "concurrent-actor")
	insertLifecycleJob(t, dbOne, "concurrent-start-b", "start", "concurrent-app", "concurrent-actor")

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index, db := range []*sql.DB{dbOne, dbTwo} {
		group.Add(1)
		go func(index int, db *sql.DB) {
			defer group.Done()
			<-start
			tx, err := db.Begin()
			if err != nil {
				results <- err
				return
			}
			_, created, err := New(db).AuthorTransition(context.Background(), tx, AuthorRequest{
				ApplicationID:    "concurrent-app",
				JobID:            fmt.Sprintf("concurrent-start-%c", 'a'+index),
				Action:           Start,
				ActorID:          "concurrent-actor",
				ExpectedRevision: 0,
			})
			if err == nil && !created {
				err = errors.New("distinct operation replayed")
			}
			if err != nil {
				_ = tx.Rollback()
				results <- err
				return
			}
			results <- tx.Commit()
		}(index, db)
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent writers=%d want=1", successes)
	}
	assertLifecycleHeadAndHistory(t, dbOne, "concurrent-app", 1, 1)
	var minimum, maximum int64
	if err := dbOne.QueryRow(`SELECT MIN(revision_number),MAX(revision_number)
		FROM application_desired_lifecycle_transitions WHERE app_id='concurrent-app'`).Scan(&minimum, &maximum); err != nil {
		t.Fatal(err)
	}
	if minimum != 1 || maximum != 1 {
		t.Fatalf("concurrent history range=%d..%d", minimum, maximum)
	}
}

func TestApplicationDesiredLifecycleComposesWithFinalizedJobRollback(t *testing.T) {
	fixture := newLifecycleFixture(t)
	service := jobs.New(fixture.db)
	if _, _, err := service.CreateWithInput(jobs.CreateRequest{
		Type: "start", ResourceType: "application", ResourceID: "wake-app",
		IdempotencyKey: "wake", RequestedBy: fixture.actorID, Input: jobs.NoInput{},
	}); err != nil {
		t.Fatal(err)
	}
	wakeBefore := jobWakeLength(service)
	rollbackErr := errors.New("force finalized lifecycle rollback")
	request := jobs.CreateRequest{
		Type: "start", ResourceType: "application", ResourceID: fixture.appID,
		IdempotencyKey: "lifecycle-rollback", RequestedBy: fixture.actorID, Input: jobs.NoInput{},
	}
	_, _, err := service.CreateWithInputFinalized(request, func(tx *sql.Tx, job jobs.Job) error {
		transition, created, err := fixture.repo.AuthorTransition(context.Background(), tx, AuthorRequest{
			ApplicationID: fixture.appID, JobID: job.ID, Action: Start,
			ActorID: fixture.actorID, ExpectedRevision: 0,
		})
		if err != nil {
			return err
		}
		if !created || transition.Revision != 1 {
			return fmt.Errorf("unexpected finalized transition: %#v created=%t", transition, created)
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("finalized create error=%v", err)
	}
	if wakeAfter := jobWakeLength(service); wakeBefore != 1 || wakeAfter != wakeBefore {
		t.Fatalf("job wake length before=%d after=%d", wakeBefore, wakeAfter)
	}
	var jobsCount, eventsCount int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE resource_id=? AND idempotency_key=?`, fixture.appID, request.IdempotencyKey).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM job_events e JOIN jobs j ON j.id=e.job_id
		WHERE j.resource_id=? AND j.idempotency_key=?`, fixture.appID, request.IdempotencyKey).Scan(&eventsCount); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 0 || eventsCount != 0 {
		t.Fatalf("rolled-back jobs=%d events=%d", jobsCount, eventsCount)
	}
	assertLifecycleHeadAndHistory(t, fixture.db, fixture.appID, 0, 0)
}

type lifecycleFixture struct {
	db      *sql.DB
	repo    *Repository
	appID   string
	actorID string
}

func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := lifecycleFixture{db: db, repo: New(db), appID: "lifecycle-app", actorID: "lifecycle-actor"}
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at)
		VALUES(?,?,'hash','2026-10-18T01:00:00Z','2026-10-18T01:00:00Z')`, fixture.actorID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	insertLifecycleApplication(t, db, fixture.appID, "deployed")
	return fixture
}

func (fixture lifecycleFixture) authorAndCommit(request AuthorRequest) (Transition, bool, error) {
	tx, err := fixture.db.Begin()
	if err != nil {
		return Transition{}, false, err
	}
	transition, created, err := fixture.repo.AuthorTransition(context.Background(), tx, request)
	if err != nil {
		_ = tx.Rollback()
		return Transition{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Transition{}, false, err
	}
	return transition, created, nil
}

func insertLifecycleApplication(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,?,'2026-10-18T01:00:00Z','2026-10-18T01:00:00Z')`, id, id, id, status); err != nil {
		t.Fatal(err)
	}
}

func insertLifecycleJob(t *testing.T, db *sql.DB, id, action, appID, actorID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at
	) VALUES(?,?,'application',?,'succeeded','finalize',?,'{}',
		'2026-10-18T01:00:00Z','2026-10-18T01:00:00Z')`, id, action, appID, actorID); err != nil {
		t.Fatal(err)
	}
}

func assertLifecycleHeadAndHistory(t *testing.T, db *sql.DB, appID string, revision, history int64) {
	t.Helper()
	var gotRevision, gotHistory int64
	if err := db.QueryRow(`SELECT revision_number FROM application_desired_lifecycle_heads
		WHERE app_id=?`, appID).Scan(&gotRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_transitions
		WHERE app_id=?`, appID).Scan(&gotHistory); err != nil {
		t.Fatal(err)
	}
	if gotRevision != revision || gotHistory != history {
		t.Fatalf("app=%s revision=%d history=%d want revision=%d history=%d",
			appID, gotRevision, gotHistory, revision, history)
	}
}

func statePointer(value State) *State {
	return &value
}

func sameState(left, right *State) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func jobWakeLength(service *jobs.Service) int {
	return reflect.ValueOf(service).Elem().FieldByName("wake").Len()
}
