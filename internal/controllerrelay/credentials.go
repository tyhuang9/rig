package controllerrelay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	credentialVersion                 = 1
	pollTokenBytes                    = 32
	maxCredentialBytes                = 4 << 10
	maxInventoryDirectoryEntries      = 10_000
	controllerKeyCredentialUnreadable = "credential_unreadable"
	controllerKeyCredentialUnexpected = "unexpected_artifact"
	controllerKeyCredentialUnsafe     = "unsafe_artifact"
	controllerKeyTemporaryPrefix      = ".hostd-secret-"
)

// ControllerKeyBundle is decrypted controller signing material. Callers must
// call Destroy as soon as the key is no longer needed.
type ControllerKeyBundle struct {
	Version      int
	ControllerID string
	KeyID        string
	PrivateKey   ed25519.PrivateKey
	PublicKey    ed25519.PublicKey
}

type ControllerKeyCredentialMetadata struct {
	ControllerID string
	KeyID        string
	PublicKey    []byte
	ProtectedRef string
}

type ControllerKeyCredentialPage struct {
	Credentials []ControllerKeyCredentialMetadata
	Issues      []ControllerKeyCredentialIssue
	NextCursor  string
	Complete    bool
}

type ControllerKeyCredentialIssue struct {
	ControllerID string
	KeyID        string
	ProtectedRef string
	Code         string
}

type ControllerKeyTemporaryArtifact struct {
	ControllerID string
	Name         string
}

type ControllerKeyTemporaryArtifactPage struct {
	Artifacts  []ControllerKeyTemporaryArtifact
	NextCursor string
	Complete   bool
}

func (bundle ControllerKeyBundle) String() string   { return "protected relay controller key" }
func (bundle ControllerKeyBundle) GoString() string { return bundle.String() }
func (bundle ControllerKeyBundle) LogValue() slog.Value {
	return slog.GroupValue(slog.String("state", "protected"))
}

func (bundle *ControllerKeyBundle) Destroy() {
	if bundle == nil {
		return
	}
	clear(bundle.PrivateKey)
	clear(bundle.PublicKey)
	bundle.PrivateKey = nil
	bundle.PublicKey = nil
}

// EnrollmentPollToken is a short-lived bearer credential. Callers must call
// Destroy after use.
type EnrollmentPollToken struct {
	Version      int
	ControllerID string
	EnrollmentID string
	OwnerUserID  string
	Token        []byte
}

type EnrollmentPollCredentialMetadata struct {
	ControllerID string
	EnrollmentID string
	OwnerUserID  string
	ProtectedRef string
}

type EnrollmentPollCredentialPage struct {
	Credentials []EnrollmentPollCredentialMetadata
	NextCursor  string
	Complete    bool
}

func (token EnrollmentPollToken) String() string   { return "protected relay enrollment poll token" }
func (token EnrollmentPollToken) GoString() string { return token.String() }
func (token EnrollmentPollToken) LogValue() slog.Value {
	return slog.GroupValue(slog.String("state", "protected"))
}

func (token *EnrollmentPollToken) Destroy() {
	if token == nil {
		return
	}
	clear(token.Token)
	token.Token = nil
}

type persistedControllerKey struct {
	Version      int    `json:"version"`
	ControllerID string `json:"controllerId"`
	KeyID        string `json:"keyId"`
	PrivateKey   []byte `json:"privateKey"`
}

type persistedEnrollmentPollToken struct {
	Version      int    `json:"version"`
	ControllerID string `json:"controllerId"`
	EnrollmentID string `json:"enrollmentId"`
	OwnerUserID  string `json:"ownerUserId"`
	Token        []byte `json:"pollToken"`
}

// FileCredentialStore keeps controller relay credentials below one managed,
// deterministic subtree. References returned by this type are relative to the
// protected secret root and are safe to persist in SQLite.
type FileCredentialStore struct {
	dataRoot string
}

func NewFileCredentialStore(dataRoot string) (*FileCredentialStore, error) {
	if dataRoot == "" || strings.IndexByte(dataRoot, 0) >= 0 {
		return nil, errors.New("relay credential data root is required")
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, errors.New("invalid relay credential data root")
	}
	absolute = filepath.Clean(absolute)
	if absolute == filepath.Clean(string(filepath.Separator)) || filepath.VolumeName(absolute)+string(filepath.Separator) == absolute {
		return nil, errors.New("relay credential data root is too broad")
	}
	return &FileCredentialStore{dataRoot: absolute}, nil
}

func (store *FileCredentialStore) WriteControllerKey(bundle ControllerKeyBundle) (string, error) {
	if store == nil || !validCanonicalUUID(bundle.ControllerID) || !validCanonicalUUID(bundle.KeyID) || bundle.Version != credentialVersion || len(bundle.PrivateKey) != ed25519.PrivateKeySize {
		return "", errors.New("invalid relay controller key")
	}
	publicKey, ok := bundle.PrivateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("invalid relay controller key")
	}
	if len(bundle.PublicKey) != 0 && subtle.ConstantTimeCompare(bundle.PublicKey, publicKey) != 1 {
		return "", errors.New("relay controller public key does not match private key")
	}
	path, ref, err := store.controllerKeyLocation(bundle.ControllerID, bundle.KeyID)
	if err != nil {
		return "", err
	}
	if err = store.prepareParent(filepath.Dir(path)); err != nil {
		return "", err
	}
	persisted := persistedControllerKey{
		Version: credentialVersion, ControllerID: bundle.ControllerID, KeyID: bundle.KeyID,
		PrivateKey: append([]byte(nil), bundle.PrivateKey...),
	}
	defer clear(persisted.PrivateKey)
	plaintext, err := json.Marshal(persisted)
	if err != nil || len(plaintext) > maxCredentialBytes {
		clear(plaintext)
		return "", errors.New("encode relay controller key")
	}
	defer clear(plaintext)
	if err = secretfile.WriteNew(path, controllerKeyPurpose(bundle.ControllerID, bundle.KeyID), plaintext); err != nil {
		if secretfile.WasInstalled(err) {
			_ = secretfile.Remove(path)
		}
		return "", errors.New("store relay controller key")
	}
	return ref, nil
}

// ReadControllerKey derives the public key from protected private material and
// requires it to match the canonical public-key metadata stored in SQLite.
func (store *FileCredentialStore) ReadControllerKey(controllerID, keyID string, expectedPublicKey []byte) (ControllerKeyBundle, error) {
	if store == nil || !validCanonicalUUID(controllerID) || !validCanonicalUUID(keyID) || len(expectedPublicKey) != ed25519.PublicKeySize {
		return ControllerKeyBundle{}, errors.New("invalid relay controller key metadata")
	}
	privateKey, publicKey, err := store.loadControllerKey(controllerID, keyID)
	if err != nil {
		return ControllerKeyBundle{}, err
	}
	if subtle.ConstantTimeCompare(publicKey, expectedPublicKey) != 1 {
		clear(privateKey)
		clear(publicKey)
		return ControllerKeyBundle{}, errors.New("relay controller public key metadata does not match protected key")
	}
	return ControllerKeyBundle{Version: credentialVersion, ControllerID: controllerID, KeyID: keyID, PrivateKey: privateKey, PublicKey: publicKey}, nil
}

func (store *FileCredentialStore) loadControllerKey(controllerID, keyID string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	path, _, err := store.controllerKeyLocation(controllerID, keyID)
	if err != nil {
		return nil, nil, err
	}
	if err = store.validateExistingPath(path); err != nil {
		return nil, nil, err
	}
	plaintext, err := secretfile.Read(path, controllerKeyPurpose(controllerID, keyID))
	if err != nil {
		return nil, nil, errors.New("load relay controller key")
	}
	defer clear(plaintext)
	var persisted persistedControllerKey
	if err = decodeStrictJSON(plaintext, &persisted); err != nil {
		clear(persisted.PrivateKey)
		return nil, nil, errors.New("invalid relay controller key")
	}
	defer clear(persisted.PrivateKey)
	if persisted.Version != credentialVersion || persisted.ControllerID != controllerID || persisted.KeyID != keyID || len(persisted.PrivateKey) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("invalid relay controller key")
	}
	privateKey := append(ed25519.PrivateKey(nil), persisted.PrivateKey...)
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		clear(privateKey)
		return nil, nil, errors.New("invalid relay controller key")
	}
	return privateKey, append(ed25519.PublicKey(nil), publicKey...), nil
}

func (store *FileCredentialStore) RemoveControllerKey(controllerID, keyID string) error {
	_, err := store.RemoveControllerKeyWithResult(controllerID, keyID)
	return err
}

// RemoveControllerKeyWithResult reports whether the exact protected file was
// present and removed. Callers use this to distinguish cleanup work from an
// idempotent already-absent result.
func (store *FileCredentialStore) RemoveControllerKeyWithResult(controllerID, keyID string) (bool, error) {
	if store == nil || !validCanonicalUUID(controllerID) || !validCanonicalUUID(keyID) {
		return false, errors.New("invalid relay controller key identity")
	}
	path, _, err := store.controllerKeyLocation(controllerID, keyID)
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, errors.New("inspect relay credential")
	}
	if err = store.validateDirectoryChain(filepath.Dir(path)); err != nil {
		return false, err
	}
	if err = removeExactSecret(path); err != nil {
		return false, err
	}
	removeEmptyCredentialDirectory(filepath.Dir(path))
	return true, nil
}

// ControllerKeyCredentials returns a bounded metadata-only inventory for
// crash recovery. Each file is decrypted with its exact purpose and private
// material is cleared before the public identity is returned.
func (store *FileCredentialStore) ControllerKeyCredentials(cursor string, limit int) (ControllerKeyCredentialPage, error) {
	if store == nil || limit < 1 || limit > 1000 {
		return ControllerKeyCredentialPage{}, errors.New("invalid relay controller key inventory limit")
	}
	cursorControllerName, cursorEntryName, err := parseControllerKeyInventoryCursor(cursor)
	if err != nil {
		return ControllerKeyCredentialPage{}, err
	}
	controllersRoot := filepath.Join(store.dataRoot, "secrets", "relay", "controllers")
	info, err := os.Lstat(controllersRoot)
	if errors.Is(err, os.ErrNotExist) {
		return ControllerKeyCredentialPage{Complete: true}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ControllerKeyCredentialPage{}, errors.New("relay controller credential directory is unsafe")
	}
	controllerEntries, err := readBoundedSortedDirectory(controllersRoot, maxInventoryDirectoryEntries)
	if err != nil {
		return ControllerKeyCredentialPage{}, errors.New("list relay controller credentials")
	}
	result := make([]ControllerKeyCredentialMetadata, 0, limit)
	issues := make([]ControllerKeyCredentialIssue, 0)
	processed := 0
	for _, controllerEntry := range controllerEntries {
		controllerName := controllerEntry.Name()
		if cursorControllerName != "" && controllerName < cursorControllerName {
			continue
		}
		if !validCanonicalUUID(controllerName) {
			if cursorControllerName == "" || controllerName > cursorControllerName {
				issues = append(issues, ControllerKeyCredentialIssue{Code: controllerKeyCredentialUnexpected})
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerName, "")}, nil
				}
			}
			continue
		}
		controllerID := controllerName
		controllerPath := filepath.Join(controllersRoot, controllerID)
		controllerInfo, inspectErr := os.Lstat(controllerPath)
		if inspectErr != nil || !controllerInfo.IsDir() || controllerInfo.Mode()&os.ModeSymlink != 0 {
			if cursorControllerName == "" || controllerID > cursorControllerName {
				issues = append(issues, ControllerKeyCredentialIssue{ControllerID: controllerID, Code: controllerKeyCredentialUnsafe})
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, "")}, nil
				}
			}
			continue
		}
		keysPath := filepath.Join(controllerPath, "keys")
		keysInfo, inspectErr := os.Lstat(keysPath)
		if errors.Is(inspectErr, os.ErrNotExist) {
			continue
		}
		if inspectErr != nil || !keysInfo.IsDir() || keysInfo.Mode()&os.ModeSymlink != 0 {
			if cursorControllerName == "" || controllerID > cursorControllerName {
				issues = append(issues, ControllerKeyCredentialIssue{ControllerID: controllerID, Code: controllerKeyCredentialUnsafe})
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, "")}, nil
				}
			}
			continue
		}
		keyEntries, readErr := readBoundedSortedDirectory(keysPath, maxInventoryDirectoryEntries)
		if readErr != nil {
			return ControllerKeyCredentialPage{}, errors.New("list relay controller keys")
		}
		for _, keyEntry := range keyEntries {
			name := keyEntry.Name()
			if cursorControllerName != "" && (controllerID < cursorControllerName || controllerID == cursorControllerName && name <= cursorEntryName) {
				continue
			}
			keyPath := filepath.Join(keysPath, name)
			keyInfo, inspectErr := os.Lstat(keyPath)
			if validControllerKeyTemporaryArtifactName(name) {
				if inspectErr != nil || !keyInfo.Mode().IsRegular() {
					issues = append(issues, ControllerKeyCredentialIssue{ControllerID: controllerID, Code: controllerKeyCredentialUnsafe})
				}
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, name)}, nil
				}
				continue
			}
			keyID := strings.TrimSuffix(name, ".key")
			if !strings.HasSuffix(name, ".key") || !validCanonicalUUID(keyID) {
				issues = append(issues, ControllerKeyCredentialIssue{ControllerID: controllerID, Code: controllerKeyCredentialUnexpected})
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, name)}, nil
				}
				continue
			}
			if inspectErr != nil || !keyInfo.Mode().IsRegular() {
				issues = append(issues, ControllerKeyCredentialIssue{ControllerID: controllerID, KeyID: keyID, ProtectedRef: ProtectedKeyRef(controllerID, keyID), Code: controllerKeyCredentialUnsafe})
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, name)}, nil
				}
				continue
			}
			privateKey, publicKey, loadErr := store.loadControllerKey(controllerID, keyID)
			if loadErr != nil {
				issues = append(issues, ControllerKeyCredentialIssue{ControllerID: controllerID, KeyID: keyID, ProtectedRef: ProtectedKeyRef(controllerID, keyID), Code: controllerKeyCredentialUnreadable})
				processed++
				if processed == limit {
					return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, name)}, nil
				}
				continue
			}
			clear(privateKey)
			result = append(result, ControllerKeyCredentialMetadata{ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, ProtectedRef: ProtectedKeyRef(controllerID, keyID)})
			processed++
			if processed == limit {
				return ControllerKeyCredentialPage{Credentials: result, Issues: issues, NextCursor: controllerKeyInventoryCursor(controllerID, name)}, nil
			}
		}
	}
	return ControllerKeyCredentialPage{Credentials: result, Issues: issues, Complete: true}, nil
}

func controllerKeyCredentialCursor(controllerID, keyID string) string {
	return controllerKeyInventoryCursor(controllerID, keyID+".key")
}

func parseControllerKeyCredentialCursor(cursor string) (string, string, error) {
	controllerID, entryName, err := parseControllerKeyInventoryCursor(cursor)
	if err != nil {
		return "", "", err
	}
	if controllerID == "" {
		return "", "", nil
	}
	keyID := strings.TrimSuffix(entryName, ".key")
	if !validCanonicalUUID(controllerID) || !strings.HasSuffix(entryName, ".key") || !validCanonicalUUID(keyID) || cursor != controllerKeyCredentialCursor(controllerID, keyID) {
		return "", "", errors.New("invalid relay controller key inventory cursor")
	}
	return controllerID, keyID, nil
}

func controllerKeyInventoryCursor(controllerName, entryName string) string {
	return "v2:" + base64.RawURLEncoding.EncodeToString([]byte(controllerName)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(entryName))
}

func parseControllerKeyInventoryCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	if !strings.HasPrefix(cursor, "v2:") {
		return "", "", errors.New("invalid relay controller key inventory cursor")
	}
	parts := strings.Split(cursor[3:], ":")
	if len(parts) != 2 {
		return "", "", errors.New("invalid relay controller key inventory cursor")
	}
	controller, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(controller) == 0 || len(controller) > 255 || strings.IndexByte(string(controller), 0) >= 0 {
		return "", "", errors.New("invalid relay controller key inventory cursor")
	}
	entry, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(entry) > 255 || strings.IndexByte(string(entry), 0) >= 0 || cursor != controllerKeyInventoryCursor(string(controller), string(entry)) {
		return "", "", errors.New("invalid relay controller key inventory cursor")
	}
	return string(controller), string(entry), nil
}

// ControllerKeyTemporaryArtifacts inventories only safe regular generic
// secretfile staging artifacts. Unsafe entries are surfaced by the credential
// inventory and are never returned as deletion candidates.
func (store *FileCredentialStore) ControllerKeyTemporaryArtifacts(cursor string, limit int) (ControllerKeyTemporaryArtifactPage, error) {
	if store == nil || limit < 1 || limit > 1000 {
		return ControllerKeyTemporaryArtifactPage{}, errors.New("invalid relay controller key temporary inventory limit")
	}
	cursorController, cursorEntry, err := parseControllerKeyInventoryCursor(cursor)
	if err != nil {
		return ControllerKeyTemporaryArtifactPage{}, err
	}
	controllersRoot := filepath.Join(store.dataRoot, "secrets", "relay", "controllers")
	entries, err := readBoundedSortedDirectoryIfPresent(controllersRoot, maxInventoryDirectoryEntries)
	if err != nil {
		return ControllerKeyTemporaryArtifactPage{}, errors.New("list relay controller credentials")
	}
	page := ControllerKeyTemporaryArtifactPage{Artifacts: make([]ControllerKeyTemporaryArtifact, 0, limit)}
	for _, controllerEntry := range entries {
		controllerID := controllerEntry.Name()
		if !validCanonicalUUID(controllerID) || controllerID < cursorController {
			continue
		}
		controllerPath := filepath.Join(controllersRoot, controllerID)
		controllerInfo, inspectErr := os.Lstat(controllerPath)
		if inspectErr != nil || !controllerInfo.IsDir() || controllerInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		keysPath := filepath.Join(controllerPath, "keys")
		keyEntries, readErr := readBoundedSortedDirectoryIfPresent(keysPath, maxInventoryDirectoryEntries)
		if readErr != nil {
			continue
		}
		for _, keyEntry := range keyEntries {
			name := keyEntry.Name()
			if (controllerID == cursorController && name <= cursorEntry) || !validControllerKeyTemporaryArtifactName(name) {
				continue
			}
			info, inspectErr := os.Lstat(filepath.Join(keysPath, name))
			if inspectErr != nil || !info.Mode().IsRegular() {
				continue
			}
			page.Artifacts = append(page.Artifacts, ControllerKeyTemporaryArtifact{ControllerID: controllerID, Name: name})
			if len(page.Artifacts) == limit {
				page.NextCursor = controllerKeyInventoryCursor(controllerID, name)
				return page, nil
			}
		}
	}
	page.Complete = true
	return page, nil
}

func (store *FileCredentialStore) RemoveControllerKeyTemporaryArtifact(controllerID, name string) (bool, error) {
	if store == nil || !validCanonicalUUID(controllerID) || !validControllerKeyTemporaryArtifactName(name) {
		return false, errors.New("invalid relay controller key temporary artifact")
	}
	keysPath := filepath.Join(store.dataRoot, "secrets", "relay", "controllers", controllerID, "keys")
	if err := store.validateDirectoryChain(keysPath); err != nil {
		return false, err
	}
	path := filepath.Join(keysPath, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("relay controller key temporary artifact is unsafe")
	}
	if err = secretfile.Remove(path); err != nil {
		return false, errors.New("remove relay controller key temporary artifact")
	}
	return true, nil
}

func validControllerKeyTemporaryArtifactName(name string) bool {
	if len(name) < len(controllerKeyTemporaryPrefix)+1 || len(name) > 128 || !strings.HasPrefix(name, controllerKeyTemporaryPrefix) {
		return false
	}
	for _, character := range name[len(controllerKeyTemporaryPrefix):] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (store *FileCredentialStore) WriteEnrollmentPollToken(token EnrollmentPollToken) (string, error) {
	if store == nil || token.Version != credentialVersion || !validCanonicalUUID(token.ControllerID) || !validCanonicalUUID(token.EnrollmentID) || !validCredentialOwner(token.OwnerUserID) || len(token.Token) != pollTokenBytes {
		return "", errors.New("invalid relay enrollment poll token")
	}
	path, ref, err := store.pollTokenLocation(token.ControllerID, token.EnrollmentID)
	if err != nil {
		return "", err
	}
	if err = store.prepareParent(filepath.Dir(path)); err != nil {
		return "", err
	}
	persisted := persistedEnrollmentPollToken{Version: credentialVersion, ControllerID: token.ControllerID, EnrollmentID: token.EnrollmentID, OwnerUserID: token.OwnerUserID, Token: append([]byte(nil), token.Token...)}
	defer clear(persisted.Token)
	plaintext, err := json.Marshal(persisted)
	if err != nil || len(plaintext) > maxCredentialBytes {
		clear(plaintext)
		return "", errors.New("encode relay enrollment poll token")
	}
	defer clear(plaintext)
	if err = secretfile.WriteNew(path, enrollmentPollPurpose(token.ControllerID, token.EnrollmentID), plaintext); err != nil {
		if secretfile.WasInstalled(err) {
			_ = secretfile.Remove(path)
		}
		return "", errors.New("store relay enrollment poll token")
	}
	return ref, nil
}

func (store *FileCredentialStore) ReadEnrollmentPollToken(controllerID, enrollmentID string) (EnrollmentPollToken, error) {
	if store == nil || !validCanonicalUUID(controllerID) || !validCanonicalUUID(enrollmentID) {
		return EnrollmentPollToken{}, errors.New("invalid relay enrollment identity")
	}
	path, _, err := store.pollTokenLocation(controllerID, enrollmentID)
	if err != nil {
		return EnrollmentPollToken{}, err
	}
	if err = store.validateExistingPath(path); err != nil {
		return EnrollmentPollToken{}, err
	}
	plaintext, err := secretfile.Read(path, enrollmentPollPurpose(controllerID, enrollmentID))
	if err != nil {
		return EnrollmentPollToken{}, errors.New("load relay enrollment poll token")
	}
	defer clear(plaintext)
	var persisted persistedEnrollmentPollToken
	if err = decodeStrictJSON(plaintext, &persisted); err != nil {
		clear(persisted.Token)
		return EnrollmentPollToken{}, errors.New("invalid relay enrollment poll token")
	}
	defer clear(persisted.Token)
	if persisted.Version != credentialVersion || persisted.ControllerID != controllerID || persisted.EnrollmentID != enrollmentID || !validCredentialOwner(persisted.OwnerUserID) || len(persisted.Token) != pollTokenBytes {
		return EnrollmentPollToken{}, errors.New("invalid relay enrollment poll token")
	}
	return EnrollmentPollToken{Version: credentialVersion, ControllerID: controllerID, EnrollmentID: enrollmentID, OwnerUserID: persisted.OwnerUserID, Token: append([]byte(nil), persisted.Token...)}, nil
}

func (store *FileCredentialStore) RemoveEnrollmentPollToken(controllerID, enrollmentID string) error {
	if store == nil || !validCanonicalUUID(controllerID) || !validCanonicalUUID(enrollmentID) {
		return errors.New("invalid relay enrollment identity")
	}
	path, _, err := store.pollTokenLocation(controllerID, enrollmentID)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("inspect relay credential")
	}
	if err = store.validateDirectoryChain(filepath.Dir(path)); err != nil {
		return err
	}
	if err = removeExactSecret(path); err != nil {
		return err
	}
	removeEmptyCredentialDirectory(filepath.Dir(path))
	return nil
}

// EnrollmentPollCredentials returns a bounded, deterministic inventory used
// only for startup reconciliation. It decrypts each exact purpose-bound file,
// copies non-secret identity metadata, and clears the bearer token before
// returning. Unexpected path shapes fail closed and are never removed.
func (store *FileCredentialStore) EnrollmentPollCredentials(cursor string, limit int) (EnrollmentPollCredentialPage, error) {
	if store == nil || limit < 1 || limit > 1000 {
		return EnrollmentPollCredentialPage{}, errors.New("invalid relay credential inventory limit")
	}
	cursorControllerID, cursorEnrollmentID, err := parseEnrollmentPollCursor(cursor)
	if err != nil {
		return EnrollmentPollCredentialPage{}, err
	}
	controllersRoot := filepath.Join(store.dataRoot, "secrets", "relay", "controllers")
	info, err := os.Lstat(controllersRoot)
	if errors.Is(err, os.ErrNotExist) {
		return EnrollmentPollCredentialPage{Complete: true}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return EnrollmentPollCredentialPage{}, errors.New("relay controller credential directory is unsafe")
	}
	controllerEntries, err := readBoundedSortedDirectory(controllersRoot, maxInventoryDirectoryEntries)
	if err != nil {
		return EnrollmentPollCredentialPage{}, errors.New("list relay controller credentials")
	}
	result := make([]EnrollmentPollCredentialMetadata, 0)
	for _, controllerEntry := range controllerEntries {
		if !validCanonicalUUID(controllerEntry.Name()) {
			continue
		}
		controllerID := controllerEntry.Name()
		if cursorControllerID != "" && controllerID < cursorControllerID {
			continue
		}
		controllerPath := filepath.Join(controllersRoot, controllerID)
		controllerInfo, inspectErr := os.Lstat(controllerPath)
		if inspectErr != nil || !controllerInfo.IsDir() || controllerInfo.Mode()&os.ModeSymlink != 0 {
			return EnrollmentPollCredentialPage{}, errors.New("relay controller credential path is unsafe")
		}
		enrollmentsPath := filepath.Join(controllerPath, "enrollments")
		enrollmentsInfo, inspectErr := os.Lstat(enrollmentsPath)
		if errors.Is(inspectErr, os.ErrNotExist) {
			continue
		}
		if inspectErr != nil || !enrollmentsInfo.IsDir() || enrollmentsInfo.Mode()&os.ModeSymlink != 0 {
			return EnrollmentPollCredentialPage{}, errors.New("relay enrollment credential path is unsafe")
		}
		enrollmentEntries, readErr := readBoundedSortedDirectory(enrollmentsPath, maxInventoryDirectoryEntries)
		if readErr != nil {
			return EnrollmentPollCredentialPage{}, errors.New("list relay enrollment credentials")
		}
		for _, enrollmentEntry := range enrollmentEntries {
			if !validCanonicalUUID(enrollmentEntry.Name()) {
				continue
			}
			enrollmentID := enrollmentEntry.Name()
			if cursorControllerID != "" && (controllerID < cursorControllerID || controllerID == cursorControllerID && enrollmentID <= cursorEnrollmentID) {
				continue
			}
			enrollmentPath := filepath.Join(enrollmentsPath, enrollmentID)
			enrollmentInfo, inspectErr := os.Lstat(enrollmentPath)
			if inspectErr != nil || !enrollmentInfo.IsDir() || enrollmentInfo.Mode()&os.ModeSymlink != 0 {
				return EnrollmentPollCredentialPage{}, errors.New("relay enrollment credential path is unsafe")
			}
			pollPath := filepath.Join(enrollmentPath, "poll")
			pollInfo, inspectErr := os.Lstat(pollPath)
			if errors.Is(inspectErr, os.ErrNotExist) {
				continue
			}
			if inspectErr != nil || !pollInfo.Mode().IsRegular() {
				return EnrollmentPollCredentialPage{}, errors.New("relay enrollment poll credential is unsafe")
			}
			token, readErr := store.ReadEnrollmentPollToken(controllerID, enrollmentID)
			if readErr != nil {
				return EnrollmentPollCredentialPage{}, readErr
			}
			result = append(result, EnrollmentPollCredentialMetadata{
				ControllerID: token.ControllerID, EnrollmentID: token.EnrollmentID, OwnerUserID: token.OwnerUserID,
				ProtectedRef: ProtectedEnrollmentPollRef(token.ControllerID, token.EnrollmentID),
			})
			token.Destroy()
			if len(result) == limit {
				last := result[len(result)-1]
				return EnrollmentPollCredentialPage{Credentials: result, NextCursor: enrollmentPollCursor(last.ControllerID, last.EnrollmentID)}, nil
			}
		}
	}
	return EnrollmentPollCredentialPage{Credentials: result, Complete: true}, nil
}

func enrollmentPollCursor(controllerID, enrollmentID string) string {
	return "v1:" + controllerID + ":" + enrollmentID
}

func parseEnrollmentPollCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	const cursorLength = len("v1:") + 36 + 1 + 36
	if len(cursor) != cursorLength || !strings.HasPrefix(cursor, "v1:") || cursor[39] != ':' {
		return "", "", errors.New("invalid relay credential inventory cursor")
	}
	controllerID, enrollmentID := cursor[3:39], cursor[40:]
	if !validCanonicalUUID(controllerID) || !validCanonicalUUID(enrollmentID) || cursor != enrollmentPollCursor(controllerID, enrollmentID) {
		return "", "", errors.New("invalid relay credential inventory cursor")
	}
	return controllerID, enrollmentID, nil
}

func (store *FileCredentialStore) controllerKeyLocation(controllerID, keyID string) (string, string, error) {
	if !validCanonicalUUID(controllerID) || !validCanonicalUUID(keyID) {
		return "", "", errors.New("invalid relay controller key ID")
	}
	ref := ProtectedKeyRef(controllerID, keyID)
	return filepath.Join(store.dataRoot, "secrets", filepath.FromSlash(ref)), ref, nil
}

func (store *FileCredentialStore) pollTokenLocation(controllerID, enrollmentID string) (string, string, error) {
	if !validCanonicalUUID(controllerID) || !validCanonicalUUID(enrollmentID) {
		return "", "", errors.New("invalid relay enrollment ID")
	}
	ref := ProtectedEnrollmentPollRef(controllerID, enrollmentID)
	return filepath.Join(store.dataRoot, "secrets", filepath.FromSlash(ref)), ref, nil
}

// prepareParent rejects symlinked or non-directory components inside the
// managed subtree. It creates only deterministic path components and never
// enumerates or removes neighboring credentials.
func (store *FileCredentialStore) prepareParent(parent string) error {
	managedRoot := filepath.Join(store.dataRoot, "secrets", "relay")
	if err := ensureDirectoryPath(store.dataRoot, managedRoot); err != nil {
		return errors.New("prepare relay credential directory")
	}
	if err := ensureDirectoryPath(managedRoot, parent); err != nil {
		return errors.New("prepare relay credential directory")
	}
	return nil
}

func (store *FileCredentialStore) validateExistingPath(path string) error {
	parent := filepath.Dir(path)
	if err := store.validateDirectoryChain(parent); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("inspect relay credential")
	}
	if !info.Mode().IsRegular() {
		return errors.New("relay credential path must be a regular file")
	}
	return nil
}

func (store *FileCredentialStore) validateDirectoryChain(parent string) error {
	managedRoot := filepath.Join(store.dataRoot, "secrets", "relay")
	relative, err := filepath.Rel(managedRoot, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("relay credential path escapes managed root")
	}
	for _, path := range append([]string{store.dataRoot, filepath.Join(store.dataRoot, "secrets"), managedRoot}, pathPrefixes(managedRoot, relative)...) {
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("relay credential directory is unsafe")
		}
	}
	return nil
}

func ensureDirectoryPath(base, target string) error {
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("path escapes base")
	}
	if err = ensureOneDirectory(base); err != nil {
		return err
	}
	for _, path := range pathPrefixes(base, relative) {
		if err = ensureOneDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func ensureOneDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path component is not a safe directory")
	}
	if err = os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}

func pathPrefixes(base, relative string) []string {
	if relative == "." || relative == "" {
		return nil
	}
	parts := strings.Split(relative, string(filepath.Separator))
	paths := make([]string, 0, len(parts))
	current := base
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil
		}
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	return paths
}

func removeExactSecret(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("inspect relay credential")
	}
	if !info.Mode().IsRegular() {
		return errors.New("relay credential path must be a regular file")
	}
	if err = secretfile.Remove(path); err != nil {
		return errors.New("remove relay credential")
	}
	return nil
}

func removeEmptyCredentialDirectory(path string) {
	// Enrollment/key directories contain only immutable credential files. An
	// exact non-recursive removal bounds future inventories; a non-empty or
	// concurrently changed directory is intentionally retained.
	_ = os.Remove(path)
}

func readBoundedSortedDirectory(path string, maximum int) ([]os.DirEntry, error) {
	if maximum < 1 {
		return nil, errors.New("invalid relay credential directory bound")
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maximum {
		return nil, errors.New("relay credential directory exceeds bounded inventory capacity")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func readBoundedSortedDirectoryIfPresent(path string, maximum int) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("relay credential directory is unsafe")
	}
	return readBoundedSortedDirectory(path, maximum)
}

func controllerKeyPurpose(controllerID, keyID string) string {
	return "controller-relay-ed25519-key:v1:" + controllerID + ":" + keyID
}

func enrollmentPollPurpose(controllerID, enrollmentID string) string {
	return "controller-relay-enrollment-poll:v1:" + controllerID + ":" + enrollmentID
}

func validCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validCredentialOwner(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func decodeStrictJSON(body []byte, target any) error {
	if len(body) == 0 || len(body) > maxCredentialBytes {
		return errors.New("invalid JSON size")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON key")
				}
				seen[key] = struct{}{}
				if err = walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
