package generatedimage

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedStaticServerReadinessControls(t *testing.T) {
	start := time.Unix(100, 0)
	t.Run("successful readiness", func(t *testing.T) {
		completion := newManagedStaticServerCompletion()
		output := newManagedStaticServerOutput(16)
		response, err := awaitManagedStaticServerReadiness(start.Add(time.Second), func() time.Time { return start },
			func(<-chan struct{}, time.Duration) bool { t.Fatal("ready probe waited"); return false },
			func() (*http.Response, error) { return &http.Response{StatusCode: http.StatusOK}, nil }, completion, output)
		if err != nil || response == nil || response.StatusCode != http.StatusOK {
			t.Fatal("ready probe was not returned")
		}
	})

	for _, testCase := range []struct {
		name    string
		waitErr error
	}{
		{name: "clean child exit"},
		{name: "nonzero child exit", waitErr: errors.New("child exit detail is not diagnostic output")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			completion := newManagedStaticServerCompletion()
			completion.publish(testCase.waitErr)
			_, err := awaitManagedStaticServerReadiness(start.Add(time.Second), func() time.Time { return start },
				func(<-chan struct{}, time.Duration) bool { return false },
				func() (*http.Response, error) { t.Fatal("exited child was probed"); return nil, nil }, completion,
				newManagedStaticServerOutput(16))
			diagnostic := managedStaticServerDiagnostic(t, err)
			if diagnostic.category != managedStaticServerChildExited {
				t.Fatal("child exit was not classified")
			}
			if testCase.waitErr == nil && (!diagnostic.hasExit || diagnostic.exitCode != 0) {
				t.Fatal("clean child exit did not report exit code zero")
			}
		})
	}

	t.Run("deadline expiry while child runs", func(t *testing.T) {
		completion := newManagedStaticServerCompletion()
		_, err := awaitManagedStaticServerReadiness(start, func() time.Time { return start },
			func(<-chan struct{}, time.Duration) bool { t.Fatal("expired readiness waited"); return false },
			func() (*http.Response, error) { t.Fatal("expired readiness probed"); return nil, nil }, completion,
			newManagedStaticServerOutput(16))
		diagnostic := managedStaticServerDiagnostic(t, err)
		if diagnostic.category != managedStaticServerReadinessExpiry || diagnostic.hasExit {
			t.Fatal("running child deadline was not classified")
		}
	})

	t.Run("child completion wins at deadline boundary", func(t *testing.T) {
		completion := newManagedStaticServerCompletion()
		completion.publish(nil)
		_, err := awaitManagedStaticServerReadiness(start, func() time.Time { return start },
			func(<-chan struct{}, time.Duration) bool { return false },
			func() (*http.Response, error) { return nil, errors.New("unexpected") }, completion,
			newManagedStaticServerOutput(16))
		if managedStaticServerDiagnosticCategory(t, err) != managedStaticServerChildExited {
			t.Fatal("deadline boundary did not preserve child completion")
		}
	})

	t.Run("transport failure after readiness budget", func(t *testing.T) {
		completion := newManagedStaticServerCompletion()
		now := start
		_, err := awaitManagedStaticServerReadiness(start.Add(25*time.Millisecond), func() time.Time { return now },
			func(_ <-chan struct{}, duration time.Duration) bool { now = now.Add(duration); return false },
			func() (*http.Response, error) {
				return nil, errors.New("connect failure detail is not diagnostic output")
			}, completion,
			newManagedStaticServerOutput(16))
		diagnostic := managedStaticServerDiagnostic(t, err)
		if diagnostic.category != managedStaticServerTransportFailed || diagnostic.hasExit {
			t.Fatal("transport failure was not classified")
		}
	})

	t.Run("child completion during retry wait", func(t *testing.T) {
		completion := newManagedStaticServerCompletion()
		_, err := awaitManagedStaticServerReadiness(start.Add(time.Second), func() time.Time { return start },
			func(<-chan struct{}, time.Duration) bool { completion.publish(nil); return true },
			func() (*http.Response, error) { return nil, errors.New("retry") }, completion,
			newManagedStaticServerOutput(16))
		if managedStaticServerDiagnosticCategory(t, err) != managedStaticServerChildExited {
			t.Fatal("retry wait did not observe child completion")
		}
	})
}

func TestManagedStaticServerOutputDiagnosticsControls(t *testing.T) {
	output := newManagedStaticServerOutput(4)
	for _, value := range [][]byte{[]byte("ab"), []byte("cdef")} {
		count, err := output.Write(value)
		if err != nil || count != len(value) {
			t.Fatal("bounded output writer violated io.Writer semantics")
		}
	}
	snapshot := output.snapshot()
	if snapshot.bytes != 4 || !snapshot.truncated || len(snapshot.digest) != 64 {
		t.Fatal("bounded output snapshot is incomplete")
	}

	concurrent := newManagedStaticServerOutput(32)
	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			count, err := concurrent.Write([]byte("concurrent-output"))
			if err != nil || count != len("concurrent-output") {
				t.Error("concurrent output writer violated io.Writer semantics")
			}
		}()
	}
	group.Wait()
	concurrentSnapshot := concurrent.snapshot()
	if concurrentSnapshot.bytes != 32 || !concurrentSnapshot.truncated || len(concurrentSnapshot.digest) != 64 {
		t.Fatal("concurrent output capture exceeded or lost its bounded state")
	}

	secret := "secret-text-C:/dynamic/path"
	_, _ = output.Write([]byte(secret))
	err := managedStaticServerFailure(managedStaticServerChildExited, errors.New(secret), output)
	diagnostic := err.Error()
	if strings.Contains(diagnostic, secret) || strings.Contains(diagnostic, "dynamic/path") ||
		!strings.Contains(diagnostic, "category=child_exited") || !strings.Contains(diagnostic, "output_sha256=") {
		t.Fatal("startup diagnostic exposed raw child output or error text")
	}
}

func managedStaticServerDiagnosticCategory(t *testing.T, err error) managedStaticServerStartupCategory {
	t.Helper()
	return managedStaticServerDiagnostic(t, err).category
}

func managedStaticServerDiagnostic(t *testing.T, err error) managedStaticServerStartupDiagnostic {
	t.Helper()
	diagnostic, ok := err.(managedStaticServerStartupDiagnostic)
	if !ok {
		t.Fatal("startup failure did not return a closed diagnostic")
	}
	return diagnostic
}
