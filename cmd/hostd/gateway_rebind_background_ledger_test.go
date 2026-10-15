package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

const liveHostdRebindBackgroundLedgerLimit = 4

// liveHostdRebindDenyTransport is installed before GitHub provider creation in
// the child. It records only a finite classification and returns a fixed local
// error, never a URL, header, credential, or remote response.
type liveHostdRebindDenyTransport struct {
	ledger *liveHostdRebindBoundedLedger
}

func (transport liveHostdRebindDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if transport.ledger == nil {
		panic("rebind background child network guard is missing")
	}
	if err := transport.ledger.append("github"); err != nil {
		panic("rebind background child could not record denied network request")
	}
	return nil, errors.New("test-owned network request denied")
}

// liveHostdRebindBoundedLedger permits only finite test-owned event names and
// fails its child caller on a durable write error. The child owns each path in
// its validated private manifest directory; no parent or runtime input chooses
// a ledger destination.
type liveHostdRebindBoundedLedger struct {
	mu     sync.Mutex
	path   string
	limit  int
	count  int
	failed error
}

func (ledger *liveHostdRebindBoundedLedger) append(event string) error {
	if ledger == nil || (event != "auto_deploy" && event != "controller_relay" && event != "github" && event != "relay_accept") {
		return errors.New("invalid background ledger event")
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.failed != nil {
		return ledger.failed
	}
	if ledger.count >= ledger.limit {
		ledger.failed = errors.New("background ledger limit exceeded")
		return ledger.failed
	}
	if info, err := os.Lstat(ledger.path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		ledger.failed = errors.New("background ledger path is unsafe")
		return ledger.failed
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		ledger.failed = err
		return ledger.failed
	}
	file, err := os.OpenFile(ledger.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		ledger.failed = err
		return err
	}
	if err = file.Chmod(0o600); err == nil {
		_, err = file.WriteString(event + "\n")
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		ledger.failed = err
		return err
	}
	ledger.count++
	return nil
}

func (ledger *liveHostdRebindBoundedLedger) err() error {
	if ledger == nil {
		return errors.New("missing background ledger")
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.failed
}

func (ledger *liveHostdRebindBoundedLedger) eventCount() int {
	if ledger == nil {
		return 0
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.count
}

func liveHostdRebindReadBackgroundLedger(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bounded background ledger: %w", err)
	}
	if len(body) == 0 || len(body) > 1024 {
		return nil, errors.New("invalid background ledger length")
	}
	entries := strings.Fields(string(body))
	if len(entries) > liveHostdRebindBackgroundLedgerLimit {
		return nil, errors.New("background ledger exceeds limit")
	}
	for _, entry := range entries {
		if entry != "auto_deploy" && entry != "controller_relay" && entry != "github" && entry != "relay_accept" {
			return nil, errors.New("background ledger contains invalid event")
		}
	}
	return entries, nil
}

func TestGatewayRebindBackgroundLedgerAndDenyTransport(t *testing.T) {
	ledger := &liveHostdRebindBoundedLedger{path: t.TempDir() + "/background.log", limit: liveHostdRebindBackgroundLedgerLimit}
	for _, event := range []string{"auto_deploy", "controller_relay", "github", "github"} {
		if err := ledger.append(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.append("github"); err == nil || ledger.err() == nil {
		t.Fatalf("bounded ledger accepted too many events: append=%v ledger=%v", err, ledger.err())
	}
	entries, err := liveHostdRebindReadBackgroundLedger(ledger.path)
	if err != nil || strings.Join(entries, ",") != "auto_deploy,controller_relay,github,github" {
		t.Fatalf("ledger entries=%v error=%v", entries, err)
	}

	denied := &liveHostdRebindBoundedLedger{path: t.TempDir() + "/network.log", limit: liveHostdRebindBackgroundLedgerLimit}
	request, err := http.NewRequest(http.MethodGet, "https://example.invalid/ignored", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (liveHostdRebindDenyTransport{ledger: denied}).RoundTrip(request)
	if response != nil || err == nil {
		t.Fatalf("deny transport response=%v error=%v", response, err)
	}
	events, readErr := liveHostdRebindReadBackgroundLedger(denied.path)
	if readErr != nil || strings.Join(events, ",") != "github" {
		t.Fatalf("deny transport events=%v error=%v", events, readErr)
	}
}

func TestGatewayRebindBackgroundLedgerWriteFailureIsRetained(t *testing.T) {
	ledger := &liveHostdRebindBoundedLedger{path: t.TempDir(), limit: liveHostdRebindBackgroundLedgerLimit}
	if err := ledger.append("github"); err == nil || ledger.err() == nil {
		t.Fatalf("background ledger write failure append=%v ledger=%v", err, ledger.err())
	}
}
