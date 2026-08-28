package controllerclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
)

const maxSSEEventBytes = 64 << 10
const maxSSEEventsPerConnection = 1000
const minSSEReconnectBackoff = 250 * time.Millisecond
const maxSSEReconnectBackoff = 5 * time.Second
const defaultSSEConnectionTimeout = 35 * time.Second
const healthySSEConnectionDuration = 5 * time.Second

var sseStreamSequence atomic.Uint64

// JobEventStream replays events after After and reconnects until its context is cancelled.
// Errors reports terminal response, decode, and limit failures. It closes when
// Events closes; a nil error is never sent.
type JobEventStream struct {
	Events <-chan apicontract.JobEvent
	Errors <-chan error
}

// StreamJobEvents opens a cancellable replaying SSE stream. after is sent as both
// a query parameter for the first connection and Last-Event-ID on subsequent requests.
func (c *Client) StreamJobEvents(ctx context.Context, session Session, jobID string, after int64) *JobEventStream {
	events := make(chan apicontract.JobEvent)
	failures := make(chan error, 1)
	reconnectSeed := jobID + ":" + strconv.FormatUint(sseStreamSequence.Add(1), 10)
	go func() {
		defer close(failures)
		defer close(events)
		last, attempts := after, 0
		for {
			if ctx.Err() != nil {
				return
			}
			started := time.Now()
			next, terminal, err := c.streamConnection(ctx, session, jobID, last, events)
			connectionDuration := time.Since(started)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if terminal {
					select {
					case failures <- err:
					case <-ctx.Done():
					}
					return
				}
			}
			if next > last {
				last = next
			}
			attempts = nextSSEReconnectAttempt(attempts, connectionDuration)
			timer := time.NewTimer(sseReconnectDelay(attempts, reconnectSeed))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return &JobEventStream{Events: events, Errors: failures}
}

func nextSSEReconnectAttempt(attempt int, connectionDuration time.Duration) int {
	if connectionDuration >= healthySSEConnectionDuration {
		return 0
	}
	if attempt < 0 {
		return 1
	}
	return attempt + 1
}

// sseReconnectDelay applies deterministic per-job jitter so multiple followers
// do not reconnect in lockstep while keeping tests and operational behavior
// reproducible.
func sseReconnectDelay(attempt int, jobID string) time.Duration {
	base := sseReconnectBackoff(attempt)
	var hash uint32 = 2166136261
	for _, b := range []byte(jobID + ":" + strconv.Itoa(attempt)) {
		hash ^= uint32(b)
		hash *= 16777619
	}
	// A 0.8x-1.2x window avoids herds without weakening the minimum materially.
	factor := 800 + int(hash%401)
	return time.Duration(int64(base) * int64(factor) / 1000)
}

func newSSEHTTPClient(base *http.Client, connectionTimeout time.Duration) *http.Client {
	streamClient := *base
	streamClient.Timeout = 0
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if typed, ok := transport.(*http.Transport); ok {
		clone := typed.Clone()
		if clone.DialContext == nil {
			clone.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		}
		if clone.TLSHandshakeTimeout <= 0 || clone.TLSHandshakeTimeout > 5*time.Second {
			clone.TLSHandshakeTimeout = 5 * time.Second
		}
		headerTimeout := 10 * time.Second
		if connectionTimeout < headerTimeout {
			headerTimeout = connectionTimeout
		}
		if clone.ResponseHeaderTimeout <= 0 || clone.ResponseHeaderTimeout > headerTimeout {
			clone.ResponseHeaderTimeout = headerTimeout
		}
		if clone.MaxConnsPerHost <= 0 || clone.MaxConnsPerHost > 4 {
			clone.MaxConnsPerHost = 4
		}
		if clone.MaxIdleConnsPerHost <= 0 || clone.MaxIdleConnsPerHost > 2 {
			clone.MaxIdleConnsPerHost = 2
		}
		streamClient.Transport = clone
	}
	return &streamClient
}

func sseReconnectBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := minSSEReconnectBackoff
	for attempt > 0 && delay < maxSSEReconnectBackoff {
		delay *= 2
		attempt--
	}
	if delay > maxSSEReconnectBackoff {
		return maxSSEReconnectBackoff
	}
	return delay
}

func (c *Client) streamConnection(ctx context.Context, session Session, jobID string, after int64, output chan<- apicontract.JobEvent) (int64, bool, error) {
	q := url.Values{"after": []string{strconv.FormatInt(after, 10)}}
	connectionCtx, cancel := context.WithTimeout(ctx, c.streamConnectionTimeout)
	defer cancel()
	response, err := c.requestWithClient(connectionCtx, c.streamClient, "streamJobEvents", &session, nil, false, "", q, jobID)
	if err != nil {
		return after, false, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return after, response.StatusCode >= 400 && response.StatusCode < 500, decodeProblem(response)
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return after, true, errors.New("hostd job event stream has invalid content type")
	}
	limited := &io.LimitedReader{R: response.Body, N: int64(maxSSEEventBytes*maxSSEEventsPerConnection) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxSSEEventBytes)
	var data strings.Builder
	lastDelivered := after
	count := 0
	emit := func() error {
		if data.Len() == 0 {
			return nil
		}
		if count >= maxSSEEventsPerConnection {
			return errors.New("hostd job event stream exceeded event limit")
		}
		if data.Len() > maxSSEEventBytes {
			return errors.New("hostd job event stream exceeded event size limit")
		}
		var event apicontract.JobEvent
		if err := json.Unmarshal([]byte(data.String()), &event); err != nil {
			return fmt.Errorf("decode hostd job event: %w", err)
		}
		if event.ID <= lastDelivered {
			data.Reset()
			count++
			return nil
		}
		select {
		case output <- event:
		case <-connectionCtx.Done():
			return connectionCtx.Err()
		}
		lastDelivered = event.ID
		data.Reset()
		count++
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := emit(); err != nil {
				return lastDelivered, true, err
			}
			continue
		}
		if strings.HasPrefix(line, "id:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(part, " ") {
				part = part[1:]
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(part)
			if data.Len() > maxSSEEventBytes {
				return lastDelivered, true, errors.New("hostd job event stream exceeded event size limit")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return lastDelivered, true, errors.New("hostd job event stream exceeded event size limit")
		}
		return lastDelivered, false, fmt.Errorf("read hostd job event stream: %w", err)
	}
	if limited.N == 0 {
		return lastDelivered, true, errors.New("hostd job event stream exceeded connection limit")
	}
	if err := emit(); err != nil {
		return lastDelivered, true, err
	}
	return lastDelivered, false, nil
}
