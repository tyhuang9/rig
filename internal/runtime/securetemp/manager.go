package securetemp

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/pathsecurity"
)

const (
	envFilename     = "runtime.env"
	composeFilename = "compose.json"
)

var operationName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}-[1-9][0-9]*$`)

type Manager struct{ root string }

// GeneratedBuilderDirectory is a persistent, controller-owned location for
// BuildKit's identity and configuration. It is intentionally distinct from
// short-lived generated build contexts: recovery of the latter must never
// delete the builder that may be serving another compile.
type GeneratedBuilderDirectory struct {
	root     string
	identity os.FileInfo
}

// GeneratedBuilderFileSnapshot binds one protected file read to both the
// opened filesystem object and its exact contents. Callers use Same to ensure
// an immutable controller identity was not replaced or rewritten between
// independently validated observations.
type GeneratedBuilderFileSnapshot struct {
	root, name string
	identity   os.FileInfo
	digest     [sha256.Size]byte
}

type Files struct {
	Directory   string
	EnvPath     string
	ComposePath string
	once        sync.Once
	cleanupErr  error
}

func New(dataRoot string) (*Manager, error) {
	return newManager(dataRoot, "compose")
}

// NewGeneratedBuild creates a separate protected namespace for compiler-owned
// build contexts. Keeping it separate prevents runtime configuration files and
// untrusted source trees from sharing a cleanup boundary.
func NewGeneratedBuild(dataRoot string) (*Manager, error) {
	return newManager(dataRoot, "generated-build")
}

// NewGeneratedRuntime creates a separate protected namespace for generated
// runtime environment files. Its recovery can never remove Compose material or
// generated build contexts.
func NewGeneratedRuntime(dataRoot string) (*Manager, error) {
	return newManager(dataRoot, "generated-runtime")
}

// NewGeneratedBuilderDirectory creates the persistent protected namespace
// used by the generated-image BuildKit controller. Callers can create only
// direct, simple child directories and files through this value, preventing a
// builder configuration path from escaping the controller data root.
func NewGeneratedBuilderDirectory(dataRoot string) (*GeneratedBuilderDirectory, error) {
	if dataRoot == "" || pathsecurity.RejectWindowsNamespace(dataRoot) || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("data root must be an absolute clean path")
	}
	if err := rejectPathAncestors(dataRoot); err != nil {
		return nil, fmt.Errorf("protect data root: %w", err)
	}
	runtimeRoot, err := ensureSecureDirectory(dataRoot, "runtime")
	if err != nil {
		return nil, fmt.Errorf("create runtime root: %w", err)
	}
	root, err := ensureSecureDirectory(runtimeRoot, "generated-builder")
	if err != nil {
		return nil, fmt.Errorf("create generated builder root: %w", err)
	}
	identity, err := generatedBuilderDirectoryIdentity(root)
	if err != nil {
		return nil, errors.New("generated builder root is unsafe")
	}
	return &GeneratedBuilderDirectory{root: root, identity: identity}, nil
}

// OpenGeneratedBuilderDirectory opens an existing generated-builder namespace
// without creating or changing any path. It is used by read-only restart
// accounting that must not turn missing persistent state into new authority.
func OpenGeneratedBuilderDirectory(dataRoot string) (*GeneratedBuilderDirectory, error) {
	if dataRoot == "" || pathsecurity.RejectWindowsNamespace(dataRoot) || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("data root must be an absolute clean path")
	}
	if err := rejectPathAncestors(dataRoot); err != nil {
		return nil, fmt.Errorf("protect data root: %w", err)
	}
	runtimeRoot := filepath.Join(dataRoot, "runtime")
	root := filepath.Join(runtimeRoot, "generated-builder")
	for _, path := range []string{runtimeRoot, root} {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmtPrivateDirectoryError(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) {
			return nil, errors.New("generated builder root is unsafe")
		}
	}
	if !within(dataRoot, runtimeRoot) || !within(dataRoot, root) ||
		rejectReparseAncestors(dataRoot, root) != nil || ValidatePrivateDirectory(runtimeRoot) != nil ||
		ValidatePrivateDirectory(root) != nil {
		return nil, errors.New("generated builder root is unsafe")
	}
	identity, err := generatedBuilderDirectoryIdentity(root)
	if err != nil {
		return nil, errors.New("generated builder root is unsafe")
	}
	return &GeneratedBuilderDirectory{root: root, identity: identity}, nil
}

// Root returns the protected persistent directory. It is suitable as a
// process working directory, but all controller-created children should use
// EnsureDirectory or WriteNewFile so Windows ACLs and reparse-point checks are
// preserved.
func (d *GeneratedBuilderDirectory) Root() string {
	if d == nil {
		return ""
	}
	return d.root
}

// Same reports whether two observations pinned the same existing directory.
func (d *GeneratedBuilderDirectory) Same(other *GeneratedBuilderDirectory) bool {
	return d != nil && other != nil && d.identity != nil && other.identity != nil &&
		d.root == other.root && os.SameFile(d.identity, other.identity)
}

// EnsureDirectory returns a direct protected child of the builder root.
func (d *GeneratedBuilderDirectory) EnsureDirectory(name string) (string, error) {
	if d == nil || !validBuilderChildName(name) {
		return "", errors.New("invalid generated builder directory")
	}
	if err := d.validateRoot(); err != nil {
		return "", err
	}
	return ensureSecureDirectory(d.root, name)
}

// WriteNewFile writes a direct protected child exactly once. It never follows
// a link and deliberately does not replace an existing identity/config file.
func (d *GeneratedBuilderDirectory) WriteNewFile(name string, contents []byte) error {
	defer clear(contents)
	if d == nil || !validBuilderChildName(name) {
		return errors.New("invalid generated builder file")
	}
	path := filepath.Join(d.root, name)
	if !within(d.root, path) || d.validateRoot() != nil {
		return errors.New("generated builder file escaped root")
	}
	return writeNew(d.root, path, contents)
}

// ReadFile returns the direct regular child after validating the protected
// parent and entry. The returned bytes belong to the caller and may contain
// controller state, so callers should clear them after parsing.
func (d *GeneratedBuilderDirectory) ReadFile(name string, maximum int64) ([]byte, error) {
	body, _, err := d.ReadFileSnapshot(name, maximum)
	return body, err
}

// ReadFileSnapshot returns the protected bytes and an opaque identity/content
// snapshot suitable for a later Same comparison.
func (d *GeneratedBuilderDirectory) ReadFileSnapshot(name string, maximum int64) (
	[]byte, *GeneratedBuilderFileSnapshot, error,
) {
	if d == nil || !validBuilderChildName(name) || maximum < 1 {
		return nil, nil, errors.New("invalid generated builder file")
	}
	path := filepath.Join(d.root, name)
	if !within(d.root, path) || d.validateRoot() != nil {
		return nil, nil, errors.New("generated builder file escaped root")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) || before.Size() > maximum {
		return nil, nil, errors.New("generated builder file is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	body, opened, readErr := func() ([]byte, os.FileInfo, error) {
		defer file.Close()
		body, err := io.ReadAll(io.LimitReader(file, maximum+1))
		if err != nil {
			return nil, nil, err
		}
		opened, err := file.Stat()
		if err != nil || len(body) > int(maximum) || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() != before.Size() || int64(len(body)) != before.Size() {
			clear(body)
			return nil, nil, errors.New("generated builder file changed")
		}
		return body, opened, nil
	}()
	if readErr != nil {
		return nil, nil, readErr
	}
	current, currentErr := os.Lstat(path)
	if currentErr != nil || d.validateRoot() != nil || !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) || !os.SameFile(opened, current) {
		clear(body)
		return nil, nil, errors.New("generated builder file changed")
	}
	return body, &GeneratedBuilderFileSnapshot{root: d.root, name: name,
		identity: opened, digest: sha256.Sum256(body)}, nil
}

// Same reports whether two snapshots observed the same protected file object
// with the same exact contents.
func (s *GeneratedBuilderFileSnapshot) Same(other *GeneratedBuilderFileSnapshot) bool {
	return s != nil && other != nil && s.identity != nil && other.identity != nil &&
		s.root == other.root && s.name == other.name && s.digest == other.digest &&
		os.SameFile(s.identity, other.identity)
}

func (d *GeneratedBuilderDirectory) validateRoot() error {
	if d == nil || d.root == "" || d.identity == nil || rejectReparseAncestors(d.root, d.root) != nil {
		return errors.New("generated builder root is unsafe")
	}
	current, err := os.Lstat(d.root)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || isReparsePoint(d.root) ||
		!os.SameFile(d.identity, current) {
		return errors.New("generated builder root changed")
	}
	return nil
}

func generatedBuilderDirectoryIdentity(root string) (os.FileInfo, error) {
	file, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() {
		return nil, errors.New("generated builder root is unavailable")
	}
	current, err := os.Lstat(root)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || isReparsePoint(root) ||
		!os.SameFile(opened, current) {
		return nil, errors.New("generated builder root changed")
	}
	return opened, nil
}

func newManager(dataRoot, namespace string) (*Manager, error) {
	if dataRoot == "" || pathsecurity.RejectWindowsNamespace(dataRoot) || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("data root must be an absolute clean path")
	}
	if namespace != "compose" && namespace != "generated-build" && namespace != "generated-runtime" {
		return nil, errors.New("unsupported runtime temp namespace")
	}
	if err := rejectPathAncestors(dataRoot); err != nil {
		return nil, fmt.Errorf("protect data root: %w", err)
	}
	runtimeRoot, err := ensureSecureDirectory(dataRoot, "runtime")
	if err != nil {
		return nil, fmt.Errorf("create runtime root: %w", err)
	}
	root, err := ensureSecureDirectory(runtimeRoot, namespace)
	if err != nil {
		return nil, fmt.Errorf("create %s runtime root: %w", namespace, err)
	}
	return &Manager{root: root}, nil
}

func ensureSecureDirectory(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	if pathsecurity.RejectWindowsNamespace(parent) || pathsecurity.RejectWindowsNamespace(path) {
		return "", errors.New("unsafe runtime temp path namespace")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := secureMkdir(path); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return "", err
			}
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) {
		return "", errors.New("runtime directory is unsafe")
	}
	return path, nil
}

func validBuilderChildName(name string) bool {
	if len(name) < 1 || len(name) > 80 || name == "." || name == ".." {
		return false
	}
	for _, character := range name {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return !strings.Contains(name, "..")
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
