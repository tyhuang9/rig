package generatedingress

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	statePurpose  = "hostd/generated-ingress/routes/v1"
	stateFilename = "routes.bundle"
	stateVersion  = 1
	maxStateApps  = 64
	maxStateBytes = 48 << 10
)

type routeRecord struct {
	Slot      generatedruntime.Slot            `json:"slot"`
	Endpoints []generatedruntime.RouteEndpoint `json:"endpoints"`
}

type pendingRoute struct {
	AppID    string       `json:"appId"`
	Previous *routeRecord `json:"previous,omitempty"`
	Proposed routeRecord  `json:"proposed"`
}

type routeState struct {
	Version int                    `json:"version"`
	Active  map[string]routeRecord `json:"active"`
	Pending *pendingRoute          `json:"pending,omitempty"`
}

type stateStore struct {
	root string
	path string
}

func newStateStore(dataRoot string) (*stateStore, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || pathsecurity.RejectWindowsNamespace(dataRoot) {
		return nil, errors.New("invalid generated ingress data root")
	}
	root := filepath.Join(dataRoot, "runtime", "generated-ingress")
	if err := secureDirectory(root); err != nil {
		return nil, err
	}
	return &stateStore{root: root, path: filepath.Join(root, stateFilename)}, nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(current) {
			return errors.New("generated ingress data directory is unsafe")
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return os.Chmod(path, 0o700)
}

func (s *stateStore) load() (routeState, error) {
	state := routeState{Version: stateVersion, Active: map[string]routeRecord{}}
	before, err := s.directoryIdentity()
	if err != nil {
		return routeState{}, err
	}
	body, err := secretfile.Read(s.path, statePurpose)
	if errors.Is(err, os.ErrNotExist) {
		if s.sameDirectory(before) != nil {
			return routeState{}, errors.New("generated ingress state directory changed")
		}
		return state, nil
	}
	if err != nil {
		return routeState{}, err
	}
	defer clear(body)
	if s.sameDirectory(before) != nil {
		return routeState{}, errors.New("generated ingress state directory changed")
	}
	if len(body) > maxStateBytes {
		return routeState{}, errors.New("generated ingress state is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || decoder.Decode(&struct{}{}) != io.EOF || state.Version != stateVersion || state.Active == nil || len(state.Active) > maxStateApps {
		return routeState{}, errors.New("generated ingress state is invalid")
	}
	if !validRouteState(state) {
		return routeState{}, errors.New("generated ingress state is invalid")
	}
	return state, nil
}

func (s *stateStore) save(state routeState) error {
	if !validRouteState(state) {
		return errors.New("generated ingress state is invalid")
	}
	body, err := json.Marshal(state)
	if err != nil || len(body) > maxStateBytes {
		clear(body)
		if err == nil {
			err = errors.New("generated ingress state is too large")
		}
		return err
	}
	defer clear(body)
	before, err := s.directoryIdentity()
	if err != nil {
		return err
	}
	if err := secretfile.Write(s.path, statePurpose, body); err != nil {
		return err
	}
	return s.sameDirectory(before)
}

func (s *stateStore) directoryIdentity() (os.FileInfo, error) {
	if s == nil || s.root == "" || filepath.Dir(s.path) != s.root || secureDirectory(s.root) != nil {
		return nil, errors.New("generated ingress state directory is unsafe")
	}
	info, err := os.Lstat(s.root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(s.root) {
		return nil, errors.New("generated ingress state directory is unsafe")
	}
	return info, nil
}

func (s *stateStore) sameDirectory(before os.FileInfo) error {
	after, err := s.directoryIdentity()
	if err != nil || before == nil || !os.SameFile(before, after) {
		return errors.New("generated ingress state directory changed")
	}
	return nil
}

func validRouteState(state routeState) bool {
	if state.Version != stateVersion || state.Active == nil || len(state.Active) > maxStateApps {
		return false
	}
	for appID, route := range state.Active {
		if !validAppID(appID) || validateRoute(route) != nil {
			return false
		}
	}
	if state.Pending != nil {
		if !validAppID(state.Pending.AppID) || validateRoute(state.Pending.Proposed) != nil {
			return false
		}
		if state.Pending.Previous != nil && validateRoute(*state.Pending.Previous) != nil {
			return false
		}
		active, exists := state.Active[state.Pending.AppID]
		if exists != (state.Pending.Previous != nil) || (exists && !sameRoute(active, *state.Pending.Previous)) || active.Slot == state.Pending.Proposed.Slot {
			return false
		}
	}
	return true
}

func sameRoute(left, right routeRecord) bool {
	if left.Slot != right.Slot || len(left.Endpoints) != len(right.Endpoints) {
		return false
	}
	for index := range left.Endpoints {
		if left.Endpoints[index] != right.Endpoints[index] {
			return false
		}
	}
	return true
}

func cloneRoutes(values map[string]routeRecord) map[string]routeRecord {
	result := make(map[string]routeRecord, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		value.Endpoints = append([]generatedruntime.RouteEndpoint(nil), value.Endpoints...)
		result[key] = value
	}
	return result
}
