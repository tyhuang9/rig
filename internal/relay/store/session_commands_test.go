package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"testing"
	"time"

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
	m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, command.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectQuery("SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state,s.key_id::text").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnRows(
		pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked", "controller_state", "key_state", "key_id"}).AddRow(fixedNow.Add(time.Minute), fixedNow.Add(time.Hour), nil, "active", "active", testKey),
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

func TestRenewLeaseUsesExactFenceAndRejectsExpiredOrSupersededLease(t *testing.T) {
	lease := commandTestLease()

	t.Run("renews same fence with compare and swap", func(t *testing.T) {
		s, m := mockStore(t)
		oldExpiry := fixedNow.Add(30 * time.Second)
		newExpiry := fixedNow.Add(2 * time.Minute)
		m.ExpectBegin()
		m.ExpectQuery("SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnRows(
			pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked", "controller_state", "key_state"}).AddRow(oldExpiry, fixedNow.Add(time.Hour), nil, "active", "active"),
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
		m.ExpectQuery("SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnRows(
			pgxmock.NewRows([]string{"lease_expires", "session_expires", "revoked", "controller_state", "key_state"}).AddRow(fixedNow, fixedNow.Add(time.Hour), nil, "active", "active"),
		)
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
		m.ExpectQuery("SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(pgx.ErrNoRows)
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
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	expectEmptySubscriptionLockSnapshot(m)
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

func TestApplySubscriptionsSyncRejectsDigestMismatchAndStaleFenceBeforeMutation(t *testing.T) {
	lease, command := commandTestLease(), subscriptionTestCommand()

	t.Run("digest mismatch", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
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
		m.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).WithArgs(sessionCommandLockKey(lease.ControllerID, command.MessageID)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
		m.ExpectQuery("SELECT l.expires_at,s.expires_at,s.revoked_at,c.state,k.state,s.key_id::text").WithArgs(lease.ControllerID, lease.SessionID, lease.LeaseID, lease.Fence).WillReturnError(pgx.ErrNoRows)
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
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	expectEmptySubscriptionLockSnapshot(m)
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
	expectActiveCommandLease(m, lease, command)
	expectMissingCommand(m, lease, command)
	m.ExpectQuery("SELECT state FROM relay_controllers").WithArgs(testController).WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow("active"))
	expectEmptySubscriptionLockSnapshot(m)
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
