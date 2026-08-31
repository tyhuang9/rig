package projectanalysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	maxSourceEntries  = 20_000
	maxPackageFiles   = 256
	maxPackageBytes   = int64(256 << 10)
	maxMetadataBytes  = int64(64 << 10)
	maxMigrationBytes = int64(1 << 20)
	maxTotalReadBytes = int64(16 << 20)
	maxJSONDepth      = 64
)

type snapshot struct {
	files       []File
	fileSet     map[string]File
	contents    map[string][]byte
	packages    []packageFile
	findings    []Finding
	fingerprint string
}

type packageFile struct {
	path     string
	dir      string
	manifest packageManifest
	issue    *Finding
}

type packageManifest struct {
	Name                 string
	PackageManager       string
	EnginesNode          string
	Scripts              map[string]string
	Dependencies         map[string]string
	DevDependencies      map[string]string
	OptionalDependencies map[string]string
	Workspaces           []string
}

func loadSnapshot(ctx context.Context, files []File, reader FileReader) (snapshot, error) {
	if reader == nil {
		return snapshot{}, &AnalysisError{Code: CodeReadFailed, Err: errors.New("nil file reader")}
	}
	if len(files) > maxSourceEntries {
		return snapshot{}, &AnalysisError{Code: CodeSourceTooLarge, Err: fmt.Errorf("%d entries exceeds %d", len(files), maxSourceEntries)}
	}

	normalized := slices.Clone(files)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Path < normalized[j].Path })
	seen := make(map[string]string, len(normalized))
	kept := make([]File, 0, len(normalized))
	fileSet := make(map[string]File, len(normalized))
	findings := make([]Finding, 0)
	packageCount := 0
	for _, file := range normalized {
		if err := validateRepositoryPath(file.Path); err != nil {
			return snapshot{}, &AnalysisError{Code: CodeUnsafePath, Path: file.Path, Err: err}
		}
		if file.Size < 0 {
			return snapshot{}, &AnalysisError{Code: CodeSourceChanged, Path: file.Path, Err: errors.New("negative declared size")}
		}
		key := strings.ToLower(file.Path)
		if previous, ok := seen[key]; ok {
			return snapshot{}, &AnalysisError{Code: CodeDuplicatePath, Path: file.Path, Err: fmt.Errorf("conflicts with %q", previous)}
		}
		seen[key] = file.Path
		if excludedDirectory(file.Path) {
			continue
		}
		if sensitiveFile(file.Path) {
			findings = append(findings, Finding{
				Code: "excluded_sensitive_file", Severity: "warning",
				Message: "A potentially sensitive file was excluded from analysis.", Path: file.Path,
			})
			continue
		}
		if path.Base(file.Path) == "package.json" {
			packageCount++
			if packageCount > maxPackageFiles {
				return snapshot{}, &AnalysisError{Code: CodeSourceTooLarge, Err: fmt.Errorf("package manifest count exceeds %d", maxPackageFiles)}
			}
		}
		kept = append(kept, file)
		fileSet[file.Path] = file
	}

	contents := make(map[string][]byte)
	packages := make([]packageFile, 0, packageCount)
	var totalReadBytes int64
	for _, file := range kept {
		limit, shouldRead := analysisReadLimit(file.Path)
		if !shouldRead {
			continue
		}
		if file.Size > limit {
			if path.Base(file.Path) == "package.json" {
				finding := Finding{Code: "package_json_too_large", Severity: "error", Message: "package.json exceeds the analysis size limit.", Path: file.Path}
				packages = append(packages, packageFile{path: file.Path, dir: packageDirectory(file.Path), issue: &finding})
				continue
			}
			return snapshot{}, &AnalysisError{Code: CodeFileTooLarge, Path: file.Path, Err: ErrFileTooLarge}
		}
		if totalReadBytes+file.Size > maxTotalReadBytes {
			return snapshot{}, &AnalysisError{Code: CodeSourceTooLarge, Path: file.Path, Err: fmt.Errorf("analysis read budget exceeds %d bytes", maxTotalReadBytes)}
		}
		body, err := reader.ReadFile(ctx, file.Path, limit)
		if err != nil {
			code := CodeReadFailed
			if errors.Is(err, ErrFileTooLarge) {
				code = CodeFileTooLarge
			}
			return snapshot{}, &AnalysisError{Code: code, Path: file.Path, Err: err}
		}
		totalReadBytes += int64(len(body))
		if int64(len(body)) != file.Size {
			return snapshot{}, &AnalysisError{Code: CodeSourceChanged, Path: file.Path, Err: fmt.Errorf("declared %d bytes, read %d", file.Size, len(body))}
		}
		contents[file.Path] = body
		if path.Base(file.Path) != "package.json" {
			continue
		}
		manifest, finding := decodePackageManifest(file.Path, body)
		packages = append(packages, packageFile{path: file.Path, dir: packageDirectory(file.Path), manifest: manifest, issue: finding})
	}

	fingerprint := structuralFingerprint(kept, contents)
	return snapshot{
		files: kept, fileSet: fileSet, contents: contents, packages: packages,
		findings: findings, fingerprint: fingerprint,
	}, nil
}

func validateRepositoryPath(name string) error {
	if name == "" || name != strings.TrimSpace(name) || strings.ContainsRune(name, '\x00') {
		return errors.New("path is empty or contains invalid whitespace/NUL")
	}
	if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return errors.New("path must be a slash-separated repository-relative path")
	}
	if path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") {
		return errors.New("path is not normalized")
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return errors.New("path contains an unsafe segment")
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return errors.New("path contains a control character")
			}
		}
	}
	return nil
}

func excludedDirectory(name string) bool {
	excluded := map[string]bool{
		".git": true, "node_modules": true, ".next": true, "dist": true,
		"build": true, "out": true, "coverage": true, ".turbo": true, ".cache": true,
	}
	for _, segment := range strings.Split(name, "/") {
		if excluded[segment] {
			return true
		}
	}
	return false
}

func sensitiveFile(name string) bool {
	base := strings.ToLower(path.Base(name))
	if strings.HasPrefix(base, ".env") && base != ".env.example" && base != ".env.sample" && base != ".env.template" {
		return true
	}
	if base == ".npmrc" || base == "id_rsa" || base == "id_ed25519" || strings.Contains(base, "credentials") || strings.Contains(base, "service-account") {
		return true
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx":
		return true
	default:
		return false
	}
}

func analysisReadLimit(name string) (int64, bool) {
	base := path.Base(name)
	switch base {
	case "package.json":
		return maxPackageBytes, true
	case ".nvmrc", ".node-version", "pnpm-workspace.yaml", "pnpm-workspace.yml":
		return maxMetadataBytes, true
	default:
		if migrationFingerprintPath(name) {
			return maxMigrationBytes, true
		}
		return 0, false
	}
}

func migrationFingerprintPath(name string) bool {
	base := path.Base(name)
	if base == "schema.prisma" || strings.HasPrefix(base, "drizzle.config.") || strings.HasPrefix(base, "knexfile.") {
		return true
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "migrations" || segment == "drizzle" {
			return true
		}
	}
	return false
}

func packageDirectory(manifestPath string) string {
	dir := path.Dir(manifestPath)
	if dir == "." {
		return ""
	}
	return dir
}

func structuralFingerprint(files []File, contents map[string][]byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("rig-source-analysis-v1\n"))
	for _, file := range files {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", file.Path, file.Size)
		if body, ok := contents[file.Path]; ok {
			sum := sha256.Sum256(body)
			_, _ = hash.Write(sum[:])
		}
		_, _ = hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func decodePackageManifest(name string, body []byte) (packageManifest, *Finding) {
	code, err := validateStrictJSON(body, maxJSONDepth)
	if err != nil {
		findingCode := code
		if findingCode == "" {
			findingCode = "malformed_package_json"
		}
		finding := Finding{Code: findingCode, Severity: "error", Message: "package.json is not valid for safe analysis.", Path: name}
		return packageManifest{}, &finding
	}
	var raw struct {
		Name                 string            `json:"name"`
		PackageManager       string            `json:"packageManager"`
		Engines              map[string]string `json:"engines"`
		Scripts              map[string]string `json:"scripts"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		Workspaces           json.RawMessage   `json:"workspaces"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		finding := Finding{Code: "malformed_package_json", Severity: "error", Message: "package.json fields have invalid types.", Path: name}
		return packageManifest{}, &finding
	}
	workspaces, err := decodeWorkspaces(raw.Workspaces)
	if err != nil {
		finding := Finding{Code: "malformed_workspaces", Severity: "error", Message: "package.json workspaces must be an array or packages array.", Path: name, Field: "workspaces"}
		return packageManifest{}, &finding
	}
	return packageManifest{
		Name: raw.Name, PackageManager: raw.PackageManager, EnginesNode: raw.Engines["node"],
		Scripts: nonnilMap(raw.Scripts), Dependencies: nonnilMap(raw.Dependencies),
		DevDependencies: nonnilMap(raw.DevDependencies), OptionalDependencies: nonnilMap(raw.OptionalDependencies),
		Workspaces: workspaces,
	}, nil
}

func decodeWorkspaces(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return sortedUniqueNonempty(list), nil
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &object); err != nil || object.Packages == nil {
		return nil, errors.New("invalid workspaces")
	}
	return sortedUniqueNonempty(object.Packages), nil
}

func pnpmWorkspacePatterns(body []byte) ([]string, error) {
	var document struct {
		Packages []string `yaml:"packages"`
	}
	if err := yaml.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	return sortedUniqueNonempty(document.Packages), nil
}

func nonnilMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	return input
}

func sortedUniqueNonempty(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
