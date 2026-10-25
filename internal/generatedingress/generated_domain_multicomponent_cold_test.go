//go:build linux || windows

package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"text/template"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedbuilder"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/projectanalysis"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const generatedDomainTwoComponentBuilderSuffix = "aaaaaaaaaaaaaaaaaaaaaaaa"

type generatedDomainTwoComponentColdFixture struct {
	identity     generatedDomainIdentityFixture
	migration    *generatedMigrationFixture
	runner       *generatedDomainConstructionRunner
	construction runtimeprocess.CommandRunner
	builder      *generatedDomainTwoComponentBuilderFixture
	environment  *candidateFactoryEnvironment
}

type generatedDomainTwoComponentBuilderFixture struct {
	directory *securetemp.GeneratedBuilderDirectory
	identity  generatedbuilder.Identity
	physical  generatedDomainColdBuilderPhysical
	runner    *generatedDomainTwoComponentRouteRunner
}

type generatedDomainCandidateRosterRunner struct {
	physical   []candidateOwnerPhysical
	failKind   candidateOwnerCommandKind
	failErr    error
	beforeKind candidateOwnerCommandKind
	before     func()
	afterKind  candidateOwnerCommandKind
	afterAt    int
	afterSeen  int
	after      func()
}

type generatedDomainTwoComponentColdCase struct {
	name                   string
	builder                bool
	mutate                 func(*generatedDomainTwoComponentColdFixture)
	pass                   bool
	stage                  generatedDomainConstructionStage
	reason                 generatedDomainConstructionReason
	withoutRefusalSentinel bool
	privateCause           bool
}

type generatedDomainTwoComponentColdPartition uint8

const (
	generatedDomainTwoComponentColdBaseline generatedDomainTwoComponentColdPartition = iota
	generatedDomainTwoComponentColdBaselinePart2
	generatedDomainTwoComponentColdGroupAttestation
	generatedDomainTwoComponentColdGroupAttestationPart2
	generatedDomainTwoComponentColdBuilderNamespace
	generatedDomainTwoComponentColdBuilderNamespacePart2
	generatedDomainTwoComponentColdBuilderIdentityAndRead
	generatedDomainTwoComponentColdBuilderIdentityAndReadPart2
	generatedDomainTwoComponentColdBuilderPhysicalPolicy
	generatedDomainTwoComponentColdBuilderPhysicalPolicyPart2
	generatedDomainTwoComponentColdBuilderRequiredNetworkOptions
	generatedDomainTwoComponentColdBuilderRequiredNetworkOptionsPart2
	generatedDomainTwoComponentColdBuilderNetworkOptionCorrespondence
	generatedDomainTwoComponentColdBuilderNetworkOptionCorrespondencePart2
	generatedDomainTwoComponentColdBuilderFinalIdentity
	generatedDomainTwoComponentColdBuilderFinalIdentityPart2
)

type generatedDomainTwoComponentRouteRunner struct {
	daemonID           string
	delegate           runtimeprocess.CommandRunner
	builder            *generatedDomainColdBuilderPhysical
	afterFirstPhysical func()
	imageReads         int
	builderReads       int
	failBuilderReadAt  int
}

const generatedDomainTwoComponentBuilderPrivateReadError = "private persisted builder observation failed"

var generatedDomainTwoComponentGroupPrivateReadError = errors.New("private cold group attestation failed")

func TestGeneratedDomainColdBuilderContainerProjectionHonorsDockerTypedAndRawFallbackFields(t *testing.T) {
	type dockerContainerShape struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		Image string `json:"Image"`
		State struct {
			Running    bool `json:"Running"`
			Paused     bool `json:"Paused"`
			Restarting bool `json:"Restarting"`
		} `json:"State"`
		Config struct {
			Image  string            `json:"Image"`
			Cmd    []string          `json:"Cmd"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		HostConfig struct {
			Memory       int64                              `json:"Memory"`
			MemorySwap   int64                              `json:"MemorySwap"`
			CPUPeriod    int64                              `json:"CpuPeriod"`
			CPUQuota     int64                              `json:"CpuQuota"`
			PidsLimit    *int64                             `json:"PidsLimit"`
			NetworkMode  string                             `json:"NetworkMode"`
			Privileged   bool                               `json:"Privileged"`
			Binds        []string                           `json:"Binds"`
			Mounts       []generatedbuilder.ConfiguredMount `json:"Mounts"`
			PortBindings map[string]json.RawMessage         `json:"PortBindings"`
			LogConfig    struct {
				Type   string            `json:"Type"`
				Config map[string]string `json:"Config"`
			} `json:"LogConfig"`
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
		} `json:"HostConfig"`
		NetworkSettings struct {
			Networks map[string]generatedbuilder.NetworkAttachment `json:"Networks"`
		} `json:"NetworkSettings"`
		Mounts []generatedbuilder.Mount `json:"Mounts"`
	}

	pids := generatedbuilder.PIDsLimit
	var input dockerContainerShape
	input.ID = strings.Repeat("b", 64)
	input.Name = "/rig-buildkitd-" + generatedDomainTwoComponentBuilderSuffix
	input.Image = "sha256:" + strings.Repeat("a", 64)
	input.HostConfig.CPUPeriod = 100000
	input.HostConfig.CPUQuota = 100000
	input.HostConfig.PidsLimit = &pids

	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal("Docker inspection raw response did not encode")
	}
	originalFormat := strings.NewReplacer(
		".HostConfig.CPUPeriod", ".HostConfig.CpuPeriod",
		".HostConfig.CPUQuota", ".HostConfig.CpuQuota",
	).Replace(generatedDomainColdBuilderContainerInspectFormat)
	typedFormat := strings.NewReplacer(
		".HostConfig.CpuPeriod", ".HostConfig.CPUPeriod",
		".HostConfig.CpuQuota", ".HostConfig.CPUQuota",
	).Replace(generatedDomainColdBuilderContainerInspectFormat)
	if originalFormat == typedFormat {
		t.Fatal("Docker inspection selector variants were not distinct")
	}
	if generatedDomainColdBuilderContainerInspectFormat != typedFormat {
		t.Fatal("generated builder container projection did not use typed Docker selectors")
	}

	originalResult := renderDockerInspectWithRawFallback(t, originalFormat, input, raw)
	if originalResult.typedErr == nil || !originalResult.usedFallback || originalResult.fallbackErr == nil ||
		!strings.Contains(originalResult.fallbackErr.Error(), `map has no entry for key "ID"`) ||
		len(originalResult.body) != 0 {
		t.Fatalf("legacy Docker inspection renderer = typed %v, fallback %v, body %d bytes",
			originalResult.typedErr, originalResult.fallbackErr, len(originalResult.body))
	}
	typedResult := renderDockerInspectWithRawFallback(t, typedFormat, input, raw)
	if typedResult.typedErr != nil || typedResult.usedFallback || typedResult.fallbackErr != nil {
		t.Fatalf("typed Docker inspection renderer = typed %v, fallback %v",
			typedResult.typedErr, typedResult.fallbackErr)
	}
	typedProjection, typedErr := generatedbuilder.DecodeContainer(typedResult.body)
	if typedErr != nil || typedProjection.HostConfig.CPUPeriod != input.HostConfig.CPUPeriod ||
		typedProjection.HostConfig.CPUQuota != input.HostConfig.CPUQuota ||
		typedProjection.HostConfig.PidsLimit != pids || typedProjection.Image != input.Image ||
		!generatedDomainCanonicalDockerID(typedProjection.Image, true) {
		t.Fatalf("typed generated builder projection = %#v, %v", typedProjection, typedErr)
	}
}

type dockerInspectRenderResult struct {
	body         []byte
	typedErr     error
	usedFallback bool
	fallbackErr  error
}

func renderDockerInspectWithRawFallback(t *testing.T, format string, typed any, raw []byte) dockerInspectRenderResult {
	t.Helper()
	templateValue, err := template.New("docker-inspect").Funcs(template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
	}).Parse(format)
	if err != nil {
		t.Fatal("Docker inspection template did not parse")
	}
	var rendered bytes.Buffer
	typedErr := templateValue.Execute(&rendered, typed)
	if typedErr == nil {
		return dockerInspectRenderResult{body: rendered.Bytes()}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var rawValue any
	if err := decoder.Decode(&rawValue); err != nil {
		t.Fatal("Docker inspection raw response did not decode")
	}
	rendered.Reset()
	fallbackErr := templateValue.Option("missingkey=error").Execute(&rendered, rawValue)
	if fallbackErr != nil {
		return dockerInspectRenderResult{typedErr: typedErr, usedFallback: true, fallbackErr: fallbackErr}
	}
	return dockerInspectRenderResult{body: rendered.Bytes(), typedErr: typedErr, usedFallback: true}
}

func (r *generatedDomainTwoComponentRouteRunner) builderResult(
	result runtimeprocess.CommandResult, image bool,
) (runtimeprocess.CommandResult, error) {
	r.builderReads++
	if r.builderReads == r.failBuilderReadAt {
		return runtimeprocess.CommandResult{}, errors.New(generatedDomainTwoComponentBuilderPrivateReadError)
	}
	if image {
		r.imageReads++
		if r.imageReads == 1 && r.afterFirstPhysical != nil {
			r.afterFirstPhysical()
		}
	}
	return result, nil
}

func (r *generatedDomainTwoComponentRouteRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if r.daemonID != "" && len(request.Args) == 3 && request.Args[0] == "info" && request.Args[1] == "--format" &&
		request.Args[2] == "{{json .ID}}" {
		return jsonResult(r.daemonID), nil
	}
	if r.builder != nil && len(request.Args) == 5 && request.Args[1] == "inspect" &&
		request.Args[2] == "--format" {
		switch {
		case request.Args[0] == "container" && request.Args[3] == generatedDomainColdBuilderContainerInspectFormat &&
			request.Args[4] == r.builder.container.ID:
			return r.builderResult(jsonResult(r.builder.container), false)
		case request.Args[0] == "network" && request.Args[3] == generatedDomainColdBuilderNetworkInspectFormat &&
			request.Args[4] == r.builder.network.ID:
			return r.builderResult(jsonResult(r.builder.network), false)
		case request.Args[0] == "image" && request.Args[3] == generatedDomainColdBuilderImageInspectFormat &&
			request.Args[4] == r.builder.container.Image:
			return r.builderResult(jsonResult(r.builder.image), true)
		}
	}
	return r.delegate.Run(ctx, request)
}

func (r *generatedDomainCandidateRosterRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	kind := candidateOwnerKind(request)
	if len(request.Args) != 5 ||
		(kind != candidateOwnerImageInspect && kind != candidateOwnerNetworkInspect && kind != candidateOwnerInspect) {
		return runtimeprocess.CommandResult{}, errors.New("unexpected two-component candidate command")
	}
	if kind == r.beforeKind && r.before != nil {
		before := r.before
		r.before = nil
		before()
	}
	if kind == r.failKind {
		return runtimeprocess.CommandResult{}, r.failErr
	}
	target := request.Args[4]
	for _, physical := range r.physical {
		matches := kind == candidateOwnerImageInspect && target == physical.policy.identity.ImageContentID ||
			kind == candidateOwnerNetworkInspect && target == physical.policy.identity.NetworkName ||
			kind == candidateOwnerInspect &&
				(target == physical.containerID || target == physical.policy.identity.ContainerName)
		if !matches {
			continue
		}
		candidate := &candidateOwnerRunner{physical: physical}
		result, err := candidate.defaultResult(kind, request)
		if err == nil && kind == r.afterKind && r.after != nil {
			r.afterSeen++
			if r.afterSeen == r.afterAt {
				after := r.after
				r.after = nil
				after()
			}
		}
		return result, err
	}
	return runtimeprocess.CommandResult{}, errors.New("two-component candidate object is absent")
}

func TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRoster(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBaseline)
}

func TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRosterPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBaselinePart2)
}

func TestGeneratedDomainColdAcceptsDockerMergedCandidateContainerLabels(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func(candidatePolicy, map[string]string) bool
		pass   bool
	}{
		{name: "docker_merged_authenticated_definition", pass: true},
		{name: "missing_authenticated_definition", mutate: func(policy candidatePolicy, labels map[string]string) bool {
			if policy.identity.Component == "frontend" {
				delete(labels, "io.rig.definition")
				return true
			}
			return false
		}},
		{name: "crossed_authenticated_definition", mutate: func(policy candidatePolicy, labels map[string]string) bool {
			if policy.identity.Component == "frontend" {
				crossed := strings.Repeat("0", 64)
				if crossed == policy.buildDefinitionDigest {
					crossed = strings.Repeat("1", 64)
				}
				labels["io.rig.definition"] = crossed
				return true
			}
			return false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainTwoComponentColdFixture(t)
				generatedDomainTwoComponentApplyLegacyDockerLabelMerge(t, &fixture, test.mutate)
				result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
					fixture.construction, fixture.environment, fixture.identity.options)
				if test.pass {
					if err != nil {
						t.Fatalf("Docker-merged authenticated candidate labels remained refused: %v", err)
					}
					if result == nil || result.cold == nil || len(result.cold.groups) != 1 ||
						len(result.cold.groups[0].components) != 2 {
						t.Fatal("Docker-merged authenticated candidate labels did not reconstruct the exact group")
					}
				} else {
					if result != nil || err == nil || !errors.Is(err, errGeneratedDomainConstructionRefused) {
						t.Fatalf("invalid Docker-merged authenticated candidate labels = %#v, %v", result, err)
					}
					assertGeneratedDomainConstructionDiagnostic(t, err,
						generatedDomainConstructionStageColdConstruction,
						generatedDomainConstructionReasonColdGroupLabels,
						"generated domain construction was refused: stage=cold_construction reason=cold_group_labels")
				}
				commands := fixture.runner.snapshot()
				for _, command := range commands {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatal("Docker-merged candidate labels dispatched an effect or capacity probe")
					}
				}
				if !test.pass {
					generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
				}
			})
		})
	}
}

func generatedDomainTwoComponentApplyLegacyDockerLabelMerge(t *testing.T,
	fixture *generatedDomainTwoComponentColdFixture, mutate func(candidatePolicy, map[string]string) bool,
) {
	t.Helper()
	roster, ok := fixture.runner.roster.(*generatedDomainCandidateRosterRunner)
	if !ok {
		t.Fatal("two-component candidate roster is unavailable")
	}
	mutations := 0
	for _, physical := range roster.physical {
		args, err := physical.policy.createArgs("")
		if err != nil {
			t.Fatal("create authenticated candidate arguments")
		}
		explicit := candidatePolicyLabels(args)
		if explicit["io.rig.definition"] != physical.policy.buildDefinitionDigest {
			t.Fatal("candidate create arguments omitted the authenticated definition")
		}
		delete(explicit, "io.rig.definition")
		if _, exists := explicit["io.rig.definition"]; exists {
			t.Fatal("legacy candidate labels retained an explicit definition")
		}

		var imageLabels map[string]string
		containerIndex := -1
		for index := range fixture.runner.delegate.objects {
			object := &fixture.runner.delegate.objects[index]
			switch {
			case object.kind == "image" && object.id == physical.policy.identity.ImageContentID:
				imageLabels = cloneStringMap(object.labels)
			case object.kind == "container" && object.id == physical.containerID:
				containerIndex = index
			}
		}
		if imageLabels["io.rig.definition"] != physical.policy.buildDefinitionDigest || containerIndex < 0 {
			t.Fatal("authenticated image and container labels are incomplete")
		}
		for key, value := range imageLabels {
			if _, exists := explicit[key]; !exists {
				explicit[key] = value
			}
		}
		if explicit["io.rig.definition"] != physical.policy.buildDefinitionDigest {
			t.Fatal("Docker image label merge omitted the authenticated definition")
		}
		if mutate != nil && mutate(physical.policy, explicit) {
			mutations++
		}
		fixture.runner.delegate.objects[containerIndex].labels = explicit
	}
	if mutate != nil && mutations != 1 {
		t.Fatal("Docker label mismatch control did not alter exactly one candidate container")
	}
}

func TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDrift(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdGroupAttestation)
}

func TestGeneratedDomainColdRejectsTwoComponentGroupAttestationDriftPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdGroupAttestationPart2)
}

func TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDrift(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderNamespace)
}

func TestGeneratedDomainColdRejectsPersistedBuilderNamespaceDriftPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderNamespacePart2)
}

func TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDrift(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderIdentityAndRead)
}

func TestGeneratedDomainColdRejectsPersistedBuilderIdentityAndReadDriftPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderIdentityAndReadPart2)
}

func TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDrift(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderPhysicalPolicy)
}

func TestGeneratedDomainColdRejectsPersistedBuilderPhysicalPolicyDriftPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderPhysicalPolicyPart2)
}

func TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptions(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderRequiredNetworkOptions)
}

func TestGeneratedDomainColdRejectsPersistedBuilderRequiredNetworkOptionsPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderRequiredNetworkOptionsPart2)
}

func TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondence(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderNetworkOptionCorrespondence)
}

func TestGeneratedDomainColdRejectsPersistedBuilderNetworkOptionCorrespondencePart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderNetworkOptionCorrespondencePart2)
}

func TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDrift(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderFinalIdentity)
}

func TestGeneratedDomainColdRejectsPersistedBuilderFinalIdentityDriftPart2(t *testing.T) {
	testGeneratedDomainTwoComponentColdPartition(t, generatedDomainTwoComponentColdBuilderFinalIdentityPart2)
}

func TestGeneratedDomainColdRejectsTwoComponentCensusDetails(t *testing.T) {
	testGeneratedDomainTwoComponentCensusDetails(t, 0, 3)
}

func TestGeneratedDomainColdRejectsTwoComponentCensusDetailsPart2(t *testing.T) {
	testGeneratedDomainTwoComponentCensusDetails(t, 3, 5)
}

func testGeneratedDomainTwoComponentCensusDetails(t *testing.T, start, end int) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	cases := []struct {
		name   string
		mutate func(*generatedDomainTwoComponentColdFixture)
		reason generatedDomainConstructionReason
	}{
		{name: "exact_rig_labels", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "container" && object.labels["io.rig.component"] == "frontend" {
					object.labels["io.rig.unexpected"] = "crossed"
					return
				}
			}
			t.Fatal("frontend container is absent from the cold census")
		}, reason: generatedDomainConstructionReasonColdGroupLabels},
		{name: "application_network_shape", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "network" && object.labels["io.rig.managed"] == "generated-runtime-network" {
					object.data.driver = "overlay"
					return
				}
			}
			t.Fatal("application network is absent from the cold census")
		}, reason: generatedDomainConstructionReasonColdGroupNetworkShape},
		{name: "application_network_options", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "network" && object.labels["io.rig.managed"] == "generated-runtime-network" {
					object.data.options["com.docker.network.bridge.enable_icc"] = "false"
					return
				}
			}
			t.Fatal("application network is absent from the cold census")
		}, reason: generatedDomainConstructionReasonColdGroupNetworkOptions},
		{name: "missing_frontend_image", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			objects := make([]generatedDomainDockerObject, 0, len(fixture.runner.delegate.objects)-1)
			found := false
			for _, object := range fixture.runner.delegate.objects {
				if !found && object.kind == "image" && object.labels["io.rig.component"] == "frontend" {
					found = true
					continue
				}
				objects = append(objects, object)
			}
			if !found {
				t.Fatal("frontend image is absent from the cold census")
			}
			fixture.runner.delegate.objects = objects
		}, reason: generatedDomainConstructionReasonColdGroupMissingImage},
		{name: "missing_application_network", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			objects := make([]generatedDomainDockerObject, 0, len(fixture.runner.delegate.objects)-1)
			found := false
			for _, object := range fixture.runner.delegate.objects {
				if !found && object.kind == "network" &&
					object.labels["io.rig.managed"] == "generated-runtime-network" {
					found = true
					continue
				}
				objects = append(objects, object)
			}
			if !found {
				t.Fatal("application network is absent from the cold census")
			}
			fixture.runner.delegate.objects = objects
		}, reason: generatedDomainConstructionReasonColdGroupMissingNetwork},
	}
	for _, test := range cases[start:end] {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainTwoComponentColdFixture(t)
				test.mutate(&fixture)
				result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
					fixture.construction, fixture.environment, fixture.identity.options)
				if result != nil || err == nil || !errors.Is(err, errGeneratedDomainConstructionRefused) {
					t.Fatalf("invalid two-component cold census = %#v, %v", result, err)
				}
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageColdConstruction, test.reason,
					"generated domain construction was refused: stage=cold_construction reason="+string(test.reason))
				commands := fixture.runner.snapshot()
				for _, command := range commands {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("cold census refusal dispatched an effect/capacity probe: %#v", command)
					}
				}
				generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
			})
		})
	}
}

func generatedDomainTwoComponentColdCases(t *testing.T) []generatedDomainTwoComponentColdCase {
	t.Helper()
	return []generatedDomainTwoComponentColdCase{
		{name: "exact_api_and_frontend_without_builder", pass: true},
		{name: "exact_api_frontend_and_persisted_builder", builder: true, pass: true},
		{name: "writer_accepted_persisted_builder_network_options", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			const option = "com.docker.network.bridge.enable_ip_masquerade"
			fixture.builder.physical.network.Options[option] = "true"
			if !generatedbuilder.MatchesNetwork(fixture.builder.physical.network, fixture.builder.identity) {
				t.Fatal("generated builder writer rejected its compatible network options")
			}
			generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).data.options[option] = "true"
		}, pass: true},
		{name: "missing_frontend", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.runner.delegate.objects = removeGeneratedDomainTwoComponentContainer(
				fixture.runner.delegate.objects, "frontend")
		}, reason: generatedDomainConstructionReasonColdGroupMissingContainer},
		{name: "crossed_frontend", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "container" && object.labels["io.rig.component"] == "frontend" {
					object.id = strings.Repeat("9", 64)
					return
				}
			}
			t.Fatal("frontend container is absent from the cold census")
		}, reason: generatedDomainConstructionReasonColdGroupCorrespondence},
		{name: "cold_group_image_attestation_failure", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			roster := fixture.runner.roster.(*generatedDomainCandidateRosterRunner)
			roster.failKind = candidateOwnerImageInspect
			roster.failErr = generatedDomainTwoComponentGroupPrivateReadError
		}, reason: generatedDomainConstructionReasonColdGroupImageAttestation,
			withoutRefusalSentinel: true, privateCause: true},
		{name: "cold_group_container_attestation_failure", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			roster := fixture.runner.roster.(*generatedDomainCandidateRosterRunner)
			roster.failKind = candidateOwnerInspect
			roster.failErr = generatedDomainTwoComponentGroupPrivateReadError
		}, reason: generatedDomainConstructionReasonColdGroupContainerAttestation, privateCause: true},
		{name: "cold_group_network_attestation_failure", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			roster := fixture.runner.roster.(*generatedDomainCandidateRosterRunner)
			roster.failKind = candidateOwnerNetworkInspect
			roster.failErr = generatedDomainTwoComponentGroupPrivateReadError
		}, reason: generatedDomainConstructionReasonColdGroupNetworkAttestation,
			withoutRefusalSentinel: true, privateCause: true},
		{name: "nested_gateway_reason_remains_authoritative", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			roster := fixture.runner.roster.(*generatedDomainCandidateRosterRunner)
			roster.beforeKind = candidateOwnerNetworkInspect
			roster.before = func() {
				fixture.runner.delegate.gateway.live = []byte("changed-after-startup-observation")
			}
		}, stage: generatedDomainConstructionStageStartupObservation,
			reason: generatedDomainConstructionReasonDockerLiveConfig},
		{name: "orphan_persisted_builder_label", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
				kind: "volume", name: "ordinary-volume", labels: map[string]string{"rig.builder": "orphan"}})
		}, reason: generatedDomainConstructionReasonColdBuilderCensus},
		{name: "missing_persisted_builder_network", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.runner.delegate.objects = removeGeneratedDomainTwoComponentObject(
				fixture.runner.delegate.objects, "network", "rig-buildnet-"+generatedDomainTwoComponentBuilderSuffix)
		}, reason: generatedDomainConstructionReasonColdBuilderCensus},
		{name: "crossed_persisted_builder_identity", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "container" && object.name == "rig-buildkitd-"+generatedDomainTwoComponentBuilderSuffix {
					object.labels["rig.builder"] = "rig-buildkit-bbbbbbbbbbbbbbbbbbbbbbbb"
					return
				}
			}
			t.Fatal("persisted builder container is absent from the cold census")
		}, reason: generatedDomainConstructionReasonColdBuilderContainerPolicy},
		{name: "persisted_builder_identity_without_resources", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.runner.delegate.objects = removeGeneratedDomainTwoComponentObject(
				fixture.runner.delegate.objects, "container", generatedbuilder.ContainerName(fixture.builder.identity))
			fixture.runner.delegate.objects = removeGeneratedDomainTwoComponentObject(
				fixture.runner.delegate.objects, "network", fixture.builder.identity.NetworkName)
		}, reason: generatedDomainConstructionReasonColdBuilderCensus},
		{name: "persisted_builder_resources_without_identity", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			if err := os.Remove(filepath.Join(fixture.builder.directory.Root(), generatedbuilder.IdentityFilename)); err != nil {
				t.Fatal(err)
			}
		}, reason: generatedDomainConstructionReasonColdBuilderCensus},
		{name: "malformed_persisted_builder_identity", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			replaceGeneratedDomainTwoComponentBuilderIdentity(t, fixture.builder,
				[]byte(`{"schema":2,"builderName":"rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa",`+
					`"nodeName":"rig-node-aaaaaaaaaaaaaaaaaaaaaaaa",`+
					`"networkName":"rig-buildnet-aaaaaaaaaaaaaaaaaaaaaaaa","unknown":true}`))
		}, reason: generatedDomainConstructionReasonColdBuilderIdentity},
		{name: "extra_persisted_builder_resource", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
				kind: "container", id: strings.Repeat("f", 64), name: "rig-buildkitd-bbbbbbbbbbbbbbbbbbbbbbbb",
				labels: map[string]string{"rig.controller": "generated-builder"}})
		}, reason: generatedDomainConstructionReasonColdBuilderCensus},
		{name: "persisted_builder_container_read_failure", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.failBuilderReadAt = 1
		}, reason: generatedDomainConstructionReasonColdBuilderContainerRead},
		{name: "persisted_builder_network_read_failure", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.failBuilderReadAt = 2
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkRead},
		{name: "persisted_builder_image_read_failure", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.failBuilderReadAt = 3
		}, reason: generatedDomainConstructionReasonColdBuilderImageRead},
		{name: "invalid_persisted_builder_container_configuration", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.container.HostConfig.Memory++
		}, reason: generatedDomainConstructionReasonColdBuilderContainerPolicy},
		{name: "stopped_persisted_builder_container", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.container.State.Running = false
		}, reason: generatedDomainConstructionReasonColdBuilderContainerPolicy},
		{name: "crossed_persisted_builder_image", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.image.ID = "sha256:" + strings.Repeat("f", 64)
		}, reason: generatedDomainConstructionReasonColdBuilderImagePolicy},
		{name: "extra_persisted_builder_network_member", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.network.Containers[strings.Repeat("f", 64)] = generatedbuilder.NetworkContainer{
				Name: "foreign", IPv4Address: "172.30.0.3/16"}
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "missing_persisted_builder_census_required_network_option", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			const option = "com.docker.network.bridge.enable_icc"
			delete(generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).data.options, option)
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "true_persisted_builder_census_required_network_option", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			const option = "com.docker.network.bridge.enable_icc"
			generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).data.options[option] = "true"
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "other_persisted_builder_census_required_network_option", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			const option = "com.docker.network.bridge.enable_icc"
			generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).data.options[option] = "unexpected"
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "missing_persisted_builder_detailed_required_network_option", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			delete(fixture.builder.physical.network.Options, "com.docker.network.bridge.enable_icc")
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "true_persisted_builder_detailed_required_network_option", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.network.Options["com.docker.network.bridge.enable_icc"] = "true"
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "other_persisted_builder_detailed_required_network_option", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.network.Options["com.docker.network.bridge.enable_icc"] = "unexpected"
		}, reason: generatedDomainConstructionReasonColdBuilderNetworkPolicy},
		{name: "crossed_persisted_builder_census_network_options", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).
				data.options["com.docker.network.bridge.enable_ip_masquerade"] = "true"
		}, reason: generatedDomainConstructionReasonColdBuilderCorrespondence},
		{name: "persisted_builder_network_option_added_between_reads", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.afterFirstPhysical = func() {
				fixture.builder.physical.network.Options["com.docker.network.bridge.enable_ip_masquerade"] = "true"
			}
		}, reason: generatedDomainConstructionReasonColdBuilderRepeated},
		{name: "persisted_builder_network_option_removed_between_reads", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			const option = "com.docker.network.bridge.enable_ip_masquerade"
			fixture.builder.physical.network.Options[option] = "true"
			generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).data.options[option] = "true"
			fixture.builder.runner.afterFirstPhysical = func() {
				delete(fixture.builder.physical.network.Options, option)
			}
		}, reason: generatedDomainConstructionReasonColdBuilderRepeated},
		{name: "persisted_builder_network_option_changed_between_reads", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			const option = "com.docker.network.bridge.enable_ip_masquerade"
			fixture.builder.physical.network.Options[option] = "true"
			generatedDomainTwoComponentBuilderCensusNetwork(t, fixture).data.options[option] = "true"
			fixture.builder.runner.afterFirstPhysical = func() {
				fixture.builder.physical.network.Options[option] = "false"
			}
		}, reason: generatedDomainConstructionReasonColdBuilderRepeated},
		{name: "crossed_persisted_builder_container_attachment", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.container.NetworkSettings.Networks[fixture.builder.identity.NetworkName] =
				json.RawMessage(`{"NetworkID":"` + strings.Repeat("f", 64) + `"}`)
		}, reason: generatedDomainConstructionReasonColdBuilderContainerPolicy},
		{name: "labelled_persisted_builder_image", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.physical.image.Labels["rig.controller"] = "generated-builder"
		}, reason: generatedDomainConstructionReasonColdBuilderImagePolicy},
		{name: "persisted_builder_identity_changes_during_accounting", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.afterFirstPhysical = func() {
				changed := fixture.builder.identity
				changed.NetworkName = "rig-buildnet-bbbbbbbbbbbbbbbbbbbbbbbb"
				body, err := generatedbuilder.EncodeIdentity(changed)
				if err != nil {
					t.Fatal(err)
				}
				replaceGeneratedDomainTwoComponentBuilderIdentity(t, fixture.builder, body)
			}
		}, reason: generatedDomainConstructionReasonColdBuilderFinal},
		{name: "persisted_builder_identity_file_replaced_with_same_bytes", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.afterFirstPhysical = func() {
				body, err := generatedbuilder.EncodeIdentity(fixture.builder.identity)
				if err != nil {
					t.Fatal(err)
				}
				replaceGeneratedDomainTwoComponentBuilderIdentity(t, fixture.builder, body)
			}
		}, reason: generatedDomainConstructionReasonColdBuilderFinal},
		{name: "persisted_builder_identity_reencoded_during_accounting", builder: true, mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.builder.runner.afterFirstPhysical = func() {
				body := []byte(`{"schema":2, "builderName":"` + fixture.builder.identity.BuilderName +
					`", "nodeName":"` + fixture.builder.identity.NodeName + `", "networkName":"` +
					fixture.builder.identity.NetworkName + `"}`)
				path := filepath.Join(fixture.builder.directory.Root(), generatedbuilder.IdentityFilename)
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
				clear(body)
			}
		}, reason: generatedDomainConstructionReasonColdBuilderFinal},
	}
}

func generatedDomainTwoComponentColdPartitionBounds(t *testing.T,
	partition generatedDomainTwoComponentColdPartition,
) (int, int) {
	t.Helper()
	switch partition {
	case generatedDomainTwoComponentColdBaseline:
		return 0, 3
	case generatedDomainTwoComponentColdBaselinePart2:
		return 3, 5
	case generatedDomainTwoComponentColdGroupAttestation:
		return 5, 8
	case generatedDomainTwoComponentColdGroupAttestationPart2:
		return 8, 9
	case generatedDomainTwoComponentColdBuilderNamespace:
		return 9, 12
	case generatedDomainTwoComponentColdBuilderNamespacePart2:
		return 12, 13
	case generatedDomainTwoComponentColdBuilderIdentityAndRead:
		return 13, 16
	case generatedDomainTwoComponentColdBuilderIdentityAndReadPart2:
		return 16, 18
	case generatedDomainTwoComponentColdBuilderPhysicalPolicy:
		return 18, 21
	case generatedDomainTwoComponentColdBuilderPhysicalPolicyPart2:
		return 21, 23
	case generatedDomainTwoComponentColdBuilderRequiredNetworkOptions:
		return 23, 26
	case generatedDomainTwoComponentColdBuilderRequiredNetworkOptionsPart2:
		return 26, 28
	case generatedDomainTwoComponentColdBuilderNetworkOptionCorrespondence:
		return 28, 31
	case generatedDomainTwoComponentColdBuilderNetworkOptionCorrespondencePart2:
		return 31, 33
	case generatedDomainTwoComponentColdBuilderFinalIdentity:
		return 33, 36
	case generatedDomainTwoComponentColdBuilderFinalIdentityPart2:
		return 36, 38
	default:
		t.Fatalf("unknown two-component cold partition: %d", partition)
		return 0, 0
	}
}

func testGeneratedDomainTwoComponentColdPartition(t *testing.T,
	partition generatedDomainTwoComponentColdPartition,
) {
	t.Helper()
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	cases := generatedDomainTwoComponentColdCases(t)
	if len(cases) != 38 {
		t.Fatalf("two-component cold case count = %d, want 38", len(cases))
	}
	start, end := generatedDomainTwoComponentColdPartitionBounds(t, partition)
	for _, test := range cases[start:end] {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainTwoComponentColdFixture(t, test.builder)
				if test.mutate != nil {
					test.mutate(&fixture)
				}
				result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
					fixture.construction, fixture.environment, fixture.identity.options)
				if test.pass {
					if err != nil || result == nil || result.cold == nil || len(result.cold.groups) != 1 {
						t.Fatalf("two-component cold construction = %#v, %v", result, err)
					}
					group := result.cold.groups[0]
					if group.appID != fixture.migration.guarded.appID ||
						group.deploymentID != fixture.migration.request.DeploymentID ||
						group.head == nil || group.head.deploymentID != fixture.migration.request.DeploymentID ||
						len(group.components) != 2 {
						t.Fatalf("two-component cold manifest group = %#v", group)
					}
					names := make([]string, len(group.components))
					receipts := make(map[string]struct{}, len(group.components))
					for index, component := range group.components {
						if component.charged || component.retained || component.receiptID == "" {
							t.Fatalf("two-component cold manifest component = %#v", component)
						}
						names[index] = component.name
						receipts[component.receiptID] = struct{}{}
					}
					sort.Strings(names)
					if !equalStrings(names, []string{"api", "frontend"}) || len(receipts) != 2 {
						t.Fatalf("two-component cold names=%v receipts=%d", names, len(receipts))
					}
				} else {
					if result != nil || err == nil ||
						(!test.withoutRefusalSentinel && !errors.Is(err, errGeneratedDomainConstructionRefused)) ||
						(test.withoutRefusalSentinel && errors.Is(err, errGeneratedDomainConstructionRefused)) {
						t.Fatalf("invalid two-component cold roster = %#v, %v", result, err)
					}
					reason := test.reason
					if reason == "" {
						reason = generatedDomainConstructionReasonColdBuilderAttestation
					}
					stage := test.stage
					if stage == "" {
						stage = generatedDomainConstructionStageColdConstruction
					}
					var diagnostic *generatedDomainConstructionDiagnostic
					if !errors.As(err, &diagnostic) || diagnostic == nil ||
						diagnostic.stage != stage || diagnostic.reason != reason {
						t.Fatalf("invalid two-component cold reason = %#v, want %q", diagnostic, reason)
					}
					if generatedDomainDiagnosticErrorTreeContains(err,
						generatedDomainTwoComponentBuilderPrivateReadError) {
						t.Fatalf("invalid two-component cold reason exposed private read error: %v", err)
					}
					if strings.Contains(err.Error(), generatedDomainTwoComponentGroupPrivateReadError.Error()) {
						t.Fatalf("invalid two-component cold reason rendered private group error: %v", err)
					}
					if test.privateCause && !errors.Is(err, generatedDomainTwoComponentGroupPrivateReadError) {
						t.Fatalf("invalid two-component cold reason hid its previously visible cause: %v", err)
					}
				}
				for _, command := range fixture.runner.snapshot() {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("two-component cold reconstruction dispatched an effect/capacity probe: %#v", command)
					}
				}
			})
		})
	}

	if partition == generatedDomainTwoComponentColdGroupAttestationPart2 {
		t.Run("cold_group_gateway_attestation_failure", func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, observed, groups := generatedDomainTwoComponentColdAttestationFixture(t)
				roster := fixture.runner.roster.(*generatedDomainCandidateRosterRunner)
				for _, group := range groups {
					for _, component := range group.snapshot.components {
						if component.state == generatedruntimestate.ComponentRunning ||
							component.state == generatedruntimestate.ComponentHealthy ||
							component.state == generatedruntimestate.ComponentActive ||
							component.state == generatedruntimestate.ComponentDraining {
							roster.afterAt++
						}
					}
				}
				roster.afterKind = candidateOwnerNetworkInspect
				roster.after = func() {
					result.candidates.process = &generatedDomainCandidateRosterRunner{}
				}
				err := attestGeneratedDomainColdGroups(context.Background(), result.manager, result.candidates, observed, groups)
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupGatewayAttestation,
					"generated domain construction was refused: stage=cold_construction reason=cold_group_gateway_attestation")
				if !errors.Is(err, errGeneratedDomainConstructionRefused) {
					t.Fatalf("cold group gateway attestation hid refusal sentinel: %v", err)
				}
				for _, command := range fixture.runner.snapshot() {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("cold group gateway refusal dispatched effect/capacity probe: %#v", command)
					}
				}
			})
		})
	}

	if partition == generatedDomainTwoComponentColdBuilderNamespacePart2 {
		t.Run("cold_group_structural_refusal_retains_specific_reason", func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, observed, groups := generatedDomainTwoComponentColdAttestationFixture(t)
				delete(groups[0].policies, "api")
				err := attestGeneratedDomainColdGroups(context.Background(), result.manager, result.candidates, observed, groups)
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupStructure,
					"generated domain construction was refused: stage=cold_construction reason=cold_group_structure")
				if !errors.Is(err, errGeneratedDomainConstructionRefused) {
					t.Fatalf("cold group structural refusal hid construction sentinel: %v", err)
				}
				for _, command := range fixture.runner.snapshot() {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("cold group structural refusal dispatched effect/capacity probe: %#v", command)
					}
				}
			})
		})
	}
}

func generatedDomainTwoComponentColdAttestationFixture(t *testing.T) (generatedDomainTwoComponentColdFixture,
	*generatedDomainConstruction, generatedDomainStartupObservation, []generatedDomainColdGroup,
) {
	t.Helper()
	fixture := newGeneratedDomainTwoComponentColdFixture(t)
	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.construction, fixture.environment, fixture.identity.options)
	if err != nil || result == nil || result.manager == nil || result.candidates == nil {
		t.Fatalf("valid cold attestation fixture = %#v, %v", result, err)
	}
	observed, err := inspectGeneratedDomainStartupObservation(context.Background(), fixture.identity.stores,
		fixture.construction, fixture.identity.options)
	if err != nil {
		t.Fatalf("inspect valid cold attestation fixture: %v", err)
	}
	groups, err := readGeneratedDomainColdGroups(context.Background(), result.candidates, observed)
	if err != nil || len(groups) != 1 {
		t.Fatalf("read valid cold attestation groups = %d, %v", len(groups), err)
	}
	return fixture, result, observed, groups
}

func TestGeneratedDomainColdGroupStructuralAttribution(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func([]generatedDomainColdGroup)
	}{
		{name: "missing_component_policy", mutate: func(groups []generatedDomainColdGroup) {
			delete(groups[0].policies, "api")
		}},
		{name: "starting_component", mutate: func(groups []generatedDomainColdGroup) {
			groups[0].snapshot.components[0].state = generatedruntimestate.ComponentStarting
		}},
		{name: "invalid_present_container_identity", mutate: func(groups []generatedDomainColdGroup) {
			groups[0].snapshot.components[0].containerID = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, observed, groups := generatedDomainTwoComponentColdAttestationFixture(t)
				test.mutate(groups)
				before := len(fixture.runner.snapshot())
				var retained generatedDomainColdRetainedGatewayProof
				err := attestGeneratedDomainColdGroupsWithRetained(context.Background(), result.manager,
					result.candidates, observed, groups, &retained)
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReason("cold_group_structure"),
					"generated domain construction was refused: stage=cold_construction reason=cold_group_structure")
				if !reflect.DeepEqual(retained, generatedDomainColdRetainedGatewayProof{}) {
					t.Fatalf("cold group structural refusal populated retained proof: %#v", retained)
				}
				commands := fixture.runner.snapshot()[before:]
				for _, command := range commands {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("cold group structural refusal dispatched effect/capacity probe: %#v", command)
					}
				}
				generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
			})
		})
	}
}

func TestGeneratedDomainColdGroupGatewaySelectionAttribution(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture, result, observed, expected := generatedDomainRetainedV1ColdAttestationFixture(t)
		groups, err := readGeneratedDomainColdGroups(context.Background(), result.candidates, observed)
		if err != nil || len(groups) == 0 {
			t.Fatalf("read repeated-selection cold groups = %d, %v", len(groups), err)
		}
		lineage, err := gatewayUpgradeCurrentLineage(*expected.upgrade)
		if err != nil {
			t.Fatal(err)
		}
		selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: lineage,
			Upgrade: expected.upgrade, UpgradeSource: expected.nativeV1}
		authority := gatewayCurrentV2PromotionAuthority{Snapshot: observed.database.Gateway}
		if !gatewayCurrentV2PromotionSnapshotMatches(selection, authority.Snapshot) {
			t.Fatal("repeated-selection promotion authority does not match the exact startup snapshot")
		}
		current, err := gatewayCurrentV2BaselineFor(selection, authority)
		if err != nil {
			t.Fatal(err)
		}
		observed.protected.currentV2 = gatewayCurrentV2Census{files: map[string]gatewayCurrentV2CensusEntry{
			"first": {State: current}, "second": {State: current},
		}}
		if _, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected); err == nil {
			t.Fatal("duplicate current V2 projections did not make repeated selection ambiguous")
		}
		before := len(fixture.runner.snapshot())
		var retained generatedDomainColdRetainedGatewayProof
		err = attestGeneratedDomainColdGroupsWithRetained(context.Background(), result.manager,
			result.candidates, observed, groups, &retained)
		assertGeneratedDomainConstructionDiagnostic(t, err,
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReason("cold_group_gateway_selection"),
			"generated domain construction was refused: stage=cold_construction reason=cold_group_gateway_selection")
		commands := fixture.runner.snapshot()[before:]
		if len(commands) == 0 {
			t.Fatal("repeated gateway selection refusal did not traverse the required cold group attestations")
		}
		for _, command := range commands {
			if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
				t.Fatalf("repeated gateway selection refusal dispatched effect/capacity probe: %#v", command)
			}
			if generatedDomainRetainedV1DetailedReadCommand(command, fixture.observation) ||
				len(command) == 5 && command[0] == "image" && command[1] == "inspect" &&
					command[3] == imageInspectFormat &&
					normalizeID(command[4]) == normalizeID(fixture.observation.Image.ID) {
				t.Fatalf("repeated gateway selection refusal performed a retained gateway physical read: %#v", command)
			}
			builderInspection := len(command) == 5 && command[1] == "inspect" && command[2] == "--format" &&
				(command[0] == "container" && command[3] == generatedDomainColdBuilderContainerInspectFormat ||
					command[0] == "network" && command[3] == generatedDomainColdBuilderNetworkInspectFormat ||
					command[0] == "image" && command[3] == generatedDomainColdBuilderImageInspectFormat)
			if !generatedDomainCandidateInspectionCommand(command) && !builderInspection {
				t.Fatalf("repeated gateway selection refusal dispatched a read outside the builder/component prefix: %#v", command)
			}
		}
		generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
		if !reflect.DeepEqual(retained, generatedDomainColdRetainedGatewayProof{}) {
			t.Fatalf("repeated gateway selection populated retained proof: %#v", retained)
		}
	})
}

func generatedDomainTwoComponentBuilderCensusNetwork(t *testing.T,
	fixture *generatedDomainTwoComponentColdFixture,
) *generatedDomainDockerObject {
	t.Helper()
	for index := range fixture.runner.delegate.objects {
		object := &fixture.runner.delegate.objects[index]
		if object.kind == "network" && object.name == fixture.builder.identity.NetworkName {
			return object
		}
	}
	t.Fatal("persisted builder network is absent from the cold census")
	return nil
}

func newGeneratedDomainTwoComponentColdFixture(t *testing.T, builderOptions ...bool) generatedDomainTwoComponentColdFixture {
	t.Helper()
	withBuilder := len(builderOptions) == 1 && builderOptions[0]
	if len(builderOptions) > 1 {
		t.Fatal("two-component cold fixture accepts at most one builder option")
	}
	identity := newGeneratedDomainIdentityFixture(t)
	plan := generatedDomainTwoComponentPlan()
	migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity,
		[]string{"api", "frontend"}, []string{"api", "frontend"}, plan)
	identity.stores = migration.repository.stores
	validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
	options := generatedDomainCandidateOptions(identity.options)
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: 2 * uint64(options.MemoryBytes),
		DiskAvailableBytes:   2 * options.ReplacementDiskBytes,
	}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	base := &candidateOwnerRunner{absenceErr: generatedMigrationCompletedExitOne(t),
		before:  make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results: make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
	candidateRunner := &generatedRouteCandidateRunner{delegate: base, physical: make(map[string]candidateOwnerPhysical)}
	candidateEnvironment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := migration.repository.GeneratedCandidateAuthority(candidateRunner, candidateEnvironment, budget, options)
	if err != nil {
		t.Fatal(err)
	}
	validation.authority = authority
	candidate := &candidateOwnerFixture{validation: validation, authority: authority, runner: base,
		environment: candidateEnvironment, source: source, budget: budget, options: options}
	reservation := candidate.reserve(t)
	t.Cleanup(reservation.Release)
	execution := candidate.pin(t, reservation)
	t.Cleanup(execution.Leave)

	names := []string{"api", "frontend"}
	containerIDs := []string{strings.Repeat("7", 64), strings.Repeat("8", 64)}
	invocations := make([]GeneratedCandidateInvocation, 0, len(names))
	identities := make([]CandidateIdentity, 0, len(names))
	for index, name := range names {
		invocation, err := execution.BeginCreate(name)
		if err != nil {
			t.Fatalf("begin %s create: %v", name, err)
		}
		permit, ok := invocation.(*generatedCandidateInvocationPermit)
		if !ok || permit == nil {
			t.Fatalf("%s invocation type = %T", name, invocation)
		}
		base.mu.Lock()
		base.containerID = containerIDs[index]
		base.physical = candidateOwnerPhysical{policy: permit.policy}
		base.mu.Unlock()
		created, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatalf("create %s candidate: %v", name, err)
		}
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatalf("start %s candidate: %v", name, err)
		}
		invocations = append(invocations, invocation)
		identities = append(identities, created)
	}
	state, err := execution.FinishStarting()
	if err != nil || state.Phase != generatedruntimestate.PhaseWaitingHealth {
		t.Fatalf("finish two-component starting = %#v, %v", state, err)
	}
	for index, invocation := range invocations {
		if err := execution.WaitHealthyAndRecord(invocation); err != nil {
			t.Fatalf("observe %s health: %v", names[index], err)
		}
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	if identities[0].NetworkName == "" || identities[0].NetworkName != identities[1].NetworkName ||
		identities[0].ContainerID == identities[1].ContainerID ||
		identities[0].ReceiptID == "" || identities[0].ReceiptID == identities[1].ReceiptID {
		t.Fatalf("two-component candidate identities = %#v", identities)
	}

	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, _, ingressIP := ingressNetworkCandidate(0)
	ingress := &ingressRunner{appID: migration.guarded.appID, network: identities[0].NetworkName,
		endpoint: generatedRouteEndpoint(identities[0]),
		caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
			GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
		ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
			Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
	physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
		slot: generatedidentity.Slot(identities[0].Slot), endpoints: make(map[string]generatedRouteEndpointSnapshot)}
	for _, candidateIdentity := range identities {
		physicalRoutes.endpoints[candidateIdentity.ContainerID] = generatedRouteEndpointSnapshot{
			appID: migration.guarded.appID, endpoint: generatedRouteEndpoint(candidateIdentity),
			slot: generatedidentity.Slot(candidateIdentity.Slot)}
	}
	routeRunner := &generatedDomainTwoComponentRouteRunner{daemonID: "generated-domain-two-component-cold",
		delegate: physicalRoutes}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database,
		stores: identity.stores, runner: routeRunner, options: identity.options}, store)
	if err != nil {
		t.Fatal(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores,
		routeRunner, identity.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		t.Fatal(err)
	}
	routes, err := newGeneratedRouteAuthority(manager, candidate.authority, candidate.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	endpoints := []generatedroute.RouteEndpoint{generatedRouteEndpoint(identities[0]), generatedRouteEndpoint(identities[1])}
	request := generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
		ToSlot: generatedidentity.Slot(identities[0].Slot), Endpoints: endpoints}
	sealed, err := routes.Pin(migration.guarded.context, execution, request)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := sealed.Activate(migration.guarded.context)
	if err != nil || activated.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activate two-component route = %#v, %v", activated, err)
	}
	observed, err := manager.Observe(context.Background(), migration.guarded.appID)
	if err != nil || !sameRoute(routeRecord{Slot: request.ToSlot, Endpoints: endpoints},
		routeRecord{Slot: observed.Slot, Endpoints: observed.Endpoints}) {
		t.Fatalf("two-component physical route = %#v, %v", observed, err)
	}
	runtimeState := generatedruntimestate.New(identity.database)
	if _, err := runtimeState.Advance(context.Background(), migration.guarded.appID,
		migration.request.DeploymentID, generatedruntimestate.PhaseDraining,
		generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.repository.Transition(context.Background(), migration.guarded.appID,
		migration.request.DeploymentID, Succeeded, ""); err != nil {
		t.Fatal(err)
	}
	execution.Leave()
	reservation.Release()
	if err := migration.releaseEffects(); err != nil {
		t.Fatal(err)
	}

	stateValue, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	_, v2, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, stateValue, v2, journal, gatewayTopologyExactV1Only)
	observation.V1Container.ID = strings.Repeat("d", 64)
	observation.V1NetworkID = strings.Repeat("e", 64)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = observation.V1Container.ID
			objects[index].labels["rig.live"] = "generated-domain-two-component-cold"
		case caddyNetworkName:
			objects[index].id = observation.V1NetworkID
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
		}
	}
	physicalRoster := make([]candidateOwnerPhysical, 0, len(candidateRunner.physical))
	for _, value := range candidateRunner.physical {
		physicalRoster = append(physicalRoster, value)
	}
	sort.Slice(physicalRoster, func(i, j int) bool {
		return physicalRoster[i].policy.identity.Component < physicalRoster[j].policy.identity.Component
	})
	if len(physicalRoster) != 2 ||
		physicalRoster[0].policy.identity.ImageContentID == physicalRoster[1].policy.identity.ImageContentID {
		t.Fatalf("two-component physical roster = %#v", physicalRoster)
	}
	for _, value := range physicalRoster {
		policy := value.policy
		objects = append(objects,
			generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID,
				labels: map[string]string{"io.rig.managed": "generated-image",
					"io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role,
					"io.rig.definition": policy.buildDefinitionDigest},
				data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
			generatedDomainDockerObject{kind: "container", id: value.containerID,
				name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
		)
	}
	objects = append(objects, generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64),
		name: physicalRoster[0].policy.identity.NetworkName,
		labels: map[string]string{"io.rig.managed": "generated-runtime-network",
			"io.rig.application": physicalRoster[0].policy.identity.AppID},
		data: generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}})
	var builder *generatedDomainTwoComponentBuilderFixture
	if withBuilder {
		builderObjects, builderFixture := generatedDomainTwoComponentBuilderObjects(t, identity.dataRoot)
		objects = append(objects, builderObjects...)
		builder = &builderFixture
	}
	identity.options.HostPort = journal.Source.LocalHostPort
	runner := &generatedDomainConstructionRunner{roster: &generatedDomainCandidateRosterRunner{physical: physicalRoster},
		delegate: &generatedDomainRecoveryCensusRunner{daemonID: "generated-domain-two-component-cold",
			objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	var construction runtimeprocess.CommandRunner = runner
	if builder != nil {
		builder.runner = &generatedDomainTwoComponentRouteRunner{delegate: runner, builder: &builder.physical}
		construction = builder.runner
	}
	return generatedDomainTwoComponentColdFixture{identity: identity, migration: migration, runner: runner,
		construction: construction, builder: builder, environment: &candidateFactoryEnvironment{path: t.TempDir()}}
}

func generatedDomainTwoComponentBuilderObjects(t *testing.T, dataRoot string) (
	[]generatedDomainDockerObject, generatedDomainTwoComponentBuilderFixture,
) {
	t.Helper()
	identity := generatedbuilder.Identity{Schema: generatedbuilder.StateSchema,
		BuilderName: "rig-buildkit-" + generatedDomainTwoComponentBuilderSuffix,
		NodeName:    "rig-node-" + generatedDomainTwoComponentBuilderSuffix,
		NetworkName: "rig-buildnet-" + generatedDomainTwoComponentBuilderSuffix}
	body, err := generatedbuilder.EncodeIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := securetemp.NewGeneratedBuilderDirectory(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.WriteNewFile(generatedbuilder.IdentityFilename, body); err != nil {
		t.Fatal(err)
	}
	opened, err := securetemp.OpenGeneratedBuilderDirectory(dataRoot)
	if err != nil || !directory.Same(opened) {
		t.Fatalf("reopen generated builder directory: same=%t err=%v", directory.Same(opened), err)
	}
	containerID := strings.Repeat("4", 64)
	networkID := strings.Repeat("5", 64)
	imageID := "sha256:" + strings.Repeat("3", 64)
	var container generatedbuilder.Container
	container.ID = containerID
	container.Name = "/" + generatedbuilder.ContainerName(identity)
	container.Image = imageID
	container.State.Running = true
	container.Config.Image = generatedbuilder.ImageReference
	container.Config.Cmd = generatedbuilder.Command(generatedbuilder.DefaultStateQuotaBytes)
	container.Config.Labels = generatedbuilder.ContainerLabels(identity, generatedbuilder.DefaultStateQuotaBytes)
	container.HostConfig.Memory = generatedbuilder.MemoryLimit(generatedbuilder.DefaultStateQuotaBytes)
	container.HostConfig.MemorySwap = generatedbuilder.MemoryLimit(generatedbuilder.DefaultStateQuotaBytes)
	container.HostConfig.CPUPeriod = 100000
	container.HostConfig.CPUQuota = 100000
	container.HostConfig.PidsLimit = generatedbuilder.PIDsLimit
	container.HostConfig.NetworkMode = identity.NetworkName
	container.HostConfig.Privileged = true
	container.HostConfig.Binds = []string{}
	container.HostConfig.PortBindings = map[string]json.RawMessage{}
	container.HostConfig.LogConfig.Type = "json-file"
	container.HostConfig.LogConfig.Config = map[string]string{"max-size": "10m", "max-file": "1"}
	container.HostConfig.RestartPolicy.Name = "unless-stopped"
	tmpfs := &struct {
		SizeBytes int64 `json:"SizeBytes"`
		Mode      int64 `json:"Mode"`
	}{SizeBytes: generatedbuilder.DefaultStateQuotaBytes, Mode: 0o700}
	container.HostConfig.Mounts = []generatedbuilder.ConfiguredMount{{
		Type: "tmpfs", Target: generatedbuilder.StatePath, TmpfsOptions: tmpfs}}
	container.NetworkSettings.Networks = map[string]json.RawMessage{
		identity.NetworkName: json.RawMessage(`{"NetworkID":"` + networkID + `"}`)}
	container.Mounts = []generatedbuilder.Mount{{Type: "tmpfs", Destination: generatedbuilder.StatePath, RW: true}}
	network := generatedbuilder.Network{ID: networkID, Name: identity.NetworkName,
		Driver: "bridge", Scope: "local", Labels: generatedbuilder.NetworkLabels(identity),
		Options: map[string]string{"com.docker.network.bridge.enable_icc": "false"},
		Containers: map[string]generatedbuilder.NetworkContainer{containerID: {
			Name: generatedbuilder.ContainerName(identity), IPv4Address: "172.30.0.2/16"}}}
	image := generatedbuilder.Image{ID: imageID, Labels: map[string]string{}}
	objects := []generatedDomainDockerObject{
		{kind: "container", id: containerID, name: generatedbuilder.ContainerName(identity),
			labels: cloneStringMap(container.Config.Labels)},
		{kind: "network", id: networkID, name: identity.NetworkName,
			labels: cloneStringMap(network.Labels),
			data: generatedDomainDockerObjectData{driver: "bridge", scope: "local",
				options: map[string]string{"com.docker.network.bridge.enable_icc": "false"}}},
	}
	return objects, generatedDomainTwoComponentBuilderFixture{directory: directory, identity: identity,
		physical: generatedDomainColdBuilderPhysical{container: container, network: network, image: image}}
}

func replaceGeneratedDomainTwoComponentBuilderIdentity(t *testing.T,
	fixture *generatedDomainTwoComponentBuilderFixture, body []byte,
) {
	t.Helper()
	path := filepath.Join(fixture.directory.Root(), generatedbuilder.IdentityFilename)
	if err := os.Rename(path, path+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	clear(body)
}

func generatedDomainTwoComponentPlan() deploymentplans.Plan {
	components := []deploymentplans.Component{
		{Name: "api", Role: "server", Technology: "node", RootDirectory: "apps/api",
			PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: "apps/api",
			NodeVersion: "24", BuildCommand: "npm run build", RunCommand: "node server.js",
			InternalPort: 3000, HealthProbe: "/health"},
		{Name: "frontend", Role: "static", Technology: "static", RootDirectory: "apps/frontend",
			StaticOutputDirectory: "dist", PackageManager: "npm", InstallBehavior: "npm ci",
			InstallDirectory: "apps/frontend", NodeVersion: "24", BuildCommand: "npm run build",
			RunCommand: projectanalysis.ManagedStaticCommand("dist", 8080), InternalPort: 8080, HealthProbe: "/"},
	}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory",
		"nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe", "technology",
		"staticOutputDirectory"}
	provenance := make([]deploymentplans.FieldProvenance, 0, len(components)*len(fields))
	for _, component := range components {
		for _, field := range fields {
			provenance = append(provenance, deploymentplans.FieldProvenance{
				Field:  "components." + component.Name + "." + field,
				Origin: deploymentplans.ProvenanceUser, Confidence: 100,
				Evidence: []string{"user:deployment-setup"},
			})
		}
	}
	return deploymentplans.Plan{SetupVersion: projectanalysis.SetupVersion,
		Strategy: deploymentplans.StrategyGeneratedNode,
		Detector: deploymentplans.Detector{Name: "manual-setup", Version: "1",
			SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source: deploymentplans.SourceIdentity{Provider: "github", RepositoryID: 7,
			ResolvedDigest: strings.Repeat("c", 64)},
		Components: components, FieldProvenance: provenance,
		Migration: &deploymentplans.Migration{ComponentName: "api", RootDirectory: "apps/api",
			Command: "npm run migrate -- --locked", EnvironmentKeys: []string{"DATABASE_URL"},
			EvidenceDigest: strings.Repeat("b", 64),
			Approval:       deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}},
	}
}

func removeGeneratedDomainTwoComponentContainer(objects []generatedDomainDockerObject,
	component string,
) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, 0, len(objects))
	for _, object := range objects {
		if object.kind == "container" && object.labels["io.rig.component"] == component {
			continue
		}
		result = append(result, object)
	}
	return result
}

func removeGeneratedDomainTwoComponentObject(objects []generatedDomainDockerObject,
	kind, name string,
) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, 0, len(objects))
	for _, object := range objects {
		if object.kind == kind && object.name == name {
			continue
		}
		result = append(result, object)
	}
	return result
}
