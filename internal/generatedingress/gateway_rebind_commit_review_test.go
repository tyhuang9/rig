package generatedingress

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func gatewayRebindCommitReviewFixture(t *testing.T) (gatewayCurrentStateFixture,
	*gatewayRebindAdmissionRepositoryFake, GatewayRebindProposalInput, GatewayRebindProposalInspection,
) {
	t.Helper()
	f := newGatewayCurrentStateFixture(t)
	f.manager.mu = newContextMutex()
	f.manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	if err := f.store.installBaseline(f.baseline); err != nil {
		t.Fatal(err)
	}
	repository := &gatewayRebindAdmissionRepositoryFake{
		gatewayRebindProposalRepositoryFake: gatewayRebindProposalRepositoryForCurrentFixture(t, f),
	}
	input := GatewayRebindProposalInput{
		OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: f.baseline.Profile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: appaccess.GatewayProfileSpec{
			SelectedIPv4: f.baseline.Profile.SelectedIPv4, InterfaceID: f.baseline.Profile.InterfaceID,
			PortStart: f.baseline.Profile.PortStart, PortEnd: f.baseline.Profile.PortEnd,
		},
	}
	inspection, err := f.manager.InspectGatewayRebindProposal(context.Background(), repository, input)
	if err != nil || len(inspection.Roster) == 0 || len(inspection.RuntimeHeads) == 0 ||
		inspection.Roster[0].PredecessorTransferDigest == nil {
		t.Fatalf("complete rebound review fixture: %v", err)
	}
	return f, repository, input, inspection
}

func refreshGatewayRebindCommitReviewFixture(t *testing.T, value *GatewayRebindProposalInspection) {
	t.Helper()
	var err error
	for index := range value.Roster {
		value.Roster[index].OperationID = value.Spec.OperationID
		value.Roster[index].EntryDigest, err = appaccess.GatewayRebindRosterEntryV2Digest(value.Roster[index])
		if err != nil {
			t.Fatal(err)
		}
	}
	value.Spec.RosterCount, value.Spec.RuntimeHeadsCount = int64(len(value.Roster)), int64(len(value.RuntimeHeads))
	value.Spec.RosterDigest, err = appaccess.GatewayRebindRosterV2Digest(value.Roster)
	if err != nil {
		t.Fatal(err)
	}
	value.Spec.RuntimeHeadsDigest, err = appaccess.GatewayRebindRuntimeHeadsV2Digest(value.Spec.OperationID, value.RuntimeHeads)
	if err != nil {
		t.Fatal(err)
	}
	value.SpecDigest, err = appaccess.GatewayRebindSpecV2Digest(value.Spec)
	if err != nil {
		t.Fatal(err)
	}
	value.SuccessorProfileSpecDigest, err = appaccess.GatewayProfileSpecDigest(value.Spec.SuccessorProfile)
	if err != nil {
		t.Fatal(err)
	}
	value.ProtectedGeneration = value.Spec.SuccessorProtectedGeneration
	value.PredecessorCheckpointDigest = value.Spec.Predecessor.PredecessorCheckpointDigest
	value.SourceStateVersion = value.Spec.Predecessor.SourceStateVersion
	value.SourceStateRevision = value.Spec.Predecessor.SourceStateRevision
	value.SourceStateDigest = value.Spec.Predecessor.SourceStateDigest
}

func differentGatewayRebindReviewDigest(value string) string {
	digest := strings.Repeat("a", 64)
	if value == digest {
		return strings.Repeat("b", 64)
	}
	return digest
}

func TestGatewayRebindCommitReviewBindsReviewedAuthority(t *testing.T) {
	_, _, _, inspection := gatewayRebindCommitReviewFixture(t)
	baseline, err := newGatewayRebindCommitReview(inspection)
	if err != nil || baseline.Version != 1 || !validSHA256(baseline.ProposalDigest) ||
		baseline.ConfigureApprovalDigest != inspection.SuccessorProfileSpecDigest ||
		baseline.RebindApprovalDigest != inspection.SpecDigest ||
		baseline.ProposalDigest == baseline.ConfigureApprovalDigest || baseline.ProposalDigest == baseline.RebindApprovalDigest {
		t.Fatalf("distinct review/approval purposes missing: %v", err)
	}
	tests := []struct {
		name string
		edit func(*GatewayRebindProposalInspection)
	}{
		{"operation", func(v *GatewayRebindProposalInspection) { v.Spec.OperationID = uuid.NewString() }},
		{"successor_profile_identity", func(v *GatewayRebindProposalInspection) { v.Spec.SuccessorProfileRevisionID = uuid.NewString() }},
		{"successor_profile_operation", func(v *GatewayRebindProposalInspection) { v.Spec.SuccessorProfileOperationID = uuid.NewString() }},
		{"successor_interface", func(v *GatewayRebindProposalInspection) { v.Spec.SuccessorProfile.InterfaceID += "-changed" }},
		{"source_revision", func(v *GatewayRebindProposalInspection) { v.Spec.Predecessor.SourceStateRevision++ }},
		{"source_digest", func(v *GatewayRebindProposalInspection) {
			v.Spec.Predecessor.SourceStateDigest = differentGatewayRebindReviewDigest(v.SourceStateDigest)
		}},
		{"checkpoint", func(v *GatewayRebindProposalInspection) {
			v.Spec.Predecessor.PredecessorCheckpointDigest = differentGatewayRebindReviewDigest(v.PredecessorCheckpointDigest)
		}},
		{"generation", func(v *GatewayRebindProposalInspection) { v.Spec.SuccessorProtectedGeneration++ }},
		{"runtime_generation", func(v *GatewayRebindProposalInspection) { v.RuntimeHeads[0].Generation++ }},
		{"runtime_timestamp", func(v *GatewayRebindProposalInspection) {
			v.RuntimeHeads[0].UpdatedAt = v.RuntimeHeads[0].UpdatedAt.Add(time.Nanosecond)
		}},
		{"raw_grant", func(v *GatewayRebindProposalInspection) {
			v.Roster[0].GrantProtectedStateDigest = differentGatewayRebindReviewDigest(v.Roster[0].GrantProtectedStateDigest)
		}},
		{"prior_transfer", func(v *GatewayRebindProposalInspection) {
			*v.Roster[0].PredecessorTransferDigest = differentGatewayRebindReviewDigest(*v.Roster[0].PredecessorTransferDigest)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneGatewayRebindCommitInspection(inspection)
			test.edit(&changed)
			refreshGatewayRebindCommitReviewFixture(t, &changed)
			review, err := newGatewayRebindCommitReview(changed)
			if err != nil || review.ProposalDigest == baseline.ProposalDigest {
				t.Fatalf("changed reviewed authority retained consent digest: %v", err)
			}
		})
	}
}

func TestGatewayRebindCommitReviewRejectsInconsistentInspection(t *testing.T) {
	_, _, _, inspection := gatewayRebindCommitReviewFixture(t)
	tests := []struct {
		name string
		edit func(*GatewayRebindProposalInspection)
	}{
		{"empty", func(v *GatewayRebindProposalInspection) { *v = GatewayRebindProposalInspection{} }},
		{"spec_digest", func(v *GatewayRebindProposalInspection) {
			v.SpecDigest = differentGatewayRebindReviewDigest(v.SpecDigest)
		}},
		{"profile_digest", func(v *GatewayRebindProposalInspection) {
			v.SuccessorProfileSpecDigest = differentGatewayRebindReviewDigest(v.SuccessorProfileSpecDigest)
		}},
		{"checkpoint", func(v *GatewayRebindProposalInspection) {
			v.PredecessorCheckpointDigest = differentGatewayRebindReviewDigest(v.PredecessorCheckpointDigest)
		}},
		{"source_version", func(v *GatewayRebindProposalInspection) { v.SourceStateVersion++ }},
		{"source_revision", func(v *GatewayRebindProposalInspection) { v.SourceStateRevision++ }},
		{"source_digest", func(v *GatewayRebindProposalInspection) {
			v.SourceStateDigest = differentGatewayRebindReviewDigest(v.SourceStateDigest)
		}},
		{"generation", func(v *GatewayRebindProposalInspection) { v.ProtectedGeneration++ }},
		{"missing_roster", func(v *GatewayRebindProposalInspection) { v.Roster = nil }},
		{"missing_heads", func(v *GatewayRebindProposalInspection) { v.RuntimeHeads = nil }},
		{"entry_digest", func(v *GatewayRebindProposalInspection) {
			v.Roster[0].EntryDigest = differentGatewayRebindReviewDigest(v.Roster[0].EntryDigest)
		}},
		{"raw_grant_drift", func(v *GatewayRebindProposalInspection) { v.Roster[0].GrantStateSequence++ }},
		{"head_drift", func(v *GatewayRebindProposalInspection) { v.RuntimeHeads[0].Generation++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneGatewayRebindCommitInspection(inspection)
			test.edit(&changed)
			result, err := newGatewayRebindCommitReview(changed)
			var domain *Error
			if !errors.As(err, &domain) || domain.Code != DiagnosticValidationFailed ||
				!reflect.DeepEqual(result, GatewayRebindCommitReview{}) {
				t.Fatalf("inconsistent inspection produced a review: %v", err)
			}
		})
	}
}

func TestGatewayRebindCommitReviewCanonicalizesCensusWithoutAliasing(t *testing.T) {
	_, _, _, inspection := gatewayRebindCommitReviewFixture(t)
	baseline, err := newGatewayRebindCommitReview(inspection)
	if err != nil {
		t.Fatal(err)
	}
	shifted := cloneGatewayRebindCommitInspection(inspection)
	shifted.RuntimeHeads[0].UpdatedAt = shifted.RuntimeHeads[0].UpdatedAt.In(time.FixedZone("review-fixture", 3600))
	review, err := newGatewayRebindCommitReview(shifted)
	if err != nil || review.ProposalDigest != baseline.ProposalDigest || !reflect.DeepEqual(review.Inspection, shifted) {
		t.Fatalf("equivalent census timestamp changed digest or returned inspection: %v", err)
	}
	*shifted.Roster[0].PredecessorTransferDigest = differentGatewayRebindReviewDigest(*shifted.Roster[0].PredecessorTransferDigest)
	shifted.RuntimeHeads[0].Generation++
	again, err := newGatewayRebindCommitReview(review.Inspection)
	if err != nil || again.ProposalDigest != baseline.ProposalDigest {
		t.Fatalf("caller mutation changed retained review: %v", err)
	}
	*review.Inspection.Roster[0].PredecessorTransferDigest = "review-only-mutation"
	if *shifted.Roster[0].PredecessorTransferDigest == "review-only-mutation" {
		t.Fatal("review exposed a caller-owned transfer pointer")
	}

	empty := cloneGatewayRebindCommitInspection(inspection)
	empty.Roster, empty.RuntimeHeads = nil, nil
	refreshGatewayRebindCommitReviewFixture(t, &empty)
	nilReview, err := newGatewayRebindCommitReview(empty)
	if err != nil {
		t.Fatal(err)
	}
	empty.Roster, empty.RuntimeHeads = []appaccess.GatewayRebindRosterEntryV2{}, []appaccess.GatewayRebindRuntimeHead{}
	emptyReview, err := newGatewayRebindCommitReview(empty)
	if err != nil || nilReview.ProposalDigest != emptyReview.ProposalDigest ||
		nilReview.Inspection.Roster != nil || emptyReview.Inspection.Roster == nil ||
		nilReview.Inspection.RuntimeHeads != nil || emptyReview.Inspection.RuntimeHeads == nil {
		t.Fatalf("empty census canonicalization changed exact returned representation: %v", err)
	}
}

type gatewayRebindCommitReviewUnavailableRepository struct {
	*gatewayRebindAdmissionRepositoryFake
}

func (r gatewayRebindCommitReviewUnavailableRepository) GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
	return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("private repository read error")
}

func TestInspectGatewayRebindCommitReviewRemainsReadOnly(t *testing.T) {
	f, repository, input, inspection := gatewayRebindCommitReviewFixture(t)
	runner := &gatewayCurrentNoCommandRunner{}
	f.manager.runner = runner
	protectedBytes := func() map[string][32]byte {
		t.Helper()
		result := make(map[string][32]byte)
		err := filepath.WalkDir(f.dataRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256(body)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := protectedBytes()
	review, err := f.manager.InspectGatewayRebindCommitReview(context.Background(), repository, input)
	if err != nil || !reflect.DeepEqual(review.Inspection, inspection) || !validSHA256(review.ProposalDigest) {
		t.Fatalf("stable advisory review: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		repo gatewayRebindProposalRepository
	}{
		{"cancelled", ctx, repository},
		{"unavailable", context.Background(), gatewayRebindCommitReviewUnavailableRepository{repository}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := f.manager.InspectGatewayRebindCommitReview(test.ctx, test.repo, input)
			if err == nil || strings.Contains(err.Error(), "private repository") || !reflect.DeepEqual(result, GatewayRebindCommitReview{}) {
				t.Fatalf("failed read produced consent or leaked repository error: %v", err)
			}
		})
	}
	if repository.claimCalls != 0 || runner.calls != 0 || !reflect.DeepEqual(before, protectedBytes()) {
		t.Fatalf("advisory review caused effects: claims=%d commands=%d", repository.claimCalls, runner.calls)
	}
}
