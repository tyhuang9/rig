package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type gatewayRebindCanonicalEventRepository struct {
	*gatewayRebindAdmissionRepositoryFake
}

func (r *gatewayRebindCanonicalEventRepository) ClaimGatewayRebindV2(ctx context.Context,
	proposal appaccess.GatewayRebindPreclaimProposalV2,
) (appaccess.GatewayRebindClaimV2, bool, error) {
	claim, created, err := r.gatewayRebindAdmissionRepositoryFake.ClaimGatewayRebindV2(ctx, proposal)
	if err != nil || !created || r.snapshot.Active == nil {
		return claim, created, err
	}
	event := appaccess.GatewayRebindEvent{OperationID: claim.Spec.OperationID, Sequence: 1,
		State: appaccess.GatewayRebindPrepared, CreatedAt: claim.CreatedAt}
	r.snapshot.Active.Events = []appaccess.GatewayRebindEvent{event}
	r.snapshot.History[len(r.snapshot.History)-1].Events = []appaccess.GatewayRebindEvent{event}
	return claim, created, nil
}

type gatewayRebindAdmissionRepositoryView struct {
	gatewayRebindAdmissionRepository
	mutateSnapshot func(*appaccess.GatewayRebindRecoverySnapshot)
	mutateHeads    func(*[]appaccess.GatewayRebindRuntimeHead)
}

func (r gatewayRebindAdmissionRepositoryView) GatewayRebindRecoverySnapshot(ctx context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	value, err := r.gatewayRebindAdmissionRepository.GatewayRebindRecoverySnapshot(ctx)
	if err == nil && r.mutateSnapshot != nil {
		r.mutateSnapshot(&value)
	}
	return value, err
}

func (r gatewayRebindAdmissionRepositoryView) GatewayRebindRuntimeHeads(ctx context.Context) (
	[]appaccess.GatewayRebindRuntimeHead, error,
) {
	value, err := r.gatewayRebindAdmissionRepository.GatewayRebindRuntimeHeads(ctx)
	if err == nil && r.mutateHeads != nil {
		snapshot, snapshotErr := r.gatewayRebindAdmissionRepository.GatewayRebindRecoverySnapshot(ctx)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if snapshot.Active != nil {
			r.mutateHeads(&value)
		}
	}
	return value, err
}

func gatewayRebindAdmissionTestReads(spec appaccess.GatewayRebindSpecV2) gatewayRebindSuccessorPreflightReads {
	selected := netip.MustParseAddr(spec.SuccessorProfile.SelectedIPv4)
	return gatewayRebindSuccessorPreflightReads{network: gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return []hostNetworkCandidate{{InterfaceID: spec.SuccessorProfile.InterfaceID,
				IPv4: selected.String(), Prefix: netip.PrefixFrom(selected, 24).Masked()}}, nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{Routes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/28")}}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			return []netip.Prefix{netip.MustParsePrefix("10.0.0.16/28")}, nil
		},
	}, dockerIDs: func(context.Context) ([]string, error) {
		return []string{strings.Repeat("a", 64)}, nil
	}}
}

func gatewayRebindCrossStoreAdmissionTestReads(spec appaccess.GatewayRebindSpecV2,
	predecessorSubnet string,
) gatewayRebindSuccessorPreflightReads {
	selected := netip.MustParseAddr(spec.SuccessorProfile.SelectedIPv4)
	return gatewayRebindSuccessorPreflightReads{network: gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) {
			return []hostNetworkCandidate{{InterfaceID: spec.SuccessorProfile.InterfaceID,
				IPv4: selected.String(), Prefix: netip.PrefixFrom(selected, 24).Masked()}}, nil
		},
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) {
			return []netip.Prefix{netip.MustParsePrefix(predecessorSubnet)}, nil
		},
	}, dockerIDs: func(context.Context) ([]string, error) {
		return []string{strings.Repeat("a", 64)}, nil
	}}
}

func gatewayRebindAdmissionFixtureProofReader(t *testing.T, manager *Manager,
	inspectDocker gatewayRebindDockerInspector,
	reads gatewayRebindSuccessorPreflightReads,
) gatewayRebindAdmissionProofReader {
	t.Helper()
	return func(ctx context.Context, target gatewayRebindAdmissionProofTarget,
		selection gatewayCurrentSelection,
	) (gatewayRebindAdmissionPhysicalProof, error) {
		return manager.readGatewayRebindAdmissionPhysicalWithReadsLocked(ctx, target, selection,
			inspectDocker, reads)
	}
}

func gatewayRebindPreparedFixtureProofReader(t *testing.T, manager *Manager,
	snapshot appaccess.GatewayRebindRecoverySnapshot, inspectDocker gatewayRebindDockerInspector,
) gatewayRebindAdmissionProofReader {
	t.Helper()
	if snapshot.Active == nil || snapshot.Active.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		snapshot.Active.Claim.V2 == nil {
		t.Fatal("prepared fixture has no V2 claim")
	}
	return gatewayRebindAdmissionFixtureProofReader(t, manager, inspectDocker,
		gatewayRebindAdmissionTestReads(snapshot.Active.Claim.V2.Spec))
}

func gatewayRebindAdmissionFixtureProof(t *testing.T, manager *Manager,
	snapshot appaccess.GatewayRebindRecoverySnapshot, inspection GatewayRebindProposalInspection,
	inspectDocker gatewayRebindDockerInspector, reads gatewayRebindSuccessorPreflightReads,
) gatewayRebindAdmissionPhysicalProof {
	t.Helper()
	selection, err := manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := gatewayRebindCheckpointForSelection(inspection.ProtectedGeneration,
		inspection.Spec.OperationID, selection)
	if err != nil {
		t.Fatal(err)
	}
	target := gatewayRebindAdmissionTarget(inspection, checkpoint)
	proof, err := gatewayRebindAdmissionFixtureProofReader(t, manager, inspectDocker,
		reads)(context.Background(), target, selection)
	if err != nil || !gatewayRebindAdmissionProofMatchesTarget(context.Background(), proof, target, selection) {
		t.Fatalf("capture fixture admission proof: %v", err)
	}
	return proof
}

func gatewayRebindNativeFixtureInspector(t *testing.T,
	fixture gatewayRebindPredecessorFixture,
) gatewayRebindDockerInspector {
	t.Helper()
	return func(context.Context, routeState, gatewayV2RouteState,
		gatewayMigrationJournal,
	) (gatewayV2DockerObservation, error) {
		// Build a fresh observation each time because the proof reader clears its
		// first sample after comparing the independent second sample.
		return gatewayRebindFixtureDockerObservation(t, fixture), nil
	}
}

func gatewayRebindImmutableFixtureProofReader(proof gatewayRebindAdmissionPhysicalProof) gatewayRebindAdmissionProofReader {
	return func(ctx context.Context, target gatewayRebindAdmissionProofTarget,
		selection gatewayCurrentSelection,
	) (gatewayRebindAdmissionPhysicalProof, error) {
		if ctx == nil || ctx.Err() != nil {
			return gatewayRebindAdmissionPhysicalProof{}, errors.New("fixture admission proof context is unavailable")
		}
		value := proof
		// Store handles are process-local and do not form physical evidence. The
		// production matcher still binds every captured fact to this fresh selection.
		value.Selection = selection
		return value, nil
	}
}

func gatewayRebindStrictNativeFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindCommitInput, gatewayRebindAdmissionProofReader,
) {
	t.Helper()
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	inspection := gatewayRebindAdmissionProcessInspection(t, f)
	completeCrossStoreRepositoryFixture(t, f, inspection.Roster[0])
	input := gatewayRebindAdmissionProcessInput(inspection)
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.CurrentProfile == nil || snapshot.CurrentProfile.ApprovedAt.IsZero() {
		t.Fatalf("read current profile timestamp: snapshot=%#v error=%v", snapshot, err)
	}
	claimTime := snapshot.CurrentProfile.ApprovedAt.Add(time.Minute)
	f.manager.gatewayRebindClock = func() time.Time { return claimTime }
	return f, input, gatewayRebindAdmissionFixtureProofReader(t, f.manager,
		gatewayRebindNativeFixtureInspector(t, f),
		gatewayRebindAdmissionTestReads(inspection.Spec))
}

func gatewayRebindStrictReboundFixture(t *testing.T) (gatewayCurrentStateFixture,
	*gatewayRebindCanonicalEventRepository, gatewayRebindCommitInput,
	gatewayRebindAdmissionProofReader, *gatewayCurrentPhysicalExecutor,
) {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.mu = newContextMutex()
	fixture.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workingIdentity, err := os.Lstat(directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.options.WorkingDirectory = directories.WorkingDirectory
	fixture.manager.workingDirectoryIdentity = workingIdentity
	fixture.manager.options.HostPort = fixture.history.Predecessor.Journal.Source.LocalHostPort
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
	runner := newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State,
		fixture.history.Predecessor.Journal.Source.LocalHostPort)
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, target.State))
	fixture.manager.runner = runner
	base := &gatewayRebindAdmissionRepositoryFake{
		gatewayRebindProposalRepositoryFake: gatewayRebindProposalRepositoryForCurrentFixture(t, fixture)}
	repository := &gatewayRebindCanonicalEventRepository{gatewayRebindAdmissionRepositoryFake: base}
	proposal := GatewayRebindProposalInput{OperationID: uuid.NewString(),
		SuccessorProfileRevisionID:     uuid.NewString(),
		SuccessorProfileRevisionNumber: fixture.baseline.Profile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: appaccess.GatewayProfileSpec{
			SelectedIPv4: fixture.baseline.Profile.SelectedIPv4,
			InterfaceID:  fixture.baseline.Profile.InterfaceID,
			PortStart:    fixture.baseline.Profile.PortStart, PortEnd: fixture.baseline.Profile.PortEnd}}
	inspection, err := fixture.manager.InspectGatewayRebindProposal(context.Background(), repository, proposal)
	if err != nil {
		t.Fatal(err)
	}
	input := gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: uuid.NewString()},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: uuid.NewString()}}
	fixture.manager.gatewayRebindClock = func() time.Time { return time.Unix(4, 0).UTC() }
	reader := gatewayRebindAdmissionFixtureProofReader(t, fixture.manager,
		fixture.manager.inspectGatewayRebindDocker,
		gatewayRebindAdmissionTestReads(inspection.Spec))
	return fixture, repository, input, reader, runner
}

func TestPrepareGatewayRebindStrictLockedRequiresLiveSessionAndProof(t *testing.T) {
	f, input, reader := gatewayRebindStrictNativeFixture(t)
	if _, err := f.manager.prepareGatewayRebindStrictLocked(context.Background(), f.repository,
		input, nil, func(context.Context) error { return nil }); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("nil proof reader error=%v", err)
	}
	if _, err := f.manager.prepareGatewayRebindStrictLocked(context.Background(), f.repository,
		input, reader, nil); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("nil session error=%v", err)
	}

	for _, test := range []struct {
		name   string
		failAt int
		cancel bool
		wantA0 bool
	}{
		{name: "before inspection", failAt: 1},
		{name: "immediately before claim", failAt: 2},
		{name: "after proof before protected write", failAt: 3, wantA0: true},
		{name: "callback cancels before protected write", failAt: 3, cancel: true, wantA0: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, input, reader := gatewayRebindStrictNativeFixture(t)
			beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			session := func(context.Context) error {
				calls++
				if calls != test.failAt {
					return nil
				}
				if test.cancel {
					cancel()
					return nil
				}
				return errors.New("revoked request session")
			}
			_, prepareErr := withCrossStoreFixtureEffectLocks(t, f.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					return f.manager.prepareGatewayRebindStrictLocked(ctx, f.repository, input, reader, session)
				})
			if prepareErr == nil || calls != test.failAt {
				t.Fatalf("strict admission error=%v sessionCalls=%d", prepareErr, calls)
			}
			snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
			afterFiles, filesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil || filesErr != nil || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
				t.Fatalf("session failure changed protected state: sql=%v files=%v", err, filesErr)
			}
			if test.wantA0 != (snapshot.Active != nil && snapshot.Phase == appaccess.GatewayRebindPrepared) {
				t.Fatalf("active=%#v phase=%s wantA0=%t", snapshot.Active, snapshot.Phase, test.wantA0)
			}
		})
	}
}

func TestPrepareGatewayRebindStrictLockedBindsNativeAndReboundProof(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		f, input, reader := gatewayRebindStrictNativeFixture(t)
		attempt, err := withCrossStoreFixtureEffectLocks(t, f.manager,
			func() (gatewayRebindPreparedAttempt, error) {
				return f.manager.prepareGatewayRebindStrictLocked(context.Background(), f.repository,
					input, reader, func(context.Context) error { return nil })
			})
		if err != nil || attempt.Checkpoint.UpgradeState == nil || attempt.Checkpoint.CurrentState != nil ||
			attempt.Progress.Sequence != 1 {
			t.Fatalf("native strict attempt=%#v error=%v", attempt, err)
		}
	})

	t.Run("rebound", func(t *testing.T) {
		fixture, repository, input, reader, runner := gatewayRebindStrictReboundFixture(t)
		attempt, err := withCrossStoreFixtureEffectLocks(t, fixture.manager,
			func() (gatewayRebindPreparedAttempt, error) {
				return fixture.manager.prepareGatewayRebindStrictLocked(context.Background(), repository,
					input, reader, func(context.Context) error { return nil })
			})
		if err != nil || attempt.Checkpoint.CurrentState == nil || attempt.Checkpoint.UpgradeState != nil ||
			attempt.Progress.Sequence != 1 || len(runner.effects) != 0 {
			t.Fatalf("rebound strict attempt=%#v effects=%v error=%v", attempt, runner.effects, err)
		}
	})

	for _, mutation := range []struct {
		name   string
		mutate func(*gatewayRebindAdmissionPhysicalProof)
	}{
		{name: "state", mutate: func(value *gatewayRebindAdmissionPhysicalProof) {
			value.Predecessor.Current.State.Revision++
		}},
		{name: "derived target", mutate: func(value *gatewayRebindAdmissionPhysicalProof) {
			value.Predecessor.Current.Target.State.Revision++
		}},
		{name: "physical", mutate: func(value *gatewayRebindAdmissionPhysicalProof) {
			value.Predecessor.Current.Physical.Digest = strings.Repeat("f", 64)
		}},
	} {
		t.Run("malformed rebound "+mutation.name, func(t *testing.T) {
			fixture, repository, input, actual, runner := gatewayRebindStrictReboundFixture(t)
			reader := func(ctx context.Context, target gatewayRebindAdmissionProofTarget,
				selection gatewayCurrentSelection,
			) (gatewayRebindAdmissionPhysicalProof, error) {
				value, err := actual(ctx, target, selection)
				if err == nil {
					mutation.mutate(&value)
				}
				return value, err
			}
			if _, err := withCrossStoreFixtureEffectLocks(t, fixture.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					return fixture.manager.prepareGatewayRebindStrictLocked(context.Background(), repository,
						input, reader, func(context.Context) error { return nil })
				}); err == nil {
				t.Fatal("stable malformed rebound proof was accepted")
			}
			snapshot, err := repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil || snapshot.Active != nil || len(runner.effects) != 0 {
				t.Fatalf("malformed rebound proof changed state: active=%t effects=%v error=%v",
					snapshot.Active != nil, runner.effects, err)
			}
		})
	}

	for _, mutation := range []struct {
		name   string
		mutate func(*gatewayRebindAdmissionPhysicalProof)
	}{
		{name: "successor profile", mutate: func(value *gatewayRebindAdmissionPhysicalProof) {
			value.Successor.Target.Profile.PortEnd++
		}},
		{name: "native immutable container", mutate: func(value *gatewayRebindAdmissionPhysicalProof) {
			value.Predecessor.Upgrade.FinalContainerID = strings.Repeat("f", 64)
		}},
	} {
		t.Run("malformed "+mutation.name, func(t *testing.T) {
			f, input, actual := gatewayRebindStrictNativeFixture(t)
			reader := func(ctx context.Context, target gatewayRebindAdmissionProofTarget,
				selection gatewayCurrentSelection,
			) (gatewayRebindAdmissionPhysicalProof, error) {
				value, err := actual(ctx, target, selection)
				mutation.mutate(&value)
				return value, err
			}
			if _, err := withCrossStoreFixtureEffectLocks(t, f.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					return f.manager.prepareGatewayRebindStrictLocked(context.Background(), f.repository,
						input, reader, func(context.Context) error { return nil })
				}); err == nil {
				t.Fatal("stable malformed proof was accepted")
			}
			snapshot, snapshotErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
			if snapshotErr != nil || snapshot.Active != nil {
				t.Fatalf("malformed preclaim proof state: active=%t error=%v", snapshot.Active != nil, snapshotErr)
			}
		})
	}

	t.Run("second successor observation drifts", func(t *testing.T) {
		f, input, _ := gatewayRebindStrictNativeFixture(t)
		reads := gatewayRebindAdmissionTestReads(input.Inspection.Spec)
		baseDockerIDs := reads.dockerIDs
		readCalls := 0
		reads.dockerIDs = func(ctx context.Context) ([]string, error) {
			readCalls++
			values, err := baseDockerIDs(ctx)
			if err == nil && readCalls == 2 {
				values = append(values, strings.Repeat("b", 64))
			}
			return values, err
		}
		reader := func(ctx context.Context, target gatewayRebindAdmissionProofTarget,
			selection gatewayCurrentSelection,
		) (gatewayRebindAdmissionPhysicalProof, error) {
			return f.manager.readGatewayRebindAdmissionPhysicalWithReadsLocked(ctx, target, selection,
				gatewayRebindNativeFixtureInspector(t, f), reads)
		}
		if _, err := withCrossStoreFixtureEffectLocks(t, f.manager,
			func() (gatewayRebindPreparedAttempt, error) {
				return f.manager.prepareGatewayRebindStrictLocked(context.Background(), f.repository,
					input, reader, func(context.Context) error { return nil })
			}); err == nil || readCalls != 2 {
			t.Fatalf("successor drift error=%v readCalls=%d", err, readCalls)
		}
		snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil || snapshot.Active != nil {
			t.Fatalf("successor drift created claim: active=%t error=%v", snapshot.Active != nil, err)
		}
	})
}

func TestPrepareGatewayRebindStrictLockedRejectsPostClaimDrift(t *testing.T) {
	for _, test := range []struct {
		name       string
		wrap       func(gatewayRebindAdmissionRepository) gatewayRebindAdmissionRepository
		proofDrift bool
	}{
		{name: "initial event sequence changed", wrap: func(repository gatewayRebindAdmissionRepository) gatewayRebindAdmissionRepository {
			return gatewayRebindAdmissionRepositoryView{gatewayRebindAdmissionRepository: repository,
				mutateSnapshot: func(value *appaccess.GatewayRebindRecoverySnapshot) {
					if value.Active != nil && len(value.Active.Events) == 1 {
						value.Active.Events[0].Sequence++
					}
				}}
		}},
		{name: "initial event missing", wrap: func(repository gatewayRebindAdmissionRepository) gatewayRebindAdmissionRepository {
			return gatewayRebindAdmissionRepositoryView{gatewayRebindAdmissionRepository: repository,
				mutateSnapshot: func(value *appaccess.GatewayRebindRecoverySnapshot) {
					if value.Active != nil {
						value.Active.Events = nil
					}
				}}
		}},
		{name: "initial event duplicated", wrap: func(repository gatewayRebindAdmissionRepository) gatewayRebindAdmissionRepository {
			return gatewayRebindAdmissionRepositoryView{gatewayRebindAdmissionRepository: repository,
				mutateSnapshot: func(value *appaccess.GatewayRebindRecoverySnapshot) {
					if value.Active != nil && len(value.Active.Events) == 1 {
						value.Active.Events = append(value.Active.Events, value.Active.Events[0])
					}
				}}
		}},
		{name: "runtime heads changed", wrap: func(repository gatewayRebindAdmissionRepository) gatewayRebindAdmissionRepository {
			return gatewayRebindAdmissionRepositoryView{gatewayRebindAdmissionRepository: repository,
				mutateHeads: func(value *[]appaccess.GatewayRebindRuntimeHead) {
					if len(*value) != 0 {
						(*value)[0].Generation++
					}
				}}
		}},
		{name: "successor physical inventory changed", wrap: func(repository gatewayRebindAdmissionRepository) gatewayRebindAdmissionRepository {
			return repository
		}, proofDrift: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, input, actual := gatewayRebindStrictNativeFixture(t)
			beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			proofCalls := 0
			reader := func(ctx context.Context, target gatewayRebindAdmissionProofTarget,
				selection gatewayCurrentSelection,
			) (gatewayRebindAdmissionPhysicalProof, error) {
				proofCalls++
				value, err := actual(ctx, target, selection)
				if err == nil && test.proofDrift && proofCalls == 2 {
					value.Successor.Network.HostRoutes = append(value.Successor.Network.HostRoutes,
						"192.168.240.0/24")
				}
				return value, err
			}
			repository := test.wrap(f.repository)
			_, prepareErr := withCrossStoreFixtureEffectLocks(t, f.manager,
				func() (gatewayRebindPreparedAttempt, error) {
					return f.manager.prepareGatewayRebindStrictLocked(context.Background(), repository,
						input, reader, func(context.Context) error { return nil })
				})
			afterFiles, filesErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			snapshot, sqlErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
			if prepareErr == nil || filesErr != nil || sqlErr != nil || snapshot.Active == nil ||
				snapshot.Phase != appaccess.GatewayRebindPrepared ||
				!sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
				t.Fatalf("drift error=%v files=%v sql=%v active=%t", prepareErr, filesErr, sqlErr, snapshot.Active != nil)
			}
		})
	}
}

func TestRecoverGatewayRebindPreparedA0RequiresStablePhysicalProof(t *testing.T) {
	f, input, reader := gatewayRebindStrictNativeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, prepareErr := withCrossStoreFixtureEffectLocks(t, f.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			return f.manager.prepareGatewayRebindStrictLocked(ctx, f.repository, input, reader,
				func(context.Context) error {
					calls++
					if calls == 3 {
						cancel()
					}
					return nil
				})
		})
	if prepareErr == nil {
		t.Fatal("postclaim cancellation did not retain A0")
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil {
		t.Fatalf("A0 snapshot=%#v error=%v", snapshot, err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	bad := func(context.Context, gatewayRebindAdmissionProofTarget, gatewayCurrentSelection) (
		gatewayRebindAdmissionPhysicalProof, error,
	) {
		return gatewayRebindAdmissionPhysicalProof{}, errors.New("physical proof unavailable")
	}
	if _, err := withCrossStoreFixtureEffectLocks(t, f.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			return f.manager.recoverGatewayRebindPreparedAdmissionWithProofLocked(context.Background(),
				f.repository, snapshot, nil)
		}); err == nil {
		t.Fatal("A0 recovery accepted a nil physical proof reader")
	}
	if _, err := withCrossStoreFixtureEffectLocks(t, f.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			return f.manager.recoverGatewayRebindPreparedAdmissionWithProofLocked(context.Background(),
				f.repository, snapshot, bad)
		}); err == nil {
		t.Fatal("A0 recovery accepted a missing physical proof")
	}
	afterFailure, failureReadErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	afterFailureSQL, failureSQLErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if failureReadErr != nil || failureSQLErr != nil || !reflect.DeepEqual(snapshot, afterFailureSQL) ||
		!sameGatewayHistorySnapshot(beforeFiles, afterFailure) {
		t.Fatalf("failed A0 proof changed state: files=%v sql=%v", failureReadErr, failureSQLErr)
	}
	recovered, err := withCrossStoreFixtureEffectLocks(t, f.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			return f.manager.recoverGatewayRebindPreparedAdmissionWithProofLocked(context.Background(),
				f.repository, snapshot, reader)
		})
	if err != nil || recovered.Progress.Sequence != 1 {
		t.Fatalf("stable A0 recovery=%#v error=%v", recovered, err)
	}
	replayProofCalls := 0
	replay, err := withCrossStoreFixtureEffectLocks(t, f.manager,
		func() (gatewayRebindPreparedAttempt, error) {
			return f.manager.recoverGatewayRebindPreparedAdmissionWithProofLocked(context.Background(),
				f.repository, snapshot, func(context.Context, gatewayRebindAdmissionProofTarget,
					gatewayCurrentSelection,
				) (gatewayRebindAdmissionPhysicalProof, error) {
					replayProofCalls++
					return gatewayRebindAdmissionPhysicalProof{}, errors.New("complete replay read physical state")
				})
		})
	if err != nil || replayProofCalls != 0 || !reflect.DeepEqual(recovered, replay) {
		t.Fatalf("complete replay=%#v calls=%d error=%v", replay, replayProofCalls, err)
	}
}
