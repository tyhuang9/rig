package releasesnapshot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveEntry struct {
	name, body string
	typ        byte
}

func testArchive(t *testing.T, entries ...archiveEntry) string {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o600, Size: int64(len(e.body))}
		if typ == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "source.tar.gz")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractArchiveRequiresOneSafeRootAndTrueEOF(t *testing.T) {
	valid := testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, archiveEntry{"repo/compose.yaml", "services: {}\n", 0})
	dest := filepath.Join(t.TempDir(), "workspace")
	if err := extractArchive(context.Background(), valid, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]archiveEntry{
		{{"one/a", "x", 0}, {"two/b", "x", 0}},
		{{"repo/a", "x", 0}, {"repo/A", "x", 0}},
		{{"repo/../escape", "x", 0}},
		{{"repo/link", "", tar.TypeSymlink}},
		{{"repo/CON.txt", "x", 0}},
		{{"repo/control\x01", "x", 0}},
		{{"repo/file", "x", 0}, {"repo/file/child", "x", 0}},
	} {
		p := testArchive(t, entries...)
		if err := extractArchive(context.Background(), p, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Fatalf("unsafe archive accepted: %#v", entries)
		}
	}
	trailing := testArchive(t, archiveEntry{"repo/a", "x", 0})
	f, err := os.OpenFile(trailing, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("trailing")
	_ = f.Close()
	if err := extractArchive(context.Background(), trailing, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}

func TestExtractArchiveRejectsConcatenatedAndCorruptGzip(t *testing.T) {
	first := testArchive(t, archiveEntry{"repo/a", "x", 0})
	second := testArchive(t, archiveEntry{"repo/b", "x", 0})
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if err := os.WriteFile(first, append(a, b...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), first, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("concatenated gzip members accepted")
	}
	corrupt := testArchive(t, archiveEntry{"repo/a", "x", 0})
	raw, _ := os.ReadFile(corrupt)
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(corrupt, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), corrupt, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("corrupt gzip accepted")
	}
}

func TestValidateComposeWorkspaceRejectsDynamicAndEscapingPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "Dockerfile"), []byte("FROM scratch"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := "services:\n  app:\n    build:\n      context: .\n      dockerfile: Dockerfile\n"
	if err := os.WriteFile(filepath.Join(root, "app", "compose.yaml"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateComposeWorkspace(filepath.Join(root, "app"), "compose.yaml"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"services:\n  app:\n    build: ${CONTEXT}\n",
		"services:\n  app:\n    build: $CONTEXT\n",
		"services:\n  app:\n    env_file: ../outside.env\n",
		"include: other.yaml\nservices: {}\n",
		"configs:\n  x:\n    file: https://example.test/x\nservices: {}\n",
		"services:\n  a: &app\n    build: .\n  b: *app\n",
		"services:\n  a:\n    build: .\n    build: ../bad\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "app", "compose.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateComposeWorkspace(filepath.Join(root, "app"), "compose.yaml"); err == nil {
			t.Fatalf("unsafe compose accepted: %s", strings.TrimSpace(body))
		}
	}
}
