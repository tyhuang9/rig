package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestPruneDurableStateUsesBoundedForeignKeySafeOrder(t *testing.T) {
	s, m := mockStore(t)
	policy := DefaultDurableRetentionPolicy()
	setBefore := fixedNow.Add(-policy.SubscriptionSetRetention)
	historyBefore := fixedNow.Add(-policy.DeliveryHistoryRetention)
	sessionBefore := fixedNow.Add(-policy.ExpiredSessionRetention)
	commandBefore := fixedNow.Add(-policy.CommandReplayRetention)
	expectOperation := func(pattern string, count int64, args ...any) {
		m.ExpectBegin()
		m.ExpectExec(pattern).WithArgs(args...).WillReturnResult(pgxmock.NewResult("DELETE", count))
		m.ExpectCommit()
	}
	expectOperation("WITH candidates AS \\(SELECT t.delivery_id,t.subscription_id", 1, historyBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT d.subscription_id", 2, historyBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT i.controller_id", 3, setBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT s.subscription_id", 4, historyBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT a.event_id", 5, historyBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT i.delivery_id", 6, historyBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT a.delivery_number", 7, historyBefore, policy.BatchSize)
	deliveryIDs := make([]string, 8)
	recoveryRows := pgxmock.NewRows([]string{"delivery_id", "delivery_number"})
	for index := range deliveryIDs {
		deliveryIDs[index] = maintenanceUUID(index + 1)
		recoveryRows.AddRow(deliveryIDs[index], nil)
	}
	m.ExpectBegin()
	m.ExpectQuery("SELECT r.delivery_id::text,r.delivery_number").WithArgs(historyBefore, policy.BatchSize).WillReturnRows(recoveryRows)
	m.ExpectExec("DELETE FROM relay_recovery_delivery_attempts a USING relay_recovery_deliveries r").WithArgs(deliveryIDs).WillReturnResult(pgxmock.NewResult("DELETE", 0))
	m.ExpectExec("DELETE FROM relay_recovery_deliveries r WHERE").WithArgs(deliveryIDs).WillReturnResult(pgxmock.NewResult("DELETE", 8))
	m.ExpectCommit()
	expectOperation("WITH candidates AS \\(SELECT g.delivery_id", 9, historyBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT l.controller_id", 10, sessionBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT c.controller_id,c.message_id", 11, commandBefore, sessionBefore, fixedNow, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT m.session_id,m.message_id", 12, sessionBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT k.key_id", 13, sessionBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT s.session_id", 14, sessionBefore, policy.BatchSize)
	expectOperation("WITH candidates AS \\(SELECT c.session_id", 15, sessionBefore, policy.BatchSize)

	result, err := s.PruneDurableState(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceDeliveryTargets != 1 || result.RetiredDesiredStates != 2 || result.SubscriptionSetItems != 3 || result.RetiredSubscriptions != 4 || result.TerminalAccessEvents != 5 || result.IgnoredDeliveries != 6 || result.RecoveryAttempts != 7 || result.RecoveryDeliveries != 8 || result.OrphanDeliveries != 9 || result.ExpiredLeases != 10 || result.SessionCommands != 11 || result.SessionMessages != 12 || result.RotationReferences != 13 || result.ExpiredSessions != 14 || result.ExpiredChallenges != 15 || result.Total() != 120 {
		t.Fatalf("result=%+v total=%d", result, result.Total())
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPruneDurableStateReturnsCommittedPartialProgressOnFailure(t *testing.T) {
	s, m := mockStore(t)
	policy := DefaultDurableRetentionPolicy()
	outage := errors.New("maintenance unavailable")
	m.ExpectBegin()
	m.ExpectExec("WITH candidates AS \\(SELECT t.delivery_id,t.subscription_id").WithArgs(fixedNow.Add(-policy.DeliveryHistoryRetention), policy.BatchSize).WillReturnResult(pgxmock.NewResult("DELETE", 4))
	m.ExpectCommit()
	m.ExpectBegin()
	m.ExpectExec("WITH candidates AS \\(SELECT d.subscription_id").WithArgs(fixedNow.Add(-policy.DeliveryHistoryRetention), policy.BatchSize).WillReturnError(outage)
	m.ExpectRollback()
	result, err := s.PruneDurableState(context.Background(), policy)
	if !errors.Is(err, outage) || result.SourceDeliveryTargets != 4 || result.Total() != 4 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func maintenanceUUID(value int) string {
	return fmt.Sprintf("bbbbbbbb-bbbb-4bbb-8bbb-%012x", value)
}

func TestPruneRecoveryDeliveryGroupsDeletesSelectedAttemptThenParentAtomically(t *testing.T) {
	selectQuery := `SELECT r.delivery_id::text,r.delivery_number FROM relay_recovery_deliveries r WHERE r.recovered_at<$1 LIMIT $2 FOR UPDATE OF r SKIP LOCKED`
	ids := []string{maintenanceUUID(41), maintenanceUUID(42)}

	t.Run("commits child then parent", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT r.delivery_id::text,r.delivery_number").WithArgs(fixedNow, 2).WillReturnRows(
			pgxmock.NewRows([]string{"delivery_id", "delivery_number"}).AddRow(ids[0], int64(1001)).AddRow(ids[1], nil),
		)
		m.ExpectExec("DELETE FROM relay_recovery_delivery_attempts a USING relay_recovery_deliveries r").WithArgs(ids).WillReturnResult(pgxmock.NewResult("DELETE", 1))
		m.ExpectExec("DELETE FROM relay_recovery_deliveries r WHERE").WithArgs(ids).WillReturnResult(pgxmock.NewResult("DELETE", 2))
		m.ExpectCommit()
		count, err := s.pruneRecoveryDeliveryGroups(context.Background(), selectQuery, fixedNow, 2)
		if err != nil || count != 2 {
			t.Fatalf("count=%d error=%v", count, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("parent failure rolls back selected attempt", func(t *testing.T) {
		s, m := mockStore(t)
		outage := errors.New("parent delete unavailable")
		m.ExpectBegin()
		m.ExpectQuery("SELECT r.delivery_id::text,r.delivery_number").WithArgs(fixedNow, 1).WillReturnRows(
			pgxmock.NewRows([]string{"delivery_id", "delivery_number"}).AddRow(ids[0], int64(1001)),
		)
		m.ExpectExec("DELETE FROM relay_recovery_delivery_attempts a USING relay_recovery_deliveries r").WithArgs(ids[:1]).WillReturnResult(pgxmock.NewResult("DELETE", 1))
		m.ExpectExec("DELETE FROM relay_recovery_deliveries r WHERE").WithArgs(ids[:1]).WillReturnError(outage)
		m.ExpectRollback()
		count, err := s.pruneRecoveryDeliveryGroups(context.Background(), selectQuery, fixedNow, 1)
		if !errors.Is(err, outage) || count != 0 {
			t.Fatalf("count=%d error=%v", count, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("selected attempt count mismatch rolls back before parent", func(t *testing.T) {
		s, m := mockStore(t)
		m.ExpectBegin()
		m.ExpectQuery("SELECT r.delivery_id::text,r.delivery_number").WithArgs(fixedNow, 1).WillReturnRows(
			pgxmock.NewRows([]string{"delivery_id", "delivery_number"}).AddRow(ids[0], int64(1001)),
		)
		m.ExpectExec("DELETE FROM relay_recovery_delivery_attempts a USING relay_recovery_deliveries r").WithArgs(ids[:1]).WillReturnResult(pgxmock.NewResult("DELETE", 0))
		m.ExpectRollback()
		count, err := s.pruneRecoveryDeliveryGroups(context.Background(), selectQuery, fixedNow, 1)
		if !errors.Is(err, ErrConflict) || count != 0 {
			t.Fatalf("count=%d error=%v", count, err)
		}
		if err = m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPruneDurableStateRejectsUnsafeRetentionOrBatch(t *testing.T) {
	valid := DefaultDurableRetentionPolicy()
	for _, mutate := range []func(*DurableRetentionPolicy){
		func(policy *DurableRetentionPolicy) {
			policy.CommandReplayRetention = MinimumCommandReplayRetention - time.Nanosecond
		},
		func(policy *DurableRetentionPolicy) {
			policy.ExpiredSessionRetention = MinimumExpiredSessionRetention - time.Nanosecond
		},
		func(policy *DurableRetentionPolicy) {
			policy.SubscriptionSetRetention = MinimumSubscriptionSetRetention - time.Nanosecond
		},
		func(policy *DurableRetentionPolicy) {
			policy.DeliveryHistoryRetention = MinimumDeliveryHistoryRetention - time.Nanosecond
		},
		func(policy *DurableRetentionPolicy) { policy.BatchSize = 0 },
		func(policy *DurableRetentionPolicy) { policy.BatchSize = MaximumMaintenanceBatch + 1 },
	} {
		policy := valid
		mutate(&policy)
		s, _ := mockStore(t)
		if _, err := s.PruneDurableState(context.Background(), policy); !errors.Is(err, ErrInvalid) {
			t.Fatalf("policy=%+v error=%v", policy, err)
		}
	}
}

func TestPruneDurableStateSourceRetainsLiveReplayAndCurrentRows(t *testing.T) {
	body, err := os.ReadFile("maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		"s.retired_generation IS NOT NULL AND s.retired_at<$1",
		"d.generation=t.generation AND d.decision IS NULL",
		"i.set_generation<>h.generation",
		"i.created_at<$1",
		"a.decision IS NOT NULL AND a.decided_at<$1",
		"r.recovered_at<$1 AND r.claim_id IS NULL AND r.claim_expires_at IS NULL",
		"DELETE FROM relay_recovery_delivery_attempts",
		"a.delivery_number<>r.delivery_number",
		"NOT EXISTS(SELECT 1 FROM relay_ignored_deliveries",
		"NOT EXISTS(SELECT 1 FROM relay_recovery_deliveries",
		"c.applied_at<$1",
		"NOT EXISTS(SELECT 1 FROM relay_controller_leases",
		"NOT (c.result_kind='rotation_challenge' AND c.result_expires_at>=$3)",
		"k.state='revoked' AND k.rotation_session_id IS NOT NULL",
		"NOT EXISTS(SELECT 1 FROM relay_controller_keys k WHERE k.rotation_session_id=s.session_id)",
		"NOT EXISTS(SELECT 1 FROM relay_sessions s WHERE s.session_id=c.session_id)",
		"LIMIT $2 FOR UPDATE OF i SKIP LOCKED",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("maintenance query missing safety invariant %q", required)
		}
	}
	for _, forbidden := range []string{"a.decision IS NULL AND a.decided_at<$1", "s.retired_generation IS NULL AND s.retired_at<$1"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("maintenance deletes live delivery state through %q", forbidden)
		}
	}
	if strings.Contains(source, "deleted_selected_attempts AS") {
		t.Fatal("recovery child/parent deletion must not use a data-modifying CTE")
	}
}

func TestPruneDurableStateTreatsRetirementAsTerminalForNeverACKedDesired(t *testing.T) {
	body, err := os.ReadFile("maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	start := strings.Index(source, "SELECT d.subscription_id FROM relay_desired_states")
	if start < 0 {
		t.Fatal("retired desired-state pruning query not found")
	}
	end := strings.Index(source[start:], "DELETE FROM relay_desired_states")
	if end < 0 {
		t.Fatal("retired desired-state pruning query not found")
	}
	query := source[start : start+end]
	if !strings.Contains(query, "s.retired_generation IS NOT NULL") || !strings.Contains(query, "s.retired_at<$1") || strings.Contains(query, "d.decision IS NOT NULL") {
		t.Fatalf("retired never-ACK desired state is not horizon bounded: %s", query)
	}
}
