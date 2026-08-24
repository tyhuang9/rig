package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/relay/store"
)

type webhookStore struct {
	fakeStore
	batch         store.AccessEventBatchInput
	pushError     error
	ignoredReason string
	ignoredError  error
	storeCalls    int
}

func (f *webhookStore) PushIgnoredDelivery(_ context.Context, _ string, reason string, _ time.Time) (bool, error) {
	f.storeCalls++
	f.ignoredReason = reason
	return false, f.ignoredError
}

func (f *webhookStore) AccessRoutes(_ context.Context, _ int64, repositoryID int64) ([]string, error) {
	f.storeCalls++
	if repositoryID == 2 {
		return []string{"11111111-1111-4111-8111-111111111111"}, nil
	}
	return []string{"22222222-2222-4222-8222-222222222222"}, nil
}

func (f *webhookStore) PushAccessEvents(_ context.Context, batch store.AccessEventBatchInput) (store.AccessPushResult, error) {
	f.storeCalls++
	f.batch = batch
	return store.AccessPushResult{}, f.pushError
}

func (f *webhookStore) SourceRoutes(context.Context, int64, int64, string) ([]store.SourceRoute, error) {
	f.storeCalls++
	return nil, nil
}

func (f *webhookStore) PushSourceEvent(context.Context, store.SourceEvent, []store.SourceRoute) (store.SourcePushResult, error) {
	f.storeCalls++
	return store.SourcePushResult{}, nil
}

func TestWebhookAcceptsDocumentedExtraFieldsAndPersistsMultiRepositoryRemovalAtomically(t *testing.T) {
	persistence := &webhookStore{}
	s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider call"); return nil, nil }), time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	body := []byte(`{"action":"removed","installation":{"id":1},"repositories_added":[],"repositories_removed":[{"id":2,"name":"a"},{"id":3,"name":"b"}],"repository_selection":"selected","sender":{"id":7},"organization":{"id":8}}`)
	recorder := performWebhook(t, s, "installation_repositories", body)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(persistence.batch.Events) != 2 || persistence.batch.Events[0].RepositoryID != 2 || persistence.batch.Events[1].RepositoryID != 3 {
		t.Fatalf("batch=%+v", persistence.batch)
	}
	for _, event := range persistence.batch.Events {
		if !event.RemoveAccess || event.ChangeCode != "repository.removed" || len(event.Routes) != 1 {
			t.Fatalf("event=%+v", event)
		}
	}
}

func TestWebhookFailsClosedUntilDurableBatchCompletes(t *testing.T) {
	persistence := &webhookStore{pushError: errors.New("database unavailable")}
	s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }), time.Now().UTC())
	body := []byte(`{"action":"removed","installation":{"id":1},"repositories_added":[],"repositories_removed":[{"id":2}],"repository_selection":"selected"}`)
	recorder := performWebhook(t, s, "installation_repositories", body)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWebhookRejectsInvalidPushAsPermanentEventError(t *testing.T) {
	s := newEnrollmentTestService(t, &webhookStore{}, fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }), time.Now().UTC())
	body := []byte(`{"installation":{"id":1},"repository":{"id":2},"ref":"refs/tags/v1","after":"not-a-sha","deleted":false,"sender":{"id":7}}`)
	recorder := performWebhook(t, s, "push", body)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWebhookSignatureMatchesGitHubOfficialVector(t *testing.T) {
	if !verifyWebhookSignature([]byte("It's a Secret to Everybody"), []byte("Hello, World!"), "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17") {
		t.Fatal("official GitHub HMAC-SHA256 vector did not verify")
	}
}

func TestWebhookRejectsNoncanonicalOrInvalidHMACBeforeStoreAccess(t *testing.T) {
	original := []byte(`{"zen":"keep it logically awesome"}`)
	mac := hmac.New(sha256.New, bytes.Repeat([]byte{2}, 32))
	_, _ = mac.Write(original)
	digest := hex.EncodeToString(mac.Sum(nil))
	tests := []struct {
		name       string
		body       []byte
		signatures []string
		status     int
	}{
		{name: "body mutated after signing", body: []byte(`{"zen":"keep it logically awesomE"}`), signatures: []string{"sha256=" + digest}, status: http.StatusUnauthorized},
		{name: "uppercase hex", body: original, signatures: []string{"sha256=" + strings.ToUpper(digest)}, status: http.StatusUnauthorized},
		{name: "wrong length", body: original, signatures: []string{"sha256=" + digest[:62]}, status: http.StatusUnauthorized},
		{name: "wrong prefix", body: original, signatures: []string{"SHA256=" + digest}, status: http.StatusUnauthorized},
		{name: "duplicate signature header", body: original, signatures: []string{"sha256=" + digest, "sha256=" + digest}, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persistence := &webhookStore{}
			s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider call"); return nil, nil }), time.Now().UTC())
			request := httptest.NewRequest(http.MethodPost, "/v1/github/webhook", bytes.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-GitHub-Delivery", "66666666-6666-4666-8666-666666666666")
			request.Header.Set("X-GitHub-Event", "ping")
			for _, signature := range test.signatures {
				request.Header.Add("X-Hub-Signature-256", signature)
			}
			recorder := httptest.NewRecorder()
			s.Handler().ServeHTTP(recorder, request)
			if recorder.Code != test.status || persistence.storeCalls != 0 {
				t.Fatalf("status=%d store calls=%d", recorder.Code, persistence.storeCalls)
			}
		})
	}
}

func TestWebhookDurablyRecordsDeletedBranchWithoutDesiredState(t *testing.T) {
	persistence := &webhookStore{}
	s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }), time.Now().UTC())
	body := []byte(`{"installation":{"id":1},"repository":{"id":2},"ref":"refs/heads/old","after":"0000000000000000000000000000000000000000","deleted":true}`)
	recorder := performWebhook(t, s, "push", body)
	if recorder.Code != http.StatusNoContent || persistence.ignoredReason != "push.deleted" {
		t.Fatalf("status=%d reason=%q body=%s", recorder.Code, persistence.ignoredReason, recorder.Body.String())
	}
}

func TestWebhookDurablyIgnoresValidUntrackedRefAndFutureAction(t *testing.T) {
	for _, test := range []struct {
		event, body, reason string
	}{
		{event: "push", body: `{"installation":{"id":1},"repository":{"id":2},"ref":"refs/tags/v1","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deleted":false}`, reason: "push.untracked_ref"},
		{event: "installation", body: `{"action":"future_action","installation":{"id":1}}`, reason: "installation.unsupported_action"},
		{event: "installation_repositories", body: `{"action":"future_action","installation":{"id":1}}`, reason: "installation_repositories.unsupported_action"},
	} {
		persistence := &webhookStore{}
		s := newEnrollmentTestService(t, persistence, fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }), time.Now().UTC())
		recorder := performWebhook(t, s, test.event, []byte(test.body))
		if recorder.Code != http.StatusNoContent || persistence.ignoredReason != test.reason {
			t.Fatalf("event=%s status=%d reason=%q", test.event, recorder.Code, persistence.ignoredReason)
		}
	}
}

func TestWebhookRejectsDuplicateJSONKeysAfterValidHMAC(t *testing.T) {
	s := newEnrollmentTestService(t, &webhookStore{}, fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }), time.Now().UTC())
	body := []byte(`{"installation":{"id":1,"id":2},"repository":{"id":2},"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deleted":false}`)
	recorder := performWebhook(t, s, "push", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWebhookRejectsHeaderBodyAndRequiredFieldAmbiguity(t *testing.T) {
	s := newEnrollmentTestService(t, &webhookStore{}, fakeHTTP(func(*http.Request) (*http.Response, error) { return nil, nil }), time.Now().UTC())
	t.Run("duplicate delivery header", func(t *testing.T) {
		body := []byte(`{"zen":"keep it logically awesome"}`)
		request := httptest.NewRequest(http.MethodPost, "/v1/github/webhook", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Add("X-GitHub-Delivery", "66666666-6666-4666-8666-666666666666")
		request.Header.Add("X-GitHub-Delivery", "77777777-7777-4777-8777-777777777777")
		request.Header.Set("X-GitHub-Event", "ping")
		request.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		recorder := performWebhook(t, s, "ping", bytes.Repeat([]byte{'x'}, maxWebhookBody+1))
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	for name, body := range map[string][]byte{
		"missing push deleted":         []byte(`{"installation":{"id":1},"repository":{"id":2},"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		"inconsistent delete":          []byte(`{"installation":{"id":1},"repository":{"id":2},"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":false}`),
		"missing repository arrays":    []byte(`{"action":"removed","installation":{"id":1},"repository_selection":"selected"}`),
		"null repository arrays":       []byte(`{"action":"removed","installation":{"id":1},"repositories_added":null,"repositories_removed":null,"repository_selection":"selected"}`),
		"missing repository selection": []byte(`{"action":"removed","installation":{"id":1},"repositories_added":[],"repositories_removed":[]}`),
		"invalid repository selection": []byte(`{"action":"removed","installation":{"id":1},"repositories_added":[],"repositories_removed":[],"repository_selection":"partial"}`),
	} {
		t.Run(name, func(t *testing.T) {
			event := "push"
			if strings.Contains(name, "repository") {
				event = "installation_repositories"
			}
			recorder := performWebhook(t, s, event, body)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func performWebhook(t *testing.T, s *Service, event string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/github/webhook", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Delivery", "66666666-6666-4666-8666-666666666666")
	request.Header.Set("X-GitHub-Event", event)
	mac := hmac.New(sha256.New, s.webhookSecret)
	_, _ = mac.Write(body)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	return recorder
}
