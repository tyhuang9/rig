//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainConstructionDiagnosticRunner struct {
	stdout []byte
	err    error
}

type generatedDomainConstructionDiagnosticFaultRunner struct {
	delegate runtimeprocess.CommandRunner
	match    func([]string) bool
	failAt   int
	seen     int
	err      error
}

func (r *generatedDomainConstructionDiagnosticRunner) Run(_ context.Context,
	_ runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.stdout...)}, r.err
}

func (r *generatedDomainConstructionDiagnosticFaultRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if r.match != nil && r.match(request.Args) {
		r.seen++
		if r.seen == r.failAt {
			return runtimeprocess.CommandResult{}, r.err
		}
	}
	return r.delegate.Run(ctx, request)
}

func TestGeneratedDomainConstructionDiagnosticClosedAttribution(t *testing.T) {
	t.Run("closed_codes_render_without_private_causes", func(t *testing.T) {
		privateCause := errors.New("private Docker response and path")
		nested := generatedDomainDockerConstructionDiagnostic(
			generatedDomainConstructionReasonDockerNetwork, privateCause)
		startup := generatedDomainStartupObservationDiagnostic(
			generatedDomainConstructionReasonDockerAttestation, errGeneratedDomainStartupRead, nested)
		assertGeneratedDomainConstructionDiagnostic(t, startup,
			generatedDomainConstructionStageStartupObservation,
			generatedDomainConstructionReasonDockerNetwork,
			"generated domain construction was refused: stage=startup_observation reason=docker_network")
		if !errors.Is(startup, errGeneratedDomainStartupRead) || errors.Is(startup, privateCause) ||
			strings.Contains(startup.Error(), privateCause.Error()) {
			t.Fatalf("startup diagnostic exposed its replaced cause: %v", startup)
		}

		final := generatedDomainConstructionFinalDiagnostic(
			generatedDomainConstructionStageStableFinal,
			generatedDomainConstructionReasonClassification, startup)
		assertGeneratedDomainConstructionDiagnostic(t, final,
			generatedDomainConstructionStageStableFinal,
			generatedDomainConstructionReasonDockerNetwork,
			"generated domain construction was refused: stage=stable_final reason=docker_network")
		if !errors.Is(final, errGeneratedDomainConstructionRefused) ||
			errors.Is(final, errGeneratedDomainStartupRead) || errors.Is(final, privateCause) {
			t.Fatalf("final diagnostic changed its closed sentinel boundary: %v", final)
		}

		invalid := newGeneratedDomainConstructionDiagnostic(
			generatedDomainConstructionStage("untrusted-stage"),
			generatedDomainConstructionReasonDockerNetwork, privateCause)
		if invalid != errGeneratedDomainConstructionRefused ||
			strings.Contains(invalid.Error(), "untrusted-stage") || strings.Contains(invalid.Error(), privateCause.Error()) {
			t.Fatalf("invalid diagnostic code escaped: %v", invalid)
		}

		collapsedCause := errors.New("collapsed private Docker error")
		collapsed := generatedDomainDockerCollapsedDiagnostic(
			generatedDomainConstructionReasonDockerCensus, privateCause, collapsedCause)
		if !errors.Is(collapsed, collapsedCause) || errors.Is(collapsed, privateCause) ||
			generatedDomainDiagnosticErrorTreeContains(collapsed, privateCause.Error()) {
			t.Fatalf("collapsed Docker diagnostic exposed its replaced cause: %v", collapsed)
		}

		cold := generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdAccounting, privateCause)
		assertGeneratedDomainConstructionDiagnostic(t, cold,
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdAccounting,
			"generated domain construction was refused: stage=cold_construction reason=cold_accounting")
		if !errors.Is(cold, privateCause) {
			t.Fatalf("cold boundary hid its previously visible cause: %v", cold)
		}
		for _, reason := range []generatedDomainConstructionReason{
			generatedDomainConstructionReasonColdGroupReconstruction,
			generatedDomainConstructionReasonColdBuilderAttestation,
			generatedDomainConstructionReasonColdBuilderIdentity,
			generatedDomainConstructionReasonColdBuilderCensus,
			generatedDomainConstructionReasonColdBuilderContainerRead,
			generatedDomainConstructionReasonColdBuilderNetworkRead,
			generatedDomainConstructionReasonColdBuilderImageRead,
			generatedDomainConstructionReasonColdBuilderContainerPolicy,
			generatedDomainConstructionReasonColdBuilderNetworkPolicy,
			generatedDomainConstructionReasonColdBuilderImagePolicy,
			generatedDomainConstructionReasonColdBuilderCorrespondence,
			generatedDomainConstructionReasonColdBuilderRepeated,
			generatedDomainConstructionReasonColdBuilderFinal,
			generatedDomainConstructionReasonColdReservation,
			generatedDomainConstructionReasonColdOwnership,
			generatedDomainConstructionReasonColdHead,
			generatedDomainConstructionReasonColdGroupAttestation,
			generatedDomainConstructionReasonColdGroupStructure,
			generatedDomainConstructionReasonColdGroupGatewaySelection,
			generatedDomainConstructionReasonColdGroupImageAttestation,
			generatedDomainConstructionReasonColdGroupContainerAttestation,
			generatedDomainConstructionReasonColdGroupNetworkAttestation,
			generatedDomainConstructionReasonColdGroupCensus,
			generatedDomainConstructionReasonColdGroupCorrespondence,
			generatedDomainConstructionReasonColdGroupLabels,
			generatedDomainConstructionReasonColdGroupNetworkShape,
			generatedDomainConstructionReasonColdGroupNetworkOptions,
			generatedDomainConstructionReasonColdGroupMissingContainer,
			generatedDomainConstructionReasonColdGroupMissingImage,
			generatedDomainConstructionReasonColdGroupMissingNetwork,
			generatedDomainConstructionReasonColdGroupGatewayAttestation,
			generatedDomainConstructionReasonColdRetainedGatewaySelection,
			generatedDomainConstructionReasonColdRetainedGatewayTriple,
			generatedDomainConstructionReasonColdRetainedGatewayRead,
			generatedDomainConstructionReasonColdRetainedGatewayPolicy,
			generatedDomainConstructionReasonColdRetainedGatewayDigest,
			generatedDomainConstructionReasonColdRetainedGatewayFinalIdentity,
			generatedDomainConstructionReasonColdRetainedGatewayFinalLabels,
			generatedDomainConstructionReasonColdRetainedGatewayFinalProjection,
			generatedDomainConstructionReasonColdRetainedGatewayFinal,
			generatedDomainConstructionReasonColdManifest,
		} {
			diagnostic := generatedDomainConstructionBoundaryDiagnostic(
				generatedDomainConstructionStageColdConstruction, reason, privateCause)
			assertGeneratedDomainConstructionDiagnostic(t, diagnostic,
				generatedDomainConstructionStageColdConstruction, reason,
				"generated domain construction was refused: stage=cold_construction reason="+string(reason))
			if !errors.Is(diagnostic, privateCause) {
				t.Fatalf("%s diagnostic hid its previously visible cause: %v", reason, diagnostic)
			}
		}

		secondary := errors.New("secondary visible release error")
		joined := errors.Join(cold, secondary)
		retained := generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageManifest,
			generatedDomainConstructionReasonManifest, joined)
		assertGeneratedDomainConstructionDiagnostic(t, retained,
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdAccounting,
			"generated domain construction was refused: stage=cold_construction reason=cold_accounting")
		if retained == joined || !errors.Is(retained, privateCause) || !errors.Is(retained, secondary) ||
			strings.Contains(retained.Error(), secondary.Error()) {
			t.Fatalf("existing specific diagnostic was replaced: %v", retained)
		}
		if direct := generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageManifest,
			generatedDomainConstructionReasonManifest, cold); direct != cold {
			t.Fatalf("direct specific diagnostic identity changed: %v", direct)
		}
		prefixed := fmt.Errorf("private prefix: %w", cold)
		closed := generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageManifest,
			generatedDomainConstructionReasonManifest, prefixed)
		assertGeneratedDomainConstructionDiagnostic(t, closed,
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdAccounting,
			"generated domain construction was refused: stage=cold_construction reason=cold_accounting")
		if !errors.Is(closed, privateCause) || strings.Contains(closed.Error(), "private prefix") {
			t.Fatalf("wrapped diagnostic rendered its private prefix: %v", closed)
		}
	})

	t.Run("public_and_private_entry_boundaries_are_named", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainStableConstructionFixture(t)
			domain, err := NewGeneratedDomain(context.Background(), nil, fixture.runner, fixture.environment,
				GeneratedDomainOptions{})
			if domain != nil {
				t.Fatalf("invalid public construction published %#v", domain)
			}
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageStartupObservation,
				generatedDomainConstructionReasonInput,
				"generated domain construction was refused: stage=startup_observation reason=input")
			if !errors.Is(err, errGeneratedDomainStartupInvalid) {
				t.Fatalf("public input diagnostic hid startup sentinel: %v", err)
			}

			privateCause := errors.New("private physical inspection cause")
			fault := &generatedDomainConstructionDiagnosticFaultRunner{delegate: fixture.runner,
				failAt: 1, err: privateCause, match: func(args []string) bool {
					return reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"})
				}}
			result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fault, fixture.environment, fixture.identity.options)
			if result != nil {
				t.Fatalf("failed physical inspection published %#v", result)
			}
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageStartupObservation,
				generatedDomainConstructionReasonPhysicalIdentity,
				"generated domain construction was refused: stage=startup_observation reason=physical_identity")
			if errors.Is(err, privateCause) ||
				generatedDomainDiagnosticErrorTreeContains(err, privateCause.Error()) {
				t.Fatalf("physical diagnostic exposed the reader's collapsed cause: %v", err)
			}
		})
	})

	t.Run("sticky_recovery_retains_first_closed_diagnostic", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity, runner, environment := newGeneratedDomainPreparedRecoveryConstructionFixture(t)
			result, firstErr := newGeneratedDomainConstruction(context.Background(), identity.stores,
				runner, environment, identity.options)
			if result != nil {
				t.Fatalf("recovery construction published %#v", result)
			}
			assertGeneratedDomainConstructionDiagnostic(t, firstErr,
				generatedDomainConstructionStageInitialClassification,
				generatedDomainConstructionReasonRecovery,
				"generated domain construction was refused: stage=initial_classification reason=recovery")
			if !errors.Is(firstErr, errGeneratedDomainConstructionRecovery) {
				t.Fatalf("recovery diagnostic hid recovery sentinel: %v", firstErr)
			}
			joined, joinErr := newGeneratedDomainConstruction(context.Background(), identity.stores,
				runner, environment, identity.options)
			if joined != nil || !errors.Is(joinErr, firstErr) || joinErr.Error() != firstErr.Error() {
				t.Fatalf("sticky recovery join = %#v, %v; want %v", joined, joinErr, firstErr)
			}
		})
	})

	t.Run("cold_accounting_refusal_is_named_without_effects", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainTwoComponentColdFixture(t)
			fixture.runner.delegate.objects = removeGeneratedDomainTwoComponentContainer(
				fixture.runner.delegate.objects, "frontend")
			result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fixture.runner, fixture.environment, fixture.identity.options)
			if result != nil {
				t.Fatalf("incomplete cold roster published %#v", result)
			}
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdGroupMissingContainer,
				"generated domain construction was refused: stage=cold_construction reason=cold_group_missing_container")
			if !errors.Is(err, errGeneratedDomainConstructionRefused) {
				t.Fatalf("cold accounting diagnostic hid refusal sentinel: %v", err)
			}
			for _, command := range fixture.runner.snapshot() {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
					t.Fatalf("cold accounting refusal dispatched effect/capacity probe: %#v", command)
				}
			}
		})
	})

	t.Run("gateway_reader_failures_have_finite_reasons", func(t *testing.T) {
		cases := []struct {
			name   string
			reason generatedDomainConstructionReason
			read   func(*generatedDomainStartupReader, time.Time) error
		}{
			{name: "container", reason: generatedDomainConstructionReasonDockerContainer,
				read: func(reader *generatedDomainStartupReader, deadline time.Time) error {
					_, _, err := reader.readDetailedContainer(context.Background(), deadline, strings.Repeat("a", 64))
					return err
				}},
			{name: "volume", reason: generatedDomainConstructionReasonDockerVolume,
				read: func(reader *generatedDomainStartupReader, deadline time.Time) error {
					_, _, err := reader.readDetailedVolume(context.Background(), deadline, "rig-volume")
					return err
				}},
			{name: "network", reason: generatedDomainConstructionReasonDockerNetwork,
				read: func(reader *generatedDomainStartupReader, deadline time.Time) error {
					_, _, err := reader.readDetailedNetwork(context.Background(), deadline, strings.Repeat("b", 64))
					return err
				}},
			{name: "restart_config", reason: generatedDomainConstructionReasonDockerRestartConfig,
				read: func(reader *generatedDomainStartupReader, deadline time.Time) error {
					_, err := reader.readRestartConfig(context.Background(), deadline, strings.Repeat("c", 64), "autosave.json")
					return err
				}},
			{name: "live_config", reason: generatedDomainConstructionReasonDockerLiveConfig,
				read: func(reader *generatedDomainStartupReader, deadline time.Time) error {
					_, err := reader.readLiveConfig(context.Background(), deadline, strings.Repeat("d", 64))
					return err
				}},
			{name: "static_response", reason: generatedDomainConstructionReasonDockerStaticResponse,
				read: func(reader *generatedDomainStartupReader, deadline time.Time) error {
					return reader.proveGatewayStaticResponse(context.Background(), deadline, strings.Repeat("e", 64),
						"127.0.0.1", 8080, "example.test", strings.Repeat("f", 64), "expected")
				}},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				runner := &generatedDomainConstructionDiagnosticRunner{stdout: []byte("invalid")}
				reader := &generatedDomainStartupReader{runner: runner, options: generatedDomainOptions{
					DockerExecutable: "docker", WorkingDirectory: t.TempDir(),
					CommandTimeout: time.Minute, OutputLimit: 1 << 20,
				}}
				err := test.read(reader, time.Now().Add(time.Minute))
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageStartupObservation, test.reason,
					"generated domain construction was refused: stage=startup_observation reason="+string(test.reason))
			})
		}
	})

	t.Run("collapsed_Docker_boundaries_hide_source_errors", func(t *testing.T) {
		tainted := errors.New("tainted Docker path and arguments")
		newEmptyReader := func(runner runtimeprocess.CommandRunner) *generatedDomainStartupReader {
			root := t.TempDir()
			return &generatedDomainStartupReader{runner: runner, daemonID: "diagnostic-daemon", options: generatedDomainOptions{
				DockerExecutable: filepath.Join(root, "docker"), WorkingDirectory: root,
				CommandTimeout: time.Minute, OutputLimit: 1 << 20,
			}, environment: []string{"DOCKER_CONFIG=" + filepath.Join(root, "config")}}
		}
		assertClosed := func(t *testing.T, err error, reason generatedDomainConstructionReason,
			forbidden ...string,
		) {
			t.Helper()
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageStartupObservation, reason,
				"generated domain construction was refused: stage=startup_observation reason="+string(reason))
			for _, value := range forbidden {
				if generatedDomainDiagnosticErrorTreeContains(err, value) {
					t.Fatalf("diagnostic %q retained collapsed error %q", err, value)
				}
			}
		}

		t.Run("daemon", func(t *testing.T) {
			base := &generatedDomainRecoveryCensusRunner{daemonID: "diagnostic-daemon"}
			fault := &generatedDomainConstructionDiagnosticFaultRunner{delegate: base, failAt: 1, err: tainted,
				match: func(args []string) bool {
					return reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"})
				}}
			_, err := readGeneratedDomainDockerStartup(context.Background(), newEmptyReader(fault))
			assertClosed(t, err, generatedDomainConstructionReasonDockerDaemon,
				tainted.Error(), "generated domain Docker daemon inspection failed")
		})

		t.Run("second_census", func(t *testing.T) {
			base := &generatedDomainRecoveryCensusRunner{daemonID: "diagnostic-daemon"}
			fault := &generatedDomainConstructionDiagnosticFaultRunner{delegate: base, failAt: 2, err: tainted,
				match: func(args []string) bool {
					return reflect.DeepEqual(args, []string{"container", "ls", "--all", "--no-trunc", "--format",
						generatedDomainContainerListFormat})
				}}
			_, err := readGeneratedDomainDockerStartup(context.Background(), newEmptyReader(fault))
			assertClosed(t, err, generatedDomainConstructionReasonDockerCensus,
				tainted.Error(), "generated domain Docker read failed")
		})

		t.Run("second_native_V1_attestation", func(t *testing.T) {
			reader := newGeneratedDomainConstructionDiagnosticNativeV1Reader(t)
			base := reader.runner
			fault := &generatedDomainConstructionDiagnosticFaultRunner{delegate: base, failAt: 2, err: tainted,
				match: func(args []string) bool {
					return len(args) == 5 && args[0] == "container" && args[1] == "inspect" &&
						args[2] == "--format" && args[3] == generatedDomainStartupContainerInspectFormat
				}}
			reader.runner = fault
			_, err := readGeneratedDomainDockerStartup(context.Background(), reader)
			assertClosed(t, err, generatedDomainConstructionReasonDockerContainer,
				tainted.Error(), "generated domain Docker read failed")
		})

		t.Run("live_config", func(t *testing.T) {
			runner := &generatedDomainConstructionDiagnosticRunner{err: tainted}
			reader := newEmptyReader(runner)
			_, err := reader.readLiveConfig(context.Background(), time.Now().Add(time.Minute), strings.Repeat("d", 64))
			assertClosed(t, err, generatedDomainConstructionReasonDockerLiveConfig,
				tainted.Error(), "generated domain Docker read failed")
		})

		t.Run("static_response", func(t *testing.T) {
			runner := &generatedDomainConstructionDiagnosticRunner{err: tainted}
			reader := newEmptyReader(runner)
			err := reader.proveGatewayStaticResponse(context.Background(), time.Now().Add(time.Minute),
				strings.Repeat("e", 64), "127.0.0.1", 8080, "example.test", strings.Repeat("f", 64), "expected")
			assertClosed(t, err, generatedDomainConstructionReasonDockerStaticResponse,
				tainted.Error(), "generated domain Docker read failed")
		})
	})

	t.Run("ambiguous_initial_classification_is_named", func(t *testing.T) {
		if !generatedDomainRegistryIsolated(t) {
			return
		}
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainStableConstructionFixture(t)
			fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
				kind: "volume", name: "rig-unexpected-construction-diagnostic",
				labels: map[string]string{"io.rig.unexpected": "construction-diagnostic"},
				data: generatedDomainDockerObjectData{driver: "local", scope: "local",
					options: map[string]string{}, mountpoint: "/var/lib/docker/volumes/rig-unexpected/_data",
					createdAt: "2026-10-23T00:00:00Z"},
			})
			result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fixture.runner, fixture.environment, fixture.identity.options)
			if result != nil {
				t.Fatalf("ambiguous construction published %#v", result)
			}
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageStartupObservation,
				generatedDomainConstructionReasonDockerAttestation,
				"generated domain construction was refused: stage=startup_observation reason=docker_attestation")
			if !errors.Is(err, errGeneratedDomainStartupRead) ||
				errors.Is(err, errGeneratedDomainConstructionRefused) {
				t.Fatalf("ambiguous construction changed its startup-read sentinel boundary: %v", err)
			}
		})
	})
}

func newGeneratedDomainConstructionDiagnosticNativeV1Reader(t *testing.T) *generatedDomainStartupReader {
	t.Helper()
	state, journal, observation := generatedDomainNativeV1AttestationFixture(t)
	digest, err := canonicalDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	expected := generatedDomainGatewayExpectation{kind: generatedDomainGatewayNativeV1,
		stateDigest: digest, nativeV1: &state}
	expected.names = generatedDomainGatewayExpectedNames(expected)
	expected.ids, err = generatedDomainGatewayExpectedIDs(expected)
	if err != nil {
		t.Fatal(err)
	}
	descriptors, _, ok := generatedDomainGatewayDockerDescriptors(expected)
	if !ok {
		t.Fatal("native-v1 Docker descriptors are unavailable")
	}
	objects := generatedDomainDockerObjectsForDescriptors(descriptors)
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = gateway.container.ID
		case caddyNetworkName:
			objects[index].id = gateway.network.ID
		}
	}
	root := t.TempDir()
	return &generatedDomainStartupReader{
		runner: &generatedDomainRecoveryCensusRunner{daemonID: "diagnostic-daemon", objects: objects,
			imageID: gateway.image.ID, gateway: gateway},
		options: generatedDomainOptions{DockerExecutable: filepath.Join(root, "docker"), WorkingDirectory: root,
			HostPort: journal.Source.LocalHostPort, CommandTimeout: time.Minute, OutputLimit: 1 << 20},
		environment: []string{"DOCKER_CONFIG=" + filepath.Join(root, "config")}, daemonID: "diagnostic-daemon", expectation: expected,
	}
}

func generatedDomainDiagnosticErrorTreeContains(err error, value string) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), value) {
		return true
	}
	if multiple, ok := err.(interface{ Unwrap() []error }); ok {
		for _, item := range multiple.Unwrap() {
			if generatedDomainDiagnosticErrorTreeContains(item, value) {
				return true
			}
		}
		return false
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		return generatedDomainDiagnosticErrorTreeContains(single.Unwrap(), value)
	}
	return false
}

func assertGeneratedDomainConstructionDiagnostic(t *testing.T, err error,
	stage generatedDomainConstructionStage, reason generatedDomainConstructionReason, message string,
) {
	t.Helper()
	var diagnostic *generatedDomainConstructionDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic == nil || diagnostic.stage != stage ||
		diagnostic.reason != reason || err.Error() != message {
		t.Fatalf("construction diagnostic = %T %#v %q; want stage=%q reason=%q message=%q",
			err, diagnostic, err, stage, reason, message)
	}
}
