package autodeploy

import (
	"context"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/wss"
)

const (
	relayOutageInitialSyncTriggerSetupDeadline = "setup_deadline"
	relayOutageInitialSyncTriggerLocalDeadline = "initial_sync_deadline"
	relayOutageDiagnosticMillisecondsLimit     = int64(60000)
)

type relayOutageInitialSyncFailureDiagnostics struct {
	trigger          string
	setupElapsedMS   int64
	setupRemainingMS int64
	waitElapsedMS    int64
}

func relayOutageInitialSyncTrigger(parentDone, localDeadline bool) string {
	if localDeadline {
		return relayOutageInitialSyncTriggerLocalDeadline
	}
	if parentDone {
		return relayOutageInitialSyncTriggerSetupDeadline
	}
	return relayOutageInitialSyncTriggerSetupDeadline
}

func relayOutageCaptureInitialSyncFailure(trigger string, setupElapsed, setupRemaining, waitElapsed time.Duration) relayOutageInitialSyncFailureDiagnostics {
	return relayOutageInitialSyncFailureDiagnostics{
		trigger:          relayOutageSafeInitialSyncTrigger(trigger),
		setupElapsedMS:   relayOutageDiagnosticDurationMilliseconds(setupElapsed),
		setupRemainingMS: relayOutageDiagnosticDurationMilliseconds(setupRemaining),
		waitElapsedMS:    relayOutageDiagnosticDurationMilliseconds(waitElapsed),
	}
}

func relayOutageSafeInitialSyncTrigger(trigger string) string {
	switch trigger {
	case relayOutageInitialSyncTriggerSetupDeadline, relayOutageInitialSyncTriggerLocalDeadline:
		return trigger
	default:
		return relayOutageInitialSyncTriggerSetupDeadline
	}
}

func relayOutageSetupRemainingAt(parent context.Context, at time.Time) time.Duration {
	if parent == nil {
		return 0
	}
	deadline, ok := parent.Deadline()
	if !ok {
		return 0
	}
	return deadline.Sub(at)
}

func relayOutageDiagnosticDurationMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	milliseconds := int64(value / time.Millisecond)
	if milliseconds > relayOutageDiagnosticMillisecondsLimit {
		return relayOutageDiagnosticMillisecondsLimit
	}
	return milliseconds
}

func relayOutageDiagnosticSigned(value int64) int64 {
	if value < 0 {
		return -1
	}
	if value > relayOutageDiagnosticCountLimit {
		return relayOutageDiagnosticCountLimit
	}
	return value
}

func relayOutageDiagnosticUint64(value uint64) int64 {
	if value > uint64(relayOutageDiagnosticCountLimit) {
		return relayOutageDiagnosticCountLimit
	}
	return int64(value)
}

type relayOutageSafeHandlerSnapshot struct {
	handshake            int64
	authenticated        int64
	closed               int64
	storeFailure         int64
	readFailure          int64
	writeFailure         int64
	currentActive        int64
	currentAuthenticated int64
	leasesActive         int64
}

func relayOutageSafeHandlerStats(handler *wss.Handler) relayOutageSafeHandlerSnapshot {
	if handler == nil {
		return relayOutageSafeHandlerSnapshot{
			handshake: -1, authenticated: -1, closed: -1, storeFailure: -1,
			readFailure: -1, writeFailure: -1, currentActive: -1,
			currentAuthenticated: -1, leasesActive: -1,
		}
	}
	return relayOutageSafeHandlerStatsFrom(handler.Stats())
}

func relayOutageSafeHandlerStatsFrom(stats wss.Stats) relayOutageSafeHandlerSnapshot {
	return relayOutageSafeHandlerSnapshot{
		handshake:            relayOutageWSSHistoryCount(stats, "handshake"),
		authenticated:        relayOutageWSSHistoryCount(stats, "authenticated"),
		closed:               relayOutageWSSHistoryCount(stats, "closed"),
		storeFailure:         relayOutageWSSHistoryCount(stats, "store_failure"),
		readFailure:          relayOutageWSSHistoryCount(stats, "read_failure"),
		writeFailure:         relayOutageWSSHistoryCount(stats, "write_failure"),
		currentActive:        relayOutageDiagnosticSigned(stats.Active),
		currentAuthenticated: relayOutageDiagnosticSigned(stats.Authenticated),
		leasesActive:         relayOutageDiagnosticSigned(stats.LeasesActive),
	}
}

func relayOutageWSSHistoryCount(stats wss.Stats, name string) int64 {
	for index, candidate := range wss.LifecycleOutcomeNames() {
		if candidate == name {
			return relayOutageDiagnosticUint64(stats.LifecycleOutcomes[index])
		}
	}
	return -1
}

func TestRelayOutageDiagnosticDurationAndCountClamping(t *testing.T) {
	durationTests := []struct {
		name string
		in   time.Duration
		want int64
	}{
		{name: "negative", in: -time.Second, want: 0},
		{name: "submillisecond", in: 999 * time.Microsecond, want: 0},
		{name: "ordinary", in: 1500 * time.Millisecond, want: 1500},
		{name: "huge", in: 24 * time.Hour, want: relayOutageDiagnosticMillisecondsLimit},
	}
	for _, test := range durationTests {
		t.Run("duration_"+test.name, func(t *testing.T) {
			if got := relayOutageDiagnosticDurationMilliseconds(test.in); got != test.want {
				t.Fatalf("duration=%d want=%d", got, test.want)
			}
		})
	}

	countTests := []struct {
		name string
		in   int64
		want int64
	}{
		{name: "negative", in: -1, want: -1},
		{name: "ordinary", in: 17, want: 17},
		{name: "huge", in: int64(^uint64(0) >> 1), want: relayOutageDiagnosticCountLimit},
	}
	for _, test := range countTests {
		t.Run("count_"+test.name, func(t *testing.T) {
			if got := relayOutageDiagnosticSigned(test.in); got != test.want {
				t.Fatalf("count=%d want=%d", got, test.want)
			}
		})
	}
	if got := relayOutageDiagnosticUint64(^uint64(0)); got != relayOutageDiagnosticCountLimit {
		t.Fatalf("unsigned count=%d want=%d", got, relayOutageDiagnosticCountLimit)
	}
}

func TestRelayOutageInitialSyncDiagnosticsCaptureParentAndLocalDeadline(t *testing.T) {
	parent := relayOutageCaptureInitialSyncFailure(relayOutageInitialSyncTrigger(true, false), 3*time.Second, 17*time.Second, 2*time.Second)
	if parent.trigger != relayOutageInitialSyncTriggerSetupDeadline || parent.setupElapsedMS != 3000 || parent.setupRemainingMS != 17000 || parent.waitElapsedMS != 2000 {
		t.Fatalf("parent diagnostics=%#v", parent)
	}

	local := relayOutageCaptureInitialSyncFailure(relayOutageInitialSyncTrigger(false, true), 4*time.Second, 16*time.Second, 5*time.Second)
	if local.trigger != relayOutageInitialSyncTriggerLocalDeadline || local.setupElapsedMS != 4000 || local.setupRemainingMS != 16000 || local.waitElapsedMS != 5000 {
		t.Fatalf("local diagnostics=%#v", local)
	}
}

func TestRelayOutageDiagnosticsSanitizeUnknownTriggerAndSupervisorState(t *testing.T) {
	failure := relayOutageCaptureInitialSyncFailure("untrusted-trigger", -time.Second, 24*time.Hour, -time.Second)
	if failure.trigger != relayOutageInitialSyncTriggerSetupDeadline || failure.setupElapsedMS != 0 || failure.setupRemainingMS != relayOutageDiagnosticMillisecondsLimit || failure.waitElapsedMS != 0 {
		t.Fatalf("sanitized failure=%#v", failure)
	}

	if got := relayOutageSafeSupervisorState("untrusted-state"); got != "unavailable" {
		t.Fatalf("state=%q want unavailable", got)
	}
	if got := relayOutageSafeSupervisorOutcome("untrusted-outcome"); got != "unavailable" {
		t.Fatalf("outcome=%q want unavailable", got)
	}
}

func TestRelayOutageDiagnosticsExcludeUnrequestedWSSHistory(t *testing.T) {
	stats := wss.Stats{Active: 4, Authenticated: 3, LeasesActive: 2}
	for index, name := range wss.LifecycleOutcomeNames() {
		stats.LifecycleOutcomes[index] = 777
		switch name {
		case "handshake", "authenticated", "closed", "store_failure", "read_failure", "write_failure":
			stats.LifecycleOutcomes[index] = 99
		}
	}
	safe := relayOutageSafeHandlerStatsFrom(stats)
	if safe.handshake != 99 || safe.authenticated != 99 || safe.closed != 99 || safe.storeFailure != 99 || safe.readFailure != 99 || safe.writeFailure != 99 {
		t.Fatalf("selected history=%#v", safe)
	}
	if safe.currentActive != 4 || safe.currentAuthenticated != 3 || safe.leasesActive != 2 {
		t.Fatalf("selected gauges=%#v", safe)
	}
}
