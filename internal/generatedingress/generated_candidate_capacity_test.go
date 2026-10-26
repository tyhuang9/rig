package generatedingress

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/generatedcapacity"
)

type candidateCapacityTestSource struct {
	mu       sync.Mutex
	snapshot generatedcapacity.Snapshot
	entered  chan struct{}
	unblock  chan struct{}
}

func (s *candidateCapacityTestSource) Snapshot(ctx context.Context) (generatedcapacity.Snapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshot
	entered := s.entered
	unblock := s.unblock
	if entered != nil {
		s.entered = nil
		s.unblock = nil
	}
	s.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-unblock:
		case <-ctx.Done():
			return generatedcapacity.Snapshot{}, ctx.Err()
		}
	}
	return snapshot, nil
}

func (s *candidateCapacityTestSource) blockNextSnapshot() (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entered = make(chan struct{})
	s.unblock = make(chan struct{})
	return s.entered, s.unblock
}

func candidateCapacityTestLedger(
	t *testing.T,
	memory uint64,
	disk uint64,
) (*candidateCapacityLedger, *generatedcapacity.Budget, *candidateCapacityTestSource) {
	t.Helper()
	source := &candidateCapacityTestSource{snapshot: generatedcapacity.Snapshot{
		MemoryAvailableBytes: memory,
		DiskAvailableBytes:   disk,
	}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newCandidateCapacityLedger(budget)
	if err != nil {
		t.Fatal(err)
	}
	return ledger, budget, source
}

func TestCandidateCapacityRestoredDebtRequiresExactPublishedTokenGraph(t *testing.T) {
	newLedger := func(t *testing.T, components ...string) (*candidateCapacityLedger,
		*generatedDomainCapacityCoordinator, []*generatedDomainCapacityDebtToken, candidateGroupSpec,
	) {
		t.Helper()
		manager, _ := newManagerFixture(t, false)
		coordinator, err := newGeneratedDomainCapacityCoordinator(manager)
		if err != nil {
			t.Fatal(err)
		}
		limits := make([]generatedcapacity.Limits, len(components))
		for index := range limits {
			limits[index] = generatedcapacity.Limits{MemoryBytes: 10, DiskBytes: 10}
		}
		tokens, err := coordinator.installBootstrapBatch(limits)
		if err != nil {
			t.Fatal(err)
		}
		source := &candidateCapacityTestSource{snapshot: generatedcapacity.Snapshot{
			MemoryAvailableBytes: 100, DiskAvailableBytes: 100,
		}}
		budget, err := generatedcapacity.New(source)
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := newCandidateCapacityLedger(budget, coordinator)
		if err != nil {
			t.Fatal(err)
		}
		return ledger, coordinator, tokens, candidateCapacityTestSpec("app-restored", "deployment-restored", components...)
	}
	receipts := func(components ...string) map[string]candidateColdReceiptSeed {
		result := make(map[string]candidateColdReceiptSeed, len(components))
		for _, component := range components {
			result[component] = candidateColdReceiptSeed{id: "receipt-" + component,
				physical: candidatePhysicalHandedToRoute}
		}
		return result
	}

	t.Run("pending graph refuses consumers until exact publication", func(t *testing.T) {
		ledger, coordinator, tokens, spec := newLedger(t, "api", "web")
		if err := ledger.seedColdOwnershipWithDebt(spec, receipts("api", "web"), tokens); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.enterRetirementRoster(spec.appID, spec.deploymentID,
			map[string]string{"api": "receipt-api", "web": "receipt-web"}); !errors.Is(err, errCandidateReceiptMismatch) {
			t.Fatalf("pending retirement error=%v", err)
		}
		if reservation, err := ledger.reserve(context.Background(), spec); reservation != nil ||
			!errors.Is(err, errCandidateCapacityStale) {
			t.Fatalf("pending ordinary reserve=%#v error=%v", reservation, err)
		}
		if err := coordinator.sealBootstrap(); err != nil {
			t.Fatal(err)
		}
		if err := ledger.publishRestoredOwnership(); err != nil {
			t.Fatal(err)
		}
		operation, err := ledger.enterRetirementRoster(spec.appID, spec.deploymentID,
			map[string]string{"api": "receipt-api", "web": "receipt-web"})
		if err != nil || operation == nil {
			t.Fatalf("published retirement operation=%#v error=%v", operation, err)
		}
		operation.leave()
		for _, token := range tokens {
			if !token.published() {
				t.Fatal("published graph contains an unpublished token")
			}
		}
	})

	t.Run("partial crossed duplicate and alias inputs refuse atomically", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func([]*generatedDomainCapacityDebtToken) []*generatedDomainCapacityDebtToken
			rows   map[string]candidateColdReceiptSeed
		}{
			{name: "partial roster", rows: receipts("api")},
			{name: "duplicate token alias", rows: receipts("api", "web"),
				mutate: func(tokens []*generatedDomainCapacityDebtToken) []*generatedDomainCapacityDebtToken {
					return []*generatedDomainCapacityDebtToken{tokens[0], tokens[0]}
				}},
			{name: "crossed coordinator", rows: receipts("api", "web"),
				mutate: func(tokens []*generatedDomainCapacityDebtToken) []*generatedDomainCapacityDebtToken {
					manager, _ := newManagerFixture(t, false)
					foreign, err := newGeneratedDomainCapacityCoordinator(manager)
					if err != nil {
						t.Fatal(err)
					}
					other, err := foreign.installBootstrapBatch([]generatedcapacity.Limits{{MemoryBytes: 10, DiskBytes: 10}})
					if err != nil {
						t.Fatal(err)
					}
					return []*generatedDomainCapacityDebtToken{tokens[0], other[0]}
				}},
		} {
			t.Run(test.name, func(t *testing.T) {
				ledger, coordinator, tokens, spec := newLedger(t, "api", "web")
				if test.mutate != nil {
					tokens = test.mutate(tokens)
				}
				if err := ledger.seedColdOwnershipWithDebt(spec, test.rows, tokens); !errors.Is(err, errCandidateCapacityInvalid) {
					t.Fatalf("invalid restored graph error=%v", err)
				}
				ledger.mu.Lock()
				groups := len(ledger.groups)
				ledger.mu.Unlock()
				if groups != 0 || coordinator.memory != 20 || coordinator.disk != 20 || len(coordinator.tokens) != 2 {
					t.Fatalf("refusal mutated graph: groups=%d memory=%d disk=%d tokens=%d",
						groups, coordinator.memory, coordinator.disk, len(coordinator.tokens))
				}
			})
		}
	})
}

func TestCandidateCapacityRestoredPredecessorReleasesExactDebtAndKeepsSuccessorCharge(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.capacityOutput = []byte("30 30\n")
	coordinator, err := newGeneratedDomainCapacityCoordinator(manager)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := coordinator.installBootstrapBatch([]generatedcapacity.Limits{{MemoryBytes: 10, DiskBytes: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.sealBootstrap(); err != nil {
		t.Fatal(err)
	}
	budget, err := generatedcapacity.New(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newCandidateCapacityLedger(budget, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "web")
	predecessorReceipts := map[string]candidateColdReceiptSeed{"web": {
		id: "receipt-old", physical: candidatePhysicalHandedToRoute,
	}}
	if err := ledger.seedColdOwnershipWithDebt(predecessorSpec, predecessorReceipts, tokens); err != nil {
		t.Fatal(err)
	}
	if err := ledger.publishRestoredOwnership(); err != nil {
		t.Fatal(err)
	}
	predecessor, err := ledger.enterRetirementRoster(predecessorSpec.appID, predecessorSpec.deploymentID,
		map[string]string{"web": "receipt-old"})
	if err != nil {
		t.Fatal(err)
	}
	defer predecessor.leave()
	if err := predecessor.markConfirmedAbsentRetained("web"); err != nil {
		t.Fatal(err)
	}

	successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
	successorReservation, successor, _ := candidateCapacityTestHandedRoster(t, ledger, successorSpec, "successor")
	defer successor.leave()
	successorCharge := successor.group.byName["web"].charge
	successorLease := successorCharge.lease
	if successorLease == nil {
		t.Fatal("successor has no raw capacity charge")
	}
	handoff, err := ledger.beginAccountingHandoff(successor, predecessor)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.markGatewayUnlocked(); err != nil {
		t.Fatal(err)
	}
	successor.leave()
	if err := successorReservation.release(); err != nil {
		t.Fatal(err)
	}
	if err := handoff.releasePredecessorOnce(); err != nil {
		t.Fatal(err)
	}
	if err := handoff.releasePredecessorOnce(); err != nil {
		t.Fatalf("predecessor release replay: %v", err)
	}
	if coordinator.memory != 0 || coordinator.disk != 0 || len(coordinator.tokens) != 0 || tokens[0].configured() {
		t.Fatalf("restored predecessor debt remains: memory=%d disk=%d tokens=%d configured=%t",
			coordinator.memory, coordinator.disk, len(coordinator.tokens), tokens[0].configured())
	}
	if successorCharge.token == nil || successorCharge.lease != successorLease ||
		successorCharge.owner != candidateAccountingRouteActive || successorCharge.state != candidateChargeActive {
		t.Fatalf("restored predecessor release changed successor charge=%+v", successorCharge)
	}
}

func candidateCapacityTestSpec(
	appID string,
	deploymentID string,
	components ...string,
) candidateGroupSpec {
	return candidateGroupSpec{
		appID:        appID,
		deploymentID: deploymentID,
		pins: candidateImmutablePins{
			jobID:                        "job-1",
			releaseID:                    "release-1",
			configurationRevisionID:      "config-1",
			configurationRevisionNumber:  3,
			deploymentPlanRevisionID:     "plan-1",
			deploymentPlanRevisionNumber: 4,
			candidateSlot:                "green",
			previousDeploymentID:         "deployment-previous",
			previousSlot:                 "blue",
		},
		components: components,
		limits: generatedcapacity.Limits{
			MemoryBytes: 10,
			DiskBytes:   10,
		},
	}
}

func candidateCapacityTestReserve(
	t *testing.T,
	ledger *candidateCapacityLedger,
	spec candidateGroupSpec,
) *candidateCapacityReservation {
	t.Helper()
	reservation, err := ledger.reserve(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return reservation
}

func candidateCapacityTestRecordReceipt(
	t *testing.T,
	reservation *candidateCapacityReservation,
	component string,
	receiptID string,
	commit candidateReceiptCommit,
) {
	t.Helper()
	operation, err := reservation.enterReceiptAttachment(component)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.leave()
	if err := operation.recordNewReceipt(receiptID, commit); err != nil {
		t.Fatal(err)
	}
}

func candidateCapacityTestMarkPresent(
	t *testing.T,
	reservation *candidateCapacityReservation,
	component string,
	receiptID string,
) {
	t.Helper()
	operation, err := reservation.enterComponent(component, receiptID, candidateOperationMutate)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.leave()
	if err := operation.enterCommand(candidateCommandCreate); err != nil {
		t.Fatal(err)
	}
	if err := operation.markPresent(); err != nil {
		t.Fatal(err)
	}
}

func candidateCapacityTestHandedRoster(t *testing.T, ledger *candidateCapacityLedger,
	spec candidateGroupSpec, receiptPrefix string,
) (*candidateCapacityReservation, *candidateRosterOperation, map[string]string) {
	t.Helper()
	reservation := candidateCapacityTestReserve(t, ledger, spec)
	receipts := make(map[string]string, len(spec.components))
	for _, component := range spec.components {
		receipt := receiptPrefix + "-" + component
		receipts[component] = receipt
		candidateCapacityTestRecordReceipt(t, reservation, component, receipt, candidateReceiptCommitAcknowledged)
		candidateCapacityTestMarkPresent(t, reservation, component, receipt)
	}
	roster, err := reservation.closeCleanupAndEnterRoster(receipts)
	if err != nil {
		t.Fatal(err)
	}
	if err := roster.markHandedToRoute(); err != nil {
		t.Fatal(err)
	}
	return reservation, roster, receipts
}

func candidateCapacityTestAbsentPredecessor(t *testing.T, ledger *candidateCapacityLedger,
	spec candidateGroupSpec, receiptPrefix string,
) (*candidateCapacityReservation, *candidateRetirementRosterOperation, map[string]string) {
	t.Helper()
	reservation, handed, receipts := candidateCapacityTestHandedRoster(t, ledger, spec, receiptPrefix)
	handed.leave()
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
	retirement, err := ledger.enterRetirementRoster(spec.appID, spec.deploymentID, receipts)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range spec.components {
		if err := retirement.markConfirmedAbsentRetained(component); err != nil {
			t.Fatal(err)
		}
	}
	return reservation, retirement, receipts
}

func candidateCapacityTestRequireCompetitorRefused(t *testing.T, ledger *candidateCapacityLedger,
	appID, deploymentID string,
) {
	t.Helper()
	competitor := candidateCapacityTestSpec(appID, deploymentID, "competitor")
	competitor.limits = generatedcapacity.Limits{MemoryBytes: 1, DiskBytes: 1}
	if _, err := ledger.reserve(context.Background(), competitor); !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("competing ledger reservation error = %v", err)
	}
}

func candidateCapacityTestWait(t *testing.T, condition func() bool, message string) {
	t.Helper()
	for range 100000 {
		if condition() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal(message)
}

func TestCandidateCapacityAtomicRosterAdmissionUsesExactBudget(t *testing.T) {
	ledger, budget, _ := candidateCapacityTestLedger(t, 20, 20)
	reservation := candidateCapacityTestReserve(
		t,
		ledger,
		candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker"),
	)

	competitor := candidateCapacityTestSpec("app-2", "deployment-2", "web")
	competitor.limits = generatedcapacity.Limits{MemoryBytes: 1, DiskBytes: 1}
	if _, err := ledger.reserve(context.Background(), competitor); !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("competing ledger reservation error = %v", err)
	}
	if _, err := budget.Acquire(
		context.Background(),
		generatedcapacity.Limits{MemoryBytes: 1, DiskBytes: 1},
		1,
	); !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("direct consumer did not contend with ledger: %v", err)
	}

	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
	admitted := candidateCapacityTestReserve(t, ledger, competitor)
	if err := admitted.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCapacityCopiesRosterAndRejectsPinOrRosterDrift(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 100, 100)
	components := []string{"worker", "web"}
	spec := candidateCapacityTestSpec("app-1", "deployment-1", components...)
	first := candidateCapacityTestReserve(t, ledger, spec)
	components[0] = "mutated-by-caller"
	spec.components[0] = "also-mutated"

	exact := candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker")
	second := candidateCapacityTestReserve(t, ledger, exact)

	driftedPins := exact
	driftedPins.pins.releaseID = "release-2"
	if _, err := ledger.reserve(context.Background(), driftedPins); !errors.Is(err, errCandidateCapacityPinsChanged) {
		t.Fatalf("pin drift error = %v", err)
	}
	driftedRoster := candidateCapacityTestSpec("app-1", "deployment-1", "web")
	if _, err := ledger.reserve(context.Background(), driftedRoster); !errors.Is(err, errCandidateCapacityPinsChanged) {
		t.Fatalf("roster drift error = %v", err)
	}

	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCapacitySerializesFreshGroupAdmissionWithoutBorrowingCallerRoster(t *testing.T) {
	ledger, _, source := candidateCapacityTestLedger(t, 40, 40)
	callerRoster := []string{"worker", "web"}
	spec := candidateCapacityTestSpec("app-1", "deployment-1", callerRoster...)
	entered, unblock := source.blockNextSnapshot()
	type reserveResult struct {
		reservation *candidateCapacityReservation
		err         error
	}
	result := make(chan reserveResult, 1)
	go func() {
		reservation, err := ledger.reserve(context.Background(), spec)
		result <- reserveResult{reservation: reservation, err: err}
	}()
	<-entered
	callerRoster[0] = "caller-mutation"
	spec.components[1] = "second-caller-mutation"

	exact := candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker")
	if _, err := ledger.reserve(context.Background(), exact); !errors.Is(err, errCandidateCapacityBusy) {
		t.Fatalf("competing fresh admission error = %v", err)
	}
	close(unblock)
	first := <-result
	if first.err != nil {
		t.Fatal(first.err)
	}
	second := candidateCapacityTestReserve(t, ledger, exact)
	if err := first.reservation.release(); err != nil {
		t.Fatal(err)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateReceiptCommitFactsDoNotMarkPhysicalAttempt(t *testing.T) {
	for _, test := range []struct {
		name   string
		commit candidateReceiptCommit
	}{
		{name: "acknowledged", commit: candidateReceiptCommitAcknowledged},
		{name: "unknown", commit: candidateReceiptCommitUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
			spec := candidateCapacityTestSpec("app-1", "deployment-1", "web")
			original := candidateCapacityTestReserve(t, ledger, spec)
			candidateCapacityTestRecordReceipt(t, original, "web", "receipt-1", test.commit)

			observe, err := original.enterComponent("web", "receipt-1", candidateOperationObserve)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := observe.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			observe.leave()
			if snapshot.physical != candidatePhysicalUnused || snapshot.createEntered {
				t.Fatalf("receipt commit changed physical facts: %+v", snapshot)
			}
			if snapshot.commitAcknowledged != (test.commit == candidateReceiptCommitAcknowledged) ||
				snapshot.commitUnknown != (test.commit == candidateReceiptCommitUnknown) {
				t.Fatalf("receipt commit facts = %+v", snapshot)
			}

			oldEpoch := snapshot.chargeEpoch
			if err := original.release(); err != nil {
				t.Fatal(err)
			}
			component := original.group.byName["web"]
			if component.charge.state != candidateChargeReleased {
				t.Fatalf("original exact never-entered charge state = %v", component.charge.state)
			}

			reattached := candidateCapacityTestReserve(t, ledger, spec)
			if component.charge.epoch <= oldEpoch || !component.charge.receiptRetained {
				t.Fatalf("fresh receipt epoch = %d retained = %v", component.charge.epoch, component.charge.receiptRetained)
			}
			attachment, err := reattached.enterReceiptAttachment("web")
			if err != nil {
				t.Fatal(err)
			}
			if err := attachment.attachExistingReceipt("receipt-1"); err != nil {
				t.Fatal(err)
			}
			snapshot, err = attachment.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			attachment.leave()
			if snapshot.physical != candidatePhysicalUnused || !snapshot.observed || !snapshot.accountingRetained {
				t.Fatalf("reattached receipt facts = %+v", snapshot)
			}
			if err := reattached.release(); err != nil {
				t.Fatal(err)
			}
			if component.charge.state != candidateChargeActive || component.charge.refs != 0 {
				t.Fatalf("retained charge state=%v refs=%d", component.charge.state, component.charge.refs)
			}
		})
	}
}

func TestCandidateSameEpochAliasRetainsNewReceipt(t *testing.T) {
	t.Run("outer release remains conservative", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		spec := candidateCapacityTestSpec("app-1", "deployment-1", "web")
		creator := candidateCapacityTestReserve(t, ledger, spec)
		alias := candidateCapacityTestReserve(t, ledger, spec)
		candidateCapacityTestRecordReceipt(
			t,
			creator,
			"web",
			"receipt-1",
			candidateReceiptCommitAcknowledged,
		)
		charge := creator.bindings["web"].charge
		if !charge.receiptRetained {
			t.Fatal("same-epoch alias did not retain the new receipt charge")
		}
		if err := creator.release(); err != nil {
			t.Fatal(err)
		}
		if err := alias.release(); err != nil {
			t.Fatal(err)
		}
		if charge.state != candidateChargeActive || charge.refs != 0 {
			t.Fatalf("aliased receipt charge state=%v refs=%d", charge.state, charge.refs)
		}
	})

	t.Run("authorized cleanup retires exact charge and stale alias releases siblings", func(t *testing.T) {
		ledger, budget, _ := candidateCapacityTestLedger(t, 20, 20)
		spec := candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker")
		creator := candidateCapacityTestReserve(t, ledger, spec)
		alias := candidateCapacityTestReserve(t, ledger, spec)
		candidateCapacityTestRecordReceipt(
			t,
			creator,
			"web",
			"receipt-web",
			candidateReceiptCommitAcknowledged,
		)
		candidateCapacityTestMarkPresent(t, creator, "web", "receipt-web")

		webCharge := creator.bindings["web"].charge
		workerCharge := creator.bindings["worker"].charge
		cleanup, err := creator.enterCleanup("web", "receipt-web")
		if err != nil {
			t.Fatal(err)
		}
		if err := cleanup.enterCommand(candidateCommandRemove); err != nil {
			t.Fatal(err)
		}
		if err := cleanup.retireConfirmedAbsent(); err != nil {
			t.Fatal(err)
		}
		cleanup.leave()

		if webCharge.state != candidateChargeReleased || !webCharge.logicallyRetired ||
			webCharge.refs != 0 {
			t.Fatalf(
				"cleaned alias charge state=%v retired=%v refs=%d",
				webCharge.state,
				webCharge.logicallyRetired,
				webCharge.refs,
			)
		}
		if _, err := alias.enterComponent("web", "receipt-web", candidateOperationMutate); !errors.Is(err, errCandidateCapacityStale) {
			t.Fatalf("retired alias command entry error = %v", err)
		}
		word := creator.group.byName["web"].receipt.state.Load()
		if candidatePhysicalFromWord(word) != candidatePhysicalConfirmedAbsent ||
			word&candidateReceiptRemove == 0 {
			t.Fatalf("cleanup tombstone word = %#x", word)
		}

		if err := creator.release(); err != nil {
			t.Fatal(err)
		}
		if workerCharge.state != candidateChargeActive || workerCharge.refs != 1 {
			t.Fatalf("creator sibling release state=%v refs=%d", workerCharge.state, workerCharge.refs)
		}
		if err := alias.release(); err != nil {
			t.Fatal(err)
		}
		if workerCharge.state != candidateChargeReleased || workerCharge.refs != 0 {
			t.Fatalf("alias sibling release state=%v refs=%d", workerCharge.state, workerCharge.refs)
		}

		leases, err := budget.Acquire(context.Background(), spec.limits, len(spec.components))
		if err != nil {
			t.Fatalf("released exact roster capacity was unavailable: %v", err)
		}
		for _, lease := range leases {
			lease.Release()
		}
	})
}

func TestCandidateSharedTokenAndEntryMarkersPreventDuplicateCommands(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 30, 30)
	spec := candidateCapacityTestSpec("app-1", "deployment-1", "web")
	first := candidateCapacityTestReserve(t, ledger, spec)
	attachment, err := first.enterReceiptAttachment("web")
	if err != nil {
		t.Fatal(err)
	}
	if err := attachment.recordNewReceipt("receipt-1", candidateReceiptCommitAcknowledged); err != nil {
		t.Fatal(err)
	}
	if err := attachment.enterCommand(candidateCommandCreate); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("attachment create error = %v", err)
	}
	if err := attachment.markPresent(); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("attachment physical mutation error = %v", err)
	}
	if err := attachment.retireConfirmedAbsent(); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("attachment retirement error = %v", err)
	}
	attachment.latchMutationUnquiesced()
	attachmentSnapshot, err := attachment.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if attachmentSnapshot.physical != candidatePhysicalUnused || attachmentSnapshot.mutationUnquiesced {
		t.Fatalf("attachment mutated receipt facts: %+v", attachmentSnapshot)
	}
	attachment.leave()

	observation, err := first.enterComponent("web", "receipt-1", candidateOperationObserve)
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.enterCommand(candidateCommandCreate); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("observation create error = %v", err)
	}
	if err := observation.markPresent(); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("observation physical mutation error = %v", err)
	}
	if err := observation.retireConfirmedAbsent(); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("observation retirement error = %v", err)
	}
	observation.latchMutationUnquiesced()
	observation.leave()

	second := candidateCapacityTestReserve(t, ledger, spec)

	create, err := first.enterComponent("web", "receipt-1", candidateOperationMutate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.enterComponent("web", "receipt-1", candidateOperationMutate); !errors.Is(err, errCandidateCapacityBusy) {
		t.Fatalf("competing token error = %v", err)
	}
	for _, command := range []candidateCommand{candidateCommandStop, candidateCommandRemove} {
		if err := create.enterCommand(command); !errors.Is(err, errCandidateOperationKind) {
			t.Fatalf("ordinary token command %v error = %v", command, err)
		}
	}
	if err := create.retireConfirmedAbsent(); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("ordinary token retirement error = %v", err)
	}
	if err := create.enterCommand(candidateCommandCreate); err != nil {
		t.Fatal(err)
	}
	if err := create.markPresent(); err != nil {
		t.Fatal(err)
	}
	create.leave()

	start, err := second.enterComponent("web", "receipt-1", candidateOperationMutate)
	if err != nil {
		t.Fatal(err)
	}
	if err := start.enterCommand(candidateCommandStart); err != nil {
		t.Fatal(err)
	}
	if err := start.enterCommand(candidateCommandStart); !errors.Is(err, errCandidateCommandEntered) {
		t.Fatalf("duplicate start error = %v", err)
	}
	snapshot, err := start.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	start.leave()
	if snapshot.physical != candidatePhysicalPresent || !snapshot.createEntered || !snapshot.startEntered {
		t.Fatalf("shared marker snapshot = %+v", snapshot)
	}

	replayed, err := first.enterComponent("web", "receipt-1", candidateOperationMutate)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayed.enterCommand(candidateCommandStart); !errors.Is(err, errCandidateCommandEntered) {
		t.Fatalf("replayed start error = %v", err)
	}
	replayed.leave()

	cleanup, err := first.enterCleanup("web", "receipt-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []candidateCommand{candidateCommandCreate, candidateCommandStart} {
		if err := cleanup.enterCommand(command); !errors.Is(err, errCandidateOperationKind) {
			t.Fatalf("cleanup token command %v error = %v", command, err)
		}
	}
	if err := cleanup.enterCommand(candidateCommandStop); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.enterCommand(candidateCommandRemove); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.retireConfirmedAbsent(); err != nil {
		t.Fatal(err)
	}
	cleanup.leave()
	if _, err := second.enterComponent("web", "receipt-1", candidateOperationMutate); !errors.Is(err, errCandidateCapacityStale) {
		t.Fatalf("retired alias operation error = %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	if err := second.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCleanupClosureJoinsBeforeWholeRosterTokens(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 40, 40)
	spec := candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker")
	reservation := candidateCapacityTestReserve(t, ledger, spec)
	candidateCapacityTestRecordReceipt(t, reservation, "web", "receipt-web", candidateReceiptCommitAcknowledged)
	candidateCapacityTestRecordReceipt(t, reservation, "worker", "receipt-worker", candidateReceiptCommitAcknowledged)
	candidateCapacityTestMarkPresent(t, reservation, "web", "receipt-web")
	candidateCapacityTestMarkPresent(t, reservation, "worker", "receipt-worker")

	cleanup, err := reservation.enterCleanup("web", "receipt-web")
	if err != nil {
		t.Fatal(err)
	}
	type rosterResult struct {
		operation *candidateRosterOperation
		err       error
	}
	result := make(chan rosterResult, 1)
	go func() {
		operation, enterErr := reservation.closeCleanupAndEnterRoster(map[string]string{
			"web":    "receipt-web",
			"worker": "receipt-worker",
		})
		result <- rosterResult{operation: operation, err: enterErr}
	}()
	candidateCapacityTestWait(t, func() bool {
		reservation.group.mu.Lock()
		defer reservation.group.mu.Unlock()
		return reservation.group.cleanupClosed
	}, "cleanup closure was not published")
	select {
	case early := <-result:
		t.Fatalf("whole-roster entry did not join cleanup: %+v", early)
	default:
	}
	if _, err := reservation.enterCleanup("worker", "receipt-worker"); !errors.Is(err, errCandidateCleanupClosed) {
		t.Fatalf("cleanup reopened after closure: %v", err)
	}

	cleanup.leave()
	joined := <-result
	if joined.err != nil {
		t.Fatal(joined.err)
	}
	if err := joined.operation.markHandedToRoute(); err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.enterComponent("worker", "receipt-worker", candidateOperationObserve); !errors.Is(err, errCandidateCapacityBusy) {
		t.Fatalf("whole-roster tokens were not exclusive: %v", err)
	}
	joined.operation.leave()

	if _, err := reservation.enterCleanup("web", "receipt-web"); !errors.Is(err, errCandidateCleanupClosed) {
		t.Fatalf("cleanup closure did not survive roster leave: %v", err)
	}
	for _, component := range spec.components {
		word := reservation.group.byName[component].receipt.state.Load()
		if candidatePhysicalFromWord(word) != candidatePhysicalHandedToRoute {
			t.Fatalf("%s physical state = %v", component, candidatePhysicalFromWord(word))
		}
	}
	if err := reservation.retainAll(); err != nil {
		t.Fatal(err)
	}
	for _, component := range spec.components {
		word := reservation.group.byName[component].receipt.state.Load()
		if candidatePhysicalFromWord(word) != candidatePhysicalHandedToRoute {
			t.Fatalf("retain downgraded %s physical state", component)
		}
	}
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateReleaseAndRetainSerializeAtReservationActivity(t *testing.T) {
	t.Run("release wins", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		reservation := candidateCapacityTestReserve(
			t,
			ledger,
			candidateCapacityTestSpec("app-1", "deployment-1", "web"),
		)
		operation, err := reservation.enterReceiptAttachment("web")
		if err != nil {
			t.Fatal(err)
		}
		released := make(chan error, 1)
		go func() { released <- reservation.release() }()
		candidateCapacityTestWait(t, func() bool {
			reservation.mu.Lock()
			defer reservation.mu.Unlock()
			return reservation.closing
		}, "release did not close reservation entry")
		if err := reservation.retainAll(); !errors.Is(err, errCandidateCapacityClosing) {
			t.Fatalf("retain after close error = %v", err)
		}
		select {
		case err := <-released:
			t.Fatalf("release did not join active operation: %v", err)
		default:
		}
		operation.leave()
		if err := <-released; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("retain wins", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		reservation := candidateCapacityTestReserve(
			t,
			ledger,
			candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker"),
		)
		if err := reservation.retainAll(); err != nil {
			t.Fatal(err)
		}
		if err := reservation.release(); err != nil {
			t.Fatal(err)
		}
		for _, binding := range reservation.bindings {
			if !binding.charge.explicitRetained || binding.charge.state != candidateChargeActive {
				t.Fatalf("retained charge state=%v retained=%v", binding.charge.state, binding.charge.explicitRetained)
			}
		}
	})
}

func TestCandidateRetainAllRejectsPartialCleanupRetirement(t *testing.T) {
	ledger, budget, _ := candidateCapacityTestLedger(t, 30, 30)
	spec := candidateCapacityTestSpec("app-1", "deployment-1", "web", "worker")
	reservation := candidateCapacityTestReserve(
		t,
		ledger,
		spec,
	)
	candidateCapacityTestRecordReceipt(t, reservation, "web", "receipt-web", candidateReceiptCommitAcknowledged)
	candidateCapacityTestRecordReceipt(t, reservation, "worker", "receipt-worker", candidateReceiptCommitAcknowledged)

	cleanup, err := reservation.enterCleanup("web", "receipt-web")
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.retireConfirmedAbsent(); err != nil {
		t.Fatal(err)
	}
	cleanup.leave()
	if err := reservation.retainAll(); !errors.Is(err, errCandidateCapacityStale) {
		t.Fatalf("partial retain error = %v", err)
	}
	if reservation.bindings["worker"].charge.receiptRetained ||
		reservation.bindings["worker"].charge.explicitRetained {
		t.Fatal("retainAll partially retained the surviving sibling")
	}
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.reserve(context.Background(), spec); !errors.Is(err, errCandidateConfirmedAbsent) {
		t.Fatalf("confirmed-absent tombstone reserve error = %v", err)
	}
	leases, err := budget.Acquire(context.Background(), spec.limits, len(spec.components))
	if err != nil {
		t.Fatalf("confirmed-absent reserve reacquired or leaked capacity: %v", err)
	}
	for _, lease := range leases {
		lease.Release()
	}
}

func TestCandidateRetainAllWinningBeforeCleanupKeepsTheExactCharge(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
	reservation := candidateCapacityTestReserve(
		t,
		ledger,
		candidateCapacityTestSpec("app-1", "deployment-1", "web"),
	)
	candidateCapacityTestRecordReceipt(
		t,
		reservation,
		"web",
		"receipt-1",
		candidateReceiptCommitAcknowledged,
	)
	if err := reservation.retainAll(); err != nil {
		t.Fatal(err)
	}
	cleanup, err := reservation.enterCleanup("web", "receipt-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.retireConfirmedAbsent(); err != nil {
		t.Fatal(err)
	}
	cleanup.leave()
	charge := reservation.bindings["web"].charge
	if charge.state != candidateChargeActive || charge.refs != 0 || !charge.explicitRetained ||
		!charge.logicallyRetired {
		t.Fatalf(
			"retained cleanup charge state=%v refs=%d retained=%v retired=%v",
			charge.state,
			charge.refs,
			charge.explicitRetained,
			charge.logicallyRetired,
		)
	}
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCleanupClosureSurvivesRosterEntryFailure(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
	reservation := candidateCapacityTestReserve(
		t,
		ledger,
		candidateCapacityTestSpec("app-1", "deployment-1", "web"),
	)
	candidateCapacityTestRecordReceipt(
		t,
		reservation,
		"web",
		"receipt-1",
		candidateReceiptCommitAcknowledged,
	)
	if _, err := reservation.closeCleanupAndEnterRoster(map[string]string{
		"web": "wrong-receipt",
	}); !errors.Is(err, errCandidateReceiptMismatch) {
		t.Fatalf("roster entry error = %v", err)
	}
	if _, err := reservation.enterCleanup("web", "receipt-1"); !errors.Is(err, errCandidateCleanupClosed) {
		t.Fatalf("cleanup closure reopened after roster error: %v", err)
	}
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateRetiringIsVisibleBeforeRawReleaseAndFreshEpochIsIsolated(t *testing.T) {
	ledger, budget, source := candidateCapacityTestLedger(t, 100, 100)
	spec := candidateCapacityTestSpec("app-1", "deployment-1", "web")
	old := candidateCapacityTestReserve(t, ledger, spec)
	oldEpoch := old.bindings["web"].epoch

	entered, unblock := source.blockNextSnapshot()
	competitor := make(chan error, 1)
	go func() {
		_, err := budget.Acquire(
			context.Background(),
			generatedcapacity.Limits{MemoryBytes: 100, DiskBytes: 100},
			1,
		)
		competitor <- err
	}()
	<-entered

	released := make(chan error, 1)
	go func() { released <- old.release() }()
	candidateCapacityTestWait(t, func() bool {
		old.group.mu.Lock()
		defer old.group.mu.Unlock()
		return old.bindings["web"].charge.state == candidateChargeRetiring
	}, "retiring was not published before raw release")
	if _, err := ledger.reserve(context.Background(), spec); !errors.Is(err, errCandidateCapacityBusy) {
		t.Fatalf("attachment to retiring epoch error = %v", err)
	}
	close(unblock)
	if err := <-competitor; !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("blocked competitor error = %v", err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}

	fresh := candidateCapacityTestReserve(t, ledger, spec)
	if fresh.bindings["web"].epoch <= oldEpoch {
		t.Fatalf("fresh epoch = %d, old = %d", fresh.bindings["web"].epoch, oldEpoch)
	}
	if err := old.release(); err != nil {
		t.Fatal(err)
	}
	if fresh.bindings["web"].charge.refs != 1 || fresh.bindings["web"].charge.state != candidateChargeActive {
		t.Fatalf(
			"old release affected fresh epoch: refs=%d state=%v",
			fresh.bindings["web"].charge.refs,
			fresh.bindings["web"].charge.state,
		)
	}
	if err := fresh.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateMutationUnquiescedRetainsChargeAndRefusesLaterMutations(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
	reservation := candidateCapacityTestReserve(
		t,
		ledger,
		candidateCapacityTestSpec("app-1", "deployment-1", "web"),
	)
	candidateCapacityTestRecordReceipt(
		t,
		reservation,
		"web",
		"receipt-1",
		candidateReceiptCommitAcknowledged,
	)
	mutation, err := reservation.enterComponent("web", "receipt-1", candidateOperationMutate)
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.enterCommand(candidateCommandCreate); err != nil {
		t.Fatal(err)
	}
	mutation.latchMutationUnquiesced()
	if err := mutation.markPresent(); !errors.Is(err, errCandidateMutationUnquiesced) {
		t.Fatalf("unquiesced present transition error = %v", err)
	}
	if err := mutation.retireConfirmedAbsent(); !errors.Is(err, errCandidateOperationKind) {
		t.Fatalf("non-cleanup absence retirement error = %v", err)
	}
	mutation.leave()

	if _, err := reservation.enterComponent("web", "receipt-1", candidateOperationMutate); !errors.Is(err, errCandidateMutationUnquiesced) {
		t.Fatalf("later mutation error = %v", err)
	}
	if _, err := reservation.enterCleanup("web", "receipt-1"); !errors.Is(err, errCandidateMutationUnquiesced) {
		t.Fatalf("later cleanup error = %v", err)
	}
	observation, err := reservation.enterComponent("web", "receipt-1", candidateOperationObserve)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := observation.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	observation.leave()
	if !snapshot.mutationUnquiesced || snapshot.physical != candidatePhysicalAttemptedUnknown ||
		!snapshot.accountingRetained {
		t.Fatalf("unquiesced snapshot = %+v", snapshot)
	}

	charge := reservation.bindings["web"].charge
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
	if charge.state != candidateChargeActive || charge.refs != 0 || !charge.explicitRetained {
		t.Fatalf("unquiesced charge state=%v refs=%d retained=%v", charge.state, charge.refs, charge.explicitRetained)
	}

	cleanupLedger, _, _ := candidateCapacityTestLedger(t, 20, 20)
	cleanupReservation := candidateCapacityTestReserve(
		t,
		cleanupLedger,
		candidateCapacityTestSpec("app-2", "deployment-2", "web"),
	)
	candidateCapacityTestRecordReceipt(
		t,
		cleanupReservation,
		"web",
		"receipt-2",
		candidateReceiptCommitAcknowledged,
	)
	candidateCapacityTestMarkPresent(t, cleanupReservation, "web", "receipt-2")
	cleanup, err := cleanupReservation.enterCleanup("web", "receipt-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup.enterCommand(candidateCommandRemove); err != nil {
		t.Fatal(err)
	}
	cleanup.latchMutationUnquiesced()
	if err := cleanup.retireConfirmedAbsent(); !errors.Is(err, errCandidateMutationUnquiesced) {
		t.Fatalf("unquiesced cleanup retirement error = %v", err)
	}
	cleanupSnapshot, err := cleanup.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	cleanup.leave()
	if cleanupSnapshot.physical != candidatePhysicalPresent || !cleanupSnapshot.mutationUnquiesced ||
		!cleanupSnapshot.explicitRetained || cleanupSnapshot.logicallyRetired {
		t.Fatalf("unquiesced cleanup snapshot = %+v", cleanupSnapshot)
	}
	if err := cleanupReservation.release(); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateRetirementRosterRetainsExactHandedCharges(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
	spec := candidateCapacityTestSpec("app-1", "deployment-1", "api", "web")
	reservation := candidateCapacityTestReserve(t, ledger, spec)
	receipts := map[string]string{"api": "receipt-api", "web": "receipt-web"}
	for component, receipt := range receipts {
		candidateCapacityTestRecordReceipt(t, reservation, component, receipt, candidateReceiptCommitAcknowledged)
		candidateCapacityTestMarkPresent(t, reservation, component, receipt)
	}
	handoff, err := reservation.closeCleanupAndEnterRoster(receipts)
	if err != nil {
		t.Fatal(err)
	}
	if err := handoff.markHandedToRoute(); err != nil {
		t.Fatal(err)
	}
	handoff.leave()
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}

	t.Run("exact_full_roster_retains_charges", func(t *testing.T) {
		operation, err := ledger.enterRetirementRoster("app-1", "deployment-1", receipts)
		if err != nil {
			t.Fatal(err)
		}
		defer operation.leave()
		if err := operation.enterCommand("api", candidateCommandStop); err != nil {
			t.Fatal(err)
		}
		if err := operation.enterCommand("api", candidateCommandRemove); err != nil {
			t.Fatal(err)
		}
		if err := operation.markConfirmedAbsentRetained("api"); err != nil {
			t.Fatal(err)
		}
		component := operation.byName["api"]
		if component.charge.state != candidateChargeActive || !component.charge.explicitRetained ||
			component.charge.logicallyRetired || candidatePhysicalFromWord(component.receipt.state.Load()) != candidatePhysicalConfirmedAbsent {
			t.Fatalf("retirement charge=%+v word=%d", component.charge, component.receipt.state.Load())
		}
	})

	t.Run("partial_roster_refuses", func(t *testing.T) {
		if _, err := ledger.enterRetirementRoster("app-1", "deployment-1", map[string]string{"api": "receipt-api"}); !errors.Is(err, errCandidateReceiptMismatch) && !errors.Is(err, errCandidateCapacityBusy) {
			t.Fatalf("partial roster error = %v", err)
		}
	})

	t.Run("crossed_receipt_refuses", func(t *testing.T) {
		if _, err := ledger.enterRetirementRoster("app-1", "deployment-1", map[string]string{
			"api": "receipt-web", "web": "receipt-api",
		}); !errors.Is(err, errCandidateReceiptMismatch) {
			t.Fatalf("crossed receipt error = %v", err)
		}
	})

	t.Run("duplicate_command_refuses", func(t *testing.T) {
		operation, err := ledger.enterRetirementRoster("app-1", "deployment-1", receipts)
		if err != nil {
			t.Fatal(err)
		}
		defer operation.leave()
		if err := operation.enterCommand("api", candidateCommandStop); !errors.Is(err, errCandidateCommandEntered) {
			t.Fatalf("duplicate stop error = %v", err)
		}
	})
}

func TestCandidateAccountingHandoffTransfersWholeRoster(t *testing.T) {
	t.Run("full_predecessor_keeps_every_lease_live", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 40, 40)
		successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "api", "web")
		predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "api", "web")
		successorReservation, successorRoster, successorReceipts := candidateCapacityTestHandedRoster(
			t, ledger, successorSpec, "successor",
		)
		_, predecessorRoster, _ := candidateCapacityTestAbsentPredecessor(
			t, ledger, predecessorSpec, "predecessor",
		)
		successorLeases := map[string]*generatedcapacity.Lease{}
		predecessorLeases := map[string]*generatedcapacity.Lease{}
		for _, name := range successorSpec.components {
			successorLeases[name] = successorRoster.group.byName[name].charge.lease
			predecessorLeases[name] = predecessorRoster.group.byName[name].charge.lease
		}

		handoff, err := ledger.beginAccountingHandoff(successorRoster, predecessorRoster)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range successorSpec.components {
			charge := successorRoster.group.byName[name].charge
			if charge.owner != candidateAccountingTransferring || charge.lease != successorLeases[name] ||
				charge.state != candidateChargeActive || charge.logicallyRetired {
				t.Fatalf("successor %s charge = %+v", name, charge)
			}
			predecessorCharge := predecessorRoster.group.byName[name].charge
			if predecessorCharge.owner != candidateAccountingPredecessorRetiring ||
				predecessorCharge.lease != predecessorLeases[name] || predecessorCharge.state != candidateChargeActive ||
				predecessorCharge.logicallyRetired {
				t.Fatalf("predecessor %s charge = %+v", name, predecessorCharge)
			}
		}
		// Roster tokens may be returned after authorization failure. The exact
		// private handoff remains charged and retryable in the same process.
		successorRoster.leave()
		predecessorRoster.leave()
		if err := handoff.markGatewayUnlocked(); err != nil {
			t.Fatal(err)
		}
		if err := successorReservation.release(); err != nil {
			t.Fatal(err)
		}
		for _, name := range successorSpec.components {
			charge := handoff.successorGroup.byName[name].charge
			if charge.owner != candidateAccountingRouteActive || charge.lease != successorLeases[name] || charge.refs != 0 {
				t.Fatalf("route-active successor %s charge = %+v", name, charge)
			}
		}
		future, err := ledger.enterRetirementRoster(successorSpec.appID, successorSpec.deploymentID, successorReceipts)
		if err != nil {
			t.Fatal(err)
		}
		future.leave()
		candidateCapacityTestRequireCompetitorRefused(t, ledger, "app-2", "deployment-competitor")
	})

	t.Run("empty_predecessor_transfers_without_release", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		spec := candidateCapacityTestSpec("app-1", "deployment-new", "api", "web")
		reservation, roster, receipts := candidateCapacityTestHandedRoster(t, ledger, spec, "successor")
		leases := map[string]*generatedcapacity.Lease{}
		for _, name := range spec.components {
			leases[name] = roster.group.byName[name].charge.lease
		}
		handoff, err := ledger.beginAccountingHandoff(roster, nil)
		if err != nil {
			t.Fatal(err)
		}
		roster.leave()
		if err := handoff.markGatewayUnlocked(); err != nil {
			t.Fatal(err)
		}
		if err := reservation.release(); err != nil {
			t.Fatal(err)
		}
		for _, name := range spec.components {
			charge := handoff.successorGroup.byName[name].charge
			if charge.owner != candidateAccountingRouteActive || charge.lease != leases[name] || charge.refs != 0 {
				t.Fatalf("empty-predecessor successor %s charge = %+v", name, charge)
			}
		}
		future, err := ledger.enterRetirementRoster(spec.appID, spec.deploymentID, receipts)
		if err != nil {
			t.Fatal(err)
		}
		future.leave()
		candidateCapacityTestRequireCompetitorRefused(t, ledger, "app-2", "deployment-competitor")
	})
}

func TestCandidateAccountingHandoffRejectsInvalidBatchWithoutMutation(t *testing.T) {
	t.Run("successor_not_handed", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 10, 10)
		spec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
		reservation := candidateCapacityTestReserve(t, ledger, spec)
		candidateCapacityTestRecordReceipt(t, reservation, "web", "receipt-web", candidateReceiptCommitAcknowledged)
		candidateCapacityTestMarkPresent(t, reservation, "web", "receipt-web")
		roster, err := reservation.closeCleanupAndEnterRoster(map[string]string{"web": "receipt-web"})
		if err != nil {
			t.Fatal(err)
		}
		defer roster.leave()
		charge := roster.group.byName["web"].charge
		lease := charge.lease
		if _, err := ledger.beginAccountingHandoff(roster, nil); !errors.Is(err, errCandidatePhysicalTransition) {
			t.Fatalf("not-handed error = %v", err)
		}
		if charge.owner != candidateAccountingCandidate || charge.lease != lease || !reservation.bindings["web"].attached {
			t.Fatalf("not-handed charge = %+v binding=%+v", charge, reservation.bindings["web"])
		}
	})

	t.Run("predecessor_not_absent", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
		predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "web")
		successorReservation, successor, _ := candidateCapacityTestHandedRoster(t, ledger, successorSpec, "successor")
		predecessorReservation, predecessorHanded, predecessorReceipts := candidateCapacityTestHandedRoster(
			t, ledger, predecessorSpec, "predecessor",
		)
		predecessorHanded.leave()
		if err := predecessorReservation.retainAll(); err != nil {
			t.Fatal(err)
		}
		if err := predecessorReservation.release(); err != nil {
			t.Fatal(err)
		}
		predecessor, err := ledger.enterRetirementRoster(
			predecessorSpec.appID, predecessorSpec.deploymentID, predecessorReceipts,
		)
		if err != nil {
			t.Fatal(err)
		}
		defer successor.leave()
		defer predecessor.leave()
		successorCharge := successor.group.byName["web"].charge
		predecessorCharge := predecessor.group.byName["web"].charge
		if _, err := ledger.beginAccountingHandoff(successor, predecessor); !errors.Is(err, errCandidatePhysicalTransition) {
			t.Fatalf("present predecessor error = %v", err)
		}
		if successorCharge.owner != candidateAccountingCandidate || predecessorCharge.owner != candidateAccountingCandidate ||
			!successorReservation.bindings["web"].attached {
			t.Fatalf("mutated charges successor=%+v predecessor=%+v", successorCharge, predecessorCharge)
		}
	})

	t.Run("crossed_ledger", func(t *testing.T) {
		successorLedger, _, _ := candidateCapacityTestLedger(t, 10, 10)
		predecessorLedger, _, _ := candidateCapacityTestLedger(t, 10, 10)
		successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
		predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "web")
		reservation, successor, _ := candidateCapacityTestHandedRoster(t, successorLedger, successorSpec, "successor")
		_, predecessor, _ := candidateCapacityTestAbsentPredecessor(t, predecessorLedger, predecessorSpec, "predecessor")
		defer successor.leave()
		defer predecessor.leave()
		if _, err := successorLedger.beginAccountingHandoff(successor, predecessor); !errors.Is(err, errCandidateCapacityInvalid) {
			t.Fatalf("crossed ledger error = %v", err)
		}
		charge := successor.group.byName["web"].charge
		if charge.owner != candidateAccountingCandidate || !reservation.bindings["web"].attached {
			t.Fatalf("crossed ledger successor = %+v", charge)
		}
	})
}

func TestCandidateRouteActiveOwnershipSurvivesCandidateAliases(t *testing.T) {
	ledger, _, _ := candidateCapacityTestLedger(t, 10, 10)
	spec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
	reservation, roster, receipts := candidateCapacityTestHandedRoster(t, ledger, spec, "successor")
	alias := candidateCapacityTestReserve(t, ledger, spec)
	charge := roster.group.byName["web"].charge
	lease := charge.lease
	if charge.refs != 2 {
		t.Fatalf("shared charge refs = %d", charge.refs)
	}
	handoff, err := ledger.beginAccountingHandoff(roster, nil)
	if err != nil {
		t.Fatal(err)
	}
	roster.leave()
	if err := handoff.markGatewayUnlocked(); err != nil {
		t.Fatal(err)
	}
	if err := reservation.release(); err != nil {
		t.Fatal(err)
	}
	if err := alias.release(); err != nil {
		t.Fatal(err)
	}
	if charge.owner != candidateAccountingRouteActive || charge.state != candidateChargeActive ||
		charge.lease != lease || charge.refs != 0 || charge.logicallyRetired {
		t.Fatalf("route-active charge after alias leave = %+v", charge)
	}
	if _, err := ledger.reserve(context.Background(), spec); !errors.Is(err, errCandidateCapacityStale) {
		t.Fatalf("route-active candidate reserve error = %v", err)
	}
	future, err := ledger.enterRetirementRoster(spec.appID, spec.deploymentID, receipts)
	if err != nil {
		t.Fatal(err)
	}
	future.leave()
	candidateCapacityTestRequireCompetitorRefused(t, ledger, "app-2", "deployment-competitor")
}

func TestCandidateAccountingHandoffReleasesOnlyExactPredecessorOnce(t *testing.T) {
	t.Run("release_before_gateway_unlock_refuses", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
		predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "web")
		_, successor, _ := candidateCapacityTestHandedRoster(t, ledger, successorSpec, "successor")
		_, predecessor, _ := candidateCapacityTestAbsentPredecessor(t, ledger, predecessorSpec, "predecessor")
		defer successor.leave()
		defer predecessor.leave()
		handoff, err := ledger.beginAccountingHandoff(successor, predecessor)
		if err != nil {
			t.Fatal(err)
		}
		charge := predecessor.group.byName["web"].charge
		lease := charge.lease
		if err := handoff.releasePredecessorOnce(); !errors.Is(err, errCandidateCapacityStale) {
			t.Fatalf("release before unlock error = %v", err)
		}
		if charge.lease != lease || charge.owner != candidateAccountingPredecessorRetiring ||
			charge.state != candidateChargeActive || charge.logicallyRetired {
			t.Fatalf("release before unlock changed predecessor = %+v", charge)
		}
	})

	t.Run("crossed_entry_refuses_without_partial_release", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 40, 40)
		successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "api", "web")
		predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "api", "web")
		_, successor, _ := candidateCapacityTestHandedRoster(t, ledger, successorSpec, "successor")
		_, predecessor, _ := candidateCapacityTestAbsentPredecessor(t, ledger, predecessorSpec, "predecessor")
		defer successor.leave()
		defer predecessor.leave()
		handoff, err := ledger.beginAccountingHandoff(successor, predecessor)
		if err != nil {
			t.Fatal(err)
		}
		if err := handoff.markGatewayUnlocked(); err != nil {
			t.Fatal(err)
		}
		crossed := predecessor.group.byName["web"].charge
		crossed.epoch++
		if err := handoff.releasePredecessorOnce(); !errors.Is(err, errCandidateCapacityStale) {
			t.Fatalf("crossed release error = %v", err)
		}
		for _, name := range predecessorSpec.components {
			charge := predecessor.group.byName[name].charge
			if charge.lease == nil || charge.state != candidateChargeActive ||
				charge.owner != candidateAccountingPredecessorRetiring || charge.logicallyRetired {
				t.Fatalf("crossed release partially changed %s = %+v", name, charge)
			}
		}
	})

	t.Run("exact_predecessor_release_is_idempotent", func(t *testing.T) {
		ledger, _, _ := candidateCapacityTestLedger(t, 20, 20)
		successorSpec := candidateCapacityTestSpec("app-1", "deployment-new", "web")
		predecessorSpec := candidateCapacityTestSpec("app-1", "deployment-old", "web")
		_, successor, _ := candidateCapacityTestHandedRoster(t, ledger, successorSpec, "successor")
		_, predecessor, _ := candidateCapacityTestAbsentPredecessor(t, ledger, predecessorSpec, "predecessor")
		defer successor.leave()
		defer predecessor.leave()
		handoff, err := ledger.beginAccountingHandoff(successor, predecessor)
		if err != nil {
			t.Fatal(err)
		}
		successorCharge := successor.group.byName["web"].charge
		successorLease, successorEpoch := successorCharge.lease, successorCharge.epoch
		if err := handoff.markGatewayUnlocked(); err != nil {
			t.Fatal(err)
		}
		if err := handoff.releasePredecessorOnce(); err != nil {
			t.Fatal(err)
		}
		if err := handoff.releasePredecessorOnce(); err != nil {
			t.Fatalf("release replay: %v", err)
		}
		if err := handoff.validateFinalState(); err != nil {
			t.Fatal(err)
		}
		predecessorCharge := predecessor.group.byName["web"].charge
		if predecessorCharge.lease != nil || predecessorCharge.state != candidateChargeReleased ||
			predecessorCharge.owner != candidateAccountingPredecessorRetired || !predecessorCharge.logicallyRetired {
			t.Fatalf("terminal predecessor = %+v", predecessorCharge)
		}
		if successorCharge.lease != successorLease || successorCharge.epoch != successorEpoch ||
			successorCharge.owner != candidateAccountingRouteActive || successorCharge.logicallyRetired {
			t.Fatalf("terminal successor = %+v", successorCharge)
		}
	})
}

func TestCandidateAccountingHandoffRetainsSuccessorMemoryCharge(t *testing.T) {
	for _, test := range []struct {
		name            string
		withPredecessor bool
	}{{"full_predecessor", true}, {"empty_predecessor", false}} {
		t.Run(test.name, func(t *testing.T) {
			candidateCapacityTestAccountingContinuity(t, test.withPredecessor, true)
		})
	}
}

func TestCandidateAccountingHandoffRetainsSuccessorDiskCharge(t *testing.T) {
	for _, test := range []struct {
		name            string
		withPredecessor bool
	}{{"full_predecessor", true}, {"empty_predecessor", false}} {
		t.Run(test.name, func(t *testing.T) {
			candidateCapacityTestAccountingContinuity(t, test.withPredecessor, false)
		})
	}
}

func candidateCapacityTestAccountingContinuity(t *testing.T, withPredecessor, memoryOnly bool) {
	t.Helper()
	ledger, _, source := candidateCapacityTestLedger(t, 20, 20)
	currentSpec := candidateCapacityTestSpec("app-current", "deployment-current", "web")
	_, current, currentReceipts := candidateCapacityTestHandedRoster(t, ledger, currentSpec, "current")
	defer current.leave()
	currentCharge := current.group.byName["web"].charge
	currentLease, currentEpoch := currentCharge.lease, currentCharge.epoch

	var predecessor *candidateRetirementRosterOperation
	if withPredecessor {
		predecessorSpec := candidateCapacityTestSpec("app-current", "deployment-old", "web")
		_, predecessor, _ = candidateCapacityTestAbsentPredecessor(t, ledger, predecessorSpec, "old")
		defer predecessor.leave()
	}
	handoff, err := ledger.beginAccountingHandoff(current, predecessor)
	if err != nil {
		t.Fatal(err)
	}
	current.leave()
	if err := handoff.markGatewayUnlocked(); err != nil {
		t.Fatal(err)
	}
	if err := handoff.releasePredecessorOnce(); err != nil {
		t.Fatal(err)
	}
	if currentCharge.lease != currentLease || currentCharge.epoch != currentEpoch ||
		currentCharge.owner != candidateAccountingRouteActive || currentCharge.logicallyRetired {
		t.Fatalf("route-active successor = %+v", currentCharge)
	}

	setSnapshot := func(memory, disk uint64) {
		source.mu.Lock()
		source.snapshot = generatedcapacity.Snapshot{MemoryAvailableBytes: memory, DiskAvailableBytes: disk}
		source.mu.Unlock()
	}
	if memoryOnly {
		setSnapshot(10, 100)
	} else {
		setSnapshot(100, 10)
	}
	candidateCapacityTestRequireCompetitorRefused(t, ledger, "app-competitor", "deployment-competitor")

	// A later authentic successor may release this exact route-active charge
	// only after it becomes the fixed, freshly absent predecessor of that next
	// handoff. Replaying the release cannot return another share.
	setSnapshot(20, 20)
	nextSpec := candidateCapacityTestSpec("app-current", "deployment-next", "web")
	_, next, _ := candidateCapacityTestHandedRoster(t, ledger, nextSpec, "next")
	defer next.leave()
	futurePredecessor, err := ledger.enterRetirementRoster(
		currentSpec.appID, currentSpec.deploymentID, currentReceipts)
	if err != nil {
		t.Fatal(err)
	}
	defer futurePredecessor.leave()
	if err := futurePredecessor.markConfirmedAbsentRetained("web"); err != nil {
		t.Fatal(err)
	}
	future, err := ledger.beginAccountingHandoff(next, futurePredecessor)
	if err != nil {
		t.Fatal(err)
	}
	next.leave()
	if err := future.markGatewayUnlocked(); err != nil {
		t.Fatal(err)
	}
	if err := future.releasePredecessorOnce(); err != nil {
		t.Fatal(err)
	}
	if err := future.releasePredecessorOnce(); err != nil {
		t.Fatalf("future release replay: %v", err)
	}
	if currentCharge.lease != nil || currentCharge.state != candidateChargeReleased ||
		currentCharge.owner != candidateAccountingPredecessorRetired || !currentCharge.logicallyRetired {
		t.Fatalf("later authenticated release = %+v", currentCharge)
	}
	if memoryOnly {
		setSnapshot(11, 100)
	} else {
		setSnapshot(100, 11)
	}
	competitor := candidateCapacityTestSpec("app-later", "deployment-later", "competitor")
	competitor.limits = generatedcapacity.Limits{MemoryBytes: 1, DiskBytes: 1}
	admitted := candidateCapacityTestReserve(t, ledger, competitor)
	defer func() {
		if err := admitted.release(); err != nil {
			t.Error(err)
		}
	}()
	second := candidateCapacityTestSpec("app-later-2", "deployment-later-2", "competitor")
	second.limits = competitor.limits
	if _, err := ledger.reserve(context.Background(), second); !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("later release admitted more than one restored share: %v", err)
	}
}

func TestCandidateInvalidAndZeroValuesRefuseSafely(t *testing.T) {
	if _, err := newCandidateCapacityLedger(nil); !errors.Is(err, errCandidateCapacityInvalid) {
		t.Fatalf("nil budget error = %v", err)
	}
	var ledger candidateCapacityLedger
	if _, err := ledger.reserve(context.Background(), candidateGroupSpec{}); !errors.Is(err, errCandidateCapacityInvalid) {
		t.Fatalf("zero ledger reserve error = %v", err)
	}
	var reservation candidateCapacityReservation
	if err := reservation.retainAll(); !errors.Is(err, errCandidateCapacityInvalid) {
		t.Fatalf("zero reservation retain error = %v", err)
	}
	if err := reservation.release(); !errors.Is(err, errCandidateCapacityInvalid) {
		t.Fatalf("zero reservation release error = %v", err)
	}
	var operation candidateComponentOperation
	operation.leave()
	operation.latchMutationUnquiesced()
	var roster candidateRosterOperation
	roster.leave()
}
