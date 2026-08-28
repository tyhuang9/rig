//go:build windows

package releasesnapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestManagedWorkspaceAcceptsWindowsShortNameWithoutFollowingReparsePoints(t *testing.T) {
	longRoot := t.TempDir()
	shortRoot := windowsShortPathForTest(t, longRoot)
	if strings.EqualFold(filepath.Clean(shortRoot), filepath.Clean(longRoot)) {
		t.Skip("volume did not provide a distinct Windows 8.3 path")
	}
	if !sameFilesystemPath(shortRoot, longRoot) {
		t.Fatalf("Windows short path %q did not match long path %q", shortRoot, longRoot)
	}
	if sameFilesystemPath(filepath.Join(shortRoot, "missing"), filepath.Join(shortRoot, "missing")) {
		t.Fatal("missing paths were treated as safe filesystem identities")
	}

	db := snapshotDB(t)
	materializer, err := New(db, &fakeSources{archive: composeArchive(t, "services: {}\n")}, shortRoot)
	if err != nil {
		t.Fatal(err)
	}
	appID := "11111111-1111-1111-1111-111111111111"
	release, err := materializer.Materialize(context.Background(), "owner", appID)
	if err != nil {
		t.Fatalf("materialize through Windows short path: %v", err)
	}
	if _, err := materializer.ReadyRelease(context.Background(), appID, release.ID); err != nil {
		t.Fatalf("validate ready release through Windows short path: %v", err)
	}
	if size, err := logicalTreeSize(release.WorkspacePath); err != nil || size != release.WorkspaceSizeBytes {
		t.Fatalf("logicalTreeSize() = %d, %v; release size = %d", size, err, release.WorkspaceSizeBytes)
	}

	external := t.TempDir()
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(release.WorkspacePath, "linked-workspace")
	command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, external)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if !localPathIsReparsePoint(link) {
		t.Fatal("directory junction not identified as a reparse point")
	}
	if treeHasExactPaths(release.WorkspacePath) {
		t.Fatal("ready release accepted a junction entry")
	}
	if _, err := logicalTreeSize(release.WorkspacePath); err == nil {
		t.Fatal("logical size accounting followed a junction entry")
	}
	if _, err := materializer.ReadyRelease(context.Background(), appID, release.ID); !IsCode(err, "invalid_source") {
		t.Fatalf("junction-tainted ready release error = %v", err)
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "outside" {
		t.Fatalf("outside sentinel changed: %q, %v", body, err)
	}
}

func windowsShortPathForTest(t *testing.T, path string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	required, err := windows.GetShortPathName(name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if required == 0 || required > maxWindowsLongPathCodeUnits {
		t.Fatalf("invalid short-path buffer size %d", required)
	}
	buffer := make([]uint16, required)
	written, err := windows.GetShortPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatal(err)
	}
	if written == 0 || written >= uint32(len(buffer)) {
		t.Fatalf("invalid short-path result size %d", written)
	}
	return windows.UTF16ToString(buffer[:written])
}
