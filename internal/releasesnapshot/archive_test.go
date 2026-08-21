package releasesnapshot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		{{"repo/COM¹.txt", "x", 0}},
		{{"repo/LPT³.txt", "x", 0}},
		{{"repo/file", "x", 0}},
		{{"repo/file", "x", 0}, {"repo/file/child", "x", 0}},
	} {
		p := testArchive(t, entries...)
		if err := extractArchive(context.Background(), p, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Fatalf("unsafe archive accepted: %#v", entries)
		}
	}
	noRoot := testArchive(t, archiveEntry{"repo/file", "x", 0})
	if err := extractArchive(context.Background(), noRoot, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("file-first archive accepted")
	}
	trailing := testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, archiveEntry{"repo/a", "x", 0})
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
	first := testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, archiveEntry{"repo/a", "x", 0})
	second := testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, archiveEntry{"repo/b", "x", 0})
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if err := os.WriteFile(first, append(a, b...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), first, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("concatenated gzip members accepted")
	}
	corrupt := testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, archiveEntry{"repo/a", "x", 0})
	raw, _ := os.ReadFile(corrupt)
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(corrupt, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), corrupt, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("corrupt gzip accepted")
	}
}

func TestTarLimitReaderUsesBoundedPlusOneOverflowSemantics(t *testing.T) {
	exact := newTarLimitReader(bytes.NewReader([]byte("1234")), 4)
	got, err := io.ReadAll(exact)
	if err != nil || string(got) != "1234" || exact.overflow {
		t.Fatalf("exact=%q err=%v overflow=%v", got, err, exact.overflow)
	}
	over := newTarLimitReader(bytes.NewReader([]byte("12345")), 4)
	got, err = io.ReadAll(over)
	if err != nil || string(got) != "12345" || !over.overflow {
		t.Fatalf("overflow=%q err=%v overflow=%v", got, err, over.overflow)
	}
}

func TestExtractArchiveBoundsPostTarDecompression(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "repo/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "repo/compose.yaml", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len("services: {}\n"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("services: {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write(bytes.Repeat([]byte{0}, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "bomb.tar.gz")
	if err := os.WriteFile(p, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchiveWithLimit(context.Background(), p, filepath.Join(t.TempDir(), "out"), 4096); !errors.Is(err, errTooLarge) {
		t.Fatalf("post-tar bomb=%v", err)
	}
}

type failingReadCloser struct{ reads int }

func (f *failingReadCloser) Read(p []byte) (int, error) {
	if f.reads == 0 {
		f.reads++
		copy(p, "abc")
		return 3, nil
	}
	return 0, errors.New("network")
}
func (*failingReadCloser) Close() error { return nil }
func TestDownloadArchiveClassifiesProviderFailureAndCleansPartial(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "archive.part")
	_, err := downloadArchive(context.Background(), &failingReadCloser{}, destination)
	if !errors.Is(err, errProvider) {
		t.Fatalf("download error=%v", err)
	}
	if _, stat := os.Stat(destination); !os.IsNotExist(stat) {
		t.Fatalf("partial archive remains: %v", stat)
	}
	good := io.NopCloser(bytes.NewReader([]byte("exact bytes")))
	hash, err := downloadArchive(context.Background(), good, destination)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "e38e581aade78b64cc86f7ac9f3555ca78c2dcca747942a7f1d9b3275a834f75" {
		t.Fatalf("hash=%s", hash)
	}
}

func TestArchiveErrorClassifiesLocalExtractionOpenFailureAsInternal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.tar.gz")
	err := extractArchive(context.Background(), missing, filepath.Join(t.TempDir(), "workspace"))
	if err == nil {
		t.Fatal("missing archive was accepted")
	}
	if got := archiveError(err); got != "internal_error" {
		t.Fatalf("missing archive taxonomy = %q, want internal_error (error: %v)", got, err)
	}
}

func TestArchiveNamesRejectWindowsReservedSuperscriptDevices(t *testing.T) {
	for _, name := range []string{"repo/COM\u00b9.txt", "repo/COM\u00b2", "repo/COM\u00b3", "repo/LPT\u00b9.txt", "repo/LPT\u00b2", "repo/LPT\u00b3"} {
		if _, err := validArchiveName(name); err == nil {
			t.Fatalf("Windows reserved device name accepted: %q", name)
		}
	}
}

func TestArchivePathBoundariesAndUnsafeEntryClasses(t *testing.T) {
	exactSegment := "repo/" + strings.Repeat("a", MaxSegmentBytes)
	if _, err := validArchiveName(exactSegment); err != nil {
		t.Fatalf("exact segment rejected: %v", err)
	}
	if _, err := validArchiveName("repo/" + strings.Repeat("a", MaxSegmentBytes+1)); err == nil {
		t.Fatal("overlong segment accepted")
	}
	depth := append([]string{"repo"}, make([]string, MaxPathDepth-1)...)
	for i := 1; i < len(depth); i++ {
		depth[i] = "a"
	}
	if _, err := validArchiveName(strings.Join(depth, "/")); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	if _, err := validArchiveName(strings.Join(append(depth, "a"), "/")); err == nil {
		t.Fatal("over-depth path accepted")
	}
	exactPath := "repo/" + strings.Repeat("a", 255) + "/" + strings.Repeat("b", 255) + "/" + strings.Repeat("c", 255) + "/" + strings.Repeat("d", 251)
	if len(exactPath) != MaxPathBytes {
		t.Fatalf("fixture path length=%d", len(exactPath))
	}
	if _, err := validArchiveName(exactPath); err != nil {
		t.Fatalf("exact path rejected: %v", err)
	}
	if _, err := validArchiveName(exactPath + "x"); err == nil {
		t.Fatal("overlong path accepted")
	}
	for _, entry := range []archiveEntry{
		{"repo/hard", "", tar.TypeLink},
		{"repo/device", "", tar.TypeChar},
		{"repo/fifo", "", tar.TypeFifo},
		{"repo/absolute", "", tar.TypeReg},
	} {
		if entry.name == "repo/absolute" {
			entry.name = "/absolute"
		}
		if err := extractArchive(context.Background(), testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, entry), filepath.Join(t.TempDir(), "out")); err == nil {
			t.Fatalf("unsafe entry accepted: %#v", entry)
		}
	}
	if hasSparse(&tar.Header{Format: tar.FormatGNU, PAXRecords: map[string]string{"GNU.sparse.map": "0,1"}}) == false {
		t.Fatal("GNU sparse entry not detected")
	}
}

func TestExtractArchiveClassifiesFilesystemCollisionsAndCancellation(t *testing.T) {
	archive := testArchive(t, archiveEntry{"repo/", "", tar.TypeDir}, archiveEntry{"repo/file", "contents", tar.TypeReg})
	destinationFile := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(destinationFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := archiveError(extractArchive(context.Background(), archive, destinationFile)); got != "internal_error" {
		t.Fatalf("destination collision taxonomy=%q", got)
	}
	destination := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "file"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := archiveError(extractArchive(context.Background(), archive, destination)); got != "internal_error" {
		t.Fatalf("output collision taxonomy=%q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := archiveError(extractArchive(ctx, archive, filepath.Join(t.TempDir(), "canceled"))); got != "canceled" {
		t.Fatalf("cancellation taxonomy=%q", got)
	}
}

func TestValidateComposeWorkspaceBoundaryPathsResourcesAndLinks(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	for _, directory := range []string{"context", "additional"} {
		if err := os.MkdirAll(filepath.Join(app, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"context/Dockerfile", "app.env", "common.yaml", "config.txt", "secret.txt"} {
		if err := os.WriteFile(filepath.Join(app, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	valid := "services:\n  app:\n    build:\n      context: context\n      dockerfile: Dockerfile\n      additional_contexts:\n        one: additional\n        two: additional\n    env_file:\n      - app.env\n      - path: app.env\n        required: true\n        format: raw\n    extends: {file: common.yaml}\nconfigs: {cfg: {file: config.txt}}\nsecrets: {sec: {file: secret.txt}}\n"
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(app, "compose.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(valid)
	if err := validateComposeWorkspace(app, "compose.yaml"); err != nil {
		t.Fatalf("valid compose rejected: %v", err)
	}
	for _, path := range []string{"", "../compose.yaml", "./compose.yaml", "dir/../compose.yaml", "C:/compose.yaml", "dir\\compose.yaml"} {
		if err := validateComposeWorkspace(app, path); err == nil {
			t.Fatalf("unsafe selected compose path accepted: %q", path)
		}
	}
	exact := "services: {}\n#" + strings.Repeat("x", (1<<20)-len("services: {}\n#"))
	write(exact)
	if err := validateComposeWorkspace(app, "compose.yaml"); err != nil {
		t.Fatalf("exact 1MiB compose rejected: %v", err)
	}
	write(exact + "x")
	if err := validateComposeWorkspace(app, "compose.yaml"); err == nil {
		t.Fatal("over-1MiB compose accepted")
	}
	for _, body := range []string{
		"services: {app: {build: https://example.test/repo}}\n",
		"services: {app: {build: {context: context, dockerfile: ../Dockerfile}}}\n",
		"services: {app: {build: {additional_contexts: [bad]}}}\n",
		"services: {app: {build: {additional_contexts: {bad: service:other}}}}\n",
		"services: {app: {env_file: {path: app.env}}}\n",
		"services: {app: {env_file: [{required: true}]}}\n",
		"services: {app: {env_file: [{path: app.env, required: yes}]}}\n",
		"services: {app: {env_file: [{path: app.env, future: true}]}}\n",
		"services: {app: {extends: {file: missing.yaml}}}\n",
		"configs: {cfg: {file: missing.txt}}\nservices: {}\n",
		"secrets: {sec: {file: context}}\nservices: {}\n",
	} {
		write(body)
		if err := validateComposeWorkspace(app, "compose.yaml"); err == nil {
			t.Fatalf("unsafe compose accepted: %s", strings.TrimSpace(body))
		}
	}
	external := filepath.Join(root, "external-compose.yaml")
	if err := os.WriteFile(external, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(app, "linked.yaml")
	if err := os.Symlink(external, link); err == nil {
		if err := validateComposeWorkspace(app, "linked.yaml"); err == nil {
			t.Fatal("symlinked compose accepted")
		}
	} else {
		t.Logf("symlink fixture unavailable: %v", err)
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

func TestSafeYAMLLimitsExactAndOneOver(t *testing.T) {
	chain := &yaml.Node{Kind: yaml.ScalarNode, Value: "leaf"}
	for i := 0; i < 3; i++ {
		chain = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{chain}}
	}
	count := 0
	if !safeYAMLWithLimits(chain, 0, &count, 3, 4) {
		t.Fatal("exact depth/node boundary rejected")
	}
	chain = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{chain}}
	count = 0
	if safeYAMLWithLimits(chain, 0, &count, 3, 5) {
		t.Fatal("depth overage accepted")
	}
	nodes := &yaml.Node{Kind: yaml.SequenceNode}
	for i := 0; i < 4; i++ {
		nodes.Content = append(nodes.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "x"})
	}
	count = 0
	if !safeYAMLWithLimits(nodes, 0, &count, 2, 5) {
		t.Fatal("exact node boundary rejected")
	}
	nodes.Content = append(nodes.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "x"})
	count = 0
	if safeYAMLWithLimits(nodes, 0, &count, 2, 5) {
		t.Fatal("node overage accepted")
	}
}
func TestValidateComposeWorkspaceRejectsMultipleDocuments(t *testing.T) {
	root := t.TempDir()
	body := "services: {}\n---\nservices: {}\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateComposeWorkspace(root, "compose.yaml"); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
}
