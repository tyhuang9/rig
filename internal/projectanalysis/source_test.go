package projectanalysis

import "testing"

func TestPrebuiltStaticArtifactsStayOutOfDetectionAndFingerprints(t *testing.T) {
	base := memoryReader{
		"package.json":      []byte(`{"scripts":{"build":"vite build"},"dependencies":{"vite":"6"}}`),
		"package-lock.json": []byte(`{"lockfileVersion":3}`),
	}
	withArtifacts := memoryReader{
		"package.json":           base["package.json"],
		"package-lock.json":      base["package-lock.json"],
		"dist/index.html":        []byte("<h1>prebuilt</h1>"),
		"dist/assets/app.js":     []byte("window.ready = true"),
		"dist/node_modules/x.js": []byte("excluded"),
	}
	without := analyzeMemory(t, base)
	with := analyzeMemory(t, withArtifacts)
	if with.StructuralFingerprint != without.StructuralFingerprint {
		t.Fatalf("prebuilt output changed structural fingerprint: %s != %s", with.StructuralFingerprint, without.StructuralFingerprint)
	}
	if len(with.Candidates) != len(without.Candidates) || len(with.Candidates) != 1 || with.Candidates[0].Digest != without.Candidates[0].Digest {
		t.Fatalf("prebuilt output changed detection: %#v != %#v", with.Candidates, without.Candidates)
	}
	if with.source == nil || len(with.source.prebuiltFiles) != 2 {
		t.Fatalf("prebuilt output was not retained as private existence evidence: %#v", with.source)
	}
}

func TestStaticOutputAtSourceRootUsesOrdinarySourceEvidence(t *testing.T) {
	analysis := analyzeMemory(t, memoryReader{"index.html": []byte("<h1>root</h1>")})
	if analysis.source == nil || !analysis.source.hasStaticOutput(".", ".") {
		t.Fatal("ordinary root source files were not recognized as static output evidence")
	}
}
