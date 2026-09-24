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
}

func controllerJourneyNewGitHubProvider(t *testing.T, source string) *controllerJourneyGitHubProvider {
	t.Helper()
	provider := &controllerJourneyGitHubProvider{files: make(map[string][]byte)}
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
		provider.files[name] = contents
		return nil
	})
	if err != nil {
		t.Fatal("prepare controlled GitHub source:", err)
	}
	sort.Strings(directories)
	fileNames := make([]string, 0, len(provider.files))
	for name := range provider.files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	root := "hosting-notes-" + controllerJourneyGitHubSHA + "/"
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
		write(root+name, 0644, tar.TypeReg, provider.files[name])
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	provider.archive = compressed.Bytes()
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/fixture/hosting-notes/tarball/"+controllerJourneyGitHubSHA ||
			r.Header.Get("Authorization") != "Bearer "+controllerJourneyGitHubToken {
			http.Error(w, "fixture request rejected", http.StatusForbidden)
			return
		}
		provider.archiveReads.Add(1)
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(provider.archive)
	}))
	t.Cleanup(provider.server.Close)
	return provider
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
	return githubapp.BranchPage{Branches: []githubapp.Branch{{Name: "main", SHA: controllerJourneyGitHubSHA}}}, nil
}
func (p *controllerJourneyGitHubProvider) Branch(_ context.Context, token, owner, repository, branch string) (githubapp.Branch, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || branch != "main" {
		return githubapp.Branch{}, &githubapp.Error{Code: "not_found"}
	}
	return githubapp.Branch{Name: "main", SHA: controllerJourneyGitHubSHA}, nil
}
func (p *controllerJourneyGitHubProvider) Tree(_ context.Context, token, owner, repository, sha string) (githubapp.Tree, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || sha != controllerJourneyGitHubSHA {
		return githubapp.Tree{}, &githubapp.Error{Code: "not_found"}
	}
	entries := make([]githubapp.TreeEntry, 0, len(p.files))
	for name, contents := range p.files {
		entries = append(entries, githubapp.TreeEntry{Path: name, Type: "blob", Mode: "100644", Size: int64(len(contents)), SHA: controllerJourneyGitHubSHA})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return githubapp.Tree{Entries: entries}, nil
}
func (p *controllerJourneyGitHubProvider) Content(_ context.Context, token, owner, repository, name, sha string) ([]byte, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || sha != controllerJourneyGitHubSHA || path.Clean(name) != name {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	contents, found := p.files[name]
	if !found {
		return nil, &githubapp.Error{Code: "not_found"}
	}
	return append([]byte(nil), contents...), nil
}
func (p *controllerJourneyGitHubProvider) Archive(ctx context.Context, token, owner, repository, sha string) (io.ReadCloser, error) {
	if token != controllerJourneyGitHubToken || owner != "fixture" || repository != "hosting-notes" || sha != controllerJourneyGitHubSHA {
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
