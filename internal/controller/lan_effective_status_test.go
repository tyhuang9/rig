package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// Embed the mutation interface so an unexpected mutation cannot silently pass.
// This fixture supplies only the locked observation used by the status endpoint.
type statusLANObservation struct {
	controller.LANAppGrantRuntime
	observation generatedingress.GatewayV2LANGrantObservation
	seen        generatedingress.GatewayV2LANGrantRequest
	err         error
}

func (r *statusLANObservation) WithGatewayV2LANObservation(ctx context.Context, request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	r.seen = request
	if r.err != nil {
		return r.err
	}
	value := r.observation
	value.Request = request
	return fn(ctx, value)
}

type statusLANAuthorization struct {
	controller.LANAppGrantService
	alter func(*appaccess.AppAccessGrantAuthorization)
	err   error
}

func (s statusLANAuthorization) AuthorizeAppAccessGrant(ctx context.Context, input appaccess.AppAccessGrantAuthorizationInput) (appaccess.AppAccessGrantAuthorization, error) {
	if s.err != nil {
		return appaccess.AppAccessGrantAuthorization{}, s.err
	}
	value, err := s.LANAppGrantService.AuthorizeAppAccessGrant(ctx, input)
	if err == nil && s.alter != nil {
		s.alter(&value)
	}
	return value, err
}

type lanStatusFixture struct {
	api        deploymentAPIFixture
	repository *appaccess.Repository
	profile    appaccess.GatewayProfileRevision
	claim      appaccess.AppAccessGrantClaim
	state      *routeRuntimeState
	runtime    *statusLANObservation
	server     *controller.Server
}

func newVerifiedLANStatusFixture(t *testing.T) *lanStatusFixture {
	t.Helper()
	ctx := context.Background()
	f := newDeploymentAPIFixtureWithRuntimes(t, false, true, false)
	plan := f.acceptPlan(t, f.app.ID, deploymentplans.StrategyGeneratedNode)
	job := f.createDeploymentWithPlan(t, plan)
	var deploymentID string
	if err := f.db.QueryRow(`SELECT id FROM deployments WHERE job_id=? AND app_id=?`, job.ID, f.app.ID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []deployments.Status{deployments.Applying, deployments.Succeeded} {
		if _, err := f.deployments.Transition(ctx, f.app.ID, deploymentID, status, ""); err != nil {
			t.Fatal(err)
		}
	}
	deployment, err := f.deployments.Get(ctx, f.app.ID, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	repository := appaccess.New(f.db)
	spec := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.50.20", InterfaceID: "7/Ethernet LAN", PortStart: 8100, PortEnd: 8101}
	digest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	actor := f.userID(t)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), Spec: spec,
		Approval: appaccess.Approval{Action: appaccess.ActionConfigureGateway, SpecDigest: digest, ActorID: actor},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := uuid.NewString()
	allocation, _, err := repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
		AppID: f.app.ID, OperationID: operation, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err = appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{
		AppID: f.app.ID, OperationID: operation, AllocationID: allocation.ID,
		Approval: appaccess.Approval{Action: appaccess.ActionEnableAppAccess, SpecDigest: digest, ActorID: actor},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := repository.ClaimAppAccessGrant(ctx, appaccess.ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, profile), ActorID: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := appaccess.AppAccessGrantClaimOwnerFor(claim)
	for _, states := range [][2]appaccess.AppAccessGrantState{
		{appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying},
		{appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive},
	} {
		if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, owner, states[0], states[1]); err != nil {
			t.Fatal(err)
		}
	}
	gatewayOperation := uuid.NewString()
	claim, _, err = repository.ResolveAppAccessGrantClaim(ctx, owner, appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted,
		appaccess.AppAccessGrantProof{GatewayOperationID: gatewayOperation, ProtectedStateDigest: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	description, err := generatedruntime.DescribeInactiveCandidate(f.app.ID, "web", generatedruntime.SlotGreen)
	if err != nil {
		t.Fatal(err)
	}
	containerID := strings.Repeat("a", 64)
	state := &routeRuntimeState{
		head: generatedruntimestate.ActiveHead{AppID: f.app.ID, DeploymentID: deploymentID, ReleaseID: deployment.ReleaseID, Slot: "blue", Generation: 1},
		deployment: generatedruntimestate.Deployment{
			AppID: f.app.ID, DeploymentID: deploymentID, ReleaseID: deployment.ReleaseID,
			DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
			CandidateSlot: "blue", Phase: generatedruntimestate.PhaseSucceeded,
			Components: []generatedruntimestate.Component{{DeploymentID: deploymentID, Name: "web", Slot: "blue", ContainerID: containerID,
				ContainerName: description.ContainerName, State: generatedruntimestate.ComponentActive}},
		},
	}
	runtime := &statusLANObservation{observation: generatedingress.GatewayV2LANGrantObservation{
		Disposition: generatedingress.GatewayV2LANGrantCommitted, GatewayOperationID: gatewayOperation,
		ProtectedStateDigest: strings.Repeat("b", 64), ObservedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), Slot: generatedruntime.SlotBlue,
		Endpoints: []generatedruntime.RouteEndpoint{{Component: "web", Role: generatedruntime.RoleServer, ContainerID: containerID,
			NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias, InternalPort: 3000}},
	}}
	server := &controller.Server{Logger: slog.New(slog.NewJSONHandler(f.logs, nil)), Auth: f.auth, Apps: f.apps, Deployments: f.deployments, GeneratedRuntime: true,
		GeneratedRuntimeState: state, AppAccess: repository, AppGrants: repository, LANGrantRuntime: runtime}
	f.handler = server.Handler()
	return &lanStatusFixture{api: f, repository: repository, profile: profile, claim: claim, state: state, runtime: runtime, server: server}
}

func (f *lanStatusFixture) read(t *testing.T) map[string]any {
	t.Helper()
	f.state.reads = 0
	response := f.api.request(http.MethodGet, "/api/v1/apps/"+f.api.app.ID+"/lan-access", "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status response: %d %s", response.Code, response.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestLANAccessVerifiedStatusUsesAuthorizedProfileAndRetainsRawGrant(t *testing.T) {
	f := newVerifiedLANStatusFixture(t)
	value := f.read(t)
	want := "http://" + f.profile.Spec.SelectedIPv4 + ":" + strconv.Itoa(int(f.claim.Spec.Port)) + "/"
	if value["availability"] != "verified" || value["url"] != want || value["observedAt"] != f.runtime.observation.ObservedAt.Format(time.RFC3339Nano) {
		t.Fatalf("verified LAN projection: %#v", value)
	}
	if f.runtime.seen.GatewayProfileRevisionID != f.claim.Spec.GatewayProfileRevisionID ||
		f.runtime.seen.GatewayProfileSpecDigest != f.claim.Spec.GatewayProfileSpecDigest || f.runtime.seen.ClaimRequestDigest != f.claim.RequestDigest {
		t.Fatal("status observation changed the immutable grant request")
	}
}

func TestLANAccessStatusWithholdsURLWhenServingEvidenceChanges(t *testing.T) {
	for _, name := range []string{"runtime error", "uncertain activation", "wrong endpoint", "changed serving head", "missing timestamp"} {
		t.Run(name, func(t *testing.T) {
			f := newVerifiedLANStatusFixture(t)
			switch name {
			case "runtime error":
				f.runtime.err = errors.New("gateway recovery required")
			case "uncertain activation":
				f.runtime.observation.ActivationUncertain = true
			case "wrong endpoint":
				f.runtime.observation.Endpoints[0].ContainerID = strings.Repeat("c", 64)
			case "changed serving head":
				second := f.state.head
				second.Generation++
				f.state.second = &second
			case "missing timestamp":
				f.runtime.observation.ObservedAt = time.Time{}
			}
			value := f.read(t)
			if value["availability"] != "unverified" || value["url"] != nil || value["observedAt"] != nil {
				t.Fatalf("unproved route exposed a URL or timestamp: %#v", value)
			}
		})
	}
}

func TestLANAccessStatusWithholdsURLWhenAuthorizationChanges(t *testing.T) {
	for _, name := range []string{"authorization lost", "raw grant changed", "state sequence changed", "allocation inactive"} {
		t.Run(name, func(t *testing.T) {
			f := newVerifiedLANStatusFixture(t)
			service := statusLANAuthorization{LANAppGrantService: f.repository}
			switch name {
			case "authorization lost":
				service.err = appaccess.ErrConflict
			case "raw grant changed":
				service.alter = func(value *appaccess.AppAccessGrantAuthorization) {
					value.Claim.Spec.GatewayProfileSpecDigest = strings.Repeat("d", 64)
				}
			case "state sequence changed":
				service.alter = func(value *appaccess.AppAccessGrantAuthorization) { value.Claim.StateSequence++ }
			case "allocation inactive":
				service.alter = func(value *appaccess.AppAccessGrantAuthorization) {
					value.Allocation.State = appaccess.AllocationUncertain
				}
			}
			f.server.AppGrants = service
			value := f.read(t)
			if value["availability"] != "unverified" || value["url"] != nil || value["observedAt"] != nil {
				t.Fatalf("authorization mismatch exposed a URL or timestamp: %#v", value)
			}
		})
	}
}
