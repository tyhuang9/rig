package generatedingress

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
)

func candidateHeadFixture() (candidateHeadPins, candidateHeadSnapshot) {
	pins := candidateHeadPins{
		appID: "00000000-0000-4000-8000-000000000001", deploymentID: "00000000-0000-4000-8000-000000000002",
		releaseID: "00000000-0000-4000-8000-000000000003", slot: "green",
		previousDeploymentID: "00000000-0000-4000-8000-000000000004", previousReleaseID: "00000000-0000-4000-8000-000000000005", previousSlot: "blue",
	}
	return pins, candidateHeadSnapshot{appID: pins.appID, deploymentID: pins.previousDeploymentID, releaseID: pins.previousReleaseID, slot: pins.previousSlot, generation: 7}
}

func TestCandidateHeadPredecessorGenerationIsBoundOnce(t *testing.T) {
	pins, head := candidateHeadFixture()
	registry := newCandidateHeadRegistry()
	anchor, err := registry.bindPredecessor(pins, head)
	if err != nil || anchor.checkPredecessor(head) != nil {
		t.Fatal("exact predecessor refused")
	}
	alias, err := registry.bindPredecessor(pins, head)
	if err != nil || alias != anchor {
		t.Fatal("alias did not reuse exact immutable snapshot")
	}
	changed := head
	changed.generation++
	if _, err := registry.bindPredecessor(pins, changed); !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
		t.Fatal("repin refreshed generation")
	}
	if anchor.checkPredecessor(changed) == nil {
		t.Fatal("changed generation passed final comparison")
	}
	for _, mutate := range []func(*candidateHeadSnapshot){
		func(h *candidateHeadSnapshot) { h.appID = "foreign" }, func(h *candidateHeadSnapshot) { h.deploymentID = pins.deploymentID },
		func(h *candidateHeadSnapshot) { h.releaseID = pins.releaseID }, func(h *candidateHeadSnapshot) { h.slot = pins.slot },
	} {
		changed = head
		mutate(&changed)
		if anchor.checkPredecessor(changed) == nil {
			t.Fatal("incomplete head identity compared")
		}
	}
}

func TestCandidateHeadMismatchNeverFabricatesGeneration(t *testing.T) {
	pins, head := candidateHeadFixture()
	registry := newCandidateHeadRegistry()
	unrelated := head
	unrelated.deploymentID = pins.deploymentID
	unrelated.generation = 99
	if _, err := registry.bindPredecessor(pins, unrelated); !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
		t.Fatal("unrelated head accepted")
	}
	group := registry.groups[candidateCapacityGroupKey{appID: pins.appID, deploymentID: pins.deploymentID}]
	if group == nil || group.predecessor != nil || group.self != nil {
		t.Fatal("unknown generation was invented")
	}
	anchor, err := registry.bindPredecessor(pins, head)
	if err != nil || anchor.snapshot.generation != 7 {
		t.Fatal("exact later head did not bind its actual generation")
	}
}

func TestCandidateHeadInitialEmptyRequiresExactZero(t *testing.T) {
	pins, _ := candidateHeadFixture()
	pins.previousDeploymentID, pins.previousReleaseID, pins.previousSlot, pins.slot = "", "", "", "blue"
	head := candidateHeadSnapshot{appID: pins.appID}
	registry := newCandidateHeadRegistry()
	for _, generation := range []int64{-1, 1, 9} {
		changed := head
		changed.generation = generation
		if _, err := registry.bindPredecessor(pins, changed); err == nil {
			t.Fatal("nonzero empty head accepted")
		}
	}
	anchor, err := registry.bindPredecessor(pins, head)
	if err != nil || anchor.checkPredecessor(head) != nil {
		t.Fatal("genuine empty generation0 refused")
	}
}

func TestCandidateHeadSelfIsReadOnlyAndNeverInfersPredecessor(t *testing.T) {
	pins, predecessor := candidateHeadFixture()
	self := candidateHeadSnapshot{
		appID: pins.appID, deploymentID: pins.deploymentID, releaseID: pins.releaseID, slot: pins.slot,
		generation: 42, activationReceiptID: "00000000-0000-4000-8000-000000000001",
	}
	registry := newCandidateHeadRegistry()
	for name, receiptID := range map[string]string{"missing": "", "malformed": "not-a-receipt"} {
		invalid := self
		invalid.activationReceiptID = receiptID
		if _, err := registry.bindSelf(pins, invalid); !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
			t.Fatalf("%s activation receipt error=%v", name, err)
		}
	}
	anchor, err := registry.bindSelf(pins, self)
	if err != nil || anchor.checkRouteObservation(self) != nil || anchor.checkPredecessor(self) == nil {
		t.Fatal("self head granted wrong authority")
	}
	group := registry.groups[candidateCapacityGroupKey{appID: pins.appID, deploymentID: pins.deploymentID}]
	if group.predecessor != nil {
		t.Fatal("historical generation inferred from self")
	}
	if _, err := registry.bindPredecessor(pins, predecessor); err == nil {
		t.Fatal("read-only group converted to predecessor mutation")
	}
	changed := self
	changed.generation++
	if _, err := registry.bindSelf(pins, changed); err == nil || anchor.checkRouteObservation(changed) == nil {
		t.Fatal("self observation refreshed after drift")
	}
}

func TestCandidateHeadSelfRequiresKnownSuccessorWithoutOverflow(t *testing.T) {
	pins, predecessor := candidateHeadFixture()
	for _, generation := range []int64{7, math.MaxInt64} {
		registry := newCandidateHeadRegistry()
		predecessor.generation = generation
		if _, err := registry.bindPredecessor(pins, predecessor); err != nil {
			t.Fatal(err)
		}
		self := candidateHeadSnapshot{
			appID: pins.appID, deploymentID: pins.deploymentID, releaseID: pins.releaseID, slot: pins.slot,
			generation: generation, activationReceiptID: "00000000-0000-4000-8000-000000000001",
		}
		if _, err := registry.bindSelf(pins, self); err == nil {
			t.Fatal("non-successor self accepted")
		}
		if generation != math.MaxInt64 {
			self.generation++
			if _, err := registry.bindSelf(pins, self); err != nil {
				t.Fatal("exact successor refused")
			}
		}
	}
}

func TestCandidateHeadConcurrentFirstBindingHasOneGeneration(t *testing.T) {
	pins, head := candidateHeadFixture()
	registry := newCandidateHeadRegistry()
	start := make(chan struct{})
	results := make(chan candidateHeadAnchor, 2)
	var workers sync.WaitGroup
	for _, generation := range []int64{7, 8} {
		workers.Add(1)
		go func(generation int64) {
			defer workers.Done()
			<-start
			observed := head
			observed.generation = generation
			anchor, err := registry.bindPredecessor(pins, observed)
			if err == nil {
				results <- anchor
			}
		}(generation)
	}
	close(start)
	workers.Wait()
	close(results)
	winners := 0
	for anchor := range results {
		winners++
		if anchor.snapshot.generation != 7 && anchor.snapshot.generation != 8 {
			t.Fatal("invented generation")
		}
	}
	if winners != 1 {
		t.Fatalf("generation winners=%d", winners)
	}
}

func TestCandidateHeadRejectsInvalidAndChangedPins(t *testing.T) {
	pins, head := candidateHeadFixture()
	for _, registry := range []*candidateHeadRegistry{nil, {}} {
		if _, err := registry.bindPredecessor(pins, head); err == nil {
			t.Fatal("unconfigured registry accepted")
		}
	}
	registry := newCandidateHeadRegistry()
	if _, err := registry.bindPredecessor(pins, head); err != nil {
		t.Fatal(err)
	}
	changed := pins
	changed.releaseID = "00000000-0000-4000-8000-000000000006"
	if _, err := registry.bindPredecessor(changed, head); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatal("immutable pins drift accepted")
	}
	if (candidateHeadAnchor{}).checkPredecessor(head) == nil || (candidateHeadAnchor{}).checkRouteObservation(head) == nil {
		t.Fatal("zero anchor accepted")
	}
}
