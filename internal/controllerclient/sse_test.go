package controllerclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamJobEventsUsesReplayPositionAndCancels(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 8\nevent: job-event\ndata: {\"id\":8,\"jobId\":\"job\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.StreamJobEvents(ctx, Session{SessionToken: "s"}, "job", 7)
	select {
	case event := <-stream.Events:
		if event.ID != 8 {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event not received")
	}
	select {
	case got := <-seen:
		if got != "7" {
			t.Fatalf("Last-Event-ID=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("request not received")
	}
	cancel()
	select {
	case _, ok := <-stream.Events:
		if ok {
			t.Fatal("stream remains open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close")
	}
}

func TestStreamJobEventsReconnectsWithCursorAndDeduplicates(t *testing.T) {
	requests := make(chan string, 3)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		if count.Add(1) == 1 {
			_, _ = fmt.Fprint(w, "id: 1\ndata: {\"id\":1,\"jobId\":\"job\"}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "id: 1\ndata: {\"id\":1,\"jobId\":\"job\"}\n\nid: 2\ndata: {\"id\":2,\"jobId\":\"job\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.StreamJobEvents(ctx, Session{SessionToken: "s"}, "job", 0)
	for _, want := range []int64{1, 2} {
		select {
		case event := <-stream.Events:
			if event.ID != want {
				t.Fatalf("event id=%d want=%d", event.ID, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("reconnected event not received")
		}
	}
	for _, want := range []string{"0", "1"} {
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("Last-Event-ID=%q want=%q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("reconnect request not observed")
		}
	}
}

func TestStreamJobEventsDoesNotUseWholeBodyTimeout(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(75 * time.Millisecond)
		_, _ = fmt.Fprint(w, "id: 1\ndata: {\"id\":1,\"jobId\":\"job\"}\n\n")
	}))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, HTTPClient: &http.Client{Timeout: 20 * time.Millisecond}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.StreamJobEvents(ctx, Session{SessionToken: "s"}, "job", 0)
	select {
	case event := <-stream.Events:
		if event.ID != 1 {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("SSE inherited whole-body timeout")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestStreamJobEventsReportsTerminalClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	client, _ := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
	stream := client.StreamJobEvents(context.Background(), Session{SessionToken: "s"}, "job", 0)
	select {
	case err := <-stream.Errors:
		if err == nil {
			t.Fatal("terminal 4xx was not reported")
		}
	case <-time.After(time.Second):
		t.Fatal("terminal 4xx was retried forever")
	}
}

func TestStreamJobEventsReportsTerminalDecodeAndLimitErrors(t *testing.T) {
	for _, body := range []string{
		"data: not-json\n\n",
		"data: " + string(make([]byte, maxSSEEventBytes+1)) + "\n\n",
	} {
		body := body
		t.Run("terminal", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			client, _ := New(Options{Endpoint: server.URL, HTTPClient: server.Client()})
			stream := client.StreamJobEvents(context.Background(), Session{SessionToken: "s"}, "job", 0)
			select {
			case err := <-stream.Errors:
				if err == nil {
					t.Fatal("terminal stream error was not reported")
				}
			case <-time.After(time.Second):
				t.Fatal("terminal stream error was retried")
			}
		})
	}
}

func TestSSEReconnectBackoffHasMinimumAndCap(t *testing.T) {
	if got := sseReconnectBackoff(0); got < minSSEReconnectBackoff {
		t.Fatalf("minimum backoff = %s", got)
	}
	if got := sseReconnectBackoff(100); got != maxSSEReconnectBackoff {
		t.Fatalf("capped backoff = %s", got)
	}
}
