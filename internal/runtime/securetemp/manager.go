package securetemp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/google/uuid"
)

const (
	envFilename     = "runtime.env"
	composeFilename = "compose.json"
)

var operationName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[1-9][0-9]*$`)

type Manager struct{ root string }

type Files struct {
	Directory   string
	EnvPath     string
	ComposePath string
	once        sync.Once
	cleanupErr  error
}

func New(dataRoot string) (*Manager, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("data root must be an absolute clean path")
	}
	if err := rejectPathAncestors(dataRoot); err != nil {
		return nil, fmt.Errorf("protect data root: %w", err)
	}
	runtimeRoot, err := ensureSecureDirectory(dataRoot, "runtime")
	if err != nil {
		return nil, fmt.Errorf("create runtime root: %w", err)
	}
	root, err := ensureSecureDirectory(runtimeRoot, "compose")
	if err != nil {
		return nil, fmt.Errorf("create compose runtime root: %w", err)
	}
	return &Manager{root: root}, nil
}

func ensureSecureDirectory(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := secureMkdir(path); err != nil {
			return "", err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) {
		return "", errors.New("runtime directory is unsafe")
	}
	return path, nil
}

func (m *Manager) Create(jobID string, attempt int) (*Files, error) {
	parsed, err := uuid.Parse(jobID)
	if err != nil || parsed.String() != jobID || attempt < 1 {
		return nil, errors.New("invalid runtime operation identity")
	}
	name := jobID + "-" + strconv.Itoa(attempt)
	directory := filepath.Join(m.root, name)
	if !within(m.root, directory) {
		return nil, errors.New("runtime temp path escaped root")
	}
	if err := secureMkdir(directory); err != nil {
		return nil, fmt.Errorf("create runtime operation directory: %w", err)
	}
	return &Files{Directory: directory, EnvPath: filepath.Join(directory, envFilename), ComposePath: filepath.Join(directory, composeFilename)}, nil
}

// WriteEnv takes ownership of contents and clears it before returning.
func (f *Files) WriteEnv(contents []byte) error {
	defer clear(contents)
	return writeNew(f.Directory, f.EnvPath, contents)
}

// WriteCompose takes ownership of secret-bearing effective Compose JSON and
// clears it before returning.
func (f *Files) WriteCompose(contents []byte) error {
	defer clear(contents)
	return writeNew(f.Directory, f.ComposePath, contents)
}

func writeNew(root, path string, contents []byte) error {
	if len(contents) == 0 || !within(root, path) {
		return errors.New("invalid protected temporary content")
	}
	if err := rejectReparseAncestors(root, filepath.Dir(path)); err != nil {
		return err
	}
	file, err := secureOpenNew(path)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func (f *Files) Cleanup() error {
	f.once.Do(func() { f.cleanupErr = removeExactOperation(f.Directory) })
	return f.cleanupErr
}

// Recover removes only exact operation directories owned by this manager.
// Unknown names and reparse points are left untouched and reported.
func (m *Manager) Recover() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !operationName.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(m.root, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 || isReparsePoint(path) || !entry.IsDir() {
			return errors.New("unsafe runtime temp recovery entry")
		}
		if err := removeExactOperation(path); err != nil {
			return err
		}
	}
	return nil
}

func removeExactOperation(directory string) error {
	if !operationName.MatchString(filepath.Base(directory)) {
		return errors.New("refusing to remove unrecognized runtime temp path")
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(directory) {
		return errors.New("refusing to remove unsafe runtime temp path")
	}
	return os.RemoveAll(directory)
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && relative != "." && !filepath.IsAbs(relative) && !hasParentPrefix(relative)
}

func hasParentPrefix(value string) bool {
	return len(value) > 3 && value[:3] == ".."+string(filepath.Separator)
}

func rejectPathAncestors(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(current) {
			return errors.New("unsafe runtime temp ancestor")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
