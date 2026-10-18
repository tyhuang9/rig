//go:build live_docker

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/database"
)

const (
	liveHostdRebindStartupControlEnvironment = "RIG_TEST_LIVE_HOSTD_REBIND_STARTUP_CONTROL"
	liveHostdRebindStartupControlMarker      = "RIG_TEST_LIVE_HOSTD_REBIND_STARTUP_MARKER"
	liveHostdRebindStartupControlStarted     = "RIG_TEST_LIVE_HOSTD_REBIND_STARTUP_STARTED"
	liveHostdRebindStartupControlAddress     = "RIG_TEST_LIVE_HOSTD_REBIND_STARTUP_ADDRESS"
	liveHostdRebindStartupControlMode        = "RIG_TEST_LIVE_HOSTD_REBIND_STARTUP_MODE"
	liveHostdRebindStartupControlSecret      = "RIG_TEST_LIVE_HOSTD_REBIND_STARTUP_SECRET"
)

// TestLiveHostdRebindControlProcessHelper is a no-Docker process control for
// its separately named parent test. It finishes any ready marker before its
// handshake, then stalls or exits, so parent controls neither read a partial
// marker nor rely on a fixed startup sleep.
func TestLiveHostdRebindControlProcessHelper(t *testing.T) {
	if os.Getenv(liveHostdRebindStartupControlEnvironment) != "1" {
		return
	}
	marker, started := os.Getenv(liveHostdRebindStartupControlMarker), os.Getenv(liveHostdRebindStartupControlStarted)
	if marker == "" || started == "" {
		t.Fatal("startup control is missing its private paths")
	}
	mode := os.Getenv(liveHostdRebindStartupControlMode)
	address := os.Getenv(liveHostdRebindStartupControlAddress)
	switch mode {
	case "ready", "refusal-normal":
		body, err := json.Marshal(liveHostdRebindReadyMarker{Address: address})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, body, 0o600); err != nil {
			t.Fatal(err)
		}
	case "refusal-recovery":
		body, err := json.Marshal(liveHostdRebindReadyMarker{Address: address, RecoveryOnly: true,
			RecoveryKind: "lan_disable", OperationID: "d5228ec6-7325-458f-851c-345277d57e1c"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, body, 0o600); err != nil {
			t.Fatal(err)
		}
	case "refusal-invalid":
		if err := os.WriteFile(marker, []byte(`{"address":"`+address+`","private":"`+
			os.Getenv(liveHostdRebindStartupControlSecret)+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(started, []byte("started"), 0o600); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "ready":
		<-time.After(time.Hour)
	case "exit":
		_, _ = fmt.Fprintf(os.Stderr, `{"msg":"runtime composition failed","error":%q}`+"\n",
			os.Getenv(liveHostdRebindStartupControlSecret))
		return
	case "refusal", "refusal-normal", "refusal-recovery", "refusal-invalid":
		_, _ = fmt.Fprintf(os.Stderr, `{"msg":"runtime composition failed","error":%q}`+"\n",
			os.Getenv(liveHostdRebindStartupControlSecret))
		os.Exit(2)
	case "stall":
		<-time.After(time.Hour)
	default:
		t.Fatal("unknown startup control mode")
	}
}

func TestLiveHostdRebindChildStartupControls(t *testing.T) {
	t.Run("readiness budget", func(t *testing.T) {
		now := time.Date(2026, time.October, 15, 18, 0, 0, 0, time.UTC)
		for _, test := range []struct {
			name        string
			deadline    time.Time
			hasDeadline bool
			want        time.Duration
			wantErr     bool
		}{
			{name: "no test deadline", want: liveHostdRebindStartupReadinessCap},
			{name: "ample test deadline", deadline: now.Add(time.Hour), hasDeadline: true, want: liveHostdRebindStartupReadinessCap},
			{name: "inherited shorter deadline", deadline: now.Add(liveHostdRebindStartupCleanupReserve + 90*time.Second), hasDeadline: true, want: 90 * time.Second},
			{name: "cleanup reserve exhausts deadline", deadline: now.Add(liveHostdRebindStartupCleanupReserve), hasDeadline: true, wantErr: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, err := liveHostdRebindStartupReadinessBudget(now, test.deadline, test.hasDeadline)
				if test.wantErr {
					if err == nil {
						t.Fatal("readiness budget unexpectedly accepted an exhausted deadline")
					}
					return
				}
				if err != nil || got != test.want {
					t.Fatalf("readiness budget=%s error=%v want=%s", got, err, test.want)
				}
			})
		}
	})

	t.Run("healthy marker requires authenticated probe", func(t *testing.T) {
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "ready", "")
		marker, err := liveHostdRebindWaitReadyResult(fixture, child, time.Now().Add(time.Hour), true)
		if err != nil || marker.Address != fixture.listen {
			t.Fatalf("healthy child readiness marker=%+v error=%v", marker, err)
		}
		liveHostdRebindKillAndReap(t, child)
	})

	t.Run("parent cancellation during authenticated probe reaps", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		probeStarted := make(chan struct{})
		fixture, server := liveHostdRebindStartupControlFixture(t, parent,
			func(_ http.ResponseWriter, request *http.Request) {
				close(probeStarted)
				<-request.Context().Done()
			})
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "ready", "")
		result := make(chan error, 1)
		go func() {
			_, err := liveHostdRebindWaitReadyResult(fixture, child, time.Now().Add(time.Hour), true)
			result <- err
		}()
		select {
		case <-probeStarted:
			cancel()
		case <-time.After(time.Minute):
			t.Fatal("authenticated probe did not start")
		}
		select {
		case err := <-result:
			if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
				t.Fatalf("cancelled probe readiness error=%q", err)
			}
		case <-time.After(time.Minute):
			t.Fatal("cancelled authenticated probe did not return")
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	t.Run("child exit keeps stderr closed", func(t *testing.T) {
		secret := "private-startup-control-secret"
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "exit", secret)
		_, err := liveHostdRebindWaitReadyResult(fixture, child, time.Now().Add(time.Hour), true)
		if err == nil || !strings.Contains(err.Error(), "startup=runtime_composition") ||
			!strings.Contains(err.Error(), "sha256:") || strings.Contains(err.Error(), secret) {
			t.Fatalf("child-exit readiness error=%q", err)
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	for _, test := range []struct {
		name      string
		parent    func() context.Context
		wantCause string
	}{
		{
			name: "cancelled parent rejects preexisting marker and reaps",
			parent: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantCause: context.Canceled.Error(),
		},
		{
			name: "expired parent deadline rejects preexisting marker and reaps",
			parent: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				return ctx
			},
			wantCause: context.DeadlineExceeded.Error(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, server := liveHostdRebindStartupControlFixture(t, test.parent(), nil)
			defer server.Close()
			child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "stall", "")
			if err := liveHostdRebindWritePrivateJSON(child.manifest.ReadyMarker,
				liveHostdRebindReadyMarker{Address: fixture.listen}); err != nil {
				t.Fatal(err)
			}
			_, err := liveHostdRebindWaitReadyResult(fixture, child, time.Now().Add(time.Hour), true)
			if err == nil || !strings.Contains(err.Error(), test.wantCause) || strings.Contains(err.Error(), "private-startup-control-secret") {
				t.Fatalf("cancelled readiness error=%q want cause %q", err, test.wantCause)
			}
			liveHostdRebindAssertChildReaped(t, child)
		})
	}

	t.Run("positive readiness budget expires and reaps stalled child", func(t *testing.T) {
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "stall", "")
		result := make(chan error, 1)
		go func() {
			_, err := liveHostdRebindWaitReadyResult(fixture, child,
				time.Now().Add(liveHostdRebindStartupCleanupReserve+200*time.Millisecond), true)
			result <- err
		}()
		select {
		case err := <-result:
			if err == nil || !strings.Contains(err.Error(), "timeout waiting for daemon assembly: "+context.DeadlineExceeded.Error()) {
				t.Fatalf("positive-budget readiness error=%q", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("positive readiness budget was not applied")
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	t.Run("bounded pending claim observation", liveHostdRebindClaimObservationControls)
	t.Run("bounded pending route observation", liveHostdRebindPendingRouteObservationControls)
	t.Run("closed startup refusal", liveHostdRebindStartupRefusalControls)

	t.Run("cleanup fallback reaps stalled child", func(t *testing.T) {
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "stall", "")
		liveHostdRebindCleanupChild(t, child)
		liveHostdRebindAssertChildReaped(t, child)
	})
}

func liveHostdRebindClaimObservationControls(t *testing.T) {
	t.Helper()
	t.Run("budget preserves the cleanup reserve", func(t *testing.T) {
		now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
		for _, test := range []struct {
			name        string
			parent      context.Context
			deadline    time.Time
			hasDeadline bool
			want        time.Duration
			wantErr     bool
		}{
			{name: "no deadlines", parent: context.Background(), want: liveHostdRebindClaimObservationCap},
			{name: "ample test deadline", parent: context.Background(), deadline: now.Add(time.Hour), hasDeadline: true, want: liveHostdRebindClaimObservationCap},
			{name: "short test deadline", parent: context.Background(), deadline: now.Add(liveHostdRebindStartupCleanupReserve + 90*time.Second), hasDeadline: true, want: 90 * time.Second},
			{name: "short fixture deadline", parent: liveHostdRebindControlDeadlineContext(t, now.Add(liveHostdRebindStartupCleanupReserve+30*time.Second)), want: 30 * time.Second},
			{name: "exhausted reserve", parent: context.Background(), deadline: now.Add(liveHostdRebindStartupCleanupReserve), hasDeadline: true, wantErr: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, err := liveHostdRebindClaimObservationBudget(now, test.parent, test.deadline, test.hasDeadline)
				if test.wantErr {
					if err == nil {
						t.Fatal("claim observation budget unexpectedly accepted exhausted reserve")
					}
					return
				}
				if err != nil || got != test.want {
					t.Fatalf("claim observation budget=%s error=%v want=%s", got, err, test.want)
				}
			})
		}
	})

	t.Run("two exact pending claims succeed from one bounded snapshot", func(t *testing.T) {
		requests := liveHostdRebindControlRequests(t, "one", "first", "two", "second")
		reads := 0
		snapshot := func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
			reads++
			return liveHostdRebindControlDisableSnapshot(
				liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared),
				liveHostdRebindControlDisableClaim("two", appaccess.AppAccessDisableWithdrawing)), nil
		}
		census, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
			nil, []string{"one", "two"}, requests, snapshot)
		if err != nil || !census.complete() || reads != 1 {
			t.Fatalf("pending claim result=%s error=%v reads=%d", census, err, reads)
		}
	})

	t.Run("real observer remains usable across bounded reads until closed", func(t *testing.T) {
		db, err := database.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		closed := false
		t.Cleanup(func() {
			if !closed {
				_ = db.Close()
			}
		})
		observer := appaccess.New(db)
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		requests := liveHostdRebindControlRequests(t, "one", "first")
		reads := 0
		_, waitErr := liveHostdRebindWaitDisableClaimsResult(parent, time.Time{}, false,
			nil, []string{"one"}, requests, func(ctx context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				value, readErr := observer.AppAccessDisableStartupSnapshot(ctx)
				reads++
				if reads == 2 {
					cancel()
				}
				return value, readErr
			})
		if waitErr == nil || reads != 2 {
			t.Fatalf("real observer wait error=%v reads=%d", waitErr, reads)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		closed = true
		if _, err := observer.AppAccessDisableStartupSnapshot(context.Background()); err == nil {
			t.Fatal("closed observer accepted a later snapshot")
		}
	})

	t.Run("missing and foreign claims do not satisfy the exact request", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			snapshot appaccess.AppAccessDisableStartupSnapshot
			foreign  int
		}{
			{
				name: "one missing",
				snapshot: liveHostdRebindControlDisableSnapshot(
					liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared)),
			},
			{
				name: "foreign operation",
				snapshot: liveHostdRebindControlDisableSnapshot(
					liveHostdRebindControlDisableClaim("foreign", appaccess.AppAccessDisablePrepared)),
				foreign: 1,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				requests := liveHostdRebindControlRequests(t, "one", "first", "two", "second")
				reads := 0
				census, err := liveHostdRebindWaitDisableClaimsResult(parent, time.Time{}, false, nil,
					[]string{"one", "two"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
						reads++
						cancel()
						return test.snapshot, nil
					})
				if err == nil || reads != 1 || census.complete() || census.foreign != test.foreign {
					t.Fatalf("missing claim result=%s error=%v reads=%d", census, err, reads)
				}
			})
		}
	})

	t.Run("duplicate requested operation IDs are rejected before a snapshot", func(t *testing.T) {
		requests := liveHostdRebindControlRequests(t, "one", "first", "two", "second")
		called := false
		if _, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
			nil, []string{"one", "one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				called = true
				return appaccess.AppAccessDisableStartupSnapshot{}, nil
			}); err == nil || !strings.Contains(err.Error(), "duplicate") || called {
			t.Fatalf("duplicate request IDs error=%v snapshot_called=%t", err, called)
		}
	})

	t.Run("committed and duplicate required claims fail", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			snapshot appaccess.AppAccessDisableStartupSnapshot
		}{
			{
				name: "committed",
				snapshot: liveHostdRebindControlDisableSnapshot(
					liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisableCommitted)),
			},
			{
				name: "duplicate",
				snapshot: liveHostdRebindControlDisableSnapshot(
					liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared),
					liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisableWithdrawing)),
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				requests := liveHostdRebindControlRequests(t, "one", "first")
				census, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
					nil, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
						return test.snapshot, nil
					})
				if err == nil || census.complete() {
					t.Fatalf("invalid required claim result=%s error=%v", census, err)
				}
			})
		}
	})

	t.Run("incompatible completed second request fails after a fresh snapshot", func(t *testing.T) {
		requests := liveHostdRebindControlRequests(t, "one", "first", "two", "second")
		requests.complete(liveHostdRebindDisableRequestOutcomeFor("two", "second", http.StatusConflict, nil))
		reads := 0
		census, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
			nil, []string{"one", "two"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				reads++
				return liveHostdRebindControlDisableSnapshot(
					liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared)), nil
			})
		if err == nil || reads != 1 || census.missing != 1 || !strings.Contains(err.Error(), "lacks its durable claim") ||
			strings.Contains(err.Error(), "private") {
			t.Fatalf("incompatible outcome result=%s error=%v reads=%d", census, err, reads)
		}
	})

	t.Run("transport error retains an observed durable claim", func(t *testing.T) {
		const secret = "private-http-error"
		requests := liveHostdRebindControlRequests(t, "one", "first")
		requests.complete(liveHostdRebindDisableRequestOutcomeFor("one", "first", 0, errors.New(secret)))
		census, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
			nil, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				return liveHostdRebindControlDisableSnapshot(
					liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared)), nil
			})
		if err != nil || !census.complete() || strings.Contains(requests.summary(), secret) {
			t.Fatalf("transport outcome result=%s error=%v requests=%s", census, err, requests.summary())
		}
	})

	t.Run("HTTP success cannot substitute for a missing durable claim", func(t *testing.T) {
		for _, status := range []int{http.StatusOK, http.StatusCreated} {
			t.Run(http.StatusText(status), func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				requests := liveHostdRebindControlRequests(t, "one", "first")
				requests.complete(liveHostdRebindDisableRequestOutcomeFor("one", "first", status, nil))
				census, err := liveHostdRebindWaitDisableClaimsResult(parent, time.Time{}, false,
					nil, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
						cancel()
						return appaccess.AppAccessDisableStartupSnapshot{}, nil
					})
				if err == nil || census.complete() || !strings.Contains(requests.summary(), "status=") {
					t.Fatalf("HTTP success result=%s error=%v requests=%s", census, err, requests.summary())
				}
			})
		}
	})

	t.Run("read error and cancellation remain observable", func(t *testing.T) {
		const secret = "private-snapshot-error"
		requests := liveHostdRebindControlRequests(t, "one", "first")
		if _, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
			nil, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				return appaccess.AppAccessDisableStartupSnapshot{}, errors.New(secret)
			}); err == nil || !strings.Contains(err.Error(), "snapshot failed") || strings.Contains(err.Error(), secret) {
			t.Fatalf("read error=%v", err)
		}

		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan struct{})
		go func() {
			<-started
			cancel()
		}()
		var observed error
		requests = liveHostdRebindControlRequests(t, "one", "first")
		if _, err := liveHostdRebindWaitDisableClaimsResult(parent, time.Time{}, false,
			nil, []string{"one"}, requests, func(ctx context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				close(started)
				<-ctx.Done()
				observed = ctx.Err()
				return appaccess.AppAccessDisableStartupSnapshot{}, observed
			}); err == nil || !errors.Is(observed, context.Canceled) {
			t.Fatalf("cancelled snapshot error=%v observed=%v", err, observed)
		}
	})

	t.Run("complete snapshot cannot outrun cancellation or child exit", func(t *testing.T) {
		t.Run("cancellation", func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			requests := liveHostdRebindControlRequests(t, "one", "first")
			census, err := liveHostdRebindWaitDisableClaimsResult(parent, time.Time{}, false,
				nil, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
					cancel()
					return liveHostdRebindControlDisableSnapshot(
						liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared)), nil
				})
			if err == nil || !strings.Contains(err.Error(), "ended: canceled") || !census.complete() {
				t.Fatalf("cancelled complete snapshot result=%s error=%v", census, err)
			}
		})
		t.Run("child exit", func(t *testing.T) {
			childDone := make(chan struct{})
			requests := liveHostdRebindControlRequests(t, "one", "first")
			census, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
				childDone, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
					close(childDone)
					return liveHostdRebindControlDisableSnapshot(
						liveHostdRebindControlDisableClaim("one", appaccess.AppAccessDisablePrepared)), nil
				})
			if err == nil || !strings.Contains(err.Error(), "child exited") || !census.complete() {
				t.Fatalf("exited complete snapshot result=%s error=%v", census, err)
			}
		})
	})

	t.Run("early child exit and both buffered completions return", func(t *testing.T) {
		requests := liveHostdRebindControlRequests(t, "one", "first")
		childDone := make(chan struct{})
		close(childDone)
		called := false
		if _, err := liveHostdRebindWaitDisableClaimsResult(context.Background(), time.Time{}, false,
			childDone, []string{"one"}, requests, func(context.Context) (appaccess.AppAccessDisableStartupSnapshot, error) {
				called = true
				return appaccess.AppAccessDisableStartupSnapshot{}, nil
			}); err == nil || called {
			t.Fatalf("early child exit error=%v snapshot_called=%t", err, called)
		}

		requests = liveHostdRebindControlRequests(t, "one", "first", "two", "second")
		sent := make(chan struct{})
		go func() {
			requests.complete(liveHostdRebindDisableRequestOutcomeFor("one", "first", http.StatusOK, nil))
			requests.complete(liveHostdRebindDisableRequestOutcomeFor("two", "second", http.StatusCreated, nil))
			close(sent)
		}()
		select {
		case <-sent:
		case <-time.After(time.Second):
			t.Fatal("buffered request completions blocked")
		}
		joinContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := requests.join(joinContext); err != nil || len(requests.outcomes) != 2 {
			t.Fatalf("buffered request join error=%v outcomes=%d", err, len(requests.outcomes))
		}
	})

	t.Run("request cleanup shares its join deadline and remains idempotent", func(t *testing.T) {
		requests := liveHostdRebindControlRequests(t, "one", "first", "two", "second")
		requests.complete(liveHostdRebindDisableRequestOutcomeFor("one", "first", http.StatusOK, nil))
		joiner := liveHostdRebindNewDisableRequestJoiner(requests, 25*time.Millisecond)
		firstJoinErr := joiner.join()
		requests.complete(liveHostdRebindDisableRequestOutcomeFor("two", "second", http.StatusCreated, nil))
		secondJoinErr := joiner.join()
		if !errors.Is(firstJoinErr, context.DeadlineExceeded) || secondJoinErr != firstJoinErr || !joiner.attempted ||
			len(requests.outcomes) != 1 {
			t.Fatalf("shared join first=%v second=%v attempted=%t outcomes=%d", firstJoinErr, secondJoinErr,
				joiner.attempted, len(requests.outcomes))
		}

		cancelled := make(chan struct{})
		reapCalls, joinCalls := 0, 0
		wantReap, wantJoin := errors.New("reap"), errors.New("join")
		cleanup := liveHostdRebindPendingCurrentCleanup{
			cancel: func() { close(cancelled) },
			reap: func() error {
				reapCalls++
				select {
				case <-cancelled:
				default:
					t.Fatal("cleanup reaped before canceling requests")
				}
				return wantReap
			},
			join: func() error { joinCalls++; return wantJoin },
		}
		firstReap, firstCleanupJoin := cleanup.run()
		secondReap, secondCleanupJoin := cleanup.run()
		if firstReap != wantReap || firstCleanupJoin != wantJoin || secondReap != wantReap ||
			secondCleanupJoin != wantJoin || reapCalls != 1 || joinCalls != 1 {
			t.Fatalf("cleanup results=%v/%v then %v/%v calls reaped=%d joined=%d",
				firstReap, firstCleanupJoin, secondReap, secondCleanupJoin, reapCalls, joinCalls)
		}
	})

	t.Run("late second outcome joins after the waiter retained the first", func(t *testing.T) {
		requests := liveHostdRebindControlRequests(t, "one", "first", "two", "second")
		firstRetained := make(chan struct{})
		requests.afterRetain = func(outcome liveHostdRebindDisableRequestOutcome) {
			if outcome.operationID == "one" {
				close(firstRetained)
			}
		}
		requests.complete(liveHostdRebindDisableRequestOutcomeFor("one", "first", http.StatusOK, nil))
		joinContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		joined := make(chan error, 1)
		go func() { joined <- requests.join(joinContext) }()
		select {
		case <-firstRetained:
		case <-time.After(time.Second):
			t.Fatal("waiter did not retain the first outcome")
		}
		requests.complete(liveHostdRebindDisableRequestOutcomeFor("two", "second", http.StatusCreated, nil))
		select {
		case err := <-joined:
			if err != nil || len(requests.outcomes) != 2 {
				t.Fatalf("late second join error=%v outcomes=%d", err, len(requests.outcomes))
			}
		case <-time.After(time.Second):
			t.Fatal("late second outcome did not release the waiter")
		}
	})

	t.Run("blocked HTTP request is canceled before reaping and joining", func(t *testing.T) {
		requestStarted := make(chan struct{})
		releaseHandler := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(requestStarted)
			select {
			case <-request.Context().Done():
			case <-releaseHandler:
			}
		}))
		defer server.Close()
		requestContext, cancelRequest := context.WithCancel(context.Background())
		defer cancelRequest()
		request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		requests := liveHostdRebindControlRequests(t, "one", "first")
		clientDone := make(chan struct{})
		go func() {
			_, requestErr := server.Client().Do(request)
			requests.complete(liveHostdRebindDisableRequestOutcomeFor("one", "first", 0, requestErr))
			close(clientDone)
		}()
		select {
		case <-requestStarted:
		case <-time.After(time.Second):
			t.Fatal("HTTP request did not reach its blocked handler")
		}
		joiner := liveHostdRebindNewDisableRequestJoiner(requests, time.Second)
		cancelled, reaped := false, false
		cleanup := liveHostdRebindPendingCurrentCleanup{
			cancel: func() {
				cancelled = true
				cancelRequest()
			},
			reap: func() error {
				if !cancelled {
					return errors.New("request was not canceled before reap")
				}
				reaped = true
				return nil
			},
			join: func() error {
				if !reaped {
					return errors.New("request joined before reap")
				}
				return joiner.join()
			},
		}
		reapErr, joinErr := cleanup.run()
		close(releaseHandler)
		select {
		case <-clientDone:
		case <-time.After(time.Second):
			t.Fatal("canceled HTTP request did not report an outcome")
		}
		if reapErr != nil || joinErr != nil || !cancelled || !reaped || !strings.Contains(requests.summary(), "transport=canceled") {
			t.Fatalf("HTTP cancellation cleanup reaping=%v joining=%v cancelled=%t reaped=%t requests=%s",
				reapErr, joinErr, cancelled, reaped, requests.summary())
		}
	})
}

func liveHostdRebindStartupRefusalControls(t *testing.T) {
	t.Helper()
	run := func(t *testing.T, mode string, deadline time.Time, hasDeadline bool,
		reap func(*liveHostdRebindChild) error,
	) (liveHostdRebindStartupRefusalObservation, error) {
		t.Helper()
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		t.Cleanup(server.Close)
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, mode, "private-refusal-control-secret")
		return liveHostdRebindStartupRefusalResult(fixture, child, deadline, hasDeadline, reap)
	}

	t.Run("nonzero exit with absent marker succeeds", func(t *testing.T) {
		result, err := run(t, "refusal", time.Now().Add(time.Hour), true, liveHostdRebindKillAndReapError)
		if err != nil || result.exit != "nonzero" || result.marker != "absent" ||
			!strings.Contains(result.diagnostic, "sha256:") || strings.Contains(result.String(), "private-refusal-control-secret") {
			t.Fatalf("refusal result=%s error=%v", result, err)
		}
	})

	t.Run("pre-canceled parent overrides an exited child", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		fixture, server := liveHostdRebindStartupControlFixture(t, parent, nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "refusal", "private-refusal-control-secret")
		result, err := liveHostdRebindStartupRefusalResult(fixture, child,
			time.Now().Add(time.Hour), true, liveHostdRebindKillAndReapError)
		if err == nil || result.exit != "canceled" || strings.Contains(result.String(), "private-refusal-control-secret") {
			t.Fatalf("pre-canceled refusal result=%s error=%v", result, err)
		}
	})

	for _, test := range []struct {
		name       string
		mode       string
		deadline   func() (time.Time, bool)
		wantExit   string
		wantMarker string
	}{
		{name: "zero exit fails", mode: "exit", deadline: func() (time.Time, bool) { return time.Now().Add(time.Hour), true }, wantExit: "zero", wantMarker: "absent"},
		{name: "normal marker fails", mode: "refusal-normal", deadline: func() (time.Time, bool) { return time.Now().Add(time.Hour), true }, wantExit: "nonzero", wantMarker: "normal"},
		{name: "recovery only marker fails", mode: "refusal-recovery", deadline: func() (time.Time, bool) { return time.Now().Add(time.Hour), true }, wantExit: "nonzero", wantMarker: "recovery-only"},
		{name: "invalid marker fails", mode: "refusal-invalid", deadline: func() (time.Time, bool) { return time.Now().Add(time.Hour), true }, wantExit: "nonzero", wantMarker: "invalid"},
		{name: "stalled child fails", mode: "stall", deadline: func() (time.Time, bool) {
			return time.Now().Add(liveHostdRebindStartupCleanupReserve + 3*time.Second), true
		}, wantExit: "deadline_exceeded", wantMarker: "absent"},
		{name: "exhausted deadline fails", mode: "stall", deadline: func() (time.Time, bool) { return time.Now().Add(liveHostdRebindStartupCleanupReserve), true }, wantExit: "unknown", wantMarker: "absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			deadline, hasDeadline := test.deadline()
			result, err := run(t, test.mode, deadline, hasDeadline, liveHostdRebindKillAndReapError)
			if err == nil || result.exit != test.wantExit || result.marker != test.wantMarker ||
				strings.Contains(result.String(), "private-refusal-control-secret") {
				t.Fatalf("refusal result=%s error=%v", result, err)
			}
		})
	}

	t.Run("failed reap never reads stderr", func(t *testing.T) {
		const secret = "private-reap-failure-secret"
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "stall", "")
		child.stderr.WriteString(secret)
		result, err := liveHostdRebindStartupRefusalResult(fixture, child,
			time.Now().Add(liveHostdRebindStartupCleanupReserve), true,
			func(*liveHostdRebindChild) error { return errors.New(secret) })
		if err == nil || result.diagnostic != "unread" || strings.Contains(result.String(), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("failed reap result=%s error=%v", result, err)
		}
		liveHostdRebindKillAndReap(t, child)
	})
}

func liveHostdRebindControlDeadlineContext(t *testing.T, deadline time.Time) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	return ctx
}

func liveHostdRebindControlRequests(t *testing.T, values ...string) *liveHostdRebindDisableRequests {
	t.Helper()
	if len(values)%2 != 0 {
		t.Fatal("pending request control requires operation/phase pairs")
	}
	requests := liveHostdRebindNewDisableRequests()
	for index := 0; index < len(values); index += 2 {
		if err := requests.register(values[index], values[index+1]); err != nil {
			t.Fatal(err)
		}
	}
	return requests
}

func liveHostdRebindControlDisableClaim(operationID string,
	state appaccess.AppAccessDisableState,
) appaccess.AppAccessDisableStartupClaim {
	return appaccess.AppAccessDisableStartupClaim{Claim: appaccess.AppAccessDisableClaim{OperationID: operationID, State: state}}
}

func liveHostdRebindControlDisableSnapshot(values ...appaccess.AppAccessDisableStartupClaim) appaccess.AppAccessDisableStartupSnapshot {
	return appaccess.AppAccessDisableStartupSnapshot{Claims: values}
}

func liveHostdRebindStartupControlFixture(t *testing.T, ctx context.Context,
	afterAuthenticatedRequest func(http.ResponseWriter, *http.Request),
) (*liveHostdGatewayRebindFixture, *httptest.Server) {
	t.Helper()
	const token = "startup-control-session"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(auth.SessionCookie)
		if err != nil || cookie.Value != token || request.Method != http.MethodGet ||
			!strings.HasPrefix(request.URL.Path, "/api/v1/system/lan-gateway-rebind/") {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if afterAuthenticatedRequest != nil {
			afterAuthenticatedRequest(response, request)
			return
		}
		response.WriteHeader(http.StatusNotFound)
	}))
	return &liveHostdGatewayRebindFixture{
		ctx: ctx, listen: strings.TrimPrefix(server.URL, "http://"), session: auth.Session{Token: token},
	}, server
}

func liveHostdRebindStartStartupControlChild(t *testing.T, directory, address, mode, secret string) *liveHostdRebindChild {
	t.Helper()
	readyMarker, started := filepath.Join(directory, "ready.json"), filepath.Join(directory, "started")
	command := exec.Command(os.Args[0], "-test.run=^TestLiveHostdRebindControlProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(),
		liveHostdRebindStartupControlEnvironment+"=1",
		liveHostdRebindStartupControlMarker+"="+readyMarker,
		liveHostdRebindStartupControlStarted+"="+started,
		liveHostdRebindStartupControlAddress+"="+address,
		liveHostdRebindStartupControlMode+"="+mode,
		liveHostdRebindStartupControlSecret+"="+secret,
	)
	child := &liveHostdRebindChild{
		command: command, manifest: liveHostdRebindChildManifest{ReadyMarker: readyMarker}, waitDone: make(chan struct{}),
	}
	command.Stdout = io.Discard
	command.Stderr = &child.stderr
	if err := command.Start(); err != nil {
		t.Fatal("start startup control child:", err)
	}
	t.Cleanup(func() { liveHostdRebindCleanupChild(t, child) })
	liveHostdRebindWaitForStartupControlHandshake(t, started)
	return child
}

func liveHostdRebindWaitForStartupControlHandshake(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("startup control child did not write its handshake")
		case <-ticker.C:
		}
	}
}

func liveHostdRebindAssertChildReaped(t *testing.T, child *liveHostdRebindChild) {
	t.Helper()
	if !liveHostdRebindChildWaited(child) || child.command.ProcessState == nil {
		t.Fatal("startup control child was not reaped")
	}
}
