package generateddomain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type retainedRootOwner struct {
	path       string
	parent     string
	info       os.FileInfo
	parentInfo os.FileInfo
	marker     os.FileInfo
}

var retainedRoots struct {
	sync.Mutex
	owner *retainedRootOwner
}

// NewRetainedRoot returns a private empty data root that remains present until
// the package test process exits. Generated-domain reservations retain sealed
// filesystem identities for the process lifetime, so real-domain fixtures must
// keep the corresponding filesystem objects alive for that same lifetime.
func NewRetainedRoot(t testing.TB) string {
	t.Helper()

	retainedRoots.Lock()
	defer retainedRoots.Unlock()
	if retainedRoots.owner == nil {
		owner, err := newRetainedRootOwner(os.TempDir())
		if err != nil {
			t.Fatalf("create retained generated-domain test root: %v", err)
		}
		retainedRoots.owner = owner
	}
	root, err := os.MkdirTemp(retainedRoots.owner.path, "domain-")
	if err != nil {
		t.Fatalf("create retained generated-domain fixture root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatalf("protect retained generated-domain fixture root: %v", err)
	}
	return root
}

// RunTestMain retains every root created through NewRetainedRoot until all
// tests and their cleanup callbacks have finished, then removes only the one
// package-process root owned by this helper.
func RunTestMain(m *testing.M) {
	code := m.Run()
	if err := cleanupRetainedRoots(); err != nil {
		fmt.Fprintln(os.Stderr, "remove retained generated-domain test root:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func newRetainedRootOwner(parent string) (*retainedRootOwner, error) {
	parent = filepath.Clean(parent)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect retained root parent: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return nil, errors.New("retained root parent is not an exact directory")
	}
	root, err := os.MkdirTemp(parent, "rig-generated-domain-test-roots-")
	if err != nil {
		return nil, err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(root)
		}
	}()
	root = filepath.Clean(root)
	if filepath.Clean(filepath.Dir(root)) != parent {
		return nil, errors.New("retained root escaped its parent scope")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect retained generated-domain test root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect retained generated-domain test root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("retained generated-domain test root is not an exact directory")
	}
	markerPath := filepath.Join(root, ".owner")
	marker, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create retained generated-domain root marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		return nil, fmt.Errorf("close retained generated-domain root marker: %w", err)
	}
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return nil, fmt.Errorf("inspect retained generated-domain root marker: %w", err)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return nil, errors.New("retained generated-domain root marker is not an exact file")
	}
	remove = false
	return &retainedRootOwner{path: root, parent: parent, info: info, parentInfo: parentInfo, marker: markerInfo}, nil
}

func cleanupRetainedRoots() error {
	retainedRoots.Lock()
	defer retainedRoots.Unlock()
	if retainedRoots.owner == nil {
		return nil
	}
	if err := retainedRoots.owner.cleanup(); err != nil {
		return err
	}
	retainedRoots.owner = nil
	return nil
}

func (o *retainedRootOwner) cleanup() error {
	if o == nil || o.path == "" {
		return nil
	}
	path := filepath.Clean(o.path)
	if path != o.path || filepath.Clean(filepath.Dir(path)) != o.parent {
		return errors.New("retained generated-domain test root parent scope changed")
	}
	parentInfo, err := os.Lstat(o.parent)
	if err != nil {
		return fmt.Errorf("inspect retained root parent before cleanup: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || !os.SameFile(o.parentInfo, parentInfo) {
		return errors.New("retained root parent identity changed")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect retained generated-domain test root before cleanup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(o.info, info) {
		return errors.New("retained generated-domain test root identity changed")
	}
	markerInfo, err := os.Lstat(filepath.Join(path, ".owner"))
	if err != nil {
		return fmt.Errorf("inspect retained generated-domain root marker before cleanup: %w", err)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() || !os.SameFile(o.marker, markerInfo) {
		return errors.New("retained generated-domain root marker identity changed")
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove exact retained generated-domain test root: %w", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("retained generated-domain test root remains after cleanup")
		}
		return fmt.Errorf("verify retained generated-domain test root cleanup: %w", err)
	}
	o.path = ""
	o.info = nil
	o.marker = nil
	return nil
}
