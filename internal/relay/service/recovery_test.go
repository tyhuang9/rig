package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/store"
)

type recoveryStore struct {
	fakeStore
	cursor     store.RecoveryCursor
	discovered []store.RecoveryDelivery
	claims     []store.RecoveryClaim
	recorded   []string
	completed  bool
	advanced   []string
}

func (f *recoveryStore) StartRecoveryScan(context.Context, time.Time, time.Time) (store.RecoveryCursor, error) {
	return f.cursor, nil
}
func (f *recoveryStore) AdvanceRecoveryCursor(_ context.Context, cursor store.RecoveryCursor, next string) (store.RecoveryCursor, error) {
	f.advanced = append(f.advanced, next)
	cursor.PageCursor = next
	cursor.Fence++
	return cursor, nil
}
func (f *recoveryStore) CompleteRecoveryScan(context.Context, store.RecoveryCursor) error {
	f.completed = true
	return nil
}
func (f *recoveryStore) DiscoverRecoveryDelivery(_ context.Context, item store.RecoveryDelivery) (bool, error) {
	f.discovered = append(f.discovered, item)
	return false, nil
}
func (f *recoveryStore) ClaimRecovery(context.Context, int, time.Duration) ([]store.RecoveryClaim, error) {
	return f.claims, nil
}
func (f *recoveryStore) RecordRecoveryAttempt(_ context.Context, _ store.RecoveryClaim, _ time.Time, code string) error {
	f.recorded = append(f.recorded, code)
	return nil
}

func TestRecoveryScanUsesDurableWindowAndListsAllOutcomesAcrossCursorPages(t *testing.T) {
	durableStart := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	durableEnd := durableStart.Add(time.Hour)
	persistence := &recoveryStore{cursor: store.RecoveryCursor{
		ScanID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Fence: 1,
		WindowStartedAt: durableStart, WindowEndsAt: durableEnd,
	}}
	calls := 0
	transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Query().Has("status") {
			t.Fatal("delivery listing filtered failures instead of listing all outcomes")
		}
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}
		if calls == 1 {
			if request.URL.String() != "https://api.github.com/app/hook/deliveries?per_page=100" {
				t.Fatalf("first page URL=%s", request.URL)
			}
			response.Header.Set("Link", `<https://api.github.com/app/hook/deliveries?cursor=opaque-next&per_page=100>; rel="next"`)
			response.Body = io.NopCloser(strings.NewReader(`[{"id":11,"guid":"11111111-1111-4111-8111-111111111111","delivered_at":"2026-08-24T10:30:00Z","status":"Internal Server Error","status_code":500,"event":"push"},{"id":12,"guid":"22222222-2222-4222-8222-222222222222","delivered_at":"2026-08-24T11:30:00Z","status":"Internal Server Error","status_code":500,"event":"push"}]`))
		} else {
			if request.URL.Query().Get("cursor") != "opaque-next" {
				t.Fatalf("cursor=%q", request.URL.Query().Get("cursor"))
			}
			response.Body = io.NopCloser(strings.NewReader(`[{"id":13,"guid":"11111111-1111-4111-8111-111111111111","delivered_at":"2026-08-24T10:45:00Z","status":"OK","status_code":200,"event":"push"}]`))
		}
		return response, nil
	})
	// The injected clock has advanced; the durable cursor window must remain authoritative.
	s := newEnrollmentTestService(t, persistence, transport, durableEnd.Add(24*time.Hour))
	if err := s.RunRecoveryScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !persistence.completed || len(persistence.advanced) != 1 || len(persistence.discovered) != 2 {
		t.Fatalf("calls=%d completed=%v advanced=%v discovered=%+v", calls, persistence.completed, persistence.advanced, persistence.discovered)
	}
	if persistence.discovered[0].Successful || !persistence.discovered[1].Successful || persistence.discovered[0].DeliveryID != persistence.discovered[1].DeliveryID {
		t.Fatalf("all-outcome GUID grouping inputs=%+v", persistence.discovered)
	}
}

func TestRecoveryRedeliveryAcceptedOnlyRecordsFencedObservation(t *testing.T) {
	persistence := &recoveryStore{claims: []store.RecoveryClaim{{
		RecoveryDelivery: store.RecoveryDelivery{DeliveryNumber: 42, DeliveryID: "11111111-1111-4111-8111-111111111111"},
		ClaimID:          "22222222-2222-4222-8222-222222222222", Fence: 1,
	}}}
	transport := fakeHTTP(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.github.com/app/hook/deliveries/42/attempts" || request.Method != http.MethodPost {
			t.Fatalf("redelivery request=%s %s", request.Method, request.URL)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	s := newEnrollmentTestService(t, persistence, transport, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	if err := s.RunRedeliveryBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(persistence.recorded) != 1 || persistence.recorded[0] != "github.redelivery_accepted" {
		t.Fatalf("recorded=%v", persistence.recorded)
	}
}

func TestRecoveryLinkRejectsOriginAndQueryExpansion(t *testing.T) {
	for _, link := range []string{
		`<https://evil.example/app/hook/deliveries?cursor=x&per_page=100>; rel="next"`,
		`<https://api.github.com/app/hook/deliveries?cursor=x&per_page=100&status=failure>; rel="next"`,
		`<https://api.github.com/app/hook/deliveries?cursor=x&per_page=99>; rel="next"`,
		`<https://api.github.com/app/hook/deliveries?cursor=%zz&per_page=100>; rel="next"`,
		`<https://api.github.com/app/hook/deliveries?cursor=x&per_page=100>; rel="next"; rel="prev"`,
		`<https://api.github.com/app/hook/deliveries?cursor=x&per_page=100>; rel="next", <https://api.github.com/app/hook/deliveries?cursor=y&per_page=100>; rel="next"`,
	} {
		if _, err := parseNextDeliveryCursor([]string{link}); err == nil {
			t.Fatalf("accepted unsafe Link %q", link)
		}
	}
}
