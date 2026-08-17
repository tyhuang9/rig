package sourceconnections

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/hostd/hostd/internal/secretfile"
)

const tokenBundleVersion = 1

var connectionIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type CredentialStore interface {
	WriteDevice(string, string) error
	ReadDevice(string) (string, error)
	RemoveDevice(string) error
	WriteBundle(string, TokenBundle) error
	ReadBundle(string) (TokenBundle, error)
	RemoveBundle(string) error
}

type FileCredentialStore struct{ dataRoot string }

func NewFileCredentialStore(dataRoot string) *FileCredentialStore {
	return &FileCredentialStore{dataRoot: dataRoot}
}

func (store *FileCredentialStore) WriteDevice(connectionID, deviceCode string) error {
	if !validConnectionID(connectionID) || !validCredentialText(deviceCode, 4096) {
		return errors.New("invalid device credential")
	}
	value := []byte(deviceCode)
	defer clear(value)
	return secretfile.Write(store.devicePath(connectionID), devicePurpose(connectionID), value)
}

func (store *FileCredentialStore) ReadDevice(connectionID string) (string, error) {
	if !validConnectionID(connectionID) {
		return "", errors.New("invalid connection ID")
	}
	value, err := secretfile.Read(store.devicePath(connectionID), devicePurpose(connectionID))
	if err != nil {
		return "", err
	}
	defer clear(value)
	result := string(value)
	if !validCredentialText(result, 4096) {
		return "", errors.New("invalid device credential")
	}
	return result, nil
}

func (store *FileCredentialStore) RemoveDevice(connectionID string) error {
	if !validConnectionID(connectionID) {
		return errors.New("invalid connection ID")
	}
	return secretfile.Remove(store.devicePath(connectionID))
}

func (store *FileCredentialStore) WriteBundle(connectionID string, bundle TokenBundle) error {
	if !validConnectionID(connectionID) || !validBundle(bundle) {
		return errors.New("invalid token bundle")
	}
	bundle.Version = tokenBundleVersion
	value, err := json.Marshal(bundle)
	if err != nil {
		return errors.New("encode token bundle")
	}
	defer clear(value)
	return secretfile.Write(store.bundlePath(connectionID), bundlePurpose(connectionID), value)
}

func (store *FileCredentialStore) ReadBundle(connectionID string) (TokenBundle, error) {
	if !validConnectionID(connectionID) {
		return TokenBundle{}, errors.New("invalid connection ID")
	}
	value, err := secretfile.Read(store.bundlePath(connectionID), bundlePurpose(connectionID))
	if err != nil {
		return TokenBundle{}, err
	}
	defer clear(value)
	var bundle TokenBundle
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !validBundle(bundle) {
		return TokenBundle{}, errors.New("invalid token bundle")
	}
	return bundle, nil
}

func (store *FileCredentialStore) RemoveBundle(connectionID string) error {
	if !validConnectionID(connectionID) {
		return errors.New("invalid connection ID")
	}
	return secretfile.Remove(store.bundlePath(connectionID))
}

func (store *FileCredentialStore) connectionDir(connectionID string) string {
	return filepath.Join(store.dataRoot, "secrets", "github", connectionID)
}

func (store *FileCredentialStore) devicePath(connectionID string) string {
	return filepath.Join(store.connectionDir(connectionID), "device.secret")
}

func (store *FileCredentialStore) bundlePath(connectionID string) string {
	return filepath.Join(store.connectionDir(connectionID), "tokens.secret")
}

func devicePurpose(connectionID string) string { return "github-device:" + connectionID }
func bundlePurpose(connectionID string) string { return "github-token-bundle:" + connectionID }
func validConnectionID(connectionID string) bool {
	return connectionIDPattern.MatchString(connectionID)
}

func validBundle(bundle TokenBundle) bool {
	return bundle.Version == tokenBundleVersion && bundle.Generation > 0 && validCredentialText(bundle.AccessToken, 4096) && validCredentialText(bundle.RefreshToken, 4096) && validCredentialText(bundle.ProviderUserID, 128) && validCredentialText(bundle.ProviderLogin, 255) && !bundle.AccessExpiresAt.IsZero() && !bundle.RefreshExpiresAt.IsZero() && bundle.AccessExpiresAt.Before(bundle.RefreshExpiresAt)
}

func credentialMissing(err error) bool { return errors.Is(err, os.ErrNotExist) }

func validCredentialText(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func (bundle TokenBundle) String() string {
	return "GitHub token bundle generation " + strconv.FormatInt(bundle.Generation, 10)
}

func (bundle TokenBundle) GoString() string { return bundle.String() }

func (bundle TokenBundle) LogValue() slog.Value {
	return slog.GroupValue(slog.Int64("generation", bundle.Generation))
}
