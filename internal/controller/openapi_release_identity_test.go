package controller

import (
	"os"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIRebindReleaseIdentityMatchesMaterializedReleases(t *testing.T) {
	content, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type identitySchema struct {
		Type    string `yaml:"type"`
		Format  string `yaml:"format"`
		Pattern string `yaml:"pattern"`
	}
	var document struct {
		Components struct {
			Parameters map[string]struct {
				Schema identitySchema `yaml:"schema"`
			} `yaml:"parameters"`
			Schemas map[string]struct {
				Properties map[string]identitySchema `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	release := document.Components.Parameters["ReleaseId"].Schema
	if release.Type != "string" || release.Format != "" || release.Pattern == "" {
		t.Fatal("release path parameter lacks its identity contract")
	}
	for _, field := range []struct{ schema, property string }{
		{"LANGatewayRebindRosterEntry", "servingReleaseId"},
		{"LANGatewayRebindRuntimeHead", "releaseId"},
	} {
		t.Run(field.schema, func(t *testing.T) {
			property := document.Components.Schemas[field.schema].Properties[field.property]
			if property != release {
				t.Fatal("rebind release identity differs from the release path contract")
			}
			pattern, err := regexp.Compile(property.Pattern)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				value string
				valid bool
			}{
				{"0123456789abcdef0123456789abcdef", true},
				{"01234567-89ab-cdef-0123-456789abcdef", true},
				{"", false},
				{"0123456789ABCDEF0123456789ABCDEF", false},
				{"0123456789abcdef0123456789abcde", false},
				{"0123456789abcdef0123456789abcdef0", false},
				{"0123456789abcdef0123456789abcdeg", false},
				{" 0123456789abcdef0123456789abcdef", false},
				{"01234567-89AB-cdef-0123-456789abcdef", false},
			} {
				if got := pattern.MatchString(test.value); got != test.valid {
					t.Errorf("release identity %q accepted=%t, want=%t", test.value, got, test.valid)
				}
			}
		})
	}
}
