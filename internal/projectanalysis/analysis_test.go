package projectanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

type memoryReader map[string][]byte

func (m memoryReader) ReadFile(_ context.Context, name string, maxBytes int64) ([]byte, error) {
	b, ok := m[name]
	if !ok {
		return nil, errors.New("not found")
	}
	if int64(len(b)) > maxBytes {
		return nil, ErrFileTooLarge
	}
	return slices.Clone(b), nil
}

func sourceFiles(contents memoryReader) []File {
	files := make([]File, 0, len(contents))
	for name, body := range contents {
		files = append(files, File{Path: name, Size: int64(len(body))})
	}
	return files
}

func analyzeMemory(t *testing.T, contents memoryReader) SourceAnalysis {
	t.Helper()
	got, err := Analyze(context.Background(), sourceFiles(contents), contents)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return got
}

func TestAnalyzeViteAndExpressWorkspace(t *testing.T) {
	contents := memoryReader{
		"package.json": []byte(`{
			"packageManager":"pnpm@10.0.0",
			"workspaces":["apps/*"],
			"scripts":{"build":"pnpm -r build"}
		}`),
		"pnpm-lock.yaml": []byte("lockfileVersion: '9.0'\n"),
		".nvmrc":         []byte("22.14.0\n"),
		"apps/web/package.json": []byte(`{
			"name":"web","scripts":{"build":"vite build"},
			"dependencies":{"vite":"6.0.0"}
		}`),
		"apps/api/package.json": []byte(`{
			"name":"api","scripts":{"build":"tsc","start":"node dist/index.js","migrate":"prisma migrate deploy"},
			"dependencies":{"express":"5.0.0","prisma":"6.0.0"}
		}`),
		"apps/api/prisma/schema.prisma": []byte("datasource db { provider = \"postgresql\" }\n"),
	}

	got := analyzeMemory(t, contents)
	if got.SchemaVersion != SchemaVersion || got.StructuralFingerprint == "" {
		t.Fatalf("missing stable metadata: %#v", got)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1: %#v", len(got.Candidates), got.Candidates)
	}
	plan := got.Candidates[0]
	if plan.Status != StatusReady || plan.Kind != PlanKindJavaScript {
		t.Fatalf("status/kind = %q/%q, want ready/javascript: %#v", plan.Status, plan.Kind, plan)
	}
	if plan.PackageManager.Name != "pnpm" || plan.Install.Command != "corepack pnpm install --frozen-lockfile" {
		t.Fatalf("package manager/install = %#v / %#v", plan.PackageManager, plan.Install)
	}
	if plan.NodeVersion.Value != "22.14.0" || plan.NodeVersion.Provenance != ProvenanceRuntimeFile {
		t.Fatalf("node version = %#v", plan.NodeVersion)
	}
	if len(plan.Components) != 2 {
		t.Fatalf("components = %#v", plan.Components)
	}
	web := componentByRoot(t, plan, "apps/web")
	if web.Kind != ComponentStatic || web.Framework != FrameworkVite || web.StaticOutputDirectory != "dist" {
		t.Fatalf("web = %#v", web)
	}
	if web.Run == nil || web.Run.Command != ManagedStaticServerCommand || web.Run.Provenance != ProvenanceManagedRuntime {
		t.Fatalf("web run = %#v", web.Run)
	}
	api := componentByRoot(t, plan, "apps/api")
	if api.Kind != ComponentServer || api.Framework != FrameworkExpress || api.Run == nil || api.Run.Command != "pnpm run start" {
		t.Fatalf("api = %#v", api)
	}
	if api.Migration == nil || api.Migration.Command != "pnpm exec prisma migrate deploy" || api.MigrationFingerprint == "" {
		t.Fatalf("migration = %#v", api.Migration)
	}
	if plan.Digest == "" {
		t.Fatal("candidate digest is empty")
	}
}

func TestAnalyzeNestUsesStartProdAndPinnedNodeFallback(t *testing.T) {
	got := analyzeMemory(t, memoryReader{
		"package.json": []byte(`{
			"packageManager":"npm@11.0.0",
			"scripts":{"build":"nest build","start":"nest start","start:prod":"node dist/main"},
			"dependencies":{"@nestjs/core":"11.0.0"}
		}`),
		"package-lock.json": []byte(`{"lockfileVersion":3}`),
	})
	plan := got.Candidates[0]
	component := plan.Components[0]
	if plan.Status != StatusReady || component.Framework != FrameworkNestJS {
		t.Fatalf("plan = %#v", plan)
	}
	if component.Run == nil || component.Run.Command != "npm run start:prod" {
		t.Fatalf("run = %#v", component.Run)
	}
	if plan.NodeVersion.Value != PinnedNodeLTS || plan.NodeVersion.Provenance != ProvenanceRuntimeDefault {
		t.Fatalf("node fallback = %#v", plan.NodeVersion)
	}
}

func TestAnalyzeNeedsInputForConflictingLockfiles(t *testing.T) {
	got := analyzeMemory(t, memoryReader{
		"package.json":      []byte(`{"scripts":{"start":"node server.js"},"dependencies":{"fastify":"5.0.0"}}`),
		"package-lock.json": []byte(`{"lockfileVersion":3}`),
		"yarn.lock":         []byte("# yarn lockfile v1\n"),
	})
	plan := got.Candidates[0]
	if plan.Status != StatusNeedsInput || !slices.Contains(plan.MissingFields, FieldPackageManager) {
		t.Fatalf("plan = %#v", plan)
	}
	assertFinding(t, plan.Findings, "conflicting_lockfiles")
}

func TestAnalyzeNeedsInputForAmbiguousAPIsAndMissingStart(t *testing.T) {
	got := analyzeMemory(t, memoryReader{
		"package.json":        []byte(`{"packageManager":"npm@11","workspaces":["apps/*"]}`),
		"package-lock.json":   []byte(`{"lockfileVersion":3}`),
		"apps/a/package.json": []byte(`{"scripts":{"start":"node a"},"dependencies":{"express":"5"}}`),
		"apps/b/package.json": []byte(`{"scripts":{"build":"tsc"},"dependencies":{"fastify":"5"}}`),
	})
	plan := got.Candidates[0]
	if plan.Status != StatusNeedsInput {
		t.Fatalf("status = %q", plan.Status)
	}
	assertFinding(t, plan.Findings, "multiple_server_components")
	if !slices.Contains(plan.MissingFields, "components.apps-b.run") {
		t.Fatalf("missing fields = %#v", plan.MissingFields)
	}
}

func TestAnalyzeDetectsContainerCandidates(t *testing.T) {
	got := analyzeMemory(t, memoryReader{
		"compose.yaml": []byte("services: {}\n"),
		"Dockerfile":   []byte("FROM node:24-alpine\n"),
	})
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %#v", got.Candidates)
	}
	if got.Candidates[0].Kind != PlanKindCompose || got.Candidates[0].ConfigPath != "compose.yaml" || got.Candidates[0].Status != StatusNeedsInput {
		t.Fatalf("compose candidate = %#v", got.Candidates[0])
	}
	if got.Candidates[1].Kind != PlanKindDockerfile || got.Candidates[1].ConfigPath != "Dockerfile" || got.Candidates[1].Status != StatusNeedsInput {
		t.Fatalf("docker candidate = %#v", got.Candidates[1])
	}
}

func TestAnalyzeIsByteStableAcrossInputOrder(t *testing.T) {
	contents := memoryReader{
		"package.json":      []byte(`{"scripts":{"build":"vite build"},"dependencies":{"vite":"6"}}`),
		"package-lock.json": []byte(`{"lockfileVersion":3}`),
	}
	files := sourceFiles(contents)
	a, err := Analyze(context.Background(), files, contents)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(files)
	b, err := Analyze(context.Background(), files, contents)
	if err != nil {
		t.Fatal(err)
	}
	encodedA, _ := json.Marshal(a)
	encodedB, _ := json.Marshal(b)
	if string(encodedA) != string(encodedB) {
		t.Fatalf("outputs differ:\n%s\n%s", encodedA, encodedB)
	}
}

func TestMigrationContentChangeInvalidatesCandidateDigest(t *testing.T) {
	contents := memoryReader{
		"package.json": []byte(`{
			"packageManager":"npm@11.0.0",
			"scripts":{"start":"node index.js"},
			"dependencies":{"express":"5","prisma":"6"}
		}`),
		"package-lock.json":       []byte(`{"lockfileVersion":3}`),
		"prisma/schema.prisma":    []byte("model A { id Int @id }\n"),
		"prisma/migrations/1.sql": []byte("SELECT 1;\n"),
	}
	before := analyzeMemory(t, contents).Candidates[0]
	contents["prisma/migrations/1.sql"] = []byte("SELECT 2;\n") // same byte length
	after := analyzeMemory(t, contents).Candidates[0]
	if before.Components[0].MigrationFingerprint == after.Components[0].MigrationFingerprint {
		t.Fatal("same-size migration edit did not change migration fingerprint")
	}
	if before.Digest == after.Digest {
		t.Fatal("same-size migration edit did not invalidate candidate digest")
	}
}

func TestAnalyzeRejectsUnsafeDuplicateAndChangedPaths(t *testing.T) {
	tests := []struct {
		name   string
		files  []File
		reader memoryReader
		code   ErrorCode
	}{
		{"traversal", []File{{Path: "../package.json", Size: 2}}, memoryReader{}, CodeUnsafePath},
		{"backslash", []File{{Path: `app\package.json`, Size: 2}}, memoryReader{}, CodeUnsafePath},
		{"case duplicate", []File{{Path: "App/package.json", Size: 2}, {Path: "app/package.json", Size: 2}}, memoryReader{}, CodeDuplicatePath},
		{"changed size", []File{{Path: "package.json", Size: 1}}, memoryReader{"package.json": []byte(`{}`)}, CodeSourceChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Analyze(context.Background(), tt.files, tt.reader)
			if !IsErrorCode(err, tt.code) {
				t.Fatalf("error = %v, want %s", err, tt.code)
			}
		})
	}
}

func TestAnalyzeRejectsMalformedHostilePackageJSON(t *testing.T) {
	deep := []byte(`{"x":`)
	for range maxJSONDepth + 1 {
		deep = append(deep, `{"x":`...)
	}
	deep = append(deep, '0')
	for range maxJSONDepth + 1 {
		deep = append(deep, '}')
	}
	deep = append(deep, '}')

	tests := []struct {
		name string
		body []byte
		code string
	}{
		{"duplicate key", []byte(`{"scripts":{},"scripts":{"start":"bad"}}`), "duplicate_json_key"},
		{"excessive nesting", deep, "json_too_deep"},
		{"malformed", []byte(`{"scripts":`), "malformed_package_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analyzeMemory(t, memoryReader{"package.json": tt.body})
			if len(got.Candidates) != 1 || got.Candidates[0].Status != StatusUnsupported {
				t.Fatalf("candidates = %#v", got.Candidates)
			}
			assertFinding(t, got.Candidates[0].Findings, tt.code)
		})
	}
}

func TestAnalyzeDoesNotReadExcludedFiles(t *testing.T) {
	reader := &recordingReader{memoryReader: memoryReader{
		"package.json":      []byte(`{"scripts":{"start":"node server"},"dependencies":{"express":"5"}}`),
		".env.production":   []byte("SECRET=value"),
		"node_modules/x.js": []byte("malicious"),
		"dist/package.json": []byte(`{"scripts":{"start":"malicious"}}`),
	}}
	got, err := Analyze(context.Background(), sourceFiles(reader.memoryReader), reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{".env.production", "node_modules/x.js", "dist/package.json"} {
		if slices.Contains(reader.reads, forbidden) {
			t.Fatalf("excluded file %q was read: %#v", forbidden, reader.reads)
		}
	}
	assertFinding(t, got.Findings, "excluded_sensitive_file")
}

type recordingReader struct {
	memoryReader
	reads []string
}

func (r *recordingReader) ReadFile(ctx context.Context, name string, maxBytes int64) ([]byte, error) {
	r.reads = append(r.reads, name)
	return r.memoryReader.ReadFile(ctx, name, maxBytes)
}

func componentByRoot(t *testing.T, plan DeploymentPlanCandidate, root string) Component {
	t.Helper()
	for _, component := range plan.Components {
		if component.RootDirectory == root {
			return component
		}
	}
	t.Fatalf("component root %q not found in %#v", root, plan.Components)
	return Component{}
}

func assertFinding(t *testing.T, findings []Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("finding %q not found in %#v", code, findings)
}
