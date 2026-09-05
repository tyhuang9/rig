package generatedimage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultContextBytes   = int64(256 << 20)
	defaultContextEntries = 20_000
)

var normalizedBuildTime = time.Unix(0, 0).UTC()

var (
	errInvalidBuildContext  = errors.New("invalid build context")
	errBuildContextTooLarge = errors.New("build context too large")
)

type contextLimits struct {
	bytes   int64
	entries int
}

type buildLayout struct {
	contextDirectory string
	containerfile    string
	imageIDFile      string
	installCommand   string
	buildCommand     string
}

func prepareBuildContext(ctx context.Context, workspace, operationDirectory string, component componentDefinition, limits contextLimits) (buildLayout, error) {
	if !validComponentRoot(component.rootDirectory) || !validComponentRoot(component.installDirectory) {
		return buildLayout{}, errInvalidBuildContext
	}
	if limits.bytes <= 0 {
		limits.bytes = defaultContextBytes
	}
	if limits.entries <= 0 {
		limits.entries = defaultContextEntries
	}
	layout := buildLayout{
		contextDirectory: filepath.Join(operationDirectory, "context"),
		containerfile:    filepath.Join(operationDirectory, "Containerfile"),
		imageIDFile:      filepath.Join(operationDirectory, "image.id"),
		installCommand:   filepath.Join(operationDirectory, "install.command"),
		buildCommand:     filepath.Join(operationDirectory, "build.command"),
	}
	for _, directory := range []string{layout.contextDirectory, filepath.Join(layout.contextDirectory, "source"), filepath.Join(layout.contextDirectory, "rig")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return buildLayout{}, err
		}
	}
	staticOutput, err := selectedStaticOutput(component)
	if err != nil {
		return buildLayout{}, err
	}
	if err := copySanitizedTree(ctx, workspace, filepath.Join(layout.contextDirectory, "source"), limits, staticOutput); err != nil {
		return buildLayout{}, err
	}
	root := filepath.Join(layout.contextDirectory, "source", filepath.FromSlash(component.rootDirectory))
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || generatedImagePathIsReparsePoint(root) {
		return buildLayout{}, errInvalidBuildContext
	}
	installRoot := filepath.Join(layout.contextDirectory, "source", filepath.FromSlash(component.installDirectory))
	if info, err := os.Lstat(installRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || generatedImagePathIsReparsePoint(installRoot) {
		return buildLayout{}, errInvalidBuildContext
	}
	if component.installBehavior != "" {
		if err := writeBuildFile(layout.installCommand, []byte(component.installBehavior), 0o600); err != nil {
			return buildLayout{}, err
		}
	}
	if component.buildCommand != "" {
		if err := writeBuildFile(layout.buildCommand, []byte(component.buildCommand), 0o600); err != nil {
			return buildLayout{}, err
		}
	}
	for name, body := range map[string][]byte{
		"rig-entrypoint": []byte(entrypointScript),
		"rig-static":     []byte(staticLauncherScript),
		"rig-static.mjs": []byte(staticServerScript),
	} {
		if err := writeBuildFile(filepath.Join(layout.contextDirectory, "rig", name), body, 0o700); err != nil {
			return buildLayout{}, err
		}
	}
	return layout, nil
}

func validComponentRoot(value string) bool {
	if value == "." {
		return true
	}
	return value != "" && !strings.Contains(value, `\`) && !strings.Contains(value, ":") && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func selectedStaticOutput(component componentDefinition) (string, error) {
	if component.staticOutputDirectory == "" {
		return "", nil
	}
	if !validComponentRoot(component.staticOutputDirectory) {
		return "", errInvalidBuildContext
	}
	output := component.staticOutputDirectory
	if component.rootDirectory != "." {
		output = path.Join(component.rootDirectory, output)
	}
	if unsafeStaticOutputDirectory(output) {
		return "", errInvalidBuildContext
	}
	if component.buildCommand != "" {
		return "", nil
	}
	if hasStaticArtifactDirectory(output) && !prebuiltStaticOutputDirectory(output) {
		return "", errInvalidBuildContext
	}
	if !prebuiltStaticOutputDirectory(output) {
		return "", nil
	}
	return output, nil
}

func prebuiltStaticOutputDirectory(value string) bool {
	segments := strings.Split(value, "/")
	artifactRoot := false
	for _, segment := range segments {
		switch strings.ToLower(segment) {
		case "dist", "build", "out":
			if artifactRoot {
				return false
			}
			artifactRoot = true
		case ".git", ".hg", ".svn", ".rig", ".hostd", "node_modules", ".next", ".nuxt", ".svelte-kit", "coverage", ".turbo", ".cache":
			return false
		}
	}
	return artifactRoot
}

func hasStaticArtifactDirectory(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		switch strings.ToLower(segment) {
		case "dist", "build", "out":
			return true
		}
	}
	return false
}

func unsafeStaticOutputDirectory(value string) bool {
	segments := strings.Split(strings.ToLower(value), "/")
	for index, segment := range segments {
		if sensitiveStaticOutputSegment(segment) {
			return true
		}
		switch segment {
		case ".git", ".hg", ".svn", ".rig", ".hostd", "node_modules", ".next", ".nuxt", ".svelte-kit", "coverage", ".turbo", ".cache", ".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker":
			return true
		case ".config":
			if index+1 < len(segments) {
				switch segments[index+1] {
				case "gcloud", "gh", "hub":
					return true
				}
			}
		}
	}
	return false
}

func sensitiveStaticOutputSegment(name string) bool {
	if strings.HasPrefix(name, ".env") {
		return true
	}
	switch name {
	case ".netrc", ".git-credentials", "credentials", "credentials.json", "auth.json", ".pypirc", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	if strings.HasPrefix(name, "service-account") && strings.HasSuffix(name, ".json") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func copySanitizedTree(ctx context.Context, sourceRoot, destinationRoot string, limits contextLimits, staticOutput string) error {
	root, err := filepath.Abs(sourceRoot)
	if err != nil || filepath.Clean(sourceRoot) != root {
		return errInvalidBuildContext
	}
	seen := map[string]struct{}{}
	var total int64
	entries := 0
	err = filepath.WalkDir(root, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if source == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || generatedImagePathIsReparsePoint(source) {
			return errInvalidBuildContext
		}
		relative, err := filepath.Rel(root, source)
		if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errInvalidBuildContext
		}
		canonical := filepath.ToSlash(relative)
		key := strings.ToLower(canonical)
		if _, duplicate := seen[key]; duplicate {
			return errInvalidBuildContext
		}
		seen[key] = struct{}{}
		excluded, rejected := classifiedBuildPath(canonical, entry.IsDir(), staticOutput)
		if rejected {
			return errInvalidBuildContext
		}
		if excluded {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entries++
		if entries > limits.entries {
			return errBuildContextTooLarge
		}
		target := filepath.Join(destinationRoot, filepath.FromSlash(canonical))
		if entry.IsDir() {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			return os.Chtimes(target, normalizedBuildTime, normalizedBuildTime)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
			return errInvalidBuildContext
		}
		total += info.Size()
		if total > limits.bytes {
			return errBuildContextTooLarge
		}
		if err := copyBuildFile(source, target, canonical, info); err != nil {
			return err
		}
		return nil
	})
	return err
}

func classifiedBuildPath(canonical string, directory bool, staticOutput string) (excluded, rejected bool) {
	canonical = strings.ToLower(canonical)
	staticOutput = strings.ToLower(staticOutput)
	if artifactRoot := prebuiltStaticArtifactRoot(staticOutput); artifactRoot != "" && (canonical == artifactRoot || strings.HasPrefix(canonical, artifactRoot+"/")) {
		if canonical == staticOutput || strings.HasPrefix(staticOutput, canonical+"/") {
			// Keep only the selected output's ancestors. Their ordinary artifact
			// names would otherwise stop WalkDir before it reaches the selection.
			return false, false
		}
		if !strings.HasPrefix(canonical, staticOutput+"/") {
			return true, false
		}
	}
	segments := strings.Split(canonical, "/")
	name := segments[len(segments)-1]
	if directory {
		switch name {
		case "dist", "build", "out":
			if canonical == staticOutput {
				return false, false
			}
			return true, false
		case ".git", ".hg", ".svn", ".rig", ".hostd", "node_modules", ".next", ".nuxt", ".svelte-kit", "coverage", ".turbo":
			return true, false
		case ".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker":
			return false, true
		}
		if len(segments) >= 2 && segments[len(segments)-2] == ".config" && (name == "gcloud" || name == "gh" || name == "hub") {
			return false, true
		}
		return false, false
	}
	if strings.HasPrefix(name, ".env") {
		return true, false
	}
	if name == ".netrc" || name == ".git-credentials" || name == "credentials" || name == "credentials.json" || name == "auth.json" || name == ".pypirc" || name == "id_rsa" || name == "id_dsa" || name == "id_ecdsa" || name == "id_ed25519" || strings.HasPrefix(name, "service-account") && strings.HasSuffix(name, ".json") {
		return false, true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx"} {
		if strings.HasSuffix(name, suffix) {
			return false, true
		}
	}
	return false, false
}

func prebuiltStaticArtifactRoot(value string) string {
	segments := strings.Split(strings.ToLower(value), "/")
	for index, segment := range segments {
		switch segment {
		case "dist", "build", "out":
			return strings.Join(segments[:index+1], "/")
		}
	}
	return ""
}

func copyBuildFile(source, target, canonical string, before os.FileInfo) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() != before.Size() {
		return errors.New("source file changed")
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()
	written, err := io.Copy(output, io.LimitReader(input, before.Size()+1))
	if err != nil || written != before.Size() {
		return errors.New("copy source file")
	}
	after, err := input.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		return errors.New("source file changed")
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if protectedCredentialFile(target, canonical) {
		return errInvalidBuildContext
	}
	mode := os.FileMode(0o600)
	if before.Mode().Perm()&0o111 != 0 {
		mode = 0o700
	}
	if err := os.Chmod(target, mode); err != nil {
		return err
	}
	if err := os.Chtimes(target, normalizedBuildTime, normalizedBuildTime); err != nil {
		return err
	}
	ok = true
	return nil
}

func protectedCredentialFile(target, canonical string) bool {
	file, err := os.Open(target)
	if err != nil {
		return true
	}
	defer file.Close()
	name := strings.ToLower(filepath.Base(filepath.FromSlash(canonical)))
	packageConfig := name == ".npmrc" || name == ".yarnrc" || name == ".yarnrc.yml" || name == ".pnpmrc"
	markers := [][]byte{[]byte("-----begin "), []byte(" private key-----")}
	if packageConfig {
		markers = append(markers, []byte("_authtoken"), []byte("npmauthtoken"), []byte("npmauthident"), []byte("_auth="), []byte("password="), []byte("password:"), []byte("username="), []byte("username:"))
	}
	buffer := make([]byte, 64<<10)
	tail := make([]byte, 0, 128)
	for {
		count, readErr := file.Read(buffer)
		window := append(tail, buffer[:count]...)
		lower := bytes.ToLower(window)
		privateKey := bytes.Contains(lower, markers[0]) && bytes.Contains(lower, markers[1])
		credential := false
		for _, marker := range markers[2:] {
			credential = credential || bytes.Contains(lower, marker)
		}
		clear(lower)
		if privateKey || credential {
			clear(window)
			clear(buffer)
			return true
		}
		if readErr != nil {
			clear(window)
			clear(buffer)
			return readErr != io.EOF
		}
		const overlap = 128
		start := len(window) - overlap
		if start < 0 {
			start = 0
		}
		nextTail := append([]byte(nil), window[start:]...)
		clear(window)
		clear(tail)
		tail = nextTail
	}
}

func writeBuildFile(path string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(path, normalizedBuildTime, normalizedBuildTime); err != nil {
		return err
	}
	ok = true
	return nil
}
