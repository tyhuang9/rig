package controllerrelay

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
)

const (
	managementTestController = "11111111-1111-4111-8111-111111111111"
	managementTestKey        = "22222222-2222-4222-8222-222222222222"
	managementTestBinding    = "33333333-3333-4333-8333-333333333333"
	managementTestEnrollment = "44444444-4444-4444-8444-444444444444"
	managementTestRotation   = "55555555-5555-4555-8555-555555555555"
)

type managementRepositoryFake struct {
	mu sync.Mutex

	identity    ControllerIdentity
	key         ControllerKey
	identityErr error
	binding     InstallationBinding
	bindingErr  error
	enrollment  Enrollment
	enrollErr   error
	rotation    KeyRotation
	rotationErr error

	bindingOwners    []string
	enrollmentOwners []string
	rotationIDs      []string
}

func (repository *managementRepositoryFake) ActiveIdentity(context.Context) (ControllerIdentity, ControllerKey, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.identity, repository.key, repository.identityErr
}

func (repository *managementRepositoryFake) Binding(_ context.Context, owner, _ string) (InstallationBinding, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.bindingOwners = append(repository.bindingOwners, owner)
	return repository.binding, repository.bindingErr
}

func (repository *managementRepositoryFake) Enrollment(_ context.Context, owner, _ string) (Enrollment, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.enrollmentOwners = append(repository.enrollmentOwners, owner)
	return repository.enrollment, repository.enrollErr
}

func (repository *managementRepositoryFake) Rotation(_ context.Context, controllerID, rotationID string) (KeyRotation, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.rotationIDs = append(repository.rotationIDs, controllerID+"/"+rotationID)
	return repository.rotation, repository.rotationErr
}

type managementEnrollmentFake struct {
	startResult EnrollmentStartResult
	startErr    error
	pollResult  EnrollmentPollResult
	pollErr     error
	startInput  StartEnrollmentInput
	pollOwner   string
}

func (service *managementEnrollmentFake) Start(_ context.Context, input StartEnrollmentInput) (EnrollmentStartResult, error) {
	service.startInput = input
	return service.startResult, service.startErr
}

func (service *managementEnrollmentFake) Poll(_ context.Context, owner, _ string) (EnrollmentPollResult, error) {
	service.pollOwner = owner
	return service.pollResult, service.pollErr
}

type managementControlsFake struct {
	removeFrame *protocol.BindingRemove
	removeErr   error
	removeOwner string
	removeCalls int
	remove      func()

	rotationFrame      *protocol.KeyRotationPropose
	rotationErr        error
	rotationController string
	rotationCalls      int
}

func (controls *managementControlsFake) RequestBindingRemoval(_ context.Context, owner, _ string) (*protocol.BindingRemove, error) {
	controls.removeOwner = owner
	controls.removeCalls++
	if controls.remove != nil && controls.removeErr == nil {
		controls.remove()
	}
	return controls.removeFrame, controls.removeErr
}

func (controls *managementControlsFake) StartKeyRotation(_ context.Context, controllerID string) (*protocol.KeyRotationPropose, error) {
	controls.rotationController = controllerID
	controls.rotationCalls++
	return controls.rotationFrame, controls.rotationErr
}

type managementSupervisorFake struct {
	mu         sync.Mutex
	snapshot   SupervisorSnapshot
	reconciles int
}

func (supervisor *managementSupervisorFake) Snapshot() SupervisorSnapshot { return supervisor.snapshot }
func (supervisor *managementSupervisorFake) Reconcile() {
	supervisor.mu.Lock()
	supervisor.reconciles++
	supervisor.mu.Unlock()
}
func (supervisor *managementSupervisorFake) reconcileCount() int {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.reconciles
}

func newManagementFixture(t *testing.T) (*ManagementService, *managementRepositoryFake, *managementEnrollmentFake, *managementControlsFake, *managementSupervisorFake) {
	t.Helper()
	repository := &managementRepositoryFake{}
	enrollment := &managementEnrollmentFake{}
	controls := &managementControlsFake{}
	supervisor := &managementSupervisorFake{}
	service, err := newManagementService(repository, enrollment, controls, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, enrollment, controls, supervisor
}

func TestNewManagementServiceRetainsExactSharedGraph(t *testing.T) {
	repository := &Repository{}
	enrollment := &EnrollmentService{}
	controls := &SessionControlService{}
	supervisor := &Supervisor{}
	service, err := NewManagementService(repository, enrollment, controls, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if service.repository != repository || service.enrollment != enrollment || service.controls != controls || service.supervisor != supervisor {
		t.Fatal("management service did not retain the supplied shared graph")
	}
	if _, err = NewManagementService(nil, enrollment, controls, supervisor); err == nil {
		t.Fatal("nil concrete dependency was accepted")
	}
}

func TestManagementStatusIsSanitizedAndStructured(t *testing.T) {
	service, _, _, _, supervisor := newManagementFixture(t)
	supervisor.snapshot = SupervisorSnapshot{
		State: "secret-state", Paused: true, DiagnosticsUnavailable: true, ObserverDropped: 7,
		Diagnostics: SessionLifecycleDiagnostics{PendingCommands: -1, ActiveLeases: 2, ExpiredLeases: -3, OldestPendingAge: 1500 * time.Millisecond},
	}
	status := service.Status()
	if status.Availability != ManagementAvailable || status.State != SessionDisconnected || !status.Paused || status.Outcome != "" || !status.DiagnosticsUnavailable || status.PendingCommands != 0 || status.ActiveLeases != 2 || status.ExpiredLeases != 0 || status.OldestPendingAgeSeconds != 2 || status.ObserverDropped != 7 {
		t.Fatalf("unexpected safe status: %#v", status)
	}
	supervisor.snapshot.Outcome = "credential-and-endpoint-secret"
	if got := service.Status().Outcome; got != ErrorProtocol {
		t.Fatalf("unsafe outcome mapped to %q", got)
	}
}

func TestManagementEnrollmentUsesExplicitOwnerAndReconcilesTerminalPoll(t *testing.T) {
	service, repository, enrollment, _, supervisor := newManagementFixture(t)
	now := time.Now().UTC().Round(0)
	completed := now.Add(time.Minute)
	enrollment.startResult = EnrollmentStartResult{EnrollmentID: managementTestEnrollment, AuthorizationURL: "https://relay.example/authorize", Status: EnrollmentPending, ExpiresAt: now.Add(10 * time.Minute)}
	started, err := service.StartEnrollment(context.Background(), "owner-a", ManagementEnrollmentInput{ConnectionID: "connection-a", InstallationID: 7, RepositoryID: 8})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.startInput.OwnerUserID != "owner-a" || started.EnrollmentID != managementTestEnrollment || started.State != EnrollmentPending {
		t.Fatalf("owner/result mismatch: input=%#v result=%#v", enrollment.startInput, started)
	}

	enrollment.pollResult = EnrollmentPollResult{EnrollmentID: managementTestEnrollment, BindingID: managementTestBinding, Status: EnrollmentAuthorized, ExpiresAt: now.Add(10 * time.Minute)}
	repository.enrollment = Enrollment{EnrollmentID: managementTestEnrollment, BindingID: managementTestBinding, State: EnrollmentAuthorized, CreatedAt: now, UpdatedAt: completed, ExpiresAt: now.Add(10 * time.Minute), CompletedAt: &completed}
	status, err := service.PollEnrollment(context.Background(), "owner-a", managementTestEnrollment)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.pollOwner != "owner-a" || len(repository.enrollmentOwners) != 1 || repository.enrollmentOwners[0] != "owner-a" {
		t.Fatalf("owner not preserved: poll=%q reads=%v", enrollment.pollOwner, repository.enrollmentOwners)
	}
	if status.CreatedAt != now || status.UpdatedAt != completed || status.CompletedAt == nil || *status.CompletedAt != completed || supervisor.reconcileCount() != 1 {
		t.Fatalf("terminal status/reconcile mismatch: status=%#v reconciles=%d", status, supervisor.reconcileCount())
	}
	*repository.enrollment.CompletedAt = time.Time{}
	if status.CompletedAt.IsZero() {
		t.Fatal("management result retained repository timestamp pointer")
	}
}

func TestManagementEnrollmentErrorsAreFixedAndRetryIsBounded(t *testing.T) {
	service, _, enrollment, _, supervisor := newManagementFixture(t)
	enrollment.pollErr = &EnrollmentError{Code: "relay_unavailable", RetryAfter: time.Hour}
	_, err := service.PollEnrollment(context.Background(), "owner-a", managementTestEnrollment)
	var managementErr *ManagementError
	if !errors.As(err, &managementErr) || managementErr.Code != "relay_unavailable" || managementErr.RetryAfter != maxManagementRetryAfter {
		t.Fatalf("unexpected mapped error: %#v", err)
	}
	if strings.Contains(err.Error(), "hour") || supervisor.reconcileCount() != 0 {
		t.Fatalf("unsafe error or reconcile after failure: %q/%d", err, supervisor.reconcileCount())
	}
	enrollment.pollErr = errors.New("https://provider.example/token-secret")
	_, err = service.PollEnrollment(context.Background(), "owner-a", managementTestEnrollment)
	if !IsManagementCode(err, ManagementErrorUnavailable) || strings.Contains(err.Error(), "provider.example") || strings.Contains(err.Error(), "token-secret") {
		t.Fatalf("raw error escaped: %v", err)
	}
}

func TestManagementPendingEnrollmentPollDoesNotReconcile(t *testing.T) {
	service, repository, enrollment, _, supervisor := newManagementFixture(t)
	now := time.Now().UTC().Round(0)
	enrollment.pollResult = EnrollmentPollResult{EnrollmentID: managementTestEnrollment, Status: EnrollmentPending, ExpiresAt: now.Add(time.Minute)}
	repository.enrollment = Enrollment{EnrollmentID: managementTestEnrollment, State: EnrollmentPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := service.PollEnrollment(context.Background(), "owner-a", managementTestEnrollment); err != nil {
		t.Fatal(err)
	}
	if supervisor.reconcileCount() != 0 {
		t.Fatalf("pending poll reconciled %d times", supervisor.reconcileCount())
	}
}

func TestManagementBindingOwnerIsolationStateAndReplay(t *testing.T) {
	service, repository, _, controls, supervisor := newManagementFixture(t)
	repository.bindingErr = ErrNotFound
	for _, owner := range []string{"owner-a", "owner-b"} {
		if _, err := service.RemoveBinding(context.Background(), owner, managementTestBinding); !IsManagementCode(err, ManagementErrorBindingMissing) {
			t.Fatalf("owner=%q error=%v", owner, err)
		}
	}
	if controls.removeCalls != 0 {
		t.Fatalf("foreign/missing binding reached controls %d times", controls.removeCalls)
	}

	repository.bindingErr = nil
	repository.binding = InstallationBinding{BindingID: managementTestBinding, State: BindingDenied}
	if _, err := service.RemoveBinding(context.Background(), "owner-a", managementTestBinding); !IsManagementCode(err, ManagementErrorBindingState) {
		t.Fatalf("state conflict error=%v", err)
	}

	updated := time.Now().UTC().Round(0)
	repository.binding = InstallationBinding{BindingID: managementTestBinding, State: BindingRemovalPending, UpdatedAt: updated}
	controls.removeFrame = &protocol.BindingRemove{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemove, managementTestEnrollment, updated), InstallationID: 7, RepositoryID: 8}
	for attempt := 0; attempt < 2; attempt++ {
		status, err := service.RemoveBinding(context.Background(), "owner-a", managementTestBinding)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != BindingRemovalPending || status.UpdatedAt != updated || controls.removeOwner != "owner-a" {
			t.Fatalf("replay mismatch: %#v owner=%q", status, controls.removeOwner)
		}
	}
	if controls.removeCalls != 2 || supervisor.reconcileCount() != 2 || *controls.removeFrame != (protocol.BindingRemove{}) {
		t.Fatalf("calls/reconciles/frame=%d/%d/%#v", controls.removeCalls, supervisor.reconcileCount(), controls.removeFrame)
	}
}

func TestManagementRemovedBindingIsIdempotentWithoutNewMutation(t *testing.T) {
	service, repository, _, controls, supervisor := newManagementFixture(t)
	updated := time.Now().UTC().Round(0)
	repository.binding = InstallationBinding{BindingID: managementTestBinding, State: BindingRemoved, UpdatedAt: updated}
	status, err := service.RemoveBinding(context.Background(), "owner-a", managementTestBinding)
	if err != nil || status.State != BindingRemoved || status.UpdatedAt != updated || controls.removeCalls != 0 || supervisor.reconcileCount() != 0 {
		t.Fatalf("removed replay mismatch: status=%#v err=%v controls=%d reconciles=%d", status, err, controls.removeCalls, supervisor.reconcileCount())
	}
}

func TestManagementRotationUsesActiveIdentityDestroysMaterialAndReadsExactRow(t *testing.T) {
	service, repository, _, controls, supervisor := newManagementFixture(t)
	now := time.Now().UTC().Round(0)
	publicKey := []byte("public-key-material")
	repository.identity = ControllerIdentity{ControllerID: managementTestController, State: ControllerActive}
	repository.key = ControllerKey{ControllerID: managementTestController, KeyID: managementTestKey, State: KeyActive, PublicKey: publicKey, ProtectedKeyRef: "protected-secret-ref"}
	repository.rotation = KeyRotation{RotationID: managementTestRotation, ControllerID: managementTestController, State: RotationCompleted, ExpiresAt: now.Add(15 * time.Minute)}
	controls.rotationFrame = &protocol.KeyRotationPropose{
		Envelope:   protocol.NewEnvelope(protocol.TypeKeyRotationPropose, managementTestEnrollment, now),
		RotationID: managementTestRotation, ControllerID: managementTestController,
		OldKeyID: managementTestKey, NewKeyID: managementTestBinding, NewPublicKey: "new-public-key-material",
	}
	status, err := service.RotateKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.RotationID != managementTestRotation || status.State != RotationCompleted || controls.rotationController != managementTestController || supervisor.reconcileCount() != 1 {
		t.Fatalf("rotation mismatch: %#v controller=%q reconciles=%d", status, controls.rotationController, supervisor.reconcileCount())
	}
	if *controls.rotationFrame != (protocol.KeyRotationPropose{}) || len(repository.rotationIDs) != 1 || repository.rotationIDs[0] != managementTestController+"/"+managementTestRotation {
		t.Fatalf("frame/read not sanitized: frame=%#v reads=%v", controls.rotationFrame, repository.rotationIDs)
	}
	for index, value := range publicKey {
		if value != 0 {
			t.Fatalf("active public key byte %d not destroyed", index)
		}
	}
	for _, resultType := range []reflect.Type{reflect.TypeOf(status), reflect.TypeOf(ManagementBindingStatus{}), reflect.TypeOf(ManagementEnrollmentStatus{})} {
		for _, forbidden := range []string{"ControllerID", "KeyID", "PublicKey", "ProtectedKeyRef", "MessageID"} {
			if _, found := resultType.FieldByName(forbidden); found {
				t.Fatalf("safe DTO %s exposes %s", resultType, forbidden)
			}
		}
	}
}

func TestManagementDoesNotReconcileFailedDurableControls(t *testing.T) {
	service, repository, _, controls, supervisor := newManagementFixture(t)
	now := time.Now().UTC()
	repository.binding = InstallationBinding{BindingID: managementTestBinding, State: BindingAuthorized}
	controls.removeFrame = &protocol.BindingRemove{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemove, managementTestEnrollment, now), InstallationID: 7}
	controls.removeErr = controlFailure(controlErrorPersistence)
	if _, err := service.RemoveBinding(context.Background(), "owner-a", managementTestBinding); !IsManagementCode(err, "persistence_unavailable") {
		t.Fatalf("remove error=%v", err)
	}
	if supervisor.reconcileCount() != 0 || *controls.removeFrame != (protocol.BindingRemove{}) {
		t.Fatalf("failed removal reconciled or retained frame: %d/%#v", supervisor.reconcileCount(), controls.removeFrame)
	}

	repository.identity = ControllerIdentity{ControllerID: managementTestController, State: ControllerActive}
	repository.key = ControllerKey{ControllerID: managementTestController, KeyID: managementTestKey, State: KeyActive, PublicKey: []byte("public")}
	controls.rotationFrame = &protocol.KeyRotationPropose{RotationID: managementTestRotation, NewPublicKey: "public"}
	controls.rotationErr = controlFailure(controlErrorState)
	if _, err := service.RotateKey(context.Background()); !IsManagementCode(err, ManagementErrorRotationConflict) {
		t.Fatalf("rotation error=%v", err)
	}
	if supervisor.reconcileCount() != 0 || *controls.rotationFrame != (protocol.KeyRotationPropose{}) || len(repository.rotationIDs) != 0 {
		t.Fatalf("failed rotation mutated follow-up state: %d/%#v/%v", supervisor.reconcileCount(), controls.rotationFrame, repository.rotationIDs)
	}
}
