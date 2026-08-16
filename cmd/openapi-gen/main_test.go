package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConcreteModelsPreserveFieldsTypesReferencesAndRequiredness(t *testing.T) {
	var spec document
	if err := yaml.Unmarshal([]byte(`
components:
  schemas:
    Child:
      type: object
      required: [id]
      properties:
        id: {type: string}
        count: {type: integer, format: int64}
    Envelope:
      type: object
      required: [child, items, labels]
      properties:
        child: {$ref: '#/components/schemas/Child'}
        items: {type: array, items: {$ref: '#/components/schemas/Child'}}
        labels: {type: object, additionalProperties: {type: string}}
        note: {type: string}
`), &spec); err != nil {
		t.Fatal(err)
	}

	goArtifact, err := renderGo("fixture", nil, spec.Components.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"ID    string `json:\"id\"`",
		"Count int64  `json:\"count,omitempty\"`",
		"Child  Child             `json:\"child\"`",
		"Items  []Child           `json:\"items\"`",
		"Labels map[string]string `json:\"labels\"`",
		"Note   string            `json:\"note,omitempty\"`",
	} {
		if !strings.Contains(string(goArtifact), expected) {
			t.Errorf("generated Go contract missing %q:\n%s", expected, goArtifact)
		}
	}

	typeScriptArtifact, err := renderTypeScript("fixture", nil, spec.Components.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"count"?: number;`,
		`"child": Child;`,
		`"items": Child[];`,
		`"labels": Record<string, string>;`,
		`"note"?: string;`,
	} {
		if !strings.Contains(string(typeScriptArtifact), expected) {
			t.Errorf("generated TypeScript contract missing %q:\n%s", expected, typeScriptArtifact)
		}
	}
}

func TestConcreteModelGenerationRejectsMissingRequiredProperty(t *testing.T) {
	schemas := map[string]schema{
		"Broken": {Type: "object", Required: []string{"missing"}, Properties: map[string]schema{}},
	}
	if _, err := renderGo("fixture", nil, schemas); err == nil || !strings.Contains(err.Error(), `required property "missing" is not defined`) {
		t.Fatalf("Go generation error = %v", err)
	}
	if _, err := renderTypeScript("fixture", nil, schemas); err == nil || !strings.Contains(err.Error(), `required property "missing" is not defined`) {
		t.Fatalf("TypeScript generation error = %v", err)
	}
}
