//go:build linux || windows

package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedColdBoundaryExecutor func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error)

func (e generatedColdBoundaryExecutor) Execute(ctx context.Context, job jobs.Job, progress jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if err := progress.Report(jobs.ProgressUpdate{Status: jobs.Running, Phase: "validate", Progress: 1, Code: "phase_started"}); err != nil {
		return jobs.ExecutionResult{}, err
	}
	return e(ctx, job, progress)
}

func TestGeneratedDomainColdRuntimeBoundary(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("no_effect_successor_resumes_with_one_capacity_charge", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			predecessor, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			candidate.moveToWaitingHealth(t, execution)
			if err := execution.WaitHealthyAndRecord(invocation); err != nil {
				t.Fatal(err)
			}
			if _, err := execution.FinishHealth(); err != nil {
				t.Fatal(err)
			}

			store, err := newStateStore(identity.dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.createInitialEmpty(); err != nil {
				t.Fatal(err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, _, ingressIP := ingressNetworkCandidate(0)
			ingress := &ingressRunner{
				appID: migration.guarded.appID, network: predecessor.NetworkName, endpoint: generatedRouteEndpoint(predecessor),
				caddyNetworks:     map[string]*networkAttachment{caddyNetworkName: {GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
				ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {Name: caddyContainerName, IPv4Address: ingressIP + "/28"}},
				files:             map[string][]byte{},
			}
			physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
				slot: generatedidentity.Slot(predecessor.Slot), endpoints: map[string]generatedRouteEndpointSnapshot{
					predecessor.ContainerID: {appID: migration.guarded.appID,
						endpoint: generatedRouteEndpoint(predecessor), slot: generatedidentity.Slot(predecessor.Slot)},
				}}
			routeSeed := &generatedDomainRouteSeedRunner{daemonID: "generated-cold-runtime-boundary", delegate: ingress}
			reader := &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes, delegate: routeSeed}
			manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database, stores: identity.stores, runner: reader, options: identity.options}, store)
			if err != nil {
				t.Fatal(err)
			}
			physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores, reader, identity.options)
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
			sealed, err := routes.Pin(migration.guarded.context, execution, generatedRouteRequest(migration.guarded.appID, predecessor))
			if err != nil {
				t.Fatal(err)
			}
			activated, err := sealed.Activate(migration.guarded.context)
			if err != nil || activated.Phase != generatedruntimestate.PhaseDraining {
				t.Fatalf("activate predecessor = %+v, %v", activated, err)
			}
			runtimeState := generatedruntimestate.New(identity.database)
			if _, err := runtimeState.Advance(context.Background(), migration.guarded.appID, migration.request.DeploymentID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := migration.repository.Transition(context.Background(), migration.guarded.appID, migration.request.DeploymentID, Succeeded, ""); err != nil {
				t.Fatal(err)
			}
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database, time.Now().UTC(), migration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover completed predecessor job: result=%+v err=%v", recovered, err)
			}
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return jobs.ExecutionResult{}, nil
				}), migration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory, nil)

			created, err := migration.jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
				ApplicationID: migration.guarded.appID, IdempotencyKey: "cold-runtime-successor", RequestedBy: migration.guarded.actorID,
				Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
			}, jobs.DeploymentCreateHooks{})
			if err != nil || !created.Created {
				t.Fatalf("create successor job: created=%t err=%v", created.Created, err)
			}
			var successor deployments.Deployment
			pauseStage := "not_entered"
			var pauseCause error
			pauseExecutor := generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				pauseStage = "get_or_create"
				var err error
				successor, _, err = migration.repository.GetOrCreateByJob(ctx, migration.guarded.appID, job.ID, "current")
				if err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "initialize_runtime"
				successor, err = migration.repository.InitializeRuntime(ctx, migration.guarded.appID, successor.ID, migration.request.ReleaseID,
					migration.request.ConfigurationRevisionID, migration.request.ConfigurationRevisionNumber, deployments.RuntimeGeneratedNode,
					migration.request.DeploymentPlanRevisionID, migration.request.DeploymentPlanRevisionNumber)
				if err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "claim"
				claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, migration.guarded.appID, job.Attempt)
				if err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "gate"
				if err := migration.repository.Gate(ctx, deployments.GateRequest{Claim: claim, DeploymentID: successor.ID}); err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "begin_runtime"
				begun, wasCreated, err := runtimeState.Begin(ctx, generatedruntimestate.BeginInput{DeploymentID: successor.ID, AppID: migration.guarded.appID,
					ReleaseID: migration.request.ReleaseID, DeploymentPlanRevisionID: migration.request.DeploymentPlanRevisionID,
					DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber, ComponentNames: []string{"api"}})
				if err != nil || !wasCreated {
					pauseCause = errors.Join(err, errors.New("successor runtime was not freshly created"))
					return jobs.ExecutionResult{}, pauseCause
				}
				pauseStage = "advance_building"
				if _, err := runtimeState.Advance(ctx, migration.guarded.appID, successor.ID, begun.Phase, generatedruntimestate.PhaseBuilding, ""); err != nil {
					pauseCause = err
					return jobs.ExecutionResult{}, err
				}
				pauseStage = "paused"
				return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser, PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
			})
			runGeneratedColdBoundaryWorker(t, migration.jobService, pauseExecutor, created.Job.ID, jobs.WaitingUser, identity.options.WorkingDirectory,
				func() string { return "stage=" + pauseStage + " cause=" + boundedErrorText(pauseCause) })

			state, err = store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
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
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver, scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver, scope: observation.V1Volume.Scope,
						options: cloneStringMap(observation.V1Volume.Options), mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := candidate.runner.snapshot().policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: predecessor.ContainerID, name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-boundary", objects: objects, imageID: observation.Image.ID, gateway: gateway,
			}}
			domain, err := NewGeneratedDomain(context.Background(), identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			if err != nil || domain == nil {
				diagnostic := generatedDomainDiagnoseOuterStable(t, identity, runner.delegate)
				stage := generatedColdBoundaryStageTrace(t, identity, runner, store)
				t.Fatalf("construct cold domain: domain=%v err=%v diagnostic=%s stage=%s snapshot=%s commandCount=%d", domain, err,
					diagnostic, stage, generatedColdBoundarySnapshotSummary(t, identity), len(runner.snapshot()))
			}
			if domain.construction.cold == nil || len(domain.construction.cold.groups) != 2 {
				t.Fatalf("cold groups=%#v", domain.construction.cold)
			}
			var successorGroup *generatedDomainColdManifestGroup
			for index := range domain.construction.cold.groups {
				group := &domain.construction.cold.groups[index]
				if group.deploymentID == successor.ID {
					successorGroup = group
				}
			}
			if successorGroup == nil || successorGroup.head == nil || successorGroup.head.deploymentID != migration.request.DeploymentID ||
				len(successorGroup.components) != 1 || successorGroup.components[0].charged || successorGroup.components[0].retained || successorGroup.components[0].receiptID != "" {
				t.Fatalf("no-effect successor group=%#v", successorGroup)
			}

			resumed, err := migration.jobService.Resume(created.Job.ID)
			if err != nil || resumed.Attempt != 1 || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume successor=%+v err=%v", resumed, err)
			}
			var reserveCalls int
			capacityProbesBefore := generatedColdBoundaryCapacityProbes(runner.snapshot())
			successorSlot := "blue"
			if predecessor.Slot == successorSlot {
				successorSlot = "green"
			}
			reserveExecutor := generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				reserveCalls++
				claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, migration.guarded.appID, job.Attempt)
				if err != nil {
					return jobs.ExecutionResult{}, err
				}
				reserved, err := domain.Candidates().ReserveReplacement(ctx, GeneratedReplacementRequest{
					Claim: claim, DeploymentID: successor.ID, ReleaseID: migration.request.ReleaseID,
					ConfigurationRevisionID: migration.request.ConfigurationRevisionID, ConfigurationRevisionNumber: migration.request.ConfigurationRevisionNumber,
					DeploymentPlanRevisionID: migration.request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
					CandidateSlot: successorSlot,
				})
				if err != nil {
					return jobs.ExecutionResult{}, err
				}
				reserved.Release()
				return jobs.ExecutionResult{}, nil
			})
			runGeneratedColdBoundaryWorker(t, migration.jobService, reserveExecutor, created.Job.ID, jobs.Succeeded, identity.options.WorkingDirectory, nil)
			if reserveCalls != 1 {
				t.Fatalf("resumed reserve calls=%d want 1", reserveCalls)
			}
			capacityProbesAfter := generatedColdBoundaryCapacityProbes(runner.snapshot())
			if capacityProbesAfter-capacityProbesBefore != 1 {
				t.Fatalf("resumed capacity probe delta=%d want 1 commands=%#v", capacityProbesAfter-capacityProbesBefore, runner.snapshot())
			}
		})
	})
	t.Run("retained_risk_resumes_without_a_second_capacity_charge", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			candidateIdentity, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			candidate.moveToWaitingHealth(t, execution)
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database, time.Now().UTC(), migration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover receipt-owned job: result=%+v err=%v", recovered, err)
			}
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser, PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
				}), migration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)

			store, err := newStateStore(identity.dataRoot)
			if err != nil || store.createInitialEmpty() != nil {
				t.Fatalf("install empty gateway state: %v", err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
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
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver, scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver, scope: observation.V1Volume.Scope,
						options: cloneStringMap(observation.V1Volume.Options), mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := candidate.runner.snapshot().policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: candidateIdentity.ContainerID, name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-risk", objects: objects, imageID: observation.Image.ID, gateway: gateway,
			}}
			priorManager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
				database: identity.database, stores: identity.stores, runner: runner, options: identity.options,
			}, store)
			if err != nil {
				t.Fatal(err)
			}
			releasePriorLock, err := priorManager.lockGateway(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := releasePriorLock(); err != nil {
				t.Fatal(err)
			}
			domain, err := NewGeneratedDomain(context.Background(), identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			if err != nil || domain == nil || domain.construction.cold == nil || len(domain.construction.cold.groups) != 1 {
				diagnostic := generatedDomainDiagnoseOuterStable(t, identity, runner.delegate)
				t.Fatalf("construct retained risk: domain=%v err=%v diagnostic=%s snapshot=%s commandCount=%d", domain, err,
					diagnostic, generatedColdBoundarySnapshotSummary(t, identity), len(runner.snapshot()))
			}
			group := domain.construction.cold.groups[0]
			if len(group.components) != 1 || !group.components[0].charged || !group.components[0].retained || group.components[0].receiptID == "" {
				t.Fatalf("retained-risk group=%#v", group)
			}
			if probes := generatedColdBoundaryCapacityProbes(runner.snapshot()); probes != 1 {
				t.Fatalf("cold retained-risk probes=%d want 1 commands=%#v", probes, runner.snapshot())
			}
			resumed, err := migration.jobService.Resume(migration.claim.JobID())
			if err != nil || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume retained-risk job=%+v err=%v", resumed, err)
			}
			var reserveErr error
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					claim, err := deploymentexecution.NewClaim(migration.binding, job.ID, migration.guarded.appID, job.Attempt)
					if err != nil {
						return jobs.ExecutionResult{}, err
					}
					request := GeneratedReplacementRequest(validation.request)
					request.Claim = claim
					value, err := domain.Candidates().ReserveReplacement(ctx, request)
					reserveErr = err
					if value != nil {
						value.Release()
					}
					return jobs.ExecutionResult{}, err
				}), migration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory, nil)
			if reserveErr != nil {
				t.Fatalf("resumed retained-risk reserve=%v", reserveErr)
			}
			if probes := generatedColdBoundaryCapacityProbes(runner.snapshot()); probes != 1 {
				t.Fatalf("resumed retained-risk double charged: probes=%d commands=%#v", probes, runner.snapshot())
			}
		})
	})
	t.Run("stopped_physical_disagreement_refuses_without_traffic", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, []string{"api"}, []string{"api"})
			identity.stores = migration.repository.stores
			validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
			candidate := newCandidateOwnerFixtureWithValidationAndOptions(t, validation, generatedDomainCandidateOptions(identity.options))
			reservation := candidate.reserve(t)
			execution := candidate.pin(t, reservation)
			invocation := candidate.beginCreate(t, execution)
			candidate.bindInvocation(t, invocation)
			candidateIdentity, err := execution.CreateAndRecord(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			candidate.moveToWaitingHealth(t, execution)
			if err := execution.WaitHealthyAndRecord(invocation); err != nil {
				t.Fatal(err)
			}
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			stopped := candidate.runner.snapshot()
			stopped.running = false
			candidate.runner.setPhysical(stopped)

			store, err := newStateStore(identity.dataRoot)
			if err != nil || store.createInitialEmpty() != nil {
				t.Fatalf("install empty gateway state: %v", err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			_, v2, journal := gatewayV2IdentityTestState(t)
			observation := gatewayV2IdentityTestObservation(t, state, v2, journal, gatewayTopologyExactV1Only)
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
				case caddyNetworkName:
					objects[index].id = observation.V1NetworkID
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver, scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver, scope: observation.V1Volume.Scope,
						options: cloneStringMap(observation.V1Volume.Options), mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := candidate.runner.snapshot().policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: candidateIdentity.ContainerID, name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-stopped", objects: objects, imageID: observation.Image.ID, gateway: gateway,
			}}
			creates, starts := candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart)
			domain, err := NewGeneratedDomain(context.Background(), identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			if err == nil || domain != nil {
				t.Fatalf("stopped uncertain candidate published domain=%v err=%v", domain, err)
			}
			if candidate.runner.count(candidateOwnerCreate) != creates || candidate.runner.count(candidateOwnerStart) != starts ||
				candidate.runner.count(candidateOwnerStop) != 0 || candidate.runner.count(candidateOwnerRemove) != 0 {
				t.Fatalf("refusal emitted candidate effects: creates=%d/%d starts=%d/%d stops=%d removes=%d", candidate.runner.count(candidateOwnerCreate), creates,
					candidate.runner.count(candidateOwnerStart), starts, candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove))
			}
			for _, command := range runner.snapshot() {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) &&
					!(len(command) >= 6 && command[0] == "container" && command[1] == "exec" && command[len(command)-1] == capacityProbeCommand) {
					t.Fatalf("stopped refusal emitted Docker/app traffic: %#v", command)
				}
				if len(command) >= 2 && command[0] == "container" && command[1] == "exec" &&
					!(len(command) >= 6 && command[2] == caddyContainerName && command[len(command)-1] == capacityProbeCommand) &&
					!generatedDomainGatewayAdminCommand(command, observation.V1Container.ID) &&
					!generatedDomainGatewayChallengeCommand(command, observation.V1Container.ID) {
					t.Fatalf("stopped refusal emitted application or unowned-listener traffic: %#v", command)
				}
			}
		})
	})
	t.Run("activation_attempt_lineage_bounds", func(t *testing.T) {
		const (
			appID        = "11111111-1111-4111-8111-111111111111"
			deploymentID = "22222222-2222-4222-8222-222222222222"
			releaseID    = "33333333-3333-4333-8333-333333333333"
			jobID        = "44444444-4444-4444-8444-444444444444"
			receiptID    = "55555555-5555-4555-8555-555555555555"
			artifactID   = "66666666-6666-4666-8666-666666666666"
			intentID     = "77777777-7777-4777-8777-777777777777"
		)
		snapshot := candidateAdmissionSnapshot{
			pins: candidateImmutablePins{jobID: jobID, releaseID: releaseID, candidateSlot: "blue"},
			headPins: candidateHeadPins{appID: appID, deploymentID: deploymentID,
				releaseID: releaseID, slot: "blue"},
			observedHead: candidateHeadSnapshot{appID: appID, deploymentID: deploymentID,
				releaseID: releaseID, slot: "blue", activationReceiptID: receiptID, generation: 1},
			components: []candidateAdmissionComponent{{
				plan: deploymentplans.Component{Name: "api"}, slot: "blue",
				containerName: "rig-test-api-blue", containerID: strings.Repeat("8", 64),
				artifact: &candidateArtifactSnapshot{id: artifactID},
				receipt:  &candidateIntentSnapshot{id: intentID, policyDigest: strings.Repeat("9", 64)},
			}},
		}
		roster, ok := generatedDomainColdActivationRoster(snapshot)
		if !ok {
			t.Fatal("canonical activation roster was rejected")
		}
		activation := generatedruntimestate.GeneratedDomainStartupRouteActivation{
			ReceiptID: receiptID, AppID: appID, SuccessorDeploymentID: deploymentID,
			SuccessorReleaseID: releaseID, SuccessorSlot: "blue", SuccessorGeneration: 1,
			CreatorJobID: jobID, CreatorWorkerAttempt: 1, WriterJobID: jobID, WriterWorkerAttempt: 1,
			CandidateRosterDigest:   generatedRouteActivationRosterDigest(roster),
			PredecessorRosterDigest: generatedRouteActivationRosterDigest(nil),
			RouteIntentDigest:       strings.Repeat("a", 64), CommittedRouteProofDigest: strings.Repeat("b", 64),
		}
		pins := candidateValidationPins{identity: candidateValidationIdentity{
			applicationID: appID, jobID: jobID, attempt: 2,
		}}
		if !generatedDomainColdActivationMatches(pins, snapshot, nil,
			[]generatedruntimestate.GeneratedDomainStartupRouteActivation{activation}) {
			t.Fatal("immutable prior activation attempt did not survive a later same-job completion attempt")
		}
		for _, test := range []struct {
			name   string
			mutate func(*generatedruntimestate.GeneratedDomainStartupRouteActivation)
		}{
			{name: "zero creator attempt", mutate: func(value *generatedruntimestate.GeneratedDomainStartupRouteActivation) {
				value.CreatorWorkerAttempt = 0
			}},
			{name: "future creator attempt", mutate: func(value *generatedruntimestate.GeneratedDomainStartupRouteActivation) {
				value.CreatorWorkerAttempt = 3
			}},
			{name: "crossed creator job", mutate: func(value *generatedruntimestate.GeneratedDomainStartupRouteActivation) {
				value.CreatorJobID = "88888888-8888-4888-8888-888888888888"
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				crossed := activation
				test.mutate(&crossed)
				if generatedDomainColdActivationMatches(pins, snapshot, nil,
					[]generatedruntimestate.GeneratedDomainStartupRouteActivation{crossed}) {
					t.Fatal("crossed activation lineage was accepted")
				}
			})
		}
	})
}

func TestGeneratedDomainColdPreActivationHandoffSeeds(t *testing.T) {
	const (
		successorID   = "22222222-2222-4222-8222-222222222222"
		predecessorID = "11111111-1111-4111-8111-111111111111"
		apiReceipt    = "33333333-3333-4333-8333-333333333333"
		webReceipt    = "44444444-4444-4444-8444-444444444444"
	)
	newGroup := func() generatedDomainColdGroup {
		return generatedDomainColdGroup{
			pins:     candidateValidationPins{deploymentID: successorID},
			kind:     generatedDomainColdRisky,
			bindHead: true,
			snapshot: candidateAdmissionSnapshot{
				headPins:     candidateHeadPins{previousDeploymentID: predecessorID},
				observedHead: candidateHeadSnapshot{deploymentID: predecessorID},
				runtime:      candidateRuntimeSnapshot{phase: generatedruntimestate.PhaseSwitchingRoute},
				components: []candidateAdmissionComponent{
					{plan: deploymentplans.Component{Name: "api"}, state: generatedruntimestate.ComponentHealthy,
						receipt: &candidateIntentSnapshot{id: apiReceipt}},
					{plan: deploymentplans.Component{Name: "web"}, state: generatedruntimestate.ComponentHealthy,
						receipt: &candidateIntentSnapshot{id: webReceipt}},
				},
			},
			receipts: map[string]candidateColdReceiptSeed{
				"api": {id: apiReceipt, physical: candidatePhysicalPresent},
				"web": {id: webReceipt, physical: candidatePhysicalPresent},
			},
		}
	}
	pending := generatedDomainColdPendingActivationEvidence{successorDeployment: successorID, beforeActivation: true}

	t.Run("exact pending promotes only the cloned complete roster", func(t *testing.T) {
		group := newGroup()
		seeds, ok := generatedDomainColdPreActivationHandoffSeeds(group, pending)
		if !ok || len(seeds) != 2 || seeds["api"].physical != candidatePhysicalHandedToRoute ||
			seeds["web"].physical != candidatePhysicalHandedToRoute ||
			group.receipts["api"].physical != candidatePhysicalPresent ||
			group.receipts["web"].physical != candidatePhysicalPresent {
			t.Fatalf("exact pending handoff seeds=%#v source=%#v ok=%t", seeds, group.receipts, ok)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*generatedDomainColdGroup, *generatedDomainColdPendingActivationEvidence)
	}{
		{name: "missing receipt refuses whole roster", mutate: func(group *generatedDomainColdGroup, _ *generatedDomainColdPendingActivationEvidence) {
			delete(group.receipts, "web")
		}},
		{name: "crossed pending successor refuses", mutate: func(_ *generatedDomainColdGroup, pending *generatedDomainColdPendingActivationEvidence) {
			pending.successorDeployment = predecessorID
		}},
		{name: "post activation pending refuses", mutate: func(_ *generatedDomainColdGroup, pending *generatedDomainColdPendingActivationEvidence) {
			pending.beforeActivation = false
		}},
		{name: "partial healthy roster refuses", mutate: func(group *generatedDomainColdGroup, _ *generatedDomainColdPendingActivationEvidence) {
			group.snapshot.components = group.snapshot.components[:1]
		}},
		{name: "non-present receipt refuses", mutate: func(group *generatedDomainColdGroup, _ *generatedDomainColdPendingActivationEvidence) {
			seed := group.receipts["api"]
			seed.physical = candidatePhysicalHandedToRoute
			group.receipts["api"] = seed
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			group, candidate := newGroup(), pending
			test.mutate(&group, &candidate)
			if seeds, ok := generatedDomainColdPreActivationHandoffSeeds(group, candidate); ok || seeds != nil {
				t.Fatalf("invalid pre-activation handoff seeds=%#v ok=%t", seeds, ok)
			}
		})
	}
}

type generatedDomainGatewayConfigObservationTestRunner struct {
	containerID string
	filename    string
	config      []byte
	failRestart bool
	failLive    bool
	requests    [][]string
}

func (r *generatedDomainGatewayConfigObservationTestRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, append([]string(nil), request.Args...))
	args := request.Args
	if reflect.DeepEqual(args, []string{"container", "cp", r.containerID + ":/config/" + r.filename, "-"}) {
		if r.failRestart {
			return runtimeprocess.CommandResult{}, errors.New("injected restart config read failure")
		}
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if err := writer.WriteHeader(&tar.Header{Name: r.filename, Mode: 0o600, Size: int64(len(r.config)), Typeflag: tar.TypeReg}); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		if _, err := writer.Write(r.config); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		if err := writer.Close(); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		return runtimeprocess.CommandResult{Stdout: archive.Bytes()}, nil
	}
	if reflect.DeepEqual(args, []string{"container", "exec", r.containerID,
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/"}) {
		if r.failLive {
			return runtimeprocess.CommandResult{}, errors.New("injected live config read failure")
		}
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.config...)}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected gateway config observation command")
}

func TestGeneratedDomainGatewayConfigObservationBindsExactReader(t *testing.T) {
	const filename = "active.json"
	containerID := strings.Repeat("a", 64)
	config := []byte(`{"apps":{}}`)
	runner := &generatedDomainGatewayConfigObservationTestRunner{containerID: containerID, filename: filename, config: config}
	reader := &generatedDomainStartupReader{options: generatedDomainOptions{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Second, OutputLimit: 1024,
	}, runner: runner}
	observation, err := reader.readGatewayConfigObservation(context.Background(), time.Now().Add(time.Second), containerID, filename)
	wantRequests := [][]string{
		{"container", "cp", containerID + ":/config/" + filename, "-"},
		{"container", "exec", containerID,
			"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
			"--max-time", "10", "http://127.0.0.1:2019/config/"},
	}
	if err != nil || observation == nil || !reflect.DeepEqual(runner.requests, wantRequests) {
		t.Fatalf("strict config observation=%#v error=%v requests=%#v", observation, err, runner.requests)
	}

	restart, err := reader.readGatewayRestartConfigObserved(context.Background(), time.Now().Add(time.Second),
		containerID, filename, observation)
	if err != nil || string(restart) != `{"apps":{}}` {
		t.Fatalf("borrow restart config=%q error=%v", restart, err)
	}
	restart[0] = 'x'
	clear(restart)
	if string(observation.restart) != `{"apps":{}}` {
		t.Fatal("borrowed restart config aliased the observation")
	}
	live, err := reader.readGatewayLiveConfigObserved(context.Background(), time.Now().Add(time.Second),
		containerID, filename, observation)
	if err != nil || string(live) != `{"apps":{}}` {
		t.Fatalf("borrow live config=%q error=%v", live, err)
	}
	live[0] = 'x'
	clear(live)
	if string(observation.live) != `{"apps":{}}` {
		t.Fatal("borrowed live config aliased the observation")
	}

	for _, test := range []struct {
		name     string
		reader   *generatedDomainStartupReader
		id, file string
	}{
		{name: "wrong reader", reader: &generatedDomainStartupReader{}, id: containerID, file: filename},
		{name: "wrong immutable id", reader: reader, id: strings.Repeat("b", 64), file: filename},
		{name: "wrong restart file", reader: reader, id: containerID, file: "other.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if value, err := test.reader.readGatewayRestartConfigObserved(context.Background(), time.Now().Add(time.Second),
				test.id, test.file, observation); err == nil || value != nil {
				clear(value)
				t.Fatalf("borrowed restart value=%q error=%v", value, err)
			}
			if value, err := test.reader.readGatewayLiveConfigObserved(context.Background(), time.Now().Add(time.Second),
				test.id, test.file, observation); err == nil || value != nil {
				clear(value)
				t.Fatalf("borrowed live value=%q error=%v", value, err)
			}
		})
	}

	failed := &generatedDomainGatewayConfigObservationTestRunner{containerID: containerID, filename: filename, config: config, failLive: true}
	reader.runner = failed
	if value, err := reader.readGatewayConfigObservation(context.Background(), time.Now().Add(time.Second),
		containerID, filename); err == nil || value != nil || !reflect.DeepEqual(failed.requests, wantRequests) {
		t.Fatalf("fresh staged observation=%#v error=%v requests=%#v", value, err, failed.requests)
	}
	restartFailed := &generatedDomainGatewayConfigObservationTestRunner{containerID: containerID, filename: filename, config: config, failRestart: true}
	reader.runner = restartFailed
	if value, err := reader.readGatewayConfigObservation(context.Background(), time.Now().Add(time.Second),
		containerID, filename); err == nil || value != nil || !reflect.DeepEqual(restartFailed.requests, wantRequests[:1]) {
		t.Fatalf("restart observation=%#v error=%v requests=%#v", value, err, restartFailed.requests)
	}
	observation.clear()
	if observation.reader != nil || observation.containerID != "" || observation.filename != "" ||
		observation.restart != nil || observation.live != nil {
		t.Fatalf("cleared observation=%#v", observation)
	}
}

func TestGeneratedDomainColdPendingGatewayConfigDisposition(t *testing.T) {
	before := []byte(`{"apps":{"http":{"servers":{"before":{}}}}}`)
	serving := []byte(`{"apps":{"http":{"servers":{"serving":{}}}}}`)
	for _, test := range []struct {
		name          string
		restart, live []byte
		want          generatedDomainColdPendingGatewayDisposition
		accepted      bool
	}{
		{name: "previous exact", restart: before, live: before, want: generatedDomainColdPendingGatewayPreviousExact, accepted: true},
		{name: "proposed exact", restart: serving, live: serving, want: generatedDomainColdPendingGatewayProposedExact, accepted: true},
		{name: "mixed projections refuse", restart: before, live: serving},
		{name: "neither projection refuses", restart: []byte(`{"apps":{}}`), live: []byte(`{"apps":{}}`)},
		{name: "both projections refuse", restart: before, live: before, want: generatedDomainColdPendingGatewayUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateBefore, candidateServing := before, serving
			if test.name == "both projections refuse" {
				candidateServing = before
			}
			got, accepted := classifyGeneratedDomainColdPendingGatewayConfigs(candidateBefore, candidateServing,
				test.restart, test.live)
			if accepted != test.accepted || got != test.want {
				t.Fatalf("disposition=%d accepted=%t want=%d/%t", got, accepted, test.want, test.accepted)
			}
		})
	}
}

// TestGeneratedDomainColdPreviousExactProtectedModeProjection is deliberately
// a protected-mode classifier boundary, not a second end-to-end cold process
// fixture. The authentic Legacy recovery parent exercises the common Pin path;
// these rows prove the NativeV2 and CurrentV1 switches reconstruct and clone
// their complete typed transitions without collapsing them into Legacy state.
func TestGeneratedDomainColdPreviousExactProtectedModeProjection(t *testing.T) {
	t.Run("committed_v2", func(t *testing.T) {
		fixture := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		fixture = generatedDomainColdCanonicalCommittedV2Pending(t, fixture)
		fixture.installProtected(t, fixture.pending)
		protected := generatedRoutePreviousCommittedV2ProtectedSnapshot(t, fixture)
		database, _ := generatedDomainNativeV2RecoveryFixture(t, fixture.pending, fixture.journal)
		originalSource := cloneRouteState(*protected.nativeV1)
		originalState := cloneGatewayV2RouteState(protected.upgrade.state)
		originalJournal := protected.upgrade.journal
		originalGenerations := make([]gatewayV2RouteState, len(protected.upgrade.generations))
		for index := range protected.upgrade.generations {
			originalGenerations[index] = cloneGatewayV2RouteState(protected.upgrade.generations[index].State)
		}
		if _, accepted := generatedDomainCommittedUpgradeGatewayCandidate(database, protected, false); accepted {
			t.Fatal("ordinary committed selector admitted a Pending route transition")
		}
		selected, accepted := generatedDomainCommittedUpgradeGatewaySelection(database, protected)
		candidate, routeAccepted := generatedDomainCommittedUpgradeRoutePendingGatewayCandidate(database, protected)
		digest, digestErr := canonicalDigest(fixture.pending)
		if !accepted || !routeAccepted || digestErr != nil || candidate.nativeV1 == nil || candidate.nativeV2 == nil ||
			candidate.journal == nil || candidate.upgrade == nil || candidate.kind != generatedDomainGatewayNativeV2 ||
			candidate.stateDigest != digest || !reflect.DeepEqual(*candidate.nativeV1, *protected.nativeV1) ||
			!reflect.DeepEqual(*candidate.nativeV2, protected.upgrade.state) ||
			!reflect.DeepEqual(*candidate.journal, protected.upgrade.journal) ||
			!reflect.DeepEqual(*candidate.upgrade, selected) ||
			!reflect.DeepEqual(candidate.upgrade.State, *candidate.nativeV2) ||
			!reflect.DeepEqual(candidate.upgrade.Journal, *candidate.journal) {
			t.Fatalf("committed V2 route-Pending candidate=%#v selected=%#v accepted=%t/%t digest=%v",
				candidate, selected, accepted, routeAccepted, digestErr)
		}

		lanRequest := gatewayV2LANGrantRequestForState(t, fixture.before)
		lanBinding, err := gatewayV2LANBindingForRequest(lanRequest)
		if err != nil {
			t.Fatal(err)
		}
		lanState := cloneGatewayV2RouteState(fixture.before)
		lanPrevious := cloneGatewayV2AppRoute(lanState.Apps[lanRequest.AppID])
		lanProposed := cloneGatewayV2AppRoute(lanPrevious)
		lanProposed.LAN = &lanBinding
		lanState.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingLANGrant,
			AppID: lanRequest.AppID, Previous: &lanPrevious, Proposed: lanProposed}
		if !validGatewayV2RouteState(lanState) {
			t.Fatal("committed V2 LAN refusal control is not a valid protected Pending")
		}
		lanProtected := generatedDomainColdCommittedV2ProtectedState(t, protected, lanState)
		generatedDomainColdAssertNoCommittedV2RoutePending(t, database, lanProtected, fixture.transition,
			"LAN Pending")

		invalidState := cloneGatewayV2RouteState(fixture.pending)
		invalidState.SourceV1StateDigest = strings.Repeat("f", 64)
		if invalidState.SourceV1StateDigest == fixture.pending.SourceV1StateDigest {
			invalidState.SourceV1StateDigest = strings.Repeat("e", 64)
		}
		invalidCleared := cloneGatewayV2RouteState(invalidState)
		invalidCleared.Pending = nil
		if !validGatewayV2RouteState(invalidState) ||
			validGatewayTopologyInputs(*protected.nativeV1, invalidCleared, fixture.journal) {
			t.Fatal("committed V2 cleared-topology refusal control is not isolated")
		}
		invalidProtected := generatedDomainColdCommittedV2ProtectedState(t, protected, invalidState)
		generatedDomainColdAssertNoCommittedV2RoutePending(t, database, invalidProtected, fixture.transition,
			"invalid Pending-cleared topology")

		recoveries, recoveriesValid := generatedDomainStartupRecoveries(database, protected,
			generatedDomainDockerStartupSnapshot{cold: true})
		classification, classifyErr := classifyGeneratedDomainStartup(database, protected,
			generatedDomainDockerStartupSnapshot{cold: true})
		if !recoveriesValid || len(recoveries) != 0 || classifyErr != nil ||
			classification.disposition != generatedDomainStartupRefused ||
			classification.reason != generatedDomainStartupReasonColdAccounting || len(classification.recovery) != 0 {
			t.Fatalf("committed V2 route-Pending cold classification=%#v recoveries=%#v valid=%t error=%v",
				classification, recoveries, recoveriesValid, classifyErr)
		}
		unresolvedLAN := database
		unresolvedLAN.Gateway.Grants = generatedDomainGrantStartupSnapshot([]GatewayV2LANStartupClaim{
			gatewayV2LANStartupClaim(lanRequest, appaccess.AppAccessGrantPrepared, 1),
		})
		classification, classifyErr = classifyGeneratedDomainStartup(unresolvedLAN, protected,
			generatedDomainDockerStartupSnapshot{cold: true})
		if classifyErr != nil || classification.disposition != generatedDomainStartupRefused ||
			classification.reason != generatedDomainStartupReasonAmbiguous || len(classification.recovery) != 0 {
			t.Fatalf("route-Pending with unresolved LAN claim classification=%#v error=%v",
				classification, classifyErr)
		}

		evidence := generatedDomainColdPreviousProtectedModeEvidence(t, database, protected, fixture.transition)
		if evidence.transition.kind != generatedDomainGatewayNativeV2 ||
			evidence.transition.nativeV2 == nil ||
			!reflect.DeepEqual(*evidence.transition.nativeV2, *fixture.transition.nativeV2) ||
			evidence.protectedGateway.kind != generatedDomainGatewayNativeV2 ||
			evidence.protectedGateway.nativeV1 == nil || evidence.protectedGateway.nativeV2 == nil ||
			evidence.protectedGateway.journal == nil || evidence.protectedGateway.upgrade == nil ||
			!reflect.DeepEqual(*evidence.protectedGateway.nativeV2, fixture.pending) ||
			!reflect.DeepEqual(*evidence.protectedGateway.journal, fixture.journal) {
			t.Fatalf("committed V2 Pending projection=%#v", evidence)
		}
		if !reflect.DeepEqual(*protected.nativeV1, originalSource) ||
			!reflect.DeepEqual(protected.upgrade.state, originalState) ||
			!reflect.DeepEqual(protected.upgrade.journal, originalJournal) ||
			len(protected.upgrade.generations) != len(originalGenerations) {
			t.Fatal("committed V2 route-Pending classification mutated its protected input")
		}
		for index := range originalGenerations {
			if !reflect.DeepEqual(protected.upgrade.generations[index].State, originalGenerations[index]) {
				t.Fatalf("committed V2 route-Pending classification mutated generation %d", index)
			}
		}
	})

	t.Run("gateway_current_with_retained_v2", func(t *testing.T) {
		fixture := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		fixture.installPending(t)
		snapshot := generatedRoutePreviousGatewayCurrentSnapshotFor(t, fixture)
		protected, currentV2Name := generatedDomainColdPreviousCurrentV2Companion(t,
			snapshot.protected, fixture.transition)
		var database deployments.GeneratedDomainStartupSnapshot
		database.Gateway.Rebind = routeOperationCommittedSnapshot(fixture.fixture)
		exactEntry, exactEntryFound := protected.currentV2.files[currentV2Name]
		if !exactEntryFound || !validGatewayCurrentRouteStateV2(exactEntry.State) ||
			!generatedDomainCurrentDatabaseMatches(database, exactEntry.State.Lineage,
				exactEntry.State.Profile, exactEntry.State.TransferManifestDigest) {
			t.Fatal("exact Current V2 companion is not structurally valid and database-selectable")
		}
		crossed := protected
		crossed.currentV2.files = make(map[string]gatewayCurrentV2CensusEntry, len(protected.currentV2.files))
		for name, entry := range protected.currentV2.files {
			entry.State = cloneGatewayCurrentRouteStateV2(entry.State)
			crossed.currentV2.files[name] = entry
		}
		crossedEntry := crossed.currentV2.files[currentV2Name]
		if crossedEntry.State.SourceV1 == nil {
			t.Fatal("Current V2 companion has no V1 origin")
		}
		origin := *crossedEntry.State.SourceV1
		origin.Digest = strings.Repeat("f", 64)
		crossedEntry.State.SourceV1 = &origin
		crossedEntry.State.Digest = ""
		var err error
		crossedEntry.State.Digest, err = gatewayCurrentRouteStateDigestV2(crossedEntry.State)
		if err != nil || !validGatewayCurrentRouteStateV2(crossedEntry.State) ||
			!generatedDomainCurrentDatabaseMatches(database, crossedEntry.State.Lineage,
				crossedEntry.State.Profile, crossedEntry.State.TransferManifestDigest) {
			t.Fatalf("crossed Current V2 companion is not a valid independent candidate: %v", err)
		}
		crossed.currentV2.files[currentV2Name] = crossedEntry
		if crossedEvidence, found, err := generatedDomainColdPendingActivation(database, crossed,
			[]generatedDomainColdGroup{generatedDomainColdPreviousProtectedModeGroup(t, fixture.sealed)}); found || !errors.Is(err, errGeneratedDomainConstructionRefused) || crossedEvidence.transition != nil {
			t.Fatalf("crossed Current V2 companion evidence=%#v found=%t error=%v",
				crossedEvidence, found, err)
		}

		evidence := generatedDomainColdPreviousProtectedModeEvidence(t, database, protected, fixture.sealed)
		if evidence.transition.kind != generatedDomainGatewayCurrentV1 ||
			evidence.transition.currentV1 == nil ||
			!reflect.DeepEqual(*evidence.transition.currentV1, fixture.transition) ||
			evidence.protectedGateway.kind != generatedDomainGatewayCurrentV1 ||
			evidence.protectedGateway.currentV1 == nil ||
			!reflect.DeepEqual(*evidence.protectedGateway.currentV1, fixture.transition.Pending) ||
			evidence.protectedGateway.retainedV2 == nil ||
			!generatedDomainRetainedV2ProjectionValid(evidence.protectedGateway.retainedV2) ||
			!reflect.DeepEqual(evidence.protectedGateway.retainedV2, snapshot.retained) {
			t.Fatalf("Current Pending retained-V2 projection=%#v retained=%#v",
				evidence.protectedGateway, snapshot.retained)
		}
		beforeRetained := cloneGeneratedDomainRetainedV2Projection(*snapshot.retained)
		beforeRetained.current = cloneGatewayCurrentRouteState(fixture.transition.Before)
		effectiveRetained := cloneGeneratedDomainRetainedV2Projection(*snapshot.retained)
		effectiveRetained.current = cloneGatewayCurrentRouteState(fixture.transition.Effective)
		if evidence.routeGateway.retainedV2 == nil || evidence.servingGateway.retainedV2 == nil ||
			!reflect.DeepEqual(*evidence.routeGateway.retainedV2, beforeRetained) ||
			!reflect.DeepEqual(*evidence.servingGateway.retainedV2, effectiveRetained) ||
			!generatedDomainRetainedV2ProjectionValid(evidence.routeGateway.retainedV2) ||
			!generatedDomainRetainedV2ProjectionValid(evidence.servingGateway.retainedV2) {
			t.Fatalf("Current projected retained-V2 before=%#v effective=%#v",
				evidence.routeGateway.retainedV2, evidence.servingGateway.retainedV2)
		}
	})
}

func generatedDomainColdCanonicalCommittedV2Pending(t *testing.T,
	fixture generatedRoutePreviousCommittedV2ClearFixture,
) generatedRoutePreviousCommittedV2ClearFixture {
	t.Helper()
	transition := cloneGeneratedRouteActivationGatewayTransition(fixture.transition)
	if transition == nil || transition.nativeV2 == nil || transition.nativeV2.pending.Pending == nil {
		t.Fatal("committed V2 fixture has no Pending transition")
	}
	request := canonicalGeneratedRouteRequest(transition.request)
	pending := cloneGatewayV2RouteState(transition.nativeV2.pending)
	pending.Pending.Proposed.Route.Endpoints = append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)
	effective := cloneGatewayV2RouteState(transition.nativeV2.effective)
	app := effective.Apps[request.AppID]
	app.Route.Endpoints = append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)
	effective.Apps[request.AppID] = app
	transition.request = request
	transition.nativeV2.pending = cloneGatewayV2RouteState(pending)
	transition.nativeV2.effective = cloneGatewayV2RouteState(effective)
	if !validGeneratedRouteActivationGatewayTransition(transition) {
		t.Fatal("canonical committed V2 fixture produced an invalid transition")
	}
	fixture.pending, fixture.effective, fixture.transition = pending, effective, transition
	return fixture
}

func generatedDomainColdCommittedV2ProtectedState(t *testing.T,
	protected generatedDomainProtectedStartupSnapshot, state gatewayV2RouteState,
) generatedDomainProtectedStartupSnapshot {
	t.Helper()
	result := protected
	result.upgrade.state = cloneGatewayV2RouteState(state)
	result.upgrade.generations = append([]gatewayUpgradeGenerationSelection(nil), protected.upgrade.generations...)
	matched := 0
	for index := range result.upgrade.generations {
		candidate := result.upgrade.generations[index]
		candidate.State = cloneGatewayV2RouteState(candidate.State)
		if reflect.DeepEqual(candidate.State, protected.upgrade.state) {
			candidate.State = cloneGatewayV2RouteState(state)
			matched++
		}
		result.upgrade.generations[index] = candidate
	}
	if matched != 1 {
		t.Fatalf("committed V2 protected control matched %d active generations", matched)
	}
	return result
}

func generatedDomainColdAssertNoCommittedV2RoutePending(t *testing.T,
	database deployments.GeneratedDomainStartupSnapshot, protected generatedDomainProtectedStartupSnapshot,
	transition *generatedRouteActivationGatewayTransition, name string,
) {
	t.Helper()
	if candidate, accepted := generatedDomainCommittedUpgradeRoutePendingGatewayCandidate(database, protected); accepted {
		t.Fatalf("%s admitted route-Pending candidate %#v", name, candidate)
	}
	evidence, found, err := generatedDomainColdPendingActivation(database, protected,
		[]generatedDomainColdGroup{generatedDomainColdPreviousProtectedModeGroup(t, transition)})
	if err != nil || found || evidence.transition != nil {
		t.Fatalf("%s cold evidence=%#v found=%t error=%v", name, evidence, found, err)
	}
}

func generatedDomainColdPreviousProtectedModeEvidence(t *testing.T,
	database deployments.GeneratedDomainStartupSnapshot, protected generatedDomainProtectedStartupSnapshot,
	expected *generatedRouteActivationGatewayTransition,
) generatedDomainColdPendingActivationEvidence {
	t.Helper()
	group := generatedDomainColdPreviousProtectedModeGroup(t, expected)
	evidence, found, err := generatedDomainColdPendingActivation(database, protected,
		[]generatedDomainColdGroup{group})
	if err != nil || !found || evidence.transition == nil || !evidence.beforeActivation ||
		evidence.successorDeployment != group.pins.deploymentID ||
		!reflect.DeepEqual(evidence.transition, expected) {
		t.Fatalf("protected Pending evidence=%#v found=%t error=%v", evidence, found, err)
	}
	wantBefore, wantHadBefore, wantProposed, wantValid := generatedRouteActivationGatewayRoutes(expected)
	gotBefore, gotHadBefore, gotProposed, gotValid := generatedRouteActivationGatewayRoutes(evidence.transition)
	if !wantValid || !gotValid || !wantHadBefore || !gotHadBefore ||
		!sameRoute(gotBefore, wantBefore) || !sameRoute(gotProposed, wantProposed) ||
		!sameGeneratedRouteActivationGatewayRequest(evidence.transition.request, expected.request) {
		t.Fatalf("protected Pending routes before=%#v/%#v had=%t/%t proposed=%#v/%#v valid=%t/%t",
			gotBefore, wantBefore, gotHadBefore, wantHadBefore, gotProposed, wantProposed, gotValid, wantValid)
	}
	wantRoute, err := generatedDomainColdPendingGatewayProjection(evidence.protectedGateway, *expected, true)
	if err != nil {
		t.Fatal(err)
	}
	wantServing, err := generatedDomainColdPendingGatewayProjection(evidence.protectedGateway, *expected, false)
	if err != nil || !reflect.DeepEqual(evidence.routeGateway, wantRoute) ||
		!reflect.DeepEqual(evidence.servingGateway, wantServing) {
		t.Fatalf("protected Pending projections route=%#v serving=%#v error=%v",
			evidence.routeGateway, evidence.servingGateway, err)
	}
	cloned := cloneGeneratedRouteActivationGatewayTransition(evidence.transition)
	if cloned == nil || !reflect.DeepEqual(cloned, evidence.transition) || len(cloned.request.Endpoints) == 0 {
		t.Fatal("protected Pending transition did not deep-clone")
	}
	cloned.request.Endpoints[0].NetworkAlias += "-clone-control"
	switch cloned.kind {
	case generatedDomainGatewayNativeV2:
		if cloned.nativeV2 == nil || cloned.nativeV2.pending.Pending == nil ||
			len(cloned.nativeV2.pending.Pending.Proposed.Route.Endpoints) == 0 {
			t.Fatal("committed V2 transition clone omitted nested Pending")
		}
		cloned.nativeV2.pending.Pending.Proposed.Route.Endpoints[0].NetworkAlias += "-clone-control"
	case generatedDomainGatewayCurrentV1:
		if cloned.currentV1 == nil || cloned.currentV1.Pending.Pending == nil ||
			len(cloned.currentV1.Pending.Pending.Proposed.Route.Endpoints) == 0 {
			t.Fatal("Current transition clone omitted nested Pending")
		}
		cloned.currentV1.Pending.Pending.Proposed.Route.Endpoints[0].NetworkAlias += "-clone-control"
	default:
		t.Fatalf("unexpected protected Pending transition kind %d", cloned.kind)
	}
	if reflect.DeepEqual(cloned, evidence.transition) || !reflect.DeepEqual(evidence.transition, expected) {
		t.Fatal("protected Pending transition clone aliases classifier evidence")
	}
	return evidence
}

func generatedDomainColdPreviousCurrentV2Companion(t *testing.T,
	protected generatedDomainProtectedStartupSnapshot, transition gatewayCurrentPhysicalTransition,
) (generatedDomainProtectedStartupSnapshot, string) {
	t.Helper()
	before := cloneGatewayCurrentRouteState(transition.Before)
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind,
		Lineage: before.Lineage, State: &before}
	authority := gatewayCurrentV2PromotionAuthorityForTest(t, selection)
	state, err := gatewayCurrentV2BaselineFor(selection, authority)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := gatewayCurrentRouteStateNameV2(state.Lineage.ProtectedGeneration, state.Lineage.OperationID)
	protected.currentV2 = gatewayCurrentV2Census{files: map[string]gatewayCurrentV2CensusEntry{name: {
		Generation: state.Lineage.ProtectedGeneration, OperationID: state.Lineage.OperationID, State: state,
	}}}
	return protected, name
}

func generatedDomainColdPreviousProtectedModeGroup(t *testing.T,
	transition *generatedRouteActivationGatewayTransition,
) generatedDomainColdGroup {
	t.Helper()
	before, hadBefore, proposed, valid := generatedRouteActivationGatewayRoutes(transition)
	if !valid || !hadBefore || len(proposed.Endpoints) == 0 ||
		transition.request.AppID == "" || transition.request.FromSlot != before.Slot ||
		transition.request.ToSlot != proposed.Slot {
		t.Fatalf("protected Pending transition is not a replacement: %#v", transition)
	}
	deploymentID, releaseID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	predecessorID, predecessorReleaseID := uuid.NewString(), uuid.NewString()
	components := make([]candidateAdmissionComponent, 0, len(proposed.Endpoints))
	policies := make(map[string]candidatePolicy, len(proposed.Endpoints))
	for _, endpoint := range proposed.Endpoints {
		components = append(components, candidateAdmissionComponent{
			plan: deploymentplans.Component{Name: endpoint.Component, Role: endpoint.Role,
				InternalPort: endpoint.InternalPort},
			state: generatedruntimestate.ComponentHealthy, containerID: endpoint.ContainerID,
		})
		policies[endpoint.Component] = candidatePolicy{identity: generatedcontainer.Identity{
			AppID: transition.request.AppID, DeploymentID: deploymentID,
			Component: endpoint.Component, Role: endpoint.Role,
			NetworkName: endpoint.NetworkName, NetworkAlias: endpoint.NetworkAlias,
		}}
	}
	group := generatedDomainColdGroup{pins: candidateValidationPins{
		identity: candidateValidationIdentity{applicationID: transition.request.AppID,
			jobID: jobID, attempt: 1},
		deploymentID: deploymentID, releaseID: releaseID, candidateSlot: string(proposed.Slot),
	}, snapshot: candidateAdmissionSnapshot{
		pins: candidateImmutablePins{jobID: jobID, releaseID: releaseID,
			candidateSlot: string(proposed.Slot), previousDeploymentID: predecessorID,
			previousSlot: string(before.Slot)},
		headPins: candidateHeadPins{appID: transition.request.AppID, deploymentID: deploymentID,
			releaseID: releaseID, slot: string(proposed.Slot), previousDeploymentID: predecessorID,
			previousReleaseID: predecessorReleaseID, previousSlot: string(before.Slot)},
		observedHead: candidateHeadSnapshot{appID: transition.request.AppID,
			deploymentID: predecessorID, releaseID: predecessorReleaseID,
			slot: string(before.Slot), generation: 1, activationReceiptID: uuid.NewString()},
		runtime:    candidateRuntimeSnapshot{phase: generatedruntimestate.PhaseSwitchingRoute},
		components: components,
	}, kind: generatedDomainColdRisky, policies: policies, bindHead: true}
	if route, ok := generatedDomainColdGroupRoute(group); !ok || !sameRoute(route, proposed) {
		t.Fatalf("protected Pending cloned group route=%#v valid=%t want=%#v", route, ok, proposed)
	}
	return group
}

func runGeneratedColdBoundaryWorker(t *testing.T, service *jobs.Service, executor jobs.Executor, id string, status jobs.Status, workingDirectory string, diagnostic func() string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("worker stopped: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("worker did not stop")
			}
		})
	}
	t.Cleanup(stop)
	admission := func(parent context.Context) (context.Context, func() error, error) {
		scope, release, err := deploymenteffects.AcquireScoped(parent, workingDirectory)
		if err != nil {
			return nil, nil, err
		}
		return deploymenteffects.WithScope(parent, scope), release, nil
	}
	go func() { done <- service.RunWorkerWithScopedAdmission(ctx, executor, admission) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		job, err := service.Get(id)
		if err == nil && job.Status == string(status) {
			stop()
			return
		}
		select {
		case <-deadline.C:
			detail := ""
			if diagnostic != nil {
				detail = diagnostic()
			}
			t.Fatalf("job %s did not reach %s: status=%s phase=%s code=%s attempt=%d getErr=%v diagnostic=%s", id, status,
				job.Status, job.Phase, job.ErrorCode, job.Attempt, err, detail)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func boundedErrorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	value := err.Error()
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func generatedColdBoundarySnapshotSummary(t *testing.T, identity generatedDomainIdentityFixture) string {
	t.Helper()
	snapshot, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		return "snapshot_error=" + boundedErrorText(err)
	}
	result := fmt.Sprintf("deployments=%d runtimes=%d components=%d intents=%d artifacts=%d heads=%d activations=%d",
		len(snapshot.GeneratedDeployments), len(snapshot.Runtime.Deployments), len(snapshot.Runtime.Components),
		len(snapshot.Runtime.CandidateIntents), len(snapshot.Artifacts), len(snapshot.Runtime.ActiveHeads), len(snapshot.Runtime.RouteActivations))
	for _, deployment := range snapshot.GeneratedDeployments {
		result += fmt.Sprintf(" main[%s]=%s/job:%s/release:%s/plan:%s:%d/config:%s:%d/init:%t", deployment.ID,
			deployment.Status, deployment.JobID, deployment.ReleaseID, deployment.DeploymentPlanRevisionID,
			deployment.DeploymentPlanRevisionNumber, deployment.ActualConfigurationRevisionID,
			deployment.ActualConfigurationRevisionNumber, deployment.ProvenanceInitialized)
	}
	for _, runtime := range snapshot.Runtime.Deployments {
		result += fmt.Sprintf(" runtime[%s]=%s/migration:%s/slot:%s/previous:%s:%s", runtime.DeploymentID,
			runtime.Phase, runtime.MigrationState, runtime.CandidateSlot, runtime.PreviousActiveDeploymentID, runtime.PreviousActiveSlot)
	}
	if len(result) > 2048 {
		return result[:2048]
	}
	return result
}

func generatedColdBoundaryStageTrace(t *testing.T, identity generatedDomainIdentityFixture, runner *generatedDomainConstructionRunner, store *stateStore) string {
	t.Helper()
	ctx := context.Background()
	observed, err := inspectGeneratedDomainStartupObservation(ctx, identity.stores, runner, identity.options)
	if err != nil {
		return "inspect=" + boundedErrorText(err)
	}
	prefix := fmt.Sprintf("startup=%d/reason:%d/recovery:%d", observed.classification.disposition, observed.classification.reason, len(observed.classification.recovery))
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database, stores: identity.stores, runner: runner, options: identity.options}, store)
	if err != nil {
		return prefix + "/manager=" + boundedErrorText(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, identity.stores, runner, identity.options)
	if err != nil {
		return prefix + "/physical=" + boundedErrorText(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		return prefix + "/view=" + boundedErrorText(err)
	}
	budget, err := generatedcapacity.New(manager)
	if err != nil {
		return prefix + "/budget=" + boundedErrorText(err)
	}
	authority, err := newGeneratedCandidateAuthorityConcrete(identity.stores, runner, &candidateFactoryEnvironment{path: t.TempDir()}, budget, generatedDomainCandidateOptions(identity.options))
	if err != nil {
		return prefix + "/authority=" + boundedErrorText(err)
	}
	groups, err := readGeneratedDomainColdGroups(ctx, authority, observed)
	if err != nil {
		return prefix + "/groups=" + boundedErrorText(err)
	}
	groupSummary := ""
	for _, group := range groups {
		groupSummary += fmt.Sprintf("[%s kind=%d head=%t self=%t legacy=%t components=%d]", group.pins.deploymentID, group.kind, group.bindHead, group.selfHead, group.legacyHead, len(group.snapshot.components))
	}
	if err := attestGeneratedDomainColdGroups(ctx, manager, authority, observed, groups); err != nil {
		return prefix + "/groups=ok" + groupSummary + "/attest=" + boundedErrorText(err)
	}
	return prefix + "/groups=ok" + groupSummary + "/attest=ok"
}

func generatedColdBoundaryCapacityProbes(commands [][]string) int {
	result := 0
	for _, command := range commands {
		if len(command) >= 6 && command[0] == "container" && command[1] == "exec" && command[len(command)-1] == capacityProbeCommand {
			result++
		}
	}
	return result
}

func generatedColdBoundaryPublicOptions(options generatedDomainOptions) GeneratedDomainOptions {
	return GeneratedDomainOptions{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint, DockerConfigDirectory: options.DockerConfigDirectory,
		WorkingDirectory: options.WorkingDirectory, DataRoot: options.DataRoot, HostPort: options.HostPort, CommandTimeout: options.CommandTimeout,
		PullTimeout: options.PullTimeout, HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		MemoryBytes: options.MemoryBytes, MilliCPUs: options.MilliCPUs, PIDs: options.PIDs, TmpfsBytes: options.TmpfsBytes,
		LogSize: options.LogSize, LogFiles: options.LogFiles, ReplacementDiskBytes: options.ReplacementDiskBytes,
	}
}
