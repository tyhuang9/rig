//go:build linux || windows

package generatedingress

import (
	"context"
	"testing"
	"time"

	generateddomaintest "github.com/hostd/hostd/internal/testsupport/generateddomain"
)

func TestGeneratedDomainSharedTranscriptConstructsExactEmptyOwner(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		identity := newGeneratedDomainIdentityFixture(t)
		identity.options.CommandTimeout = generatedDomainStartupObservationTimeout
		identity.options.OutputLimit = 1 << 20
		runner := generateddomaintest.NewRunner(generateddomaintest.Options{HostPort: identity.options.HostPort})
		environment := &candidateFactoryEnvironment{path: t.TempDir()}
		result, err := newGeneratedDomainConstruction(context.Background(), identity.stores, runner, environment,
			identity.options)
		if err != nil {
			ctx := context.Background()
			physical, _, physicalErr := inspectGeneratedDomainPhysicalIdentity(ctx, identity.stores, runner, identity.options)
			database, databaseErr := identity.stores.GeneratedDomainStartupSnapshot(ctx)
			protected, protectedErr := readGeneratedDomainProtectedStartup(ctx, identity.options.DataRoot)
			expectation, expectationErr := resolveGeneratedDomainGatewayExpectation(database, protected)
			recovery := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
			census, censusErr := resolveGeneratedDomainDockerCensus(protected)
			reader, readerErr := newGeneratedDomainStartupReader(runner, identity.options, physical.manager.daemonID)
			var dockerErr error
			var containerDiagnostic any
			if readerErr == nil {
				reader.expectation = generatedDomainStartupObservationGateway(expectation, recovery)
				reader.recovery, reader.census = recovery, census
				_, dockerErr = readGeneratedDomainDockerStartup(ctx, reader)
				snapshot, _, snapshotErr := reader.readDockerCensus(ctx, time.Now().Add(time.Minute))
				object, found := generatedDomainObjectByName(snapshot, "container", caddyContainerName)
				image, imageErr := reader.readPinnedImage(ctx, time.Now().Add(time.Minute), "")
				container, runtime, containerErr := reader.readDetailedContainer(ctx, time.Now().Add(time.Minute), object.id)
				containerDiagnostic = struct {
					SnapshotErr, ImageErr, ContainerErr                   error
					Found, Running, Restarting, Runtime, Bindings, Static bool
					Container                                             caddyInspection
				}{snapshotErr, imageErr, containerErr, found, container.Running, container.Restarting,
					validGatewayContainerRuntime(runtime, true),
					gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, container.PortBindings),
					validCaddyInspection(container, image.ID, identity.options.HostPort), container}
			}
			t.Fatalf("shared transcript construction: %v; physical=%v database=%v protected=%v expectation=%v census=%v reader=%v docker=%v container=%#v",
				err, physicalErr, databaseErr, protectedErr, expectationErr, censusErr, readerErr, dockerErr, containerDiagnostic)
		}
		if !validGeneratedDomainConstructionResult(result.reservation, result) {
			t.Fatal("shared transcript did not construct the exact private owner family")
		}
	})
}
