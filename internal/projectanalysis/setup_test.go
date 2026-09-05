package projectanalysis

import (
	"encoding/json"
	"strings"
	"testing"
)

func manualComponent() SetupComponent {
	return SetupComponent{ID: "app", Technology: "node", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}
}

func TestManualSetupWorksWithoutDetectedCandidateAndDoesNotExecute(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"server.js": []byte("throw new Error('must not run')")})
	if len(analysis.Candidates) != 0 {
		t.Fatal("expected no inferred candidate")
	}
	input := DeploymentSetup{Components: []SetupComponent{manualComponent()}}
	candidate, normalized, err := PrepareSetup(analysis, input)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ID != SetupCandidateID || candidate.Status != StatusReady || candidate.Origin != OriginUser || candidate.Install != nil || candidate.Components[0].Build != nil || candidate.Components[0].Run.Command != input.Components[0].StartCommand {
		t.Fatalf("invalid manual candidate: %#v", candidate)
	}
	_, reordered, err := PrepareSetup(analysis, normalized)
	if err != nil || reordered.Components[0].InstallCommand != "" {
		t.Fatal("skipped steps changed")
	}
	input.Components[0].InstallCommand = "npm install"
	changed, _, err := PrepareSetup(analysis, input)
	if err != nil || changed.Digest == candidate.Digest {
		t.Fatal("explicit install did not change acceptance identity")
	}
	encoded, err := json.Marshal(analysis)
	if err != nil || strings.Contains(string(encoded), "must not run") {
		t.Fatal("private snapshot metadata leaked into analysis JSON")
	}
	var decoded SourceAnalysis
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareSetup(decoded, normalized); err == nil {
		t.Fatal("client-supplied analysis substituted for source evidence")
	}
}

func TestManualSetupRejectsInvalidConfigurationAndSource(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"server.js": []byte("// code"), "public/index.html": []byte("hello")})
	cases := map[string]func(*SetupComponent){
		"escape":          func(c *SetupComponent) { c.RootDirectory = "../outside" },
		"absolute":        func(c *SetupComponent) { c.RootDirectory = "/tmp" },
		"windows":         func(c *SetupComponent) { c.RootDirectory = `C:\project` },
		"missing":         func(c *SetupComponent) { c.RootDirectory = "missing" },
		"file":            func(c *SetupComponent) { c.RootDirectory = "server.js" },
		"dependencies":    func(c *SetupComponent) { c.RootDirectory = "node_modules" },
		"runtime":         func(c *SetupComponent) { c.Technology = "python" },
		"node":            func(c *SetupComponent) { c.NodeVersion = "18" },
		"port":            func(c *SetupComponent) { c.InternalPort = 65536 },
		"health":          func(c *SetupComponent) { c.HealthProbe = "/../secret" },
		"empty start":     func(c *SetupComponent) { c.StartCommand = "" },
		"newline":         func(c *SetupComponent) { c.BuildCommand = "npm run build\nother" },
		"whitespace skip": func(c *SetupComponent) { c.InstallCommand = " " },
		"oversized":       func(c *SetupComponent) { c.InstallCommand = strings.Repeat("x", 8193) },
		"static escape": func(c *SetupComponent) {
			c.Technology = "static"
			c.StartCommand = ""
			c.OutputDirectory = "../outside"
		},
		"static command": func(c *SetupComponent) { c.Technology = "static"; c.OutputDirectory = "public" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := manualComponent()
			mutate(&c)
			if _, _, err := PrepareSetup(analysis, DeploymentSetup{Components: []SetupComponent{c}}); err == nil {
				t.Fatal("invalid setup accepted")
			}
		})
	}
	for name, source := range map[string]memoryReader{
		"malformed":       {"package.json": []byte(`{"scripts":`)},
		"duplicate keys":  {"package.json": []byte(`{"scripts":{},"scripts":{}}`)},
		"engine mismatch": {"package.json": []byte(`{"engines":{"node":">=30"}}`)},
		"sensitive only":  {".env": []byte("SECRET=must-not-read")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := PrepareSetup(analyzeMemory(t, source), DeploymentSetup{Components: []SetupComponent{manualComponent()}}); err == nil {
				t.Fatal("unsafe or incompatible source accepted")
			}
		})
	}
}

func TestManualSetupSupportsIndependentStaticAndServerRoots(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"site/index.html": []byte("hello"), "api/server.js": []byte("// code")})
	server, static := manualComponent(), manualComponent()
	server.ID, server.RootDirectory = "a-server", "api"
	static.ID, static.RootDirectory, static.Technology = "z-site", "site", "static"
	static.StartCommand, static.OutputDirectory, static.InternalPort = "", "public files'$(touch no)", 8080
	static.BuildCommand = "npm run build"
	candidate, _, err := PrepareSetup(analysis, DeploymentSetup{Components: []SetupComponent{static, server}})
	if err != nil || len(candidate.Components) != 2 {
		t.Fatalf("paired setup: %v", err)
	}
	for _, c := range candidate.Components {
		if c.Kind == ComponentStatic && c.Run.Command != ManagedStaticCommand(static.OutputDirectory, 8080) {
			t.Fatal("static output was not passed as quoted data")
		}
	}
	static.Technology, static.OutputDirectory, static.StartCommand = "node", "", "node another.js"
	if _, _, err := PrepareSetup(analysis, DeploymentSetup{Components: []SetupComponent{static, server}}); err == nil {
		t.Fatal("two servers accepted")
	}
}

func TestManualSetupPreservesMigrationEvidenceWithoutAStartScript(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"package.json": []byte(`{"dependencies":{"prisma":"6"}}`), "prisma/schema.prisma": []byte(`datasource db { provider = "postgresql" url = env("DATABASE_URL") }`)})
	candidate, _, err := PrepareSetup(analysis, DeploymentSetup{Components: []SetupComponent{manualComponent()}})
	if err != nil || candidate.Components[0].Migration == nil || candidate.Components[0].MigrationFingerprint == "" {
		t.Fatalf("missing migration boundary: %v", err)
	}
	if _, _, err := PrepareSetup(analyzeMemory(t, memoryReader{"server.js": []byte("// code")}), DeploymentSetup{Components: []SetupComponent{manualComponent()}, MigrationCommand: "wipe-database"}); err == nil {
		t.Fatal("undetected migration accepted")
	}
}

func TestManualStaticSetupAcceptsSelectedPrebuiltOutputWithoutMetadata(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"dist/index.html": []byte("<h1>prebuilt</h1>")})
	setup := DeploymentSetup{Components: []SetupComponent{{
		ID: "site", Technology: "static", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24",
		OutputDirectory: "dist", InternalPort: 8080, HealthProbe: "/",
	}}}
	candidate, normalized, err := PrepareSetup(analysis, setup)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Components[0].BuildCommand != "" || candidate.Components[0].StaticOutputDirectory != "dist" || candidate.Components[0].Run.Command != ManagedStaticCommand("dist", 8080) {
		t.Fatalf("prebuilt static candidate = %#v", candidate)
	}
}

func TestManualStaticSetupDefersSkippedOutputCheckUntilAfterInstall(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"index.html": []byte("<h1>source</h1>")})
	setup := DeploymentSetup{Components: []SetupComponent{{
		ID: "site", Technology: "static", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24",
		InstallCommand: "npm install", OutputDirectory: "dist", InternalPort: 8080, HealthProbe: "/",
	}}}
	candidate, _, err := PrepareSetup(analysis, setup)
	if err != nil || candidate.Components[0].StaticOutputDirectory != "dist" {
		t.Fatalf("skipped output was validated before install: candidate=%#v err=%v", candidate, err)
	}
}

func TestManualStaticSetupAcceptsContainedPrebuiltOutput(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"dist/client/index.html": []byte("<h1>prebuilt</h1>")})
	setup := DeploymentSetup{Components: []SetupComponent{{
		ID: "site", Technology: "static", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24",
		OutputDirectory: "dist/client", InternalPort: 8080, HealthProbe: "/",
	}}}
	candidate, _, err := PrepareSetup(analysis, setup)
	if err != nil || candidate.Components[0].StaticOutputDirectory != "dist/client" {
		t.Fatalf("contained prebuilt output was not accepted: candidate=%#v err=%v", candidate, err)
	}
}

func TestManualStaticSetupRejectsProtectedOutputDirectory(t *testing.T) {
	for name, output := range map[string]string{
		"dependencies":         "node_modules/dist",
		"credential directory": "dist/.aws",
		"protected ancestor":   "dist/.aws/client",
		"protected config":     "dist/.config/gh",
		"environment file":     "dist/.env.production",
	} {
		t.Run(name, func(t *testing.T) {
			setup := DeploymentSetup{Components: []SetupComponent{{
				ID: "site", Technology: "static", RootDirectory: ".", PackageManager: "npm", NodeVersion: "24",
				OutputDirectory: output, BuildCommand: "npm run build", InternalPort: 8080, HealthProbe: "/",
			}}}
			if _, err := NormalizeSetup(setup); err == nil {
				t.Fatalf("protected output directory %q accepted", output)
			}
		})
	}
}
