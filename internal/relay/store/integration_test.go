package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLRelayStateIntegration(t *testing.T) {
	dsn := os.Getenv("RIG_RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skipf("RIG_RELAY_TEST_DATABASE_URL is unset; real PostgreSQL integration not run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "relay_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s, err := New(pool, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	controllerID, keyID, subscriptionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	publicKey := bytes.Repeat([]byte{3}, 32)
	stateHash, pollHash := bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{5}, 32)
	enrollmentID, err := s.CreateEnrollment(ctx, EnrollmentInput{ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, InstallationID: 10, RepositoryID: 20, StateHash: stateHash, PollHash: pollHash, PKCECiphertext: bytes.Repeat([]byte{6}, 29), PKCESealNonce: bytes.Repeat([]byte{7}, 12), RequestNonce: bytes.Repeat([]byte{8}, 32), ExpiresAt: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimEnrollmentState(ctx, stateHash)
	if err != nil {
		t.Fatal(err)
	}
	claim.Destroy()
	if err = s.CompleteEnrollment(ctx, enrollmentID); err != nil {
		t.Fatal(err)
	}
	status, err := s.PollEnrollment(ctx, pollHash)
	if err != nil || status.Status != "authorized" {
		t.Fatalf("poll=%#v %v", status, err)
	}
	status, err = s.PollEnrollment(ctx, pollHash)
	if err != nil || status.Status != "authorized" {
		t.Fatalf("idempotent poll=%#v %v", status, err)
	}
	// Existing controller/key can authorize a second exact repository and can
	// reauthorize a binding without replacing its original creation audit.
	secondState, secondPoll := bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{10}, 32)
	secondID, err := s.CreateEnrollment(ctx, EnrollmentInput{ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, InstallationID: 10, RepositoryID: 21, StateHash: secondState, PollHash: secondPoll, PKCECiphertext: bytes.Repeat([]byte{11}, 29), PKCESealNonce: bytes.Repeat([]byte{12}, 12), RequestNonce: bytes.Repeat([]byte{13}, 32), ExpiresAt: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimEnrollmentState(ctx, secondState); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEnrollment(ctx, secondID); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeBinding(ctx, controllerID, 10, 21); err != nil {
		t.Fatal(err)
	}
	reauthState, reauthPoll := bytes.Repeat([]byte{15}, 32), bytes.Repeat([]byte{16}, 32)
	reauthID, err := s.CreateEnrollment(ctx, EnrollmentInput{ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, InstallationID: 10, RepositoryID: 21, StateHash: reauthState, PollHash: reauthPoll, PKCECiphertext: bytes.Repeat([]byte{17}, 29), PKCESealNonce: bytes.Repeat([]byte{18}, 12), RequestNonce: bytes.Repeat([]byte{19}, 32), ExpiresAt: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimEnrollmentState(ctx, reauthState); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEnrollment(ctx, reauthID); err != nil {
		t.Fatalf("reauthorize binding: %v", err)
	}
	mismatchState := bytes.Repeat([]byte{20}, 32)
	mismatchID, err := s.CreateEnrollment(ctx, EnrollmentInput{ControllerID: controllerID, KeyID: keyID, PublicKey: bytes.Repeat([]byte{21}, 32), InstallationID: 10, RepositoryID: 22, StateHash: mismatchState, PollHash: bytes.Repeat([]byte{22}, 32), PKCECiphertext: bytes.Repeat([]byte{23}, 29), PKCESealNonce: bytes.Repeat([]byte{24}, 12), RequestNonce: bytes.Repeat([]byte{25}, 32), ExpiresAt: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimEnrollmentState(ctx, mismatchState); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEnrollment(ctx, mismatchID); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched key enrollment=%v", err)
	}
	if err = s.SyncSubscriptions(ctx, controllerID, 1, []Subscription{{SubscriptionID: subscriptionID, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncSubscriptions(ctx, controllerID, 1, []Subscription{{SubscriptionID: subscriptionID, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}}); err != nil {
		t.Fatalf("same generation retransmit: %v", err)
	}
	if err = s.SyncSubscriptions(ctx, controllerID, 3, []Subscription{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("skipped generation=%v", err)
	}
	event := SourceEvent{DeliveryID: uuid.NewString(), InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), ReceivedAt: now, ObservedAt: now}
	route := SourceRoute{ControllerID: controllerID, SubscriptionID: subscriptionID}
	push, err := s.PushSourceEvent(ctx, event, []SourceRoute{route})
	if err != nil || push.Deduplicated || push.Desired[0].Generation != 1 {
		t.Fatalf("push=%#v %v", push, err)
	}
	duplicate, err := s.PushSourceEvent(ctx, event, nil)
	if err != nil || !duplicate.Deduplicated {
		t.Fatalf("dedupe=%#v %v", duplicate, err)
	}
	// ACK/new-push share a subscription lock: either ACK wins before generation
	// 2 or it cleanly conflicts after it; generation 2 remains pending.
	second := event
	second.DeliveryID = uuid.NewString()
	second.SHA = strings.Repeat("b", 40)
	decision := Decision{MessageID: uuid.NewString(), Accepted: true}
	var wg sync.WaitGroup
	var ackErr, pushErr error
	wg.Add(2)
	go func() { defer wg.Done(); ackErr = s.DecideSource(ctx, controllerID, subscriptionID, 1, decision) }()
	go func() { defer wg.Done(); _, pushErr = s.PushSourceEvent(ctx, second, []SourceRoute{route}) }()
	wg.Wait()
	if pushErr != nil {
		t.Fatal(pushErr)
	}
	if ackErr != nil && !errors.Is(ackErr, ErrConflict) {
		t.Fatal(ackErr)
	}
	pending, err := s.PendingDesired(ctx, controllerID, 10)
	if err != nil || len(pending) != 1 || pending[0].Generation != 2 {
		t.Fatalf("pending=%#v %v", pending, err)
	}
	sessionID := uuid.NewString()
	if err = s.CreateChallenge(ctx, ChallengeInput{SessionID: sessionID, ControllerID: controllerID, KeyID: keyID, ClientNonce: bytes.Repeat([]byte{1}, 32), ServerNonce: bytes.Repeat([]byte{2}, 32), ACKDigest: bytes.Repeat([]byte{3}, 32), ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	authChallenge, err := s.LoadChallengeForAuthentication(ctx, sessionID)
	if err != nil || !bytes.Equal(authChallenge.PublicKey, publicKey) || authChallenge.ControllerID != controllerID || authChallenge.KeyID != keyID {
		t.Fatalf("auth challenge=%#v %v", authChallenge, err)
	}
	authChallenge.Destroy()
	if err = s.ConsumeChallenge(ctx, sessionID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.ConsumeChallenge(ctx, sessionID, now.Add(time.Hour)); !errors.Is(err, ErrReplay) {
		t.Fatalf("challenge replay=%v", err)
	}
	lease, err := s.AcquireLease(ctx, sessionID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	messageID := uuid.NewString()
	if err = s.RecordSessionMessage(ctx, lease, messageID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordSessionMessage(ctx, lease, messageID); !errors.Is(err, ErrReplay) {
		t.Fatalf("message replay=%v", err)
	}
	rotation := RotationInput{RotationID: uuid.NewString(), ControllerID: controllerID, OldKeyID: keyID, NewKeyID: uuid.NewString(), SessionID: sessionID, NewPublicKey: bytes.Repeat([]byte{14}, 32)}
	rotationChallenge, err := s.ProposeRotation(ctx, rotation, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadRotationChallenge(ctx, controllerID, rotation.RotationID)
	if err != nil || !bytes.Equal(loaded.Nonce, rotationChallenge.Nonce) {
		t.Fatalf("rotation load=%#v %v", loaded, err)
	}
	if err = s.ConfirmRotation(ctx, controllerID, rotation.RotationID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeRotation(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	revokedState := bytes.Repeat([]byte{26}, 32)
	revokedID, err := s.CreateEnrollment(ctx, EnrollmentInput{ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, InstallationID: 10, RepositoryID: 23, StateHash: revokedState, PollHash: bytes.Repeat([]byte{27}, 32), PKCECiphertext: bytes.Repeat([]byte{28}, 29), PKCESealNonce: bytes.Repeat([]byte{29}, 12), RequestNonce: bytes.Repeat([]byte{30}, 32), ExpiresAt: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimEnrollmentState(ctx, revokedState); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEnrollment(ctx, revokedID); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoked key enrollment=%v", err)
	}
	cursor, err := s.StartRecoveryScan(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err = s.AdvanceRecoveryCursor(ctx, cursor, "opaque-next")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRecoveryScan(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	recovery := RecoveryDelivery{DeliveryNumber: 100, DeliveryID: uuid.NewString(), OccurredAt: now}
	if _, err = s.DiscoverRecoveryDelivery(ctx, recovery); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimRecovery(ctx, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%#v %v", claims, err)
	}
	recoveryPush := SourceEvent{DeliveryID: recovery.DeliveryID, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main", SHA: strings.Repeat("d", 40), ReceivedAt: now, ObservedAt: now}
	if _, err = s.PushSourceEvent(ctx, recoveryPush, []SourceRoute{route}); err != nil {
		t.Fatalf("persist recovered delivery: %v", err)
	}
	claims, err = s.ClaimRecovery(ctx, 1, time.Minute)
	if err != nil || len(claims) != 0 {
		t.Fatalf("recovered delivery reclaimed=%#v %v", claims, err)
	}
	// Access removal is persisted before binding revocation and remains pending.
	access := AccessEventInput{DeliveryID: uuid.NewString(), InstallationID: 10, RepositoryID: 20, ChangeCode: "repository.removed", ReceivedAt: now, ObservedAt: now, RemoveAccess: true}
	accessRoute := AccessRoute{EventID: uuid.NewString(), ControllerID: controllerID}
	if _, err = s.PushAccessEvent(ctx, access, []AccessRoute{accessRoute}); err != nil {
		t.Fatal(err)
	}
	accessPending, err := s.PendingAccess(ctx, controllerID, 10)
	if err != nil || len(accessPending) != 1 {
		t.Fatalf("pending access=%#v %v", accessPending, err)
	}
	emptyEvent := SourceEvent{DeliveryID: uuid.NewString(), InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main", SHA: strings.Repeat("c", 40), ReceivedAt: now, ObservedAt: now}
	if _, err = s.PushSourceEvent(ctx, emptyEvent, nil); err != nil {
		t.Fatalf("empty fanout after revoke: %v", err)
	}
	if dedupe, err := s.PushSourceEvent(ctx, emptyEvent, []SourceRoute{route}); err != nil || !dedupe.Deduplicated {
		t.Fatalf("empty fanout dedupe=%#v %v", dedupe, err)
	}
}
