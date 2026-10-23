package generatedruntime

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

func newNetworkAuthorityEngine(t *testing.T, run func(runtimeprocess.CommandRequest) runtimeRequestResult) (*Engine, *runtimeFakeRunner, runtimeNetworkAuthority) {
	t.Helper()
	root := generateddomain.NewRetainedRoot(t)
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtimeFakeRunner{run: run}
	environment := &runtimeFakeEnvironment{path: filepath.Join(root, "env")}
	options := EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second,
	}
	fixture := newRuntimeDomainFixture(t, root, runner, environment, options, generateddomain.Options{
		MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30,
	})
	authority := newRuntimeNetworkAuthority(t, fixture, candidateSpec())
	return fixture.engine, runner, authority
}

func exactNetworkInspection(appID string) runtimeRequestResult {
	return runtimeJSON(networkInspection{
		Name: networkName(appID), Driver: "bridge", Scope: "local",
		Labels: map[string]string{"io.rig.managed": NetworkOwnershipLabelValue, "io.rig.application": appID},
	})
}

func missingNetworkInspection() runtimeRequestResult {
	return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such network")}, err: errors.New("inspect missing")}
}

func TestGeneratedNetworkBoundaryPreservesCreateInspectionAndCancellationCauses(t *testing.T) {
	createCause := errors.New("create sentinel")
	inspectCause := errors.New("reinspection sentinel")
	ctx, cancel := context.WithCancel(context.Background())
	inspections := 0
	engine, _, authority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "network inspect":
			inspections++
			if inspections == 1 {
				return missingNetworkInspection()
			}
			return runtimeRequestResult{err: inspectCause}
		case "network create":
			cancel()
			return runtimeRequestResult{err: createCause}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	err := engine.EnsureAppNetwork(deploymenteffects.WithScope(ctx, authority.scope), authority.spec.Network)
	if !IsGeneratedNetworkBoundaryFailure(err) || !errors.Is(err, createCause) || !errors.Is(err, inspectCause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("network failure lost causes: %v", err)
	}
	if strings.Contains(err.Error(), createCause.Error()) || strings.Contains(err.Error(), inspectCause.Error()) {
		t.Fatalf("network failure exposed runner output: %v", err)
	}
}

func TestGeneratedNetworkBoundaryFailedCreateReinspectionPreservesCurrentCancellation(t *testing.T) {
	for _, test := range []struct {
		name         string
		reinspection func(string) runtimeRequestResult
	}{
		{name: "missing", reinspection: func(string) runtimeRequestResult { return missingNetworkInspection() }},
		{name: "truncated", reinspection: func(string) runtimeRequestResult {
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(`{}`), StdoutTruncated: true}}
		}},
		{name: "wrong exact identity", reinspection: func(string) runtimeRequestResult {
			return runtimeJSON(networkInspection{Name: "foreign", Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": NetworkOwnershipLabelValue}})
		}},
		{name: "exact identity", reinspection: func(appID string) runtimeRequestResult { return exactNetworkInspection(appID) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			createCause := errors.New("create sentinel")
			ctx, cancel := context.WithCancel(context.Background())
			inspections := 0
			var authority runtimeNetworkAuthority
			engine, runner, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				switch commandKind(request.Args) {
				case "network inspect":
					inspections++
					if inspections == 1 {
						return missingNetworkInspection()
					}
					cancel()
					return test.reinspection(authority.spec.AppID)
				case "network create":
					return runtimeRequestResult{err: createCause}
				default:
					t.Fatalf("unexpected request: %#v", request.Args)
					return runtimeRequestResult{}
				}
			})
			authority = createdAuthority
			err := engine.EnsureAppNetwork(deploymenteffects.WithScope(ctx, authority.scope), authority.spec.Network)
			if !IsGeneratedNetworkBoundaryFailure(err) || !errors.Is(err, createCause) || !errors.Is(err, context.Canceled) {
				t.Fatalf("failed-create reconciliation lost causes: %v", err)
			}
			if len(runner.requests) != 3 {
				t.Fatalf("failed-create reconciliation requests=%d", len(runner.requests))
			}
		})
	}
}

func TestGeneratedNetworkBoundaryPreservesCreateErrorWhenObservationRefuses(t *testing.T) {
	createCause := errors.New("create lost acknowledgement")
	inspections := 0
	var authority runtimeNetworkAuthority
	engine, _, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "network inspect":
			inspections++
			if inspections == 1 {
				return missingNetworkInspection()
			}
			return exactNetworkInspection(authority.spec.AppID)
		case "network create":
			if _, err := authority.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, authority.spec.Network.Claim.JobID()); err != nil {
				t.Fatal(err)
			}
			return runtimeRequestResult{err: createCause}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	authority = createdAuthority
	err := engine.EnsureAppNetwork(authority.context, authority.spec.Network)
	if !IsGeneratedNetworkBoundaryFailure(err) || !errors.Is(err, createCause) || (!errors.Is(err, deploymentexecution.ErrAuthorityStale) && !errors.Is(err, deploymentexecution.ErrIdentityLost)) {
		t.Fatalf("lost acknowledgement refusal lost causes: %v", err)
	}
	var mainStatus, runtimePhase string
	if err := authority.db.QueryRow(`SELECT d.status,r.phase FROM deployments d JOIN generated_runtime_deployments r ON r.deployment_id=d.id WHERE d.id=?`, authority.spec.DeploymentID).Scan(&mainStatus, &runtimePhase); err != nil {
		t.Fatal(err)
	}
	if mainStatus != string(deployments.Applying) || runtimePhase != "starting_candidate" {
		t.Fatalf("network refusal changed durable state: main=%s runtime=%s", mainStatus, runtimePhase)
	}
}

func TestGeneratedNetworkBoundaryAdoptsExactNetworkAfterLostCreateAcknowledgement(t *testing.T) {
	createCause := errors.New("create acknowledgement lost")
	inspections := 0
	var authority runtimeNetworkAuthority
	engine, runner, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "network inspect":
			inspections++
			if inspections == 1 {
				return missingNetworkInspection()
			}
			return exactNetworkInspection(authority.spec.AppID)
		case "network create":
			return runtimeRequestResult{err: createCause}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	authority = createdAuthority
	if err := engine.EnsureAppNetwork(authority.context, authority.spec.Network); err != nil {
		t.Fatalf("exact lost-ack network was not observed: %v", err)
	}
	if len(runner.requests) != 3 {
		t.Fatalf("lost-ack requests=%d", len(runner.requests))
	}
}

func TestGeneratedNetworkBoundaryExistingExactNetworkRequiresFreshAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(runtimeNetworkAuthority)
		cause  error
	}{
		{name: "attempt drift", cause: deploymentexecution.ErrIdentityLost, mutate: func(authority runtimeNetworkAuthority) {
			if _, err := authority.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, authority.spec.Network.Claim.JobID()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "cancellation", cause: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			var authority runtimeNetworkAuthority
			engine, runner, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				if commandKind(request.Args) != "network inspect" {
					t.Fatalf("existing network reached mutation: %#v", request.Args)
				}
				if test.mutate != nil {
					test.mutate(authority)
				} else {
					cancel()
				}
				return exactNetworkInspection(authority.spec.AppID)
			})
			authority = createdAuthority
			err := engine.EnsureAppNetwork(deploymenteffects.WithScope(parent, authority.scope), authority.spec.Network)
			if !IsGeneratedNetworkBoundaryFailure(err) || !errors.Is(err, test.cause) {
				t.Fatalf("existing exact network accepted stale authority: %v", err)
			}
			if len(runner.requests) != 1 {
				t.Fatalf("existing network requests=%d", len(runner.requests))
			}
		})
	}
}

func TestGeneratedNetworkBoundaryPreservesInitialInspectionCancellation(t *testing.T) {
	inspectCause := errors.New("inspect sentinel")
	ctx, cancel := context.WithCancel(context.Background())
	var authority runtimeNetworkAuthority
	engine, runner, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "network inspect" {
			t.Fatalf("cancelled inspection reached mutation: %#v", request.Args)
		}
		cancel()
		return runtimeRequestResult{err: inspectCause}
	})
	authority = createdAuthority
	err := engine.EnsureAppNetwork(deploymenteffects.WithScope(ctx, authority.scope), authority.spec.Network)
	if !IsGeneratedNetworkBoundaryFailure(err) || !errors.Is(err, inspectCause) || !errors.Is(err, context.Canceled) {
		t.Fatalf("initial inspection lost causes: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("cancelled inspection requests=%d", len(runner.requests))
	}
}

func TestGeneratedNetworkBoundaryPreservesCancellationFromNilErrorInspectionResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		result runtimeRequestResult
	}{
		{name: "truncated", result: runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(`{}`), StdoutTruncated: true}}},
		{name: "wrong exact identity", result: runtimeJSON(networkInspection{Name: "foreign", Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": NetworkOwnershipLabelValue}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var authority runtimeNetworkAuthority
			engine, runner, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				if commandKind(request.Args) != "network inspect" {
					t.Fatalf("cancelled inspection reached mutation: %#v", request.Args)
				}
				cancel()
				return test.result
			})
			authority = createdAuthority
			err := engine.EnsureAppNetwork(deploymenteffects.WithScope(ctx, authority.scope), authority.spec.Network)
			if !IsGeneratedNetworkBoundaryFailure(err) || !errors.Is(err, context.Canceled) {
				t.Fatalf("nil-error inspection lost cancellation: %v", err)
			}
			if len(runner.requests) != 1 {
				t.Fatalf("cancelled inspection requests=%d", len(runner.requests))
			}
		})
	}
}

func TestGeneratedNetworkBoundaryChecksAuthorityAfterCommandPreparation(t *testing.T) {
	var authority runtimeNetworkAuthority
	engine, runner, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "network inspect" {
			t.Fatalf("stale authority reached mutation: %#v", request.Args)
		}
		return missingNetworkInspection()
	})
	authority = createdAuthority
	engine.afterNetworkCommandPrepared = func() {
		if _, err := authority.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, authority.spec.Network.Claim.JobID()); err != nil {
			t.Fatal(err)
		}
	}
	err := engine.EnsureAppNetwork(authority.context, authority.spec.Network)
	if !IsGeneratedNetworkBoundaryFailure(err) || (!errors.Is(err, deploymentexecution.ErrAuthorityStale) && !errors.Is(err, deploymentexecution.ErrIdentityLost)) {
		t.Fatalf("prepared command ignored stale authority: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("stale authority entered create: requests=%d", len(runner.requests))
	}
}

func TestGeneratedNetworkBoundaryClosesSQLBeforeRunnerAndPinsOriginalScope(t *testing.T) {
	entered := make(chan struct{})
	unblock := make(chan struct{})
	inspections := 0
	var authority runtimeNetworkAuthority
	engine, _, createdAuthority := newNetworkAuthorityEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "network inspect":
			inspections++
			if inspections == 1 {
				return missingNetworkInspection()
			}
			return exactNetworkInspection(authority.spec.AppID)
		case "network create":
			close(entered)
			<-unblock
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	authority = createdAuthority
	ensureDone := make(chan error, 1)
	defer func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	go func() { ensureDone <- engine.EnsureAppNetwork(authority.context, authority.spec.Network) }()
	awaitNetworkSignal(t, entered, "network create entry")
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- authority.release() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		leave, err := deploymenteffects.EnterScope(authority.context, engine.options.WorkingDirectory)
		if errors.Is(err, deploymenteffects.ErrScopeUnavailable) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		leave()
		if time.Now().After(deadline) {
			t.Fatal("effects scope did not begin closing")
		}
		runtime.Gosched()
	}
	select {
	case err := <-releaseDone:
		t.Fatalf("scope released while network runner was active: %v", err)
	default:
	}
	writerDone := make(chan error, 1)
	go func() {
		_, err := authority.db.Exec(`UPDATE machines SET updated_at=updated_at`)
		writerDone <- err
	}()
	if err := awaitNetworkError(t, writerDone, "independent database writer"); err != nil {
		t.Fatal(err)
	}
	close(unblock)
	if err := awaitNetworkError(t, ensureDone, "network completion"); err != nil {
		t.Fatal(err)
	}
	if err := awaitNetworkError(t, releaseDone, "scope release"); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedRuntimeEngineRequiresConfiguredDeploymentAuthority(t *testing.T) {
	root := generateddomain.NewRetainedRoot(t)
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	options := EngineOptions{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second}
	runner := &runtimeFakeRunner{run: func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} }}
	for name := range map[string]struct{}{"nil": {}, "unconfigured": {}} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewEngine(runner, &runtimeFakeEnvironment{}, nil, options); err == nil {
				t.Fatal("engine accepted missing generated command authority")
			}
		})
	}
}

func awaitNetworkSignal(t *testing.T, channel <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func awaitNetworkError(t *testing.T, channel <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}
