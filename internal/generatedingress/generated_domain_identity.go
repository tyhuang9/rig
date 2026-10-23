package generatedingress

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	controldatabase "github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	generatedDaemonIdentityOutputLimit = 1024
	generatedDaemonIdentityMaximum     = 512
)

type generatedDomainPhysicalIdentity struct {
	manager      generatedManagerPhysicalIdentity
	databaseFile os.FileInfo
	database     *sql.DB
}

type generatedManagerPhysicalIdentity struct {
	dataRoot    os.FileInfo
	ingressRoot os.FileInfo
	daemonID    string
	namespace   generatedGatewayNamespace
}

type generatedGatewayNamespace struct {
	container string
	volume    string
	network   string
}

// generatedDomainPathProof retains only read-only file identities and their
// fixed paths. It grants no filesystem or effect authority.
type generatedDomainPathProof struct {
	dataRootPath string
	databasePath string
	ingressPath  string
	dataRoot     os.FileInfo
	databaseFile os.FileInfo
	ingressRoot  os.FileInfo
	ancestors    map[string]os.FileInfo
}

type generatedDomainOwnedPathTransition struct {
	final   generatedDomainPathProof
	created map[string]os.FileInfo
}

func fixedGeneratedGatewayNamespace() generatedGatewayNamespace {
	return generatedGatewayNamespace{
		container: caddyContainerName,
		volume:    caddyVolumeName,
		network:   caddyNetworkName,
	}
}

func inspectGeneratedDomainPhysicalIdentity(
	ctx context.Context,
	stores *deployments.GeneratedOwnerStores,
	runner runtimeprocess.CommandRunner,
	options generatedDomainOptions,
) (generatedDomainPhysicalIdentity, generatedDomainPathProof, error) {
	if ctx == nil || stores == nil || !generatedCandidatePointerIdentity(runner) {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain identity configuration is invalid")
	}
	fixedStores := *stores
	stores = &fixedStores
	if !stores.ConfiguredForDomain(options.DataRoot, options.WorkingDirectory) ||
		!validGeneratedDomainIdentityOptions(options) {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain identity configuration is invalid")
	}
	if err := ctx.Err(); err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, err
	}
	dockerEnv, err := dockerEnvironment(options.DockerEndpoint, options.DockerConfigDirectory)
	if err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain Docker environment is invalid")
	}
	database := stores.Database()
	provenDatabaseFile, err := controldatabase.GeneratedDomainFileIdentity(ctx, database, options.DataRoot)
	if err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain database provenance is invalid")
	}
	first, err := inspectGeneratedDomainPaths(options.DataRoot)
	if err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, err
	}
	if !sameGeneratedDomainFileIdentity(first.databaseFile, provenDatabaseFile) {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain database provenance is invalid")
	}
	mainPath, err := inspectGeneratedDomainMainDatabase(ctx, database)
	if err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, err
	}
	mainFile, err := inspectGeneratedDomainRegularFile(mainPath)
	if err != nil || !os.SameFile(first.databaseFile, mainFile) {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain database identity is invalid")
	}
	second, err := inspectGeneratedDomainPaths(options.DataRoot)
	if err != nil || !sameGeneratedDomainPathObservation(first, second, false) {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain path identity changed")
	}
	provenDatabaseFile, err = controldatabase.GeneratedDomainFileIdentity(ctx, database, options.DataRoot)
	if err != nil || !sameGeneratedDomainFileIdentity(second.databaseFile, provenDatabaseFile) {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain database provenance changed")
	}
	if err := generatedCandidateValidateCommandDirectories(generatedDomainCandidateOptions(options)); err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, errors.New("generated domain command directories changed")
	}
	daemonID, err := inspectGeneratedDockerDaemonIdentity(
		ctx, runner, options.DockerExecutable, dockerEnv, options.CommandTimeout,
	)
	if err != nil {
		return generatedDomainPhysicalIdentity{}, generatedDomainPathProof{}, err
	}
	identity := generatedDomainPhysicalIdentity{
		manager: generatedManagerPhysicalIdentity{
			dataRoot: second.dataRoot, ingressRoot: second.ingressRoot,
			daemonID: daemonID, namespace: fixedGeneratedGatewayNamespace(),
		},
		databaseFile: second.databaseFile,
		database:     database,
	}
	return identity, second, nil
}

func inspectGeneratedDomainPaths(dataRoot string) (generatedDomainPathProof, error) {
	store, present, ancestors, err := inspectStateStoreReadOnly(dataRoot)
	if err != nil {
		return generatedDomainPathProof{}, errors.New("generated domain protected path is unsafe or unreadable")
	}
	dataRootInfo := ancestors[dataRoot]
	if dataRootInfo == nil || !dataRootInfo.IsDir() || dataRootInfo.Mode()&os.ModeSymlink != 0 ||
		generatedIngressPathIsReparsePoint(dataRoot) || !os.SameFile(dataRootInfo, dataRootInfo) {
		return generatedDomainPathProof{}, errors.New("generated domain data root identity is invalid")
	}
	databasePath := filepath.Join(dataRoot, "control.db")
	databaseFile, err := inspectGeneratedDomainRegularFile(databasePath)
	if err != nil {
		return generatedDomainPathProof{}, errors.New("generated domain database file is unsafe or unreadable")
	}
	ingressPath := filepath.Join(dataRoot, "runtime", "generated-ingress")
	var ingressRoot os.FileInfo
	if present {
		if store == nil || store.root != ingressPath {
			return generatedDomainPathProof{}, errors.New("generated domain ingress path identity is invalid")
		}
		ingressRoot = ancestors[ingressPath]
		if ingressRoot == nil || !ingressRoot.IsDir() || !os.SameFile(ingressRoot, ingressRoot) {
			return generatedDomainPathProof{}, errors.New("generated domain ingress path identity is invalid")
		}
	}
	return generatedDomainPathProof{
		dataRootPath: dataRoot,
		databasePath: databasePath,
		ingressPath:  ingressPath,
		dataRoot:     dataRootInfo,
		databaseFile: databaseFile,
		ingressRoot:  ingressRoot,
		ancestors:    cloneGeneratedDomainPathIdentities(ancestors),
	}, nil
}

func inspectGeneratedDomainRegularFile(path string) (os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		pathsecurity.RejectWindowsNamespace(path) {
		return nil, errors.New("generated domain file path is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o400 == 0 || before.Mode()&os.ModeSymlink != 0 ||
		generatedIngressPathIsReparsePoint(path) || !os.SameFile(before, before) {
		return nil, errors.New("generated domain file identity is invalid")
	}
	after, afterErr := os.Lstat(path)
	if afterErr != nil || !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 ||
		generatedIngressPathIsReparsePoint(path) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		return nil, errors.New("generated domain file identity changed")
	}
	return after, nil
}

func inspectGeneratedDomainMainDatabase(ctx context.Context, database *sql.DB) (string, error) {
	if ctx == nil || database == nil {
		return "", errors.New("generated domain database is invalid")
	}
	rows, err := database.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", errors.New("generated domain database identity is unavailable")
	}
	mainCount := 0
	tempSeen := false
	mainPath := ""
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			_ = rows.Close()
			return "", errors.New("generated domain database identity is unavailable")
		}
		switch {
		case sequence == 0 && name == "main" && mainCount == 0 && validGeneratedDomainMainDatabasePath(path):
			mainCount++
			mainPath = path
		// The pinned driver reports its canonical empty temp schema even before
		// any temp object exists. It names no physical database and cannot be
		// confused with the one required main identity.
		case sequence == 1 && name == "temp" && path == "" && !tempSeen:
			tempSeen = true
		default:
			_ = rows.Close()
			return "", errors.New("generated domain database identity is ambiguous")
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", errors.New("generated domain database identity is unavailable")
	}
	if err := rows.Close(); err != nil || mainCount != 1 || mainPath == "" {
		return "", errors.New("generated domain database identity is ambiguous")
	}
	return mainPath, nil
}

func validGeneratedDomainMainDatabasePath(path string) bool {
	lower := strings.ToLower(path)
	return path != "" && lower != ":memory:" && !strings.HasPrefix(lower, "file:") &&
		!strings.ContainsAny(path, "?#\x00") && !pathsecurity.RejectWindowsNamespace(path) &&
		filepath.IsAbs(path) && filepath.Clean(path) == path
}

// recheck confirms that retained roots and the database file did not change.
// requireIngress permits only the future Manager's missing-to-safe-directory
// transition; it never permits replacement of an already observed directory.
func (proof generatedDomainPathProof) recheck(requireIngress bool) (os.FileInfo, error) {
	current, err := inspectGeneratedDomainPaths(proof.dataRootPath)
	if err != nil || proof.databasePath != current.databasePath || proof.ingressPath != current.ingressPath ||
		!sameGeneratedDomainFileIdentity(proof.dataRoot, current.dataRoot) ||
		!sameGeneratedDomainFileIdentity(proof.databaseFile, current.databaseFile) {
		return nil, errors.New("generated domain path identity changed")
	}
	allowNewAncestors := proof.ingressRoot == nil && requireIngress
	if !sameGeneratedDomainPathIdentityMap(proof.ancestors, current.ancestors, allowNewAncestors) {
		return nil, errors.New("generated domain path identity changed")
	}
	switch {
	case proof.ingressRoot != nil:
		if current.ingressRoot == nil || !sameGeneratedDomainFileIdentity(proof.ingressRoot, current.ingressRoot) {
			return nil, errors.New("generated domain ingress identity changed")
		}
	case requireIngress:
		if current.ingressRoot == nil {
			return nil, errors.New("generated domain ingress directory is absent")
		}
	case current.ingressRoot != nil:
		return nil, errors.New("generated domain ingress identity changed")
	}
	return current.ingressRoot, nil
}

// createIngressRoot performs the constructor's sole absent-to-present path
// transition. Every missing path is created with create-only Mkdir and then
// bound to its concrete identity; an independently appearing path is never
// adopted. The original proof remains immutable.
func (proof generatedDomainPathProof) createIngressRoot() (generatedDomainOwnedPathTransition, error) {
	if proof.ingressRoot != nil {
		return generatedDomainOwnedPathTransition{}, errors.New("generated domain ingress directory already exists")
	}
	if _, err := proof.recheck(false); err != nil {
		return generatedDomainOwnedPathTransition{}, err
	}
	runtimeRoot := filepath.Dir(proof.ingressPath)
	paths := []string{runtimeRoot, proof.ingressPath}
	created := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		if retained := proof.ancestors[path]; retained != nil {
			current, err := os.Lstat(path)
			if err != nil || !current.IsDir() || !sameGeneratedDomainFileIdentity(retained, current) ||
				current.Mode() != retained.Mode() || generatedIngressPathIsReparsePoint(path) {
				return generatedDomainOwnedPathTransition{}, errors.New("generated domain path identity changed")
			}
			continue
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return generatedDomainOwnedPathTransition{}, errors.New("generated domain ingress directory creation failed")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			generatedIngressPathIsReparsePoint(path) || !os.SameFile(info, info) {
			return generatedDomainOwnedPathTransition{}, errors.New("generated domain created path identity is invalid")
		}
		created[path] = info
	}
	final, err := inspectGeneratedDomainPaths(proof.dataRootPath)
	if err != nil || final.ingressRoot == nil || !sameGeneratedDomainFileIdentity(proof.dataRoot, final.dataRoot) ||
		!sameGeneratedDomainFileIdentity(proof.databaseFile, final.databaseFile) ||
		!sameGeneratedDomainPathIdentityMap(proof.ancestors, final.ancestors, true) {
		return generatedDomainOwnedPathTransition{}, errors.New("generated domain created path identity changed")
	}
	for path, expected := range created {
		if !sameGeneratedDomainFileIdentity(expected, final.ancestors[path]) {
			return generatedDomainOwnedPathTransition{}, errors.New("generated domain created path identity changed")
		}
	}
	return generatedDomainOwnedPathTransition{final: final, created: created}, nil
}

func (transition generatedDomainOwnedPathTransition) current() bool {
	if transition.final.ingressRoot == nil || len(transition.created) == 0 {
		return false
	}
	current, err := inspectGeneratedDomainPaths(transition.final.dataRootPath)
	if err != nil || !sameGeneratedDomainPathObservation(transition.final, current, false) {
		return false
	}
	for path, expected := range transition.created {
		if !sameGeneratedDomainFileIdentity(expected, current.ancestors[path]) {
			return false
		}
	}
	return true
}

func sameGeneratedDomainPathObservation(left, right generatedDomainPathProof, allowNewAncestors bool) bool {
	return left.dataRootPath == right.dataRootPath && left.databasePath == right.databasePath &&
		left.ingressPath == right.ingressPath &&
		sameGeneratedDomainFileIdentity(left.dataRoot, right.dataRoot) &&
		sameGeneratedDomainFileIdentity(left.databaseFile, right.databaseFile) &&
		sameGeneratedDomainOptionalFileIdentity(left.ingressRoot, right.ingressRoot) &&
		sameGeneratedDomainPathIdentityMap(left.ancestors, right.ancestors, allowNewAncestors)
}

func sameGeneratedDomainPathIdentityMap(left, right map[string]os.FileInfo, allowRightAddition bool) bool {
	if (!allowRightAddition && len(left) != len(right)) || (allowRightAddition && len(left) > len(right)) {
		return false
	}
	for path, leftInfo := range left {
		rightInfo := right[path]
		if !sameGeneratedDomainFileIdentity(leftInfo, rightInfo) || leftInfo.Mode() != rightInfo.Mode() {
			return false
		}
	}
	return true
}

func cloneGeneratedDomainPathIdentities(values map[string]os.FileInfo) map[string]os.FileInfo {
	result := make(map[string]os.FileInfo, len(values))
	for path, info := range values {
		result[path] = info
	}
	return result
}

func sameGeneratedDomainOptionalFileIdentity(left, right os.FileInfo) bool {
	return left == nil && right == nil || sameGeneratedDomainFileIdentity(left, right)
}

func sameGeneratedDomainFileIdentity(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right)
}

func inspectGeneratedDockerDaemonIdentity(
	ctx context.Context,
	runner runtimeprocess.CommandRunner,
	executable string,
	env []string,
	timeout time.Duration,
) (string, error) {
	if ctx == nil || !generatedCandidatePointerIdentity(runner) ||
		!generatedCandidateCleanAbsolutePath(executable) || len(env) == 0 ||
		timeout < time.Second || timeout > 5*time.Minute {
		return "", errors.New("generated domain daemon inspection is invalid")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result, runErr := runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable:  executable,
		Args:        []string{"info", "--format", "{{json .ID}}"},
		Directory:   filepath.Dir(executable),
		Env:         append([]string(nil), env...),
		Timeout:     timeout,
		OutputLimit: generatedDaemonIdentityOutputLimit,
	})
	defer clearResult(&result)
	if cause := generatedDaemonInspectionCause(ctx.Err(), runErr); cause != nil {
		return "", fmt.Errorf("generated domain daemon inspection failed: %w", cause)
	}
	if runErr != nil || result.StdoutTruncated || result.StderrTruncated {
		return "", errors.New("generated domain daemon identity is unavailable")
	}
	if !utf8.Valid(result.Stdout) {
		return "", errors.New("generated domain daemon identity is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(result.Stdout))
	var daemonID string
	if err := decoder.Decode(&daemonID); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		!validGeneratedDaemonID(daemonID) {
		return "", errors.New("generated domain daemon identity is invalid")
	}
	return daemonID, nil
}

func generatedDaemonInspectionCause(contextErr, runErr error) error {
	causes := make([]error, 0, 2)
	switch {
	case errors.Is(contextErr, context.Canceled) || errors.Is(runErr, context.Canceled):
		causes = append(causes, context.Canceled)
	case errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded):
		causes = append(causes, context.DeadlineExceeded)
	}
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		causes = append(causes, runtimeprocess.ErrTerminationFailed)
	}
	return errors.Join(causes...)
}

func validGeneratedDaemonID(value string) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > generatedDaemonIdentityMaximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func generatedDomainRootOrDatabaseConflict(left, right generatedDomainPhysicalIdentity) bool {
	return sameGeneratedDomainFileIdentity(left.manager.dataRoot, right.manager.dataRoot) ||
		sameGeneratedDomainFileIdentity(left.databaseFile, right.databaseFile)
}

func generatedDomainDaemonNamespaceConflict(left, right generatedDomainPhysicalIdentity) bool {
	return generatedManagerDaemonNamespaceConflict(left.manager, right.manager)
}

func generatedLegacyRootConflict(legacy generatedManagerPhysicalIdentity, domain generatedDomainPhysicalIdentity) bool {
	return sameGeneratedDomainFileIdentity(legacy.dataRoot, domain.manager.dataRoot) ||
		sameGeneratedDomainFileIdentity(legacy.ingressRoot, domain.manager.ingressRoot)
}

func generatedLegacyDaemonNamespaceConflict(legacy generatedManagerPhysicalIdentity, domain generatedDomainPhysicalIdentity) bool {
	return generatedManagerDaemonNamespaceConflict(legacy, domain.manager)
}

func generatedManagerDaemonNamespaceConflict(left, right generatedManagerPhysicalIdentity) bool {
	return left.daemonID != "" && left.daemonID == right.daemonID &&
		left.namespace != (generatedGatewayNamespace{}) && left.namespace == right.namespace
}
