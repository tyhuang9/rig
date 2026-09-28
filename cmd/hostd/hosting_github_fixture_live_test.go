//go:build live_docker

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/githubapp"
)

const (
	controllerJourneyGitHubSHA   = "cccccccccccccccccccccccccccccccccccccccc"
	controllerJourneyGitHubToken = "synthetic-controller-journey-token"
)

// controllerJourneyGitHubProvider supplies deterministic metadata through the
// source-connection service and a real HTTP archive stream. The separate
// githubapp client tests cover its fixed-origin and redirect rules; this
// fixture exercises authenticated controller -> connection -> materializer.
type controllerJourneyGitHubProvider struct {
	files        map[string][]byte
	archive      []byte
	server       *httptest.Server
	archiveReads atomic.Int32
	mu           sync.RWMutex
	activeSHA    string
	revisions    map[string]controllerJourneyGitHubRevision
	archiveBySHA map[string]int
}

type controllerJourneyGitHubRevision struct {
	files   map[string][]byte
	archive []byte
}

func controllerJourneyNewGitHubProvider(t *testing.T, source string) *controllerJourneyGitHubProvider {
	t.Helper()
	revision := controllerJourneyBuildGitHubRevision(t, source, controllerJourneyGitHubSHA)
	provider := &controllerJourneyGitHubProvider{
		files: revision.files, archive: revision.archive, activeSHA: controllerJourneyGitHubSHA,
		revisions:    map[string]controllerJourneyGitHubRevision{controllerJourneyGitHubSHA: revision},
		archiveBySHA: map[string]int{},
	}
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/repos/fixture/hosting-notes/tarball/"
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, prefix) ||
			r.Header.Get("Authorization") != "Bearer "+controllerJourneyGitHubToken {
			http.Error(w, "fixture request rejected", http.StatusForbidden)
			return
		}
		sha := strings.TrimPrefix(r.URL.Path, prefix)
		provider.mu.Lock()
		snapshot, found := provider.revisions[sha]
		if !found {
			provider.mu.Unlock()
			http.Error(w, "fixture revision unavailable", http.StatusNotFound)
			return
		}
		provider.archiveBySHA[sha]++
		provider.mu.Unlock()
		provider.archiveReads.Add(1)
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(snapshot.archive)
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *controllerJourneyGitHubProvider) ArchiveReadsFor(sha string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.archiveBySHA[sha]
}

// AddRevision freezes another controlled source under a distinct commit SHA.
// Existing revisions remain available for historical release retrieval.
func (p *controllerJourneyGitHubProvider) AddRevision(t *testing.T, sha, source string) {
	t.Helper()
	if !controllerJourneyValidSHA(sha) {
		t.Fatal("invalid controlled GitHub source revision")
	}
	revision := controllerJourneyBuildGitHubRevision(t, source, sha)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.revisions[sha]; exists {
		t.Fatal("controlled GitHub source revision already exists")
	}
	p.revisions[sha] = revision
}

// SelectRevision advances the fixture branch without mutating any stored SHA.
func (p *controllerJourneyGitHubProvider) SelectRevision(sha string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.revisions[sha]; !exists {
		return errors.New("controlled GitHub source revision is unavailable")
	}
	p.activeSHA = sha
	return nil
}

func controllerJourneyValidSHA(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for _, character := range []byte(sha) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func controllerJourneyBuildGitHubRevision(t *testing.T, source, sha string) controllerJourneyGitHubRevision {
	t.Helper()
	revision := controllerJourneyGitHubRevision{files: make(map[string][]byte)}
	var directories []string
	err := filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == source {
			return nil
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("fixture source contains a link")
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("fixture source contains a nonregular file")
		}
		contents, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		revision.files[name] = contents
		return nil
	})
	if err != nil {
		t.Fatal("prepare controlled GitHub source:", err)
	}
	sort.Strings(directories)
	fileNames := make([]string, 0, len(revision.files))
	for name := range revision.files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	root := "hosting-notes-" + sha + "/"
	write := func(name string, mode int64, kind byte, data []byte) {
		t.Helper()
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Typeflag: kind, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if len(data) != 0 {
			if _, err := tarWriter.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(root, 0755, tar.TypeDir, nil)
	for _, name := range directories {
		write(root+name+"/", 0755, tar.TypeDir, nil)
	}
	for _, name := range fileNames {
		write(root+name, 0644, tar.TypeReg, revision.files[name])
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	revision.archive = compressed.Bytes()
	return revision
}

func (p *controllerJourneyGitHubProvider) StartDevice(context.Context) (githubapp.DeviceAuthorization, error) {
	return githubapp.DeviceAuthorization{DeviceCode: "synthetic-device", UserCode: "TEST-CODE", VerificationURI: githubapp.VerificationURI, ExpiresIn: 10 * time.Minute, Interval: time.Second}, nil
}
func (p *controllerJourneyGitHubProvider) PollDevice(_ context.Context, code string) (githubapp.TokenBundle, error) {
	if code != "synthetic-device" {
		return githubapp.TokenBundle{}, &githubapp.Error{Code: "unauthorized"}
	}
	return githubapp.TokenBundle{AccessToken: controllerJourneyGitHubToken, RefreshToken: "synthetic-refresh", AccessExpiresIn: 8 * time.Hour, RefreshExpiresIn: 30 * 24 * time.Hour}, nil
}
func (p *controllerJourneyGitHubProvider) Refresh(_ context.Context, token string) (githubapp.TokenBundle, error) {
	if token != "synthetic-refresh" {
		return githubapp.TokenBundle{}, &githubapp.Error{Code: "unauthorized"}
	}
	return p.PollDevice(context.Background(), "synthetic-device")
}
func (p *controllerJourneyGitHubProvider) CurrentUser(_ context.Context, token string) (githubapp.User, error) {
	if token != controllerJourneyGitHubToken {
		return githubapp.User{}, &githubapp.Error{Code: "unauthorized"}
	}
	return githubapp.User{ID: "4242", Login: "fixture-user"}, nil
}
func (p *controllerJourneyGitHubProvider) Installations(_ context.Context, token string, page, perPage int) (githubapp.InstallationPage, error) {
	if token != controllerJourneyGitHubToken || page != 1 || perPage < 1 {
		return githubapp.InstallationPage{}, &githubapp.Error{Code: "unauthorized"}
	}
	return githubapp.InstallationPage{TotalCount: 1, Installations: []githubapp.Installation{{ID: 7, AccountLogin: "fixture", AccountType: "Organization", TargetType: "Organization", RepositorySelection: "selected"}}}, nil
}
func (p *controllerJourneyGitHubProvider) Repositories(_ context.Context, token string, installationID int64, page, perPage int) (githubapp.RepositoryPage, error) {
	if token != controllerJourneyGitHubToken || installationID != 7 || page != 1 || perPage < 1 {
		return githubapp.RepositoryPage{}, &githubapp.Error{Code: "unauthorized"}
	}
	return githubapp.RepositoryPage{TotalCount: 1, Repositories: []githubapp.Repository{controllerJourneyRepository()}}, nil
}
func controllerJourneyRepository() githubapp.Repository {
	return githubapp.Repository{ID: 17, Owner: "fixture", Name: "hosting-notes", DefaultBranch: "main"}
}
func (p *controllerJourneyGitHubProvider) Repository(_ context.Context, token string, installationID, repositoryID int64) (githubapp.Repository, error) {
	if token != controllerJourneyGitHubToken || installationID != 7 || repositoryID != 17 {
		return githubapp.Repository{}, &githubapp.Error{Code: "not_found"}
	}
	return controllerJourneyRepository(), nil
}
func (p *controllerJourneyGitHubProvider) Branches(_ context.Context, token, owner, repository string, page, perPage int) (githubapp.BranchPage, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || page != 1 || perPage < 1 {
		return githubapp.BranchPage{}, &githubapp.Error{Code: "not_found"}
	}
	p.mu.RLock()
	sha := p.activeSHA
	p.mu.RUnlock()
	return githubapp.BranchPage{Branches: []githubapp.Branch{{Name: "main", SHA: sha}}}, nil
}
func (p *controllerJourneyGitHubProvider) Branch(_ context.Context, token, owner, repository, branch string) (githubapp.Branch, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || branch != "main" {
		return githubapp.Branch{}, &githubapp.Error{Code: "not_found"}
	}
	p.mu.RLock()
	sha := p.activeSHA
	p.mu.RUnlock()
	return githubapp.Branch{Name: "main", SHA: sha}, nil
}
func (p *controllerJourneyGitHubProvider) Tree(_ context.Context, token, owner, repository, sha string) (githubapp.Tree, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" {
		return githubapp.Tree{}, &githubapp.Error{Code: "not_found"}
	}
	p.mu.RLock()
	revision, found := p.revisions[sha]
	p.mu.RUnlock()
	if !found {
		return githubapp.Tree{}, &githubapp.Error{Code: "not_found"}
	}
	entries := make([]githubapp.TreeEntry, 0, len(revision.files))
	for name, contents := range revision.files {
		entries = append(entries, githubapp.TreeEntry{Path: name, Type: "blob", Mode: "100644", Size: int64(len(contents)), SHA: sha})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return githubapp.Tree{Entries: entries}, nil
}
func (p *controllerJourneyGitHubProvider) Content(_ context.Context, token, owner, repository, name, sha string) ([]byte, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || path.Clean(name) != name {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	p.mu.RLock()
	revision, found := p.revisions[sha]
	p.mu.RUnlock()
	if !found {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	contents, found := revision.files[name]
	if !found {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	return append([]byte(nil), contents...), nil
}
func (p *controllerJourneyGitHubProvider) Archive(ctx context.Context, token, owner, repository, sha string) (io.ReadCloser, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	p.mu.RLock()
	_, found := p.revisions[sha]
	p.mu.RUnlock()
	if !found {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.server.URL+"/repos/fixture/hosting-notes/tarball/"+sha, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := p.server.Client().Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, &githubapp.Error{Code: "provider_rejected"}
	}
	return response.Body, nil
}

func TestControllerJourneyGitHubProviderRetainsImmutableRevisions(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, item := range []struct{ directory, value string }{{first, "first"}, {second, "second"}} {
		if err := os.Mkdir(item.directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(item.directory, "version.txt"), []byte(item.value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	provider := controllerJourneyNewGitHubProvider(t, first)
	const laterSHA = "dddddddddddddddddddddddddddddddddddddddd"
	provider.AddRevision(t, laterSHA, second)
	if err := provider.SelectRevision(laterSHA); err != nil {
		t.Fatal(err)
	}
	branch, err := provider.Branch(context.Background(), controllerJourneyGitHubToken, "fixture", "hosting-notes", "main")
	if err != nil || branch.SHA != laterSHA {
		t.Fatalf("controlled branch head=%q err=%v", branch.SHA, err)
	}
	for _, item := range []struct{ sha, value string }{{controllerJourneyGitHubSHA, "first"}, {laterSHA, "second"}} {
		contents, contentErr := provider.Content(context.Background(), controllerJourneyGitHubToken, "fixture", "hosting-notes", "version.txt", item.sha)
		if contentErr != nil || string(contents) != item.value {
			t.Fatalf("immutable content for %s changed: value=%q err=%v", item.sha, contents, contentErr)
		}
		tree, treeErr := provider.Tree(context.Background(), controllerJourneyGitHubToken, "fixture", "hosting-notes", item.sha)
		if treeErr != nil || len(tree.Entries) != 1 || tree.Entries[0].SHA != item.sha {
			t.Fatalf("immutable tree for %s unavailable: entries=%d err=%v", item.sha, len(tree.Entries), treeErr)
		}
		archive, archiveErr := provider.Archive(context.Background(), controllerJourneyGitHubToken, "fixture", "hosting-notes", item.sha)
		if archiveErr != nil {
			t.Fatal(archiveErr)
		}
		body, readErr := io.ReadAll(io.LimitReader(archive, 1<<20))
		closeErr := archive.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("immutable archive for %s unavailable: read=%v close=%v", item.sha, readErr, closeErr)
		}
		compressed, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		reader := tar.NewReader(compressed)
		found := false
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if header.Name != "hosting-notes-"+item.sha+"/version.txt" {
				continue
			}
			value, err := io.ReadAll(reader)
			if err != nil || string(value) != item.value {
				t.Fatalf("immutable archive for %s changed: err=%v", item.sha, err)
			}
			found = true
		}
		if closeErr := compressed.Close(); closeErr != nil || !found {
			t.Fatalf("immutable archive for %s omitted its source: close=%v found=%t", item.sha, closeErr, found)
		}
		if reads := provider.ArchiveReadsFor(item.sha); reads != 1 {
			t.Fatalf("immutable archive reads for %s=%d, want 1", item.sha, reads)
		}
	}
	if err := provider.SelectRevision("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); err == nil {
		t.Fatal("controlled provider selected an unknown revision")
	}
}
