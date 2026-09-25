// Package bootstraplocator finds a running hostd's protected bootstrap token
// without requiring the operator to know its data-root path.
package bootstraplocator

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	locatorPurpose = "bootstrap-token-locator-v1"
	locatorPrefix  = "locator-"
	locatorSuffix  = ".secret"
	tokenFilename  = "bootstrap-token.secret"
	maxLocators    = 128
)

type Store struct {
	Directory string
	Now       func() time.Time
}

type locator struct {
	Version   int    `json:"version"`
	DataRoot  string `json:"dataRoot"`
	ExpiresAt int64  `json:"expiresAt"`
}

func DefaultStore() Store {
	root, err := os.UserConfigDir()
	if err != nil {
		return Store{Now: time.Now}
	}
	return Store{Directory: filepath.Join(root, "hostd", "bootstrap-locators"), Now: time.Now}
}

// Register advertises one short-lived bootstrap token file. It never copies
// the token into the locator. The returned cleanup removes only this record.
func (s Store) Register(dataRoot string, lifetime time.Duration) (func() error, error) {
	if lifetime <= 0 {
		return nil, errors.New("bootstrap locator lifetime must be positive")
	}
	if err := validateDirectory(s.Directory); err != nil {
		return nil, err
	}
	root, err := canonicalRoot(dataRoot)
	if err != nil {
		return nil, err
	}
	record, err := json.Marshal(locator{Version: 1, DataRoot: root, ExpiresAt: s.now().Add(lifetime).UnixNano()})
	if err != nil {
		return nil, errors.New("encode bootstrap locator")
	}
	defer clear(record)

	var path string
	for attempt := 0; attempt < 3; attempt++ {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, errors.New("generate bootstrap locator ID")
		}
		path = filepath.Join(s.Directory, locatorPrefix+hex.EncodeToString(id[:])+locatorSuffix)
		err = secretfile.WriteNew(path, locatorPurpose, record)
		if err == nil {
			break
		}
		if secretfile.WasInstalled(err) {
			_ = secretfile.Remove(path)
			return nil, errors.New("persist bootstrap locator")
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, errors.New("persist bootstrap locator")
		}
	}
	if err != nil {
		return nil, errors.New("allocate bootstrap locator")
	}

	var once sync.Once
	var removeErr error
	remove := func() error {
		once.Do(func() { removeErr = secretfile.Remove(path) })
		return removeErr
	}
	timer := time.AfterFunc(lifetime, func() { _ = remove() })
	return func() error {
		timer.Stop()
		return remove()
	}, nil
}

// ReadToken returns the current token only when one unique registered data
// root has a live protected token file. Malformed locators fail closed.
func (s Store) ReadToken() ([]byte, error) {
	if err := validateDirectory(s.Directory); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("no active bootstrap token found")
	}
	if err != nil {
		return nil, errors.New("read bootstrap locators")
	}
	if len(entries) > maxLocators {
		return nil, errors.New("too many bootstrap locators")
	}

	candidates := make(map[string][]byte)
	defer func() {
		for _, token := range candidates {
			clear(token)
		}
	}()
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".hostd-secret-") {
			continue // An interrupted atomic write can leave a temporary file.
		}
		if !validLocatorName(entry.Name()) {
			return nil, errors.New("invalid bootstrap locator entry")
		}
		payload, err := secretfile.Read(filepath.Join(s.Directory, entry.Name()), locatorPurpose)
		if err != nil {
			return nil, errors.New("invalid protected bootstrap locator")
		}
		record, err := decodeLocator(payload)
		clear(payload)
		if err != nil {
			return nil, errors.New("invalid bootstrap locator")
		}
		if s.now().UnixNano() >= record.ExpiresAt {
			continue
		}
		root, err := canonicalRoot(record.DataRoot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !samePath(root, record.DataRoot) {
			return nil, errors.New("unsafe bootstrap locator root")
		}
		if _, exists := candidates[root]; exists {
			continue
		}
		token, err := secretfile.Read(filepath.Join(root, tokenFilename), auth.BootstrapSecretPurpose)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, errors.New("invalid protected bootstrap token")
		}
		candidates[root] = token
	}
	if len(candidates) == 0 {
		return nil, errors.New("no active bootstrap token found")
	}
	if len(candidates) > 1 {
		return nil, errors.New("multiple active bootstrap tokens found; stop other unbootstrapped hostd instances and retry")
	}
	for _, token := range candidates {
		return bytes.Clone(token), nil
	}
	panic("unreachable")
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func validateDirectory(directory string) error {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("invalid bootstrap locator directory")
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.New("unsafe bootstrap locator directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("unsafe bootstrap locator directory permissions")
	}
	return nil
}

func canonicalRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("bootstrap data root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("resolve bootstrap data root")
	}
	if runtime.GOOS == "windows" {
		// EvalSymlinks requires reparse-point access that can be unavailable
		// even for a directory the current user can read. Reject a link at
		// the root itself and retain its cleaned absolute path instead.
		info, err := os.Lstat(abs)
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("unsafe bootstrap data root")
		}
		return filepath.Clean(abs), nil
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", errors.New("resolve bootstrap data root")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errors.New("inspect bootstrap data root")
	}
	if !info.IsDir() {
		return "", errors.New("bootstrap data root must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func decodeLocator(payload []byte) (locator, error) {
	var record locator
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return locator{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return locator{}, errors.New("trailing locator data")
	}
	if record.Version != 1 || record.ExpiresAt <= 0 || record.DataRoot == "" || !filepath.IsAbs(record.DataRoot) || filepath.Clean(record.DataRoot) != record.DataRoot {
		return locator{}, errors.New("invalid locator fields")
	}
	return record, nil
}

func validLocatorName(name string) bool {
	if !strings.HasPrefix(name, locatorPrefix) || !strings.HasSuffix(name, locatorSuffix) || len(name) != len(locatorPrefix)+32+len(locatorSuffix) {
		return false
	}
	_, err := hex.DecodeString(name[len(locatorPrefix) : len(name)-len(locatorSuffix)])
	return err == nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
