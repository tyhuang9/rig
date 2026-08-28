package main

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type channelListener struct {
	connections chan net.Conn
	closed      chan struct{}
	accepts     atomic.Int64
}

func (l *channelListener) Accept() (net.Conn, error) {
	l.accepts.Add(1)
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *channelListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (*channelListener) Addr() net.Addr { return addressStub("127.0.0.1:0") }

func TestCappedListenerAdmitsExactCapacityAndBlocksNextAccept(t *testing.T) {
	base := &channelListener{connections: make(chan net.Conn, 3), closed: make(chan struct{})}
	stats := &listenerStats{}
	listener := newCappedListener(base, 2, stats)
	defer listener.Close()
	clients := make([]net.Conn, 0, 3)
	accept := func() <-chan net.Conn {
		result := make(chan net.Conn, 1)
		go func() { conn, _ := listener.Accept(); result <- conn }()
		server, client := net.Pipe()
		clients = append(clients, client)
		base.connections <- server
		return result
	}
	first := <-accept()
	second := <-accept()
	thirdResult := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); thirdResult <- conn }()
	waitForTest(t, func() bool { return stats.saturation.Load() == 1 })
	if got := base.accepts.Load(); got != 2 {
		t.Fatalf("underlying accepts=%d want=2", got)
	}
	if stats.active.Load() != 2 || stats.capacity != 2 || stats.saturation.Load() != 1 {
		t.Fatalf("stats active=%d capacity=%d saturation=%d", stats.active.Load(), stats.capacity, stats.saturation.Load())
	}
	if err := first.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	clients = append(clients, client)
	base.connections <- server
	select {
	case third := <-thirdResult:
		if third == nil {
			t.Fatal("third connection missing")
		}
		_ = third.Close()
	case <-time.After(time.Second):
		t.Fatal("third accept did not proceed after release")
	}
	_ = second.Close()
	for _, conn := range clients {
		_ = conn.Close()
	}
}

func TestCappedListenerRealTCPExactCapacity(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stats := &listenerStats{}
	listener := newCappedListener(base, 2, stats)
	defer listener.Close()
	accept := func() <-chan net.Conn {
		result := make(chan net.Conn, 1)
		go func() { conn, _ := listener.Accept(); result <- conn }()
		return result
	}
	firstResult := accept()
	client1, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server1 := <-firstResult
	secondResult := accept()
	client2, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server2 := <-secondResult
	thirdResult := accept()
	waitForTest(t, func() bool { return stats.saturation.Load() == 1 })
	client3, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-thirdResult:
		t.Fatal("third connection accepted above hard cap")
	default:
	}
	_ = server1.Close()
	select {
	case server3 := <-thirdResult:
		_ = server3.Close()
	case <-time.After(time.Second):
		t.Fatal("third connection not accepted after capacity release")
	}
	_ = server2.Close()
	_ = client1.Close()
	_ = client2.Close()
	_ = client3.Close()
}

func waitForTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
