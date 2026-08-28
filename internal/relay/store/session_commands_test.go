package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func commandTestLease() Lease {
	return Lease{ControllerID: testController, SessionID: testSession, LeaseID: testLease, Fence: 7, ExpiresAt: fixedNow.Add(time.Minute)}
}

func subscriptionTestCommand() SessionCommand {
	return SessionCommand{MessageID: testMessage, Type: CommandSubscriptionsSync, Digest: sha256.Sum256([]byte("canonical subscriptions.sync frame"))}
}

func expectEmptySubscriptionLockSnapshot(m pgxmock.PgxPoolIface) {
	m.ExpectQuery("SELECT subscription_id::text,installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"subscription_id", "installation_id", "repository_id", "tracked_ref"}))
}

func expectActiveCommandLease(m pgxmock.PgxPoolIface, lease Lease, command SessionCommand) {
	expectActiveCommandLeaseWithKey(m, lease, command, testKey)
}

func expectActiveCommandLeaseWithKey(m pgxmock.PgxPoolIface, lease Lease, command SessionCommand, keyID string) {
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, command.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnRows(
		pgxmock.NewRows([]string{"lease_expires"}).AddRow(fixedNow.Add(time.Minute)),
	)
	m.ExpectQuery("SELECT s.expires_at,s.revoked_at,c.state,k.state,s.key_id::text").WithArgs(lease.ControllerID, lease.SessionID).WillReturnRows(
		pgxmock.NewRows([]string{"session_expires", "revoked", "controller_state", "key_state", "key_id"}).AddRow(fixedNow.Add(time.Hour), nil, "active", "active", keyID),
	)
}

func expectMissingCommand(m pgxmock.PgxPoolIface, lease Lease, command SessionCommand) {
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnError(pgx.ErrNoRows)
}

func expectSubscriptionCommandInsert(m pgxmock.PgxPoolIface, lease Lease, command SessionCommand, generation uint64, count uint32) {
	m.ExpectExec("INSERT INTO relay_session_commands").WithArgs(
		lease.ControllerID, command.MessageID, lease.SessionID, lease.LeaseID, lease.Fence,
		command.Digest[:], string(command.Type), string(ResultSubscriptionsSynced), nil,
		generation, count, nil, nil, nil, nil, nil, nil, nil, nil, fixedNow,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func expectDecisionCommandInsert(m pgxmock.PgxPoolIface, lease Lease, command SessionCommand, kind SessionCommandResultKind, errorCode string) {
	m.ExpectExec("INSERT INTO relay_session_commands").WithArgs(
		lease.ControllerID, command.MessageID, lease.SessionID, lease.LeaseID, lease.Fence,
		command.Digest[:], string(command.Type), string(kind), optionalString(errorCode),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fixedNow,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func expectTypedCommandInsert(m pgxmock.PgxPoolIface, lease Lease, command SessionCommand, result SessionCommandResult) {
	m.ExpectExec("INSERT INTO relay_session_commands").WithArgs(
		lease.ControllerID, command.MessageID, lease.SessionID, lease.LeaseID, lease.Fence,
		command.Digest[:], string(command.Type), string(result.Kind), optionalString(result.ErrorCode),
		optionalUint64(result.Generation), optionalCount(result.Kind, result.Count), optionalPositive(result.InstallationID), optionalPositive(result.RepositoryID),
		optionalUUID(result.ControllerID), optionalUUID(result.KeyID), optionalUUID(result.RotationID), optionalUUID(result.RetiredKeyID), optionalBytes(result.Nonce), optionalTime(result.ExpiresAt), fixedNow,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func TestControllerSessionAdvisoryLockIsDomainSeparatedAndOrdered(t *testing.T) {
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("lock order"))}
	guard := controllerSessionLockKey(lease.ControllerID)
	if guard == sessionCommandLockKey(lease.ControllerID, command.MessageID) || guard == deliveryLockKey(command.MessageID) {
		t.Fatal("controller-session advisory lock is not domain separated")
	}

	t.Run("prepare command before controller session before lease", func(t *testing.T) {
		s, m := mockStore(t)
		_ = s
		stop := errors.New("stop at lease row")
		m.ExpectBegin()
		tx, err := m.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, command.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(guard).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(stop)
		m.ExpectRollback()
		if _, err = s.prepareSessionCommand(context.Background(), tx, lease, command); !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err = tx.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("acquire discovers then controller session before lease", func(t *testing.T) {
		s, m := mockStore(t)
		stop := errors.New("stop at controller lease")
		m.ExpectBegin()
		m.ExpectQuery("SELECT controller_id::text FROM relay_sessions").WithArgs(lease.SessionID).WillReturnRows(pgxmock.NewRows([]string{"controller_id"}).AddRow(lease.ControllerID))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(guard).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT session_id::text,fence,expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID).WillReturnError(stop)
		m.ExpectRollback()
		if _, err := s.AcquireLease(context.Background(), lease.SessionID, time.Minute); !errors.Is(err, stop) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRenewLeaseUsesExactFenceAndRejectsExpiredOrSupersededLease(t *testing.T) {
	lease := commandTestLease()

	t.Run("renews same fence with compare and swap", func(t *testing.T) {
		s, m := mockStore(t)
		oldExpiry := fixedNow.Add(30 * time.Second)
		newExpiry := fixedNow.Add(2 * time.Minute)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnRows(pgxmock.NewRows([]string{"lease_expires"}).AddRow(oldExpiry))
		m.ExpectQuery("SELECT s.expires_at,s.revoked_at,c.state,k.state").WithArgs(lease.ControllerID, lease.SessionID).WillReturnRows(
			pgxmock.NewRows([]string{"session_expires", "revoked", "controller_state", "key_state"}).AddRow(fixedNow.Add(time.Hour), nil, "active", "active"),
		)
		m.ExpectExec("UPDATE relay_controller_leases SET expires_at").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence, newExpiry, fixedNow, oldExpiry).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		got, err := s.RenewLease(context.Background(), lease, 2*time.Minute)
		if err != nil || got.Fence != lease.Fence || !got.ExpiresAt.Equal(newExpiry) {
			t.Fatalf("lease=%#v error=%v", got, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expired lease cannot renew", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnRows(pgxmock.NewRows([]string{"lease_expires"}).AddRow(fixedNow))
		m.ExpectQuery("SELECT s.expires_at,s.revoked_at,c.state,k.state").WithArgs(lease.ControllerID, lease.SessionID).WillReturnRows(pgxmock.NewRows([]string{"session_expires", "revoked", "controller_state", "key_state"}).AddRow(fixedNow.Add(time.Hour), nil, "active", "active"))
		m.ExpectRollback()
		if _, err := s.RenewLease(context.Background(), lease, time.Minute); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("superseded fence cannot renew", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(pgx.ErrNoRows)
		m.ExpectRollback()
		if _, err := s.RenewLease(context.Background(), lease, time.Minute); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestValidateLeaseRejectsRevokedAndSupersededState(t *testing.T) {
	lease := commandTestLease()
	for _, tc := range []struct {
		name       string
		rows       *pgxmock.Rows
		queryError error
	}{
		{name: "revoked session", rows: pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked", "controller_state", "key_state"}).AddRow(fixedNow.Add(time.Minute), fixedNow.Add(time.Hour), fixedNow, "active", "active")},
		{name: "superseded lease", queryError: pgx.ErrNoRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockStore(t)
			expect := m.ExpectQuery("SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence)
			if tc.queryError != nil {
				expect.WillReturnError(tc.queryError)
			} else {
				expect.WillReturnRows(tc.rows)
			}
			if err := s.ValidateLease(context.Background(), lease); !errors.Is(err, ErrConflict) {
				t.Fatalf("error=%v", err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplySubscriptionsSyncCommitsDomainAndCommandAtomically(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectEmptySubscriptionLockSnapshot(m)
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectExec("INSERT INTO relay_subscription_heads").WithArgs(testController, uint64(1), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectSubscriptionCommandInsert(m, lease, command, 1, 0)
	m.ExpectCommit()
	result, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{})
	if err != nil || result.Kind != ResultSubscriptionsSynced || result.Generation != 1 || result.Count != 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySubscriptionsSyncReplaysTypedResultWithoutDomainMutation(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectEmptySubscriptionLockSnapshot(m)
	expectActiveCommandLease(m, lease, command)
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultSubscriptionsSynced), nil, int64(1), int64(0), nil, nil, nil, nil, nil, nil, nil, nil),
	)
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	result, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{})
	if err != nil || result.Kind != ResultSubscriptionsSynced || result.Generation != 1 || result.Count != 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSessionCommandRejectsSameDigestWithDifferentCommandType(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	m.ExpectBegin()
	tx, err := m.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectActiveCommandLease(m, lease, command)
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(CommandAckSource), command.Digest[:], string(ResultDecisionApplied), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
	)
	m.ExpectRollback()
	if _, err = s.prepareSessionCommand(context.Background(), tx, lease, command); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v", err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyDecisionProtocolErrorReturnsExactDurableResultOrPersistsError(t *testing.T) {
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("durable source ack"))}

	t.Run("exact replay", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
			pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultDecisionApplied), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
		)
		m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		result, err := s.ApplyDecisionProtocolError(context.Background(), lease, command, "unknown_target")
		if err != nil || result.Kind != ResultDecisionApplied {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("exact protocol error replay keeps stored code", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
			pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultProtocolError), "target_mismatch", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
		)
		m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		result, err := s.ApplyDecisionProtocolError(context.Background(), lease, command, "unknown_target")
		if err != nil || result.Kind != ResultProtocolError || result.ErrorCode != "target_mismatch" {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing command persists typed protocol error", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		expectDecisionCommandInsert(m, lease, command, ResultProtocolError, "unknown_target")
		m.ExpectCommit()
		result, err := s.ApplyDecisionProtocolError(context.Background(), lease, command, "unknown_target")
		if err != nil || result.Kind != ResultProtocolError || result.ErrorCode != "unknown_target" {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplyDecisionProtocolErrorValidatesFenceTypeCodeAndRollsBack(t *testing.T) {
	lease := commandTestLease()
	valid := SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("local decision protocol error"))}
	for _, test := range []struct {
		name    string
		lease   Lease
		command SessionCommand
		code    string
	}{
		{name: "invalid lease", lease: Lease{}, command: valid, code: "unknown_target"},
		{name: "non decision type", lease: lease, command: SessionCommand{MessageID: testMessage, Type: CommandSubscriptionsSync, Digest: valid.Digest}, code: "unknown_target"},
		{name: "unknown code", lease: lease, command: valid, code: "provider_detail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, m := mockStore(t)
			if _, err := s.ApplyDecisionProtocolError(context.Background(), test.lease, test.command, test.code); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("stale fence rolls back", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, valid.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(pgx.ErrNoRows)
		m.ExpectRollback()
		if _, err := s.ApplyDecisionProtocolError(context.Background(), lease, valid, "stale_target"); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ledger failure rolls back", func(t *testing.T) {
		s, m := mockStore(t)
		outage := errors.New("ledger unavailable")
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, valid)
		expectMissingCommand(m, lease, valid)
		expectAnyCommandInsert(m).WillReturnError(outage)
		m.ExpectRollback()
		if _, err := s.ApplyDecisionProtocolError(context.Background(), lease, valid, "target_mismatch"); !errors.Is(err, outage) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplySubscriptionsSyncRejectsDigestMismatchAndStaleFenceBeforeMutation(t *testing.T) {
	lease, command := commandTestLease(), subscriptionTestCommand()

	t.Run("digest mismatch", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectEmptySubscriptionLockSnapshot(m)
		expectEmptySubscriptionLockSnapshot(m)
		expectActiveCommandLease(m, lease, command)
		otherDigest := sha256.Sum256([]byte("different canonical frame"))
		m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
			pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), otherDigest[:], string(ResultSubscriptionsSynced), nil, int64(1), int64(0), nil, nil, nil, nil, nil, nil, nil, nil),
		)
		m.ExpectRollback()
		if _, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stale fence", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectEmptySubscriptionLockSnapshot(m)
		expectEmptySubscriptionLockSnapshot(m)
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, command.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(pgx.ErrNoRows)
		m.ExpectRollback()
		if _, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplySubscriptionsSyncRollsBackDomainWhenCommandLedgerFails(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	outage := errors.New("command ledger unavailable")
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectEmptySubscriptionLockSnapshot(m)
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectExec("INSERT INTO relay_subscription_heads").WithArgs(testController, uint64(1), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_session_commands").WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnError(outage)
	m.ExpectRollback()
	if _, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{}); !errors.Is(err, outage) {
		t.Fatalf("error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySubscriptionsSyncRollsBackWhenSessionTouchMisses(t *testing.T) {
	s, m := mockStore(t)
	lease, command := commandTestLease(), subscriptionTestCommand()
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectEmptySubscriptionLockSnapshot(m)
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	m.ExpectQuery("SELECT generation FROM relay_subscription_heads").WithArgs(testController).WillReturnError(pgx.ErrNoRows)
	m.ExpectExec("INSERT INTO relay_subscription_heads").WithArgs(testController, uint64(1), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("INSERT INTO relay_session_commands").WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectRollback()
	if _, err := s.ApplySubscriptionsSync(context.Background(), lease, command, 1, []Subscription{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCommandValidationRejectsZeroDigestAndCrossKindResults(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	zero := SessionCommand{MessageID: testMessage, Type: CommandSubscriptionsSync}
	m.ExpectBegin()
	expectEmptySubscriptionLockSnapshot(m)
	expectEmptySubscriptionLockSnapshot(m)
	m.ExpectRollback()
	if _, err := s.ApplySubscriptionsSync(context.Background(), lease, zero, 1, []Subscription{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero digest error=%v", err)
	}
	if err := validateSessionCommandResult(CommandSubscriptionsSync, SessionCommandResult{Kind: ResultKeyRevoked, ControllerID: testController, KeyID: testKey}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-kind error=%v", err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourceDecisionPersistsCommandIDAndDistinctACKCannotRedecideTarget(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	targetMessageID := testDelivery
	first := SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("first source ack frame"))}

	m.ExpectBegin()
	expectTopologyShards(m, subscriptionTopologyShard(testSubscription))
	expectActiveCommandLease(m, lease, first)
	expectMissingCommand(m, lease, first)
	m.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(
		pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController, int64(3), nil, nil),
	)
	// decision_message_id is the inbound ACK command ID, never targetMessageID.
	m.ExpectExec("UPDATE relay_desired_states").WithArgs(testSubscription, uint64(3), "acked", nil, first.MessageID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectDecisionCommandInsert(m, lease, first, ResultDecisionApplied, "")
	m.ExpectCommit()
	result, err := s.ApplySourceDecision(context.Background(), lease, first, testSubscription, 3, targetMessageID, true, "")
	if err != nil || result.Kind != ResultDecisionApplied {
		t.Fatalf("first result=%#v error=%v", result, err)
	}

	second := SessionCommand{MessageID: testEvent2, Type: CommandAckSource, Digest: sha256.Sum256([]byte("second source ack frame with same target"))}
	m.ExpectBegin()
	expectTopologyShards(m, subscriptionTopologyShard(testSubscription))
	expectActiveCommandLease(m, lease, second)
	expectMissingCommand(m, lease, second)
	m.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(
		pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController, int64(3), "acked", nil),
	)
	expectDecisionCommandInsert(m, lease, second, ResultProtocolError, "unknown_target")
	m.ExpectCommit()
	result, err = s.ApplySourceDecision(context.Background(), lease, second, testSubscription, 3, targetMessageID, true, "")
	if err != nil || result.Kind != ResultProtocolError || result.ErrorCode != "unknown_target" {
		t.Fatalf("second result=%#v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourceDecisionReturnsDurableStaleTargetWithoutMutation(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandRejectSource, Digest: sha256.Sum256([]byte("stale source reject frame"))}
	m.ExpectBegin()
	expectTopologyShards(m, subscriptionTopologyShard(testSubscription))
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(
		pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController, int64(4), nil, nil),
	)
	expectDecisionCommandInsert(m, lease, command, ResultProtocolError, "stale_target")
	m.ExpectCommit()
	result, err := s.ApplySourceDecision(context.Background(), lease, command, testSubscription, 3, testDelivery, false, "deployment.paused")
	if err != nil || result.Kind != ResultProtocolError || result.ErrorCode != "stale_target" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourceDecisionRetiredSubscriptionMakesLateACKAndRejectDurablyStale(t *testing.T) {
	for index, accepted := range []bool{true, false} {
		name := "reject"
		commandType := CommandRejectSource
		code := "deployment.stopped"
		if accepted {
			name = "ack"
			commandType = CommandAckSource
			code = ""
		}
		t.Run(name, func(t *testing.T) {
			s, m := mockStore(t)
			lease := commandTestLease()
			command := SessionCommand{MessageID: fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012x", index+10), Type: commandType, Digest: sha256.Sum256([]byte(name + " retired subscription"))}
			m.ExpectBegin()
			expectTopologyShards(m, subscriptionTopologyShard(testSubscription))
			expectActiveCommandLease(m, lease, command)
			expectMissingCommand(m, lease, command)
			m.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(
				pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController, int64(3), nil, int64(4)),
			)
			expectDecisionCommandInsert(m, lease, command, ResultProtocolError, "stale_target")
			m.ExpectCommit()
			result, err := s.ApplySourceDecision(context.Background(), lease, command, testSubscription, 3, testDelivery, accepted, code)
			if err != nil || result.Kind != ResultProtocolError || result.ErrorCode != "stale_target" {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if err = m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyAccessDecisionCommitsAndExactReplaySkipsEventMutation(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandRejectAccess, Digest: sha256.Sum256([]byte("access reject frame"))}

	m.ExpectBegin()
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT controller_id::text,decision FROM relay_access_events").WithArgs(testEvent).WillReturnRows(
		pgxmock.NewRows([]string{"controller_id", "decision"}).AddRow(testController, nil),
	)
	m.ExpectExec("UPDATE relay_access_events").WithArgs(testEvent, lease.ControllerID, "rejected", "access.removed", command.MessageID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectDecisionCommandInsert(m, lease, command, ResultDecisionApplied, "")
	m.ExpectCommit()
	result, err := s.ApplyAccessDecision(context.Background(), lease, command, testEvent, testDelivery, false, "access.removed")
	if err != nil || result.Kind != ResultDecisionApplied {
		t.Fatalf("result=%#v error=%v", result, err)
	}

	m.ExpectBegin()
	expectActiveCommandLease(m, lease, command)
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultDecisionApplied), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
	)
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	result, err = s.ApplyAccessDecision(context.Background(), lease, command, testEvent, testDelivery, false, "access.removed")
	if err != nil || result.Kind != ResultDecisionApplied {
		t.Fatalf("replay=%#v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyBindingRemovalPreservesBindingRouteLeaseLockOrder(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandBindingRemove, Digest: sha256.Sum256([]byte("binding remove frame"))}
	m.ExpectBegin()
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(11), int64(22)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}).AddRow(int64(11), int64(22), "refs/heads/main"))
	expectTopologyShards(m, bindingTopologyShard(11), routeTopologyShard(11, 22, "refs/heads/main"))
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(11), int64(22)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}).AddRow(int64(11), int64(22), "refs/heads/main"))
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectExec("UPDATE relay_bindings SET revoked_at").WithArgs(testController, int64(11), int64(22), fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result := SessionCommandResult{Kind: ResultBindingRemoved, InstallationID: 11, RepositoryID: 22}
	expectTypedCommandInsert(m, lease, command, result)
	m.ExpectCommit()
	got, err := s.ApplyBindingRemoval(context.Background(), lease, command, 11, 22)
	if err != nil || got.Kind != result.Kind || got.InstallationID != 11 || got.RepositoryID != 22 {
		t.Fatalf("result=%#v error=%v", got, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyKeyRevocationRequiresAndAtomicallyRevokesAuthenticatingKey(t *testing.T) {
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandKeyRevoke, Digest: sha256.Sum256([]byte("key revoke frame"))}

	t.Run("rejects another active key", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT state,rotation_old_key_id::text FROM relay_controller_keys").WithArgs(testController, testEvent).WillReturnRows(pgxmock.NewRows([]string{"state", "rotation_old_key_id"}).AddRow("active", nil))
		m.ExpectRollback()
		if _, err := s.ApplyKeyRevocation(context.Background(), lease, command, testController, testEvent); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("revokes pending rotation key without invalidating current session", func(t *testing.T) {
		s, m := mockStore(t)
		pendingKey := testEvent
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT state,rotation_old_key_id::text FROM relay_controller_keys").WithArgs(testController, pendingKey).WillReturnRows(pgxmock.NewRows([]string{"state", "rotation_old_key_id"}).AddRow("pending", testKey))
		m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'.*state='pending'").WithArgs(testController, pendingKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		result := SessionCommandResult{Kind: ResultKeyRevoked, ControllerID: testController, KeyID: pendingKey}
		expectTypedCommandInsert(m, lease, command, result)
		m.ExpectCommit()
		got, err := s.ApplyKeyRevocation(context.Background(), lease, command, testController, pendingKey)
		if err != nil || got.Kind != result.Kind || got.KeyID != pendingKey {
			t.Fatalf("result=%#v error=%v", got, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("commits terminal result with revocation", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectExec(`UPDATE relay_controller_keys SET state='revoked'.*key_id=\$2 AND state='active'`).WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec(`UPDATE relay_controller_keys SET state='revoked'.*rotation_old_key_id=\$2`).WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		result := SessionCommandResult{Kind: ResultKeyRevoked, ControllerID: testController, KeyID: testKey}
		expectTypedCommandInsert(m, lease, command, result)
		m.ExpectCommit()
		got, err := s.ApplyKeyRevocation(context.Background(), lease, command, testController, testKey)
		if err != nil || got.Kind != result.Kind || got.KeyID != testKey {
			t.Fatalf("result=%#v error=%v", got, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplyControllerRevocationCommitsTerminalResultAfterCoverageRecheck(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandControllerRevoke, Digest: sha256.Sum256([]byte("controller revoke frame"))}
	m.ExpectBegin()
	m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}))
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}))
	m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectExec("UPDATE relay_controllers SET state='revoked'").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("UPDATE relay_bindings SET revoked_at").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result := SessionCommandResult{Kind: ResultControllerRevoked, ControllerID: testController}
	expectTypedCommandInsert(m, lease, command, result)
	m.ExpectCommit()
	got, err := s.ApplyControllerRevocation(context.Background(), lease, command, testController)
	if err != nil || got.Kind != result.Kind || got.ControllerID != testController {
		t.Fatalf("result=%#v error=%v", got, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRotationProposalReplaysBeforeEntropyAndFreshProposalPersistsNonce(t *testing.T) {
	lease := commandTestLease()
	rotationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	newKeyID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	publicKey := bytes.Repeat([]byte{9}, ed25519.PublicKeySize)
	command := SessionCommand{MessageID: testMessage, Type: CommandRotationPropose, Digest: sha256.Sum256([]byte("rotation proposal frame"))}
	input := RotationInput{RotationID: rotationID, ControllerID: testController, OldKeyID: testKey, NewKeyID: newKeyID, SessionID: testSession, NewPublicKey: publicKey}

	t.Run("replay survives entropy outage", func(t *testing.T) {
		s, m := mockStore(t)
		s.randomBytes = func([]byte) error { return errors.New("entropy unavailable") }
		nonce := bytes.Repeat([]byte{4}, protocol.NonceBytes)
		expires := fixedNow.Add(time.Minute)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
			pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultRotationChallenge), nil, nil, nil, nil, nil, nil, nil, rotationID, nil, nonce, expires),
		)
		m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectCommit()
		got, err := s.ApplyRotationProposal(context.Background(), lease, command, input, time.Minute)
		if err != nil || got.Kind != ResultRotationChallenge || !bytes.Equal(got.Nonce, nonce) {
			t.Fatalf("result=%#v error=%v", got, err)
		}
		got.Destroy()
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("fresh proposal persists generated nonce", func(t *testing.T) {
		s, m := mockStore(t)
		nonce := make([]byte, protocol.NonceBytes)
		for i := range nonce {
			nonce[i] = byte(i)
		}
		expires := fixedNow.Add(time.Minute)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectExec("INSERT INTO relay_controller_keys").WithArgs(newKeyID, testController, publicKey, rotationID, testKey, testSession, nonce, expires, fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		result := SessionCommandResult{Kind: ResultRotationChallenge, RotationID: rotationID, Nonce: nonce, ExpiresAt: expires}
		expectTypedCommandInsert(m, lease, command, result)
		m.ExpectCommit()
		got, err := s.ApplyRotationProposal(context.Background(), lease, command, input, time.Minute)
		if err != nil || got.Kind != ResultRotationChallenge || !bytes.Equal(got.Nonce, nonce) {
			t.Fatalf("result=%#v error=%v", got, err)
		}
		got.Destroy()
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplyRotationConfirmationVerifiesPossessionInsideTransaction(t *testing.T) {
	lease := commandTestLease()
	rotationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	newKeyID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	newPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	newPublic := newPrivate.Public().(ed25519.PublicKey)
	nonce := bytes.Repeat([]byte{3}, protocol.NonceBytes)
	expires := fixedNow.Add(time.Minute)
	command := SessionCommand{MessageID: testMessage, Type: CommandRotationConfirm, Digest: sha256.Sum256([]byte("rotation confirmation frame"))}
	proof := protocol.RotationProof{RotationID: rotationID, ControllerID: testController, OldKeyID: testKey, NewKeyID: newKeyID, NewPublicKey: base64.RawURLEncoding.EncodeToString(newPublic), SessionID: testSession, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: expires}
	transcript, err := protocol.KeyRotationTranscript(proof)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := protocol.Sign(newPrivate, transcript)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("valid signature commits confirmation and result", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(
			pgxmock.NewRows([]string{"old_key", "new_key", "session", "public_key", "nonce", "expires", "confirmed"}).AddRow(testKey, newKeyID, testSession, []byte(newPublic), nonce, expires, nil),
		)
		m.ExpectExec("UPDATE relay_controller_keys SET possession_confirmed_at").WithArgs(testController, rotationID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		result := SessionCommandResult{Kind: ResultRotationConfirmed, RotationID: rotationID}
		expectTypedCommandInsert(m, lease, command, result)
		m.ExpectCommit()
		got, err := s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, signature)
		if err != nil || got.Kind != result.Kind || got.RotationID != rotationID {
			t.Fatalf("result=%#v error=%v", got, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("bad signature rolls back without mutation or ledger", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(
			pgxmock.NewRows([]string{"old_key", "new_key", "session", "public_key", "nonce", "expires", "confirmed"}).AddRow(testKey, newKeyID, testSession, []byte(newPublic), nonce, expires, nil),
		)
		m.ExpectRollback()
		badSignature := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0}, ed25519.SignatureSize))
		if _, err := s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, badSignature); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("corrupt key material fails closed before crypto", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(
			pgxmock.NewRows([]string{"old_key", "new_key", "session", "public_key", "nonce", "expires", "confirmed"}).AddRow(testKey, newKeyID, testSession, []byte{1}, []byte{2}, expires, nil),
		)
		m.ExpectRollback()
		if _, err := s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, signature); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplyRotationFinalizationCommitsTerminalTransitionAndReplaysUnderNewKeySession(t *testing.T) {
	s, m := mockStore(t)
	lease := commandTestLease()
	rotationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	newKeyID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	command := SessionCommand{MessageID: testMessage, Type: CommandRotationFinalize, Digest: sha256.Sum256([]byte("rotation finalization frame"))}
	m.ExpectBegin()
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT p.rotation_old_key_id::text,p.key_id::text").WithArgs(testController, rotationID).WillReturnRows(
		pgxmock.NewRows([]string{"old_key", "new_key", "session", "expires", "confirmed", "old_state"}).AddRow(testKey, newKeyID, testSession, fixedNow.Add(time.Minute), true, "active"),
	)
	m.ExpectExec("UPDATE relay_controller_keys SET state='revoked',rotation_id").WithArgs(testController, testKey, rotationID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("UPDATE relay_controller_keys SET state='active',rotation_id=NULL").WithArgs(testController, newKeyID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	result := SessionCommandResult{Kind: ResultRotationFinalized, RotationID: rotationID, RetiredKeyID: testKey}
	expectTypedCommandInsert(m, lease, command, result)
	m.ExpectCommit()
	got, err := s.ApplyRotationFinalization(context.Background(), lease, command, rotationID)
	if err != nil || got.Kind != result.Kind || got.RetiredKeyID != testKey {
		t.Fatalf("result=%#v error=%v", got, err)
	}

	newLease := Lease{ControllerID: testController, SessionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", LeaseID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Fence: 8, ExpiresAt: fixedNow.Add(time.Minute)}
	m.ExpectBegin()
	expectActiveCommandLeaseWithKey(m, newLease, command, newKeyID)
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(newLease.ControllerID, command.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultRotationFinalized), nil, nil, nil, nil, nil, nil, nil, rotationID, testKey, nil, nil),
	)
	m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(newLease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	m.ExpectCommit()
	got, err = s.ApplyRotationFinalization(context.Background(), newLease, command, rotationID)
	if err != nil || got.Kind != result.Kind || got.RetiredKeyID != testKey {
		t.Fatalf("replay=%#v error=%v", got, err)
	}

	differentDigest := command
	differentDigest.Digest = sha256.Sum256([]byte("different finalization frame"))
	m.ExpectBegin()
	expectActiveCommandLeaseWithKey(m, newLease, differentDigest, newKeyID)
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(newLease.ControllerID, differentDigest.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultRotationFinalized), nil, nil, nil, nil, nil, nil, nil, rotationID, testKey, nil, nil),
	)
	m.ExpectRollback()
	if _, err = s.ApplyRotationFinalization(context.Background(), newLease, differentDigest, rotationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("different digest error=%v", err)
	}

	differentType := command
	differentType.Type = CommandKeyRevoke
	m.ExpectBegin()
	tx, beginErr := m.Begin(context.Background())
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	expectActiveCommandLeaseWithKey(m, newLease, differentType, newKeyID)
	m.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(newLease.ControllerID, differentType.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultRotationFinalized), nil, nil, nil, nil, nil, nil, nil, rotationID, testKey, nil, nil),
	)
	m.ExpectRollback()
	if _, err = s.prepareSessionCommand(context.Background(), tx, newLease, differentType); !errors.Is(err, ErrConflict) {
		t.Fatalf("different type error=%v", err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	m.ExpectBegin()
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, command.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(controllerSessionLockKey(lease.ControllerID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT expires_at FROM relay_controller_leases").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(pgx.ErrNoRows)
	m.ExpectRollback()
	if _, err = s.ApplyRotationFinalization(context.Background(), lease, command, rotationID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale identity error=%v", err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestSessionCommandCommitFailuresRollBackDomainMutation exercises the two
// commit boundaries shared by every WSS command. The individual setup
// functions deliberately stop immediately after the irreversible domain
// transition, so an expected rollback proves neither a partial target change
// nor a durable command result can escape either failure path.
func TestSessionCommandCommitFailuresRollBackDomainMutation(t *testing.T) {
	lease := commandTestLease()
	rotationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	newKeyID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	publicKey := rotationTestPublicKey()
	confirmSignature := rotationConfirmationSignature(t, rotationID, newKeyID, publicKey)

	type commandCase struct {
		name    string
		command SessionCommand
		setup   func(pgxmock.PgxPoolIface, SessionCommand)
		apply   func(*Store, SessionCommand) (SessionCommandResult, error)
	}
	cases := []commandCase{
		{
			name:    "source decision",
			command: SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("source decision commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				expectTopologyShards(m, subscriptionTopologyShard(testSubscription))
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController, int64(3), nil, nil))
				m.ExpectExec("UPDATE relay_desired_states").WithArgs(testSubscription, uint64(3), "acked", nil, command.MessageID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplySourceDecision(context.Background(), lease, command, testSubscription, 3, testDelivery, true, "")
			},
		},
		{
			name:    "access decision",
			command: SessionCommand{MessageID: testMessage, Type: CommandRejectAccess, Digest: sha256.Sum256([]byte("access decision commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectQuery("SELECT controller_id::text,decision FROM relay_access_events").WithArgs(testEvent).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "decision"}).AddRow(testController, nil))
				m.ExpectExec("UPDATE relay_access_events").WithArgs(testEvent, testController, "rejected", "access.removed", command.MessageID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyAccessDecision(context.Background(), lease, command, testEvent, testDelivery, false, "access.removed")
			},
		},
		{
			name:    "binding removal",
			command: SessionCommand{MessageID: testMessage, Type: CommandBindingRemove, Digest: sha256.Sum256([]byte("binding removal commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(11), int64(22)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
				expectTopologyShards(m, bindingTopologyShard(11))
				m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController, int64(11), int64(22)).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectExec("UPDATE relay_bindings SET revoked_at").WithArgs(testController, int64(11), int64(22), fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyBindingRemoval(context.Background(), lease, command, 11, 22)
			},
		},
		{
			name:    "key revocation",
			command: SessionCommand{MessageID: testMessage, Type: CommandKeyRevoke, Digest: sha256.Sum256([]byte("key revocation commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectExec(`UPDATE relay_controller_keys SET state='revoked'.*key_id=\$2 AND state='active'`).WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec(`UPDATE relay_controller_keys SET state='revoked'.*rotation_old_key_id=\$2`).WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyKeyRevocation(context.Background(), lease, command, testController, testKey)
			},
		},
		{
			name:    "controller revocation",
			command: SessionCommand{MessageID: testMessage, Type: CommandControllerRevoke, Digest: sha256.Sum256([]byte("controller revocation commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}))
				m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
				m.ExpectQuery("SELECT DISTINCT installation_id FROM relay_bindings").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id"}))
				m.ExpectQuery("SELECT installation_id,repository_id,tracked_ref FROM relay_subscriptions").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"installation_id", "repository_id", "tracked_ref"}))
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectExec("UPDATE relay_controllers SET state='revoked'").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec("UPDATE relay_controller_keys SET state='revoked'").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec("UPDATE relay_bindings SET revoked_at").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyControllerRevocation(context.Background(), lease, command, testController)
			},
		},
		{
			name:    "rotation proposal",
			command: SessionCommand{MessageID: testMessage, Type: CommandRotationPropose, Digest: sha256.Sum256([]byte("rotation proposal commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectExec("INSERT INTO relay_controller_keys").WithArgs(newKeyID, testController, []byte(publicKey), rotationID, testKey, testSession, pgxmock.AnyArg(), fixedNow.Add(time.Minute), fixedNow).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyRotationProposal(context.Background(), lease, command, RotationInput{RotationID: rotationID, ControllerID: testController, OldKeyID: testKey, NewKeyID: newKeyID, SessionID: testSession, NewPublicKey: publicKey}, time.Minute)
			},
		},
		{
			name:    "rotation confirmation",
			command: SessionCommand{MessageID: testMessage, Type: CommandRotationConfirm, Digest: sha256.Sum256([]byte("rotation confirmation commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationCommandRows(testKey, newKeyID, publicKey, nil, fixedNow.Add(time.Minute)))
				m.ExpectExec("UPDATE relay_controller_keys SET possession_confirmed_at").WithArgs(testController, rotationID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, confirmSignature)
			},
		},
		{
			name:    "rotation finalization",
			command: SessionCommand{MessageID: testMessage, Type: CommandRotationFinalize, Digest: sha256.Sum256([]byte("rotation finalization commit boundary"))},
			setup: func(m pgxmock.PgxPoolIface, command SessionCommand) {
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectQuery("SELECT p.rotation_old_key_id::text,p.key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationFinalizationRows(testKey, newKeyID, fixedNow.Add(time.Minute), true, "active"))
				m.ExpectExec("UPDATE relay_controller_keys SET state='revoked',rotation_id").WithArgs(testController, testKey, rotationID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec("UPDATE relay_controller_keys SET state='active',rotation_id=NULL").WithArgs(testController, newKeyID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				m.ExpectExec("UPDATE relay_sessions SET revoked_at").WithArgs(testController, testKey, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyRotationFinalization(context.Background(), lease, command, rotationID)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" ledger failure", func(t *testing.T) {
			s, m := mockStore(t)
			outage := errors.New("command ledger unavailable")
			m.ExpectBegin()
			tc.setup(m, tc.command)
			expectAnyCommandInsert(m).WillReturnError(outage)
			m.ExpectRollback()
			if _, err := tc.apply(s, tc.command); !errors.Is(err, outage) {
				t.Fatalf("error=%v", err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})

		t.Run(tc.name+" session touch failure", func(t *testing.T) {
			s, m := mockStore(t)
			outage := errors.New("session touch unavailable")
			m.ExpectBegin()
			tc.setup(m, tc.command)
			expectAnyCommandInsert(m).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			m.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnError(outage)
			m.ExpectRollback()
			if _, err := tc.apply(s, tc.command); !errors.Is(err, outage) {
				t.Fatalf("error=%v", err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecisionTargetsHideForeignTenantAndDoNotMutate(t *testing.T) {
	lease := commandTestLease()
	for _, tc := range []struct {
		name    string
		command SessionCommand
		apply   func(*Store, SessionCommand) (SessionCommandResult, error)
		lookup  func(pgxmock.PgxPoolIface)
	}{
		{
			name:    "source target",
			command: SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("foreign source target"))},
			lookup: func(m pgxmock.PgxPoolIface) {
				m.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController2, int64(3), nil, nil))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplySourceDecision(context.Background(), lease, command, testSubscription, 3, testDelivery, true, "")
			},
		},
		{
			name:    "access target",
			command: SessionCommand{MessageID: testMessage, Type: CommandAckAccess, Digest: sha256.Sum256([]byte("foreign access target"))},
			lookup: func(m pgxmock.PgxPoolIface) {
				m.ExpectQuery("SELECT controller_id::text,decision FROM relay_access_events").WithArgs(testEvent).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "decision"}).AddRow(testController2, nil))
			},
			apply: func(s *Store, command SessionCommand) (SessionCommandResult, error) {
				return s.ApplyAccessDecision(context.Background(), lease, command, testEvent, testDelivery, true, "")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := mockStore(t)
			m.ExpectBegin()
			if tc.command.Type == CommandAckSource {
				expectTopologyShards(m, subscriptionTopologyShard(testSubscription))
			}
			expectActiveCommandLease(m, lease, tc.command)
			expectMissingCommand(m, lease, tc.command)
			tc.lookup(m)
			expectDecisionCommandInsert(m, lease, tc.command, ResultProtocolError, "unknown_target")
			m.ExpectCommit()
			result, err := tc.apply(s, tc.command)
			if err != nil || result.Kind != ResultProtocolError || result.ErrorCode != "unknown_target" {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRotationCommandTransitionGuards(t *testing.T) {
	lease := commandTestLease()
	rotationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	newKeyID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	publicKey := rotationTestPublicKey()
	signature := rotationConfirmationSignature(t, rotationID, newKeyID, publicKey)

	t.Run("confirmation rejects expired and mismatched origin", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			oldKey  string
			expires time.Time
		}{
			{name: "expired", oldKey: testKey, expires: fixedNow},
			{name: "mismatched origin", oldKey: testEvent, expires: fixedNow.Add(time.Minute)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, m := mockStore(t)
				command := SessionCommand{MessageID: testMessage, Type: CommandRotationConfirm, Digest: sha256.Sum256([]byte("confirmation " + tc.name))}
				m.ExpectBegin()
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationCommandRows(tc.oldKey, newKeyID, publicKey, nil, tc.expires))
				m.ExpectRollback()
				if _, err := s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, signature); !errors.Is(err, ErrConflict) {
					t.Fatalf("error=%v", err)
				}
				if err := m.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("confirmation rejects zero row transition", func(t *testing.T) {
		s, m := mockStore(t)
		command := SessionCommand{MessageID: testMessage, Type: CommandRotationConfirm, Digest: sha256.Sum256([]byte("confirmation zero rows"))}
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationCommandRows(testKey, newKeyID, publicKey, nil, fixedNow.Add(time.Minute)))
		m.ExpectExec("UPDATE relay_controller_keys SET possession_confirmed_at").WithArgs(testController, rotationID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		m.ExpectRollback()
		if _, err := s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, signature); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("already confirmed records a command without reconfirming", func(t *testing.T) {
		s, m := mockStore(t)
		command := SessionCommand{MessageID: testMessage, Type: CommandRotationConfirm, Digest: sha256.Sum256([]byte("already confirmed"))}
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT rotation_old_key_id::text,key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationCommandRows(testKey, newKeyID, publicKey, fixedNow, fixedNow.Add(time.Minute)))
		expectTypedCommandInsert(m, lease, command, SessionCommandResult{Kind: ResultRotationConfirmed, RotationID: rotationID})
		m.ExpectCommit()
		result, err := s.ApplyRotationConfirmation(context.Background(), lease, command, rotationID, signature)
		if err != nil || result.Kind != ResultRotationConfirmed {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("finalization rejects expired, non-current, and non-active old key", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			oldKey   string
			expires  time.Time
			oldState string
		}{
			{name: "expired", oldKey: testKey, expires: fixedNow, oldState: "active"},
			{name: "different current key", oldKey: testEvent, expires: fixedNow.Add(time.Minute), oldState: "active"},
			{name: "already revoked old key", oldKey: testKey, expires: fixedNow.Add(time.Minute), oldState: "revoked"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, m := mockStore(t)
				command := SessionCommand{MessageID: testMessage, Type: CommandRotationFinalize, Digest: sha256.Sum256([]byte("finalization " + tc.name))}
				m.ExpectBegin()
				expectActiveCommandLease(m, lease, command)
				expectMissingCommand(m, lease, command)
				m.ExpectQuery("SELECT p.rotation_old_key_id::text,p.key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationFinalizationRows(tc.oldKey, newKeyID, tc.expires, true, tc.oldState))
				m.ExpectRollback()
				if _, err := s.ApplyRotationFinalization(context.Background(), lease, command, rotationID); !errors.Is(err, ErrConflict) {
					t.Fatalf("error=%v", err)
				}
				if err := m.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("finalization rejects zero row activation", func(t *testing.T) {
		s, m := mockStore(t)
		command := SessionCommand{MessageID: testMessage, Type: CommandRotationFinalize, Digest: sha256.Sum256([]byte("finalization zero rows"))}
		m.ExpectBegin()
		expectActiveCommandLease(m, lease, command)
		expectMissingCommand(m, lease, command)
		m.ExpectQuery("SELECT p.rotation_old_key_id::text,p.key_id::text").WithArgs(testController, rotationID).WillReturnRows(rotationFinalizationRows(testKey, newKeyID, fixedNow.Add(time.Minute), true, "active"))
		m.ExpectExec("UPDATE relay_controller_keys SET state='revoked',rotation_id").WithArgs(testController, testKey, rotationID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		m.ExpectExec("UPDATE relay_controller_keys SET state='active',rotation_id=NULL").WithArgs(testController, newKeyID).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		m.ExpectRollback()
		if _, err := s.ApplyRotationFinalization(context.Background(), lease, command, rotationID); !errors.Is(err, ErrConflict) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestApplyDecisionProtocolErrorReplaysAppliedDecisionAfterNewStoreInstance(t *testing.T) {
	lease := commandTestLease()
	command := SessionCommand{MessageID: testMessage, Type: CommandAckSource, Digest: sha256.Sum256([]byte("process restart replay"))}

	firstStore, firstMock := mockStore(t)
	firstMock.ExpectBegin()
	expectTopologyShards(firstMock, subscriptionTopologyShard(testSubscription))
	expectActiveCommandLease(firstMock, lease, command)
	expectMissingCommand(firstMock, lease, command)
	firstMock.ExpectQuery("SELECT d.controller_id::text,d.generation,d.decision,s.retired_generation").WithArgs(testSubscription).WillReturnRows(pgxmock.NewRows([]string{"controller_id", "generation", "decision", "retired_generation"}).AddRow(testController, int64(3), nil, nil))
	firstMock.ExpectExec("UPDATE relay_desired_states").WithArgs(testSubscription, uint64(3), "acked", nil, command.MessageID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectDecisionCommandInsert(firstMock, lease, command, ResultDecisionApplied, "")
	firstMock.ExpectCommit()
	if _, err := firstStore.ApplySourceDecision(context.Background(), lease, command, testSubscription, 3, testDelivery, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := firstMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// A separate Store and pool model a fresh process: only the durable ledger
	// row is available, and no desired-state lookup or update is permitted.
	restartedStore, restartedMock := mockStore(t)
	restartedMock.ExpectBegin()
	expectActiveCommandLease(restartedMock, lease, command)
	restartedMock.ExpectQuery("SELECT command_type,command_digest,result_kind").WithArgs(lease.ControllerID, command.MessageID).WillReturnRows(
		pgxmock.NewRows([]string{"command_type", "command_digest", "result_kind", "error_code", "generation", "count", "installation_id", "repository_id", "controller_id", "key_id", "rotation_id", "retired_key_id", "nonce", "expires_at"}).AddRow(string(command.Type), command.Digest[:], string(ResultDecisionApplied), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
	)
	restartedMock.ExpectExec("UPDATE relay_sessions SET last_seen_at").WithArgs(lease.SessionID, fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	restartedMock.ExpectCommit()
	result, err := restartedStore.ApplyDecisionProtocolError(context.Background(), lease, command, "unknown_target")
	if err != nil || result.Kind != ResultDecisionApplied {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if err := restartedMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectAnyCommandInsert(m pgxmock.PgxPoolIface) *pgxmock.ExpectedExec {
	return m.ExpectExec("INSERT INTO relay_session_commands").WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	)
}

func rotationCommandRows(oldKeyID, newKeyID string, publicKey []byte, confirmed any, expires time.Time) *pgxmock.Rows {
	nonce := make([]byte, protocol.NonceBytes)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	// pgxmock returns the supplied backing arrays to Scan. The store clears
	// sensitive scanned key material, so clone the fixture to keep later
	// subtests signing with the intended deterministic public key.
	return pgxmock.NewRows([]string{"old_key", "new_key", "session", "public_key", "nonce", "expires", "confirmed"}).AddRow(oldKeyID, newKeyID, testSession, append([]byte(nil), publicKey...), nonce, expires, confirmed)
}

func rotationFinalizationRows(oldKeyID, newKeyID string, expires time.Time, confirmed bool, oldState string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"old_key", "new_key", "session", "expires", "confirmed", "old_state"}).AddRow(oldKeyID, newKeyID, testSession, expires, confirmed, oldState)
}

func rotationConfirmationSignature(t *testing.T, rotationID, newKeyID string, publicKey ed25519.PublicKey) string {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	if !bytes.Equal(privateKey.Public().(ed25519.PublicKey), publicKey) {
		t.Fatal("public key does not match test signing key")
	}
	nonce := make([]byte, protocol.NonceBytes)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	transcript, err := protocol.KeyRotationTranscript(protocol.RotationProof{RotationID: rotationID, ControllerID: testController, OldKeyID: testKey, NewKeyID: newKeyID, NewPublicKey: base64.RawURLEncoding.EncodeToString(publicKey), SessionID: testSession, ServerNonce: base64.RawURLEncoding.EncodeToString(nonce), ExpiresAt: fixedNow.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := protocol.Sign(privateKey, transcript)
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

func rotationTestPublicKey() ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
}
