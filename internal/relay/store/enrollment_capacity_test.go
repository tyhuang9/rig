package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func capacityTestEnrollment() EnrollmentInput {
	return EnrollmentInput{
		ControllerID: testController, KeyID: testKey, PublicKey: bytes.Repeat([]byte{2}, 32),
		InstallationID: 1, RepositoryID: 2, StateHash: bytes.Repeat([]byte{3}, 32), PollHash: bytes.Repeat([]byte{4}, 32),
		PKCECiphertext: bytes.Repeat([]byte{5}, 29), PKCESealNonce: bytes.Repeat([]byte{6}, 12), RequestNonce: bytes.Repeat([]byte{7}, 32),
		ExpiresAt: fixedNow.Add(time.Minute),
	}
}

func expectEnrollmentCapacityPrefix(m pgxmock.PgxPoolIface, input EnrollmentInput, replay bool) {
	m.ExpectBegin()
	m.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(enrollmentCapacityLock).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	m.ExpectExec("UPDATE relay_enrollments SET status='expired'").WithArgs(fixedNow).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	m.ExpectQuery("SELECT EXISTS").WithArgs(input.ControllerID, input.KeyID, input.RequestNonce).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(replay))
}

func TestCreateEnrollmentCapacityAndReplayOrdering(t *testing.T) {
	input := capacityTestEnrollment()
	t.Run("active cap rejects without insert", func(t *testing.T) {
		s, m := mockStore(t)
		expectEnrollmentCapacityPrefix(m, input, false)
		m.ExpectQuery("SELECT count").WithArgs(fixedNow).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(MaximumActiveEnrollments))
		m.ExpectRollback()
		if _, err := s.CreateEnrollment(context.Background(), input); !errors.Is(err, ErrCapacity) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("one slot inserts and commits", func(t *testing.T) {
		s, m := mockStore(t)
		expectEnrollmentCapacityPrefix(m, input, false)
		m.ExpectQuery("SELECT count").WithArgs(fixedNow).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(MaximumActiveEnrollments - 1))
		m.ExpectExec("INSERT INTO relay_enrollments").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		m.ExpectCommit()
		if _, err := s.CreateEnrollment(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("replay wins before count at cap", func(t *testing.T) {
		s, m := mockStore(t)
		expectEnrollmentCapacityPrefix(m, input, true)
		m.ExpectRollback()
		if _, err := s.CreateEnrollment(context.Background(), input); !errors.Is(err, ErrReplay) {
			t.Fatalf("error=%v", err)
		}
		if err := m.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
