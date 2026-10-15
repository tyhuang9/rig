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

	"github.com/hostd/hostd/internal/auth"
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
	if mode == "ready" {
		body, err := json.Marshal(liveHostdRebindReadyMarker{Address: os.Getenv(liveHostdRebindStartupControlAddress)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, body, 0o600); err != nil {
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

	t.Run("cleanup fallback reaps stalled child", func(t *testing.T) {
		fixture, server := liveHostdRebindStartupControlFixture(t, context.Background(), nil)
		defer server.Close()
		child := liveHostdRebindStartStartupControlChild(t, t.TempDir(), fixture.listen, "stall", "")
		liveHostdRebindCleanupChild(t, child)
		liveHostdRebindAssertChildReaped(t, child)
	})
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
