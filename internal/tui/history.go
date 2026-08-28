package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/hostd/hostd/internal/secretfile"
)

const (
	historyLimit   = 100
	historyPurpose = "hostd-tui-command-history-v1"
)

type HistoryStore interface {
	Load(context.Context) ([]string, error)
	Save(context.Context, []string) error
	Clear(context.Context) error
}

type HistoryStoreFactory func() (HistoryStore, error)

type protectedHistoryStore struct {
	path string
	mu   sync.Mutex
}

func NewProtectedHistoryStore(path string) HistoryStore {
	return &protectedHistoryStore{path: path}
}

func (s *protectedHistoryStore) Load(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := secretfile.Read(s.path, historyPurpose)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(b)
	var values []string
	if err := json.Unmarshal(b, &values); err != nil {
		return nil, err
	}
	return normalizeHistory(values), nil
}

func (s *protectedHistoryStore) Save(_ context.Context, values []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(normalizeHistory(values))
	if err != nil {
		return err
	}
	defer clear(b)
	return secretfile.Write(s.path, historyPurpose, b)
}

func (s *protectedHistoryStore) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return secretfile.Remove(s.path)
}

func normalizeHistory(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			continue
		}
		out = append(out, value)
	}
	if len(out) > historyLimit {
		out = out[len(out)-historyLimit:]
	}
	return out
}

type memoryHistoryStore struct {
	values []string
}

func (s *memoryHistoryStore) Load(context.Context) ([]string, error) {
	return append([]string(nil), s.values...), nil
}
func (s *memoryHistoryStore) Save(_ context.Context, values []string) error {
	s.values = append([]string(nil), normalizeHistory(values)...)
	return nil
}
func (s *memoryHistoryStore) Clear(context.Context) error { s.values = nil; return nil }
