package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
)

const (
	httpConnectionHeadroom = 64
	serviceConcurrency     = 64
)

// admissionTracker makes the stop-admitting transition atomic with respect to
// WaitGroup additions. Wait is only valid after StopAdmissions.
type admissionTracker struct {
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

func (a *admissionTracker) Admit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return false
	}
	a.wg.Add(1)
	return true
}

func (a *admissionTracker) Done() { a.wg.Done() }

func (a *admissionTracker) StopAdmissions() {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
}

func (a *admissionTracker) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type listenerStats struct {
	active     atomic.Int64
	saturation atomic.Uint64
	capacity   int64
}

// cappedListener acquires capacity before Accept, bounding accepted sockets
// (including TLS handshakes and keep-alive connections) rather than merely
// bounding concurrently executing handlers.
type cappedListener struct {
	net.Listener
	permits chan struct{}
	done    chan struct{}
	once    sync.Once
	stats   *listenerStats
}

func newCappedListener(base net.Listener, capacity int, stats *listenerStats) net.Listener {
	if stats != nil {
		stats.capacity = int64(capacity)
	}
	return &cappedListener{Listener: base, permits: make(chan struct{}, capacity), done: make(chan struct{}), stats: stats}
}

func (l *cappedListener) Accept() (net.Conn, error) {
	select {
	case l.permits <- struct{}{}:
	default:
		if l.stats != nil {
			l.stats.saturation.Add(1)
		}
		select {
		case l.permits <- struct{}{}:
		case <-l.done:
			return nil, net.ErrClosed
		}
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.permits
		return nil, err
	}
	if l.stats != nil {
		l.stats.active.Add(1)
	}
	return &limitedConn{Conn: conn, release: func() {
		<-l.permits
		if l.stats != nil {
			l.stats.active.Add(-1)
		}
	}}, nil
}

func (l *cappedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

var errAdmissionsStopped = errors.New("relay admissions stopped")
