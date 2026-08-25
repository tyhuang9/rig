package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type deadlineObservationHandler struct{ observed chan bool }

func (h deadlineObservationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, bounded := r.Context().Deadline()
	h.observed <- bounded
	w.WriteHeader(http.StatusTeapot)
}

func TestRealTCPDeadlineExemptionInvalidConnectAndKeepAlive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepting atomic.Bool
	accepting.Store(true)
	wssObserved := make(chan bool, 16)
	serviceObserved := make(chan bool, 1)
	handler := &relayHTTPHandler{
		service: deadlineObservationHandler{serviceObserved}, websocket: deadlineObservationHandler{wssObserved},
		store: &readinessStub{}, accepting: &accepting, metrics: &metrics{}, readTimeout: time.Second,
		writeTimeout: time.Second, serviceSlots: make(chan struct{}, serviceConcurrency),
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second, Protocols: protocols, ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
		return context.WithValue(ctx, connectionContextKey{}, conn)
	}}
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()

	request := func(raw string) (*http.Response, net.Conn) {
		conn, dialErr := net.Dial("tcp", listener.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		if _, writeErr := fmt.Fprint(conn, raw); writeErr != nil {
			t.Fatal(writeErr)
		}
		response, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return response, conn
	}
	valid := "GET /v1/controllers/connect HTTP/1.1\r\nHost: relay.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Protocol: rig.relay.v1\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\n\r\n"
	response, conn := request(valid)
	response.Body.Close()
	conn.Close()
	if bounded := <-wssObserved; bounded {
		t.Fatal("valid WSS request received HTTP deadline")
	}
	for _, header := range []string{
		"Sec-WebSocket-Version: 12\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\n",
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\n",
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: c2hvcnQ=\r\n",
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\n",
	} {
		invalidUpgrade := "GET /v1/controllers/connect HTTP/1.1\r\nHost: relay.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Protocol: rig.relay.v1\r\n" + header + "\r\n"
		response, conn = request(invalidUpgrade)
		response.Body.Close()
		conn.Close()
		if bounded := <-wssObserved; !bounded {
			t.Fatalf("invalid upgrade bypassed HTTP deadline: %q", header)
		}
	}
	invalid := "GET /v1/controllers/connect HTTP/1.1\r\nHost: relay.test\r\n\r\n"
	response, conn = request(invalid)
	response.Body.Close()
	conn.Close()
	if bounded := <-wssObserved; !bounded {
		t.Fatal("invalid connect request bypassed HTTP deadline")
	}
	response, conn = request("GET /v1/enrollments/status HTTP/1.1\r\nHost: relay.test\r\n\r\n")
	response.Body.Close()
	conn.Close()
	if bounded := <-serviceObserved; !bounded {
		t.Fatal("service request missing bounded context")
	}

	keepalive, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(keepalive)
	for i := 0; i < 2; i++ {
		if _, err = fmt.Fprint(keepalive, "GET /healthz HTTP/1.1\r\nHost: relay.test\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		response, err = http.ReadResponse(reader, nil)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("keepalive %d response=%v err=%v", i, response, err)
		}
		response.Body.Close()
	}
	keepalive.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestDeadlineSetupFailureDoesNotDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		writer deadlineSetupWriter
	}{
		{"read", &unsupportedDeadlineWriter{header: make(http.Header)}},
		{"write", &writeDeadlineFailureWriter{unsupportedDeadlineWriter: unsupportedDeadlineWriter{header: make(http.Header)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var accepting atomic.Bool
			accepting.Store(true)
			dispatched := atomic.Bool{}
			handler := &relayHTTPHandler{service: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched.Store(true) }), websocket: http.NotFoundHandler(), store: &readinessStub{}, accepting: &accepting, metrics: &metrics{}, readTimeout: time.Second, writeTimeout: time.Second, serviceSlots: make(chan struct{}, serviceConcurrency)}
			serverSide, clientSide := net.Pipe()
			defer clientSide.Close()
			conn := &closeObservingConn{Conn: serverSide}
			request := &http.Request{Method: http.MethodGet, URL: mustURLForTest("http://relay.test/v1/test"), Header: make(http.Header)}
			request = request.WithContext(context.WithValue(request.Context(), connectionContextKey{}, net.Conn(conn)))
			handler.ServeHTTP(test.writer, request)
			if test.writer.Status() != http.StatusServiceUnavailable || dispatched.Load() || test.writer.Header().Get("Cache-Control") != "no-store" || !conn.closed.Load() {
				t.Fatalf("status=%d dispatched=%v closed=%v headers=%v", test.writer.Status(), dispatched.Load(), conn.closed.Load(), test.writer.Header())
			}
			if test.name == "write" && test.writer.(*writeDeadlineFailureWriter).zeroRead.Load() != 0 {
				t.Fatal("read deadline was cleared after write deadline setup failed")
			}
		})
	}
}

func TestRealTCPBodyIgnoringRoutesReleaseIncompleteBodies(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepting atomic.Bool
	accepting.Store(true)
	handler := &relayHTTPHandler{service: http.NotFoundHandler(), websocket: http.NotFoundHandler(), store: &readinessStub{}, accepting: &accepting, metrics: &metrics{}, readTimeout: 75 * time.Millisecond, writeTimeout: time.Second, serviceSlots: make(chan struct{}, serviceConcurrency)}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	closed := make(chan struct{}, 2)
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: time.Second, Protocols: protocols,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, connectionContextKey{}, conn)
		},
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				closed <- struct{}{}
			}
		},
	}
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()
	for _, bodyFraming := range []string{
		"Content-Length: 4\r\n\r\nx",
		"Transfer-Encoding: chunked\r\n\r\n1\r\nx\r\n",
	} {
		conn, dialErr := net.Dial("tcp", listener.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		if _, writeErr := fmt.Fprint(conn, "GET /healthz HTTP/1.1\r\nHost: relay.test\r\n"+bodyFraming); writeErr != nil {
			t.Fatal(writeErr)
		}
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatalf("body-ignoring request exceeded read timeout: %q", bodyFraming)
		}
		_ = conn.Close()
	}
	handler.StopAdmissions()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), time.Second)
	if waitErr := handler.Wait(drainCtx); waitErr != nil {
		drainCancel()
		t.Fatalf("handler did not release after incomplete bodies: %v", waitErr)
	}
	drainCancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	<-done
}

func TestRealTCPSlowBodyIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepting atomic.Bool
	accepting.Store(true)
	readResult := make(chan error, 1)
	handler := &relayHTTPHandler{service: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readResult <- err
		w.WriteHeader(http.StatusNoContent)
	}), websocket: http.NotFoundHandler(), store: &readinessStub{}, accepting: &accepting, metrics: &metrics{}, readTimeout: 50 * time.Millisecond, writeTimeout: time.Second, serviceSlots: make(chan struct{}, serviceConcurrency)}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second, Protocols: protocols, ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
		return context.WithValue(ctx, connectionContextKey{}, conn)
	}}
	done := make(chan struct{})
	go func() { _ = server.Serve(listener); close(done) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprint(conn, "POST /v1/enrollments HTTP/1.1\r\nHost: relay.test\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-readResult:
		if err == nil {
			t.Fatal("slow request body was not interrupted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow request body exceeded bound")
	}
	_ = conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	<-done
}

type unsupportedDeadlineWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

type deadlineSetupWriter interface {
	http.ResponseWriter
	Status() int
}

type writeDeadlineFailureWriter struct {
	unsupportedDeadlineWriter
	zeroRead atomic.Int64
}

func (w *writeDeadlineFailureWriter) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.zeroRead.Add(1)
	}
	return nil
}

func (*writeDeadlineFailureWriter) SetWriteDeadline(time.Time) error {
	return fmt.Errorf("write deadline unsupported")
}

type closeObservingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeObservingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func (w *unsupportedDeadlineWriter) Header() http.Header             { return w.header }
func (w *unsupportedDeadlineWriter) WriteHeader(status int)          { w.status = status }
func (w *unsupportedDeadlineWriter) Write(value []byte) (int, error) { return w.body.Write(value) }
func (w *unsupportedDeadlineWriter) Status() int                     { return w.status }

func mustURLForTest(raw string) *url.URL {
	value, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return value
}
