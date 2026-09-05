package projectanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SetupVersion = 1
const SetupCandidateID = "user:deployment-setup"

// DeploymentSetup is explicit user configuration, independent of detector success.
// Empty install/build commands deliberately omit those phases.
type DeploymentSetup struct {
	Components       []SetupComponent `json:"components"`
	MigrationCommand string           `json:"migrationCommand,omitempty"`
}

type SetupComponent struct {
	ID              string `json:"id"`
	Technology      string `json:"technology"`
	RootDirectory   string `json:"rootDirectory"`
	PackageManager  string `json:"packageManager"`
	NodeVersion     string `json:"nodeVersion"`
	InstallCommand  string `json:"installCommand"`
	BuildCommand    string `json:"buildCommand"`
	StartCommand    string `json:"startCommand"`
	OutputDirectory string `json:"outputDirectory"`
	InternalPort    int    `json:"internalPort"`
	HealthProbe     string `json:"healthProbe"`
}

type SetupError struct{ Fields map[string]string }

func (e *SetupError) Error() string { return "invalid deployment setup" }

func setupError(field, message string) error {
	return &SetupError{Fields: map[string]string{field: message}}
}

// NormalizeSetup checks only configuration. Source existence and migration
// evidence are validated separately, by PrepareSetup, against the same snapshot.
func NormalizeSetup(input DeploymentSetup) (DeploymentSetup, error) {
	if len(input.Components) < 1 || len(input.Components) > 2 {
		return DeploymentSetup{}, setupError("components", "Choose one server, one static site, or a static site and server.")
	}
	input.Components = slices.Clone(input.Components)
	seen := map[string]bool{}
	servers, statics := 0, 0
	for index := range input.Components {
		c := &input.Components[index]
		prefix := "components." + strconv.Itoa(index) + "."
		if !setupText(c.ID, 256) || seen[c.ID] {
			return DeploymentSetup{}, setupError(prefix+"id", "Component identifiers must be unique and bounded.")
		}
		seen[c.ID] = true
		if c.RootDirectory == "" {
			c.RootDirectory = "."
		}
		if !SetupDirectory(c.RootDirectory) || excludedDirectory(c.RootDirectory) || sensitiveFile(c.RootDirectory) {
			return DeploymentSetup{}, setupError(prefix+"rootDirectory", "Use a project directory inside the source, without parent traversal or excluded paths.")
		}
		if c.PackageManager != "npm" && c.PackageManager != "pnpm" && c.PackageManager != "yarn" {
			return DeploymentSetup{}, setupError(prefix+"packageManager", "Choose npm, pnpm, or Yarn.")
		}
		if c.NodeVersion != "20" && c.NodeVersion != "22" && c.NodeVersion != "24" {
			return DeploymentSetup{}, setupError(prefix+"nodeVersion", "Choose a supported Node.js version.")
		}
		for field, command := range map[string]string{"installCommand": c.InstallCommand, "buildCommand": c.BuildCommand, "startCommand": c.StartCommand} {
			if command != "" && !setupText(command, 8192) {
				return DeploymentSetup{}, setupError(prefix+field, "Commands must be single lines of at most 8192 bytes; leave optional steps empty to skip them.")
			}
		}
		if c.InternalPort < 1 || c.InternalPort > 65535 {
			return DeploymentSetup{}, setupError(prefix+"internalPort", "Enter a port from 1 to 65535.")
		}
		if !setupText(c.HealthProbe, 1024) || !strings.HasPrefix(c.HealthProbe, "/") || strings.ContainsAny(c.HealthProbe, "?#") || path.Clean(c.HealthProbe) != c.HealthProbe {
			return DeploymentSetup{}, setupError(prefix+"healthProbe", "Enter a normalized health-check path beginning with /.")
		}
		switch c.Technology {
		case "node", "nextjs":
			servers++
			if c.StartCommand == "" || c.OutputDirectory != "" {
				return DeploymentSetup{}, setupError(prefix+"startCommand", "A server requires a start command and does not use a static output directory.")
			}
		case "static":
			statics++
			if !SetupDirectory(c.OutputDirectory) || unsafeStaticOutputDirectory(c.OutputDirectory) || (excludedDirectory(c.OutputDirectory) && !prebuiltStaticOutputDirectory(c.OutputDirectory)) || c.StartCommand != "" {
				return DeploymentSetup{}, setupError(prefix+"outputDirectory", "Choose an output directory inside the project; Rig starts the static server.")
			}
		default:
			return DeploymentSetup{}, setupError(prefix+"technology", "Choose Node.js, Next.js, or Static site. Use the Compose flow for Docker Compose.")
		}
	}
	if servers > 1 || statics > 1 {
		return DeploymentSetup{}, setupError("components", "Only one server and one static site are supported together.")
	}
	if input.MigrationCommand != "" && !setupText(input.MigrationCommand, 8192) {
		return DeploymentSetup{}, setupError("migrationCommand", "Migration commands must be bounded single lines.")
	}
	slices.SortFunc(input.Components, func(a, b SetupComponent) int { return strings.Compare(a.ID, b.ID) })
	return input, nil
}

func SetupDirectory(value string) bool {
	return setupText(value, 1024) && (value == "." || validateRepositoryPath(value) == nil)
}

func setupText(value string, limit int) bool {
	if len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func ManagedStaticCommand(output string, port int) string {
	// The path is data even when it includes spaces or shell metacharacters.
	return "rig-static --root '" + strings.ReplaceAll(output, "'", "'\"'\"'") + "' --port " + strconv.Itoa(port)
}

// PrepareSetup deliberately uses source evidence, not an inferred candidate.
// Missing frameworks/scripts are configurable; unreadable metadata, absent
// roots and unresolved migration evidence cannot be bypassed by manual input.
func PrepareSetup(analysis SourceAnalysis, input DeploymentSetup) (DeploymentPlanCandidate, DeploymentSetup, error) {
	setup, err := NormalizeSetup(input)
	if err != nil {
		return DeploymentPlanCandidate{}, DeploymentSetup{}, err
	}
	if analysis.source == nil {
		return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError("source", "Inspect the source before configuring deployment.")
	}
	source := *analysis.source
	candidate := DeploymentPlanCandidate{
		ID: SetupCandidateID, Origin: OriginUser, Status: StatusReady, Kind: PlanKindJavaScript,
		RootDirectory: setup.Components[0].RootDirectory, Components: []Component{}, Findings: []Finding{},
		Evidence: []Evidence{{Code: "user_deployment_setup"}}, MissingFields: []string{}, AdvancedInputs: []AdvancedInput{},
	}
	migrations := 0
	for _, c := range setup.Components {
		prefix := "components." + c.ID + "."
		found := false
		for _, file := range source.files {
			if c.RootDirectory == "." || strings.HasPrefix(file.Path, c.RootDirectory+"/") {
				found = true
				break
			}
		}
		if !found {
			if c.Technology == "static" && c.BuildCommand == "" && source.hasStaticOutput(c.RootDirectory, c.OutputDirectory) {
				found = true
			}
		}
		if !found {
			return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError(prefix+"rootDirectory", "The selected root contains no accessible project files.")
		}
		role, command := ComponentServer, c.StartCommand
		if c.Technology == "static" {
			role, command = ComponentStatic, ManagedStaticCommand(c.OutputDirectory, c.InternalPort)
		}
		component := Component{
			ID: c.ID, Name: c.ID, Origin: OriginUser, Kind: role, Framework: c.Technology,
			RootDirectory: c.RootDirectory, StaticOutputDirectory: c.OutputDirectory,
			Evidence: []Evidence{{Code: "user_component", Path: c.RootDirectory}}, Findings: []Finding{},
			Run:          setupCommand(command, "run", c.RootDirectory),
			InternalPort: &InferredValue{Origin: OriginUser, Value: strconv.Itoa(c.InternalPort), Confidence: ConfidenceHigh, Evidence: []Evidence{}},
			HealthProbe:  &HealthProbe{Origin: OriginUser, Path: c.HealthProbe, Method: "GET", Confidence: ConfidenceHigh, Evidence: []Evidence{}},
		}
		if c.BuildCommand != "" {
			component.Build = setupCommand(c.BuildCommand, "build", c.RootDirectory)
		}
		for _, pkg := range source.packages {
			pkgRoot := pkg.dir
			if pkgRoot == "" {
				pkgRoot = "."
			}
			// Ancestor manifests may define workspace or migration configuration.
			if pkgRoot != "." && pkgRoot != c.RootDirectory && !strings.HasPrefix(c.RootDirectory, pkgRoot+"/") {
				continue
			}
			if pkg.issue != nil {
				return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError(prefix+"rootDirectory", "Fix invalid or oversized package metadata in this project before deployment.")
			}
			if pkg.manifest.EnginesNode != "" && !engineSupportsVersion(pkg.manifest.EnginesNode, c.NodeVersion) {
				return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError(prefix+"nodeVersion", "The selected Node.js version does not satisfy this project's engines.node requirement.")
			}
			migration, fingerprint, _, missing := inferMigration(source, pkg, PackageManager{Name: c.PackageManager}, c.ID)
			if len(missing) != 0 {
				return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError("migrationCommand", "Resolve the project's ambiguous migration configuration before deployment.")
			}
			if migration == nil {
				continue
			}
			if pkgRoot != c.RootDirectory {
				return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError("migrationCommand", "The migration belongs to an ancestor directory; configure that project root to review it.")
			}
			migrations++
			if setup.MigrationCommand != "" {
				migration.Command = setup.MigrationCommand
				migration.Origin = OriginUser
			}
			component.Migration, component.MigrationFingerprint = migration, fingerprint
		}
		candidate.Components = append(candidate.Components, component)
	}
	if migrations > 1 || (migrations == 0 && setup.MigrationCommand != "") {
		return DeploymentPlanCandidate{}, DeploymentSetup{}, setupError("migrationCommand", "A migration command must match exactly one detected migration.")
	}
	first := setup.Components[0]
	candidate.PackageManager = PackageManager{Origin: OriginUser, Name: first.PackageManager, Confidence: ConfidenceHigh, Evidence: []Evidence{}}
	candidate.NodeVersion = InferredValue{Origin: OriginUser, Value: first.NodeVersion, Confidence: ConfidenceHigh, Evidence: []Evidence{}}
	if first.InstallCommand != "" {
		candidate.Install = setupCommand(first.InstallCommand, "install", first.RootDirectory)
	}
	normalizeCandidate(&candidate)
	// Include explicit skipped commands, per-component settings and fresh source
	// structure, not just the lossy analysis presentation, in acceptance identity.
	body, err := json.Marshal(struct {
		Version   int
		Source    string
		Setup     DeploymentSetup
		Candidate DeploymentPlanCandidate
	}{SetupVersion, analysis.StructuralFingerprint, setup, candidate})
	if err != nil {
		return DeploymentPlanCandidate{}, DeploymentSetup{}, err
	}
	sum := sha256.Sum256(body)
	candidate.Digest = hex.EncodeToString(sum[:])
	return candidate, setup, nil
}

func setupCommand(command, phase, root string) *Command {
	return &Command{Origin: OriginUser, Phase: phase, Command: command, WorkingDirectory: root, Confidence: ConfidenceHigh, Evidence: []Evidence{{Code: "user_command"}}}
}
