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

// JobEventStream replays events after After and reconnects until its context is cancelled.
// Errors is closed after Events. A nil error is never sent.
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
		last := after
		for {
			if ctx.Err() != nil {
				return
			}
			next, err := c.streamConnection(ctx, session, jobID, last, events)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				select {
				case failures <- err:
				case <-ctx.Done():
				}
				return
			}
			if next > last {
				last = next
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	return &JobEventStream{Events: events, Errors: failures}
}

func (c *Client) streamConnection(ctx context.Context, session Session, jobID string, after int64, output chan<- apicontract.JobEvent) (int64, error) {
	q := url.Values{"after": []string{strconv.FormatInt(after, 10)}}
	response, err := c.request(ctx, "streamJobEvents", &session, nil, false, "", q, jobID)
	if err != nil {
		return after, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return after, decodeProblem(response)
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return after, errors.New("hostd job event stream has invalid content type")
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, int64(maxSSEEventBytes*maxSSEEventsPerConnection)+1))
	scanner.Buffer(make([]byte, 4096), maxSSEEventBytes)
	var data strings.Builder
	var id int64 = after
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
		if event.ID > id {
			id = event.ID
		}
		select {
		case output <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
		data.Reset()
		count++
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := emit(); err != nil {
				return id, err
			}
			continue
		}
		if strings.HasPrefix(line, "id:") {
			if parsed, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64); err == nil && parsed > id {
				id = parsed
			}
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
				return id, errors.New("hostd job event stream exceeded event size limit")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return id, fmt.Errorf("read hostd job event stream: %w", err)
	}
	if err := emit(); err != nil {
		return id, err
	}
	return id, nil
}
