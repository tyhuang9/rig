package controllerclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
