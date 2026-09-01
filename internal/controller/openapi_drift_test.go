package controller

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIDocument struct {
	OpenAPI      string                          `yaml:"openapi"`
	Security     []map[string][]string           `yaml:"security"`
	Paths        map[string]map[string]yaml.Node `yaml:"paths"`
	ProblemCodes map[string]openAPIProblemCode   `yaml:"x-rig-problem-codes"`
	Components   struct {
		Schemas         map[string]any `yaml:"schemas"`
		Responses       map[string]any `yaml:"responses"`
		SecuritySchemes map[string]any `yaml:"securitySchemes"`
	} `yaml:"components"`
}

type openAPIOperation struct {
	OperationID  string                `yaml:"operationId"`
	Responses    map[string]any        `yaml:"responses"`
	Security     []map[string][]string `yaml:"security"`
	ProblemCodes []string              `yaml:"x-rig-problem-codes"`
}

type openAPIProblemCode struct {
	Description string `yaml:"description"`
	Statuses    []int  `yaml:"statuses"`
}

var expectedOpenAPIProblemCatalog = map[string]openAPIProblemCode{
	"authentication_required":         {Description: "GitHub authorization does not permit the requested operation", Statuses: []int{403}},
	"source_access_lost":              {Description: "Access to the selected GitHub source must be restored", Statuses: []int{409}},
	"provider_unavailable":            {Description: "GitHub or its configured integration is temporarily unavailable", Statuses: []int{503}},
	"invalid_source":                  {Description: "The selected GitHub source is invalid or cannot be used", Statuses: []int{400, 422}},
	"approval_required":               {Description: "Deployment requires an administrator approval before it can continue", Statuses: []int{409}},
	"migration_approval_required":     {Description: "Deployment migration requires approval before it can continue", Statuses: []int{409}},
	"application_busy":                {Description: "The application already has an active conflicting operation", Statuses: []int{409}},
	"source_too_large":                {Description: "The source exceeds the supported inspection limits", Statuses: []int{413}},
	"deployment_plan_conflict":        {Description: "The accepted deployment plan changed while this request was being reviewed", Statuses: []int{409}},
	"deployment_plan_review_required": {Description: "The source structure or inferred deployment behavior changed and must be reviewed again", Statuses: []int{409}},
	"deployment_plan_not_found":       {Description: "No accepted deployment plan exists for the application", Statuses: []int{404}},
	"invalid_deployment_plan":         {Description: "One or more deployment plan fields are invalid", Statuses: []int{422}},
	"migration_approval_conflict":     {Description: "The migration approval changed while this request was being reviewed", Statuses: []int{409}},
	"deployment_plan_forbidden":       {Description: "Administrator access is required to accept a deployment plan or approve its migration", Statuses: []int{403}},
	"relay_unavailable":               {Description: "The configured controller relay is unavailable", Statuses: []int{503}},
}

var expectedOpenAPIOperationProblemCodes = map[string][]string{
	"createApplication":                         {"authentication_required", "source_access_lost", "provider_unavailable", "invalid_source", "source_too_large"},
	"inspectImport":                             {"authentication_required", "source_access_lost", "provider_unavailable", "invalid_source", "source_too_large"},
	"getApplicationDeploymentPlan":              {"deployment_plan_not_found"},
	"acceptApplicationDeploymentPlan":           {"authentication_required", "source_access_lost", "provider_unavailable", "invalid_source", "source_too_large", "deployment_plan_conflict", "deployment_plan_review_required", "invalid_deployment_plan", "deployment_plan_forbidden"},
	"approveApplicationDeploymentPlanMigration": {"deployment_plan_not_found", "deployment_plan_conflict", "migration_approval_conflict", "invalid_deployment_plan", "deployment_plan_forbidden"},
	"deployApplication":                         {"application_busy"},
	"deployRelease":                             {"application_busy"},
	"startApplication":                          {"application_busy"},
	"stopApplication":                           {"application_busy"},
	"restartApplication":                        {"application_busy"},
	"resumeJob":                                 {"approval_required", "migration_approval_required"},
	"startGitHubDeviceConnection":               {"authentication_required", "provider_unavailable"},
	"pollGitHubDeviceConnection":                {"authentication_required", "source_access_lost", "provider_unavailable"},
	"refreshSourceConnection":                   {"authentication_required", "source_access_lost", "provider_unavailable"},
	"listGitHubInstallations":                   {"authentication_required", "source_access_lost", "provider_unavailable"},
	"listGitHubRepositories":                    {"source_access_lost", "provider_unavailable", "invalid_source", "source_too_large"},
	"listGitHubBranches":                        {"source_access_lost", "provider_unavailable", "invalid_source", "source_too_large"},
	"getRelayStatus":                            {"relay_unavailable"},
	"startRelayEnrollment":                      {"authentication_required", "source_access_lost", "provider_unavailable", "invalid_source", "relay_unavailable"},
	"pollRelayEnrollment":                       {"relay_unavailable"},
	"removeRelayBinding":                        {"relay_unavailable"},
	"startRelayKeyRotation":                     {"relay_unavailable"},
	"updateApplicationAutoDeploy":               {"source_access_lost", "application_busy"},
	"resumeApplicationAutoDeploy":               {"source_access_lost"},
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
	if !strings.Contains(string(content), "pauseDisposition: {type: string, enum: [approval_required, migration_approval_required, insufficient_replacement_capacity]}") {
		t.Error("job pause dispositions must remain an explicit stable enum")
	}
	if _, ok := document.Components.Responses["Problem"]; !ok {
		t.Error("missing reusable problem response")
	}
	assertOpenAPIProblemContract(t, content, document)

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

func assertOpenAPIProblemContract(t *testing.T, content []byte, document openAPIDocument) {
	t.Helper()
	assertNoDuplicateProblemCatalogEntries(t, content)
	if !reflect.DeepEqual(document.ProblemCodes, expectedOpenAPIProblemCatalog) {
		t.Fatalf("problem-code catalog drifted\nwant: %#v\n got: %#v", expectedOpenAPIProblemCatalog, document.ProblemCodes)
	}

	actual := make(map[string][]string)
	coverage := make(map[string]int)
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
			seen := make(map[string]bool, len(operation.ProblemCodes))
			for _, code := range operation.ProblemCodes {
				if seen[code] {
					t.Errorf("%s duplicates problem-code annotation %q", operation.OperationID, code)
				}
				seen[code] = true
				if _, known := document.ProblemCodes[code]; !known {
					t.Errorf("%s annotates unknown problem code %q", operation.OperationID, code)
				}
				coverage[code]++
			}
			if len(operation.ProblemCodes) > 0 {
				actual[operation.OperationID] = operation.ProblemCodes
			}
		}
	}
	for code := range document.ProblemCodes {
		if coverage[code] == 0 {
			t.Errorf("problem code %q has no operation coverage", code)
		}
	}
	if !reflect.DeepEqual(actual, expectedOpenAPIOperationProblemCodes) {
		t.Fatalf("operation problem-code annotations drifted\nwant: %#v\n got: %#v", expectedOpenAPIOperationProblemCodes, actual)
	}
}

func assertNoDuplicateProblemCatalogEntries(t *testing.T, content []byte) {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		t.Fatal("OpenAPI document must be a mapping")
	}
	document := root.Content[0]
	for index := 0; index+1 < len(document.Content); index += 2 {
		if document.Content[index].Value != "x-rig-problem-codes" {
			continue
		}
		catalog := document.Content[index+1]
		if catalog.Kind != yaml.MappingNode {
			t.Fatal("x-rig-problem-codes must be a mapping")
		}
		seen := make(map[string]bool, len(catalog.Content)/2)
		for catalogIndex := 0; catalogIndex+1 < len(catalog.Content); catalogIndex += 2 {
			code := catalog.Content[catalogIndex].Value
			if seen[code] {
				t.Errorf("duplicate problem-code catalog entry %q", code)
			}
			seen[code] = true
		}
		return
	}
	t.Fatal("missing x-rig-problem-codes catalog")
}
