package secretfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNewDoesNotReplaceExistingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "immutable.secret")
	if err := WriteNew(path, "first-purpose", []byte("first-value")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(path, "second-purpose", []byte("second-value")); err == nil {
		t.Fatal("WriteNew replaced an existing destination")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing destination bytes changed")
	}
	loaded, err := Read(path, "first-purpose")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(loaded)
	if !bytes.Equal(loaded, []byte("first-value")) {
		t.Fatalf("loaded %q", loaded)
	}
	if _, err := Read(path, "second-purpose"); err == nil {
		t.Fatal("wrong purpose was accepted")
	}
}

func TestWriteNewReportsInstalledDurabilityFailure(t *testing.T) {
	original := syncParentDirectory
	syncParentDirectory = func(string) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncParentDirectory = original })
	path := filepath.Join(t.TempDir(), "installed.secret")
	err := WriteNew(path, "purpose", []byte("value"))
	if err == nil || !WasInstalled(err) {
		t.Fatalf("write error = %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("installed destination missing: %v", statErr)
	}
}

func TestReadBoundedKeepsOrdinarySecretLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-protected-artifact.secret")
	value := bytes.Repeat([]byte("x"), 70<<10)
	if err := WriteNew(path, "large-artifact", value); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, "large-artifact"); err == nil {
		t.Fatal("ordinary secret read accepted an oversized artifact")
	}
	loaded, err := ReadBounded(path, "large-artifact", 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(loaded)
	if !bytes.Equal(loaded, value) {
		t.Fatal("bounded protected read changed the artifact")
	}
	if _, err := ReadBounded(path, "wrong-purpose", 128<<10); err == nil {
		t.Fatal("bounded read accepted the wrong purpose")
	}
	for _, maximum := range []int{0, maxBoundedSecretFileBytes + 1} {
		if _, err := ReadBounded(path, "large-artifact", maximum); err == nil {
			t.Fatalf("invalid bounded read limit %d was accepted", maximum)
		}
	}
}
