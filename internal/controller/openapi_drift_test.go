package controller

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIDocument struct {
	OpenAPI    string                          `yaml:"openapi"`
	Security   []map[string][]string           `yaml:"security"`
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas         map[string]any `yaml:"schemas"`
		Responses       map[string]any `yaml:"responses"`
		SecuritySchemes map[string]any `yaml:"securitySchemes"`
	} `yaml:"components"`
}

type openAPIOperation struct {
	OperationID string                `yaml:"operationId"`
	Responses   map[string]any        `yaml:"responses"`
	Security    []map[string][]string `yaml:"security"`
}

func TestOpenAPIContractMatchesRegisteredRoutes(t *testing.T) {
	content, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q", document.OpenAPI)
	}
	if len(document.Security) != 1 || len(document.Components.SecuritySchemes) != 2 {
		t.Fatal("cookie authentication and CSRF security schemes must be explicit")
	}
	for _, required := range []string{"Problem", "SessionResponse", "Application", "Service", "Job", "JobEvent", "Machine", "Diagnostics"} {
		if _, ok := document.Components.Schemas[required]; !ok {
			t.Errorf("missing required schema %q", required)
		}
	}
	if _, ok := document.Components.Responses["Problem"]; !ok {
		t.Error("missing reusable problem response")
	}

	implemented := make([]string, 0)
	for _, route := range (&Server{}).apiRoutes() {
		implemented = append(implemented, route.method+" "+route.path+" "+route.operationID)
	}
	documented := make([]string, 0)
	for path, pathItem := range document.Paths {
		for method, node := range pathItem {
			method = strings.ToUpper(method)
			if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
				continue
			}
			var operation openAPIOperation
			if err := node.Decode(&operation); err != nil {
				t.Fatalf("decode %s %s: %v", method, path, err)
			}
			if operation.OperationID == "" || len(operation.Responses) == 0 {
				t.Errorf("%s %s must define operationId and responses", method, path)
			}
			documented = append(documented, method+" "+path+" "+operation.OperationID)
		}
	}
	sort.Strings(implemented)
	sort.Strings(documented)
	if strings.Join(implemented, "\n") != strings.Join(documented, "\n") {
		t.Fatalf("registered routes and OpenAPI drifted\nimplemented:\n%s\n\ndocumented:\n%s", strings.Join(implemented, "\n"), strings.Join(documented, "\n"))
	}
}
