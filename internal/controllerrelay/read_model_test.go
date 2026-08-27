package controllerrelay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

const (
	readModelConnectionA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	readModelConnectionB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestRepositoryReadModelIsOwnerScopedDurableAndSanitized(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	insertReadModelConnection(t, repository, readModelConnectionA, "owner", "provider-private-body", now)
	insertReadModelConnection(t, repository, readModelConnectionB, "other-owner", "other-private-body", now)

	latest := "71000000-0000-4000-8000-000000000003"
	middle := "71000000-0000-4000-8000-000000000002"
	oldest := "71000000-0000-4000-8000-000000000001"
	insertReadModelBinding(t, repository, oldest, "owner", readModelConnectionA, BindingAuthorized, 101, 202, now, "")
	insertReadModelBinding(t, repository, middle, "owner", readModelConnectionA, BindingAccessLost, 102, 203, now.Add(time.Minute), ErrorSourceAccessLost)
	insertReadModelBinding(t, repository, latest, "owner", readModelConnectionA, BindingRemovalPending, 103, 204, now.Add(2*time.Minute), "")
	insertReadModelBinding(t, repository, "71000000-0000-4000-8000-000000000004", "owner", readModelConnectionA, BindingRemoved, 104, 205, now.Add(3*time.Minute), "")
	insertReadModelBinding(t, repository, "72000000-0000-4000-8000-000000000001", "other-owner", readModelConnectionB, BindingAuthorized, 101, 202, now.Add(4*time.Minute), "")

	// The read model intentionally keeps durable bindings discoverable after a
	// source is disconnected and without any application/subscription join.
	if _, err := repository.db.Exec(`UPDATE source_connections SET status='disconnected',access_expires_at=NULL,refresh_expires_at=NULL,connected_at=NULL,disconnected_at=?,updated_at=? WHERE id=?`, timestamp(now.Add(5*time.Minute)), timestamp(now.Add(5*time.Minute)), readModelConnectionA); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateKey(context.Background(), testPendingKey(now)); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{
		RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID,
		OldKeyID: repositoryTestKeyID, NewKeyID: repositoryTestNewKeyID,
		State: RotationPrepare, ExpiresAt: now.Add(time.Hour), StateChangedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	if err := repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPrepare, RotationPropose, "", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPropose, RotationConfirm, "", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rotation.State = RotationConfirm
	rotation.UpdatedAt = now.Add(6 * time.Minute)

	assertReadModel := func(repository *Repository) {
		t.Helper()
		got, err := repository.ReadModel(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.RemovableBindings) != 3 {
			t.Fatalf("bindings=%#v", got.RemovableBindings)
		}
		wantOrder := []string{latest, middle, oldest}
		for index, want := range wantOrder {
			if got.RemovableBindings[index].BindingID != want {
				t.Fatalf("binding order=%#v", got.RemovableBindings)
			}
		}
		if got.RemovableBindings[1].State != BindingAccessLost || got.RemovableBindings[1].InstallationID != 102 || got.RemovableBindings[1].RepositoryID != 203 {
			t.Fatalf("access-lost binding=%#v", got.RemovableBindings[1])
		}
		if !got.KeyRotation.InProgress || got.KeyRotation.State != RotationConfirm || !got.KeyRotation.UpdatedAt.Equal(rotation.UpdatedAt) {
			t.Fatalf("rotation=%#v", got.KeyRotation)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"provider-private-body", "other-private-body", repositoryTestControllerID, repositoryTestKeyID, repositoryTestNewKeyID, repositoryTestRotationID, "last_error", "source_access_lost"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("read model leaked %q: %s", forbidden, encoded)
			}
		}
	}
	assertReadModel(repository)

	other, err := repository.ReadModel(context.Background(), "other-owner")
	if err != nil || len(other.RemovableBindings) != 1 || other.RemovableBindings[0].ConnectionID != readModelConnectionB {
		t.Fatalf("other-owner model=%#v err=%v", other, err)
	}

	if err := repository.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertReadModel(NewRepository(reopened))
}

func TestRepositoryReadModelRotationTerminalAndMalformedStatesFailClosed(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	insertReadModelConnection(t, repository, readModelConnectionA, "owner", "provider-canary", now)

	empty, err := repository.ReadModel(context.Background(), "owner")
	if err != nil || empty.RemovableBindings == nil || empty.KeyRotation.InProgress {
		t.Fatalf("empty read model=%#v err=%v", empty, err)
	}
	if err = repository.CreateKey(context.Background(), testPendingKey(now)); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: repositoryTestNewKeyID, State: RotationPrepare, ExpiresAt: now.Add(time.Hour), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err = repository.CreateRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	live, err := repository.ReadModel(context.Background(), "owner")
	if err != nil || !live.KeyRotation.InProgress || live.KeyRotation.State != RotationPrepare {
		t.Fatalf("live rotation=%#v err=%v", live.KeyRotation, err)
	}
	if err = repository.CASRotationState(context.Background(), repositoryTestControllerID, repositoryTestRotationID, RotationPrepare, RotationFailed, ErrorRotationFailed, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	terminal, err := repository.ReadModel(context.Background(), "owner")
	if err != nil || terminal.KeyRotation.InProgress {
		t.Fatalf("terminal rotation=%#v err=%v", terminal.KeyRotation, err)
	}

	insertReadModelBinding(t, repository, "73000000-0000-4000-8000-000000000001", "owner", readModelConnectionA, BindingAuthorized, 1, 1, now, "")
	if _, err = repository.db.Exec(`UPDATE relay_installation_bindings SET updated_at='provider-raw-body' WHERE binding_id='73000000-0000-4000-8000-000000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ReadModel(context.Background(), "owner"); err == nil {
		t.Fatalf("malformed durable data error=%v", err)
	}
}

func TestRepositoryReadModelBindingCapFailsWithoutTruncation(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	insertReadModelConnection(t, repository, readModelConnectionA, "owner", "provider-canary", now)
	tx, err := repository.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,'owner',?,?,?,?,'authorized',?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= maxRelayReadModelBindings; index++ {
		at := now.Add(time.Duration(index) * time.Second)
		if _, err = statement.Exec(uuid.NewString(), readModelConnectionA, repositoryTestControllerID, int64(index+1), int64(index+1), timestamp(at), timestamp(at), timestamp(at)); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := repository.ReadModel(context.Background(), "owner")
	if !errors.Is(err, ErrState) || len(got.RemovableBindings) != maxRelayReadModelBindings+1 {
		t.Fatalf("cap result len=%d err=%v", len(got.RemovableBindings), err)
	}
}

func TestRepositoryReadModelExactQueryUsesOwnerRemovableIndex(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	insertReadModelConnection(t, repository, readModelConnectionA, "owner", "provider-canary", now)
	tx, err := repository.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := tx.Prepare(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at,completed_at) VALUES(?,'owner',?,?,?,?,'removed',?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4096; index++ {
		if _, err = terminal.Exec(uuid.NewString(), readModelConnectionA, repositoryTestControllerID, index+1, index+1, timestamp(now), timestamp(now), timestamp(now), timestamp(now)); err != nil {
			terminal.Close()
			t.Fatal(err)
		}
	}
	if err = terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for index, state := range []string{BindingAuthorized, BindingAccessLost, BindingRemovalPending} {
		errorCode := ""
		if state == BindingAccessLost {
			errorCode = ErrorSourceAccessLost
		}
		insertReadModelBinding(t, repository, uuid.NewString(), "owner", readModelConnectionA, state, int64(5000+index), int64(6000+index), now.Add(time.Duration(index)*time.Minute), errorCode)
	}

	rows, err := repository.db.Query(`EXPLAIN QUERY PLAN `+relayReadModelBindingsQuery, "owner")
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan.WriteString(strings.ToLower(detail))
		plan.WriteByte('\n')
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "using index relay_binding_owner_removable") || strings.Contains(plan.String(), "temp b-tree") {
		t.Fatalf("exact read-model query plan=%q", plan.String())
	}
	got, err := repository.ReadModel(context.Background(), "owner")
	if err != nil || len(got.RemovableBindings) != 3 {
		t.Fatalf("read model after terminal history len=%d err=%v", len(got.RemovableBindings), err)
	}
}

func insertReadModelConnection(t *testing.T, repository *Repository, connectionID, owner, providerLogin string, now time.Time) {
	t.Helper()
	if _, err := repository.db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES(?,?,'github','connected',?, ?,1,?,?,?,?,?)`, connectionID, owner, connectionID, providerLogin, timestamp(now.Add(time.Hour)), timestamp(now.Add(24*time.Hour)), timestamp(now), timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
}

func insertReadModelBinding(t *testing.T, repository *Repository, bindingID, owner, connectionID, state string, installationID, repositoryID int64, updatedAt time.Time, errorCode string) {
	t.Helper()
	completedAt := any(nil)
	if state == BindingRemoved || state == BindingFailed || state == BindingDenied || state == BindingExpired {
		completedAt = timestamp(updatedAt)
	}
	if _, err := repository.db.Exec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,last_error_code,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, bindingID, owner, connectionID, repositoryTestControllerID, installationID, repositoryID, state, timestamp(updatedAt), nullable(errorCode), timestamp(updatedAt), timestamp(updatedAt), completedAt); err != nil {
		t.Fatal(err)
	}
}
