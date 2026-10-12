package generatedingress

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayCurrentTerminalImageMetadataRequiresNamedIDAgreement(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "stable metadata"
		if changed {
			name = "metadata differs by identity lookup"
		}
		t.Run(name, func(t *testing.T) {
			runner, target, physical := newGatewayCurrentMountStabilityRuntime(t)
			runner.container.Labels["org.opencontainers.image.title"] = "Caddy"
			original := gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)
			nameReads, idReads := 0, 0
			physical.manager.runner = &gatewayCurrentTerminalStopHookExecutor{
				gatewayCurrentPhysicalExecutor: runner,
				resultHook: func(args []string, result *runtimeprocess.CommandResult) {
					if gatewayCurrentMountStabilityInspectTarget(args, target.Identity.Rebind.FinalContainer) {
						nameReads++
					}
					if gatewayCurrentMountStabilityInspectTarget(args, target.Resources.FinalContainer.ID) {
						idReads++
						if changed {
							gatewayCurrentMountStabilityMutateResult(t, result, func(value *gatewayContainerInspection) {
								value.Labels["org.opencontainers.image.title"] = "different"
							})
						}
					}
				},
			}
			facts := gatewayCurrentFinalOwnershipFacts(target, physical.manager.options.HostPort)
			container, runtime, present, err := physical.terminalOwnedOwnershipInventory(context.Background(), facts)
			if nameReads != 1 || idReads != 1 || len(runner.effects) != 0 {
				t.Fatalf("terminal lookup did not perform exact read-only name and ID inspections: %d/%d", nameReads, idReads)
			}
			if changed {
				if err == nil || present || !reflect.DeepEqual(container, caddyInspection{}) ||
					!reflect.DeepEqual(runtime, gatewayContainerRuntime{}) {
					t.Fatal("terminal lookup accepted inconsistent foreign metadata")
				}
			} else if err != nil || !present || !gatewayFinalOwnershipContainerMatches(facts, container, runtime) ||
				container.Labels["org.opencontainers.image.title"] != "Caddy" {
				t.Fatalf("terminal lookup refused or stripped stable image metadata: %v", err)
			}
			if !bytes.Equal(original, gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)) {
				t.Fatal("terminal lookup changed retained container facts")
			}
		})
	}
}

// Image metadata is not ownership authority. Rig labels, immutable resource
// bindings, and every physical safety check remain mandatory.
func TestGatewayFinalOwnershipLabelsPreserveExactRigAuthority(t *testing.T) {
	f := newGatewayCurrentStateFixture(t)
	port := f.history.Predecessor.Journal.Source.LocalHostPort
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, f)
	facts := gatewayCurrentFinalOwnershipFacts(target, port)
	container, runtime := gatewayCurrentPhysicalRuntimeTestContainer(target, port, true)
	container.Labels["org.opencontainers.image.title"] = "Caddy"
	container.Labels["maintainer"] = "fixture image metadata"
	original := gatewayCurrentMountStabilityInspectionBytes(t, container, runtime)
	if !gatewayFinalOwnershipContainerStaticMatches(facts, container) ||
		!gatewayFinalOwnershipContainerMatches(facts, container, runtime) {
		t.Error("exact terminal-owned container with foreign image metadata was rejected")
	}
	for key := range gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole) {
		for _, kind := range []string{"missing", "changed"} {
			t.Run(kind+"_"+key, func(t *testing.T) {
				candidate := cloneGatewayCurrentPhysicalTestContainer(container)
				if kind == "missing" {
					delete(candidate.Labels, key)
				} else {
					candidate.Labels[key] += "-foreign"
				}
				if gatewayFinalOwnershipContainerStaticMatches(facts, candidate) ||
					gatewayFinalOwnershipContainerMatches(facts, candidate, runtime) {
					t.Fatal("invalid Rig ownership label was accepted with image metadata")
				}
			})
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*caddyInspection)
	}{
		{"extra Rig label", func(c *caddyInspection) { c.Labels["io.rig.unexpected"] = "foreign" }},
		{"replacement container", func(c *caddyInspection) { c.ID = strings.Repeat("8", 64) }},
		{"replacement image", func(c *caddyInspection) { c.Image = strings.Repeat("8", 64) }},
		{"privileged container", func(c *caddyInspection) { c.Privileged = true }},
		{"foreign mount", func(c *caddyInspection) { c.Mounts[0].Name = "foreign-volume" }},
		{"wildcard publication", func(c *caddyInspection) {
			for key := range c.PortBindings {
				c.PortBindings[key][0]["HostIp"] = "0.0.0.0"
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneGatewayCurrentPhysicalTestContainer(container)
			candidate.Mounts = append([]mountInspection(nil), container.Mounts...)
			test.mutate(&candidate)
			if gatewayFinalOwnershipContainerStaticMatches(facts, candidate) ||
				gatewayFinalOwnershipContainerMatches(facts, candidate, runtime) {
				t.Fatal("image metadata masked invalid container authority or hardening")
			}
		})
	}
	if !bytes.Equal(original, gatewayCurrentMountStabilityInspectionBytes(t, container, runtime)) {
		t.Fatal("ownership validation changed the caller's inspection")
	}
}

func TestGatewayCurrentImageMetadataRequiresStableObservation(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "stable metadata"
		if changed {
			name = "metadata changes between reads"
		}
		t.Run(name, func(t *testing.T) {
			runner, target, physical := newGatewayCurrentMountStabilityRuntime(t)
			runner.container.Labels["org.opencontainers.image.title"] = "Caddy"
			original := gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)
			reads := 0
			physical.manager.runner = &gatewayCurrentTerminalStopHookExecutor{
				gatewayCurrentPhysicalExecutor: runner,
				resultHook: func(args []string, result *runtimeprocess.CommandResult) {
					if !gatewayCurrentMountStabilityInspectTarget(args, target.Identity.Rebind.FinalContainer) {
						return
					}
					reads++
					if changed && reads == 2 {
						gatewayCurrentMountStabilityMutateResult(t, result, func(value *gatewayContainerInspection) {
							value.Labels["org.opencontainers.image.title"] = "different"
						})
					}
				},
			}
			proof, err := physical.observe(context.Background(), target)
			if reads != 2 || len(runner.effects) != 0 {
				t.Fatalf("observation did not perform two read-only inspections: reads=%d effects=%d", reads, len(runner.effects))
			}
			if changed {
				if err == nil || gatewayCurrentAttestationCheckpointFromError(err) != gatewayCurrentAttestationCheckpointRuntimeStability ||
					!reflect.DeepEqual(proof, gatewayCurrentPhysicalAttestation{}) {
					t.Fatal("image metadata drift did not fail the full inventory stability check")
				}
			} else if err != nil || !gatewayCurrentServingRestoreProofMatches(proof, target, gatewayCurrentPhysicalRecoveryEffective) {
				t.Fatalf("stable image metadata prevented current observation: %v", err)
			}
			if !bytes.Equal(original, gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)) {
				t.Fatal("observation mutated retained container facts")
			}
		})
	}
}

func TestGatewayRebindCurrentAttestationAcceptsImageMetadataAfterDatabaseCommit(t *testing.T) {
	f, input, driver := newGatewayRebindCurrentAttestationTwoAppCompositionFixture(t)
	injected := false
	driver.runner.afterCreate = func() {
		driver.runner.final.Labels = cloneGatewayCurrentPhysicalTestContainer(driver.runner.final).Labels
		driver.runner.final.Labels["org.opencontainers.image.title"] = "Caddy"
		injected = true
	}
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
	if !injected || err != nil || result.FinalPhase != appaccess.GatewayRebindCommitted ||
		!result.FenceReleased || !driver.boundaryCalled || driver.driverEntries != 1 || driver.delegateRefused {
		t.Fatalf("image metadata blocked two-app committed attestation: injected=%t boundary=%t entries=%d refused=%t err=%v",
			injected, driver.boundaryCalled, driver.driverEntries, driver.delegateRefused, err)
	}
	if driver.runner.final.Labels["org.opencontainers.image.title"] != "Caddy" {
		t.Fatal("attestation stripped image metadata from retained container")
	}
}
