package sourceinspection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeReader struct {
	tree         githubapp.Tree
	content      []byte
	repository   sourceconnections.SourceRepository
	branch       sourceconnections.Branch
	contentReads int
}

func (reader *fakeReader) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	return reader.repository, reader.branch, nil
}
func (reader *fakeReader) ReadTree(context.Context, string, string, int64, string) (githubapp.Tree, error) {
	return reader.tree, nil
}
func (reader *fakeReader) ReadContent(_ context.Context, _ string, _ string, _ int64, path, _ string) ([]byte, error) {
	reader.contentReads++
	return reader.content, nil
}

func TestInspectGithubResolvesRenameAndReadsOnlySelectedCompose(t *testing.T) {
	reader := &fakeReader{repository: sourceconnections.SourceRepository{ID: 9, Owner: "new-owner", Name: "renamed"}, branch: sourceconnections.Branch{Name: "feature/slash", SHA: testSHA}, tree: githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: testSHA}, {Path: "deploy/compose.yml", Type: "blob", SHA: testSHA}, {Path: "README.md", Type: "blob", SHA: testSHA}}}, content: []byte("services:\n  api:\n    image: example/api\n  worker:\n    build:\n      context: ../worker\n")}
	result, err := InspectGitHub(context.Background(), reader, "owner", GitHubSource{ConnectionID: "connection", InstallationID: 7, RepositoryID: 9, Branch: "feature/slash", ComposePath: "deploy/compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source.RepositoryOwner != "new-owner" || result.Source.TrackedRef != "refs/heads/feature/slash" || result.ResolvedSHA != testSHA {
		t.Fatalf("source=%#v sha=%s", result.Source, result.ResolvedSHA)
	}
	if reader.contentReads != 1 || len(result.ComposeCandidates) != 2 || len(result.Services) != 2 {
		t.Fatalf("result=%#v reads=%d", result, reader.contentReads)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("findings=%#v", result.Findings)
	}
}

func TestInspectGithubRejectsUnsafeOrOversizedTreeWithoutContentRead(t *testing.T) {
	for name, testCase := range map[string]struct {
		tree githubapp.Tree
		code string
	}{
		"truncated": {githubapp.Tree{Truncated: true}, "source_too_large"},
		"traversal": {githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "../compose.yaml", Type: "blob", SHA: testSHA}}}, "invalid_source"},
		"absolute":  {githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "/compose.yaml", Type: "blob", SHA: testSHA}}}, "invalid_source"},
		"drive":     {githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "C:/compose.yaml", Type: "blob", SHA: testSHA}}}, "invalid_source"},
		"duplicate": {githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: testSHA}, {Path: "compose.yaml", Type: "tree", SHA: testSHA}}}, "invalid_source"},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{repository: sourceconnections.SourceRepository{ID: 9, Owner: "o", Name: "r"}, branch: sourceconnections.Branch{Name: "main", SHA: testSHA}, tree: testCase.tree}
			_, err := InspectGitHub(context.Background(), reader, "owner", GitHubSource{ConnectionID: "c", InstallationID: 1, RepositoryID: 9, Branch: "main"})
			if !IsCode(err, testCase.code) {
				t.Fatalf("error=%v", err)
			}
			if reader.contentReads != 0 {
				t.Fatal("content read")
			}
		})
	}
}

func TestInspectGithubComposeFindings(t *testing.T) {
	tests := map[string]struct {
		content string
		want    string
	}{
		"malformed": {"services: [", "malformed_compose"},
		"remote":    {"services:\n  app:\n    build: https://example.invalid/repo.git\n", "unsupported_remote_resource"},
		"escape":    {"services:\n  app:\n    build: ../../../outside\n", "workspace_escape"},
		"include":   {"include: other.yaml\nservices:\n  app:\n    image: example\n", "unsupported_compose_include"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{repository: sourceconnections.SourceRepository{ID: 9, Owner: "o", Name: "r"}, branch: sourceconnections.Branch{Name: "main", SHA: testSHA}, tree: githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: testSHA}}}, content: []byte(test.content)}
			result, err := InspectGitHub(context.Background(), reader, "owner", GitHubSource{ConnectionID: "c", InstallationID: 1, RepositoryID: 9, Branch: "main"})
			if err != nil {
				t.Fatal(err)
			}
			if !hasFinding(result.Findings, test.want) {
				t.Fatalf("findings=%#v", result.Findings)
			}
		})
	}
}

func TestInspectGithubRequiresComposeSelectionAndRejectsSelectedTraversal(t *testing.T) {
	reader := &fakeReader{repository: sourceconnections.SourceRepository{ID: 9, Owner: "o", Name: "r"}, branch: sourceconnections.Branch{Name: "main", SHA: testSHA}, tree: githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: testSHA}, {Path: "deploy/compose.yaml", Type: "blob", SHA: testSHA}}}}
	result, err := InspectGitHub(context.Background(), reader, "owner", GitHubSource{ConnectionID: "c", InstallationID: 1, RepositoryID: 9, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(result.Findings, "compose_selection_required") || reader.contentReads != 0 {
		t.Fatalf("result=%#v", result)
	}
	if _, err := InspectGitHub(context.Background(), reader, "owner", GitHubSource{ConnectionID: "c", InstallationID: 1, RepositoryID: 9, Branch: "main", ComposePath: "../compose.yaml"}); !IsCode(err, "invalid_source") {
		t.Fatalf("error=%v", err)
	}
}

func TestInspectLocalDiscoversAndParsesCompose(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := InspectLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source.Type != "local" || len(result.Services) != 1 || result.Services[0].Name != "web" {
		t.Fatalf("result=%#v", result)
	}
}

func TestInspectLocalRejectsSourceSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(target, []byte("services:\n  app:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-compose.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := InspectLocal(link); !IsCode(err, "invalid_source") {
		t.Fatalf("error=%v", err)
	}
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
