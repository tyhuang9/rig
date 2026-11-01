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

// generatedColdCreatedCandidateStartRunner preserves one immutable process
// dependency across construction and recovery. Construction sees only the
// established read/census runner. A repaired cold manifest may later enable
// only explicitly frozen candidate argv for the revalidated continuation.
type generatedColdCreatedCandidateStartRunner struct {
	mu          sync.RWMutex
	delegate    runtimeprocess.CommandRunner
	candidate   runtimeprocess.CommandRunner
	containerID string
	allowed     map[string]struct{}
}

func (r *generatedColdCreatedCandidateStartRunner) enableStart() {
	r.allow([]string{"container", "start", r.containerID})
}

func (r *generatedColdCreatedCandidateStartRunner) allow(args []string) {
	r.mu.Lock()
	if r.allowed == nil {
		r.allowed = make(map[string]struct{})
	}
	r.allowed[strings.Join(args, "\x00")] = struct{}{}
	r.mu.Unlock()
}

func (r *generatedColdCreatedCandidateStartRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.RLock()
	_, allowed := r.allowed[strings.Join(request.Args, "\x00")]
	candidate, delegate := r.candidate, r.delegate
	r.mu.RUnlock()
	if allowed && candidate != nil {
		return candidate.Run(ctx, request)
	}
	return delegate.Run(ctx, request)
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
	t.Run("initial_created_candidate_adopts_same_identity_and_starts_once_without_second_capacity_charge", func(t *testing.T) {
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
			created := validation.read(t)
			physicalCreated := candidate.runner.snapshot()
			expectedIdentity := physicalCreated.policy.identity
			expectedIdentity.ContainerID = candidateIdentity.ContainerID
			if created.runtime.phase != generatedruntimestate.PhaseStartingCandidate ||
				len(created.components) != 1 || created.components[0].state != generatedruntimestate.ComponentRunning ||
				created.components[0].receipt == nil || created.components[0].receipt.id != candidateIdentity.ReceiptID ||
				created.components[0].containerID != candidateIdentity.ContainerID ||
				created.components[0].containerName != candidateIdentity.ContainerName ||
				!physicalCreated.exists || physicalCreated.containerID != candidateIdentity.ContainerID ||
				!generatedcontainer.MatchesConfigured(candidateOwnerPhysicalInspection(physicalCreated), expectedIdentity, physicalCreated.policy.limits) ||
				!generatedcontainer.NeverStarted(candidateOwnerPhysicalInspection(physicalCreated)) ||
				candidate.runner.count(candidateOwnerCreate) != 1 || candidate.runner.count(candidateOwnerStart) != 0 ||
				candidate.runner.count(candidateOwnerStop) != 0 || candidate.runner.count(candidateOwnerRemove) != 0 {
				t.Fatalf("durable never-started candidate: admission=%#v identity=%#v physical=%#v create=%d start=%d stop=%d remove=%d",
					created, candidateIdentity, physicalCreated, candidate.runner.count(candidateOwnerCreate),
					candidate.runner.count(candidateOwnerStart), candidate.runner.count(candidateOwnerStop),
					candidate.runner.count(candidateOwnerRemove))
			}

			// The interrupted worker owns no scope when cold construction begins.
			execution.Leave()
			reservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database,
				time.Now().UTC(), migration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover created-candidate job: result=%+v err=%v", recovered, err)
			}
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
						PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
				}), migration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)

			store, err := newStateStore(identity.dataRoot)
			if err != nil || store.createInitialEmpty() != nil {
				t.Fatalf("install empty gateway state: %v", err)
			}
			state, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Version != stateVersion || len(state.Active) != 0 || state.Pending != nil {
				t.Fatalf("created-candidate gateway baseline=%#v", state)
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
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
						scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
				case caddyVolumeName:
					objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
						scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
						mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
				}
			}
			policy := physicalCreated.policy
			objects = append(objects,
				generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID, labels: map[string]string{
					"io.rig.managed": "generated-image", "io.rig.application": policy.identity.AppID,
					"io.rig.release": policy.identity.ReleaseID, "io.rig.artifact": policy.identity.ArtifactID,
					"io.rig.plan": policy.identity.DeploymentPlanRevisionID, "io.rig.component": policy.identity.Component,
					"io.rig.role": policy.identity.Role, "io.rig.definition": policy.buildDefinitionDigest,
				}, data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
				generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64), name: policy.identity.NetworkName,
					labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID},
					data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
				generatedDomainDockerObject{kind: "container", id: candidateIdentity.ContainerID,
					name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)},
			)
			identity.options.HostPort = journal.Source.LocalHostPort
			runner := &generatedDomainConstructionRunner{candidate: candidate.runner, delegate: &generatedDomainRecoveryCensusRunner{
				daemonID: "generated-cold-runtime-initial-created-candidate", objects: objects,
				imageID: observation.Image.ID, gateway: gateway,
			}}
			process := &generatedColdCreatedCandidateStartRunner{delegate: runner, candidate: candidate.runner,
				containerID: candidateIdentity.ContainerID}
			priorManager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
				database: identity.database, stores: identity.stores, runner: process, options: identity.options,
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
			startupBefore, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(startupBefore.Runtime.ActiveHeads) != 0 || len(startupBefore.Runtime.RouteActivations) != 0 {
				t.Fatalf("created-candidate baseline is not the canonical empty head: heads=%#v activations=%#v",
					startupBefore.Runtime.ActiveHeads, startupBefore.Runtime.RouteActivations)
			}
			admissionBefore := validation.read(t)
			gatewayBefore, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			physicalBefore := candidate.runner.snapshot()
			createBefore, startBefore := candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart)
			stopBefore, removeBefore := candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove)

			domain, constructErr := NewGeneratedDomain(context.Background(), identity.stores, process,
				&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			startupAfter, snapshotErr := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			gatewayAfter, stateErr := store.load()
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			admissionAfter := validation.read(t)
			physicalAfter := candidate.runner.snapshot()
			if !reflect.DeepEqual(startupBefore, startupAfter) || !reflect.DeepEqual(gatewayBefore, gatewayAfter) ||
				!reflect.DeepEqual(admissionBefore, admissionAfter) || !reflect.DeepEqual(physicalBefore, physicalAfter) ||
				candidate.runner.count(candidateOwnerCreate) != createBefore || candidate.runner.count(candidateOwnerStart) != startBefore ||
				candidate.runner.count(candidateOwnerStop) != stopBefore || candidate.runner.count(candidateOwnerRemove) != removeBefore {
				t.Fatalf("cold constructor changed created-candidate prefix: startup=%t gateway=%t admission=%t physical=%t commands=%d/%d/%d/%d want=%d/%d/%d/%d",
					reflect.DeepEqual(startupBefore, startupAfter), reflect.DeepEqual(gatewayBefore, gatewayAfter),
					reflect.DeepEqual(admissionBefore, admissionAfter), reflect.DeepEqual(physicalBefore, physicalAfter),
					candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart),
					candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove),
					createBefore, startBefore, stopBefore, removeBefore)
			}
			if constructErr != nil {
				message := "generated domain construction was refused: stage=cold_construction reason=cold_group_container_attestation"
				assertGeneratedDomainConstructionDiagnostic(t, constructErr,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerAttestation, message)
				if domain != nil || generatedColdBoundaryCapacityProbes(runner.snapshot()) != 0 {
					t.Fatalf("refused created-candidate construction published domain=%v or probed capacity: commands=%#v",
						domain, runner.snapshot())
				}
				t.Fatalf("created configured NeverStarted candidate must reconstruct and remain startable; current base refused at the exact cold container boundary: %v", constructErr)
			}
			if domain == nil || domain.construction == nil || domain.construction.cold == nil ||
				len(domain.construction.cold.groups) != 1 {
				t.Fatalf("created-candidate cold domain=%#v", domain)
			}
			group := domain.construction.cold.groups[0]
			if group.deploymentID != migration.request.DeploymentID || len(group.components) != 1 ||
				group.components[0].name != "api" || group.components[0].receiptID != candidateIdentity.ReceiptID ||
				!group.components[0].charged || !group.components[0].retained {
				t.Fatalf("created-candidate cold group=%#v", group)
			}
			if probes := generatedColdBoundaryCapacityProbes(runner.snapshot()); probes != 0 {
				t.Fatalf("created-candidate cold capacity probes=%d want 0 commands=%#v", probes, runner.snapshot())
			}

			ledger := domain.Candidates().ledger
			ledger.mu.Lock()
			capacityGroup := ledger.groups[candidateCapacityGroupKey{appID: migration.guarded.appID,
				deploymentID: migration.request.DeploymentID}]
			ledger.mu.Unlock()
			if capacityGroup == nil {
				t.Fatal("created-candidate cold construction omitted exact accounting group")
			}
			capacityGroup.mu.Lock()
			component := capacityGroup.byName["api"]
			var retainedCharge *candidateCapacityCharge
			var retainedDebt *generatedDomainCapacityDebtToken
			if component != nil && component.charge != nil {
				retainedDebt, _ = component.charge.token.(*generatedDomainCapacityDebtToken)
			}
			validAccounting := component != nil && component.receipt != nil && component.receipt.id == candidateIdentity.ReceiptID &&
				candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalPresent &&
				component.charge != nil && component.charge.state == candidateChargeActive &&
				component.charge.owner == candidateAccountingCandidate && component.charge.receiptRetained &&
				!component.charge.explicitRetained && !component.charge.logicallyRetired && component.charge.refs == 0 &&
				component.charge.lease == nil && retainedDebt != nil && retainedDebt.active &&
				retainedDebt.limits == capacityGroup.limits && !component.tokenHeld
			if component != nil {
				retainedCharge = component.charge
			}
			capacityGroup.mu.Unlock()
			if !validAccounting || !candidateCapacityChargeTokenPublished(retainedDebt) {
				t.Fatalf("created-candidate cold accounting component=%#v", component)
			}
			coordinator := domain.construction.capacity
			coordinator.mu.Lock()
			validCoordinator := coordinator.phase == generatedDomainCapacityPublished && coordinator.borrows == 0 &&
				coordinator.memory == capacityGroup.limits.MemoryBytes && coordinator.disk == capacityGroup.limits.DiskBytes &&
				len(coordinator.tokens) == 1 && retainedDebt != nil && retainedDebt.owner == coordinator
			_, debtRegistered := coordinator.tokens[retainedDebt]
			coordinatorPhase, coordinatorBorrows := coordinator.phase, coordinator.borrows
			coordinatorMemory, coordinatorDisk, coordinatorTokens := coordinator.memory, coordinator.disk, len(coordinator.tokens)
			coordinator.mu.Unlock()
			if !validCoordinator || !debtRegistered {
				t.Fatalf("created-candidate cold coordinator phase=%d borrows=%d memory=%d disk=%d tokens=%d",
					coordinatorPhase, coordinatorBorrows, coordinatorMemory, coordinatorDisk, coordinatorTokens)
			}
			process.enableStart()

			resumed, err := migration.jobService.Resume(migration.claim.JobID())
			if err != nil || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume created-candidate job=%+v err=%v", resumed, err)
			}
			var workerErr error
			runGeneratedColdBoundaryWorker(t, migration.jobService,
				generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					claim, err := deploymentexecution.NewClaim(migration.binding, job.ID, migration.guarded.appID, job.Attempt)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					request := GeneratedReplacementRequest(validation.request)
					request.Claim = claim
					freshReservation, err := domain.Candidates().ReserveReplacement(ctx, request)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshReservation.Release()
					freshExecution, err := domain.Candidates().PinCandidates(ctx, GeneratedCandidateRequest(request), freshReservation)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshExecution.Leave()
					adopted, err := freshExecution.AdoptCandidate("api")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					permit, ok := adopted.(*generatedCandidateInvocationPermit)
					if !ok || permit == nil || permit.containerID != candidateIdentity.ContainerID ||
						adopted.Identity().ReceiptID != candidateIdentity.ReceiptID {
						workerErr = fmt.Errorf("adopted created candidate = %T %#v", adopted, adopted.Identity())
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.StartCandidate(adopted); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					return jobs.ExecutionResult{}, nil
				}), migration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory,
				func() string { return boundedErrorText(workerErr) })
			if workerErr != nil {
				t.Fatalf("fresh created-candidate worker: %v", workerErr)
			}
			physicalRunning := candidate.runner.snapshot()
			if physicalRunning.containerID != candidateIdentity.ContainerID ||
				!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(physicalRunning), expectedIdentity, policy.limits) ||
				!generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(physicalRunning)) ||
				candidate.runner.count(candidateOwnerCreate) != 1 || candidate.runner.count(candidateOwnerStart) != 1 ||
				candidate.runner.count(candidateOwnerStop) != 0 || candidate.runner.count(candidateOwnerRemove) != 0 ||
				generatedColdBoundaryCapacityProbes(runner.snapshot()) != 0 {
				t.Fatalf("fresh worker did not start exact created candidate once: physical=%#v create=%d start=%d stop=%d remove=%d probes=%d",
					physicalRunning, candidate.runner.count(candidateOwnerCreate), candidate.runner.count(candidateOwnerStart),
					candidate.runner.count(candidateOwnerStop), candidate.runner.count(candidateOwnerRemove),
					generatedColdBoundaryCapacityProbes(runner.snapshot()))
			}
			capacityGroup.mu.Lock()
			component = capacityGroup.byName["api"]
			retainedAfterStart := component != nil && component.charge == retainedCharge && component.charge != nil &&
				component.charge.state == candidateChargeActive && component.charge.owner == candidateAccountingCandidate &&
				component.charge.receiptRetained && component.charge.refs == 0 && component.charge.lease == nil &&
				component.charge.token == retainedDebt && component.receipt != nil &&
				candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalPresent
			capacityGroup.mu.Unlock()
			if !retainedAfterStart || !candidateCapacityChargeTokenPublished(retainedDebt) {
				t.Fatalf("fresh worker replaced or released created-candidate accounting: %#v", component)
			}
		})
	})
	t.Run("replacement_sequential_created_prefix_recovers_without_new_capacity_or_forged_provenance", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			components := []string{"api", "frontend"}
			identity := newGeneratedDomainIdentityFixture(t)
			migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, components, components,
				generatedDomainTwoComponentPlan())
			identity.stores = migration.repository.stores
			validation := newGeneratedRouteSuccessorValidation(t, migration)
			owner, candidateMux := newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t, validation,
				generatedDomainCandidateOptions(identity.options), 4)
			predecessorReservation, predecessorExecution, predecessorIdentities :=
				prepareGeneratedRouteSequentialCandidates(t, owner, components,
					[]string{strings.Repeat("7", 64), strings.Repeat("8", 64)})
			predecessorReleased := false
			var predecessorRetirement *GeneratedPredecessorRetirement
			defer func() {
				if predecessorReleased {
					return
				}
				if predecessorRetirement != nil {
					predecessorRetirement.Leave()
				}
				predecessorExecution.Leave()
				predecessorReservation.Release()
				_ = migration.releaseEffects()
			}()
			predecessorByName := generatedRouteColdAccountingIdentityMap(t, components, predecessorIdentities)

			store, err := newStateStore(identity.dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			_, _, ingressIP := ingressNetworkCandidate(0)
			ingress := &ingressRunner{appID: migration.guarded.appID,
				network: predecessorByName["api"].NetworkName, endpoint: generatedRouteEndpoint(predecessorByName["api"]),
				caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
					GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
				ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
					Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
			physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
				slot:      generatedidentity.Slot(predecessorByName["api"].Slot),
				endpoints: make(map[string]generatedRouteEndpointSnapshot)}
			predecessorEndpoints := make([]generatedroute.RouteEndpoint, 0, len(components))
			for _, component := range components {
				value := predecessorByName[component]
				endpoint := generatedRouteEndpoint(value)
				predecessorEndpoints = append(predecessorEndpoints, endpoint)
				physicalRoutes.endpoints[value.ContainerID] = generatedRouteEndpointSnapshot{
					appID: migration.guarded.appID, endpoint: endpoint, slot: generatedidentity.Slot(value.Slot)}
			}
			routeRunner := &generatedDomainTwoComponentRouteRunner{
				daemonID: "generated-cold-runtime-created-prefix-seed", delegate: physicalRoutes}
			routeReader := &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes, delegate: routeRunner}
			manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
				database: identity.database, stores: identity.stores, runner: routeReader, options: identity.options,
			}, store)
			if err != nil {
				t.Fatal(err)
			}
			physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores,
				routeReader, identity.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
				t.Fatal(err)
			}
			routes, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
			if err != nil {
				t.Fatal(err)
			}
			predecessorSealed, err := routes.Pin(migration.guarded.context, predecessorExecution,
				generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
					ToSlot: generatedidentity.Slot(predecessorByName["api"].Slot), Endpoints: predecessorEndpoints})
			if err != nil {
				t.Fatal(err)
			}
			activated, predecessorRetirement, err := predecessorSealed.ActivateWithPredecessorRetirement(
				migration.guarded.context)
			if err != nil || predecessorRetirement == nil || activated.Phase != generatedruntimestate.PhaseDraining {
				t.Fatalf("activate served two-component predecessor state=%#v retirement=%#v error=%v",
					activated, predecessorRetirement, err)
			}
			if err := predecessorRetirement.Complete(context.Background()); err != nil {
				t.Fatalf("complete served two-component predecessor: %v", err)
			}
			predecessorHead, err := generatedruntimestate.New(identity.database).Active(context.Background(),
				migration.guarded.appID)
			if err != nil || predecessorHead.DeploymentID != migration.request.DeploymentID ||
				predecessorHead.ActivationReceiptID == "" {
				t.Fatalf("served two-component predecessor head=%#v error=%v", predecessorHead, err)
			}
			runtimeState := generatedruntimestate.New(identity.database)
			predecessorJob, err := migration.jobService.Get(migration.claim.JobID())
			if err != nil || predecessorJob.Status != string(jobs.Succeeded) {
				t.Fatalf("served predecessor completion job=%#v error=%v", predecessorJob, err)
			}
			predecessorRetirement.Leave()
			predecessorExecution.Leave()
			predecessorReservation.Release()
			if err := migration.releaseEffects(); err != nil {
				t.Fatal(err)
			}
			predecessorReleased = true

			successorScope, releaseSuccessor, err := deploymenteffects.AcquireScoped(context.Background(),
				identity.options.WorkingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			successorEffectsReleased := false
			defer func() {
				if !successorEffectsReleased {
					_ = releaseSuccessor()
				}
			}()
			t.Cleanup(func() {
				if err := releaseSuccessor(); err != nil {
					t.Errorf("release sequential-prefix successor effects: %v", err)
				}
			})
			restartedGuarded := *migration.guarded
			restartedGuarded.context = deploymenteffects.WithScope(context.Background(), successorScope)
			restartedMigration := *migration
			restartedMigration.guarded = &restartedGuarded
			restartedMigration.releaseEffects = releaseSuccessor
			successorMigration := newGeneratedRouteColdAccountingMultiSuccessorMigration(t,
				&restartedMigration, components)
			successorValidation := newGeneratedRouteSuccessorValidation(t, successorMigration)
			owner.validation = successorValidation
			successorReservation := owner.reserve(t)
			_, originalCapacityCalls := owner.source.observed()
			successorExecution := owner.pin(t, successorReservation)
			successorReleased := false
			defer func() {
				if !successorReleased {
					successorExecution.Leave()
					successorReservation.Release()
				}
			}()
			apiInvocation, err := successorExecution.BeginCreate("api")
			if err != nil {
				t.Fatal(err)
			}
			owner.runner.containerID = strings.Repeat("9", 64)
			owner.bindInvocation(t, apiInvocation)
			apiIdentity, err := successorExecution.CreateAndRecord(apiInvocation)
			if err != nil {
				t.Fatal(err)
			}
			if apiIdentity.ComponentName != "api" || apiIdentity.ContainerID != strings.Repeat("9", 64) ||
				apiIdentity.ReceiptID == "" {
				t.Fatalf("created api prefix identity=%#v", apiIdentity)
			}
			successorAdmission := successorValidation.read(t)
			if successorAdmission.runtime.phase != generatedruntimestate.PhaseStartingCandidate ||
				len(successorAdmission.components) != 2 ||
				successorAdmission.components[0].plan.Name != "api" ||
				successorAdmission.components[0].state != generatedruntimestate.ComponentRunning ||
				successorAdmission.components[0].receipt == nil ||
				successorAdmission.components[0].receipt.id != apiIdentity.ReceiptID ||
				successorAdmission.components[0].containerID != apiIdentity.ContainerID ||
				successorAdmission.components[1].plan.Name != "frontend" ||
				successorAdmission.components[1].state != generatedruntimestate.ComponentImageReady ||
				successorAdmission.components[1].receipt != nil ||
				successorAdmission.components[1].containerName != "" ||
				successorAdmission.components[1].containerID != "" {
				t.Fatalf("durable sequential created prefix=%#v", successorAdmission)
			}
			apiPhysical := candidateMux.physical[apiIdentity.ContainerID]
			apiExpected := apiPhysical.policy.identity
			apiExpected.ContainerID = apiIdentity.ContainerID
			if !apiPhysical.exists || apiPhysical.running ||
				!generatedcontainer.MatchesConfigured(candidateOwnerPhysicalInspection(apiPhysical),
					apiExpected, apiPhysical.policy.limits) ||
				!generatedcontainer.NeverStarted(candidateOwnerPhysicalInspection(apiPhysical)) {
				t.Fatalf("created api physical prefix=%#v", apiPhysical)
			}
			for _, physical := range candidateMux.physical {
				if physical.policy.identity.DeploymentID == successorMigration.request.DeploymentID &&
					physical.policy.identity.Component == "frontend" {
					t.Fatalf("frontend unexpectedly existed in created prefix: %#v", physical)
				}
			}
			var apiCreatorAttempt, successorIntentCount int
			if err := identity.database.QueryRow(`SELECT creator_worker_attempt FROM generated_candidate_intents WHERE id=?`,
				apiIdentity.ReceiptID).Scan(&apiCreatorAttempt); err != nil {
				t.Fatal(err)
			}
			if err := identity.database.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`,
				successorMigration.request.DeploymentID).Scan(&successorIntentCount); err != nil {
				t.Fatal(err)
			}
			if apiCreatorAttempt != successorMigration.claim.Attempt() || successorIntentCount != 1 {
				t.Fatalf("created prefix provenance attempt=%d want=%d intents=%d",
					apiCreatorAttempt, successorMigration.claim.Attempt(), successorIntentCount)
			}

			successorExecution.Leave()
			successorReservation.Release()
			successorReleased = true
			if err := releaseSuccessor(); err != nil {
				t.Fatal(err)
			}
			successorEffectsReleased = true
			recovered, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database,
				time.Now().UTC(), successorMigration.bindings)
			if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
				t.Fatalf("recover sequential created-prefix job: result=%+v err=%v", recovered, err)
			}
			var pausedAttempt int
			runGeneratedColdBoundaryWorker(t, successorMigration.jobService,
				generatedColdBoundaryExecutor(func(_ context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					pausedAttempt = job.Attempt
					return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
						PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
				}), successorMigration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)
			if pausedAttempt <= apiCreatorAttempt {
				t.Fatalf("recovered created-prefix attempt=%d original=%d", pausedAttempt, apiCreatorAttempt)
			}

			stateBefore, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			activeBefore, found := stateBefore.Active[migration.guarded.appID]
			predecessorRoute := routeRecord{Slot: generatedidentity.Slot(predecessorByName["api"].Slot),
				Endpoints: predecessorEndpoints}
			if stateBefore.Pending != nil || !found || !sameRoute(activeBefore, predecessorRoute) {
				t.Fatalf("served predecessor route before sequential-prefix recovery=%#v", stateBefore)
			}
			startupBefore, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(startupBefore.Runtime.ActiveHeads) != 1 || len(startupBefore.Runtime.RouteActivations) != 1 {
				t.Fatalf("sequential-prefix baseline head/history=%#v/%#v",
					startupBefore.Runtime.ActiveHeads, startupBefore.Runtime.RouteActivations)
			}
			for _, activation := range startupBefore.Runtime.RouteActivations {
				if activation.SuccessorDeploymentID == successorMigration.request.DeploymentID {
					t.Fatalf("sequential created prefix already had successor activation=%#v", activation)
				}
			}
			var successorLaterReceipts int
			if err := identity.database.QueryRow(`SELECT
				(SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts r JOIN generated_route_activations a ON a.id=r.activation_receipt_id WHERE a.successor_deployment_id=?) +
				(SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts h JOIN generated_route_activations a ON a.id=h.activation_receipt_id WHERE a.successor_deployment_id=?) +
				(SELECT COUNT(*) FROM generated_route_completion_receipts c JOIN generated_route_activations a ON a.id=c.activation_receipt_id WHERE a.successor_deployment_id=?)`,
				successorMigration.request.DeploymentID, successorMigration.request.DeploymentID,
				successorMigration.request.DeploymentID).Scan(&successorLaterReceipts); err != nil {
				t.Fatal(err)
			}
			if successorLaterReceipts != 0 {
				t.Fatalf("sequential created prefix had 043-045 rows=%d", successorLaterReceipts)
			}
			predecessorPhysical := generatedRouteColdAccountingPhysicalRoster(candidateMux, predecessorIdentities)
			processRunner, construction := generatedRouteColdAccountingPartialRestartProcess(t, identity, store,
				physicalRoutes, candidateMux, append(append([]CandidateIdentity(nil), predecessorIdentities...), apiIdentity))
			process := &generatedColdCreatedCandidateStartRunner{delegate: processRunner, candidate: candidateMux,
				containerID: apiIdentity.ContainerID}
			census := construction.delegate
			if census == nil {
				t.Fatalf("sequential created-prefix census=%T", construction.delegate)
			}
			var successorAPIContainers, successorFrontendContainers int
			for _, object := range census.objects {
				if object.kind != "container" || object.labels["io.rig.deployment"] != successorMigration.request.DeploymentID {
					continue
				}
				switch object.labels["io.rig.component"] {
				case "api":
					successorAPIContainers++
				case "frontend":
					successorFrontendContainers++
				}
			}
			if successorAPIContainers != 1 || successorFrontendContainers != 0 {
				t.Fatalf("sequential created-prefix census api=%d frontend=%d",
					successorAPIContainers, successorFrontendContainers)
			}
			admissionBefore := successorValidation.read(t)
			apiPhysicalBefore := candidateMux.physical[apiIdentity.ContainerID]
			liveBefore := append([]byte(nil), ingress.liveConfig...)
			restartBefore := append([]byte(nil), ingress.files[caddyContainerName+":/config/active.json"]...)
			if len(liveBefore) == 0 || !bytes.Equal(liveBefore, restartBefore) {
				t.Fatal("served two-component predecessor lacked exact live/restart route")
			}
			createBefore := owner.runner.count(candidateOwnerCreate)
			startBefore := owner.runner.count(candidateOwnerStart)
			stopBefore := owner.runner.count(candidateOwnerStop)
			removeBefore := owner.runner.count(candidateOwnerRemove)

			domain, constructErr := NewGeneratedDomain(context.Background(), identity.stores, process,
				&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(identity.options))
			startupAfter, snapshotErr := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			stateAfter, stateErr := store.load()
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			admissionAfter := successorValidation.read(t)
			apiPhysicalAfter := candidateMux.physical[apiIdentity.ContainerID]
			activeHeadAfter, headErr := runtimeState.Active(context.Background(), migration.guarded.appID)
			if headErr != nil || !reflect.DeepEqual(activeHeadAfter, predecessorHead) ||
				!reflect.DeepEqual(startupBefore, startupAfter) || !reflect.DeepEqual(stateBefore, stateAfter) ||
				!reflect.DeepEqual(admissionBefore, admissionAfter) ||
				!reflect.DeepEqual(apiPhysicalBefore, apiPhysicalAfter) ||
				!reflect.DeepEqual(predecessorPhysical,
					generatedRouteColdAccountingPhysicalRoster(candidateMux, predecessorIdentities)) ||
				!bytes.Equal(liveBefore, ingress.liveConfig) ||
				!bytes.Equal(restartBefore, ingress.files[caddyContainerName+":/config/active.json"]) ||
				owner.runner.count(candidateOwnerCreate) != createBefore ||
				owner.runner.count(candidateOwnerStart) != startBefore ||
				owner.runner.count(candidateOwnerStop) != stopBefore ||
				owner.runner.count(candidateOwnerRemove) != removeBefore {
				t.Fatalf("cold construction changed sequential created prefix: head=%t startup=%t gateway=%t admission=%t api=%t predecessor=%t live=%t restart=%t commands=%d/%d/%d/%d want=%d/%d/%d/%d",
					reflect.DeepEqual(activeHeadAfter, predecessorHead), reflect.DeepEqual(startupBefore, startupAfter),
					reflect.DeepEqual(stateBefore, stateAfter), reflect.DeepEqual(admissionBefore, admissionAfter),
					reflect.DeepEqual(apiPhysicalBefore, apiPhysicalAfter),
					reflect.DeepEqual(predecessorPhysical,
						generatedRouteColdAccountingPhysicalRoster(candidateMux, predecessorIdentities)),
					bytes.Equal(liveBefore, ingress.liveConfig),
					bytes.Equal(restartBefore, ingress.files[caddyContainerName+":/config/active.json"]),
					owner.runner.count(candidateOwnerCreate), owner.runner.count(candidateOwnerStart),
					owner.runner.count(candidateOwnerStop), owner.runner.count(candidateOwnerRemove),
					createBefore, startBefore, stopBefore, removeBefore)
			}
			if constructErr != nil {
				message := "generated domain construction was refused: stage=cold_construction reason=cold_group_container_attestation"
				assertGeneratedDomainConstructionDiagnostic(t, constructErr,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerAttestation, message)
				if domain != nil || generatedColdBoundaryCapacityProbes(construction.snapshot()) != 0 {
					t.Fatalf("refused sequential created-prefix construction published domain=%v or probed capacity: commands=%#v",
						domain, construction.snapshot())
				}
				t.Fatalf("authenticated StartingCandidate prefix (api durable Running/configured NeverStarted, frontend ImageReady/absent) must reconstruct without new capacity; current base refused at the exact cold container boundary: %v",
					constructErr)
			}
			if domain == nil || domain.construction == nil || domain.construction.cold == nil ||
				len(domain.construction.cold.groups) != 2 {
				t.Fatalf("sequential created-prefix cold domain=%#v", domain)
			}
			if probes := generatedColdBoundaryCapacityProbes(construction.snapshot()); probes != 0 {
				t.Fatalf("sequential created-prefix capacity probes=%d want 0 commands=%#v",
					probes, construction.snapshot())
			}
			manifestByDeployment := make(map[string]generatedDomainColdManifestGroup, 2)
			for _, group := range domain.construction.cold.groups {
				manifestByDeployment[group.deploymentID] = group
			}
			predecessorManifest, predecessorFound := manifestByDeployment[migration.request.DeploymentID]
			successorManifest, successorFound := manifestByDeployment[successorMigration.request.DeploymentID]
			if !predecessorFound || !successorFound || len(predecessorManifest.components) != 2 ||
				len(successorManifest.components) != 2 ||
				predecessorManifest.components[0].name != "api" ||
				predecessorManifest.components[0].receiptID != predecessorByName["api"].ReceiptID ||
				predecessorManifest.components[1].name != "frontend" ||
				predecessorManifest.components[1].receiptID != predecessorByName["frontend"].ReceiptID ||
				successorManifest.components[0].name != "api" ||
				successorManifest.components[0].receiptID != apiIdentity.ReceiptID ||
				successorManifest.components[1].name != "frontend" || successorManifest.components[1].receiptID != "" {
				t.Fatalf("sequential created-prefix manifests predecessor=%#v successor=%#v",
					predecessorManifest, successorManifest)
			}
			for _, component := range predecessorManifest.components {
				if !component.charged || !component.retained {
					t.Fatalf("sequential created-prefix predecessor manifest component=%#v", component)
				}
			}
			if !successorManifest.components[0].charged || !successorManifest.components[0].retained ||
				!successorManifest.components[1].charged || !successorManifest.components[1].retained {
				t.Fatalf("sequential created-prefix successor manifest=%#v", successorManifest)
			}

			type expectedDebt struct {
				deploymentID   string
				component      string
				receiptID      string
				owner          candidateAccountingOwner
				physical       candidatePhysicalState
				receiptPresent bool
				explicit       bool
			}
			expectedDebts := []expectedDebt{
				{migration.request.DeploymentID, "api", predecessorByName["api"].ReceiptID,
					candidateAccountingRouteActive, candidatePhysicalHandedToRoute, true, false},
				{migration.request.DeploymentID, "frontend", predecessorByName["frontend"].ReceiptID,
					candidateAccountingRouteActive, candidatePhysicalHandedToRoute, true, false},
				{successorMigration.request.DeploymentID, "api", apiIdentity.ReceiptID,
					candidateAccountingCandidate, candidatePhysicalPresent, true, false},
				{successorMigration.request.DeploymentID, "frontend", "",
					candidateAccountingCandidate, candidatePhysicalUnused, false, true},
			}
			ledger := domain.Candidates().ledger
			var memory, disk uint64
			debts := make([]*generatedDomainCapacityDebtToken, 0, len(expectedDebts))
			for _, expected := range expectedDebts {
				ledger.mu.Lock()
				group := ledger.groups[candidateCapacityGroupKey{appID: migration.guarded.appID,
					deploymentID: expected.deploymentID}]
				ledger.mu.Unlock()
				if group == nil {
					t.Fatalf("sequential created-prefix omitted accounting group %s", expected.deploymentID)
				}
				group.mu.Lock()
				component := group.byName[expected.component]
				var receiptID string
				physical := candidatePhysicalUnused
				if component != nil && component.receipt != nil {
					receiptID = component.receipt.id
					physical = candidatePhysicalFromWord(component.receipt.state.Load())
				}
				var debt *generatedDomainCapacityDebtToken
				if component != nil && component.charge != nil {
					debt, _ = component.charge.token.(*generatedDomainCapacityDebtToken)
				}
				receiptValid := expected.receiptPresent && component != nil && component.receipt != nil &&
					receiptID == expected.receiptID && physical == expected.physical ||
					!expected.receiptPresent && component != nil && component.receipt == nil && receiptID == ""
				valid := component != nil && receiptValid &&
					component.charge != nil && component.charge.state == candidateChargeActive &&
					component.charge.owner == expected.owner && component.charge.lease == nil &&
					component.charge.receiptRetained == expected.receiptPresent &&
					component.charge.explicitRetained == expected.explicit &&
					!component.charge.logicallyRetired && component.charge.refs == 0 && !component.tokenHeld &&
					debt != nil && debt.active && debt.limits == group.limits && candidateCapacityChargeTokenPublished(debt)
				memory += group.limits.MemoryBytes
				disk += group.limits.DiskBytes
				group.mu.Unlock()
				if !valid {
					t.Fatalf("sequential created-prefix debt %s/%s=%#v", expected.deploymentID,
						expected.component, component)
				}
				debts = append(debts, debt)
			}
			coordinator := domain.construction.capacity
			coordinator.mu.Lock()
			coordinatorValid := coordinator.phase == generatedDomainCapacityPublished && coordinator.borrows == 0 &&
				coordinator.memory == memory && coordinator.disk == disk && len(coordinator.tokens) == len(debts)
			for _, debt := range debts {
				_, registered := coordinator.tokens[debt]
				coordinatorValid = coordinatorValid && registered && debt.owner == coordinator
			}
			coordinator.mu.Unlock()
			if !coordinatorValid {
				t.Fatal("sequential created-prefix capacity coordinator did not retain exact four-component debt")
			}

			process.allow([]string{"container", "start", apiIdentity.ContainerID})
			resumed, err := successorMigration.jobService.Resume(successorMigration.claim.JobID())
			if err != nil || resumed.Status != string(jobs.Queued) {
				t.Fatalf("resume sequential created-prefix job=%+v err=%v", resumed, err)
			}
			frontendContainerID := strings.Repeat("a", 64)
			var freshAttempt int
			var frontendIdentity CandidateIdentity
			var workerErr error
			runGeneratedColdBoundaryWorker(t, successorMigration.jobService,
				generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					freshAttempt = job.Attempt
					claim, err := deploymentexecution.NewClaim(successorMigration.binding, job.ID,
						successorMigration.guarded.appID, job.Attempt)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					request := GeneratedReplacementRequest(successorValidation.request)
					request.Claim = claim
					refusalReservation, err := domain.Candidates().ReserveReplacement(ctx, request)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					crossedRequest := GeneratedCandidateRequest(request)
					if crossedRequest.CandidateSlot == "blue" {
						crossedRequest.CandidateSlot = "green"
					} else {
						crossedRequest.CandidateSlot = "blue"
					}
					crossedExecution, crossedErr := domain.Candidates().PinCandidates(ctx,
						crossedRequest, refusalReservation)
					if crossedExecution != nil {
						crossedExecution.Leave()
					}
					refusalReservation.Release()
					if !errors.Is(crossedErr, deploymentexecution.ErrIdentityLost) {
						workerErr = fmt.Errorf("crossed helper pin error=%v", crossedErr)
						return jobs.ExecutionResult{}, workerErr
					}
					freshReservation, err := domain.Candidates().ReserveReplacement(ctx, request)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshReservation.Release()
					freshExecution, err := domain.Candidates().PinCandidates(ctx,
						GeneratedCandidateRequest(request), freshReservation)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					defer freshExecution.Leave()
					adopted, err := freshExecution.AdoptCandidate("api")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					adoptedPermit, ok := adopted.(*generatedCandidateInvocationPermit)
					if !ok || adoptedPermit == nil || adoptedPermit.containerID != apiIdentity.ContainerID ||
						adopted.Identity().ReceiptID != apiIdentity.ReceiptID {
						workerErr = fmt.Errorf("adopted sequential api=%T %#v", adopted, adopted.Identity())
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.StartCandidate(adopted); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}

					frontendInvocation, err := freshExecution.BeginCreate("frontend")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					frontendPermit, ok := frontendInvocation.(*generatedCandidateInvocationPermit)
					if !ok || frontendPermit == nil {
						workerErr = fmt.Errorf("frontend invocation=%T", frontendInvocation)
						return jobs.ExecutionResult{}, workerErr
					}
					owner.runner.mu.Lock()
					owner.runner.containerID = frontendContainerID
					owner.runner.physical = candidateOwnerPhysical{policy: frontendPermit.policy}
					owner.runner.mu.Unlock()
					createArgs, err := frontendPermit.policy.createArgs("")
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					for _, args := range [][]string{
						{"image", "inspect", "--format", generatedCandidateImageInspectFormat,
							frontendPermit.policy.identity.ImageContentID},
						{"network", "inspect", "--format", generatedCandidateNetworkInspectFormat,
							frontendPermit.policy.identity.NetworkName},
						{"container", "inspect", "--format", generatedcontainer.InspectFormat,
							frontendPermit.policy.identity.ContainerName},
						createArgs,
						{"container", "inspect", "--format", generatedcontainer.InspectFormat, frontendContainerID},
						{"container", "start", frontendContainerID},
					} {
						process.allow(args)
					}
					frontendIdentity, err = freshExecution.CreateAndRecord(frontendInvocation)
					if err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if frontendIdentity.ContainerID != frontendContainerID ||
						frontendIdentity.ContainerID == apiIdentity.ContainerID || frontendIdentity.ReceiptID == "" {
						workerErr = fmt.Errorf("created sequential frontend=%#v", frontendIdentity)
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.StartCandidate(frontendInvocation); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					starting, err := freshExecution.FinishStarting()
					if err != nil || starting.Phase != generatedruntimestate.PhaseWaitingHealth {
						workerErr = errors.Join(err, fmt.Errorf("finish sequential starting phase=%s", starting.Phase))
						return jobs.ExecutionResult{}, workerErr
					}
					if err := freshExecution.WaitHealthyAndRecord(adopted); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if err := freshExecution.WaitHealthyAndRecord(frontendInvocation); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					if _, err := freshExecution.FinishHealth(); err != nil {
						workerErr = err
						return jobs.ExecutionResult{}, err
					}
					return jobs.ExecutionResult{}, nil
				}), successorMigration.claim.JobID(), jobs.Succeeded, identity.options.WorkingDirectory,
				func() string { return boundedErrorText(workerErr) })
			if workerErr != nil {
				t.Fatalf("fresh sequential created-prefix worker: %v", workerErr)
			}
			if freshAttempt <= apiCreatorAttempt || freshAttempt <= pausedAttempt {
				t.Fatalf("fresh sequential attempt=%d paused=%d original=%d",
					freshAttempt, pausedAttempt, apiCreatorAttempt)
			}
			var frontendCreatorAttempt int
			if err := identity.database.QueryRow(`SELECT creator_worker_attempt FROM generated_candidate_intents WHERE id=?`,
				frontendIdentity.ReceiptID).Scan(&frontendCreatorAttempt); err != nil {
				t.Fatal(err)
			}
			var apiCreatorAttemptAfter, intentCountAfter int
			if err := identity.database.QueryRow(`SELECT creator_worker_attempt FROM generated_candidate_intents WHERE id=?`,
				apiIdentity.ReceiptID).Scan(&apiCreatorAttemptAfter); err != nil {
				t.Fatal(err)
			}
			if err := identity.database.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`,
				successorMigration.request.DeploymentID).Scan(&intentCountAfter); err != nil {
				t.Fatal(err)
			}
			_, capacityCallsAfter := owner.source.observed()
			apiRunning := candidateMux.physical[apiIdentity.ContainerID]
			frontendRunning := candidateMux.physical[frontendIdentity.ContainerID]
			apiRunningExpected := apiRunning.policy.identity
			apiRunningExpected.ContainerID = apiIdentity.ContainerID
			frontendRunningExpected := frontendRunning.policy.identity
			frontendRunningExpected.ContainerID = frontendIdentity.ContainerID
			finalState, err := store.load()
			if err != nil {
				t.Fatal(err)
			}
			if apiCreatorAttemptAfter != apiCreatorAttempt || intentCountAfter != 2 ||
				frontendCreatorAttempt != freshAttempt || frontendCreatorAttempt == apiCreatorAttempt ||
				!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(apiRunning),
					apiRunningExpected, apiRunning.policy.limits) ||
				!generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(apiRunning)) ||
				!generatedcontainer.MatchesRunning(candidateOwnerPhysicalInspection(frontendRunning),
					frontendRunningExpected, frontendRunning.policy.limits) ||
				!generatedcontainer.CurrentlyRunning(candidateOwnerPhysicalInspection(frontendRunning)) ||
				owner.runner.count(candidateOwnerCreate) != createBefore+1 ||
				owner.runner.count(candidateOwnerStart) != startBefore+2 ||
				owner.runner.count(candidateOwnerStop) != stopBefore ||
				owner.runner.count(candidateOwnerRemove) != removeBefore ||
				capacityCallsAfter != originalCapacityCalls ||
				generatedColdBoundaryCapacityProbes(construction.snapshot()) != 0 ||
				!reflect.DeepEqual(stateBefore, finalState) {
				t.Fatalf("fresh sequential continuation effects/provenance: attempts=%d/%d apiAfter=%d intents=%d running=%t/%t commands=%d/%d/%d/%d capacity=%d/%d probes=%d",
					apiCreatorAttempt, frontendCreatorAttempt, apiCreatorAttemptAfter, intentCountAfter,
					apiRunning.running, frontendRunning.running,
					owner.runner.count(candidateOwnerCreate), owner.runner.count(candidateOwnerStart),
					owner.runner.count(candidateOwnerStop), owner.runner.count(candidateOwnerRemove),
					originalCapacityCalls, capacityCallsAfter, generatedColdBoundaryCapacityProbes(construction.snapshot()))
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

type generatedDomainColdStartingCandidatePublicFixture struct {
	identity   generatedDomainIdentityFixture
	migration  *generatedMigrationFixture
	validation *candidateValidationFixture
	owner      *candidateOwnerFixture
	candidates *generatedRouteCandidateRunner
	identities map[string]CandidateIdentity
	ordered    []CandidateIdentity
	store      *stateStore
	runner     *generatedDomainConstructionRunner
	process    runtimeprocess.CommandRunner
}

// generatedDomainColdStartingCandidateCensusRunner supplies only the detailed
// immutable image observation for a not-yet-created suffix component. The
// startup census uses a different exact format and remains owned by delegate.
type generatedDomainColdStartingCandidateCensusRunner struct {
	delegate runtimeprocess.CommandRunner
	images   map[string]candidatePolicy
}

func (r *generatedDomainColdStartingCandidateCensusRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) == 5 && reflect.DeepEqual(args[:4],
		[]string{"image", "inspect", "--format", generatedCandidateImageInspectFormat}) {
		if policy, found := r.images[args[4]]; found {
			candidate := &candidateOwnerRunner{physical: candidateOwnerPhysical{policy: policy}}
			return candidate.defaultResult(candidateOwnerImageInspect, request)
		}
	}
	return r.delegate.Run(ctx, request)
}

func newGeneratedDomainColdStartingCandidatePublicFixture(t *testing.T, createOrder []string,
	start map[string]bool,
) generatedDomainColdStartingCandidatePublicFixture {
	t.Helper()
	components := []string{"api", "frontend"}
	identity := newGeneratedDomainIdentityFixture(t)
	migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, components, components,
		generatedDomainTwoComponentPlan())
	identity.stores = migration.repository.stores
	validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
	owner, candidates := newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t, validation,
		generatedDomainCandidateOptions(identity.options), 2)
	reservation := owner.reserve(t)
	execution := owner.pin(t, reservation)
	released := false
	defer func() {
		if released {
			return
		}
		execution.Leave()
		reservation.Release()
		_ = migration.releaseEffects()
	}()

	identities := make(map[string]CandidateIdentity, len(createOrder))
	ordered := make([]CandidateIdentity, 0, len(createOrder))
	containerIDs := map[string]string{"api": strings.Repeat("7", 64), "frontend": strings.Repeat("8", 64)}
	for _, component := range createOrder {
		containerID, known := containerIDs[component]
		if !known {
			t.Fatalf("unknown starting-candidate component %q", component)
		}
		invocation, err := execution.BeginCreate(component)
		if err != nil {
			t.Fatalf("begin %s candidate create: %v", component, err)
		}
		permit, ok := invocation.(*generatedCandidateInvocationPermit)
		if !ok || permit == nil {
			t.Fatalf("%s candidate invocation=%T", component, invocation)
		}
		owner.runner.mu.Lock()
		owner.runner.containerID = containerID
		owner.runner.physical = candidateOwnerPhysical{policy: permit.policy}
		owner.runner.mu.Unlock()
		created, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatalf("create %s candidate: %v", component, err)
		}
		if start[component] {
			if err := execution.StartCandidate(invocation); err != nil {
				t.Fatalf("start %s candidate: %v", component, err)
			}
		}
		identities[component] = created
		ordered = append(ordered, created)
	}
	admission := validation.read(t)
	if admission.runtime.phase != generatedruntimestate.PhaseStartingCandidate ||
		len(admission.components) != len(components) {
		t.Fatalf("public starting-candidate prefix=%#v", admission)
	}
	execution.Leave()
	reservation.Release()
	if err := migration.releaseEffects(); err != nil {
		t.Fatal(err)
	}
	released = true
	result, err := generatedrecovery.RecoverRuntime(context.Background(), identity.database,
		time.Now().UTC(), migration.bindings)
	if err != nil || result.Jobs.RequeuedGenerated != 1 {
		t.Fatalf("recover public starting-candidate job: result=%+v err=%v", result, err)
	}
	runGeneratedColdBoundaryWorker(t, migration.jobService,
		generatedColdBoundaryExecutor(func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
				PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
		}), migration.claim.JobID(), jobs.WaitingUser, identity.options.WorkingDirectory, nil)

	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatalf("install public starting-candidate gateway state: %v", err)
	}
	if err := store.createInitialEmpty(); err != nil {
		t.Fatalf("install public starting-candidate gateway state: %v", err)
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
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
		}
	}
	physical := generatedRouteColdAccountingPhysicalRoster(candidates, ordered)
	objects = append(objects, generatedDomainA0ApplicationObjects(t, physical)...)
	physicalComponents := make(map[string]struct{}, len(physical))
	for _, value := range physical {
		physicalComponents[value.policy.identity.Component] = struct{}{}
	}
	absentImages := make(map[string]candidatePolicy)
	for _, component := range admission.components {
		if _, present := physicalComponents[component.plan.Name]; present || component.artifact == nil {
			continue
		}
		artifact := *component.artifact
		policy := candidatePolicy{identity: generatedcontainer.Identity{
			AppID: validation.pins.applicationID(), ReleaseID: validation.pins.releaseID,
			DeploymentID: validation.pins.deploymentID, ArtifactID: artifact.id,
			DeploymentPlanRevisionID: validation.pins.deploymentPlanRevisionID,
			Component:                component.plan.Name, Role: component.plan.Role,
			ImageContentID: artifact.imageContentID}, buildDefinitionDigest: artifact.buildDefinitionDigest}
		if previous, duplicate := absentImages[artifact.imageContentID]; duplicate && !reflect.DeepEqual(previous, policy) {
			t.Fatal("absent starting-candidate images cross immutable policy")
		}
		absentImages[artifact.imageContentID] = policy
		objects = append(objects, generatedDomainDockerObject{kind: "image", id: artifact.imageContentID,
			labels: generatedDomainColdImageLabels(validation.pins, component, artifact),
			data:   generatedDomainDockerObjectData{contentID: artifact.imageContentID}})
	}
	identity.options.HostPort = journal.Source.LocalHostPort
	runner := &generatedDomainConstructionRunner{
		delegate: &generatedDomainRecoveryCensusRunner{daemonID: "generated-cold-starting-candidate-public",
			objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	census := &generatedDomainColdStartingCandidateCensusRunner{delegate: runner, images: absentImages}
	process := &generatedRouteColdAccountingRunner{cold: census, predecessor: candidates,
		predecessorPhysical: physical}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
		database: identity.database, stores: identity.stores, runner: process, options: identity.options,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	return generatedDomainColdStartingCandidatePublicFixture{identity: identity, migration: migration,
		validation: validation, owner: owner, candidates: candidates, identities: identities, ordered: ordered,
		store: store, runner: runner, process: process}
}

func (f generatedDomainColdStartingCandidatePublicFixture) construct(t *testing.T) (*GeneratedDomain, error) {
	t.Helper()
	startupBefore, err := f.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := f.store.load()
	if err != nil {
		t.Fatal(err)
	}
	admissionBefore := f.validation.read(t)
	physicalBefore := generatedRouteColdAccountingPhysicalRoster(f.candidates, f.ordered)
	_, capacityCallsBefore := f.owner.source.observed()
	createBefore, startBefore := f.owner.runner.count(candidateOwnerCreate), f.owner.runner.count(candidateOwnerStart)
	stopBefore, removeBefore := f.owner.runner.count(candidateOwnerStop), f.owner.runner.count(candidateOwnerRemove)
	domain, constructErr := NewGeneratedDomain(context.Background(), f.identity.stores, f.process,
		&candidateFactoryEnvironment{path: t.TempDir()}, generatedColdBoundaryPublicOptions(f.identity.options))
	startupAfter, err := f.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := f.store.load()
	if err != nil {
		t.Fatal(err)
	}
	admissionAfter := f.validation.read(t)
	physicalAfter := generatedRouteColdAccountingPhysicalRoster(f.candidates, f.ordered)
	_, capacityCallsAfter := f.owner.source.observed()
	if !reflect.DeepEqual(startupBefore, startupAfter) || !reflect.DeepEqual(stateBefore, stateAfter) ||
		!reflect.DeepEqual(admissionBefore, admissionAfter) || !reflect.DeepEqual(physicalBefore, physicalAfter) ||
		capacityCallsAfter != capacityCallsBefore || f.owner.runner.count(candidateOwnerCreate) != createBefore ||
		f.owner.runner.count(candidateOwnerStart) != startBefore ||
		f.owner.runner.count(candidateOwnerStop) != stopBefore ||
		f.owner.runner.count(candidateOwnerRemove) != removeBefore {
		t.Fatalf("cold starting-candidate construction mutated source state: startup=%t gateway=%t admission=%t physical=%t capacity=%d/%d commands=%d/%d/%d/%d want=%d/%d/%d/%d",
			reflect.DeepEqual(startupBefore, startupAfter), reflect.DeepEqual(stateBefore, stateAfter),
			reflect.DeepEqual(admissionBefore, admissionAfter), reflect.DeepEqual(physicalBefore, physicalAfter),
			capacityCallsBefore, capacityCallsAfter, f.owner.runner.count(candidateOwnerCreate),
			f.owner.runner.count(candidateOwnerStart), f.owner.runner.count(candidateOwnerStop),
			f.owner.runner.count(candidateOwnerRemove), createBefore, startBefore, stopBefore, removeBefore)
	}
	return domain, constructErr
}

func assertGeneratedDomainColdStartingCandidateDebt(t *testing.T,
	fixture generatedDomainColdStartingCandidatePublicFixture, domain *GeneratedDomain,
) {
	t.Helper()
	if domain == nil || domain.construction == nil || domain.construction.cold == nil ||
		len(domain.construction.cold.groups) != 1 || generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()) != 0 {
		t.Fatalf("all-running starting-candidate construction=%#v probes=%d",
			domain, generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()))
	}
	manifest := domain.construction.cold.groups[0]
	if manifest.deploymentID != fixture.migration.request.DeploymentID || len(manifest.components) != 2 {
		t.Fatalf("all-running starting-candidate manifest=%#v", manifest)
	}
	ledger := domain.Candidates().ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("all-running starting-candidate accounting group is absent")
	}
	group.mu.Lock()
	debts := make([]*generatedDomainCapacityDebtToken, 0, len(group.components))
	valid := reflect.DeepEqual(group.components, []string{"api", "frontend"})
	for _, name := range group.components {
		component := group.byName[name]
		identity, hasReceipt := fixture.identities[name]
		debt := (*generatedDomainCapacityDebtToken)(nil)
		if component != nil && component.charge != nil {
			debt, _ = component.charge.token.(*generatedDomainCapacityDebtToken)
		}
		validReceipt := hasReceipt && component != nil && component.receipt != nil &&
			component.receipt.id == identity.ReceiptID &&
			candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalPresent &&
			component.charge != nil && component.charge.receiptRetained && !component.charge.explicitRetained
		if !hasReceipt {
			validReceipt = component != nil && component.receipt == nil && component.charge != nil &&
				!component.charge.receiptRetained && component.charge.explicitRetained
		}
		valid = valid && validReceipt && component.charge.state == candidateChargeActive &&
			component.charge.owner == candidateAccountingCandidate && !component.charge.logicallyRetired &&
			component.charge.refs == 0 && component.charge.lease == nil && debt != nil && debt.active &&
			debt.limits == group.limits && !component.tokenHeld
		debts = append(debts, debt)
	}
	limits := group.limits
	group.mu.Unlock()
	coordinator := domain.construction.capacity
	coordinator.mu.Lock()
	valid = valid && coordinator.phase == generatedDomainCapacityPublished && coordinator.borrows == 0 &&
		coordinator.memory == uint64(len(debts))*limits.MemoryBytes &&
		coordinator.disk == uint64(len(debts))*limits.DiskBytes && len(coordinator.tokens) == len(debts)
	for _, debt := range debts {
		_, present := coordinator.tokens[debt]
		valid = valid && present && debt != nil && debt.owner == coordinator && debt.active
	}
	phase, borrows, memory, disk, tokens := coordinator.phase, coordinator.borrows,
		coordinator.memory, coordinator.disk, len(coordinator.tokens)
	coordinator.mu.Unlock()
	if !valid {
		t.Fatalf("all-running starting-candidate debt is not exact: group=%#v phase=%d borrows=%d memory=%d disk=%d tokens=%d",
			group, phase, borrows, memory, disk, tokens)
	}
}

func TestGeneratedDomainColdStartingCandidatePrefixPublicControls(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name        string
		createOrder []string
		start       map[string]bool
	}{
		{name: "running_prefix_with_absent_tail_restores_candidate_debt_without_capacity_probe",
			createOrder: []string{"api"}, start: map[string]bool{"api": true}},
		{name: "running_prefix_with_never_started_tail_restores_candidate_debt_without_capacity_probe",
			createOrder: []string{"api", "frontend"}, start: map[string]bool{"api": true}},
		{name: "all_running_restores_candidate_debt_without_capacity_probe",
			createOrder: []string{"api", "frontend"}, start: map[string]bool{"api": true, "frontend": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainColdStartingCandidatePublicFixture(t, test.createOrder, test.start)
				domain, err := fixture.construct(t)
				if err != nil {
					t.Fatalf("construct valid starting-candidate prefix: %v", err)
				}
				assertGeneratedDomainColdStartingCandidateDebt(t, fixture, domain)
			})
		})
	}
	t.Run("noncontiguous_durable_identity_refuses_before_capacity", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainColdStartingCandidatePublicFixture(t,
				[]string{"frontend"}, map[string]bool{"frontend": true})
			domain, err := fixture.construct(t)
			assertGeneratedDomainConstructionDiagnostic(t, err,
				generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReasonColdGroupStructure,
				"generated domain construction was refused: stage=cold_construction reason=cold_group_structure")
			if domain != nil || generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()) != 0 {
				t.Fatalf("noncontiguous starting candidate published=%v probes=%d",
					domain, generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()))
			}
		})
	})
	for _, test := range []struct {
		name  string
		start map[string]bool
		stop  string
	}{
		{name: "earlier_never_started_refuses", start: map[string]bool{"frontend": true}},
		{name: "multiple_never_started_refuses", start: map[string]bool{}},
		{name: "final_stopped_refuses", start: map[string]bool{"api": true, "frontend": true}, stop: "frontend"},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainColdStartingCandidatePublicFixture(t,
					[]string{"api", "frontend"}, test.start)
				if test.stop != "" {
					identity := fixture.identities[test.stop]
					physical := fixture.candidates.physical[identity.ContainerID]
					physical.running = false
					fixture.candidates.physical[identity.ContainerID] = physical
				}
				domain, err := fixture.construct(t)
				assertGeneratedDomainConstructionDiagnostic(t, err,
					generatedDomainConstructionStageColdConstruction,
					generatedDomainConstructionReasonColdGroupContainerAttestation,
					"generated domain construction was refused: stage=cold_construction reason=cold_group_container_attestation")
				if domain != nil || generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()) != 0 {
					t.Fatalf("invalid starting-candidate physical state published=%v probes=%d",
						domain, generatedColdBoundaryCapacityProbes(fixture.runner.snapshot()))
				}
			})
		})
	}
}

func TestGeneratedDomainColdStartingCandidatePrefixHelperRefusesNoncontiguousDurableIdentity(t *testing.T) {
	group := generatedDomainColdGroup{
		kind: generatedDomainColdRisky,
		snapshot: candidateAdmissionSnapshot{
			runtime: candidateRuntimeSnapshot{phase: generatedruntimestate.PhaseStartingCandidate},
			components: []candidateAdmissionComponent{
				{plan: deploymentplans.Component{Name: "api"}, state: generatedruntimestate.ComponentImageReady},
				{plan: deploymentplans.Component{Name: "frontend"}, state: generatedruntimestate.ComponentRunning,
					receipt: &candidateIntentSnapshot{id: "receipt-frontend"}, containerName: "frontend-container",
					containerID: strings.Repeat("a", 64)},
			},
		},
		policies: map[string]candidatePolicy{"frontend": {}},
		receipts: map[string]candidateColdReceiptSeed{"frontend": {
			id: "receipt-frontend", physical: candidatePhysicalPresent,
		}},
	}
	if !generatedDomainColdHasReceipt(group.snapshot.components) {
		t.Fatal("noncontiguous helper input did not retain durable identity evidence")
	}
	if prefix, ok := generatedDomainColdStartingCandidatePrefixLength(group); ok || prefix != 0 {
		t.Fatalf("noncontiguous starting-candidate prefix=(%d,%t)", prefix, ok)
	}
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
