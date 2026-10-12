package generatedingress

import (
	"context"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayRebindCommitReviewVersion = 1
const gatewayRebindCommitReviewPurpose = "lan_gateway_rebind_commit_review"

// GatewayRebindProposalRepository is the read-only SQL boundary for advisory
// review. The alias lets controller interfaces name the existing contract
// without granting claim or transition authority.
type GatewayRebindProposalRepository = gatewayRebindProposalRepository

// GatewayRebindCommitRequest carries only reviewed candidate values. The
// authenticated actor, live session, and physical proof are supplied by the
// controller and locked runtime boundary, never by this request.
type GatewayRebindCommitRequest struct {
	Version                 int
	Proposal                GatewayRebindProposalInput
	ProposalDigest          string
	ConfigureApprovalDigest string
	RebindApprovalDigest    string
}

// GatewayRebindCommitReview is an advisory snapshot for two distinct approval
// purposes. Its digest binds the full inspection but grants no effect authority
// and makes no claim about physical readiness.
type GatewayRebindCommitReview struct {
	Version                 int
	Inspection              GatewayRebindProposalInspection
	ProposalDigest          string
	ConfigureApprovalDigest string
	RebindApprovalDigest    string
}

// ValidateGatewayRebindCommitReview checks that displayed review data and all
// approval digests describe the same canonical advisory snapshot. It grants no
// admission or physical authority and performs no reads or writes.
func ValidateGatewayRebindCommitReview(review GatewayRebindCommitReview) error {
	expected, err := newGatewayRebindCommitReview(review.Inspection)
	if err != nil || review.Version != expected.Version ||
		review.ProposalDigest != expected.ProposalDigest ||
		review.ConfigureApprovalDigest != expected.ConfigureApprovalDigest ||
		review.RebindApprovalDigest != expected.RebindApprovalDigest {
		return &Error{Code: DiagnosticValidationFailed}
	}
	return nil
}

// InspectGatewayRebindCommitReview uses the existing stable, locked proposal
// observation. Commit must rebuild this review under its full writer lock chain.
func (m *Manager) InspectGatewayRebindCommitReview(ctx context.Context,
	repository GatewayRebindProposalRepository, input GatewayRebindProposalInput,
) (GatewayRebindCommitReview, error) {
	inspection, err := m.InspectGatewayRebindProposal(ctx, repository, input)
	if err != nil {
		return GatewayRebindCommitReview{}, err
	}
	review, err := newGatewayRebindCommitReview(inspection)
	if err != nil || ctx.Err() != nil {
		return GatewayRebindCommitReview{}, gatewayRebindProposalError(ctx)
	}
	return review, nil
}

// newGatewayRebindCommitReview accepts a server-built inspection, including one
// reconstructed from validated retained history. Consistency checks here do not
// replace the SQL, protected-state, or physical validation that produced it.
func newGatewayRebindCommitReview(inspection GatewayRebindProposalInspection) (GatewayRebindCommitReview, error) {
	invalid := func() (GatewayRebindCommitReview, error) {
		return GatewayRebindCommitReview{}, &Error{Code: DiagnosticValidationFailed}
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(inspection.Spec)
	if err != nil || specDigest != inspection.SpecDigest {
		return invalid()
	}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(inspection.Spec.SuccessorProfile)
	if err != nil || profileDigest != inspection.SuccessorProfileSpecDigest {
		return invalid()
	}
	source := inspection.Spec.Predecessor
	if inspection.ProtectedGeneration != inspection.Spec.SuccessorProtectedGeneration ||
		inspection.PredecessorCheckpointDigest != source.PredecessorCheckpointDigest ||
		inspection.SourceStateVersion != source.SourceStateVersion ||
		inspection.SourceStateRevision != source.SourceStateRevision ||
		inspection.SourceStateDigest != source.SourceStateDigest ||
		int64(len(inspection.Roster)) != inspection.Spec.RosterCount ||
		int64(len(inspection.RuntimeHeads)) != inspection.Spec.RuntimeHeadsCount {
		return invalid()
	}
	for index, entry := range inspection.Roster {
		entryDigest, entryErr := appaccess.GatewayRebindRosterEntryV2Digest(entry)
		if entryErr != nil || entry.OperationID != inspection.Spec.OperationID ||
			entry.Ordinal != int64(index+1) || entry.EntryDigest != entryDigest {
			return invalid()
		}
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(inspection.Roster)
	if err != nil || rosterDigest != inspection.Spec.RosterDigest {
		return invalid()
	}
	headsDigest, err := appaccess.GatewayRebindRuntimeHeadsV2Digest(inspection.Spec.OperationID, inspection.RuntimeHeads)
	if err != nil || headsDigest != inspection.Spec.RuntimeHeadsDigest {
		return invalid()
	}

	// Empty censuses and equivalent UTC instants have one digest representation,
	// matching the existing canonical census digests. Preserve the exact returned
	// inspection for strict comparison with a fresh locked observation.
	canonical := cloneGatewayRebindCommitInspection(inspection)
	if canonical.Roster == nil {
		canonical.Roster = []appaccess.GatewayRebindRosterEntryV2{}
	}
	if canonical.RuntimeHeads == nil {
		canonical.RuntimeHeads = []appaccess.GatewayRebindRuntimeHead{}
	}
	for index := range canonical.RuntimeHeads {
		canonical.RuntimeHeads[index].UpdatedAt = canonical.RuntimeHeads[index].UpdatedAt.UTC()
	}
	digest, err := canonicalDigest(struct {
		Version    int                             `json:"version"`
		Purpose    string                          `json:"purpose"`
		Inspection GatewayRebindProposalInspection `json:"inspection"`
	}{gatewayRebindCommitReviewVersion, gatewayRebindCommitReviewPurpose, canonical})
	if err != nil {
		return invalid()
	}
	return GatewayRebindCommitReview{
		Version: gatewayRebindCommitReviewVersion, Inspection: cloneGatewayRebindCommitInspection(inspection),
		ProposalDigest: digest, ConfigureApprovalDigest: profileDigest, RebindApprovalDigest: specDigest,
	}, nil
}

func cloneGatewayRebindCommitInspection(value GatewayRebindProposalInspection) GatewayRebindProposalInspection {
	if value.Roster != nil {
		value.Roster = append([]appaccess.GatewayRebindRosterEntryV2{}, value.Roster...)
		for index := range value.Roster {
			if value.Roster[index].PredecessorTransferDigest != nil {
				digest := *value.Roster[index].PredecessorTransferDigest
				value.Roster[index].PredecessorTransferDigest = &digest
			}
		}
	}
	if value.RuntimeHeads != nil {
		value.RuntimeHeads = append([]appaccess.GatewayRebindRuntimeHead{}, value.RuntimeHeads...)
	}
	return value
}
