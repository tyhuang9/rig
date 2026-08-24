package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

const (
	maxWebhookBody      = 1 << 20
	maxWebhookChanges   = 1000
	maxRoutePushRetries = 3
)

type webhookEnvelope struct {
	Action       string `json:"action"`
	Ref          string `json:"ref"`
	After        string `json:"after"`
	Deleted      *bool  `json:"deleted"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		ID int64 `json:"id"`
	} `json:"repository"`
	RepositoriesAdded []struct {
		ID int64 `json:"id"`
	} `json:"repositories_added"`
	RepositoriesRemoved []struct {
		ID int64 `json:"id"`
	} `json:"repositories_removed"`
	RepositorySelection string `json:"repository_selection"`
}

type normalizedAccess struct {
	installationID int64
	repositoryID   int64
	changeCode     string
	removeAccess   bool
}

func (s *Service) handleWebhook(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeProblem(w, http.StatusMethodNotAllowed, "webhook.method")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(parameters) != 0 || !singleHeader(request.Header, "Content-Type") {
		writeProblem(w, http.StatusUnsupportedMediaType, "webhook.content_type")
		return
	}
	delivery := request.Header.Get("X-GitHub-Delivery")
	event := request.Header.Get("X-GitHub-Event")
	signature := request.Header.Get("X-Hub-Signature-256")
	if !singleHeader(request.Header, "X-GitHub-Delivery") || !singleHeader(request.Header, "X-GitHub-Event") || !singleHeader(request.Header, "X-Hub-Signature-256") ||
		!canonicalUUID(delivery) || !validWebhookEvent(event) {
		writeProblem(w, http.StatusBadRequest, "webhook.headers")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxWebhookBody+1))
	if err != nil || len(body) == 0 || len(body) > maxWebhookBody {
		clear(body)
		writeProblem(w, http.StatusRequestEntityTooLarge, "webhook.body")
		return
	}
	defer clear(body)
	if !verifyWebhookSignature(s.webhookSecret, body, signature) {
		writeProblem(w, http.StatusUnauthorized, "webhook.signature")
		return
	}
	if err := rejectDuplicateKeys(body); err != nil {
		writeProblem(w, http.StatusBadRequest, "webhook.json")
		return
	}
	var payload webhookEnvelope
	if err := json.Unmarshal(body, &payload); err != nil {
		writeProblem(w, http.StatusBadRequest, "webhook.json")
		return
	}
	receivedAt := s.now().UTC()
	switch event {
	case "ping":
		w.WriteHeader(http.StatusNoContent)
		return
	case "push":
		if payload.Installation.ID <= 0 || payload.Repository.ID <= 0 || payload.Deleted == nil || !validGitRef(payload.Ref) || protocol.ValidSHA(payload.After) != nil || *payload.Deleted != (payload.After == strings.Repeat("0", 40)) {
			writeProblem(w, http.StatusUnprocessableEntity, "webhook.event")
			return
		}
		ignoredReason := ""
		if !strings.HasPrefix(payload.Ref, "refs/heads/") {
			ignoredReason = "push.untracked_ref"
		} else if *payload.Deleted {
			ignoredReason = "push.deleted"
		}
		if ignoredReason != "" {
			if _, err := s.store.PushIgnoredDelivery(request.Context(), delivery, ignoredReason, receivedAt); err != nil {
				writeProblem(w, http.StatusServiceUnavailable, "webhook.persist")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := s.persistPush(request.Context(), delivery, receivedAt, payload); err != nil {
			if errors.Is(err, store.ErrInvalid) {
				writeProblem(w, http.StatusUnprocessableEntity, "webhook.event")
				return
			}
			writeProblem(w, http.StatusServiceUnavailable, "webhook.persist")
			return
		}
	case "installation", "installation_repositories":
		changes, ignoredReason, err := normalizeAccess(event, payload)
		if err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, "webhook.event")
			return
		}
		if ignoredReason != "" {
			if _, err := s.store.PushIgnoredDelivery(request.Context(), delivery, ignoredReason, receivedAt); err != nil {
				writeProblem(w, http.StatusServiceUnavailable, "webhook.persist")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := s.persistAccess(request.Context(), delivery, receivedAt, changes); err != nil {
			if errors.Is(err, store.ErrInvalid) {
				writeProblem(w, http.StatusUnprocessableEntity, "webhook.event")
				return
			}
			writeProblem(w, http.StatusServiceUnavailable, "webhook.persist")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) persistPush(ctx context.Context, delivery string, receivedAt time.Time, payload webhookEnvelope) error {
	if payload.Installation.ID <= 0 || payload.Repository.ID <= 0 || protocol.ValidRef(payload.Ref) != nil || protocol.ValidSHA(payload.After) != nil {
		return store.ErrInvalid
	}
	event := store.SourceEvent{DeliveryID: delivery, InstallationID: payload.Installation.ID, RepositoryID: payload.Repository.ID, Ref: payload.Ref, SHA: payload.After, ReceivedAt: receivedAt, ObservedAt: receivedAt}
	for attempt := 0; attempt < maxRoutePushRetries; attempt++ {
		routes, err := s.store.SourceRoutes(ctx, event.InstallationID, event.RepositoryID, event.Ref)
		if err != nil {
			return err
		}
		if _, err = s.store.PushSourceEvent(ctx, event, routes); !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return store.ErrConflict
}

func normalizeAccess(event string, payload webhookEnvelope) ([]normalizedAccess, string, error) {
	if payload.Installation.ID <= 0 {
		return nil, "", store.ErrInvalid
	}
	if event == "installation" {
		switch payload.Action {
		case "created":
			return []normalizedAccess{{installationID: payload.Installation.ID, changeCode: "installation.created"}}, "", nil
		case "deleted", "suspend":
			return []normalizedAccess{{installationID: payload.Installation.ID, changeCode: "installation.removed", removeAccess: true}}, "", nil
		case "unsuspend":
			return []normalizedAccess{{installationID: payload.Installation.ID, changeCode: "installation.restored"}}, "", nil
		case "new_permissions_accepted":
			return []normalizedAccess{{installationID: payload.Installation.ID, changeCode: "installation.permissions_updated"}}, "", nil
		default:
			if !validIdentifier(payload.Action, 128) {
				return nil, "", store.ErrInvalid
			}
			return nil, "installation.unsupported_action", nil
		}
	}
	if payload.Action != "added" && payload.Action != "removed" {
		if !validIdentifier(payload.Action, 128) {
			return nil, "", store.ErrInvalid
		}
		return nil, "installation_repositories.unsupported_action", nil
	}
	if payload.RepositoriesAdded == nil || payload.RepositoriesRemoved == nil || (payload.RepositorySelection != "all" && payload.RepositorySelection != "selected") ||
		(payload.Action == "added" && len(payload.RepositoriesRemoved) != 0) || (payload.Action == "removed" && len(payload.RepositoriesAdded) != 0) || len(payload.RepositoriesAdded)+len(payload.RepositoriesRemoved) > maxWebhookChanges {
		return nil, "", store.ErrInvalid
	}
	if len(payload.RepositoriesAdded)+len(payload.RepositoriesRemoved) == 0 {
		return []normalizedAccess{{installationID: payload.Installation.ID, changeCode: "installation.repositories_reconciled"}}, "", nil
	}
	seen := map[int64]struct{}{}
	changes := make([]normalizedAccess, 0, len(payload.RepositoriesAdded)+len(payload.RepositoriesRemoved))
	for _, repository := range payload.RepositoriesAdded {
		if repository.ID <= 0 {
			return nil, "", store.ErrInvalid
		}
		if _, exists := seen[repository.ID]; exists {
			return nil, "", store.ErrInvalid
		}
		seen[repository.ID] = struct{}{}
		changes = append(changes, normalizedAccess{installationID: payload.Installation.ID, repositoryID: repository.ID, changeCode: "repository.added"})
	}
	for _, repository := range payload.RepositoriesRemoved {
		if repository.ID <= 0 {
			return nil, "", store.ErrInvalid
		}
		if _, exists := seen[repository.ID]; exists {
			return nil, "", store.ErrInvalid
		}
		seen[repository.ID] = struct{}{}
		changes = append(changes, normalizedAccess{installationID: payload.Installation.ID, repositoryID: repository.ID, changeCode: "repository.removed", removeAccess: true})
	}
	return changes, "", nil
}

func validGitRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || len(ref) <= len("refs/") || len(ref) > 255 {
		return false
	}
	tail := strings.TrimPrefix(ref, "refs/")
	if strings.HasPrefix(tail, "/") || strings.HasSuffix(tail, "/") || strings.HasSuffix(tail, ".") || strings.Contains(tail, "//") || strings.Contains(tail, "..") || strings.Contains(tail, "@{") || tail == "@" {
		return false
	}
	for _, part := range strings.Split(tail, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	for _, r := range tail {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	return true
}

func (s *Service) persistAccess(ctx context.Context, delivery string, receivedAt time.Time, changes []normalizedAccess) error {
	for attempt := 0; attempt < maxRoutePushRetries; attempt++ {
		batch := store.AccessEventBatchInput{DeliveryID: delivery, ReceivedAt: receivedAt, Events: make([]store.AccessEventBatchItem, 0, len(changes))}
		for _, change := range changes {
			controllerIDs, err := s.store.AccessRoutes(ctx, change.installationID, change.repositoryID)
			if err != nil {
				return err
			}
			routes := make([]store.AccessRoute, 0, len(controllerIDs))
			for _, controllerID := range controllerIDs {
				eventID, err := newRandomUUID(s.random)
				if err != nil {
					return err
				}
				routes = append(routes, store.AccessRoute{EventID: eventID, ControllerID: controllerID})
			}
			batch.Events = append(batch.Events, store.AccessEventBatchItem{InstallationID: change.installationID, RepositoryID: change.repositoryID, ChangeCode: change.changeCode, ObservedAt: receivedAt, RemoveAccess: change.removeAccess, Routes: routes})
		}
		if _, err := s.store.PushAccessEvents(ctx, batch); !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return store.ErrConflict
}

func verifyWebhookSignature(secret, body []byte, header string) bool {
	if len(header) != len("sha256=")+sha256.Size*2 || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided := make([]byte, sha256.Size)
	if _, err := hex.Decode(provided, []byte(header[len("sha256="):])); err != nil || hex.EncodeToString(provided) != header[len("sha256="):] {
		clear(provided)
		return false
	}
	defer clear(provided)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	defer clear(expected)
	return hmac.Equal(provided, expected)
}

func singleHeader(header http.Header, name string) bool {
	values := header.Values(name)
	return len(values) == 1 && values[0] != "" && !strings.ContainsAny(values[0], "\r\n,")
}

func validWebhookEvent(event string) bool {
	switch event {
	case "ping", "push", "installation", "installation_repositories":
		return true
	default:
		return false
	}
}

func canonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
