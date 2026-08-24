package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/relay/store"
)

const (
	recoveryPageSize   = 100
	recoveryClaimLimit = 100
	recoveryClaimLease = 2 * time.Minute
)

type githubDelivery struct {
	Number      int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
}

// RunRecoveryScan lists every outcome because a later numeric redelivery can
// share the original stable GUID. The store groups attempts by that GUID and
// monotonically suppresses any group with a successful provider outcome or a
// locally persisted inbound ledger row.
func (s *Service) RunRecoveryScan(ctx context.Context) error {
	end := s.now().UTC()
	start := end.Add(-s.recoveryWindow)
	cursor, err := s.store.StartRecoveryScan(ctx, start, end)
	if err != nil {
		return err
	}
	start, end = cursor.WindowStartedAt, cursor.WindowEndsAt
	seenCursors := map[string]struct{}{}
	if cursor.PageCursor != "" {
		seenCursors[cursor.PageCursor] = struct{}{}
	}
	for page := 0; page < maxProviderPages; page++ {
		deliveries, next, err := s.listAppDeliveries(ctx, cursor.PageCursor)
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if delivery.Number <= 0 || !canonicalUUID(delivery.GUID) || delivery.DeliveredAt.IsZero() ||
				!validIdentifier(delivery.Event, 128) || delivery.Status == "" || len(delivery.Status) > 128 || delivery.StatusCode < 0 || delivery.StatusCode > 599 {
				return &providerError{code: "deliveries.response"}
			}
			if delivery.DeliveredAt.Before(start) || delivery.DeliveredAt.After(end) || !recoveryEvent(delivery.Event) {
				continue
			}
			successful := (delivery.StatusCode >= 200 && delivery.StatusCode <= 399) || delivery.Status == "OK"
			_, err = s.store.DiscoverRecoveryDelivery(ctx, store.RecoveryDelivery{
				DeliveryNumber: delivery.Number, DeliveryID: delivery.GUID,
				OccurredAt: delivery.DeliveredAt, Successful: successful,
			})
			if err != nil {
				return err
			}
		}
		if next == "" {
			return s.store.CompleteRecoveryScan(ctx, cursor)
		}
		if _, exists := seenCursors[next]; exists {
			return &providerError{code: "deliveries.cursor_cycle"}
		}
		seenCursors[next] = struct{}{}
		cursor, err = s.store.AdvanceRecoveryCursor(ctx, cursor, next)
		if err != nil {
			return err
		}
	}
	return &providerError{code: "deliveries.pages"}
}

// RunRedeliveryBatch requests a redelivery for each fenced claim. A provider
// 202 only schedules another observation; it never marks a group recovered.
func (s *Service) RunRedeliveryBatch(ctx context.Context) error {
	claims, err := s.store.ClaimRecovery(ctx, recoveryClaimLimit, recoveryClaimLease)
	if err != nil {
		return err
	}
	var result error
	for _, claim := range claims {
		code := "github.redelivery_accepted"
		if err := s.redeliverAppDelivery(ctx, claim.DeliveryNumber); err != nil {
			code = "github.unavailable"
			result = errors.Join(result, err)
		}
		next := s.now().UTC().Add(recoveryBackoff(claim.Attempts))
		if err := s.store.RecordRecoveryAttempt(ctx, claim, next, code); err != nil && !errors.Is(err, store.ErrConflict) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Service) listAppDeliveries(ctx context.Context, cursor string) ([]githubDelivery, string, error) {
	query := url.Values{"per_page": {strconv.Itoa(recoveryPageSize)}}
	if cursor != "" {
		if len(cursor) > 1024 {
			return nil, "", &providerError{code: "deliveries.cursor"}
		}
		query.Set("cursor", cursor)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIOrigin+"/app/hook/deliveries?"+query.Encode(), nil)
	if err != nil {
		return nil, "", &providerError{code: "deliveries.request"}
	}
	jwt, err := appJWT(s.githubAppID, s.githubPrivateKey, s.now().UTC(), s.random)
	if err != nil {
		return nil, "", &providerError{code: "authentication"}
	}
	defer clear(jwt)
	request.Header.Set("Authorization", "Bearer "+string(jwt))
	setGitHubHeaders(request)
	response, body, err := s.doBounded(request, http.StatusOK)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		clear(body)
		return nil, "", err
	}
	defer clear(body)
	var deliveries []githubDelivery
	if err := decodeProviderJSON(body, &deliveries); err != nil || len(deliveries) > recoveryPageSize {
		return nil, "", &providerError{code: "deliveries.response"}
	}
	next, err := parseNextDeliveryCursor(response.Header.Values("Link"))
	if err != nil {
		return nil, "", err
	}
	return deliveries, next, nil
}

func (s *Service) redeliverAppDelivery(ctx context.Context, deliveryNumber int64) error {
	if deliveryNumber <= 0 {
		return store.ErrInvalid
	}
	endpoint := githubAPIOrigin + "/app/hook/deliveries/" + strconv.FormatInt(deliveryNumber, 10) + "/attempts"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return &providerError{code: "redelivery.request"}
	}
	jwt, err := appJWT(s.githubAppID, s.githubPrivateKey, s.now().UTC(), s.random)
	if err != nil {
		return &providerError{code: "authentication"}
	}
	defer clear(jwt)
	request.Header.Set("Authorization", "Bearer "+string(jwt))
	setGitHubHeaders(request)
	response, body, err := s.doBounded(request, http.StatusAccepted)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	clear(body)
	return err
}

func parseNextDeliveryCursor(linkHeaders []string) (string, error) {
	if len(linkHeaders) == 0 {
		return "", nil
	}
	if len(linkHeaders) != 1 || len(linkHeaders[0]) > 8192 {
		return "", &providerError{code: "deliveries.link"}
	}
	var next string
	for _, part := range strings.Split(linkHeaders[0], ",") {
		sections := strings.Split(strings.TrimSpace(part), ";")
		relation := ""
		for _, parameter := range sections[1:] {
			parameter = strings.TrimSpace(parameter)
			if strings.HasPrefix(parameter, "rel=") {
				if relation != "" {
					return "", &providerError{code: "deliveries.link"}
				}
				relation = parameter
			}
		}
		if relation != `rel="next"` {
			continue
		}
		if next != "" {
			return "", &providerError{code: "deliveries.link"}
		}
		target := strings.TrimSpace(sections[0])
		if len(target) < 2 || target[0] != '<' || target[len(target)-1] != '>' {
			return "", &providerError{code: "deliveries.link"}
		}
		parsed, err := url.Parse(target[1 : len(target)-1])
		if err != nil || parsed.Scheme != "https" || parsed.Host != "api.github.com" || parsed.User != nil || parsed.Path != "/app/hook/deliveries" || parsed.EscapedPath() != "/app/hook/deliveries" || parsed.Fragment != "" {
			return "", &providerError{code: "deliveries.link"}
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return "", &providerError{code: "deliveries.link"}
		}
		if len(query) != 2 || len(query["cursor"]) != 1 || len(query["per_page"]) != 1 || query.Get("per_page") != strconv.Itoa(recoveryPageSize) || query.Get("cursor") == "" || len(query.Get("cursor")) > 1024 {
			return "", &providerError{code: "deliveries.link"}
		}
		next = query.Get("cursor")
	}
	return next, nil
}

func recoveryEvent(event string) bool {
	switch event {
	case "push", "installation", "installation_repositories":
		return true
	default:
		return false
	}
}

func recoveryBackoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 7 {
		attempts = 7
	}
	delay := 30 * time.Second * time.Duration(1<<attempts)
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}
