package controllerrelay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	credentialTestControllerID = "11111111-1111-4111-8111-111111111111"
	credentialTestKeyID        = "22222222-2222-4222-8222-222222222222"
	credentialTestEnrollmentID = "33333333-3333-4333-8333-333333333333"
)

func TestFileCredentialStoreControllerKeyRoundTripAndMetadataBinding(t *testing.T) {
	store := newTestCredentialStore(t)
	privateKey := testPrivateKey(7)
	defer clear(privateKey)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	defer clear(publicKey)

	ref, err := store.WriteControllerKey(ControllerKeyBundle{
		Version: credentialVersion, ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID,
		PrivateKey: privateKey, PublicKey: publicKey,
	})
	if err != nil {
		t.Fatalf("write controller key: %v", err)
	}
	if ref != ProtectedKeyRef(credentialTestControllerID, credentialTestKeyID) {
		t.Fatalf("unexpected protected ref %q", ref)
	}

	loaded, err := store.ReadControllerKey(credentialTestControllerID, credentialTestKeyID, publicKey)
	if err != nil {
		t.Fatalf("read controller key: %v", err)
	}
	defer loaded.Destroy()
	if !bytes.Equal(loaded.PrivateKey, privateKey) || !bytes.Equal(loaded.PublicKey, publicKey) {
		t.Fatal("loaded key material differs")
	}

	wrongPublic := bytes.Repeat([]byte{0x44}, ed25519.PublicKeySize)
	if _, err = store.ReadControllerKey(credentialTestControllerID, credentialTestKeyID, wrongPublic); err == nil {
		t.Fatal("expected public metadata mismatch rejection")
	}
	if _, err = store.ReadControllerKey("11111111-1111-4111-8111-11111111111A", credentialTestKeyID, publicKey); err == nil {
		t.Fatal("expected noncanonical controller UUID rejection")
	}
	if _, err = store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID, PrivateKey: testPrivateKey(8)}); err == nil {
		t.Fatal("expected create-only key protection")
	}
}

func TestFileCredentialStorePollRoundTripAndExactCleanup(t *testing.T) {
	store := newTestCredentialStore(t)
	token := bytes.Repeat([]byte{0xa5}, pollTokenBytes)
	neighborID := "44444444-4444-4444-8444-444444444444"
	neighbor := bytes.Repeat([]byte{0x5a}, pollTokenBytes)

	ref, err := store.WriteEnrollmentPollToken(EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: credentialTestEnrollmentID, OwnerUserID: "owner-1", Token: token})
	if err != nil {
		t.Fatalf("write poll token: %v", err)
	}
	if ref != ProtectedEnrollmentPollRef(credentialTestControllerID, credentialTestEnrollmentID) {
		t.Fatalf("unexpected protected ref %q", ref)
	}
	if _, err = store.WriteEnrollmentPollToken(EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: neighborID, OwnerUserID: "owner-1", Token: neighbor}); err != nil {
		t.Fatalf("write neighboring poll token: %v", err)
	}

	loaded, err := store.ReadEnrollmentPollToken(credentialTestControllerID, credentialTestEnrollmentID)
	if err != nil {
		t.Fatalf("read poll token: %v", err)
	}
	if !bytes.Equal(loaded.Token, token) {
		t.Fatal("loaded poll token differs")
	}
	loaded.Destroy()
	if err = store.RemoveEnrollmentPollToken(credentialTestControllerID, credentialTestEnrollmentID); err != nil {
		t.Fatalf("remove exact poll token: %v", err)
	}
	if _, err = store.ReadEnrollmentPollToken(credentialTestControllerID, credentialTestEnrollmentID); err == nil {
		t.Fatal("removed poll token remained readable")
	}
	neighborLoaded, err := store.ReadEnrollmentPollToken(credentialTestControllerID, neighborID)
	if err != nil {
		t.Fatalf("exact cleanup removed neighbor: %v", err)
	}
	neighborLoaded.Destroy()
}

func TestCredentialTypesRedactSecrets(t *testing.T) {
	privateKey := testPrivateKey(9)
	defer clear(privateKey)
	pollToken := bytes.Repeat([]byte("secret-token-material"), 2)
	key := ControllerKeyBundle{Version: credentialVersion, ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID, PrivateKey: privateKey}
	poll := EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: credentialTestEnrollmentID, OwnerUserID: "owner-1", Token: pollToken}

	for name, value := range map[string]string{
		"key string": fmt.Sprint(key), "key gostring": fmt.Sprintf("%#v", key),
		"poll string": fmt.Sprint(poll), "poll gostring": fmt.Sprintf("%#v", poll),
		"key log": key.LogValue().String(), "poll log": poll.LogValue().String(),
	} {
		if strings.Contains(value, string(privateKey)) || strings.Contains(value, string(pollToken)) || strings.Contains(value, "secret-token-material") {
			t.Fatalf("%s leaked credential: %q", name, value)
		}
	}
	_ = slog.Any("key", key)
	_ = slog.Any("poll", poll)
}

func TestCredentialStrictEncodingRejectsUnknownDuplicateAndTrailingFields(t *testing.T) {
	valid := persistedEnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: credentialTestEnrollmentID, OwnerUserID: "owner-1", Token: bytes.Repeat([]byte{1}, pollTokenBytes)}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	var decoded persistedEnrollmentPollToken
	if err = decodeStrictJSON(encoded, &decoded); err != nil {
		t.Fatalf("valid strict JSON rejected: %v", err)
	}
	clear(decoded.Token)

	invalid := [][]byte{
		append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":1}`)...),
		[]byte(`{"version":1,"version":1,"enrollmentId":"` + credentialTestEnrollmentID + `","pollToken":"AQ=="}`),
		append(append([]byte(nil), encoded...), []byte(` {}`)...),
	}
	for _, body := range invalid {
		if err = decodeStrictJSON(body, &decoded); err == nil {
			t.Fatalf("invalid strict JSON accepted: %s", body)
		}
		clear(body)
	}
}

func TestFileCredentialStoreRejectsSymlinkCredential(t *testing.T) {
	store := newTestCredentialStore(t)
	token := bytes.Repeat([]byte{2}, pollTokenBytes)
	if _, err := store.WriteEnrollmentPollToken(EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: credentialTestEnrollmentID, OwnerUserID: "owner-1", Token: token}); err != nil {
		t.Fatal(err)
	}
	path, _, err := store.pollTokenLocation(credentialTestControllerID, credentialTestEnrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err = os.WriteFile(target, []byte("not a credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err = store.ReadEnrollmentPollToken(credentialTestControllerID, credentialTestEnrollmentID); err == nil {
		t.Fatal("symlink credential was accepted")
	}
	if err = store.RemoveEnrollmentPollToken(credentialTestControllerID, credentialTestEnrollmentID); err == nil {
		t.Fatal("exact cleanup followed or removed symlink")
	}
}

func TestFileCredentialStoreRejectsSymlinkedParentDuringExactRemoval(t *testing.T) {
	t.Run("controller key", func(t *testing.T) {
		store := newTestCredentialStore(t)
		privateKey := testPrivateKey(4)
		defer clear(privateKey)
		if _, err := store.WriteControllerKey(ControllerKeyBundle{Version: credentialVersion, ControllerID: credentialTestControllerID, KeyID: credentialTestKeyID, PrivateKey: privateKey}); err != nil {
			t.Fatal(err)
		}
		path, _, err := store.controllerKeyLocation(credentialTestControllerID, credentialTestKeyID)
		if err != nil {
			t.Fatal(err)
		}
		assertSymlinkedParentRemovalRejected(t, filepath.Dir(path), filepath.Base(path), func() error {
			return store.RemoveControllerKey(credentialTestControllerID, credentialTestKeyID)
		})
	})

	t.Run("enrollment poll", func(t *testing.T) {
		store := newTestCredentialStore(t)
		if _, err := store.WriteEnrollmentPollToken(EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: credentialTestEnrollmentID, OwnerUserID: "owner-1", Token: bytes.Repeat([]byte{4}, pollTokenBytes)}); err != nil {
			t.Fatal(err)
		}
		path, _, err := store.pollTokenLocation(credentialTestControllerID, credentialTestEnrollmentID)
		if err != nil {
			t.Fatal(err)
		}
		assertSymlinkedParentRemovalRejected(t, filepath.Dir(path), filepath.Base(path), func() error {
			return store.RemoveEnrollmentPollToken(credentialTestControllerID, credentialTestEnrollmentID)
		})
	})
}

func assertSymlinkedParentRemovalRejected(t *testing.T, parent, fileName string, remove func() error) {
	t.Helper()
	originalParent := parent + ".original"
	if err := os.Rename(parent, originalParent); err != nil {
		t.Fatal(err)
	}
	externalParent := t.TempDir()
	externalTarget := filepath.Join(externalParent, fileName)
	if err := os.WriteFile(externalTarget, []byte("outside managed root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalParent, parent); err != nil {
		if restoreErr := os.Rename(originalParent, parent); restoreErr != nil {
			t.Fatalf("symlinks unavailable (%v) and restore failed: %v", err, restoreErr)
		}
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := remove(); err == nil {
		t.Fatal("exact removal accepted a symlinked parent")
	}
	if value, err := os.ReadFile(externalTarget); err != nil || string(value) != "outside managed root" {
		t.Fatalf("external target was changed or removed: value=%q err=%v", value, err)
	}
	if _, err := os.Stat(filepath.Join(originalParent, fileName)); err != nil {
		t.Fatalf("original protected credential was changed or removed: %v", err)
	}
}

func TestFileCredentialStoreUsesRestrictivePOSIXPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission assertion")
	}
	store := newTestCredentialStore(t)
	if _, err := store.WriteEnrollmentPollToken(EnrollmentPollToken{Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: credentialTestEnrollmentID, OwnerUserID: "owner-1", Token: bytes.Repeat([]byte{3}, pollTokenBytes)}); err != nil {
		t.Fatal(err)
	}
	path, _, _ := store.pollTokenLocation(credentialTestControllerID, credentialTestEnrollmentID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("poll credential mode is too broad: %o", info.Mode().Perm())
	}
}

func TestEnrollmentPollCredentialInventoryPagesDeterministicallyAndRejectsInvalidCursor(t *testing.T) {
	store := newTestCredentialStore(t)
	ids := []string{
		"f0000000-0000-4000-8000-000000000003",
		"10000000-0000-4000-8000-000000000001",
		"90000000-0000-4000-8000-000000000002",
	}
	for index, enrollmentID := range ids {
		if _, err := store.WriteEnrollmentPollToken(EnrollmentPollToken{
			Version: credentialVersion, ControllerID: credentialTestControllerID, EnrollmentID: enrollmentID,
			OwnerUserID: "owner-1", Token: bytes.Repeat([]byte{byte(index + 1)}, pollTokenBytes),
		}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	cursor := ""
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		page, err := store.EnrollmentPollCredentials(cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Credentials) > 1 {
			t.Fatalf("page exceeded bound: %#v", page)
		}
		for _, metadata := range page.Credentials {
			got = append(got, metadata.EnrollmentID)
		}
		if page.Complete {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatalf("inventory cursor did not advance: %#v", page)
		}
		cursor = page.NextCursor
	}
	want := []string{ids[1], ids[2], ids[0]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("deterministic page order=%v want=%v", got, want)
	}
	for _, invalid := range []string{"v1:", "../poll", "v1:A1111111-1111-4111-8111-111111111111:" + credentialTestEnrollmentID} {
		if _, err := store.EnrollmentPollCredentials(invalid, 1); err == nil {
			t.Fatalf("invalid cursor accepted: %q", invalid)
		}
	}
}

func TestCredentialDirectoryReadIsSortedAndFailsAtExplicitCapacity(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"c", "a", "b"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readBoundedSortedDirectory(directory, 2); err == nil {
		t.Fatal("directory inventory exceeded its explicit entry capacity")
	}
	entries, err := readBoundedSortedDirectory(directory, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if fmt.Sprint(got) != "[a b c]" {
		t.Fatalf("bounded directory order=%v", got)
	}
}

func newTestCredentialStore(t *testing.T) *FileCredentialStore {
	t.Helper()
	store, err := NewFileCredentialStore(t.TempDir())
	if err != nil {
		t.Fatalf("new credential store: %v", err)
	}
	return store
}

func testPrivateKey(seedByte byte) ed25519.PrivateKey {
	seed := bytes.Repeat([]byte{seedByte}, ed25519.SeedSize)
	defer clear(seed)
	return ed25519.NewKeyFromSeed(seed)
}
