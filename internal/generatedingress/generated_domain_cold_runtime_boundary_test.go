//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
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
			routeSeed := &generatedDomainRouteSeedRunner{daemonID: "generated-cold-runtime-boundary", delegate: &ingressRunner{
				appID: migration.guarded.appID, network: predecessor.NetworkName, endpoint: generatedRouteEndpoint(predecessor),
				caddyNetworks:     map[string]*networkAttachment{caddyNetworkName: {GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
				ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {Name: caddyContainerName, IPv4Address: ingressIP + "/28"}},
				files:             map[string][]byte{},
			}}
			manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database, stores: identity.stores, runner: routeSeed, options: identity.options}, store)
			if err != nil {
				t.Fatal(err)
			}
			physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores, routeSeed, identity.options)
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
			CreatorJobID: jobID, CreatorWorkerAttempt: 1,
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
