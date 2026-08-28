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
