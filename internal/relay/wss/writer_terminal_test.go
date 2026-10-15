package wss

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hostd/hostd/internal/relay/protocol"
)

type writerTerminalSocket struct {
	writeStarted chan struct{}
	releaseWrite <-chan struct{}
	writeErr     error
	writeOnce    sync.Once
}

type observeNilErrContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *observeNilErrContext) Err() error {
	err := ctx.Context.Err()
	if err == nil {
		ctx.once.Do(func() { close(ctx.observed) })
	}
	return err
}

func (socket *writerTerminalSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (socket *writerTerminalSocket) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	socket.writeOnce.Do(func() { close(socket.writeStarted) })
	select {
	case <-socket.releaseWrite:
		return socket.writeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*writerTerminalSocket) Close(websocket.StatusCode, string) error { return nil }
func (*writerTerminalSocket) CloseNow() error                          { return nil }
func (*writerTerminalSocket) SetReadLimit(int64)                       {}
func (*writerTerminalSocket) Subprotocol() string                      { return protocol.Subprotocol }

func TestWriterFailureRemainsAvailableAfterEphemeralNotifications(t *testing.T) {
	writeFailure := errors.New("forced terminal write failure")
	releaseWrite := make(chan struct{})
	socket := &writerTerminalSocket{writeStarted: make(chan struct{}), releaseWrite: releaseWrite, writeErr: writeFailure}
	session := newWriterTerminalSession(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(session.writerDone)
		session.writeLoop(ctx)
	}()

	request := writeRequest{data: []byte("first request"), done: make(chan error, 1)}
	session.writes <- request
	waitWriterTerminalSignal(t, socket.writeStarted, "writer did not start")
	close(releaseWrite)
	if err := waitWriterTerminalError(t, request.done, "in-flight request did not finish"); !errors.Is(err, writeFailure) {
		t.Fatalf("in-flight error=%v, want writer cause", err)
	}
	waitWriterTerminalSignal(t, session.writerDone, "writer did not stop")
	if err := waitWriterTerminalError(t, session.writerFailed, "writer failure notification missing"); !errors.Is(err, writeFailure) {
		t.Fatalf("ephemeral writer error=%v, want writer cause", err)
	}

	frame := &protocol.Heartbeat{Envelope: protocol.NewEnvelope(protocol.TypeHeartbeat, "00000000-0000-4000-8000-000000000001", time.Unix(1, 0).UTC()), Sequence: 1}
	if err := session.sendFrame(ctx, frame); !errors.Is(err, writeFailure) {
		t.Fatalf("post-terminal send error=%v, want durable writer cause", err)
	}
	if queued := len(session.writes); queued != 0 {
		t.Fatalf("post-terminal send queued %d orphaned request(s)", queued)
	}
}

func TestWriterFailureResolvesAndClearsQueuedRequests(t *testing.T) {
	writeFailure := errors.New("forced queued write failure")
	releaseWrite := make(chan struct{})
	socket := &writerTerminalSocket{writeStarted: make(chan struct{}), releaseWrite: releaseWrite, writeErr: writeFailure}
	session := newWriterTerminalSession(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(session.writerDone)
		session.writeLoop(ctx)
	}()

	inFlightData := []byte("in-flight private frame")
	queuedData := []byte("queued private frame")
	inFlight := writeRequest{data: inFlightData, done: make(chan error, 1)}
	queued := writeRequest{data: queuedData, done: make(chan error, 1)}
	session.writes <- inFlight
	waitWriterTerminalSignal(t, socket.writeStarted, "writer did not start")
	session.writes <- queued
	close(releaseWrite)
	if err := waitWriterTerminalError(t, inFlight.done, "in-flight request did not finish"); !errors.Is(err, writeFailure) {
		t.Fatalf("in-flight error=%v, want writer cause", err)
	}
	waitWriterTerminalSignal(t, session.writerDone, "writer did not stop")
	select {
	case err := <-queued.done:
		if !errors.Is(err, writeFailure) {
			t.Fatalf("queued error=%v, want writer cause", err)
		}
	default:
		t.Fatal("queued request did not receive terminal writer cause")
	}
	assertWriterTerminalBytesCleared(t, inFlightData, "in-flight")
	assertWriterTerminalBytesCleared(t, queuedData, "queued")
}

func TestWriterFailureClassificationIsStableAcrossConcurrentCancellation(t *testing.T) {
	writeFailure := errors.New("failure at cancellation boundary")
	releaseWrite := make(chan struct{})
	close(releaseWrite)
	socket := &writerTerminalSocket{writeStarted: make(chan struct{}), releaseWrite: releaseWrite, writeErr: writeFailure}
	session := newWriterTerminalSession(t, socket)
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observeNilErrContext{Context: baseCtx, observed: make(chan struct{})}
	inFlight := writeRequest{data: []byte("boundary in-flight frame"), done: make(chan error)}
	queuedData := []byte("boundary queued frame")
	queued := writeRequest{data: queuedData, done: make(chan error, 1)}
	session.writes <- inFlight
	session.writes <- queued
	go func() {
		defer close(session.writerDone)
		session.writeLoop(ctx)
	}()

	waitWriterTerminalSignal(t, ctx.observed, "writer did not classify the boundary failure")
	cancel()
	if err := waitWriterTerminalError(t, inFlight.done, "boundary request did not finish"); !errors.Is(err, writeFailure) {
		t.Fatalf("boundary request error=%v, want writer cause", err)
	}
	waitWriterTerminalSignal(t, session.writerDone, "boundary writer did not stop")
	select {
	case err := <-session.writerFailed:
		if !errors.Is(err, writeFailure) {
			t.Fatalf("published boundary error=%v, want writer cause", err)
		}
	default:
		t.Fatal("genuine writer failure was lost after concurrent cancellation")
	}
	select {
	case err := <-queued.done:
		if !errors.Is(err, writeFailure) {
			t.Fatalf("queued boundary error=%v, want writer cause", err)
		}
	default:
		t.Fatal("queued boundary request did not receive writer cause")
	}
	assertWriterTerminalBytesCleared(t, queuedData, "boundary queued")
}

func TestWriterCancellationResolvesQueuedRequestsWithoutFailureCause(t *testing.T) {
	socket := &writerTerminalSocket{writeStarted: make(chan struct{})}
	session := newWriterTerminalSession(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(session.writerDone)
		session.writeLoop(ctx)
	}()

	inFlightData := []byte("cancelled in-flight frame")
	queuedData := []byte("cancelled queued frame")
	inFlight := writeRequest{data: inFlightData, done: make(chan error, 1)}
	queued := writeRequest{data: queuedData, done: make(chan error, 1)}
	session.writes <- inFlight
	waitWriterTerminalSignal(t, socket.writeStarted, "writer did not start")
	session.writes <- queued
	cancel()
	if err := waitWriterTerminalError(t, inFlight.done, "cancelled in-flight request did not finish"); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation=%v", err)
	}
	waitWriterTerminalSignal(t, session.writerDone, "cancelled writer did not stop")
	select {
	case err := <-queued.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation=%v", err)
		}
	default:
		t.Fatal("queued request did not receive cancellation")
	}
	select {
	case err := <-session.writerFailed:
		t.Fatalf("cancellation published a writer failure: %v", err)
	default:
	}
	assertWriterTerminalBytesCleared(t, inFlightData, "cancelled in-flight")
	assertWriterTerminalBytesCleared(t, queuedData, "cancelled queued")
}

func TestWriterNormalCloseUsesGenericStoppedFallback(t *testing.T) {
	socket := &writerTerminalSocket{writeStarted: make(chan struct{})}
	session := newWriterTerminalSession(t, socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(session.writerDone)
		session.writeLoop(ctx)
	}()

	session.requestClose(websocket.StatusNormalClosure, "complete")
	waitWriterTerminalSignal(t, session.writerDone, "normally closed writer did not stop")
	frame := &protocol.Heartbeat{Envelope: protocol.NewEnvelope(protocol.TypeHeartbeat, "00000000-0000-4000-8000-000000000002", time.Unix(2, 0).UTC()), Sequence: 2}
	if err := session.sendFrame(ctx, frame); err == nil || err.Error() != "relay WSS writer stopped" {
		t.Fatalf("normal close error=%v, want generic stopped fallback", err)
	}
}

func newWriterTerminalSession(t *testing.T, socket socket) *session {
	t.Helper()
	handler, err := NewHandler(&fakeStateStore{}, DefaultConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return newSession(handler, socket)
}

func waitWriterTerminalSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func waitWriterTerminalError(t *testing.T, result <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal(message)
		return nil
	}
}

func assertWriterTerminalBytesCleared(t *testing.T, data []byte, label string) {
	t.Helper()
	for _, value := range data {
		if value != 0 {
			t.Fatalf("%s request bytes were not cleared", label)
		}
	}
}
