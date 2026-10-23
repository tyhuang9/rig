package deploymentexecution_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestGuardPolicyGateAcceptsExactLegacyAndRunningAuthority(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "running"}[running], func(t *testing.T) {
			fixture := newExecutionFixture(t, running)
			fixture.exec(t, `UPDATE deployments SET status='applying' WHERE id=?`, fixture.deploymentID)
			pin, claim := fixture.pin(t)
			defer pin.Leave()
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID); err != nil {
				t.Fatalf("policy gate: %v", err)
			}
			if err := pin.CheckGeneratedNetworkCreateTx(tx, claim, fixture.deploymentID); err != nil {
				t.Fatalf("generated network create: %v", err)
			}
			if err := pin.CheckGeneratedNetworkObservationTx(tx, claim, fixture.deploymentID); err != nil {
				t.Fatalf("generated network observation: %v", err)
			}
		})
	}
}

func TestGuardPolicyGateUsesClosedPersistedStagePolicy(t *testing.T) {
	for _, test := range []struct {
		status string
		want   error
	}{
		{status: "preparing"},
		{status: "needs_attention"},
		{status: "applying"},
		{status: "waiting_health"},
		{status: "failed", want: deploymentexecution.ErrStageInvalid},
		{status: "cancelled", want: deploymentexecution.ErrStageInvalid},
		{status: "unknown", want: deploymentexecution.ErrStageInvalid},
	} {
		t.Run(test.status, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			if test.status != "preparing" {
				fixture.exec(t, `UPDATE deployments SET status=? WHERE id=?`, test.status, fixture.deploymentID)
			}
			fixture.check(t, test.want)
		})
	}
}

func TestGuardComposeChecksUseSeparateClosedStagePolicies(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      string
		observation bool
		want        error
	}{
		{name: "up applying", status: "applying"},
		{name: "up waiting health", status: "waiting_health", want: deploymentexecution.ErrStageInvalid},
		{name: "up preparing", status: "preparing", want: deploymentexecution.ErrStageInvalid},
		{name: "observation applying", status: "applying", observation: true},
		{name: "observation waiting health", status: "waiting_health", observation: true},
		{name: "observation preparing", status: "preparing", observation: true, want: deploymentexecution.ErrStageInvalid},
		{name: "observation succeeded", status: "succeeded", observation: true, want: deploymentexecution.ErrStageInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			fixture.exec(t, `UPDATE deployments SET status=? WHERE id=?`, test.status, fixture.deploymentID)
			pin, claim, release := fixture.pinWithRelease(t)
			defer func() { pin.Leave(); _ = release() }()
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if test.observation {
				err = pin.CheckComposeObservationTx(tx, claim, fixture.deploymentID)
			} else {
				err = pin.CheckComposeUpTx(tx, claim, fixture.deploymentID)
			}
			if test.want == nil && err != nil {
				t.Fatalf("check error=%v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("check error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestGuardRouteActivationUsesExactWaitingHealthStage(t *testing.T) {
	for _, test := range []struct {
		status string
		want   error
	}{
		{status: "waiting_health"},
		{status: "applying", want: deploymentexecution.ErrStageInvalid},
		{status: "preparing", want: deploymentexecution.ErrStageInvalid},
		{status: "succeeded", want: deploymentexecution.ErrStageInvalid},
	} {
		t.Run(test.status, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			fixture.exec(t, `UPDATE deployments SET status=? WHERE id=?`, test.status, fixture.deploymentID)
			pin, claim, release := fixture.pinWithRelease(t)
			defer func() { pin.Leave(); _ = release() }()
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = pin.CheckRouteActivationTx(tx, claim, fixture.deploymentID)
			if test.want == nil && err != nil {
				t.Fatalf("route activation check error=%v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("route activation check error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestClaimAndGuardRequireExactIdentityAndComposition(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	if _, err := deploymentexecution.NewClaim(fixture.binding, fixture.job.ID, fixture.appID, 0); !errors.Is(err, deploymentexecution.ErrInvalidClaim) {
		t.Fatalf("zero attempt error=%v", err)
	}
	if _, err := deploymentexecution.NewClaim(fixture.binding, "wrong", fixture.appID, 1); !errors.Is(err, deploymentexecution.ErrInvalidClaim) {
		t.Fatalf("wrong job error=%v", err)
	}
	if _, err := deploymentexecution.NewClaim(fixture.binding, fixture.job.ID, "wrong", 1); !errors.Is(err, deploymentexecution.ErrInvalidClaim) {
		t.Fatalf("wrong application error=%v", err)
	}
	foreign := newExecutionFixture(t, false)
	workingDirectory := filepath.Clean(t.TempDir())
	if _, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, "relative"); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("relative directory error=%v", err)
	}
	if _, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, foreign.positive, workingDirectory); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("foreign validator error=%v", err)
	}
	if _, err := deploymentexecution.NewGuard(fixture.db, nil, fixture.positive, workingDirectory); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("nil bindings error=%v", err)
	}
	if _, err := deploymentexecution.NewGuard(fixture.db, foreign.bindings, fixture.positive, workingDirectory); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("foreign bindings error=%v", err)
	}
	var typedNil *mutablePositiveFence
	if _, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, typedNil, workingDirectory); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("typed nil validator error=%v", err)
	}
	guard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, workingDirectory)
	if err != nil || !guard.UsesDatabase(fixture.db) || guard.UsesDatabase(foreign.db) {
		t.Fatalf("guard=%#v same=%t foreign=%t error=%v", guard, guard != nil && guard.UsesDatabase(fixture.db), guard != nil && guard.UsesDatabase(foreign.db), err)
	}
	if _, err := guard.PinScope(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("missing scope error=%v", err)
	}
	var nilGuard *deploymentexecution.Guard
	if nilGuard.UsesDatabase(fixture.db) || guard.UsesDatabase(nil) {
		t.Fatal("nil guard or database reported configured")
	}
	mutable := &mutablePositiveFence{db: fixture.db, configured: true}
	mutableGuard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, mutable, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	mutable.configured = false
	if mutableGuard.UsesDatabase(fixture.db) {
		t.Fatal("guard retained a stale validator configuration")
	}
	bindingValidator := &mappingLifecycleValidator{db: fixture.db, completion: deploymentbinding.CompletionComplete}
	mutableBindings, err := deploymentbinding.New(fixture.db, bindingValidator)
	if err != nil {
		t.Fatal(err)
	}
	bindingGuard, err := deploymentexecution.NewGuard(fixture.db, mutableBindings, fixture.positive, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	bindingValidator.db = nil
	if bindingGuard.UsesDatabase(fixture.db) {
		t.Fatal("guard retained a stale binding-validator configuration")
	}
}

func TestGuardConfiguredForDomainRequiresExactDatabaseDirectoryAndDependencies(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	foreign := newExecutionFixture(t, false)
	workingDirectory := filepath.Clean(t.TempDir())
	guard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !guard.ConfiguredForDomain(fixture.db, workingDirectory) {
		t.Fatal("guard rejected its exact domain configuration")
	}
	alias := guardDomainWorkingDirectoryAlias(t, workingDirectory)
	for name, request := range map[string]struct {
		db        *sql.DB
		directory string
	}{
		"foreign database": {db: foreign.db, directory: workingDirectory},
		"wrong directory":  {db: fixture.db, directory: t.TempDir()},
		"alias directory":  {db: fixture.db, directory: alias},
		"noncanonical directory": {
			db: fixture.db, directory: workingDirectory + string(filepath.Separator) + ".",
		},
		"relative directory": {db: fixture.db, directory: "relative"},
	} {
		t.Run(name, func(t *testing.T) {
			if guard.ConfiguredForDomain(request.db, request.directory) {
				t.Fatal("mismatched domain configuration accepted")
			}
		})
	}

	unsupported := guardUnsupportedDomainWorkingDirectory()
	unsupportedGuard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, unsupported)
	if err != nil {
		t.Fatalf("construct legacy-compatible guard for unsupported-domain check: %v", err)
	}
	if unsupportedGuard.ConfiguredForDomain(fixture.db, unsupported) {
		t.Fatal("unsupported working-directory spelling accepted for domain configuration")
	}

	mutable := &mutablePositiveFence{db: fixture.db, configured: true}
	mutableGuard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, mutable, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !mutableGuard.ConfiguredForDomain(fixture.db, workingDirectory) {
		t.Fatal("guard rejected configured concrete dependency")
	}
	mutable.configured = false
	if mutableGuard.ConfiguredForDomain(fixture.db, workingDirectory) {
		t.Fatal("guard accepted changed concrete dependency")
	}
	var missing *deploymentexecution.Guard
	if missing.ConfiguredForDomain(fixture.db, workingDirectory) || guard.ConfiguredForDomain(nil, workingDirectory) {
		t.Fatal("nil guard or database reported domain configuration")
	}
}

func guardDomainWorkingDirectoryAlias(t *testing.T, directory string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		alias := strings.ToUpper(directory)
		if alias == directory {
			alias = strings.ToLower(directory)
		}
		if alias == directory {
			t.Fatal("test directory has no alternate Windows case spelling")
		}
		return alias
	}
	alias := filepath.Join(t.TempDir(), "effects-alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func guardUnsupportedDomainWorkingDirectory() string {
	if runtime.GOOS == "windows" {
		return `\\domain.invalid\share\effects`
	}
	return `/??/effects`
}

func TestGuardPolicyGateRejectsMutableIdentityAndStageChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *executionFixture)
		want   error
	}{
		{name: "job attempt", mutate: func(t *testing.T, f *executionFixture) { f.exec(t, `UPDATE jobs SET attempt=2 WHERE id=?`, f.job.ID) }, want: deploymentexecution.ErrIdentityLost},
		{name: "job status", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `UPDATE jobs SET status='assigned' WHERE id=?`, f.job.ID)
		}, want: deploymentexecution.ErrIdentityLost},
		{name: "cancelling phase", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `UPDATE jobs SET status='waiting_external',phase='cancelling' WHERE id=?`, f.job.ID)
		}, want: deploymentexecution.ErrIdentityLost},
		{name: "terminal job", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, f.job.ID)
		}, want: deploymentexecution.ErrIdentityLost},
		{name: "wrong job type", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `UPDATE jobs SET type='restart' WHERE id=?`, f.job.ID)
		}, want: deploymentexecution.ErrAuthorityInvalid},
		{name: "wrong job resource type", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `UPDATE jobs SET resource_type='machine' WHERE id=?`, f.job.ID)
		}, want: deploymentexecution.ErrAuthorityInvalid},
		{name: "wrong job resource id", mutate: func(t *testing.T, f *executionFixture) {
			otherApp := uuid.NewString()
			f.exec(t, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, otherApp, otherApp, otherApp, f.stamp, f.stamp)
			f.exec(t, `UPDATE jobs SET resource_id=? WHERE id=?`, otherApp, f.job.ID)
		}, want: deploymentexecution.ErrAuthorityInvalid},
		{name: "wrong deployment", mutate: func(t *testing.T, f *executionFixture) {
			f.deploymentID = uuid.NewString()
		}, want: deploymentexecution.ErrIdentityLost},
		{name: "wrong deployment application", mutate: func(t *testing.T, f *executionFixture) {
			otherApp := uuid.NewString()
			f.exec(t, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, otherApp, otherApp, otherApp, f.stamp, f.stamp)
			f.exec(t, `DROP TRIGGER deployment_identity_immutable`)
			f.exec(t, `DROP TRIGGER deployment_linkage_valid_update`)
			f.exec(t, `DROP TRIGGER deployment_runtime_provenance_valid_update`)
			f.exec(t, `UPDATE deployments SET app_id=? WHERE id=?`, otherApp, f.deploymentID)
		}, want: deploymentexecution.ErrIdentityLost},
		{name: "wrong deployment job", mutate: func(t *testing.T, f *executionFixture) {
			otherJob := uuid.NewString()
			f.exec(t, `INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'succeeded','succeeded',1,?,'{}',?,?)`, otherJob, f.appID, f.actorID, f.stamp, f.stamp)
			f.exec(t, `DROP TRIGGER deployment_identity_immutable`)
			f.exec(t, `UPDATE deployments SET job_id=? WHERE id=?`, otherJob, f.deploymentID)
		}, want: deploymentexecution.ErrIdentityLost},
		{name: "uninitialized provenance", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `DROP TRIGGER deployment_identity_immutable`)
			f.exec(t, `UPDATE deployments SET provenance_initialized=0 WHERE id=?`, f.deploymentID)
		}, want: deploymentexecution.ErrStageInvalid},
		{name: "unsupported stage", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `UPDATE deployments SET status='succeeded' WHERE id=?`, f.deploymentID)
		}, want: deploymentexecution.ErrStageInvalid},
		{name: "missing binding", mutate: func(t *testing.T, f *executionFixture) {
			f.exec(t, `DROP TRIGGER application_deployment_lifecycle_binding_retain`)
			f.exec(t, `DELETE FROM application_deployment_lifecycle_bindings WHERE job_id=?`, f.job.ID)
		}, want: deploymentexecution.ErrAuthorityInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			pin, claim := fixture.pin(t)
			defer pin.Leave()
			test.mutate(t, fixture)
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestGuardPolicyGatePreservesInvalidHistoricalAuthorityBeforeDrift(t *testing.T) {
	fixture := newExecutionFixture(t, true)
	pin, claim := fixture.pin(t)
	defer pin.Leave()
	fixture.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, fixture.stamp, fixture.stamp, fixture.job.ID)
	fixture.completeOperation(t, applifecycle.Restart, applifecycle.BaselineRunning, 1, "later-before-corruption")
	fixture.exec(t, `DROP TRIGGER application_lifecycle_effect_receipt_immutable`)
	fixture.exec(t, `UPDATE application_lifecycle_effect_receipts SET proof_digest=? WHERE job_id=? AND boundary='complete'`, strings.Repeat("0", 64), fixture.binding.AuthorizingLifecycleJobID())
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID); !errors.Is(err, deploymentexecution.ErrAuthorityInvalid) || errors.Is(err, deploymentexecution.ErrAuthorityStale) {
		t.Fatalf("corrupt historical authority error=%v", err)
	}
	if err := pin.CheckGeneratedNetworkCreateTx(tx, claim, fixture.deploymentID); !errors.Is(err, deploymentexecution.ErrAuthorityInvalid) || errors.Is(err, deploymentexecution.ErrAuthorityStale) {
		t.Fatalf("corrupt historical network authority error=%v", err)
	}
}

func TestGuardPolicyGatePreservesDependencyCausesAndPostCallbackCancellation(t *testing.T) {
	t.Run("unresolved mapping", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		validator := &mappingLifecycleValidator{db: fixture.db, completion: deploymentbinding.CompletionComplete}
		fixture.replaceBindingRepository(t, validator)
		validator.historicalErr = deploymentbinding.ErrLifecycleUnresolved
		fixture.check(t, deploymentexecution.ErrAuthorityUnresolved)
	})
	t.Run("unavailable mapping", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		validator := &mappingLifecycleValidator{db: fixture.db, completion: deploymentbinding.CompletionComplete}
		fixture.replaceBindingRepository(t, validator)
		validator.historicalErr = deploymentbinding.ErrUnavailable
		fixture.check(t, deploymentexecution.ErrUnavailable)
	})
	t.Run("binding callback cancellation", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		validator := &mappingLifecycleValidator{db: fixture.db, completion: deploymentbinding.CompletionComplete}
		fixture.replaceBindingRepository(t, validator)
		ctx, cancel := context.WithCancel(context.Background())
		validator.cancel = cancel
		guard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, fixture.directory)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := deploymentexecution.NewClaim(fixture.binding, fixture.job.ID, fixture.appID, 1)
		if err != nil {
			t.Fatal(err)
		}
		scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		pin, err := guard.PinScope(deploymenteffects.WithScope(ctx, scope))
		if err != nil {
			t.Fatal(err)
		}
		defer pin.Leave()
		tx, err := fixture.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID); !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	})
}

func TestGuardRefusesForeignBindingsAndInvalidScopes(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	foreign := newExecutionFixture(t, false)
	guard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	var nilPin *deploymentexecution.Pinned
	if err := nilPin.CheckPolicyGateTx(nil, deploymentexecution.Claim{}, ""); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("nil pin error=%v", err)
	}
	zeroPin := deploymentexecution.Pinned{}
	if err := zeroPin.CheckPolicyGateTx(nil, deploymentexecution.Claim{}, ""); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("zero pin error=%v", err)
	}
	foreignScope, foreignRelease, err := deploymenteffects.AcquireScoped(context.Background(), foreign.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignRelease()
	if _, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), foreignScope)); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("foreign scope error=%v", err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.PinScope(ctx); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("released scope error=%v", err)
	}
	foreignClaim, err := deploymentexecution.NewClaim(foreign.binding, foreign.job.ID, foreign.appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	pin, _, release := fixture.pinWithRelease(t)
	defer release()
	defer pin.Leave()
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := pin.CheckPolicyGateTx(tx, foreignClaim, fixture.deploymentID); !errors.Is(err, deploymentexecution.ErrAuthorityInvalid) {
		t.Fatalf("foreign binding error=%v", err)
	}
}

func TestGuardPinScopeRefusesOriginalScopeWhileItIsClosing(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	guard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, fixture.positive, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	hold, err := deploymenteffects.EnterScope(ctx, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- release() }()
	deadline := time.Now().Add(time.Second)
	for {
		entered, err := deploymenteffects.EnterScope(ctx, fixture.directory)
		if errors.Is(err, deploymenteffects.ErrScopeUnavailable) {
			break
		}
		if err != nil {
			hold()
			t.Fatal(err)
		}
		entered()
		if time.Now().After(deadline) {
			hold()
			t.Fatal("outer scope did not enter closing state")
		}
	}
	if _, err := guard.PinScope(ctx); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		hold()
		t.Fatalf("closing original scope error=%v", err)
	}
	hold()
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("outer scope did not release")
	}
}

func TestGuardPolicyGateRefusesLaterRunningAuthorityAsStale(t *testing.T) {
	for _, transition := range []string{"restart", "stop-start", "unresolved"} {
		t.Run(transition, func(t *testing.T) {
			fixture := newExecutionFixture(t, true)
			pin, claim := fixture.pin(t)
			defer pin.Leave()
			fixture.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, fixture.stamp, fixture.stamp, fixture.job.ID)
			switch transition {
			case "restart":
				fixture.completeOperation(t, applifecycle.Restart, applifecycle.BaselineRunning, 1, "later-restart")
			case "stop-start":
				fixture.completeOperation(t, applifecycle.Stop, applifecycle.BaselineRunning, 1, "later-stop")
				fixture.completeOperation(t, applifecycle.Start, applifecycle.BaselineStopped, 2, "later-start")
			case "unresolved":
				pins := fixture.pins
				pins.BaselineDisposition = applifecycle.BaselineRunning
				accepted, err := fixture.lifecycle.Accept(context.Background(), fixture.jobs, applifecycle.LifecycleRequest{
					ApplicationID: fixture.appID, ActorID: fixture.actorID, IdempotencyKey: "later-unresolved",
					Action: applifecycle.Restart, ExpectedDesiredRevision: 1,
				}, pins)
				if err != nil || !accepted.Created {
					t.Fatalf("unresolved accepted=%#v error=%v", accepted, err)
				}
			}
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID)
			if !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
				t.Fatalf("stale error=%v", err)
			}
		})
	}
}

func TestGuardPolicyGateDoesNotCacheSuccessfulAuthority(t *testing.T) {
	fixture := newExecutionFixture(t, true)
	pin, claim := fixture.pin(t)
	defer pin.Leave()
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("initial check: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	fixture.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, fixture.stamp, fixture.stamp, fixture.job.ID)
	fixture.completeOperation(t, applifecycle.Restart, applifecycle.BaselineRunning, 1, "after-successful-check")
	fixture.exec(t, `UPDATE jobs SET status='running',phase='running',attempt=1,finished_at=NULL WHERE id=?`, fixture.job.ID)
	tx, err = fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := pin.CheckPolicyGateTx(tx, claim, fixture.deploymentID); !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
		t.Fatalf("fresh drift check error=%v", err)
	}
}

func TestGuardComposeUpDoesNotCacheBindingOrGlobalFence(t *testing.T) {
	t.Run("later lifecycle authority is stale", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		fixture.exec(t, `UPDATE deployments SET status='applying' WHERE id=?`, fixture.deploymentID)
		pin, claim := fixture.pin(t)
		defer pin.Leave()
		check := func() error {
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			return pin.CheckComposeUpTx(tx, claim, fixture.deploymentID)
		}
		if err := check(); err != nil {
			t.Fatalf("initial Compose check: %v", err)
		}
		fixture.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, fixture.stamp, fixture.stamp, fixture.job.ID)
		fixture.completeOperation(t, applifecycle.Restart, applifecycle.BaselineRunning, 1, "after-compose-gate")
		fixture.exec(t, `UPDATE jobs SET status='running',phase='running',attempt=1,finished_at=NULL WHERE id=?`, fixture.job.ID)
		if err := check(); !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
			t.Fatalf("fresh Compose authority error=%v", err)
		}
	})

	t.Run("global pending start is observed", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		fixture.exec(t, `UPDATE deployments SET status='applying' WHERE id=?`, fixture.deploymentID)
		fixture.beginOtherStart(t, true)
		pin, claim := fixture.pin(t)
		defer pin.Leave()
		tx, err := fixture.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := pin.CheckComposeUpTx(tx, claim, fixture.deploymentID); !errors.Is(err, deploymentexecution.ErrPositiveFence) {
			t.Fatalf("fresh Compose fence error=%v", err)
		}
	})
}

func TestGuardPolicyGateUsesCanonicalGlobalPendingStartFence(t *testing.T) {
	t.Run("non-start boundary does not block", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		fixture.beginOtherStart(t, false)
		fixture.check(t, nil)
	})
	t.Run("pending start blocks and receipt clears", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		pending := fixture.beginOtherStart(t, true)
		fixture.check(t, deploymentexecution.ErrPositiveFence)
		fixture.checkGeneratedNetwork(t, deploymentexecution.ErrPositiveFence)
		pending.observeStart(t, fixture)
		fixture.check(t, nil)
		fixture.checkGeneratedNetwork(t, nil)
	})
	t.Run("terminal operation behind later head still blocks", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		pending := fixture.beginOtherStart(t, true)
		fixture.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, fixture.stamp, fixture.stamp, pending.operation.JobID)
		pins := pending.operation.Pins
		pins.BaselineDisposition = applifecycle.BaselineRunning
		accepted, err := fixture.lifecycle.Accept(context.Background(), fixture.jobs, applifecycle.LifecycleRequest{
			ApplicationID: pending.appID, ActorID: fixture.actorID, IdempotencyKey: "other-later-head",
			Action: applifecycle.Stop, ExpectedDesiredRevision: 1,
		}, pins)
		if err != nil || !accepted.Created {
			t.Fatalf("later operation=%#v error=%v", accepted, err)
		}
		fixture.check(t, deploymentexecution.ErrPositiveFence)
	})
	t.Run("malformed matching receipt refuses", func(t *testing.T) {
		fixture := newExecutionFixture(t, true)
		pending := fixture.beginOtherStart(t, true)
		pending.observeStart(t, fixture)
		fixture.exec(t, `DROP TRIGGER application_lifecycle_effect_receipt_immutable`)
		fixture.exec(t, `UPDATE application_lifecycle_effect_receipts SET proof_digest=? WHERE job_id=? AND boundary='start'`, strings.Repeat("0", 64), pending.operation.JobID)
		fixture.check(t, deploymentexecution.ErrAuthorityInvalid)
	})
}

type executionFixture struct {
	db           *sql.DB
	jobs         *jobs.Service
	lifecycle    *applifecycle.Repository
	bindings     *deploymentbinding.Repository
	positive     *applifecycle.PositiveFenceValidator
	appID        string
	actorID      string
	stamp        string
	job          jobs.Job
	binding      deploymentbinding.Binding
	deploymentID string
	pins         applifecycle.GeneratedLifecyclePinsV1
	directory    string
}

type pendingOtherStart struct {
	appID     string
	operation applifecycle.Operation
	claim     applifecycle.WorkerClaim
	intent    applifecycle.Intent
}

type mappingLifecycleValidator struct {
	db            *sql.DB
	completion    deploymentbinding.Completion
	historicalErr error
	currentErr    error
	unresolved    bool
	unresolvedErr error
	cancel        context.CancelFunc
}

type mutablePositiveFence struct {
	db         *sql.DB
	configured bool
}

func (v *mutablePositiveFence) ConfiguredFor(db *sql.DB) bool {
	return v != nil && v.configured && v.db == db
}

func (*mutablePositiveFence) PendingStartExistsTx(context.Context, *sql.Tx) (bool, error) {
	return false, nil
}

func (v *mappingLifecycleValidator) ConfiguredFor(db *sql.DB) bool { return v != nil && v.db == db }
func (v *mappingLifecycleValidator) HistoricalBindingTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) error {
	return v.historicalErr
}
func (v *mappingLifecycleValidator) CurrentOperationTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) (deploymentbinding.Completion, error) {
	return v.completion, v.currentErr
}
func (v *mappingLifecycleValidator) UnresolvedHistoryTx(context.Context, *sql.Tx, string) (bool, error) {
	if v.cancel != nil {
		v.cancel()
	}
	return v.unresolved, v.unresolvedErr
}

func newExecutionFixture(t *testing.T, running bool) *executionFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &executionFixture{db: db, jobs: jobs.New(db), lifecycle: applifecycle.New(db), appID: uuid.NewString(), actorID: uuid.NewString(), stamp: time.Now().UTC().Format(time.RFC3339Nano), directory: directories.WorkingDirectory}
	f.exec(t, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, f.actorID, f.actorID, "hash", f.stamp, f.stamp)
	f.exec(t, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, f.appID, f.appID, f.appID, f.stamp, f.stamp)
	if running {
		f.pins = f.prepareRunningPinsFor(t, f.appID)
		f.completeOperation(t, applifecycle.Start, applifecycle.BaselineRunning, 0, "initial-running")
	}
	bindingValidator, err := applifecycle.NewDeploymentBindingValidator(f.lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	f.bindings, err = deploymentbinding.New(db, bindingValidator)
	if err != nil {
		t.Fatal(err)
	}
	f.jobs, err = jobs.NewWithDeploymentBindings(db, f.bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: f.appID, IdempotencyKey: "ordinary-deployment", RequestedBy: f.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create deployment job created=%t error=%v", created.Created, err)
	}
	f.job, f.binding = created.Job, created.Binding
	f.exec(t, `UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, f.stamp, f.stamp, f.job.ID)
	f.deploymentID = uuid.NewString()
	f.exec(t, `INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,started_at) VALUES(?,?,?,'preparing','current',1,?)`, f.deploymentID, f.appID, f.job.ID, f.stamp)
	f.positive, err = applifecycle.NewPositiveFenceValidator(f.lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *executionFixture) pin(t *testing.T) (*deploymentexecution.Pinned, deploymentexecution.Claim) {
	t.Helper()
	pin, claim, release := f.pinWithRelease(t)
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	})
	return pin, claim
}

func (f *executionFixture) pinWithRelease(t *testing.T) (*deploymentexecution.Pinned, deploymentexecution.Claim, func() error) {
	t.Helper()
	guard, err := deploymentexecution.NewGuard(f.db, f.bindings, f.positive, f.directory)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(f.binding, f.job.ID, f.appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), f.directory)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), scope))
	if err != nil {
		t.Fatal(err)
	}
	return pin, claim, release
}

func (f *executionFixture) check(t *testing.T, want error) {
	t.Helper()
	pin, claim, release := f.pinWithRelease(t)
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = pin.CheckPolicyGateTx(tx, claim, f.deploymentID)
	pin.Leave()
	if releaseErr := release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if want == nil && err != nil {
		t.Fatalf("policy gate error=%v", err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("policy gate error=%v want=%v", err, want)
	}
}

func (f *executionFixture) checkGeneratedNetwork(t *testing.T, want error) {
	t.Helper()
	f.exec(t, `UPDATE deployments SET status='applying' WHERE id=?`, f.deploymentID)
	pin, claim, release := f.pinWithRelease(t)
	defer func() {
		pin.Leave()
		if err := release(); err != nil {
			t.Errorf("release generated network scope: %v", err)
		}
	}()
	for _, check := range []func(*sql.Tx, deploymentexecution.Claim, string) error{
		pin.CheckGeneratedNetworkCreateTx, pin.CheckGeneratedNetworkObservationTx,
	} {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		err = check(tx, claim, f.deploymentID)
		_ = tx.Rollback()
		if want == nil && err != nil {
			t.Fatalf("generated network check error=%v", err)
		}
		if want != nil && !errors.Is(err, want) {
			t.Fatalf("generated network check error=%v want=%v", err, want)
		}
	}
}

func (f *executionFixture) replaceBindingRepository(t *testing.T, validator deploymentbinding.LifecycleTxValidator) {
	t.Helper()
	f.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, f.stamp, f.stamp, f.job.ID)
	f.exec(t, `UPDATE deployments SET status='failed',finished_at=? WHERE id=?`, f.stamp, f.deploymentID)
	bindings, err := deploymentbinding.New(f.db, validator)
	if err != nil {
		t.Fatal(err)
	}
	typedJobs, err := jobs.NewWithDeploymentBindings(f.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := typedJobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: f.appID, IdempotencyKey: "mapped-ordinary-deployment", RequestedBy: f.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("mapped deployment job created=%t error=%v", created.Created, err)
	}
	f.bindings, f.jobs, f.job, f.binding = bindings, typedJobs, created.Job, created.Binding
	f.exec(t, `UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, f.stamp, f.stamp, f.job.ID)
	f.deploymentID = uuid.NewString()
	f.exec(t, `INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,started_at) VALUES(?,?,?,'preparing','current',1,?)`, f.deploymentID, f.appID, f.job.ID, f.stamp)
}

func (f *executionFixture) beginOtherStart(t *testing.T, completeWithdraw bool) pendingOtherStart {
	t.Helper()
	appID := uuid.NewString()
	f.exec(t, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, appID, appID, appID, f.stamp, f.stamp)
	pins := f.prepareRunningPinsFor(t, appID)
	pins.BaselineDisposition = applifecycle.BaselineStopped
	accepted, err := f.lifecycle.Accept(context.Background(), f.jobs, applifecycle.LifecycleRequest{
		ApplicationID: appID, ActorID: f.actorID, IdempotencyKey: "other-pending-start",
		Action: applifecycle.Start, ExpectedDesiredRevision: 0,
	}, pins)
	if err != nil || !accepted.Created {
		t.Fatalf("other operation=%#v error=%v", accepted, err)
	}
	f.exec(t, `UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, accepted.Operation.JobID)
	job, err := f.jobs.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(job)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	request := applifecycle.EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: appID, DesiredRevision: accepted.Operation.DesiredRevision, Boundary: applifecycle.BoundaryWithdraw, Mode: applifecycle.EffectObservationOnly, PinsDigest: accepted.Operation.PinsDigest}
	withdraw, created, err := f.lifecycle.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("begin withdraw created=%t error=%v", created, err)
	}
	if completeWithdraw {
		proof := applifecycle.EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: appID, DesiredRevision: accepted.Operation.DesiredRevision, Boundary: applifecycle.BoundaryWithdraw, Outcome: applifecycle.OutcomeRoutesAbsent, IntentDigest: withdraw.RequestDigest, EvidenceDigest: strings.Repeat("e", 64), PinsDigest: accepted.Operation.PinsDigest}
		if _, observed, err := f.lifecycle.ObserveBoundaryTx(context.Background(), tx, claim, withdraw, proof); err != nil || !observed {
			_ = tx.Rollback()
			t.Fatalf("observe withdraw created=%t error=%v", observed, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result := pendingOtherStart{appID: appID, operation: accepted.Operation, claim: claim}
	if !completeWithdraw {
		return result
	}
	tx, err = f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	component := accepted.Operation.Pins.Components[0].Name
	request = applifecycle.EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: appID, DesiredRevision: accepted.Operation.DesiredRevision, Boundary: applifecycle.BoundaryStart, ComponentName: component, Mode: applifecycle.EffectCommand, PinsDigest: accepted.Operation.PinsDigest}
	result.intent, created, err = f.lifecycle.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("begin start created=%t error=%v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func (p pendingOtherStart) observeStart(t *testing.T, f *executionFixture) {
	t.Helper()
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	proof := applifecycle.EffectProofV1{Version: 1, OperationID: p.operation.JobID, ApplicationID: p.appID, DesiredRevision: p.operation.DesiredRevision, Boundary: applifecycle.BoundaryStart, ComponentName: p.intent.Key.ComponentName, Outcome: applifecycle.OutcomeContainerRunning, IntentDigest: p.intent.RequestDigest, EvidenceDigest: strings.Repeat("e", 64), PinsDigest: p.operation.PinsDigest}
	if _, observed, err := f.lifecycle.ObserveBoundaryTx(context.Background(), tx, p.claim, p.intent, proof); err != nil || !observed {
		_ = tx.Rollback()
		t.Fatalf("observe start created=%t error=%v", observed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f *executionFixture) completeOperation(t *testing.T, action applifecycle.Action, baseline applifecycle.BaselineDisposition, expected int64, key string) applifecycle.Operation {
	t.Helper()
	pins := f.pins
	pins.BaselineDisposition = baseline
	accepted, err := f.lifecycle.Accept(context.Background(), f.jobs, applifecycle.LifecycleRequest{ApplicationID: f.appID, ActorID: f.actorID, IdempotencyKey: key, Action: action, ExpectedDesiredRevision: expected}, pins)
	if err != nil || !accepted.Created {
		t.Fatalf("accept operation=%#v error=%v", accepted, err)
	}
	f.exec(t, `UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, accepted.Operation.JobID)
	job, err := f.jobs.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range executionEffectSpecs(accepted.Operation) {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		request := applifecycle.EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: f.appID, DesiredRevision: accepted.Operation.DesiredRevision, Boundary: spec.boundary, ComponentName: spec.component, Mode: spec.mode, PinsDigest: accepted.Operation.PinsDigest}
		intent, created, err := f.lifecycle.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request)
		if err != nil || !created {
			_ = tx.Rollback()
			t.Fatalf("begin %s/%s created=%t error=%v", spec.boundary, spec.component, created, err)
		}
		proof := applifecycle.EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: f.appID, DesiredRevision: accepted.Operation.DesiredRevision, Boundary: spec.boundary, ComponentName: spec.component, Outcome: spec.outcome, IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("e", 64), PinsDigest: accepted.Operation.PinsDigest}
		if _, observed, err := f.lifecycle.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err != nil || !observed {
			_ = tx.Rollback()
			t.Fatalf("observe %s/%s created=%t error=%v", spec.boundary, spec.component, observed, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	f.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, f.stamp, f.stamp, accepted.Operation.JobID)
	return accepted.Operation
}

func (f *executionFixture) prepareRunningPinsFor(t *testing.T, appID string) applifecycle.GeneratedLifecyclePinsV1 {
	t.Helper()
	planID, releaseID, deploymentID, deployJobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	f.exec(t, `INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,0,'',?,?,'accepted',?,?)`, planID, appID, "apps/"+appID+"/plan.json", strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), f.actorID, f.stamp, f.actorID, f.stamp)
	f.exec(t, `INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, appID, f.stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), planID)
	f.exec(t, `INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'running','running',?,'{}',?,?)`, deployJobID, appID, f.actorID, f.stamp, f.stamp)
	f.exec(t, `INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, appID, releaseID, deployJobID, planID)
	runtimeRepository := generatedruntimestate.New(f.db)
	value, created, err := runtimeRepository.Begin(context.Background(), generatedruntimestate.BeginInput{DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"}})
	if err != nil || !created {
		t.Fatalf("runtime begin=%#v created=%t error=%v", value, created, err)
	}
	artifactID, containerID, definition, image := uuid.NewString(), strings.Repeat("6", 64), strings.Repeat("7", 64), "sha256:"+strings.Repeat("8", 64)
	f.exec(t, `INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, "api", definition, image, f.stamp, f.stamp, f.stamp)
	if _, err := runtimeRepository.SetImageReady(context.Background(), appID, deploymentID, "api", artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRepository.SetContainerStarting(context.Background(), appID, deploymentID, "api", "rig-api-retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRepository.SetContainerRunning(context.Background(), appID, deploymentID, "api", containerID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRepository.AdvanceComponent(context.Background(), appID, deploymentID, "api", generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute} {
		value, err = runtimeRepository.Advance(context.Background(), appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtimeRepository.SwitchActive(context.Background(), appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("active head=%#v changed=%t error=%v", head, changed, err)
	}
	value, err = runtimeRepository.Advance(context.Background(), appID, deploymentID, value.Phase, generatedruntimestate.PhaseDraining, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtimeRepository.Advance(context.Background(), appID, deploymentID, value.Phase, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE deployments SET status='succeeded',started_at=?,finished_at=? WHERE id=?`, f.stamp, f.stamp, deploymentID)
	f.exec(t, `UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, deployJobID)
	return applifecycle.GeneratedLifecyclePinsV1{Version: 1, DeploymentID: deploymentID, ReleaseID: releaseID, Slot: head.Slot, ActiveGeneration: head.Generation, PlanRevision: applifecycle.RevisionRef{ID: planID, Number: 1}, Configuration: applifecycle.ConfigurationRef{PlanRevision: applifecycle.RevisionRef{ID: planID, Number: 1}, RuntimeComponents: []string{"api"}}, BaselineDisposition: applifecycle.BaselineRunning, Components: []applifecycle.RetainedComponent{{Name: "api", Role: "service", Technology: "node", ContainerID: containerID, ContainerName: "rig-api-retained", ArtifactID: artifactID, ImageContentID: image, BuildDefinitionDigest: definition, NetworkName: "rig", NetworkAlias: "api", InternalPort: 8080, WorkingDirectory: "/app", RunCommandDigest: strings.Repeat("9", 64), Limits: applifecycle.RetainedLimits{MemoryBytes: 128 << 20, MemorySwapBytes: 256 << 20, CPUQuota: 50000, CPUPeriod: 100000, PIDsLimit: 128, TmpfsBytes: 16 << 20, LogDriver: "local", LogMaxSize: "10m", LogMaxFiles: 3, HardeningPolicyVersion: "v1"}}}, Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind", SelectedLineageGeneration: 1, SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("a", 64), ProtectedFormatVersion: 2, TargetRecordDigest: strings.Repeat("b", 64)}}
}

type executionEffectSpec struct {
	boundary  applifecycle.Boundary
	component string
	mode      applifecycle.EffectMode
	outcome   applifecycle.Outcome
}

func executionEffectSpecs(operation applifecycle.Operation) []executionEffectSpec {
	component := operation.Pins.Components[0].Name
	var specs []executionEffectSpec
	if !(operation.Action == applifecycle.Start && operation.Pins.BaselineDisposition == applifecycle.BaselineRunning) {
		mode := applifecycle.EffectCommand
		if operation.Pins.BaselineDisposition == applifecycle.BaselineStopped {
			mode = applifecycle.EffectObservationOnly
		}
		specs = append(specs, executionEffectSpec{applifecycle.BoundaryWithdraw, "", mode, applifecycle.OutcomeRoutesAbsent})
	}
	if operation.Action == applifecycle.Stop || operation.Action == applifecycle.Restart && operation.Pins.BaselineDisposition == applifecycle.BaselineRunning {
		specs = append(specs, executionEffectSpec{applifecycle.BoundaryStop, component, applifecycle.EffectCommand, applifecycle.OutcomeContainerStopped})
	}
	if operation.Action == applifecycle.Restart || operation.Action == applifecycle.Start && operation.Pins.BaselineDisposition == applifecycle.BaselineStopped {
		specs = append(specs, executionEffectSpec{applifecycle.BoundaryStart, component, applifecycle.EffectCommand, applifecycle.OutcomeContainerRunning})
	}
	if operation.Action == applifecycle.Start || operation.Action == applifecycle.Restart {
		specs = append(specs, executionEffectSpec{applifecycle.BoundaryHealth, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeHealthy}, executionEffectSpec{applifecycle.BoundaryPublish, "", applifecycle.EffectCommand, applifecycle.OutcomeServing})
	}
	complete := applifecycle.OutcomeCompleteRunning
	if operation.Action == applifecycle.Stop {
		complete = applifecycle.OutcomeCompleteStopped
	}
	return append(specs, executionEffectSpec{applifecycle.BoundaryComplete, "", applifecycle.EffectObservationOnly, complete})
}

func (f *executionFixture) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}
