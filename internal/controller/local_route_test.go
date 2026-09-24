package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

type routeRuntimeState struct {
	head       generatedruntimestate.ActiveHead
	deployment generatedruntimestate.Deployment
	reads      int
	second     *generatedruntimestate.ActiveHead
	onSecond   func()
}

func (s *routeRuntimeState) Active(_ context.Context, appID string) (generatedruntimestate.ActiveHead, error) {
	if appID != s.head.AppID {
		return generatedruntimestate.ActiveHead{}, generatedruntimestate.ErrNotFound
	}
	s.reads++
	if s.reads > 1 && s.onSecond != nil {
		s.onSecond()
	}
	if s.second != nil && s.reads > 1 {
		return *s.second, nil
	}
	return s.head, nil
}

func (s *routeRuntimeState) Get(_ context.Context, appID, deploymentID string) (generatedruntimestate.Deployment, error) {
	if appID != s.deployment.AppID || deploymentID != s.deployment.DeploymentID {
		return generatedruntimestate.Deployment{}, generatedruntimestate.ErrNotFound
	}
	return s.deployment, nil
}

type routeIngress struct {
	mu          sync.Mutex
	observation generatedingress.Observation
	err         error
	onLocked    func()
}

func (i *routeIngress) WithObservation(ctx context.Context, _ string, fn func(context.Context, generatedingress.Observation) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return i.err
	}
	if i.onLocked != nil {
		i.onLocked()
	}
	return fn(ctx, i.observation)
}

func TestLocalRouteIsAuthenticatedFreshAndBoundToServingDeployment(t *testing.T) {
	f := newDeploymentAPIFixtureWithRuntimes(t, false, true, false)
	plan := f.acceptPlan(t, f.app.ID, deploymentplans.StrategyGeneratedNode)
	job := f.createDeploymentWithPlan(t, plan)
	var deploymentID string
	if err := f.db.QueryRow(`SELECT id FROM deployments WHERE job_id=? AND app_id=?`, job.ID, f.app.ID).Scan(&deploymentID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []deployments.Status{deployments.Applying, deployments.Succeeded} {
		if _, err := f.deployments.Transition(context.Background(), f.app.ID, deploymentID, status, ""); err != nil {
			t.Fatal(err)
		}
	}
	mainDeployment, err := f.deployments.Get(context.Background(), f.app.ID, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	description, err := generatedruntime.DescribeInactiveCandidate(f.app.ID, "web", generatedruntime.SlotGreen)
	if err != nil || description.Slot != generatedruntime.SlotBlue {
		t.Fatalf("candidate description: %#v %v", description, err)
	}
	containerID := strings.Repeat("a", 64)
	state := &routeRuntimeState{
		head: generatedruntimestate.ActiveHead{AppID: f.app.ID, DeploymentID: deploymentID, ReleaseID: mainDeployment.ReleaseID, Slot: "blue", Generation: 1},
		deployment: generatedruntimestate.Deployment{
			AppID: f.app.ID, DeploymentID: deploymentID, ReleaseID: mainDeployment.ReleaseID,
			DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
			CandidateSlot: "blue", Phase: generatedruntimestate.PhaseSucceeded,
			Components: []generatedruntimestate.Component{{DeploymentID: deploymentID, Name: "web", Slot: "blue", ContainerID: containerID, ContainerName: description.ContainerName, State: generatedruntimestate.ComponentActive}},
		},
	}
	ingress := &routeIngress{observation: generatedingress.Observation{
		URL: "http://" + f.app.ID + ".rig.localhost:8080", Slot: generatedruntime.SlotBlue,
		Endpoints:  []generatedruntime.RouteEndpoint{{Component: "web", Role: generatedruntime.RoleServer, ContainerID: containerID, NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias, InternalPort: 3000}},
		ObservedAt: time.Date(2026, 9, 24, 12, 34, 56, 123, time.UTC),
	}}
	server := &controller.Server{
		Auth: f.auth, Apps: f.apps, Deployments: f.deployments, GeneratedRuntime: true,
		GeneratedRuntimeState: state, GeneratedIngress: ingress,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	handler := server.Handler()
	path := "/api/v1/apps/" + f.app.ID + "/local-route"
	request := func(t *testing.T, path string, authenticated bool) (int, http.Header, map[string]any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: f.session.Token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("response JSON: %v (%s)", err, w.Body.String())
		}
		return w.Code, w.Header(), body
	}
	assertNonverified := func(t *testing.T, wantStatus, wantReason string) {
		t.Helper()
		status, headers, body := request(t, path, true)
		if status != http.StatusOK || headers.Get("Cache-Control") != "no-store" || body["status"] != wantStatus || body["reason"] != wantReason {
			t.Fatalf("route envelope: %d %#v %#v", status, headers, body)
		}
		for _, key := range []string{"url", "scope", "deploymentId", "releaseId", "configurationRevisionId", "configurationRevisionNumber", "planRevisionId", "planRevisionNumber"} {
			if _, present := body[key]; present {
				t.Fatalf("nonverified route leaked %s: %#v", key, body)
			}
		}
	}

	status, headers, body := request(t, path, false)
	if status != http.StatusUnauthorized || headers.Get("Cache-Control") != "no-store" || body["code"] != "unauthenticated" {
		t.Fatalf("unauthenticated route: %d %#v %#v", status, headers, body)
	}
	status, _, body = request(t, "/api/v1/apps/"+f.otherApp.ID+"/local-route", true)
	if status != http.StatusOK || body["status"] != "unavailable" || body["url"] != nil {
		t.Fatalf("cross-app route: %d %#v", status, body)
	}
	status, _, body = request(t, "/api/v1/apps/missing/local-route", true)
	if status != http.StatusNotFound || body["code"] != "app_not_found" {
		t.Fatalf("missing application: %d %#v", status, body)
	}

	state.reads = 0
	status, headers, body = request(t, path, true)
	if status != http.StatusOK || headers.Get("Cache-Control") != "no-store" || body["status"] != "verified" || body["scope"] != "controller_loopback" || body["url"] != ingress.observation.URL || body["deploymentId"] != deploymentID || body["releaseId"] != mainDeployment.ReleaseID || body["planRevisionId"] != plan.ID || body["planRevisionNumber"] != float64(plan.RevisionNumber) {
		t.Fatalf("verified route: %d %#v %#v", status, headers, body)
	}
	if body["configurationRevisionId"] != "" || body["configurationRevisionNumber"] != float64(0) || body["observedAt"] != ingress.observation.ObservedAt.Format(time.RFC3339Nano) {
		t.Fatalf("zero configuration revision omitted: %#v", body)
	}
	if _, present := body["reason"]; present {
		t.Fatalf("verified route has failure reason: %#v", body)
	}

	t.Run("no active head", func(t *testing.T) {
		state.head.DeploymentID = ""
		assertNonverified(t, "unavailable", "no_active_deployment")
		state.head.DeploymentID = deploymentID
	})
	t.Run("active head has no switch generation", func(t *testing.T) {
		state.head.Generation = 0
		assertNonverified(t, "unverified", "provenance_mismatch")
		state.head.Generation = 1
	})
	t.Run("runtime phase incomplete", func(t *testing.T) {
		state.deployment.Phase = generatedruntimestate.PhaseDraining
		assertNonverified(t, "unverified", "provenance_mismatch")
		state.deployment.Phase = generatedruntimestate.PhaseSucceeded
	})
	t.Run("runtime release differs", func(t *testing.T) {
		state.deployment.ReleaseID = "another-release"
		defer func() { state.deployment.ReleaseID = mainDeployment.ReleaseID }()
		assertNonverified(t, "unverified", "provenance_mismatch")
	})
	t.Run("runtime plan differs", func(t *testing.T) {
		state.deployment.DeploymentPlanRevisionNumber++
		defer func() { state.deployment.DeploymentPlanRevisionNumber-- }()
		assertNonverified(t, "unverified", "provenance_mismatch")
	})
	t.Run("main deployment no longer succeeded", func(t *testing.T) {
		if _, err := f.db.Exec(`UPDATE deployments SET status='failed' WHERE id=?`, deploymentID); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = f.db.Exec(`UPDATE deployments SET status='succeeded' WHERE id=?`, deploymentID) }()
		assertNonverified(t, "unverified", "provenance_mismatch")
	})
	t.Run("exact container changed", func(t *testing.T) {
		ingress.observation.Endpoints[0].ContainerID = strings.Repeat("b", 64)
		assertNonverified(t, "unverified", "attestation_failed")
		ingress.observation.Endpoints[0].ContainerID = containerID
	})
	t.Run("runtime container name changed", func(t *testing.T) {
		state.deployment.Components[0].ContainerName = "other-container"
		assertNonverified(t, "unverified", "attestation_failed")
		state.deployment.Components[0].ContainerName = description.ContainerName
	})
	t.Run("route alias changed", func(t *testing.T) {
		ingress.observation.Endpoints[0].NetworkAlias = "other-alias"
		assertNonverified(t, "unverified", "attestation_failed")
		ingress.observation.Endpoints[0].NetworkAlias = description.NetworkAlias
	})
	t.Run("new route visible before active head persists", func(t *testing.T) {
		ingress.observation.Slot = generatedruntime.SlotGreen
		assertNonverified(t, "unverified", "attestation_failed")
		ingress.observation.Slot = generatedruntime.SlotBlue
	})
	t.Run("live ingress unavailable", func(t *testing.T) {
		ingress.err = errors.New("private Docker output must not enter response")
		assertNonverified(t, "unverified", "attestation_failed")
		ingress.err = nil
	})
	t.Run("URL must remain controller local", func(t *testing.T) {
		ingress.observation.URL = "https://public.example/app"
		assertNonverified(t, "unverified", "attestation_failed")
		ingress.observation.URL = "http://" + f.app.ID + ".rig.localhost:8080"
	})
	t.Run("active switch during observation", func(t *testing.T) {
		state.reads = 0
		changed := state.head
		changed.Generation++
		state.second = &changed
		assertNonverified(t, "unverified", "provenance_mismatch")
		state.second = nil
	})
	t.Run("route switch waits for final head read", func(t *testing.T) {
		state.reads = 0
		started := make(chan struct{})
		switched := make(chan struct{})
		ingress.onLocked = func() {
			go func() {
				close(started)
				ingress.mu.Lock()
				ingress.observation.Slot = generatedruntime.SlotGreen
				close(switched)
				ingress.mu.Unlock()
			}()
			<-started
		}
		state.onSecond = func() {
			select {
			case <-switched:
				t.Fatal("route switched before final active-head read")
			default:
			}
			if ingress.mu.TryLock() {
				ingress.mu.Unlock()
				t.Fatal("final active-head read was outside ingress lock")
			}
		}
		status, _, route := request(t, path, true)
		if status != http.StatusOK || route["status"] != "verified" || route["observedAt"] != ingress.observation.ObservedAt.Format(time.RFC3339Nano) {
			t.Fatalf("serialized route observation: %d %#v", status, route)
		}
		<-switched
		state.onSecond = nil
		ingress.onLocked = nil
		ingress.observation.Slot = generatedruntime.SlotBlue
	})
	t.Run("generated runtime disabled", func(t *testing.T) {
		server.GeneratedRuntime = false
		assertNonverified(t, "unavailable", "generated_runtime_disabled")
		server.GeneratedRuntime = true
	})
}
