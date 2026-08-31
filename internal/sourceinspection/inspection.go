package sourceinspection

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/sourceconnections"
	"gopkg.in/yaml.v3"
)

const maxLocalEntries = 10000

type Error struct{ Code string }

func (err *Error) Error() string { return "source inspection: " + err.Code }
func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

type GitHubSource struct {
	ConnectionID   string
	InstallationID int64
	RepositoryID   int64
	Branch         string
	ComposePath    string
}

type SourceMetadata struct {
	Type            string
	Path            string
	ConnectionID    string
	InstallationID  int64
	RepositoryID    int64
	RepositoryOwner string
	RepositoryName  string
	TrackedBranch   string
	TrackedRef      string
	ComposePath     string
}

type Finding struct{ Code, Message, Path string }
type Service struct{ Name, Image, BuildContext string }
type Result struct {
	Source            SourceMetadata
	ResolvedSHA       string
	ComposeCandidates []string
	Services          []Service
	Findings          []Finding
}

type GitHubReader interface {
	Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error)
	ReadTree(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (githubapp.Tree, error)
	ReadContent(context.Context, string, string, int64, sourceconnections.SourceRepository, string, string) ([]byte, error)
}

func InspectGitHub(ctx context.Context, reader GitHubReader, owner string, source GitHubSource) (Result, error) {
	composePath, err := normalizeOptionalPath(source.ComposePath)
	if err != nil {
		return Result{}, &Error{Code: "invalid_source"}
	}
	repository, branch, err := reader.Resolve(ctx, owner, source.ConnectionID, source.InstallationID, source.RepositoryID, source.Branch)
	if err != nil {
		return Result{}, err
	}
	tree, err := reader.ReadTree(ctx, owner, source.ConnectionID, source.InstallationID, repository, branch.SHA)
	if err != nil {
		return Result{}, err
	}
	if tree.Truncated {
		return Result{}, &Error{Code: "source_too_large"}
	}
	result := Result{Source: SourceMetadata{Type: "github", ConnectionID: source.ConnectionID, InstallationID: source.InstallationID, RepositoryID: repository.ID, RepositoryOwner: repository.Owner, RepositoryName: repository.Name, TrackedBranch: branch.Name, TrackedRef: "refs/heads/" + branch.Name}, ResolvedSHA: branch.SHA}
	blobs := make(map[string]githubapp.TreeEntry)
	seenPaths := make(map[string]struct{}, len(tree.Entries))
	for _, entry := range tree.Entries {
		normalized, normalizeErr := normalizePath(entry.Path)
		if normalizeErr != nil || normalized != entry.Path {
			return Result{}, &Error{Code: "invalid_source"}
		}
		if _, exists := seenPaths[entry.Path]; exists {
			return Result{}, &Error{Code: "invalid_source"}
		}
		seenPaths[entry.Path] = struct{}{}
		if entry.Type == "commit" {
			result.Findings = append(result.Findings, Finding{Code: "unsupported_submodule", Message: "Git submodules are not supported", Path: entry.Path})
		}
		if entry.Type == "blob" {
			blobs[entry.Path] = entry
			if isComposeName(path.Base(entry.Path)) {
				result.ComposeCandidates = append(result.ComposeCandidates, entry.Path)
			}
		}
	}
	sort.Strings(result.ComposeCandidates)
	selected, findings := selectCompose(composePath, result.ComposeCandidates, blobs)
	result.Findings = append(result.Findings, findings...)
	if selected == "" {
		return result, nil
	}
	result.Source.ComposePath = selected
	contents, err := reader.ReadContent(ctx, owner, source.ConnectionID, source.InstallationID, repository, selected, branch.SHA)
	if err != nil {
		return Result{}, err
	}
	services, composeFindings := inspectCompose(contents, selected)
	result.Services = services
	result.Findings = append(result.Findings, composeFindings...)
	return result, nil
}

func InspectLocal(sourcePath string) (Result, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" || pathsecurity.RejectWindowsNamespace(sourcePath) {
		return Result{}, &Error{Code: "invalid_source"}
	}
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return Result{}, &Error{Code: "invalid_source"}
	}
	linkInfo, err := os.Lstat(absolute)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return Result{}, &Error{Code: "invalid_source"}
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Result{}, &Error{Code: "invalid_source"}
	}
	result := Result{Source: SourceMetadata{Type: "local", Path: absolute}}
	root, selected := absolute, ""
	if !info.IsDir() {
		root, selected = filepath.Dir(absolute), filepath.Base(absolute)
		if !isComposeName(selected) {
			return Result{}, &Error{Code: "invalid_source"}
		}
	}
	entries := 0
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxLocalEntries {
			return &Error{Code: "source_too_large"}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !isComposeName(entry.Name()) {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		result.ComposeCandidates = append(result.ComposeCandidates, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		var inspectionErr *Error
		if errors.As(err, &inspectionErr) {
			return Result{}, err
		}
		return Result{}, &Error{Code: "invalid_source"}
	}
	sort.Strings(result.ComposeCandidates)
	if selected == "" {
		selected, result.Findings = selectCompose("", result.ComposeCandidates, nil)
	} else {
		result.Source.ComposePath = filepath.ToSlash(selected)
	}
	if selected == "" {
		return result, nil
	}
	result.Source.ComposePath = filepath.ToSlash(selected)
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(selected)))
	if err != nil {
		return Result{}, &Error{Code: "invalid_source"}
	}
	if len(contents) > 1<<20 {
		return Result{}, &Error{Code: "source_too_large"}
	}
	result.Services, result.Findings = inspectCompose(contents, result.Source.ComposePath)
	return result, nil
}

func selectCompose(requested string, candidates []string, blobs map[string]githubapp.TreeEntry) (string, []Finding) {
	if requested != "" {
		if blobs != nil {
			if _, ok := blobs[requested]; !ok {
				return "", []Finding{{Code: "compose_not_found", Message: "Selected Compose file was not found", Path: requested}}
			}
		}
		if !isComposeName(path.Base(requested)) {
			return "", []Finding{{Code: "invalid_compose_path", Message: "Selected file is not a supported Compose filename", Path: requested}}
		}
		return requested, nil
	}
	switch len(candidates) {
	case 0:
		return "", []Finding{{Code: "compose_not_found", Message: "No Compose file was found"}}
	case 1:
		return candidates[0], nil
	default:
		return "", []Finding{{Code: "compose_selection_required", Message: "Select one Compose file"}}
	}
}

func inspectCompose(contents []byte, composePath string) ([]Service, []Finding) {
	var document yaml.Node
	if len(contents) == 0 || yaml.Unmarshal(contents, &document) != nil {
		return nil, []Finding{{Code: "malformed_compose", Message: "Compose YAML could not be parsed", Path: composePath}}
	}
	root := mappingRoot(&document)
	if root == nil {
		return nil, []Finding{{Code: "malformed_compose", Message: "Compose document must be a mapping", Path: composePath}}
	}
	servicesNode := mapValue(root, "services")
	if servicesNode == nil || servicesNode.Kind != yaml.MappingNode || len(servicesNode.Content) == 0 {
		return nil, []Finding{{Code: "missing_services", Message: "Compose document must define services", Path: composePath}}
	}
	base := path.Dir(composePath)
	if base == "." {
		base = ""
	}
	services := make([]Service, 0, len(servicesNode.Content)/2)
	findings := []Finding{}
	for index := 0; index < len(servicesNode.Content); index += 2 {
		name, value := servicesNode.Content[index].Value, servicesNode.Content[index+1]
		if name == "" || value.Kind != yaml.MappingNode {
			findings = append(findings, Finding{Code: "malformed_service", Message: "Compose service must be a mapping", Path: composePath})
			continue
		}
		service := Service{Name: name}
		if image := mapValue(value, "image"); image != nil && image.Kind == yaml.ScalarNode {
			service.Image = image.Value
		}
		if build := mapValue(value, "build"); build != nil {
			contextValue := ""
			if build.Kind == yaml.ScalarNode {
				contextValue = build.Value
			} else if build.Kind == yaml.MappingNode {
				if contextNode := mapValue(build, "context"); contextNode != nil && contextNode.Kind == yaml.ScalarNode {
					contextValue = contextNode.Value
				}
			}
			service.BuildContext = contextValue
			if contextValue != "" {
				findings = append(findings, validateResourcePath(base, contextValue, "build_context")...)
			}
		}
		services = append(services, service)
	}
	if mapValue(root, "include") != nil {
		findings = append(findings, Finding{Code: "unsupported_compose_include", Message: "Compose includes are not supported", Path: composePath})
	}
	for _, section := range []string{"configs", "secrets"} {
		if node := mapValue(root, section); node != nil && node.Kind == yaml.MappingNode {
			for i := 1; i < len(node.Content); i += 2 {
				if file := mapValue(node.Content[i], "file"); file != nil && file.Kind == yaml.ScalarNode {
					findings = append(findings, validateResourcePath(base, file.Value, section+"_file")...)
				}
			}
		}
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return services, findings
}

func validateResourcePath(base, value, code string) []Finding {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") || strings.HasPrefix(value, "git@") {
		return []Finding{{Code: "unsupported_remote_resource", Message: "Remote Compose resources are not supported", Path: value}}
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") || (len(value) > 1 && value[1] == ':') {
		return []Finding{{Code: "workspace_escape", Message: "Compose resource escapes the repository workspace", Path: value}}
	}
	joined := path.Join(base, strings.ReplaceAll(value, "\\", "/"))
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return []Finding{{Code: "workspace_escape", Message: "Compose resource escapes the repository workspace", Path: value}}
	}
	return nil
}

func mappingRoot(document *yaml.Node) *yaml.Node {
	if document.Kind == yaml.DocumentNode && len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode {
		return document.Content[0]
	}
	return nil
}
func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
func isComposeName(name string) bool {
	switch strings.ToLower(name) {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	}
	return false
}
func normalizeOptionalPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	return normalizePath(value)
}
func normalizePath(value string) (string, error) {
	if strings.Contains(value, "\\") || strings.Contains(value, ":") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("unsafe path")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", fmt.Errorf("unsafe path")
	}
	return clean, nil
}
