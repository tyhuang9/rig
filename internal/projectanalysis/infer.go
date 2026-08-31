package projectanalysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Analyze inspects only bounded declarative repository metadata. It does not
// execute package scripts, package managers, Dockerfiles, or Compose files.
func Analyze(ctx context.Context, files []File, reader FileReader) (SourceAnalysis, error) {
	source, err := loadSnapshot(ctx, files, reader)
	if err != nil {
		return SourceAnalysis{}, err
	}
	result := SourceAnalysis{
		SchemaVersion: SchemaVersion, StructuralFingerprint: source.fingerprint,
		Candidates: []DeploymentPlanCandidate{}, Findings: slices.Clone(source.findings),
	}
	for _, file := range source.files {
		switch {
		case composeFile(file.Path):
			result.Candidates = append(result.Candidates, containerCandidate(PlanKindCompose, file.Path))
		case dockerfile(file.Path):
			result.Candidates = append(result.Candidates, containerCandidate(PlanKindDockerfile, file.Path))
		}
	}
	result.Candidates = append(result.Candidates, javascriptCandidates(source)...)
	for index := range result.Candidates {
		normalizeCandidate(&result.Candidates[index])
		result.Candidates[index].Digest = candidateDigest(result.Candidates[index])
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		left, right := result.Candidates[i], result.Candidates[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.RootDirectory != right.RootDirectory {
			return left.RootDirectory < right.RootDirectory
		}
		return left.ConfigPath < right.ConfigPath
	})
	sortFindings(result.Findings)
	return result, nil
}

func containerCandidate(kind, configPath string) DeploymentPlanCandidate {
	label := "Dockerfile"
	if kind == PlanKindCompose {
		label = "Compose file"
	}
	return DeploymentPlanCandidate{
		ID: "inferred:" + kind + ":" + configPath, Origin: OriginInferred,
		Status: StatusNeedsInput, Kind: kind, RootDirectory: packageDirectory(configPath), ConfigPath: configPath,
		Components:     []Component{},
		Evidence:       []Evidence{{Code: kind + "_candidate", Path: configPath, Detail: label + " detected; contents were not executed or adopted as a generated build recipe."}},
		Findings:       []Finding{{Code: "container_definition_requires_review", Severity: "warning", Message: label + " is an advanced candidate and requires explicit review before use.", Path: configPath}},
		MissingFields:  []string{"container.review_approval"},
		AdvancedInputs: []AdvancedInput{{Field: "container.review_approval", Required: true, Reason: "Repository container definitions are untrusted executable build configuration."}},
	}
}

func javascriptCandidates(source snapshot) []DeploymentPlanCandidate {
	if len(source.packages) == 0 {
		return nil
	}
	valid := make([]packageFile, 0, len(source.packages))
	invalid := make([]DeploymentPlanCandidate, 0)
	for _, pkg := range source.packages {
		if pkg.issue == nil {
			valid = append(valid, pkg)
			continue
		}
		invalid = append(invalid, DeploymentPlanCandidate{
			ID: "inferred:javascript:" + rootLabel(pkg.dir), Origin: OriginInferred,
			Status: StatusUnsupported, Kind: PlanKindJavaScript, RootDirectory: pkg.dir,
			Components: []Component{}, Evidence: []Evidence{}, Findings: []Finding{*pkg.issue},
			MissingFields: []string{}, AdvancedInputs: []AdvancedInput{},
		})
	}
	if len(valid) == 0 {
		return invalid
	}

	rootIndex := slices.IndexFunc(valid, func(pkg packageFile) bool { return pkg.dir == "" })
	if rootIndex < 0 {
		if len(valid) == 1 {
			return append(invalid, inferJavaScriptPlan(source, valid[0], []packageFile{valid[0]}, false))
		}
		plan := inferJavaScriptPlan(source, valid[0], valid, true)
		plan.Findings = append(plan.Findings, Finding{Code: "ambiguous_package_roots", Severity: "error", Message: "Multiple package roots were found without a root workspace manifest."})
		plan.MissingFields = append(plan.MissingFields, "workspace.root")
		plan.Status = StatusNeedsInput
		return append(invalid, plan)
	}

	root := valid[rootIndex]
	patterns := slices.Clone(root.manifest.Workspaces)
	for _, workspaceFile := range []string{"pnpm-workspace.yaml", "pnpm-workspace.yml"} {
		if body, ok := source.contents[workspaceFile]; ok {
			parsed, err := pnpmWorkspacePatterns(body)
			if err != nil {
				plan := inferJavaScriptPlan(source, root, []packageFile{root}, true)
				plan.Status = StatusNeedsInput
				plan.Findings = append(plan.Findings, Finding{Code: "malformed_workspace_config", Severity: "error", Message: "pnpm workspace configuration could not be parsed.", Path: workspaceFile})
				plan.MissingFields = append(plan.MissingFields, "workspace.packages")
				return append(invalid, plan)
			}
			patterns = append(patterns, parsed...)
		}
	}
	patterns = sortedUniqueNonempty(patterns)
	if len(patterns) == 0 {
		if len(valid) > 1 {
			plan := inferJavaScriptPlan(source, root, valid, true)
			plan.Status = StatusNeedsInput
			plan.Findings = append(plan.Findings, Finding{Code: "ambiguous_nested_packages", Severity: "error", Message: "Nested package manifests are not declared by a supported workspace configuration."})
			plan.MissingFields = append(plan.MissingFields, "workspace.packages")
			return append(invalid, plan)
		}
		return append(invalid, inferJavaScriptPlan(source, root, []packageFile{root}, false))
	}

	members := make([]packageFile, 0)
	for _, pkg := range valid {
		if pkg.dir == "" {
			continue
		}
		if matchesWorkspace(pkg.dir, patterns) {
			members = append(members, pkg)
		}
	}
	plan := inferJavaScriptPlan(source, root, members, true)
	if len(members) == 0 {
		plan.Status = StatusNeedsInput
		plan.Findings = append(plan.Findings, Finding{Code: "empty_workspace", Severity: "error", Message: "Workspace patterns did not match a package manifest."})
		plan.MissingFields = append(plan.MissingFields, "workspace.packages")
	}
	return append(invalid, plan)
}

func inferJavaScriptPlan(source snapshot, root packageFile, packages []packageFile, workspace bool) DeploymentPlanCandidate {
	plan := DeploymentPlanCandidate{
		ID: "inferred:javascript:" + rootLabel(root.dir), Origin: OriginInferred,
		Status: StatusReady, Kind: PlanKindJavaScript, RootDirectory: root.dir,
		Components: []Component{}, Evidence: []Evidence{}, Findings: []Finding{}, MissingFields: []string{}, AdvancedInputs: []AdvancedInput{},
	}
	manager, install, managerFindings, managerMissing := inferPackageManager(source, root)
	plan.PackageManager = manager
	plan.Install = install
	plan.Findings = append(plan.Findings, managerFindings...)
	plan.MissingFields = append(plan.MissingFields, managerMissing...)
	plan.NodeVersion, managerFindings, managerMissing = inferNodeVersion(source, root)
	plan.Findings = append(plan.Findings, managerFindings...)
	plan.MissingFields = append(plan.MissingFields, managerMissing...)

	if workspace {
		plan.Evidence = append(plan.Evidence, Evidence{Code: "workspace", Path: root.path, Field: "workspaces"})
	}
	serverCount, staticCount, workerCount := 0, 0, 0
	for _, pkg := range packages {
		component, componentFindings, missing := inferComponent(source, pkg, manager)
		if component == nil {
			continue
		}
		plan.Components = append(plan.Components, *component)
		plan.Findings = append(plan.Findings, componentFindings...)
		plan.MissingFields = append(plan.MissingFields, missing...)
		switch component.Kind {
		case ComponentServer:
			serverCount++
		case ComponentStatic:
			staticCount++
		case ComponentWorker:
			workerCount++
		}
		if component.InternalPort == nil {
			plan.AdvancedInputs = append(plan.AdvancedInputs, AdvancedInput{Field: "components." + component.ID + ".internal_port", ComponentID: component.ID, Required: false, Reason: "The manifest does not reliably declare the application's listening port."})
		}
		if component.HealthProbe == nil {
			plan.AdvancedInputs = append(plan.AdvancedInputs, AdvancedInput{Field: "components." + component.ID + ".health_probe", ComponentID: component.ID, Required: false, Reason: "A health endpoint cannot be safely inferred from package metadata."})
		}
	}
	if len(plan.Components) == 0 {
		plan.Status = StatusUnsupported
		plan.Findings = append(plan.Findings, Finding{Code: "no_supported_component", Severity: "error", Message: "No supported JavaScript deployment component was detected."})
	}
	if serverCount > 1 {
		plan.Findings = append(plan.Findings, Finding{Code: "multiple_server_components", Severity: "error", Message: "Multiple API/server components require an explicit topology."})
		plan.MissingFields = append(plan.MissingFields, "topology.server_components")
	}
	if staticCount > 1 {
		plan.Findings = append(plan.Findings, Finding{Code: "multiple_static_components", Severity: "error", Message: "Multiple static components require an explicit topology."})
		plan.MissingFields = append(plan.MissingFields, "topology.static_components")
	}
	if workerCount > 0 {
		plan.Findings = append(plan.Findings, Finding{Code: "worker_components_require_input", Severity: "error", Message: "Worker processes require explicit lifecycle and scaling settings."})
		plan.MissingFields = append(plan.MissingFields, "topology.worker_components")
	}
	if len(plan.MissingFields) > 0 && plan.Status != StatusUnsupported {
		plan.Status = StatusNeedsInput
	}
	return plan
}

func inferPackageManager(source snapshot, root packageFile) (PackageManager, *Command, []Finding, []string) {
	manager := PackageManager{Origin: OriginInferred, Evidence: []Evidence{}}
	findings := []Finding{}
	missing := []string{}
	lockfiles := managerLockfiles(source, root.dir)
	metadataName, metadataVersion, metadataOK := parsePackageManager(root.manifest.PackageManager)
	if root.manifest.PackageManager != "" && !metadataOK {
		findings = append(findings, Finding{Code: "invalid_package_manager", Severity: "error", Message: "packageManager must name npm, pnpm, or Yarn with a version.", Path: root.path, Field: "packageManager"})
		missing = append(missing, FieldPackageManager)
	}
	if len(lockfiles) > 1 {
		findings = append(findings, Finding{Code: "conflicting_lockfiles", Severity: "error", Message: "Multiple package-manager lockfile families were detected.", Path: root.dir})
		missing = append(missing, FieldPackageManager)
	}
	lockName, lockPath := "", ""
	if len(lockfiles) == 1 {
		for name, file := range lockfiles {
			lockName, lockPath = name, file
		}
	}
	if metadataOK {
		manager.Name, manager.Version = metadataName, metadataVersion
		manager.Provenance, manager.Confidence = ProvenancePackageManager, ConfidenceHigh
		manager.Evidence = append(manager.Evidence, Evidence{Code: "package_manager_metadata", Path: root.path, Field: "packageManager", Detail: root.manifest.PackageManager})
		if lockName != "" && lockName != metadataName {
			findings = append(findings, Finding{Code: "package_manager_lockfile_mismatch", Severity: "error", Message: "packageManager metadata conflicts with the detected lockfile.", Path: lockPath})
			missing = append(missing, FieldPackageManager)
		}
	} else if lockName != "" {
		manager.Name, manager.Provenance, manager.Confidence = lockName, ProvenanceLockfile, ConfidenceHigh
		manager.Evidence = append(manager.Evidence, Evidence{Code: "lockfile", Path: lockPath})
		if lockName == "yarn" {
			findings = append(findings, Finding{Code: "yarn_version_required", Severity: "error", Message: "Yarn lockfiles do not identify whether classic or modern install flags are required.", Path: lockPath})
			missing = append(missing, "package_manager.version")
		}
	} else if root.manifest.PackageManager == "" {
		manager.Name, manager.Provenance, manager.Confidence = "npm", ProvenanceRuntimeDefault, ConfidenceMedium
		manager.Evidence = append(manager.Evidence, Evidence{Code: "npm_default", Path: root.path})
		findings = append(findings, Finding{Code: "unlocked_install", Severity: "warning", Message: "No lockfile was detected; dependency installation is not reproducible.", Path: root.path})
	}
	if lockName == manager.Name {
		manager.Lockfile = lockPath
	}
	if manager.Name == "" || slices.Contains(missing, FieldPackageManager) || (manager.Name == "yarn" && manager.Version == "") {
		return manager, nil, findings, missing
	}
	command := installCommand(manager)
	return manager, &Command{
		Origin: OriginInferred, Phase: "install", Command: command, WorkingDirectory: root.dir,
		Provenance: manager.Provenance, Confidence: manager.Confidence, Evidence: slices.Clone(manager.Evidence),
	}, findings, missing
}

func managerLockfiles(source snapshot, root string) map[string]string {
	result := map[string]string{}
	for name, manager := range map[string]string{
		"package-lock.json": "npm", "npm-shrinkwrap.json": "npm", "pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn",
	} {
		candidate := joinRoot(root, name)
		if _, ok := source.fileSet[candidate]; ok {
			result[manager] = candidate
		}
	}
	return result
}

func parsePackageManager(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	separator := strings.LastIndex(value, "@")
	if separator <= 0 || separator == len(value)-1 {
		return "", "", false
	}
	name, version := strings.ToLower(value[:separator]), value[separator+1:]
	if name != "npm" && name != "pnpm" && name != "yarn" {
		return "", "", false
	}
	return name, version, true
}

func installCommand(manager PackageManager) string {
	locked := manager.Lockfile != ""
	switch manager.Name {
	case "pnpm":
		if locked {
			return "corepack pnpm install --frozen-lockfile"
		}
		return "corepack pnpm install"
	case "yarn":
		major, _ := strconv.Atoi(strings.SplitN(manager.Version, ".", 2)[0])
		if locked && major >= 2 {
			return "corepack yarn install --immutable"
		}
		if locked {
			return "corepack yarn install --frozen-lockfile"
		}
		return "corepack yarn install"
	default:
		if locked {
			return "npm ci"
		}
		return "npm install"
	}
}

func inferNodeVersion(source snapshot, root packageFile) (InferredValue, []Finding, []string) {
	value := InferredValue{Origin: OriginInferred, Evidence: []Evidence{}}
	findings := []Finding{}
	missing := []string{}
	hints := []struct{ path, value string }{}
	for _, name := range []string{".nvmrc", ".node-version"} {
		candidate := joinRoot(root.dir, name)
		if body, ok := source.contents[candidate]; ok {
			trimmed := strings.TrimSpace(string(body))
			if trimmed != "" {
				hints = append(hints, struct{ path, value string }{candidate, trimmed})
			}
		}
	}
	if len(hints) == 2 && hints[0].value != hints[1].value {
		findings = append(findings, Finding{Code: "conflicting_node_versions", Severity: "error", Message: ".nvmrc and .node-version disagree."})
		missing = append(missing, "node_version")
		return value, findings, missing
	}
	if len(hints) > 0 {
		value.Value, value.Provenance, value.Confidence = hints[0].value, ProvenanceRuntimeFile, ConfidenceHigh
		value.Evidence = append(value.Evidence, Evidence{Code: "node_version_file", Path: hints[0].path, Detail: hints[0].value})
		if root.manifest.EnginesNode != "" {
			value.Evidence = append(value.Evidence, Evidence{Code: "node_engine_constraint", Path: root.path, Field: "engines.node", Detail: root.manifest.EnginesNode})
		}
		return value, findings, missing
	}
	if root.manifest.EnginesNode != "" {
		value.Value, value.Provenance, value.Confidence = root.manifest.EnginesNode, ProvenanceEngineConstraint, ConfidenceHigh
		value.Evidence = append(value.Evidence, Evidence{Code: "node_engine_constraint", Path: root.path, Field: "engines.node", Detail: root.manifest.EnginesNode})
		return value, findings, missing
	}
	value.Value, value.Provenance, value.Confidence = PinnedNodeLTS, ProvenanceRuntimeDefault, ConfidenceMedium
	value.Evidence = append(value.Evidence, Evidence{Code: "pinned_node_lts", Detail: PinnedNodeLTS})
	return value, findings, missing
}

func inferComponent(source snapshot, pkg packageFile, manager PackageManager) (*Component, []Finding, []string) {
	frameworks := detectedFrameworks(pkg.manifest)
	findings := []Finding{}
	missing := []string{}
	if len(frameworks) == 0 {
		if _, ok := pkg.manifest.Scripts["start"]; !ok {
			if hasWorkerScript(pkg.manifest.Scripts) {
				frameworks = []string{FrameworkNode}
			} else {
				return nil, findings, missing
			}
		} else {
			frameworks = []string{FrameworkNode}
		}
	}
	if len(frameworks) > 1 {
		findings = append(findings, Finding{Code: "ambiguous_framework", Severity: "error", Message: "Multiple deployment frameworks were detected in one package.", Path: pkg.path})
		missing = append(missing, "components."+componentID(pkg)+".framework")
	}
	framework := frameworks[0]
	kind := ComponentServer
	if framework == FrameworkVite {
		kind = ComponentStatic
	}
	if hasWorkerScript(pkg.manifest.Scripts) && framework == FrameworkNode {
		kind = ComponentWorker
	}
	component := &Component{
		ID: componentID(pkg), Origin: OriginInferred, Name: pkg.manifest.Name, Kind: kind, Framework: framework,
		RootDirectory: pkg.dir, Evidence: []Evidence{{Code: "framework_dependency", Path: pkg.path, Detail: framework}}, Findings: []Finding{},
	}
	buildRequired := framework == FrameworkVite || framework == FrameworkNextJS || framework == FrameworkNestJS
	if _, ok := pkg.manifest.Scripts["build"]; ok {
		component.Build = scriptCommand(manager.Name, pkg, "build", "build")
	} else if buildRequired {
		missing = append(missing, "components."+component.ID+".build")
		findings = append(findings, Finding{Code: "missing_build_script", Severity: "error", Message: "The detected framework requires an exact build script.", Path: pkg.path, Field: "scripts.build"})
	}
	if kind == ComponentStatic {
		component.StaticOutputDirectory = "dist"
		component.Run = &Command{
			Origin: OriginInferred, Phase: "run", Command: ManagedStaticServerCommand, WorkingDirectory: pkg.dir,
			Provenance: ProvenanceManagedRuntime, Confidence: ConfidenceHigh,
			Evidence: []Evidence{{Code: "vite_static_output", Path: pkg.path, Detail: "dist"}},
		}
		component.InternalPort = &InferredValue{Origin: OriginInferred, Value: "$PORT", Provenance: ProvenanceManagedRuntime, Confidence: ConfidenceHigh, Evidence: []Evidence{{Code: "managed_static_port"}}}
		component.HealthProbe = &HealthProbe{Origin: OriginInferred, Path: "/", Method: "GET", Provenance: ProvenanceManagedRuntime, Confidence: ConfidenceHigh, Evidence: []Evidence{{Code: "managed_static_root"}}}
	} else {
		runScript := "start"
		if framework == FrameworkNestJS {
			if _, ok := pkg.manifest.Scripts["start:prod"]; ok {
				runScript = "start:prod"
			}
		}
		if _, ok := pkg.manifest.Scripts[runScript]; ok {
			component.Run = scriptCommand(manager.Name, pkg, runScript, "run")
		} else if kind == ComponentWorker {
			name := workerScript(pkg.manifest.Scripts)
			component.Run = scriptCommand(manager.Name, pkg, name, "run")
		} else {
			missing = append(missing, "components."+component.ID+".run")
			findings = append(findings, Finding{Code: "missing_start_script", Severity: "error", Message: "No production start script was found.", Path: pkg.path, Field: "scripts.start"})
		}
	}
	component.Migration, component.MigrationFingerprint = inferMigration(source, pkg, manager)
	component.Findings = slices.Clone(findings)
	return component, findings, missing
}

func detectedFrameworks(manifest packageManifest) []string {
	dependencies := make(map[string]bool)
	for name := range manifest.Dependencies {
		dependencies[name] = true
	}
	for name := range manifest.DevDependencies {
		dependencies[name] = true
	}
	for name := range manifest.OptionalDependencies {
		dependencies[name] = true
	}
	frameworks := []string{}
	for _, item := range []struct{ dependency, framework string }{
		{"next", FrameworkNextJS}, {"vite", FrameworkVite}, {"@nestjs/core", FrameworkNestJS},
		{"fastify", FrameworkFastify}, {"express", FrameworkExpress},
	} {
		if dependencies[item.dependency] {
			frameworks = append(frameworks, item.framework)
		}
	}
	return frameworks
}

func scriptCommand(manager string, pkg packageFile, script, phase string) *Command {
	prefix := manager
	if prefix == "" {
		prefix = "npm"
	}
	return &Command{
		Origin: OriginInferred, Phase: phase, Command: prefix + " run " + script, WorkingDirectory: pkg.dir,
		Provenance: ProvenanceManifestScript, Confidence: ConfidenceHigh,
		Evidence: []Evidence{{Code: "package_script", Path: pkg.path, Field: "scripts." + script}},
	}
}

func inferMigration(source snapshot, pkg packageFile, manager PackageManager) (*Command, string) {
	type migrationKind struct{ name, dependency, command string }
	kinds := []migrationKind{
		{"prisma", "prisma", "prisma migrate deploy"},
		{"drizzle", "drizzle-orm", "drizzle-kit migrate"},
		{"knex", "knex", "knex migrate:latest"},
	}
	for _, kind := range kinds {
		if !hasDependency(pkg.manifest, kind.dependency) && !(kind.name == "prisma" && hasDependency(pkg.manifest, "@prisma/client")) {
			continue
		}
		evidenceFiles := migrationFiles(source.files, pkg.dir, kind.name)
		if len(evidenceFiles) == 0 {
			continue
		}
		evidence := []Evidence{{Code: kind.name + "_migration", Path: pkg.path, Detail: kind.dependency}}
		for _, file := range evidenceFiles {
			evidence = append(evidence, Evidence{Code: "migration_input", Path: file.Path})
		}
		command := packageExecCommand(manager.Name, kind.command)
		return &Command{
			Origin: OriginInferred, Phase: "migrate", Command: command, WorkingDirectory: pkg.dir,
			Provenance: ProvenanceFrameworkDefault, Confidence: ConfidenceHigh, Evidence: evidence,
		}, fileFingerprint("rig-migration-v1", evidenceFiles)
	}
	return nil, ""
}

func migrationFiles(files []File, root, kind string) []File {
	result := []File{}
	for _, file := range files {
		relative := file.Path
		if root != "" {
			prefix := root + "/"
			if !strings.HasPrefix(relative, prefix) {
				continue
			}
			relative = strings.TrimPrefix(relative, prefix)
		}
		base := path.Base(relative)
		matched := false
		switch kind {
		case "prisma":
			matched = relative == "prisma/schema.prisma" || strings.HasPrefix(relative, "prisma/migrations/")
		case "drizzle":
			matched = strings.HasPrefix(base, "drizzle.config.") || strings.HasPrefix(relative, "drizzle/")
		case "knex":
			matched = strings.HasPrefix(base, "knexfile.") || strings.HasPrefix(relative, "migrations/")
		}
		if matched {
			result = append(result, file)
		}
	}
	return result
}

func packageExecCommand(manager, suffix string) string {
	switch manager {
	case "pnpm":
		return "pnpm exec " + suffix
	case "yarn":
		return "yarn " + suffix
	default:
		return "npm exec -- " + suffix
	}
}

func fileFingerprint(domain string, files []File) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain + "\n"))
	for _, file := range files {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\n", file.Path, file.Size)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizeCandidate(candidate *DeploymentPlanCandidate) {
	slices.Sort(candidate.MissingFields)
	candidate.MissingFields = slices.Compact(candidate.MissingFields)
	sort.Slice(candidate.AdvancedInputs, func(i, j int) bool { return candidate.AdvancedInputs[i].Field < candidate.AdvancedInputs[j].Field })
	sort.Slice(candidate.Components, func(i, j int) bool {
		return candidate.Components[i].RootDirectory < candidate.Components[j].RootDirectory
	})
	sortEvidence(candidate.Evidence)
	sortFindings(candidate.Findings)
	for index := range candidate.Components {
		sortEvidence(candidate.Components[index].Evidence)
		sortFindings(candidate.Components[index].Findings)
	}
}

func candidateDigest(candidate DeploymentPlanCandidate) string {
	candidate.Digest = ""
	body, err := json.Marshal(candidate)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func sortEvidence(values []Evidence) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		return left.Code+"\x00"+left.Path+"\x00"+left.Field+"\x00"+left.Detail < right.Code+"\x00"+right.Path+"\x00"+right.Field+"\x00"+right.Detail
	})
}

func sortFindings(values []Finding) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		return left.Code+"\x00"+left.Path+"\x00"+left.Field+"\x00"+left.Message < right.Code+"\x00"+right.Path+"\x00"+right.Field+"\x00"+right.Message
	})
}

func matchesWorkspace(directory string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := path.Match(strings.TrimSuffix(pattern, "/"), directory)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func composeFile(name string) bool {
	switch path.Base(name) {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	default:
		return false
	}
}

func dockerfile(name string) bool {
	base := path.Base(name)
	return base == "Dockerfile" || strings.HasSuffix(base, ".Dockerfile")
}

func componentID(pkg packageFile) string {
	if pkg.manifest.Name != "" {
		return sanitizeID(pkg.manifest.Name)
	}
	if pkg.dir == "" {
		return "app"
	}
	return sanitizeID(pkg.dir)
}

func sanitizeID(value string) string {
	value = strings.TrimPrefix(strings.ToLower(value), "@")
	replacer := strings.NewReplacer("/", "-", "\\", "-", "_", "-", " ", "-")
	return replacer.Replace(value)
}

func rootLabel(root string) string {
	if root == "" {
		return "."
	}
	return root
}
func joinRoot(root, name string) string {
	if root == "" {
		return name
	}
	return root + "/" + name
}

func hasDependency(manifest packageManifest, dependency string) bool {
	_, a := manifest.Dependencies[dependency]
	_, b := manifest.DevDependencies[dependency]
	_, c := manifest.OptionalDependencies[dependency]
	return a || b || c
}

func hasWorkerScript(scripts map[string]string) bool { return workerScript(scripts) != "" }
func workerScript(scripts map[string]string) string {
	for _, name := range []string{"start:worker", "worker"} {
		if _, ok := scripts[name]; ok {
			return name
		}
	}
	return ""
}
