package generatedingress

import (
	"context"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	GatewayRebindOperationInspectionVersion = 1
	GatewayRebindRecoveryReviewVersion      = 1
	GatewayRebindReconcileVersion           = appaccess.GatewayRebindReconcileVersionV1
)

// GatewayRebindRecoveryReadRepository is the complete SQL read boundary for
// dormant operation status and recovery review. It cannot claim an operation
// or apply a transition.
type GatewayRebindRecoveryReadRepository interface {
	GatewayRebindProposalRepository
	HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
	CheckGatewayRebindFence(context.Context) error
}

// GatewayRebindRecoveryRepository adds only the existing transition and
// request-scoped audit capabilities. Reconciliation never creates a claim.
type GatewayRebindRecoveryRepository interface {
	GatewayRebindRecoveryReadRepository
	ApplyGatewayRebindTransition(context.Context, appaccess.GatewayRebindTransitionProof) (
		appaccess.GatewayRebindTransitionCommand, error)
	AuthorizeAndAuditGatewayRebindReconcile(context.Context,
		appaccess.GatewayRebindReconcileAuthorizationInput) (
		appaccess.GatewayRebindReconcileAuthorization, error)
}

// GatewayRebindOperationAvailability separates a stable proof of absence from
// evidence that could not be read or validated. Only ValidatedAbsent supports
// an exact operation-not-found response.
type GatewayRebindOperationAvailability string

const (
	GatewayRebindOperationAvailable       GatewayRebindOperationAvailability = "available"
	GatewayRebindOperationValidatedAbsent GatewayRebindOperationAvailability = "validated_absent"
	GatewayRebindOperationUnavailable     GatewayRebindOperationAvailability = "unavailable"
)

// GatewayRebindOperationFenceStatus is scoped to the inspected operation. A
// historical operation may be released while a newer active operation holds
// the global fence.
type GatewayRebindOperationFenceStatus string

const (
	GatewayRebindOperationFenceHeld        GatewayRebindOperationFenceStatus = "held"
	GatewayRebindOperationFenceReleased    GatewayRebindOperationFenceStatus = "released"
	GatewayRebindOperationFenceUnavailable GatewayRebindOperationFenceStatus = "unavailable"
)

// GatewayRebindRecoveryAction is derived by the locked recovery projector.
// Clients cannot choose a forward or rollback direction.
type GatewayRebindRecoveryAction string

const GatewayRebindRecoveryActionReconcile GatewayRebindRecoveryAction = "reconcile"

// GatewayRebindOperationInspection is the curated status projection for one
// operation. It keeps durable operation history separate from selected-current
// authority and purpose-bound physical proof. Protected carries digests and
// identities only; it never exposes protected bytes, paths, or Docker state.
type GatewayRebindOperationInspection struct {
	Version                int
	OperationID            string
	Availability           GatewayRebindOperationAvailability
	Operation              GatewayRebindOperationStatus
	Active                 bool
	DatabaseCommittedEvent *appaccess.GatewayRebindEvent
	// Protected is nil when the protected scan is unavailable. A non-nil
	// ValidatedAbsent value means the complete scan proved exact absence.
	Protected *appaccess.GatewayRebindProtectedRecoveryEvidence
	// ProtectedPhase is the latest validated protected progress phase. It is
	// separate from SQL state and empty when progress is absent or unavailable.
	ProtectedPhase           string
	HistoricalTerminal       bool
	SelectedCurrentAuthority *appaccess.GatewayCurrentAuthorityRef
	TerminalProof            GatewayRebindProofStatus
	TerminalProofDigest      string
	CurrentProof             GatewayRebindProofStatus
	CurrentAttestationDigest string
	OperationFence           GatewayRebindOperationFenceStatus
	RecoveryPolicy           appaccess.GatewayRebindRecoveryPolicy
	AllowedActions           []GatewayRebindRecoveryAction
}

// GatewayRebindRecoveryReview combines one stable public status projection
// with the canonical actor-bound appaccess review. The review is advisory and
// grants no protected, Docker, transition, or other effect authority.
type GatewayRebindRecoveryReview struct {
	Version             int
	Status              GatewayRebindOperationInspection
	AuthorizationReview appaccess.GatewayRebindReconcileReview
}

// GatewayRebindReconcileRequest carries only the reviewed operation and its
// actor-bound consent digests. The authenticated actor and live session are
// supplied separately; the request contains no direction or phase override.
type GatewayRebindReconcileRequest struct {
	Version                int
	OperationID            string
	RecoveryDigest         string
	RecoveryApprovalDigest string
}

// GatewayRebindReconcileResponse reports freshly revalidated operation and
// selected-current status plus the non-authoritative audit receipt. The
// controller exposes only its curated receipt fields; the value grants no
// effect authority. This response has no Created field because recovery cannot
// create or replace a claim.
type GatewayRebindReconcileResponse struct {
	Version       int
	Status        GatewayRebindOperationInspection
	Authorization appaccess.GatewayRebindReconcileAuthorization
}
