package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
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

func TestPostgreSQLAcquireLeaseSerializesWithTerminalSessionMutations(t *testing.T) {
	dsn := os.Getenv("RIG_RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skipf("RIG_RELAY_TEST_DATABASE_URL is unset; AcquireLease terminal-mutation concurrency regression not run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "relay_lease_race_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	for index, operation := range []string{"controller revoke", "self key revoke", "rotation finalize"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			controllerID, keyID := uuid.NewString(), uuid.NewString()
			activeSessionID, candidateSessionID := uuid.NewString(), uuid.NewString()
			leaseID := uuid.NewString()
			store, err := New(pool, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO relay_controllers(controller_id,state,created_at) VALUES($1,'active',$2)`, controllerID, now); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO relay_controller_keys(key_id,controller_id,public_key,state,created_at) VALUES($1,$2,$3,'active',$4)`, keyID, controllerID, bytes.Repeat([]byte{byte(20 + index)}, protocol.PublicKeyBytes), now); err != nil {
				t.Fatal(err)
			}
			for sessionIndex, sessionID := range []string{activeSessionID, candidateSessionID} {
				challengeByte := byte(80 + index*8 + sessionIndex)
				challenge := ChallengeInput{
					SessionID: sessionID, ControllerID: controllerID, KeyID: keyID,
					ClientNonce: bytes.Repeat([]byte{challengeByte}, protocol.NonceBytes),
					ServerNonce: bytes.Repeat([]byte{challengeByte + 2}, protocol.NonceBytes),
					ACKDigest:   bytes.Repeat([]byte{challengeByte + 4}, sha256.Size),
					ExpiresAt:   now.Add(time.Minute),
				}
				if err := store.CreateChallenge(ctx, challenge); err != nil {
					t.Fatal(err)
				}
				if err := store.ConsumeChallenge(ctx, sessionID, now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `INSERT INTO relay_controller_leases(controller_id,session_id,lease_id,fence,expires_at,updated_at) VALUES($1,$2,$3,1,$4,$5)`, controllerID, activeSessionID, leaseID, now.Add(time.Minute), now); err != nil {
				t.Fatal(err)
			}
			lease := Lease{ControllerID: controllerID, SessionID: activeSessionID, LeaseID: leaseID, Fence: 1, ExpiresAt: now.Add(time.Minute)}
			commandType := CommandControllerRevoke
			terminal := func(runCtx context.Context, command SessionCommand) error {
				_, applyErr := store.ApplyControllerRevocation(runCtx, lease, command, controllerID)
				return applyErr
			}
			switch operation {
			case "self key revoke":
				commandType = CommandKeyRevoke
				terminal = func(runCtx context.Context, command SessionCommand) error {
					_, applyErr := store.ApplyKeyRevocation(runCtx, lease, command, controllerID, keyID)
					return applyErr
				}
			case "rotation finalize":
				commandType = CommandRotationFinalize
				rotationID, newKeyID := uuid.NewString(), uuid.NewString()
				if _, err := pool.Exec(ctx, `INSERT INTO relay_controller_keys(key_id,controller_id,public_key,state,rotation_id,rotation_old_key_id,rotation_session_id,rotation_nonce,rotation_expires_at,created_at,possession_confirmed_at) VALUES($1,$2,$3,'pending',$4,$5,$6,$7,$8,$9,$9)`, newKeyID, controllerID, bytes.Repeat([]byte{byte(40 + index)}, protocol.PublicKeyBytes), rotationID, keyID, activeSessionID, bytes.Repeat([]byte{byte(60 + index)}, protocol.NonceBytes), now.Add(time.Minute), now); err != nil {
					t.Fatal(err)
				}
				terminal = func(runCtx context.Context, command SessionCommand) error {
					_, applyErr := store.ApplyRotationFinalization(runCtx, lease, command, rotationID)
					return applyErr
				}
			}
			messageID := uuid.NewString()
			command := SessionCommand{MessageID: messageID, Type: commandType, Digest: sha256.Sum256([]byte(messageID))}
			runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := make(chan struct{})
			type outcome struct {
				name string
				err  error
			}
			results := make(chan outcome, 2)
			go func() {
				<-start
				_, acquireErr := store.AcquireLease(runCtx, candidateSessionID, time.Minute)
				results <- outcome{name: "acquire", err: acquireErr}
			}()
			go func() {
				<-start
				results <- outcome{name: "terminal", err: terminal(runCtx, command)}
			}()
			close(start)
			seen := make(map[string]error, 2)
			for len(seen) < 2 {
				select {
				case result := <-results:
					seen[result.name] = result.err
				case <-runCtx.Done():
					t.Fatalf("possible deadlock: %v", runCtx.Err())
				}
			}
			if seen["terminal"] != nil {
				t.Fatalf("terminal mutation: %v", seen["terminal"])
			}
			if !errors.Is(seen["acquire"], ErrConflict) && !errors.Is(seen["acquire"], ErrExpired) {
				t.Fatalf("AcquireLease error=%v, want conflict or expired", seen["acquire"])
			}
		})
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
	oldPrivateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	publicKey := oldPrivateKey.Public().(ed25519.PublicKey)
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
	newCommand := func(commandType SessionCommandType) SessionCommand {
		messageID := uuid.NewString()
		return SessionCommand{MessageID: messageID, Type: commandType, Digest: sha256.Sum256([]byte(messageID))}
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
	if result, applyErr := s.ApplyBindingRemoval(ctx, lease, newCommand(CommandBindingRemove), 10, 21); applyErr != nil || result.Kind != ResultBindingRemoved {
		t.Fatalf("binding removal=%#v %v", result, applyErr)
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
	subscriptions := []Subscription{{SubscriptionID: subscriptionID, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}}
	syncCommand := newCommand(CommandSubscriptionsSync)
	if result, applyErr := s.ApplySubscriptionsSync(ctx, lease, syncCommand, 1, subscriptions); applyErr != nil || result.Kind != ResultSubscriptionsSynced {
		t.Fatalf("subscriptions sync=%#v %v", result, applyErr)
	}
	if result, applyErr := s.ApplySubscriptionsSync(ctx, lease, syncCommand, 1, subscriptions); applyErr != nil || result.Kind != ResultSubscriptionsSynced {
		t.Fatalf("same generation retransmit=%#v %v", result, applyErr)
	}
	if _, applyErr := s.ApplySubscriptionsSync(ctx, lease, newCommand(CommandSubscriptionsSync), 3, []Subscription{}); !errors.Is(applyErr, ErrConflict) {
		t.Fatalf("skipped generation=%v", applyErr)
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
	decisionCommand := newCommand(CommandAckSource)
	targetMessageID := uuid.NewString()
	var wg sync.WaitGroup
	var ackErr, pushErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, ackErr = s.ApplySourceDecision(ctx, lease, decisionCommand, subscriptionID, 1, targetMessageID, true, "")
	}()
	go func() { defer wg.Done(); _, pushErr = s.PushSourceEvent(ctx, second, []SourceRoute{route}) }()
	wg.Wait()
	if pushErr != nil {
		t.Fatal(pushErr)
	}
	if ackErr != nil && !errors.Is(ackErr, ErrConflict) {
		t.Fatal(ackErr)
	}
	pending, err := s.PendingDesired(ctx, lease, 10)
	if err != nil || len(pending) != 1 || pending[0].Generation != 2 {
		t.Fatalf("pending=%#v %v", pending, err)
	}
	newPrivateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{14}, ed25519.SeedSize))
	newPublicKey := newPrivateKey.Public().(ed25519.PublicKey)
	rotation := RotationInput{RotationID: uuid.NewString(), ControllerID: controllerID, OldKeyID: keyID, NewKeyID: uuid.NewString(), SessionID: sessionID, NewPublicKey: newPublicKey}
	rotationChallenge, err := s.ApplyRotationProposal(ctx, lease, newCommand(CommandRotationPropose), rotation, time.Minute)
	if err != nil || rotationChallenge.Kind != ResultRotationChallenge {
		t.Fatalf("rotation proposal=%#v %v", rotationChallenge, err)
	}
	transcript, err := protocol.KeyRotationTranscript(protocol.RotationProof{RotationID: rotation.RotationID, ControllerID: controllerID, OldKeyID: keyID, NewKeyID: rotation.NewKeyID, NewPublicKey: base64.RawURLEncoding.EncodeToString(newPublicKey), SessionID: sessionID, ServerNonce: base64.RawURLEncoding.EncodeToString(rotationChallenge.Nonce), ExpiresAt: rotationChallenge.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := protocol.Sign(newPrivateKey, transcript)
	if err != nil {
		t.Fatal(err)
	}
	if result, applyErr := s.ApplyRotationConfirmation(ctx, lease, newCommand(CommandRotationConfirm), rotation.RotationID, signature); applyErr != nil || result.Kind != ResultRotationConfirmed {
		t.Fatalf("rotation confirmation=%#v %v", result, applyErr)
	}
	if result, applyErr := s.ApplyRotationFinalization(ctx, lease, newCommand(CommandRotationFinalize), rotation.RotationID); applyErr != nil || result.Kind != ResultRotationFinalized || result.RetiredKeyID != keyID {
		t.Fatalf("rotation finalization=%#v %v", result, applyErr)
	}
	rotationChallenge.Destroy()
	newSessionID := uuid.NewString()
	if err = s.CreateChallenge(ctx, ChallengeInput{SessionID: newSessionID, ControllerID: controllerID, KeyID: rotation.NewKeyID, ClientNonce: bytes.Repeat([]byte{31}, 32), ServerNonce: bytes.Repeat([]byte{32}, 32), ACKDigest: bytes.Repeat([]byte{33}, 32), ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err = s.ConsumeChallenge(ctx, newSessionID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	lease, err = s.AcquireLease(ctx, newSessionID, time.Minute)
	if err != nil {
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
	var restoredDeliveryCount, restoredAccessEventCount int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM relay_github_deliveries WHERE delivery_id=$1`, restored.DeliveryID).Scan(&restoredDeliveryCount); err != nil || restoredDeliveryCount != 1 {
		t.Fatalf("informational restore delivery count=%d err=%v", restoredDeliveryCount, err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM relay_access_events WHERE delivery_id=$1`, restored.DeliveryID).Scan(&restoredAccessEventCount); err != nil || restoredAccessEventCount != 0 {
		t.Fatalf("informational restore access events=%d err=%v", restoredAccessEventCount, err)
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
	accessPending, err := s.PendingAccess(ctx, lease, 10)
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

	// Repeatedly replace the full subscription set without acknowledging its
	// desired state. Retirement is terminal after the configured history
	// horizon, while the current generation and its pending target remain.
	churnIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for index, churnID := range churnIDs {
		generation := uint64(index + 2)
		churnSubscription := Subscription{SubscriptionID: churnID, InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main"}
		if result, applyErr := s.ApplySubscriptionsSync(ctx, lease, newCommand(CommandSubscriptionsSync), generation, []Subscription{churnSubscription}); applyErr != nil || result.Kind != ResultSubscriptionsSynced {
			t.Fatalf("churn sync generation %d=%#v %v", generation, result, applyErr)
		}
		churnEvent := SourceEvent{DeliveryID: uuid.NewString(), InstallationID: 10, RepositoryID: 20, Ref: "refs/heads/main", SHA: strings.Repeat(fmt.Sprintf("%x", index+1), 40), ReceivedAt: now, ObservedAt: now}
		if _, pushErr := s.PushSourceEvent(ctx, churnEvent, []SourceRoute{{ControllerID: controllerID, SubscriptionID: churnID}}); pushErr != nil {
			t.Fatalf("churn push generation %d: %v", generation, pushErr)
		}
	}
	now = now.Add(8 * 24 * time.Hour)
	pruned, err := s.PruneDurableState(ctx, DefaultDurableRetentionPolicy())
	if err != nil {
		t.Fatalf("prune churn: %v", err)
	}
	if pruned.RetiredSubscriptions < 3 || pruned.RetiredDesiredStates < 2 || pruned.SourceDeliveryTargets < 2 {
		t.Fatalf("retired churn was not bounded: %+v", pruned)
	}
	var retiredCount, currentCount, pendingCurrent, currentTarget int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM relay_subscriptions WHERE subscription_id IN ($1,$2,$3)`, subscriptionID, churnIDs[0], churnIDs[1]).Scan(&retiredCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM relay_subscriptions WHERE subscription_id=$1 AND retired_generation IS NULL`, churnIDs[2]).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM relay_desired_states WHERE subscription_id=$1 AND decision IS NULL`, churnIDs[2]).Scan(&pendingCurrent); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM relay_source_delivery_targets WHERE subscription_id=$1`, churnIDs[2]).Scan(&currentTarget); err != nil {
		t.Fatal(err)
	}
	if retiredCount != 0 || currentCount != 1 || pendingCurrent != 1 || currentTarget != 1 {
		t.Fatalf("retired=%d current=%d pending=%d target=%d", retiredCount, currentCount, pendingCurrent, currentTarget)
	}
}
