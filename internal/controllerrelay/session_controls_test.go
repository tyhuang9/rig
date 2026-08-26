package controllerrelay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
)

func TestBindingRemovalIsOwnerScopedAtomicReplayableAndConcurrent(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	credentials := newMemoryControlCredentials()
	service := newTestControlService(t, repository, credentials, now.Add(2*time.Minute))

	first, err := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if first.InstallationID != binding.InstallationID || first.RepositoryID != binding.RepositoryID {
		t.Fatalf("binding remove scope = %#v", first)
	}
	pending, err := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil || pending.State != BindingRemovalPending {
		t.Fatalf("pending binding = %#v err=%v", pending, err)
	}

	const workers = 12
	results := make(chan *protocol.BindingRemove, workers)
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			frame, requestErr := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID)
			results <- frame
			errorsSeen <- requestErr
		}()
	}
	wait.Wait()
	for index := 0; index < workers; index++ {
		if requestErr := <-errorsSeen; requestErr != nil {
			t.Fatal(requestErr)
		}
		frame := <-results
		if frame.MessageID != first.MessageID || !frame.SentAt.Equal(first.SentAt) || frame.InstallationID != first.InstallationID || frame.RepositoryID != first.RepositoryID {
			t.Fatalf("concurrent request changed immutable command: %#v != %#v", frame, first)
		}
	}
	if _, err = service.RequestBindingRemoval(context.Background(), "other-owner", binding.BindingID); err == nil {
		t.Fatal("cross-owner removal succeeded")
	}

	wrong := protocol.BindingRemoved{Envelope: protocol.NewEnvelope(protocol.TypeBindingRemoved, uuid.NewString(), now), TargetMessageID: first.MessageID, InstallationID: first.InstallationID, RepositoryID: first.RepositoryID + 1}
	if _, err = service.Handle(context.Background(), testReadyControlSession(t, repository, now, repositoryTestKeyID), &wrong); err == nil {
		t.Fatal("wrong-scope binding response succeeded")
	}
	stillPending, _ := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if stillPending.State != BindingRemovalPending {
		t.Fatalf("wrong response mutated binding: %#v", stillPending)
	}

	removed := wrong
	removed.MessageID = uuid.NewString()
	removed.RepositoryID = first.RepositoryID
	result, err := service.Handle(context.Background(), testReadyControlSession(t, repository, now, repositoryTestKeyID), &removed)
	if err != nil || result.Action != ControlContinue || result.Response != nil {
		t.Fatalf("binding completion = %#v err=%v", result, err)
	}
	removed.MessageID = uuid.NewString()
	if _, err = service.Handle(context.Background(), testReadyControlSession(t, repository, now, repositoryTestKeyID), &removed); err != nil {
		t.Fatalf("duplicate binding response = %v", err)
	}
	terminal, _ := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if terminal.State != BindingRemoved || terminal.CompletedAt == nil {
		t.Fatalf("removed binding = %#v", terminal)
	}
	var commandRows int
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_outbound_commands WHERE controller_id=? AND binding_id=?`, binding.ControllerID, binding.BindingID).Scan(&commandRows); err != nil || commandRows != 1 {
		t.Fatalf("command rows=%d err=%v", commandRows, err)
	}
}

func TestBindingRemovalRollsBackStateWhenCommandCannotPersist(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	if _, err := repository.db.Exec(`CREATE TRIGGER test_abort_binding_command BEFORE INSERT ON relay_outbound_commands WHEN NEW.command_type='binding.remove' BEGIN SELECT RAISE(ABORT,'forced command failure'); END`); err != nil {
		t.Fatal(err)
	}
	service := newTestControlService(t, repository, newMemoryControlCredentials(), now.Add(time.Minute))
	if _, err := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID); err == nil {
		t.Fatal("forced command failure succeeded")
	}
	current, err := repository.Binding(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil || current.State != BindingAuthorized {
		t.Fatalf("binding escaped rollback = %#v err=%v", current, err)
	}
}

func TestKeyRotationReplaysExactCommandsAndCompletesOnlyAfterFencedReady(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))

	propose, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
	if err != nil || replayed.MessageID != propose.MessageID || !replayed.SentAt.Equal(propose.SentAt) || replayed.NewKeyID != propose.NewKeyID || replayed.NewPublicKey != propose.NewPublicKey {
		t.Fatalf("proposal replay = %#v err=%v want=%#v", replayed, err, propose)
	}
	if credentials.removeCalls != 0 {
		t.Fatalf("replay unexpectedly removed protected candidate=%d", credentials.removeCalls)
	}
	var clearedLosers int
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE state='revoked' AND activated_at IS NULL AND protected_key_cleared_at IS NOT NULL`).Scan(&clearedLosers); err != nil || clearedLosers != 0 {
		t.Fatalf("serialized replay created loser clear markers=%d err=%v", clearedLosers, err)
	}
	rotation, err := repository.Rotation(context.Background(), repositoryTestControllerID, propose.RotationID)
	if err != nil || rotation.State != RotationPropose || rotation.OldKeyID != repositoryTestKeyID || rotation.NewKeyID != propose.NewKeyID {
		t.Fatalf("proposed rotation = %#v err=%v", rotation, err)
	}
	oldIdentity, oldKey, err := repository.SessionAuthenticationIdentity(context.Background())
	if err != nil || oldIdentity.ControllerID != repositoryTestControllerID || oldKey.KeyID != repositoryTestKeyID {
		t.Fatalf("pre-finalized authentication key = %#v %#v err=%v", oldIdentity, oldKey, err)
	}

	session := testReadyControlSession(t, repository, now, repositoryTestKeyID)
	nonce := bytes.Repeat([]byte{0x6a}, protocol.NonceBytes)
	challenge := &protocol.KeyRotationChallenge{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationChallenge, uuid.NewString(), now.Add(time.Minute)), TargetMessageID: propose.MessageID, RotationID: propose.RotationID, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: now.Add(4 * time.Minute)}
	result, err := service.Handle(context.Background(), session, challenge)
	confirm, ok := result.Response.(*protocol.KeyRotationConfirm)
	if err != nil || !ok || result.Action != ControlContinue || confirm.RotationID != propose.RotationID {
		t.Fatalf("challenge result = %#v err=%v", result, err)
	}
	if challenge.ServerNonce != "" {
		t.Fatal("challenge nonce was retained in caller frame")
	}
	pendingKey, err := repository.Key(context.Background(), repositoryTestControllerID, propose.NewKeyID)
	if err != nil {
		t.Fatal(err)
	}
	decodedPublic, err := base64.RawURLEncoding.DecodeString(propose.NewPublicKey)
	if err != nil || !bytes.Equal(decodedPublic, pendingKey.PublicKey) {
		t.Fatalf("proposal public key mismatch err=%v", err)
	}
	clear(decodedPublic)
	transcript, err := protocol.KeyRotationTranscript(protocol.RotationProof{RotationID: rotation.RotationID, ControllerID: rotation.ControllerID, OldKeyID: rotation.OldKeyID, NewKeyID: rotation.NewKeyID, NewPublicKey: propose.NewPublicKey, SessionID: session.SessionID, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: now.Add(4 * time.Minute)})
	if err != nil || !protocol.Verify(ed25519.PublicKey(pendingKey.PublicKey), transcript, confirm.Signature) {
		t.Fatalf("confirmation signature invalid err=%v", err)
	}
	clear(transcript)

	replayChallenge := &protocol.KeyRotationChallenge{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationChallenge, uuid.NewString(), now.Add(2*time.Minute)), TargetMessageID: propose.MessageID, RotationID: propose.RotationID, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: now.Add(4 * time.Minute)}
	replayResult, err := service.Handle(context.Background(), session, replayChallenge)
	replayedConfirm, ok := replayResult.Response.(*protocol.KeyRotationConfirm)
	if err != nil || !ok || replayedConfirm.MessageID != confirm.MessageID || !replayedConfirm.SentAt.Equal(confirm.SentAt) || replayedConfirm.Signature != confirm.Signature {
		t.Fatalf("confirmation replay = %#v err=%v", replayResult, err)
	}
	changedNonce := bytes.Repeat([]byte{0x6b}, protocol.NonceBytes)
	conflictingChallenge := &protocol.KeyRotationChallenge{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationChallenge, uuid.NewString(), now.Add(2*time.Minute)), TargetMessageID: propose.MessageID, RotationID: propose.RotationID, ServerNonce: base64.RawURLEncoding.EncodeToString(changedNonce), ExpiresAt: now.Add(4 * time.Minute)}
	if conflicting, conflictErr := service.Handle(context.Background(), session, conflictingChallenge); conflictErr == nil || conflicting.Response != nil {
		t.Fatalf("changed challenge replay response=%#v err=%v", conflicting, conflictErr)
	}
	wrongSession := session
	wrongSession.SessionID = "88888888-8888-4888-8888-888888888888"
	wrongSessionChallenge := &protocol.KeyRotationChallenge{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationChallenge, uuid.NewString(), now.Add(2*time.Minute)), TargetMessageID: propose.MessageID, RotationID: propose.RotationID, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: now.Add(4 * time.Minute)}
	if conflicting, conflictErr := service.Handle(context.Background(), wrongSession, wrongSessionChallenge); conflictErr == nil || conflicting.Response != nil {
		t.Fatalf("wrong-session challenge response=%#v err=%v", conflicting, conflictErr)
	}

	confirmed := &protocol.KeyRotationConfirmed{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirmed, uuid.NewString(), now.Add(3*time.Minute)), TargetMessageID: confirm.MessageID, RotationID: rotation.RotationID}
	wrongConfirmedSession := session
	wrongConfirmedSession.KeyID = propose.NewKeyID
	wrongResult, wrongErr := service.Handle(context.Background(), wrongConfirmedSession, confirmed)
	if wrongErr == nil || wrongResult.Response != nil {
		t.Fatalf("wrong-key Confirmed response=%#v err=%v", wrongResult, wrongErr)
	}
	unchangedRotation, err := repository.Rotation(context.Background(), repositoryTestControllerID, rotation.RotationID)
	if err != nil || unchangedRotation.State != RotationConfirm {
		t.Fatalf("wrong-key Confirmed mutated rotation=%#v err=%v", unchangedRotation, err)
	}
	if _, err = repository.ControlCommandForAggregate(context.Background(), repositoryTestControllerID, "", rotation.RotationID, "finalize"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-key Confirmed prepared Finalize: %v", err)
	}
	result, err = service.Handle(context.Background(), session, confirmed)
	finalize, ok := result.Response.(*protocol.KeyRotationFinalize)
	if err != nil || !ok || result.Action != ControlContinue || !finalize.RetireOldKey {
		t.Fatalf("confirmed result = %#v err=%v", result, err)
	}
	rotation, _ = repository.Rotation(context.Background(), repositoryTestControllerID, rotation.RotationID)
	if rotation.State != RotationFinalize {
		t.Fatalf("confirmed rotation state=%s", rotation.State)
	}
	_, ambiguousCandidates, err := repository.SessionAuthenticationCandidates(context.Background())
	if err != nil || len(ambiguousCandidates) != 2 || ambiguousCandidates[0].KeyID != repositoryTestKeyID || ambiguousCandidates[1].KeyID != propose.NewKeyID {
		t.Fatalf("ambiguous authentication candidates = %#v err=%v", ambiguousCandidates, err)
	}
	preResponseIdentity, preResponseKey, err := repository.SessionAuthenticationIdentity(context.Background())
	if err != nil || preResponseIdentity.ControllerID != repositoryTestControllerID || preResponseKey.KeyID != repositoryTestKeyID {
		t.Fatalf("authentication changed before Finalized: %#v err=%v", preResponseKey, err)
	}

	confirmed.MessageID = uuid.NewString()
	replayResult, err = service.Handle(context.Background(), session, confirmed)
	replayedFinalize, ok := replayResult.Response.(*protocol.KeyRotationFinalize)
	if err != nil || !ok || replayedFinalize.MessageID != finalize.MessageID || !replayedFinalize.SentAt.Equal(finalize.SentAt) {
		t.Fatalf("finalize replay = %#v err=%v", replayResult, err)
	}
	pendingSession := session
	pendingSession.KeyID = propose.NewKeyID
	recoveryFrames, err := service.Pending(context.Background(), pendingSession, 10)
	if err != nil || len(recoveryFrames) != 1 {
		t.Fatalf("pending-key finalize recovery = %#v err=%v", recoveryFrames, err)
	}
	recoveryFinalize, ok := recoveryFrames[0].(*protocol.KeyRotationFinalize)
	if !ok || recoveryFinalize.MessageID != finalize.MessageID || !recoveryFinalize.SentAt.Equal(finalize.SentAt) || recoveryFinalize.RotationID != finalize.RotationID {
		t.Fatalf("pending-key exact finalize replay = %#v", recoveryFrames[0])
	}
	wrongFinalized := &protocol.KeyRotationFinalized{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalized, uuid.NewString(), now.Add(4*time.Minute)), TargetMessageID: finalize.MessageID, RotationID: rotation.RotationID, RetiredKeyID: repositoryTestNewKeyID}
	if _, err = service.Handle(context.Background(), session, wrongFinalized); err == nil {
		t.Fatal("wrong retired key finalized rotation")
	}
	finalized := *wrongFinalized
	finalized.MessageID = uuid.NewString()
	finalized.RetiredKeyID = repositoryTestKeyID
	result, err = service.Handle(context.Background(), pendingSession, &finalized)
	if err != nil || result.Action != ControlReconnect || result.Response != nil {
		t.Fatalf("finalized result = %#v err=%v", result, err)
	}
	_, finalizedCandidates, err := repository.SessionAuthenticationCandidates(context.Background())
	if err != nil || len(finalizedCandidates) != 1 || finalizedCandidates[0].KeyID != propose.NewKeyID {
		t.Fatalf("finalized authentication candidates = %#v err=%v", finalizedCandidates, err)
	}
	_, selected, err := repository.SessionAuthenticationIdentity(context.Background())
	if err != nil || selected.KeyID != propose.NewKeyID || selected.State != KeyPending {
		t.Fatalf("post-finalized authentication key = %#v err=%v", selected, err)
	}

	readyAt := now.Add(5 * time.Minute)
	ready := SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 2, State: SessionReady, KeyID: propose.NewKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err = repository.AdvanceSessionStatus(context.Background(), 1, 1, ready); err != nil {
		t.Fatal(err)
	}
	service.config.Now = func() time.Time { return readyAt.Add(time.Second) }
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, propose.NewKeyID, 1, 3); err == nil {
		t.Fatal("wrong Ready fence completed rotation")
	}
	if _, err = repository.db.Exec(fmt.Sprintf(`CREATE TRIGGER test_abort_control_new_key_activation BEFORE UPDATE OF state ON relay_controller_keys WHEN OLD.key_id='%s' AND NEW.state='active' BEGIN SELECT RAISE(ABORT,'forced rotation rollback'); END`, propose.NewKeyID)); err != nil {
		t.Fatal(err)
	}
	removalsBeforeRollback := credentials.removeCalls
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, propose.NewKeyID, 1, 2); err == nil {
		t.Fatal("forced Ready completion rollback succeeded")
	}
	if credentials.removeCalls != removalsBeforeRollback || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) {
		t.Fatal("old protected key was removed before durable completion")
	}
	if _, err = repository.db.Exec(`DROP TRIGGER test_abort_control_new_key_activation`); err != nil {
		t.Fatal(err)
	}
	credentials.failNextRemoval(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID))
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, propose.NewKeyID, 1, 2); err == nil || !strings.Contains(err.Error(), controlErrorCredential) {
		t.Fatalf("cleanup failure = %v", err)
	}
	completedAfterCleanupFailure, _ := repository.Rotation(context.Background(), repositoryTestControllerID, rotation.RotationID)
	if completedAfterCleanupFailure.State != RotationCompleted || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) {
		t.Fatalf("cleanup failure lost durable state/file: rotation=%#v", completedAfterCleanupFailure)
	}
	var oldKeyCleared any
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, repositoryTestKeyID).Scan(&oldKeyCleared); err != nil || oldKeyCleared != nil {
		t.Fatalf("delete failure old-key marker=%v err=%v", oldKeyCleared, err)
	}
	if _, err = repository.db.Exec(`CREATE TRIGGER test_abort_completed_key_clear BEFORE UPDATE OF protected_key_cleared_at ON relay_controller_keys BEGIN SELECT RAISE(ABORT,'forced completed key clear failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, propose.NewKeyID, 1, 2); err == nil || !strings.Contains(err.Error(), controlErrorPersistence) {
		t.Fatalf("completed cleanup mark failure = %v", err)
	}
	if credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) {
		t.Fatal("completed cleanup mark failure retained deleted file")
	}
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, repositoryTestKeyID).Scan(&oldKeyCleared); err != nil || oldKeyCleared != nil {
		t.Fatalf("completed mark failure marker=%v err=%v", oldKeyCleared, err)
	}
	if _, err = repository.db.Exec(`DROP TRIGGER test_abort_completed_key_clear`); err != nil {
		t.Fatal(err)
	}
	service.config.Now = func() time.Time { return readyAt.Add(3 * time.Minute) }
	recovered, recoverErr := service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if recoverErr != nil || recovered.Cleaned != 0 {
		t.Fatalf("completed cleanup mark recovery=%#v err=%v", recovered, recoverErr)
	}
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, propose.NewKeyID, 1, 2); err != nil {
		t.Fatalf("completed cleanup mark retry = %v", err)
	}
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, propose.NewKeyID, 1, 2); err != nil {
		t.Fatalf("absent old key cleanup = %v", err)
	}
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, repositoryTestKeyID).Scan(&oldKeyCleared); err != nil || oldKeyCleared == nil {
		t.Fatalf("completed old-key marker=%v err=%v", oldKeyCleared, err)
	}
	completed, _ := repository.Rotation(context.Background(), repositoryTestControllerID, rotation.RotationID)
	_, active, err := repository.ActiveIdentity(context.Background())
	if err != nil || completed.State != RotationCompleted || active.KeyID != propose.NewKeyID || active.State != KeyActive {
		t.Fatalf("completed=%#v active=%#v err=%v", completed, active, err)
	}
	if credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, propose.NewKeyID)) {
		t.Fatal("cleanup removed wrong protected key")
	}
}

func TestExactFinalizedLostResponseCompletesAfterExpiryWithoutWeakeningCorrelation(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	pending := testPendingKey(now)
	if err := repository.CreateKey(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: pending.KeyID, State: RotationPrepare, ExpiresAt: now.Add(5 * time.Minute), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	for index, state := range []string{RotationPropose, RotationConfirm, RotationNewKeyAuth, RotationFinalize} {
		at := now.Add(time.Duration(index+1) * time.Minute)
		if _, err := repository.db.Exec(`UPDATE relay_key_rotations SET state=?,state_changed_at=?,updated_at=? WHERE controller_id=? AND rotation_id=?`, state, timestamp(at), timestamp(at), rotation.ControllerID, rotation.RotationID); err != nil {
			t.Fatal(err)
		}
	}
	finalize := &protocol.KeyRotationFinalize{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalize, uuid.NewString(), now.Add(4*time.Minute)), RotationID: rotation.RotationID, RetireOldKey: true}
	command, err := outboundCommand(repositoryTestControllerID, "", rotation.RotationID, "finalize", finalize)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.PrepareControlCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	credentials := newMemoryControlCredentials()
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, pending.KeyID, pending.PublicKey)
	service := newTestControlService(t, repository, credentials, now.Add(10*time.Minute))
	exact := protocol.KeyRotationFinalized{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationFinalized, uuid.NewString(), now.Add(10*time.Minute)), TargetMessageID: command.MessageID, RotationID: rotation.RotationID, RetiredKeyID: rotation.OldKeyID}

	wrongResponses := []protocol.KeyRotationFinalized{exact, exact, exact}
	wrongResponses[0].TargetMessageID = uuid.NewString()
	wrongResponses[1].RotationID = uuid.NewString()
	wrongResponses[2].RetiredKeyID = rotation.NewKeyID
	for index := range wrongResponses {
		wrongResponses[index].MessageID = uuid.NewString()
		if _, err := service.Handle(context.Background(), testReadyControlSession(t, repository, now, repositoryTestKeyID), &wrongResponses[index]); err == nil {
			t.Fatalf("wrong finalized response %d succeeded", index)
		}
		stored, loadErr := repository.LoadControlCommand(context.Background(), repositoryTestControllerID, command.MessageID)
		if loadErr != nil || stored.State != CommandPrepared || stored.CompletedAt != nil {
			t.Fatalf("wrong finalized response %d mutated command=%#v err=%v", index, stored, loadErr)
		}
	}

	result, err := service.Handle(context.Background(), testReadyControlSession(t, repository, now, repositoryTestKeyID), &exact)
	if err != nil || result.Action != ControlReconnect {
		t.Fatalf("delayed exact finalized = %#v err=%v", result, err)
	}
	stored, err := repository.LoadControlCommand(context.Background(), repositoryTestControllerID, command.MessageID)
	if err != nil || stored.State != CommandCompleted || stored.CompletedAt == nil || !stored.CompletedAt.After(rotation.ExpiresAt) {
		t.Fatalf("delayed finalized command=%#v err=%v", stored, err)
	}
	exact.MessageID = uuid.NewString()
	if _, err = service.Handle(context.Background(), testReadyControlSession(t, repository, now, repositoryTestKeyID), &exact); err != nil {
		t.Fatalf("delayed exact finalized replay = %v", err)
	}

	readyAt := now.Add(11 * time.Minute)
	ready := SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 2, State: SessionReady, KeyID: pending.KeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
	if err = repository.AdvanceSessionStatus(context.Background(), 1, 1, ready); err != nil {
		t.Fatal(err)
	}
	service.config.Now = func() time.Time { return now.Add(12 * time.Minute) }
	if err = service.CompleteRotationAfterFencedReady(context.Background(), repositoryTestControllerID, pending.KeyID, 1, 2); err != nil {
		t.Fatalf("post-expiry fenced completion = %v", err)
	}
	var cleared any
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, rotation.OldKeyID).Scan(&cleared); err != nil || cleared == nil {
		t.Fatalf("completed old-key clear marker=%v err=%v", cleared, err)
	}
}

func TestRevokedControllerKeyRecoveryPagesPastFailuresAndPreservesLiveKeys(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))

	activePublicKey := bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize)
	pending := testPendingKey(now)
	if err := repository.CreateKey(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, activePublicKey)
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, pending.KeyID, pending.PublicKey)

	revokedKeyIDs := []string{
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
		"10000000-0000-4000-8000-000000000003",
		"10000000-0000-4000-8000-000000000004",
	}
	rotationIDs := []string{
		"70000000-0000-4000-8000-000000000001",
		"70000000-0000-4000-8000-000000000002",
		"70000000-0000-4000-8000-000000000003",
		"70000000-0000-4000-8000-000000000004",
	}
	for index, keyID := range revokedKeyIDs {
		revokedAt := now.Add(time.Duration(index+1) * time.Second)
		key := ControllerKey{
			KeyID:           keyID,
			ControllerID:    repositoryTestControllerID,
			PublicKey:       bytes.Repeat([]byte{byte(index + 2)}, ed25519.PublicKeySize),
			Algorithm:       KeyAlgorithmEd25519,
			State:           KeyRevoked,
			ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, keyID),
			CreatedAt:       now.Add(-time.Hour),
			UpdatedAt:       revokedAt,
			RevokedAt:       &revokedAt,
		}
		if err := repository.CreateKey(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, keyID, key.PublicKey)
		state, oldKeyID, newKeyID, errorCode := RotationCompleted, keyID, repositoryTestKeyID, any(nil)
		if index == len(revokedKeyIDs)-1 {
			state, oldKeyID, newKeyID, errorCode = RotationFailed, repositoryTestKeyID, keyID, ErrorRotationFailed
		}
		if _, err := repository.db.Exec(`INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,last_error_code,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, rotationIDs[index], repositoryTestControllerID, oldKeyID, newKeyID, state, timestamp(now.Add(time.Hour)), timestamp(revokedAt), errorCode, timestamp(now.Add(-time.Hour)), timestamp(revokedAt), timestamp(revokedAt)); err != nil {
			t.Fatal(err)
		}
	}
	protectedNonCandidates := []string{
		"20000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
	}
	for index, keyID := range protectedNonCandidates {
		revokedAt := now.Add(time.Duration(index+10) * time.Second)
		key := ControllerKey{KeyID: keyID, ControllerID: repositoryTestControllerID, PublicKey: bytes.Repeat([]byte{byte(index + 20)}, ed25519.PublicKeySize), Algorithm: KeyAlgorithmEd25519, State: KeyRevoked, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, keyID), CreatedAt: now, UpdatedAt: revokedAt, RevokedAt: &revokedAt}
		if err := repository.CreateKey(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, keyID, key.PublicKey)
	}
	if _, err := repository.db.Exec(`INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,created_at,updated_at) VALUES('80000000-0000-4000-8000-000000000001',?,?,?,'prepare',?,?,?,?)`, repositoryTestControllerID, repositoryTestKeyID, protectedNonCandidates[0], timestamp(now.Add(time.Hour)), timestamp(now), timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,key_id,attempt,state_changed_at,updated_at) VALUES(?,1,1,'authenticating',?,0,?,?)`, repositoryTestControllerID, protectedNonCandidates[1], timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}

	failedRef := ProtectedKeyRef(repositoryTestControllerID, revokedKeyIDs[0])
	credentials.failNextRemoval(failedRef)
	first, err := service.RecoverRevokedControllerKeys(context.Background(), "", 2)
	if err == nil || !strings.Contains(err.Error(), controlErrorCredential) || strings.Contains(err.Error(), failedRef) || strings.Contains(err.Error(), revokedKeyIDs[0]) {
		t.Fatalf("first cleanup error was not sanitized: %v", err)
	}
	if len(first.Candidates) != 2 || first.Complete || first.NextCursor == "" || first.Candidates[0].KeyID != revokedKeyIDs[0] || first.Candidates[1].KeyID != revokedKeyIDs[1] {
		t.Fatalf("first cleanup page = %#v", first)
	}
	if !credentials.has(failedRef) || credentials.has(ProtectedKeyRef(repositoryTestControllerID, revokedKeyIDs[1])) || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, revokedKeyIDs[2])) || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, revokedKeyIDs[3])) {
		t.Fatal("cleanup failure starved or skipped a later key in the page")
	}

	second, err := service.RecoverRevokedControllerKeys(context.Background(), first.NextCursor, 2)
	if err != nil || len(second.Candidates) != 2 || second.Complete || second.NextCursor == "" || second.Candidates[0].KeyID != revokedKeyIDs[2] || second.Candidates[1].KeyID != revokedKeyIDs[3] {
		t.Fatalf("second cleanup page = %#v err=%v", second, err)
	}
	if credentials.has(ProtectedKeyRef(repositoryTestControllerID, revokedKeyIDs[2])) || credentials.has(ProtectedKeyRef(repositoryTestControllerID, revokedKeyIDs[3])) {
		t.Fatal("cursor did not advance past the saturated first page")
	}
	terminal, err := service.RecoverRevokedControllerKeys(context.Background(), second.NextCursor, 2)
	if err != nil || len(terminal.Candidates) != 0 || !terminal.Complete || terminal.NextCursor != "" {
		t.Fatalf("terminal cleanup page = %#v err=%v", terminal, err)
	}

	retry, err := service.RecoverRevokedControllerKeys(context.Background(), "", 1)
	if err != nil || len(retry.Candidates) != 1 || retry.Candidates[0].KeyID != revokedKeyIDs[0] || credentials.has(failedRef) {
		t.Fatalf("failed cleanup retry = %#v err=%v", retry, err)
	}
	if !credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, pending.KeyID)) {
		t.Fatal("recovery removed an active or pending protected key")
	}
	for _, keyID := range protectedNonCandidates {
		if !credentials.has(ProtectedKeyRef(repositoryTestControllerID, keyID)) {
			t.Fatalf("recovery removed live-rotation/session-referenced key %s", keyID)
		}
	}
	if _, err = service.RecoverRevokedControllerKeys(context.Background(), "not-a-cursor", 1); err == nil || !strings.Contains(err.Error(), controlErrorInvalid) {
		t.Fatalf("invalid cleanup cursor error = %v", err)
	}
	var cleared int
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE key_id IN (?,?,?,?) AND protected_key_cleared_at IS NOT NULL`, revokedKeyIDs[0], revokedKeyIDs[1], revokedKeyIDs[2], revokedKeyIDs[3]).Scan(&cleared); err != nil || cleared != len(revokedKeyIDs) {
		t.Fatalf("cleared cleanup intents=%d err=%v", cleared, err)
	}
	again, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || len(again.Candidates) != 0 || !again.Complete {
		t.Fatalf("cleared rows were reselected: %#v err=%v", again, err)
	}
}

func TestConcurrentKeyRotationRequestsConvergeOnOneProtectedKeyAndProposal(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	type outcome struct {
		frame *protocol.KeyRotationPropose
		err   error
	}
	const workers = 8
	start := make(chan struct{})
	results := make(chan outcome, workers)
	for index := 0; index < workers; index++ {
		go func() {
			<-start
			frame, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
			results <- outcome{frame: frame, err: err}
		}()
	}
	close(start)
	var first *protocol.KeyRotationPropose
	for index := 0; index < workers; index++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if first == nil {
			first = result.frame
			continue
		}
		if result.frame.MessageID != first.MessageID || !result.frame.SentAt.Equal(first.SentAt) || result.frame.RotationID != first.RotationID || result.frame.NewKeyID != first.NewKeyID || result.frame.NewPublicKey != first.NewPublicKey {
			t.Fatalf("concurrent proposal changed: %#v != %#v", result.frame, first)
		}
	}
	var rotations, pendingKeys, commands int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM relay_key_rotations WHERE state='propose'`:                         &rotations,
		`SELECT COUNT(*) FROM relay_controller_keys WHERE state='pending'`:                       &pendingKeys,
		`SELECT COUNT(*) FROM relay_outbound_commands WHERE command_type='key.rotation.propose'`: &commands,
	} {
		if err := repository.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if rotations != 1 || pendingKeys != 1 || commands != 1 || credentials.removeCalls != 0 || len(credentials.keys) != 1 {
		t.Fatalf("rotation convergence rotations=%d pending=%d commands=%d removals=%d protected=%d", rotations, pendingKeys, commands, credentials.removeCalls, len(credentials.keys))
	}
}

func TestConcurrentKeyRotationDoesNotCreateProtectedLosers(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	credentials.failNextAnyRemoval()
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	type outcome struct {
		frame *protocol.KeyRotationPropose
		err   error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			frame, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
			results <- outcome{frame: frame, err: err}
		}()
	}
	close(start)
	var first *protocol.KeyRotationPropose
	for index := 0; index < 2; index++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if first == nil {
			first = result.frame
		} else if result.frame.MessageID != first.MessageID || result.frame.NewKeyID != first.NewKeyID {
			t.Fatalf("concurrent replay changed proposal: %#v != %#v", result.frame, first)
		}
	}
	var revokedLosers, leases int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE state='revoked' AND activated_at IS NULL`).Scan(&revokedLosers); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if revokedLosers != 0 || leases != 0 || credentials.removeCalls != 0 {
		t.Fatalf("serialized rotation losers=%d leases=%d removals=%d", revokedLosers, leases, credentials.removeCalls)
	}
}

func TestRevokedKeyCleanupCountsOnlyFilesActuallyRemoved(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	revokedAt := now
	missing := ControllerKey{KeyID: "40000000-0000-4000-8000-000000000001", ControllerID: repositoryTestControllerID, PublicKey: bytes.Repeat([]byte{4}, 32), Algorithm: KeyAlgorithmEd25519, State: KeyRevoked, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, "40000000-0000-4000-8000-000000000001"), CreatedAt: now, UpdatedAt: now, RevokedAt: &revokedAt}
	present := ControllerKey{KeyID: "50000000-0000-4000-8000-000000000002", ControllerID: repositoryTestControllerID, PublicKey: bytes.Repeat([]byte{5}, 32), Algorithm: KeyAlgorithmEd25519, State: KeyRevoked, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, "50000000-0000-4000-8000-000000000002"), CreatedAt: now, UpdatedAt: now, RevokedAt: &revokedAt}
	for _, key := range []ControllerKey{missing, present} {
		if err := repository.CreateKey(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	seedMemoryControllerKey(t, credentials, present.ControllerID, present.KeyID, present.PublicKey)
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	page, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || len(page.Candidates) != 2 || page.Cleaned != 1 || credentials.has(present.ProtectedKeyRef) {
		t.Fatalf("cleanup count/page=%#v err=%v", page, err)
	}
	again, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || len(again.Candidates) != 0 || again.Cleaned != 0 {
		t.Fatalf("idempotent cleanup count/page=%#v err=%v", again, err)
	}
}

func TestAmbiguousPendingRotationMaterializationRetainsDurableLeaseUntilRecovery(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	config := DefaultSessionControlConfig()
	config.Now = func() time.Time { return now.Add(time.Minute) }
	service, err := NewSessionControlService(&ambiguousPendingRotationRepository{sessionControlRepository: repository}, credentials, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.StartKeyRotation(context.Background(), repositoryTestControllerID); err == nil || !strings.Contains(err.Error(), controlErrorPersistence) {
		t.Fatalf("ambiguous create error = %v", err)
	}
	if credentials.removeCalls != 0 || len(credentials.keys) != 1 {
		t.Fatalf("ambiguous create deleted candidate calls=%d files=%d", credentials.removeCalls, len(credentials.keys))
	}
	var rows, leases int
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE state='revoked' AND activated_at IS NULL`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("ambiguous create persisted cleanup rows=%d err=%v", rows, err)
	}
	if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases WHERE operation='write' AND phase='active'`).Scan(&leases); err != nil || leases != 1 {
		t.Fatalf("ambiguous create write leases=%d err=%v", leases, err)
	}
	config.Now = func() time.Time { return now.Add(7 * time.Minute) }
	reconstructed, err := NewSessionControlService(repository, credentials, config)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reconstructed.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 || len(credentials.keys) != 0 {
		t.Fatalf("ambiguous write recovery page=%#v err=%v keys=%d", page, err, len(credentials.keys))
	}
}

func TestStartKeyRotationRecoversMaterializedPrepareWithoutCommand(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	keyID := "44000000-0000-4000-8000-000000000004"
	rotationID := "55000000-0000-4000-8000-000000000004"
	privateKey := testPrivateKey(0x74)
	publicKey := append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...)
	materializedAt := now.Add(time.Minute)
	lease := testControllerKeyWriteLease(materializedAt, keyID, rotationID, publicKey)
	if err := repository.BeginControllerKeyWrite(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: repositoryTestControllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey}); err != nil {
		t.Fatal(err)
	}
	clear(privateKey)
	key := ControllerKey{KeyID: keyID, ControllerID: repositoryTestControllerID, PublicKey: append([]byte(nil), publicKey...), Algorithm: KeyAlgorithmEd25519, State: KeyPending, ProtectedKeyRef: lease.ProtectedKeyRef, CreatedAt: materializedAt, UpdatedAt: materializedAt}
	rotation := KeyRotation{RotationID: rotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: keyID, State: RotationPrepare, ExpiresAt: materializedAt.Add(15 * time.Minute), StateChangedAt: materializedAt, CreatedAt: materializedAt, UpdatedAt: materializedAt}
	if err := repository.MaterializePendingKeyAndRotation(context.Background(), lease, key, rotation, materializedAt); err != nil {
		t.Fatal(err)
	}
	clear(key.PublicKey)
	service := newTestControlService(t, repository, credentials, materializedAt.Add(time.Minute))
	proposal, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
	if err != nil || proposal.RotationID != rotationID || proposal.NewKeyID != keyID {
		t.Fatalf("prepare recovery proposal=%#v err=%v", proposal, err)
	}
	persisted, err := repository.Rotation(context.Background(), repositoryTestControllerID, rotationID)
	if err != nil || persisted.State != RotationPropose {
		t.Fatalf("prepare recovery rotation=%#v err=%v", persisted, err)
	}
	if _, err = repository.ControlCommandForAggregate(context.Background(), repositoryTestControllerID, "", rotationID, "propose"); err != nil {
		t.Fatal(err)
	}
	clear(lease.PublicKey)
	clear(publicKey)
}

func TestControllerKeyInventoryRecoveryIsBoundedAndFailsClosed(t *testing.T) {
	t.Run("saturated prefix", func(t *testing.T) {
		repository, _, now := newRepositoryHarness(t)
		createTestIdentity(t, repository, now)
		credentials := newMemoryControlCredentials()
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
		pending := testPendingKey(now)
		if err := repository.CreateKey(context.Background(), pending); err != nil {
			t.Fatal(err)
		}
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, pending.KeyID, pending.PublicKey)
		for index, keyID := range []string{"40000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000002", "60000000-0000-4000-8000-000000000003"} {
			seedMemoryControllerKey(t, credentials, repositoryTestControllerID, keyID, bytes.Repeat([]byte{byte(index + 3)}, ed25519.PublicKeySize))
		}
		service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
		cursor := ControllerKeyRecoveryCursor{}
		cleaned := 0
		for pageNumber := 0; pageNumber < 8; pageNumber++ {
			page, err := service.RecoverControllerKeysPage(context.Background(), cursor, 1)
			if err != nil {
				t.Fatal(err)
			}
			cleaned += page.Cleaned
			if page.Complete {
				break
			}
			if page.NextCursor.CredentialCursor == cursor.CredentialCursor {
				t.Fatalf("credential cursor did not advance: %#v", page)
			}
			cursor = page.NextCursor
		}
		if cleaned != 3 || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, pending.KeyID)) {
			t.Fatalf("bounded recovery cleaned=%d files=%d", cleaned, len(credentials.keys))
		}
	})

	t.Run("database ambiguity", func(t *testing.T) {
		repository, _, now := newRepositoryHarness(t)
		createTestIdentity(t, repository, now)
		credentials := newMemoryControlCredentials()
		activeRef := ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
		config := DefaultSessionControlConfig()
		config.Now = func() time.Time { return now.Add(time.Minute) }
		service, err := NewSessionControlService(&keyLookupErrorRepository{sessionControlRepository: repository}, credentials, config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10); err == nil || !strings.Contains(err.Error(), controlErrorPersistence) {
			t.Fatalf("ambiguous key lookup error = %v", err)
		}
		if !credentials.has(activeRef) || credentials.removeCalls != 0 {
			t.Fatal("database ambiguity deleted protected key")
		}
	})

	t.Run("public identity mismatch", func(t *testing.T) {
		repository, _, now := newRepositoryHarness(t)
		createTestIdentity(t, repository, now)
		credentials := newMemoryControlCredentials()
		activeRef := ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)
		seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x7a}, ed25519.PublicKeySize))
		service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
		if _, err := service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10); err == nil || !strings.Contains(err.Error(), controlErrorState) {
			t.Fatalf("public identity mismatch error = %v", err)
		}
		if !credentials.has(activeRef) || credentials.removeCalls != 0 {
			t.Fatal("public identity mismatch deleted protected key")
		}
	})
}

func TestControllerKeyInventoryRecoversFileStoreCrashBeforeDatabaseCall(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	orphanID := "44444444-4444-4444-8444-444444444444"
	privateKey := testPrivateKey(0x55)
	publicKey := append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...)
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: repositoryTestControllerID, KeyID: orphanID, PrivateKey: privateKey, PublicKey: publicKey}); err != nil {
		t.Fatal(err)
	}
	clear(privateKey)
	config := DefaultSessionControlConfig()
	config.Now = func() time.Time { return now.Add(time.Minute) }
	reconstructed, err := NewSessionControlService(repository, store, config)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reconstructed.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 || !page.Complete {
		t.Fatalf("crash-boundary recovery = %#v err=%v", page, err)
	}
	if _, err = store.ReadControllerKey(repositoryTestControllerID, orphanID, publicKey); err == nil {
		t.Fatal("crash orphan remained after reconstructed recovery")
	}
	clear(publicKey)
}

func TestControllerKeyRecoveryHonorsDurableWriterLeaseAndReconcilesCrashArtifacts(t *testing.T) {
	for _, test := range []struct {
		name        string
		destination bool
		temporary   bool
		wantCleaned int
	}{
		{name: "destination present", destination: true, wantCleaned: 1},
		{name: "temporary only", temporary: true, wantCleaned: 1},
		{name: "temporary and destination", destination: true, temporary: true, wantCleaned: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, root, now := newRepositoryHarness(t)
			createTestIdentity(t, repository, now)
			store, err := NewFileCredentialStore(root)
			if err != nil {
				t.Fatal(err)
			}
			keyID := "44000000-0000-4000-8000-000000000001"
			rotationID := "55000000-0000-4000-8000-000000000001"
			privateKey := testPrivateKey(0x71)
			publicKey := append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...)
			lease := testControllerKeyWriteLease(now.Add(time.Minute), keyID, rotationID, publicKey)
			if err = repository.BeginControllerKeyWrite(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			if test.destination {
				if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: lease.ControllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey}); err != nil {
					t.Fatal(err)
				}
			}
			keysPath := filepath.Join(root, "secrets", "relay", "controllers", lease.ControllerID, "keys")
			if err = store.prepareParent(keysPath); err != nil {
				t.Fatal(err)
			}
			tempName := ".hostd-secret-000000001"
			if test.temporary {
				temporary, createErr := os.CreateTemp(keysPath, controllerKeyTemporaryPrefix+"*")
				if createErr != nil {
					t.Fatal(createErr)
				}
				tempName = filepath.Base(temporary.Name())
				if _, err = temporary.Write([]byte("interrupted protected staging")); err != nil {
					temporary.Close()
					t.Fatal(err)
				}
				if err = temporary.Close(); err != nil {
					t.Fatal(err)
				}
				if !validControllerKeyTemporaryArtifactName(tempName) {
					t.Fatalf("secretfile-style temporary name was not recognized: %q", tempName)
				}
			}
			clear(privateKey)

			active := newTestControlService(t, repository, store, now.Add(2*time.Minute))
			page, err := active.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
			if err != nil || page.Cleaned != 0 {
				t.Fatalf("active writer recovery page=%#v err=%v", page, err)
			}
			if test.destination {
				loaded, readErr := store.ReadControllerKey(lease.ControllerID, keyID, publicKey)
				if readErr != nil {
					t.Fatalf("active writer destination deleted: %v", readErr)
				}
				loaded.Destroy()
			}
			if test.temporary {
				if _, err = os.Lstat(filepath.Join(keysPath, tempName)); err != nil {
					t.Fatalf("active writer temporary artifact deleted: %v", err)
				}
			}

			restarted := newTestControlService(t, repository, store, now.Add(7*time.Minute))
			page, err = restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
			if err != nil || page.Cleaned != test.wantCleaned {
				t.Fatalf("expired writer recovery page=%#v err=%v wantCleaned=%d", page, err, test.wantCleaned)
			}
			if _, err = os.Lstat(filepath.Join(keysPath, tempName)); test.temporary && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary artifact survived restart: %v", err)
			}
			var leases int
			if err = repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_key_io_leases`).Scan(&leases); err != nil || leases != 0 {
				t.Fatalf("recovery leases=%d err=%v", leases, err)
			}
			clear(publicKey)
		})
	}
}

func TestFencedWriterCannotMaterializeAndRestartFindsItsLateArtifact(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "44000000-0000-4000-8000-000000000002"
	rotationID := "55000000-0000-4000-8000-000000000002"
	privateKey := testPrivateKey(0x72)
	publicKey := append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...)
	oldLease := testControllerKeyWriteLease(now.Add(time.Minute), keyID, rotationID, publicKey)
	if err = repository.BeginControllerKeyWrite(context.Background(), oldLease); err != nil {
		t.Fatal(err)
	}
	claimAt := oldLease.LeaseExpiresAt.Add(time.Second)
	claimed, err := repository.ClaimExpiredControllerKeyIOLease(context.Background(), oldLease, "66000000-0000-4000-8000-000000000002", claimAt, claimAt.Add(2*time.Minute))
	if err != nil || claimed.Fence != oldLease.Fence+1 {
		t.Fatalf("recovery claim=%#v err=%v", claimed, err)
	}
	clear(claimed.PublicKey)
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: oldLease.ControllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey}); err != nil {
		t.Fatal(err)
	}
	clear(privateKey)
	key := ControllerKey{KeyID: keyID, ControllerID: oldLease.ControllerID, PublicKey: append([]byte(nil), publicKey...), Algorithm: KeyAlgorithmEd25519, State: KeyPending, ProtectedKeyRef: oldLease.ProtectedKeyRef, CreatedAt: oldLease.CreatedAt, UpdatedAt: oldLease.CreatedAt}
	rotation := KeyRotation{RotationID: rotationID, ControllerID: oldLease.ControllerID, OldKeyID: oldLease.OldKeyID, NewKeyID: keyID, State: RotationPrepare, ExpiresAt: oldLease.CreatedAt.Add(15 * time.Minute), StateChangedAt: oldLease.CreatedAt, CreatedAt: oldLease.CreatedAt, UpdatedAt: oldLease.CreatedAt}
	if err = repository.MaterializePendingKeyAndRotation(context.Background(), oldLease, key, rotation, claimAt); !errors.Is(err, ErrState) {
		t.Fatalf("fenced writer materialization error=%v", err)
	}
	clear(key.PublicKey)
	restarted := newTestControlService(t, repository, store, claimAt.Add(3*time.Minute))
	page, err := restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 {
		t.Fatalf("late artifact restart recovery=%#v err=%v", page, err)
	}
	if _, err = store.ReadControllerKey(oldLease.ControllerID, keyID, publicKey); err == nil {
		t.Fatal("fenced writer artifact was stranded")
	}
	clear(oldLease.PublicKey)
	clear(publicKey)
}

func TestControllerKeyRecoveryDatabaseAmbiguityFailsClosedThenConverges(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "44000000-0000-4000-8000-000000000003"
	privateKey := testPrivateKey(0x73)
	publicKey := append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...)
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: repositoryTestControllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey}); err != nil {
		t.Fatal(err)
	}
	clear(privateKey)
	config := DefaultSessionControlConfig()
	config.Now = func() time.Time { return now.Add(time.Minute) }
	ambiguous := &secondKeyLookupErrorRepository{sessionControlRepository: repository}
	service, err := NewSessionControlService(ambiguous, store, config)
	if err != nil {
		t.Fatal(err)
	}
	if page, recoverErr := service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10); recoverErr == nil || !strings.Contains(recoverErr.Error(), controlErrorPersistence) || page.Cleaned != 0 {
		t.Fatalf("ambiguous recovery page=%#v err=%v", page, recoverErr)
	}
	loaded, err := store.ReadControllerKey(repositoryTestControllerID, keyID, publicKey)
	if err != nil {
		t.Fatalf("database ambiguity deleted key: %v", err)
	}
	loaded.Destroy()

	restarted := newTestControlService(t, repository, store, now.Add(4*time.Minute))
	page, err := restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 {
		t.Fatalf("ambiguous restart recovery page=%#v err=%v", page, err)
	}
	if _, err = store.ReadControllerKey(repositoryTestControllerID, keyID, publicKey); err == nil {
		t.Fatal("database-ambiguous orphan survived expired recovery lease")
	}
	clear(publicKey)
}

func TestControllerKeyTemporaryCleanupLeaseSurvivesProcessRestart(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	keysPath := filepath.Join(root, "secrets", "relay", "controllers", repositoryTestControllerID, "keys")
	if err = store.prepareParent(keysPath); err != nil {
		t.Fatal(err)
	}
	tempName := ".hostd-secret-000000009"
	if err = os.WriteFile(filepath.Join(keysPath, tempName), []byte("interrupted cleanup"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease := ControllerKeyIOLease{
		ScopeKey: controllerKeyIOScope(repositoryTestControllerID), ControllerID: repositoryTestControllerID, LeaseID: "77000000-0000-4000-8000-000000000009", Operation: ControllerKeyIOTempCleanup,
		Phase: ControllerKeyIORecovery, Fence: 1, LeaseExpiresAt: now.Add(3 * time.Minute), ArtifactName: tempName, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute),
	}
	if err = repository.AcquireControllerKeyCleanupLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	restarted := newTestControlService(t, repository, store, now.Add(4*time.Minute))
	page, err := restarted.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || page.Cleaned != 1 {
		t.Fatalf("temporary cleanup restart page=%#v err=%v", page, err)
	}
	if _, err = os.Lstat(filepath.Join(keysPath, tempName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary cleanup artifact survived: %v", err)
	}
}

func TestControllerKeyInventorySurfacesCorruptEntryAndCleansLaterOrphan(t *testing.T) {
	repository, root, now := newRepositoryHarness(t)
	store, err := NewFileCredentialStore(root)
	if err != nil {
		t.Fatal(err)
	}
	activeID := "10000000-0000-4000-8000-000000000001"
	corruptID := "20000000-0000-4000-8000-000000000002"
	orphanID := "30000000-0000-4000-8000-000000000003"
	activePrivate := testPrivateKey(0x61)
	activePublic := append([]byte(nil), activePrivate.Public().(ed25519.PublicKey)...)
	activated := now
	identity := ControllerIdentity{ControllerID: repositoryTestControllerID, State: ControllerActive, CreatedAt: now, UpdatedAt: now}
	activeKey := ControllerKey{KeyID: activeID, ControllerID: repositoryTestControllerID, PublicKey: activePublic, Algorithm: KeyAlgorithmEd25519, State: KeyActive, ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, activeID), CreatedAt: now, UpdatedAt: now, ActivatedAt: &activated, PossessionConfirmedAt: &activated}
	persistTestIdentity(t, repository, identity, activeKey, now)
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: repositoryTestControllerID, KeyID: activeID, PrivateKey: activePrivate, PublicKey: activePublic}); err != nil {
		t.Fatal(err)
	}
	clear(activePrivate)

	corruptPrivate := testPrivateKey(0x62)
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: repositoryTestControllerID, KeyID: corruptID, PrivateKey: corruptPrivate}); err != nil {
		t.Fatal(err)
	}
	clear(corruptPrivate)
	corruptPath, _, _ := store.controllerKeyLocation(repositoryTestControllerID, corruptID)
	if err = os.WriteFile(corruptPath, []byte("damaged protected key"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanPrivate := testPrivateKey(0x63)
	orphanPublic := append([]byte(nil), orphanPrivate.Public().(ed25519.PublicKey)...)
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: repositoryTestControllerID, KeyID: orphanID, PrivateKey: orphanPrivate, PublicKey: orphanPublic}); err != nil {
		t.Fatal(err)
	}
	clear(orphanPrivate)

	config := DefaultSessionControlConfig()
	config.Now = func() time.Time { return now.Add(time.Minute) }
	service, err := NewSessionControlService(repository, store, config)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 3)
	if err == nil || !strings.Contains(err.Error(), controlErrorCredential) || page.Cleaned != 1 || page.Scanned != 3 || len(page.NeedsAttention) != 1 || page.NeedsAttention[0].KeyID != corruptID || page.NextCursor.CredentialCursor == "" || page.Complete {
		t.Fatalf("corrupt inventory page = %#v err=%v", page, err)
	}
	if _, err = os.Lstat(corruptPath); err != nil {
		t.Fatalf("corrupt credential was deleted: %v", err)
	}
	if _, err = store.ReadControllerKey(repositoryTestControllerID, activeID, activePublic); err != nil {
		t.Fatalf("valid prefix key was damaged: %v", err)
	}
	if _, err = store.ReadControllerKey(repositoryTestControllerID, orphanID, orphanPublic); err == nil {
		t.Fatal("later orphan was not deleted")
	}
	continued, err := service.RecoverControllerKeysPage(context.Background(), page.NextCursor, 3)
	if err != nil || !continued.Complete || continued.Scanned != 0 || len(continued.NeedsAttention) != 0 {
		t.Fatalf("corrupt inventory cursor did not progress: %#v err=%v", continued, err)
	}
	clear(activePublic)
	clear(orphanPublic)
}

func TestKeyRotationDatabaseFailureDefersProtectedCandidateToInventoryRecovery(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	if _, err := repository.db.Exec(`CREATE TRIGGER test_abort_rotation BEFORE INSERT ON relay_key_rotations BEGIN SELECT RAISE(ABORT,'forced rotation failure'); END`); err != nil {
		t.Fatal(err)
	}
	credentials := newMemoryControlCredentials()
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	if _, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID); err == nil {
		t.Fatal("forced rotation database failure succeeded")
	}
	if credentials.removeCalls != 0 || len(credentials.keys) != 1 {
		t.Fatalf("ambiguous protected compensation calls=%d keys=%d", credentials.removeCalls, len(credentials.keys))
	}
	var pendingKeys, rotations int
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_controller_keys WHERE state='pending'`).Scan(&pendingKeys); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`SELECT COUNT(*) FROM relay_key_rotations`).Scan(&rotations); err != nil {
		t.Fatal(err)
	}
	if pendingKeys != 0 || rotations != 0 {
		t.Fatalf("failed rotation persisted key=%d rotation=%d", pendingKeys, rotations)
	}
	reconstructed := newTestControlService(t, repository, credentials, now.Add(7*time.Minute))
	page, err := reconstructed.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || !page.Complete || page.Cleaned != 1 || len(credentials.keys) != 0 {
		t.Fatalf("inventory recovery = %#v err=%v keys=%d", page, err, len(credentials.keys))
	}
}

func TestExpiredRotationFailsClosedAndPreservesOldActiveKey(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	proposal, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
	if err != nil {
		t.Fatal(err)
	}
	service.config.Now = func() time.Time { return now.Add(30 * time.Minute) }
	if _, err = service.StartKeyRotation(context.Background(), repositoryTestControllerID); err == nil {
		t.Fatal("expired rotation was replayed")
	}
	rotation, err := repository.Rotation(context.Background(), repositoryTestControllerID, proposal.RotationID)
	if err != nil || rotation.State != RotationFailed || rotation.LastErrorCode != ErrorRotationFailed {
		t.Fatalf("expired rotation = %#v err=%v", rotation, err)
	}
	oldKey, err := repository.Key(context.Background(), repositoryTestControllerID, repositoryTestKeyID)
	if err != nil || oldKey.State != KeyActive {
		t.Fatalf("old key after expiry = %#v err=%v", oldKey, err)
	}
	failedKey, err := repository.Key(context.Background(), repositoryTestControllerID, proposal.NewKeyID)
	if err != nil || failedKey.State != KeyRevoked {
		t.Fatalf("pending key after expiry = %#v err=%v", failedKey, err)
	}
	if credentials.has(ProtectedKeyRef(repositoryTestControllerID, proposal.NewKeyID)) || !credentials.has(ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)) {
		t.Fatal("expired rotation did not delete only its revoked pending key")
	}
	var cleared any
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, proposal.NewKeyID).Scan(&cleared); err != nil || cleared == nil {
		t.Fatalf("failed new-key clear marker=%v err=%v", cleared, err)
	}
	if _, err = service.StartKeyRotation(context.Background(), repositoryTestControllerID); err != nil {
		t.Fatalf("new rotation after expiry = %v", err)
	}
}

func TestExpiredRotationCleanupFailureIsDurableAndRecoveryIsIdempotent(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	proposal, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
	if err != nil {
		t.Fatal(err)
	}
	failedRef := ProtectedKeyRef(repositoryTestControllerID, proposal.NewKeyID)
	service.config.Now = func() time.Time { return now.Add(30 * time.Minute) }
	if _, err = repository.db.Exec(`CREATE TRIGGER test_abort_expired_rotation BEFORE UPDATE OF state ON relay_key_rotations WHEN OLD.rotation_id='` + proposal.RotationID + `' AND NEW.state='failed' BEGIN SELECT RAISE(ABORT,'forced expired rotation rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.StartKeyRotation(context.Background(), repositoryTestControllerID); err == nil {
		t.Fatal("forced expired rotation rollback succeeded")
	}
	rolledBack, loadErr := repository.Rotation(context.Background(), repositoryTestControllerID, proposal.RotationID)
	if loadErr != nil || rolledBack.State != RotationPropose || !credentials.has(failedRef) {
		t.Fatalf("failed expiry transaction removed protected key: rotation=%#v err=%v", rolledBack, loadErr)
	}
	if _, err = repository.db.Exec(`DROP TRIGGER test_abort_expired_rotation`); err != nil {
		t.Fatal(err)
	}
	credentials.failNextRemoval(failedRef)
	if _, err = service.StartKeyRotation(context.Background(), repositoryTestControllerID); err == nil || !strings.Contains(err.Error(), controlErrorCredential) || strings.Contains(err.Error(), failedRef) {
		t.Fatalf("expired cleanup failure = %v", err)
	}
	rotation, err := repository.Rotation(context.Background(), repositoryTestControllerID, proposal.RotationID)
	if err != nil || rotation.State != RotationFailed || !credentials.has(failedRef) {
		t.Fatalf("cleanup failure lost durable failed rotation/file: rotation=%#v err=%v", rotation, err)
	}
	oldRef := ProtectedKeyRef(repositoryTestControllerID, repositoryTestKeyID)
	if !credentials.has(oldRef) {
		t.Fatal("expired cleanup failure removed the active old key")
	}
	var cleared any
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, proposal.NewKeyID).Scan(&cleared); err != nil || cleared != nil {
		t.Fatalf("failed delete marker=%v err=%v", cleared, err)
	}
	page, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || len(page.Candidates) != 1 || page.Candidates[0].KeyID != proposal.NewKeyID || credentials.has(failedRef) || !credentials.has(oldRef) {
		t.Fatalf("failed rotation recovery = %#v err=%v", page, err)
	}
	if _, err = service.RecoverRevokedControllerKeys(context.Background(), "", 10); err != nil {
		t.Fatalf("absent failed key recovery = %v", err)
	}
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, proposal.NewKeyID).Scan(&cleared); err != nil || cleared == nil {
		t.Fatalf("failed recovery marker=%v err=%v", cleared, err)
	}
}

func TestFailedRotationMarkFailureKeepsTerminalStateAndRecoveryRetries(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	createTestIdentity(t, repository, now)
	credentials := newMemoryControlCredentials()
	seedMemoryControllerKey(t, credentials, repositoryTestControllerID, repositoryTestKeyID, bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize))
	service := newTestControlService(t, repository, credentials, now.Add(time.Minute))
	proposal, err := service.StartKeyRotation(context.Background(), repositoryTestControllerID)
	if err != nil {
		t.Fatal(err)
	}
	failedRef := ProtectedKeyRef(repositoryTestControllerID, proposal.NewKeyID)
	if _, err = repository.db.Exec(`CREATE TRIGGER test_abort_failed_key_clear BEFORE UPDATE OF protected_key_cleared_at ON relay_controller_keys BEGIN SELECT RAISE(ABORT,'forced failed key clear failure'); END`); err != nil {
		t.Fatal(err)
	}
	live, err := repository.Rotation(context.Background(), repositoryTestControllerID, proposal.RotationID)
	if err != nil {
		t.Fatal(err)
	}
	service.config.Now = func() time.Time { return now.Add(30 * time.Minute) }
	if err = service.failExpiredRotation(context.Background(), live, now.Add(30*time.Minute)); err == nil || !strings.Contains(err.Error(), controlErrorPersistence) {
		t.Fatalf("failed rotation mark failure = %v", err)
	}
	rotation, err := repository.Rotation(context.Background(), repositoryTestControllerID, proposal.RotationID)
	if err != nil || rotation.State != RotationFailed || credentials.has(failedRef) {
		t.Fatalf("failed rotation terminal state=%#v err=%v file=%v", rotation, err, credentials.has(failedRef))
	}
	var cleared any
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, proposal.NewKeyID).Scan(&cleared); err != nil || cleared != nil {
		t.Fatalf("failed mark marker=%v err=%v", cleared, err)
	}
	if _, err = repository.db.Exec(`DROP TRIGGER test_abort_failed_key_clear`); err != nil {
		t.Fatal(err)
	}
	service.config.Now = func() time.Time { return now.Add(33 * time.Minute) }
	recovered, err := service.RecoverControllerKeysPage(context.Background(), ControllerKeyRecoveryCursor{}, 10)
	if err != nil || recovered.Cleaned != 0 {
		t.Fatalf("failed mark recovery=%#v err=%v", recovered, err)
	}
	if err = repository.db.QueryRow(`SELECT protected_key_cleared_at FROM relay_controller_keys WHERE key_id=?`, proposal.NewKeyID).Scan(&cleared); err != nil || cleared == nil {
		t.Fatalf("failed mark retry marker=%v err=%v", cleared, err)
	}
	page, err := service.RecoverRevokedControllerKeys(context.Background(), "", 10)
	if err != nil || len(page.Candidates) != 0 || !page.Complete {
		t.Fatalf("failed mark idempotent recovery=%#v err=%v", page, err)
	}
}

func TestPendingControlsReconstructOnlyDurableNonSecretFrames(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	service := newTestControlService(t, repository, newMemoryControlCredentials(), now.Add(time.Minute))
	remove, err := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := service.Pending(context.Background(), testControlSession(now, repositoryTestKeyID), 10)
	if err != nil || len(frames) != 1 {
		t.Fatalf("pending frames=%#v err=%v", frames, err)
	}
	replayed, ok := frames[0].(*protocol.BindingRemove)
	if !ok || replayed.MessageID != remove.MessageID || !replayed.SentAt.Equal(remove.SentAt) {
		t.Fatalf("pending replay=%#v", frames[0])
	}
	rows, err := repository.db.Query(`PRAGMA table_info(relay_outbound_commands)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err = rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "private_key", "signature", "nonce", "session_id", "raw_frame", "token", "secret":
			t.Fatalf("forbidden outbound command column %q", name)
		}
	}
}

func TestPendingControlsDoNotLetEarlierRotationConfirmStarveActionableCommand(t *testing.T) {
	repository, _, now := newRepositoryHarness(t)
	binding := createSessionBinding(t, repository, now)
	pendingKey := testPendingKey(now)
	if err := repository.CreateKey(context.Background(), pendingKey); err != nil {
		t.Fatal(err)
	}
	rotation := KeyRotation{RotationID: repositoryTestRotationID, ControllerID: repositoryTestControllerID, OldKeyID: repositoryTestKeyID, NewKeyID: pendingKey.KeyID, State: RotationPrepare, ExpiresAt: now.Add(10 * time.Minute), StateChangedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateRotation(context.Background(), rotation); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{RotationPropose, RotationConfirm} {
		if _, err := repository.db.Exec(`UPDATE relay_key_rotations SET state=?,state_changed_at=?,updated_at=? WHERE controller_id=? AND rotation_id=?`, state, timestamp(now.Add(time.Minute)), timestamp(now.Add(time.Minute)), rotation.ControllerID, rotation.RotationID); err != nil {
			t.Fatal(err)
		}
	}
	confirmCommand := OutboundCommand{ControllerID: repositoryTestControllerID, MessageID: uuid.NewString(), CommandType: CommandRotationConfirm, RotationID: rotation.RotationID, Stage: "confirm", SentAt: now.Add(time.Minute), Digest: sha256.Sum256([]byte("earlier confirmation")), State: CommandPrepared}
	if _, err := repository.PrepareControlCommand(context.Background(), confirmCommand); err != nil {
		t.Fatal(err)
	}
	service := newTestControlService(t, repository, newMemoryControlCredentials(), now.Add(2*time.Minute))
	service.config.Now = func() time.Time { return now.Add(2 * time.Minute) }
	remove, err := service.RequestBindingRemoval(context.Background(), binding.OwnerUserID, binding.BindingID)
	if err != nil {
		t.Fatal(err)
	}

	frames, err := service.Pending(context.Background(), testControlSession(now, repositoryTestKeyID), 1)
	if err != nil || len(frames) != 1 {
		t.Fatalf("bounded pending frames = %#v err=%v", frames, err)
	}
	replayedRemove, ok := frames[0].(*protocol.BindingRemove)
	if !ok || replayedRemove.MessageID != remove.MessageID || !replayedRemove.SentAt.Equal(remove.SentAt) {
		t.Fatalf("bounded actionable replay = %#v", frames[0])
	}
}

func TestSessionControlDiagnosticsRedactProtocolAndSessionMaterial(t *testing.T) {
	sentinelSession := "77777777-7777-4777-8777-777777777777"
	sentinelSignature := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x7e}, protocol.SignatureBytes))
	contextValue := SessionControlContext{ControllerID: repositoryTestControllerID, KeyID: repositoryTestKeyID, SessionID: sentinelSession, ExpiresAt: time.Now().Add(time.Minute)}
	result := SessionControlResult{Response: &protocol.KeyRotationConfirm{Envelope: protocol.NewEnvelope(protocol.TypeKeyRotationConfirm, uuid.NewString(), time.Now()), RotationID: repositoryTestRotationID, Signature: sentinelSignature}, Action: ControlContinue}
	for label, diagnostic := range map[string]string{"context": fmt.Sprintf("%#v", contextValue), "result": fmt.Sprintf("%#v", result)} {
		if bytes.Contains([]byte(diagnostic), []byte(sentinelSession)) || bytes.Contains([]byte(diagnostic), []byte(sentinelSignature)) {
			t.Fatalf("%s diagnostic leaked control material: %q", label, diagnostic)
		}
	}
}

func newTestControlService(t *testing.T, repository *Repository, credentials sessionControlCredentials, now time.Time) *SessionControlService {
	t.Helper()
	config := DefaultSessionControlConfig()
	config.Now = func() time.Time { return now }
	config.Entropy = rand.Reader
	config.GenerateKey = func(entropy io.Reader) (ed25519.PublicKey, ed25519.PrivateKey, error) {
		return ed25519.GenerateKey(entropy)
	}
	config.NewID = func(io.Reader) (string, error) { return uuid.NewString(), nil }
	service, err := NewSessionControlService(repository, credentials, config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testControlSession(now time.Time, keyID string) SessionControlContext {
	return SessionControlContext{ControllerID: repositoryTestControllerID, KeyID: keyID, SessionID: "77777777-7777-4777-8777-777777777777", ExpiresAt: now.Add(time.Hour)}
}

func testReadyControlSession(t *testing.T, repository *Repository, now time.Time, keyID string) SessionControlContext {
	t.Helper()
	status, err := repository.SessionStatus(context.Background(), repositoryTestControllerID)
	if errors.Is(err, ErrNotFound) {
		readyAt := now.UTC()
		status = SessionStatus{ControllerID: repositoryTestControllerID, Epoch: 1, Fence: 1, State: SessionReady, KeyID: repositoryTestKeyID, LastReadyAt: &readyAt, LastSeenAt: &readyAt, StateChangedAt: readyAt, UpdatedAt: readyAt}
		if err = repository.AdvanceSessionStatus(context.Background(), 0, 0, status); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	value := testControlSession(now, keyID)
	value.Epoch = status.Epoch
	value.Fence = status.Fence
	return value
}

type memoryControlCredentials struct {
	mu             sync.Mutex
	keys           map[string]ControllerKeyBundle
	removeFailures map[string]int
	removeAny      int
	removeCalls    int
	temporary      map[string]ControllerKeyTemporaryArtifact
}

func newMemoryControlCredentials() *memoryControlCredentials {
	return &memoryControlCredentials{
		keys:           make(map[string]ControllerKeyBundle),
		removeFailures: make(map[string]int),
		temporary:      make(map[string]ControllerKeyTemporaryArtifact),
	}
}

func (store *memoryControlCredentials) WriteControllerKey(bundle ControllerKeyBundle) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ref := ProtectedKeyRef(bundle.ControllerID, bundle.KeyID)
	if _, exists := store.keys[ref]; exists {
		return "", errors.New("protected key already exists")
	}
	store.keys[ref] = ControllerKeyBundle{Version: bundle.Version, ControllerID: bundle.ControllerID, KeyID: bundle.KeyID, PrivateKey: append(ed25519.PrivateKey(nil), bundle.PrivateKey...), PublicKey: append(ed25519.PublicKey(nil), bundle.PublicKey...)}
	return ref, nil
}

func (store *memoryControlCredentials) ReadControllerKey(controllerID, keyID string, expectedPublicKey []byte) (ControllerKeyBundle, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	bundle, exists := store.keys[ProtectedKeyRef(controllerID, keyID)]
	if !exists || !bytes.Equal(bundle.PublicKey, expectedPublicKey) {
		return ControllerKeyBundle{}, errors.New("protected key unavailable")
	}
	return ControllerKeyBundle{Version: bundle.Version, ControllerID: bundle.ControllerID, KeyID: bundle.KeyID, PrivateKey: append(ed25519.PrivateKey(nil), bundle.PrivateKey...), PublicKey: append(ed25519.PublicKey(nil), bundle.PublicKey...)}, nil
}

func (store *memoryControlCredentials) RemoveControllerKey(controllerID, keyID string) error {
	_, err := store.RemoveControllerKeyWithResult(controllerID, keyID)
	return err
}

func (store *memoryControlCredentials) RemoveControllerKeyWithResult(controllerID, keyID string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ref := ProtectedKeyRef(controllerID, keyID)
	if store.removeAny > 0 {
		store.removeAny--
		return false, errors.New("protected key removal failed")
	}
	if store.removeFailures[ref] > 0 {
		store.removeFailures[ref]--
		return false, errors.New("protected key removal failed")
	}
	bundle, exists := store.keys[ref]
	if !exists {
		return false, nil
	}
	bundle.Destroy()
	delete(store.keys, ref)
	store.removeCalls++
	return true, nil
}

func (store *memoryControlCredentials) ControllerKeyTemporaryArtifacts(cursor string, limit int) (ControllerKeyTemporaryArtifactPage, error) {
	if limit < 1 || limit > 1000 {
		return ControllerKeyTemporaryArtifactPage{}, errors.New("invalid temporary inventory limit")
	}
	cursorController, cursorName, err := parseControllerKeyInventoryCursor(cursor)
	if err != nil {
		return ControllerKeyTemporaryArtifactPage{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	values := make([]ControllerKeyTemporaryArtifact, 0, len(store.temporary))
	for _, artifact := range store.temporary {
		if cursorController != "" && (artifact.ControllerID < cursorController || artifact.ControllerID == cursorController && artifact.Name <= cursorName) {
			continue
		}
		values = append(values, artifact)
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].ControllerID < values[j].ControllerID || values[i].ControllerID == values[j].ControllerID && values[i].Name < values[j].Name
	})
	page := ControllerKeyTemporaryArtifactPage{Artifacts: values, Complete: true}
	if len(values) >= limit {
		page.Artifacts = values[:limit]
		page.Complete = false
		last := page.Artifacts[len(page.Artifacts)-1]
		page.NextCursor = controllerKeyInventoryCursor(last.ControllerID, last.Name)
	}
	return page, nil
}

func (store *memoryControlCredentials) RemoveControllerKeyTemporaryArtifact(controllerID, name string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := controllerID + "\x00" + name
	if _, ok := store.temporary[key]; !ok {
		return false, nil
	}
	delete(store.temporary, key)
	return true, nil
}

func (store *memoryControlCredentials) failNextAnyRemoval() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeAny++
}

func (store *memoryControlCredentials) ControllerKeyCredentials(cursor string, limit int) (ControllerKeyCredentialPage, error) {
	if limit < 1 || limit > 1000 {
		return ControllerKeyCredentialPage{}, errors.New("invalid inventory limit")
	}
	cursorControllerID, cursorKeyID, err := parseControllerKeyCredentialCursor(cursor)
	if err != nil {
		return ControllerKeyCredentialPage{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	metadata := make([]ControllerKeyCredentialMetadata, 0, len(store.keys))
	for ref, bundle := range store.keys {
		if cursorControllerID != "" && (bundle.ControllerID < cursorControllerID || bundle.ControllerID == cursorControllerID && bundle.KeyID <= cursorKeyID) {
			continue
		}
		metadata = append(metadata, ControllerKeyCredentialMetadata{ControllerID: bundle.ControllerID, KeyID: bundle.KeyID, PublicKey: append([]byte(nil), bundle.PublicKey...), ProtectedRef: ref})
	}
	sort.Slice(metadata, func(i, j int) bool {
		return metadata[i].ControllerID < metadata[j].ControllerID || metadata[i].ControllerID == metadata[j].ControllerID && metadata[i].KeyID < metadata[j].KeyID
	})
	if len(metadata) > limit {
		metadata = metadata[:limit]
	}
	if len(metadata) == limit {
		last := metadata[len(metadata)-1]
		return ControllerKeyCredentialPage{Credentials: metadata, NextCursor: controllerKeyCredentialCursor(last.ControllerID, last.KeyID)}, nil
	}
	return ControllerKeyCredentialPage{Credentials: metadata, Complete: true}, nil
}

func (store *memoryControlCredentials) failNextRemoval(ref string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeFailures[ref]++
}

func (store *memoryControlCredentials) has(ref string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, exists := store.keys[ref]
	return exists
}

func seedMemoryControllerKey(t *testing.T, store *memoryControlCredentials, controllerID, keyID string, publicKey []byte) {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x44}, ed25519.SeedSize))
	bundle := ControllerKeyBundle{
		Version:      credentialVersion,
		ControllerID: controllerID,
		KeyID:        keyID,
		PrivateKey:   privateKey,
		PublicKey:    append(ed25519.PublicKey(nil), publicKey...),
	}
	if _, err := store.WriteControllerKey(bundle); err != nil {
		bundle.Destroy()
		t.Fatal(err)
	}
	bundle.Destroy()
}

type ambiguousPendingRotationRepository struct {
	sessionControlRepository
}

func (*ambiguousPendingRotationRepository) MaterializePendingKeyAndRotation(context.Context, ControllerKeyIOLease, ControllerKey, KeyRotation, time.Time) error {
	return errors.New("ambiguous pending rotation result")
}

type keyLookupErrorRepository struct {
	sessionControlRepository
}

func (*keyLookupErrorRepository) Key(context.Context, string, string) (ControllerKey, error) {
	return ControllerKey{}, errors.New("database unavailable")
}

type secondKeyLookupErrorRepository struct {
	sessionControlRepository
	mu    sync.Mutex
	calls int
}

func (repository *secondKeyLookupErrorRepository) Key(ctx context.Context, controllerID, keyID string) (ControllerKey, error) {
	repository.mu.Lock()
	repository.calls++
	call := repository.calls
	repository.mu.Unlock()
	if call == 1 {
		return ControllerKey{}, ErrNotFound
	}
	if call == 2 {
		return ControllerKey{}, errors.New("database unavailable after cleanup claim")
	}
	return repository.sessionControlRepository.Key(ctx, controllerID, keyID)
}

func testControllerKeyWriteLease(now time.Time, keyID, rotationID string, publicKey []byte) ControllerKeyIOLease {
	return ControllerKeyIOLease{
		ScopeKey:        controllerKeyIOScope(repositoryTestControllerID),
		ControllerID:    repositoryTestControllerID,
		LeaseID:         "77000000-0000-4000-8000-000000000001",
		Operation:       ControllerKeyIOWrite,
		Phase:           ControllerKeyIOActive,
		Fence:           1,
		LeaseExpiresAt:  now.Add(5 * time.Minute),
		KeyID:           keyID,
		RotationID:      rotationID,
		OldKeyID:        repositoryTestKeyID,
		PublicKey:       append([]byte(nil), publicKey...),
		ProtectedKeyRef: ProtectedKeyRef(repositoryTestControllerID, keyID),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
