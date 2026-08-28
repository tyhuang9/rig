package controllerclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
)

const maxSSEEventBytes = 64 << 10
const maxSSEEventsPerConnection = 1000
const minSSEReconnectBackoff = 250 * time.Millisecond
const maxSSEReconnectBackoff = 5 * time.Second

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
	go func() {
		defer close(events)
		defer close(failures)
		last, attempts := after, 0
		for {
			if ctx.Err() != nil {
				return
			}
			next, terminal, err := c.streamConnection(ctx, session, jobID, last, events)
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
			progressed := next > last
			if progressed {
				last = next
				attempts = 0
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(sseReconnectBackoff(attempts)):
			}
			if !progressed {
				attempts++
			}
		}
	}()
	return &JobEventStream{Events: events, Errors: failures}
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
	streamClient := *c.httpClient
	streamClient.Timeout = 0 // An SSE body is intentionally long-lived.
	response, err := c.requestWithClient(ctx, &streamClient, "streamJobEvents", &session, nil, false, "", q, jobID)
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
		case <-ctx.Done():
			return ctx.Err()
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
