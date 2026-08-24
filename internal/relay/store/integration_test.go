package store

import (
	"bytes"
	"context"
	"crypto/sha256"
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

func TestPostgreSQLRelayMigrationUpgradeFrom001(t *testing.T) {
	dsn := os.Getenv("RIG_RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skipf("RIG_RELAY_TEST_DATABASE_URL is unset; real PostgreSQL migration upgrade not run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "relay_upgrade_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	initial, err := migrationFiles.ReadFile("migrations/001_relay_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(initial)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE relay_schema_migrations(version text PRIMARY KEY, checksum bytea NOT NULL CHECK(octet_length(checksum)=32), applied_at timestamptz NOT NULL)`); err == nil {
		_, err = tx.Exec(ctx, string(initial))
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO relay_schema_migrations(version,checksum,applied_at) VALUES($1,$2,clock_timestamp())`, "001_relay_state.sql", digest[:])
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	deliveryID := uuid.NewString()
	when := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = pool.Exec(ctx, `INSERT INTO relay_recovery_deliveries(delivery_number,delivery_id,occurred_at,discovered_at,next_attempt_at) VALUES($1,$2,$3,$3,$3)`, int64(9876), deliveryID, when); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatalf("upgrade 001 through current: %v", err)
	}

	var selectedNumber, attemptNumber int64
	var selectedID, attemptID string
	var successful bool
	if err = pool.QueryRow(ctx, `
		SELECT g.delivery_number,g.delivery_id::text,a.delivery_number,a.delivery_id::text,a.successful
		FROM relay_recovery_deliveries g
		JOIN relay_recovery_delivery_attempts a ON (a.delivery_number,a.delivery_id)=(g.delivery_number,g.delivery_id)
		WHERE g.delivery_id=$1`, deliveryID).Scan(&selectedNumber, &selectedID, &attemptNumber, &attemptID, &successful); err != nil {
		t.Fatal(err)
	}
	if selectedNumber != 9876 || attemptNumber != 9876 || selectedID != deliveryID || attemptID != deliveryID || successful {
		t.Fatalf("backfill group=(%d,%s) attempt=(%d,%s,%t)", selectedNumber, selectedID, attemptNumber, attemptID, successful)
	}

	fkTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fkTx.Exec(ctx, `SET CONSTRAINTS relay_recovery_selected_attempt IMMEDIATE`); err != nil {
		_ = fkTx.Rollback(ctx)
		t.Fatalf("selected attempt foreign key missing: %v", err)
	}
	_, err = fkTx.Exec(ctx, `UPDATE relay_recovery_deliveries SET delivery_number=$1 WHERE delivery_id=$2`, int64(9877), deliveryID)
	_ = fkTx.Rollback(ctx)
	if err == nil {
		t.Fatal("selected attempt foreign key accepted a delivery number absent from attempt history")
	}

	ignoredID := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO relay_github_deliveries(delivery_id,delivery_kind,received_at,persisted_at) VALUES($1,'ignored',$2,$2)`, ignoredID, when); err != nil {
		t.Fatalf("004 ignored delivery kind missing: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO relay_ignored_deliveries(delivery_id,reason_code,persisted_at) VALUES($1,'push.deleted',$2)`, ignoredID, when); err != nil {
		t.Fatalf("004 ignored table/reason enum missing: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE relay_ignored_deliveries SET reason_code='provider.detail' WHERE delivery_id=$1`, ignoredID); err == nil {
		t.Fatal("004 ignored reason enum accepted provider detail")
	}
}

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
	if _, err = s.CreateEnrollment(ctx, EnrollmentInput{ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, InstallationID: 10, RepositoryID: 20, StateHash: bytes.Repeat([]byte{31}, 32), PollHash: bytes.Repeat([]byte{32}, 32), PKCECiphertext: bytes.Repeat([]byte{33}, 29), PKCESealNonce: bytes.Repeat([]byte{34}, 12), RequestNonce: bytes.Repeat([]byte{8}, 32), ExpiresAt: now.Add(10 * time.Minute)}); !errors.Is(err, ErrReplay) {
		t.Fatalf("signed request nonce replay=%v", err)
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
	accessRoutes, err := s.AccessRoutes(ctx, 10, 20)
	if err != nil || len(accessRoutes) != 0 {
		t.Fatalf("revoked binding routes=%v err=%v", accessRoutes, err)
	}
	restored := AccessEventInput{DeliveryID: uuid.NewString(), InstallationID: 10, ChangeCode: "installation.restored", ReceivedAt: now, ObservedAt: now}
	if _, err = s.PushAccessEvent(ctx, restored, nil); err != nil {
		t.Fatalf("persist informational restore event: %v", err)
	}
	accessRoutes, err = s.AccessRoutes(ctx, 10, 20)
	if err != nil || len(accessRoutes) != 0 {
		t.Fatalf("informational restore resurrected binding routes=%v err=%v", accessRoutes, err)
	}
	emptyEvent := SourceEvent{DeliveryID: uuid.NewString(), InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main", SHA: strings.Repeat("c", 40), ReceivedAt: now, ObservedAt: now}
	if _, err = s.PushSourceEvent(ctx, emptyEvent, nil); err != nil {
		t.Fatalf("empty fanout after revoke: %v", err)
	}
	if dedupe, err := s.PushSourceEvent(ctx, emptyEvent, []SourceRoute{route}); err != nil || !dedupe.Deduplicated {
		t.Fatalf("empty fanout dedupe=%#v %v", dedupe, err)
	}
	freshState := bytes.Repeat([]byte{35}, 32)
	freshID, err := s.CreateEnrollment(ctx, EnrollmentInput{
		ControllerID: controllerID, KeyID: rotation.NewKeyID, PublicKey: rotation.NewPublicKey,
		InstallationID: 10, RepositoryID: 20, StateHash: freshState, PollHash: bytes.Repeat([]byte{36}, 32),
		PKCECiphertext: bytes.Repeat([]byte{37}, 29), PKCESealNonce: bytes.Repeat([]byte{38}, 12),
		RequestNonce: bytes.Repeat([]byte{39}, 32), ExpiresAt: now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimEnrollmentState(ctx, freshState); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEnrollment(ctx, freshID); err != nil {
		t.Fatalf("fresh re-enrollment: %v", err)
	}
	accessRoutes, err = s.AccessRoutes(ctx, 10, 20)
	if err != nil || len(accessRoutes) != 1 || accessRoutes[0] != controllerID {
		t.Fatalf("fresh re-enrollment routes=%v err=%v", accessRoutes, err)
	}
	accessPending, err := s.PendingAccess(ctx, controllerID, 10)
	if err != nil || len(accessPending) != 1 {
		t.Fatalf("pending access=%#v %v", accessPending, err)
	}

	ignoredID := uuid.NewString()
	if dedupe, err := s.PushIgnoredDelivery(ctx, ignoredID, "push.deleted", now); err != nil || dedupe {
		t.Fatalf("ignored first=%v %v", dedupe, err)
	}
	if dedupe, err := s.PushIgnoredDelivery(ctx, ignoredID, "push.deleted", now); err != nil || !dedupe {
		t.Fatalf("ignored replay=%v %v", dedupe, err)
	}
	if _, err := s.PushSourceEvent(ctx, SourceEvent{DeliveryID: ignoredID, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main", SHA: strings.Repeat("e", 40), ReceivedAt: now, ObservedAt: now}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-kind GUID=%v", err)
	}

	groupID := uuid.NewString()
	for _, attempt := range []RecoveryDelivery{
		{DeliveryNumber: 200, DeliveryID: groupID, OccurredAt: now.Add(-3 * time.Minute)},
		{DeliveryNumber: 201, DeliveryID: groupID, OccurredAt: now.Add(-2 * time.Minute)},
	} {
		if _, err := s.DiscoverRecoveryDelivery(ctx, attempt); err != nil {
			t.Fatalf("A/B discovery=%v", err)
		}
	}
	claims, err = s.ClaimRecovery(ctx, 100, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].DeliveryID != groupID || claims[0].DeliveryNumber != 201 {
		t.Fatalf("newest failure claim=%#v %v", claims, err)
	}
	staleB := claims[0]
	if _, err := s.DiscoverRecoveryDelivery(ctx, RecoveryDelivery{DeliveryNumber: 202, DeliveryID: groupID, OccurredAt: now.Add(-time.Minute), Successful: true}); err != nil {
		t.Fatalf("success C discovery=%v", err)
	}
	if err := s.RecordRecoveryAttempt(ctx, staleB, now.Add(time.Minute), "github.unavailable"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale B claim=%v", err)
	}
	claims, err = s.ClaimRecovery(ctx, 100, time.Minute)
	if err != nil || len(claims) != 0 {
		t.Fatalf("provider-success group claim=%#v %v", claims, err)
	}

	inboundFirstID := uuid.NewString()
	if _, err := s.PushIgnoredDelivery(ctx, inboundFirstID, "push.deleted", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DiscoverRecoveryDelivery(ctx, RecoveryDelivery{DeliveryNumber: 300, DeliveryID: inboundFirstID, OccurredAt: now}); err != nil {
		t.Fatalf("inbound-before-discovery=%v", err)
	}

	unresolvedID := uuid.NewString()
	if _, err := s.DiscoverRecoveryDelivery(ctx, RecoveryDelivery{DeliveryNumber: 400, DeliveryID: unresolvedID, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	resume, err := s.StartRecoveryScan(ctx, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	claims, err = s.ClaimRecovery(ctx, 100, time.Minute)
	if err != nil || len(claims) != 0 {
		t.Fatalf("incomplete scan exposed claims=%#v %v", claims, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE relay_recovery_cursor SET lease_expires_at=$1 WHERE singleton`, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	takenOver, err := s.StartRecoveryScan(ctx, now.Add(-time.Hour), now)
	if err != nil || takenOver.ScanID != resume.ScanID || !takenOver.WindowStartedAt.Equal(resume.WindowStartedAt) || !takenOver.WindowEndsAt.Equal(resume.WindowEndsAt) {
		t.Fatalf("takeover=%+v original=%+v err=%v", takenOver, resume, err)
	}
	if err = s.CompleteRecoveryScan(ctx, takenOver); err != nil {
		t.Fatal(err)
	}
	claims, err = s.ClaimRecovery(ctx, 100, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].DeliveryID != unresolvedID {
		t.Fatalf("post-scan unresolved claim=%#v %v", claims, err)
	}
}
