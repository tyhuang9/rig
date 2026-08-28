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
	"time"

	"github.com/hostd/hostd/internal/secretfile"
)

const tokenBundleVersion = 1

var connectionIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type CredentialStore interface {
	WriteDevice(string, string) error
	ReadDevice(string) (string, error)
	RemoveDevice(string) error
	WriteExchange(string, TokenExchange) error
	ReadExchange(string) (TokenExchange, error)
	RemoveExchange(string) error
	WriteBundle(string, TokenBundle) error
	ReadBundle(string) (TokenBundle, error)
	RemoveBundle(string) error
}

type persistedTokenBundle struct {
	Version          int       `json:"version"`
	Generation       int64     `json:"generation"`
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	ProviderUserID   string    `json:"providerUserId"`
	ProviderLogin    string    `json:"providerLogin"`
}

type persistedTokenExchange struct {
	Version          int       `json:"version"`
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
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

func (store *FileCredentialStore) WriteExchange(connectionID string, exchange TokenExchange) error {
	if !validConnectionID(connectionID) || !validExchange(exchange) {
		return errors.New("invalid token exchange")
	}
	persisted := persistedTokenExchange{Version: tokenBundleVersion, AccessToken: exchange.AccessToken, RefreshToken: exchange.RefreshToken, AccessExpiresAt: exchange.AccessExpiresAt, RefreshExpiresAt: exchange.RefreshExpiresAt}
	value, err := json.Marshal(persisted)
	if err != nil {
		return errors.New("encode token exchange")
	}
	defer clear(value)
	return secretfile.Write(store.exchangePath(connectionID), exchangePurpose(connectionID), value)
}

func (store *FileCredentialStore) ReadExchange(connectionID string) (TokenExchange, error) {
	if !validConnectionID(connectionID) {
		return TokenExchange{}, errors.New("invalid connection ID")
	}
	value, err := secretfile.Read(store.exchangePath(connectionID), exchangePurpose(connectionID))
	if err != nil {
		return TokenExchange{}, err
	}
	defer clear(value)
	var persisted persistedTokenExchange
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return TokenExchange{}, errors.New("invalid token exchange")
	}
	exchange := TokenExchange{Version: persisted.Version, AccessToken: persisted.AccessToken, RefreshToken: persisted.RefreshToken, AccessExpiresAt: persisted.AccessExpiresAt, RefreshExpiresAt: persisted.RefreshExpiresAt}
	if !validExchange(exchange) {
		return TokenExchange{}, errors.New("invalid token exchange")
	}
	return exchange, nil
}

func (store *FileCredentialStore) RemoveExchange(connectionID string) error {
	if !validConnectionID(connectionID) {
		return errors.New("invalid connection ID")
	}
	return secretfile.Remove(store.exchangePath(connectionID))
}

func (store *FileCredentialStore) WriteBundle(connectionID string, bundle TokenBundle) error {
	if !validConnectionID(connectionID) || !validBundle(bundle) {
		return errors.New("invalid token bundle")
	}
	persisted := persistedTokenBundle{Version: tokenBundleVersion, Generation: bundle.Generation, AccessToken: bundle.AccessToken, RefreshToken: bundle.RefreshToken, AccessExpiresAt: bundle.AccessExpiresAt, RefreshExpiresAt: bundle.RefreshExpiresAt, ProviderUserID: bundle.ProviderUserID, ProviderLogin: bundle.ProviderLogin}
	value, err := json.Marshal(persisted)
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
	var persisted persistedTokenBundle
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return TokenBundle{}, errors.New("invalid token bundle")
	}
	bundle := TokenBundle{Version: persisted.Version, Generation: persisted.Generation, AccessToken: persisted.AccessToken, RefreshToken: persisted.RefreshToken, AccessExpiresAt: persisted.AccessExpiresAt, RefreshExpiresAt: persisted.RefreshExpiresAt, ProviderUserID: persisted.ProviderUserID, ProviderLogin: persisted.ProviderLogin}
	if !validBundle(bundle) {
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
func (store *FileCredentialStore) exchangePath(connectionID string) string {
	return filepath.Join(store.connectionDir(connectionID), "exchange.secret")
}

func devicePurpose(connectionID string) string   { return "github-device:" + connectionID }
func bundlePurpose(connectionID string) string   { return "github-token-bundle:" + connectionID }
func exchangePurpose(connectionID string) string { return "github-token-exchange:" + connectionID }
func validConnectionID(connectionID string) bool {
	return connectionIDPattern.MatchString(connectionID)
}

func validBundle(bundle TokenBundle) bool {
	return bundle.Version == tokenBundleVersion && bundle.Generation > 0 && validCredentialText(bundle.AccessToken, 4096) && validCredentialText(bundle.RefreshToken, 4096) && validCredentialText(bundle.ProviderUserID, 128) && validCredentialText(bundle.ProviderLogin, 255) && !bundle.AccessExpiresAt.IsZero() && !bundle.RefreshExpiresAt.IsZero() && bundle.AccessExpiresAt.Before(bundle.RefreshExpiresAt)
}

func validExchange(exchange TokenExchange) bool {
	return exchange.Version == tokenBundleVersion && validCredentialText(exchange.AccessToken, 4096) && validCredentialText(exchange.RefreshToken, 4096) && !exchange.AccessExpiresAt.IsZero() && !exchange.RefreshExpiresAt.IsZero() && exchange.AccessExpiresAt.Before(exchange.RefreshExpiresAt)
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
