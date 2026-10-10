package generatedingress

import (
	"errors"
	"reflect"
	"strings"
	"time"
)

const (
	gatewayRebindFinalHandoverProgressVersion = 1
	gatewayRebindFinalHandoverCutoverContext  = "hostd/generated-ingress/rebind/final-handover-cutover/v1"
	gatewayRebindFinalHandoverServingContext  = "hostd/generated-ingress/rebind/final-handover-serving/v1"
	gatewayRebindFinalHandoverRollbackContext = "hostd/generated-ingress/rebind/final-handover-rollback/v1"
	gatewayRebindFinalHandoverOutcomeContext  = "hostd/generated-ingress/rebind/final-handover-outcome/v1"
)

type gatewayRebindFinalHandoverOutcomeKind string

const (
	gatewayRebindFinalHandoverOutcomeCommit gatewayRebindFinalHandoverOutcomeKind = "commit"
	gatewayRebindFinalHandoverOutcomeAbort  gatewayRebindFinalHandoverOutcomeKind = "abort"
)

// gatewayRebindFinalHandoverProgress is monotonic protected handover state.
// Rollback retains every earlier binding and proof; it never rewrites history
// to resemble a pre-handover phase.
type gatewayRebindFinalHandoverProgress struct {
	Plan     *gatewayRebindFinalHandoverPlan           `json:"plan"`
	Final    *gatewayRebindFinalContainerBinding       `json:"final,omitempty"`
	Cutover  *gatewayRebindFinalHandoverCutoverIntent  `json:"cutover,omitempty"`
	Serving  *gatewayRebindFinalHandoverServingProof   `json:"serving,omitempty"`
	Rollback *gatewayRebindFinalHandoverRollbackIntent `json:"rollback,omitempty"`
	Outcome  *gatewayRebindFinalHandoverTerminalProof  `json:"outcome,omitempty"`
}

type gatewayRebindFinalHandoverCutoverIntent struct {
	Version                      int    `json:"version"`
	Context                      string `json:"context"`
	PlanDigest                   string `json:"planDigest"`
	FinalBindingDigest           string `json:"finalBindingDigest"`
	PredecessorJournalDigest     string `json:"predecessorJournalDigest"`
	PreparationObservationDigest string `json:"preparationObservationDigest"`
	PriorProgressDigest          string `json:"priorProgressDigest"`
	Digest                       string `json:"digest"`
}

type gatewayRebindFinalHandoverServingProof struct {
	Version             int                                   `json:"version"`
	Context             string                                `json:"context"`
	PlanDigest          string                                `json:"planDigest"`
	FinalBindingDigest  string                                `json:"finalBindingDigest"`
	CutoverIntentDigest string                                `json:"cutoverIntentDigest"`
	PriorProgressDigest string                                `json:"priorProgressDigest"`
	Observation         gatewayRebindFinalHandoverObservation `json:"observation"`
	Digest              string                                `json:"digest"`
}

type gatewayRebindFinalHandoverRollbackIntent struct {
	Version             int                        `json:"version"`
	Context             string                     `json:"context"`
	FromSequence        uint64                     `json:"fromSequence"`
	FromPhase           gatewayRebindProgressPhase `json:"fromPhase"`
	PlanDigest          string                     `json:"planDigest"`
	FinalBindingDigest  string                     `json:"finalBindingDigest,omitempty"`
	CutoverBegun        bool                       `json:"cutoverBegun"`
	PriorProgressDigest string                     `json:"priorProgressDigest"`
	Digest              string                     `json:"digest"`
}

type gatewayRebindFinalHandoverTerminalProof struct {
	Version             int                                   `json:"version"`
	Context             string                                `json:"context"`
	Kind                gatewayRebindFinalHandoverOutcomeKind `json:"kind"`
	PriorProgressDigest string                                `json:"priorProgressDigest"`
	Observation         gatewayRebindFinalHandoverObservation `json:"observation"`
	Digest              string                                `json:"digest"`
}

func gatewayRebindFinalHandoverCutoverDigest(value gatewayRebindFinalHandoverCutoverIntent) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalHandoverServingDigest(value gatewayRebindFinalHandoverServingProof) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalHandoverRollbackDigest(value gatewayRebindFinalHandoverRollbackIntent) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalHandoverOutcomeDigest(value gatewayRebindFinalHandoverTerminalProof) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalContainerBindingDigest(value gatewayRebindFinalContainerBinding) (string, error) {
	return canonicalDigest(value)
}

func validGatewayRebindFinalHandoverPlanValue(value gatewayRebindFinalHandoverPlan) bool {
	if value.Version != gatewayRebindFinalHandoverProgressVersion || value.Context != gatewayRebindFinalHandoverPlanContext ||
		!validSHA256(value.PredecessorObservationDigest) || !validSHA256(value.ProtectedIntentDigest) ||
		!validSHA256(value.SequenceTwelveDigest) ||
		!validSHA256(value.PredecessorDigest) || !validSHA256(value.RoutePlanDigest) ||
		!validSHA256(value.FinalConfigDigest) || !validSHA256(value.FinalOwnershipDigest) ||
		!validSHA256(value.FinalConfigurationDigest) || value.LocalHostPort == 0 ||
		!validSHA256(value.Digest) {
		return false
	}
	if !validGatewayRebindFinalHandoverApplicationNetworkValues(value.ApplicationNetworks) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverPlanDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindFinalHandoverApplicationNetworkValues(values []gatewayRebindHandoverApplicationNetwork) bool {
	seenIDs := make(map[string]struct{}, len(values))
	for index, network := range values {
		if network.Name == "" || strings.TrimSpace(network.Name) != network.Name ||
			!validContainerID(network.ID) || normalizeID(network.ID) != network.ID ||
			(index > 0 && values[index-1].Name >= network.Name) {
			return false
		}
		if _, duplicate := seenIDs[network.ID]; duplicate {
			return false
		}
		seenIDs[network.ID] = struct{}{}
	}
	return true
}

func validGatewayRebindFinalContainerBindingValue(value gatewayRebindFinalContainerBinding) bool {
	return validContainerID(value.ID) && normalizeID(value.ID) == value.ID &&
		validSHA256(value.OwnershipDigest) && validSHA256(value.ConfigurationDigest)
}

func validGatewayRebindFinalHandoverProgressValue(value gatewayRebindFinalHandoverProgress) bool {
	if value.Plan == nil || !validGatewayRebindFinalHandoverPlanValue(*value.Plan) ||
		(value.Final == nil && (value.Cutover != nil || value.Serving != nil)) ||
		(value.Cutover == nil && value.Serving != nil) ||
		(value.Rollback != nil && value.Outcome != nil && value.Outcome.Kind != gatewayRebindFinalHandoverOutcomeAbort) {
		return false
	}
	if value.Final != nil && !validGatewayRebindFinalContainerBindingValue(*value.Final) {
		return false
	}
	if value.Cutover != nil && !validGatewayRebindFinalHandoverCutoverValue(*value.Cutover) {
		return false
	}
	if value.Serving != nil && !validGatewayRebindFinalHandoverServingValue(*value.Serving) {
		return false
	}
	if value.Rollback != nil && !validGatewayRebindFinalHandoverRollbackValue(*value.Rollback) {
		return false
	}
	return value.Outcome == nil || validGatewayRebindFinalHandoverOutcomeValue(*value.Outcome)
}

func validGatewayRebindFinalHandoverCutoverValue(value gatewayRebindFinalHandoverCutoverIntent) bool {
	if value.Version != gatewayRebindFinalHandoverProgressVersion ||
		value.Context != gatewayRebindFinalHandoverCutoverContext ||
		!validSHA256(value.PlanDigest) || !validSHA256(value.FinalBindingDigest) ||
		!validSHA256(value.PredecessorJournalDigest) ||
		!validSHA256(value.PreparationObservationDigest) ||
		!validSHA256(value.PriorProgressDigest) || !validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverCutoverDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindFinalHandoverServingValue(value gatewayRebindFinalHandoverServingProof) bool {
	if value.Version != gatewayRebindFinalHandoverProgressVersion ||
		value.Context != gatewayRebindFinalHandoverServingContext ||
		!validSHA256(value.PlanDigest) || !validSHA256(value.FinalBindingDigest) ||
		!validSHA256(value.CutoverIntentDigest) || !validSHA256(value.PriorProgressDigest) ||
		!validGatewayRebindFinalHandoverObservationValue(value.Observation) || !validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverServingDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindFinalHandoverRollbackValue(value gatewayRebindFinalHandoverRollbackIntent) bool {
	if value.Version != gatewayRebindFinalHandoverProgressVersion ||
		value.Context != gatewayRebindFinalHandoverRollbackContext || value.FromSequence < 13 || value.FromSequence > 16 ||
		(value.FromPhase != gatewayRebindProgressFinalHandoverIntent &&
			value.FromPhase != gatewayRebindProgressFinalContainerBound &&
			value.FromPhase != gatewayRebindProgressCutoverIntent &&
			value.FromPhase != gatewayRebindProgressSuccessorServing) ||
		!validSHA256(value.PlanDigest) || !validSHA256(value.PriorProgressDigest) || !validSHA256(value.Digest) ||
		(value.FinalBindingDigest != "" && !validSHA256(value.FinalBindingDigest)) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverRollbackDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindFinalHandoverOutcomeValue(value gatewayRebindFinalHandoverTerminalProof) bool {
	if value.Version != gatewayRebindFinalHandoverProgressVersion || value.Context != gatewayRebindFinalHandoverOutcomeContext ||
		(value.Kind != gatewayRebindFinalHandoverOutcomeCommit && value.Kind != gatewayRebindFinalHandoverOutcomeAbort) ||
		!validSHA256(value.PriorProgressDigest) || !validGatewayRebindFinalHandoverObservationValue(value.Observation) ||
		!validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindFinalHandoverOutcomeDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayRebindFinalHandoverProgressMatchesIntent(value gatewayRebindProgressRecord,
	intent gatewayRebindProtectedIntent, previous []gatewayRebindProgressSelection,
) bool {
	if value.Sequence < 13 || value.Sequence > gatewayRebindProgressMaximumSequence ||
		len(previous) != int(value.Sequence-1) || value.Stage == nil || value.Handover == nil ||
		len(previous) < 12 || previous[11].Record.Stage == nil ||
		!reflect.DeepEqual(*value.Stage, *previous[11].Record.Stage) ||
		value.PreviousDigest != previous[len(previous)-1].Record.Digest {
		return false
	}
	prior := previous[len(previous)-1].Record
	priorAt, priorErr := parseGatewayRebindProgressTime(prior.OccurredAt)
	currentAt, currentErr := parseGatewayRebindProgressTime(value.OccurredAt)
	if priorErr != nil || currentErr != nil || !currentAt.After(priorAt) {
		return false
	}
	return value.Sequence == prior.Sequence+1 &&
		gatewayRebindFinalHandoverTransitionMatches(intent, prior, value.Phase, *value.Handover)
}

func gatewayRebindFinalHandoverTransitionMatches(intent gatewayRebindProtectedIntent,
	prior gatewayRebindProgressRecord, phase gatewayRebindProgressPhase,
	handover gatewayRebindFinalHandoverProgress,
) bool {
	switch phase {
	case gatewayRebindProgressFinalHandoverIntent:
		return prior.Sequence == 12 && prior.Phase == gatewayRebindProgressFinalConfigCopied &&
			gatewayRebindFinalHandoverInitialProgressMatches(intent, prior, handover)
	case gatewayRebindProgressFinalContainerBound:
		return prior.Phase == gatewayRebindProgressFinalHandoverIntent &&
			gatewayRebindFinalHandoverBoundProgressMatches(intent, prior, handover)
	case gatewayRebindProgressCutoverIntent:
		return prior.Phase == gatewayRebindProgressFinalContainerBound &&
			gatewayRebindFinalHandoverCutoverProgressMatches(intent, prior, handover)
	case gatewayRebindProgressSuccessorServing:
		return prior.Phase == gatewayRebindProgressCutoverIntent &&
			gatewayRebindFinalHandoverServingProgressMatches(intent, prior, handover)
	case gatewayRebindProgressHandoverCommitted:
		return prior.Phase == gatewayRebindProgressSuccessorServing &&
			gatewayRebindFinalHandoverTerminalProgressMatches(intent, prior, handover,
				gatewayRebindFinalHandoverOutcomeCommit)
	case gatewayRebindProgressRollbackIntent:
		return gatewayRebindFinalHandoverRollbackSourcePhase(prior.Phase) &&
			gatewayRebindFinalHandoverRollbackProgressMatches(intent, prior, handover)
	case gatewayRebindProgressHandoverRolledBack:
		return prior.Phase == gatewayRebindProgressRollbackIntent &&
			gatewayRebindFinalHandoverTerminalProgressMatches(intent, prior, handover,
				gatewayRebindFinalHandoverOutcomeAbort)
	default:
		return false
	}
}

func gatewayRebindFinalHandoverRollbackSourcePhase(phase gatewayRebindProgressPhase) bool {
	return phase == gatewayRebindProgressFinalHandoverIntent || phase == gatewayRebindProgressFinalContainerBound ||
		phase == gatewayRebindProgressCutoverIntent || phase == gatewayRebindProgressSuccessorServing
}

func gatewayRebindFinalHandoverInitialProgressMatches(intent gatewayRebindProtectedIntent,
	sequenceTwelve gatewayRebindProgressRecord, value gatewayRebindFinalHandoverProgress,
) bool {
	if value.Plan == nil || !validGatewayRebindFinalHandoverPlanValue(*value.Plan) ||
		value.Plan.ProtectedIntentDigest != intent.Digest || value.Plan.SequenceTwelveDigest != sequenceTwelve.Digest ||
		sequenceTwelve.Stage == nil || sequenceTwelve.Stage.FinalConfigIntent == nil ||
		value.Plan.RoutePlanDigest != sequenceTwelve.Stage.FinalConfigIntent.RoutePlan.Digest ||
		value.Plan.FinalConfigDigest != sequenceTwelve.Stage.FinalConfigIntent.ContentDigest {
		return false
	}
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	return err == nil && value.Plan.PredecessorDigest == predecessorDigest &&
		value.Final == nil && value.Cutover == nil && value.Serving == nil && value.Rollback == nil && value.Outcome == nil
}

func gatewayRebindFinalHandoverBoundProgressMatches(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalHandoverProgress,
) bool {
	return previous.Handover != nil && value.Plan != nil && previous.Handover.Plan != nil &&
		reflect.DeepEqual(value.Plan, previous.Handover.Plan) && value.Final != nil &&
		validGatewayRebindFinalContainerBindingValue(*value.Final) &&
		value.Final.OwnershipDigest == value.Plan.FinalOwnershipDigest &&
		value.Final.ConfigurationDigest == value.Plan.FinalConfigurationDigest && previous.Stage != nil &&
		value.Final.ID != previous.Stage.StageContainer.ID &&
		value.Final.ID != intent.Intent.Predecessor.Resources.FinalContainerID &&
		value.Cutover == nil && value.Serving == nil && value.Rollback == nil && value.Outcome == nil
}

func gatewayRebindFinalHandoverCutoverProgressMatches(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalHandoverProgress,
) bool {
	return previous.Handover != nil && gatewayRebindFinalHandoverPrefixEqual(value, *previous.Handover) &&
		value.Cutover != nil && validGatewayRebindFinalHandoverCutover(previous, intent, *value.Cutover) &&
		value.Serving == nil && value.Rollback == nil && value.Outcome == nil
}

func gatewayRebindFinalHandoverServingProgressMatches(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalHandoverProgress,
) bool {
	return previous.Handover != nil && gatewayRebindFinalHandoverPrefixEqual(value, *previous.Handover) &&
		value.Cutover != nil && previous.Handover.Cutover != nil && reflect.DeepEqual(value.Cutover, previous.Handover.Cutover) &&
		value.Serving != nil && validGatewayRebindFinalHandoverServing(previous, intent, *value.Serving) &&
		value.Rollback == nil && value.Outcome == nil
}

func gatewayRebindFinalHandoverRollbackProgressMatches(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalHandoverProgress,
) bool {
	return previous.Handover != nil && gatewayRebindFinalHandoverEffectHistoryEqual(value, *previous.Handover) &&
		value.Rollback != nil && validGatewayRebindFinalHandoverRollback(previous, intent, *value.Rollback) &&
		value.Outcome == nil
}

func gatewayRebindFinalHandoverTerminalProgressMatches(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalHandoverProgress,
	kind gatewayRebindFinalHandoverOutcomeKind,
) bool {
	if previous.Handover == nil || !gatewayRebindFinalHandoverEffectHistoryEqual(value, *previous.Handover) ||
		value.Outcome == nil || !validGatewayRebindFinalHandoverOutcome(previous, intent, kind, *value.Outcome) {
		return false
	}
	if kind == gatewayRebindFinalHandoverOutcomeCommit {
		return value.Rollback == nil
	}
	return value.Rollback != nil && previous.Handover.Rollback != nil &&
		reflect.DeepEqual(value.Rollback, previous.Handover.Rollback)
}

func gatewayRebindFinalHandoverPrefixEqual(current, previous gatewayRebindFinalHandoverProgress) bool {
	return current.Plan != nil && previous.Plan != nil && reflect.DeepEqual(current.Plan, previous.Plan) &&
		current.Final != nil && previous.Final != nil && reflect.DeepEqual(current.Final, previous.Final)
}

func gatewayRebindFinalHandoverEffectHistoryEqual(current, previous gatewayRebindFinalHandoverProgress) bool {
	return reflect.DeepEqual(current.Plan, previous.Plan) && reflect.DeepEqual(current.Final, previous.Final) &&
		reflect.DeepEqual(current.Cutover, previous.Cutover) && reflect.DeepEqual(current.Serving, previous.Serving)
}

func validGatewayRebindFinalHandoverCutover(previous gatewayRebindProgressRecord,
	intent gatewayRebindProtectedIntent, value gatewayRebindFinalHandoverCutoverIntent,
) bool {
	if !validGatewayRebindFinalHandoverCutoverValue(value) || previous.Handover == nil ||
		previous.Handover.Plan == nil || previous.Handover.Final == nil ||
		value.PriorProgressDigest != previous.Digest || value.PlanDigest != previous.Handover.Plan.Digest ||
		value.PredecessorJournalDigest != intent.Intent.Predecessor.JournalDigest {
		return false
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.Handover.Final)
	return err == nil && value.FinalBindingDigest == finalDigest
}

func validGatewayRebindFinalHandoverServing(previous gatewayRebindProgressRecord,
	_ gatewayRebindProtectedIntent, value gatewayRebindFinalHandoverServingProof,
) bool {
	if !validGatewayRebindFinalHandoverServingValue(value) || previous.Handover == nil ||
		previous.Handover.Plan == nil || previous.Handover.Final == nil || previous.Handover.Cutover == nil ||
		value.PriorProgressDigest != previous.Digest || value.PlanDigest != previous.Handover.Plan.Digest ||
		value.CutoverIntentDigest != previous.Handover.Cutover.Digest {
		return false
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.Handover.Final)
	return err == nil && value.FinalBindingDigest == finalDigest
}

func validGatewayRebindFinalHandoverRollback(previous gatewayRebindProgressRecord,
	_ gatewayRebindProtectedIntent, value gatewayRebindFinalHandoverRollbackIntent,
) bool {
	if !validGatewayRebindFinalHandoverRollbackValue(value) || previous.Handover == nil || previous.Handover.Plan == nil ||
		value.FromSequence != previous.Sequence || value.FromPhase != previous.Phase ||
		value.PriorProgressDigest != previous.Digest || value.PlanDigest != previous.Handover.Plan.Digest ||
		value.CutoverBegun != (previous.Handover.Cutover != nil) {
		return false
	}
	if previous.Handover.Final == nil {
		return value.FinalBindingDigest == ""
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.Handover.Final)
	return err == nil && value.FinalBindingDigest == finalDigest
}

func validGatewayRebindFinalHandoverOutcome(previous gatewayRebindProgressRecord,
	_ gatewayRebindProtectedIntent, kind gatewayRebindFinalHandoverOutcomeKind,
	value gatewayRebindFinalHandoverTerminalProof,
) bool {
	return validGatewayRebindFinalHandoverOutcomeValue(value) && value.Kind == kind &&
		value.PriorProgressDigest == previous.Digest
}

func newGatewayRebindFinalHandoverProgressRecord(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, phase gatewayRebindProgressPhase,
	handover gatewayRebindFinalHandoverProgress, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Generation != intent.Generation || previous.OperationID != intent.OperationID ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		!validGatewayRebindFinalHandoverProgressValue(handover) || !validGatewayRebindProgressTime(occurredAt) ||
		!gatewayRebindFinalHandoverTransitionMatches(intent, previous, phase, handover) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final handover progress input")
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) || previous.Sequence >= gatewayRebindProgressMaximumSequence {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final handover progress input")
	}
	stage := *previous.Stage
	value := gatewayRebindProgressRecord{
		Version: gatewayRebindProgressVersion, Generation: intent.Generation, OperationID: intent.OperationID,
		Sequence: previous.Sequence + 1, Phase: phase, OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano),
		ProtectedIntentDigest: intent.Digest, PreviousDigest: previous.Digest, Stage: &stage, Handover: &handover,
	}
	_, value.Purpose = gatewayRebindProgressName(value.Generation, value.OperationID, value.Sequence)
	value.Digest, err = gatewayRebindProgressDigest(value)
	if err != nil || !validGatewayRebindProgressRecord(value) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final handover progress input")
	}
	return value, nil
}

func newGatewayRebindFinalHandoverIntentProgress(intent gatewayRebindProtectedIntent,
	sequenceTwelve gatewayRebindProgressRecord, plan gatewayRebindFinalHandoverPlan, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	handover := gatewayRebindFinalHandoverProgress{Plan: &plan}
	return newGatewayRebindFinalHandoverProgressRecord(intent, sequenceTwelve,
		gatewayRebindProgressFinalHandoverIntent, handover, occurredAt)
}

func newGatewayRebindFinalContainerBoundProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, binding gatewayRebindFinalContainerBinding, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if previous.Handover == nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final container binding input")
	}
	handover := *previous.Handover
	handover.Final = &binding
	return newGatewayRebindFinalHandoverProgressRecord(intent, previous,
		gatewayRebindProgressFinalContainerBound, handover, occurredAt)
}

func newGatewayRebindFinalHandoverCutoverProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, observation gatewayRebindFinalHandoverObservation, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if previous.Handover == nil || previous.Handover.Plan == nil || previous.Handover.Final == nil ||
		!gatewayRebindFinalHandoverReadyForCutover(observation, *previous.Handover.Plan, *previous.Handover.Final) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind final handover cutover input")
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.Handover.Final)
	if err != nil {
		return gatewayRebindProgressRecord{}, err
	}
	cutover := gatewayRebindFinalHandoverCutoverIntent{
		Version: gatewayRebindFinalHandoverProgressVersion, Context: gatewayRebindFinalHandoverCutoverContext,
		PlanDigest: previous.Handover.Plan.Digest, FinalBindingDigest: finalDigest,
		PredecessorJournalDigest:     intent.Intent.Predecessor.JournalDigest,
		PreparationObservationDigest: observation.Digest, PriorProgressDigest: previous.Digest,
	}
	cutover.Digest, err = gatewayRebindFinalHandoverCutoverDigest(cutover)
	if err != nil {
		return gatewayRebindProgressRecord{}, err
	}
	handover := *previous.Handover
	handover.Cutover = &cutover
	return newGatewayRebindFinalHandoverProgressRecord(intent, previous,
		gatewayRebindProgressCutoverIntent, handover, occurredAt)
}

func newGatewayRebindFinalHandoverServingProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, observation gatewayRebindFinalHandoverObservation, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if previous.Handover == nil || previous.Handover.Plan == nil || previous.Handover.Final == nil ||
		previous.Handover.Cutover == nil ||
		!gatewayRebindFinalHandoverSuccessorIsServing(observation, *previous.Handover.Plan, *previous.Handover.Final) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind successor serving input")
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.Handover.Final)
	if err != nil {
		return gatewayRebindProgressRecord{}, err
	}
	serving := gatewayRebindFinalHandoverServingProof{
		Version: gatewayRebindFinalHandoverProgressVersion, Context: gatewayRebindFinalHandoverServingContext,
		PlanDigest: previous.Handover.Plan.Digest, FinalBindingDigest: finalDigest,
		CutoverIntentDigest: previous.Handover.Cutover.Digest, PriorProgressDigest: previous.Digest,
		Observation: observation,
	}
	serving.Digest, err = gatewayRebindFinalHandoverServingDigest(serving)
	if err != nil {
		return gatewayRebindProgressRecord{}, err
	}
	handover := *previous.Handover
	handover.Serving = &serving
	return newGatewayRebindFinalHandoverProgressRecord(intent, previous,
		gatewayRebindProgressSuccessorServing, handover, occurredAt)
}

func newGatewayRebindFinalHandoverRollbackProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if previous.Handover == nil || previous.Handover.Plan == nil ||
		!gatewayRebindFinalHandoverRollbackSourcePhase(previous.Phase) {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind rollback input")
	}
	rollback := gatewayRebindFinalHandoverRollbackIntent{
		Version: gatewayRebindFinalHandoverProgressVersion, Context: gatewayRebindFinalHandoverRollbackContext,
		FromSequence: previous.Sequence, FromPhase: previous.Phase, PlanDigest: previous.Handover.Plan.Digest,
		CutoverBegun: previous.Handover.Cutover != nil, PriorProgressDigest: previous.Digest,
	}
	var err error
	if previous.Handover.Final != nil {
		rollback.FinalBindingDigest, err = gatewayRebindFinalContainerBindingDigest(*previous.Handover.Final)
		if err != nil {
			return gatewayRebindProgressRecord{}, err
		}
	}
	rollback.Digest, err = gatewayRebindFinalHandoverRollbackDigest(rollback)
	if err != nil {
		return gatewayRebindProgressRecord{}, err
	}
	handover := *previous.Handover
	handover.Rollback = &rollback
	return newGatewayRebindFinalHandoverProgressRecord(intent, previous,
		gatewayRebindProgressRollbackIntent, handover, occurredAt)
}

func newGatewayRebindFinalHandoverTerminalProgress(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, kind gatewayRebindFinalHandoverOutcomeKind,
	observation gatewayRebindFinalHandoverObservation, predecessorRoutesDigest string, occurredAt time.Time,
) (gatewayRebindProgressRecord, error) {
	if previous.Handover == nil || previous.Handover.Plan == nil {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind terminal handover input")
	}
	phase := gatewayRebindProgressHandoverCommitted
	if kind == gatewayRebindFinalHandoverOutcomeCommit {
		if previous.Handover.Final == nil || previous.Handover.Serving == nil ||
			!gatewayRebindFinalHandoverSuccessorIsServing(observation, *previous.Handover.Plan, *previous.Handover.Final) {
			return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind terminal commit input")
		}
	} else if kind == gatewayRebindFinalHandoverOutcomeAbort {
		phase = gatewayRebindProgressHandoverRolledBack
		if previous.Handover.Rollback == nil ||
			!gatewayRebindFinalHandoverSuccessorIsAbsent(observation, *previous.Handover.Plan,
				previous.Handover.Rollback.CutoverBegun, predecessorRoutesDigest) {
			return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind terminal abort input")
		}
	} else {
		return gatewayRebindProgressRecord{}, errors.New("invalid generated ingress rebind terminal outcome")
	}
	outcome := gatewayRebindFinalHandoverTerminalProof{
		Version: gatewayRebindFinalHandoverProgressVersion, Context: gatewayRebindFinalHandoverOutcomeContext,
		Kind: kind, PriorProgressDigest: previous.Digest, Observation: observation,
	}
	var err error
	outcome.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(outcome)
	if err != nil {
		return gatewayRebindProgressRecord{}, err
	}
	handover := *previous.Handover
	handover.Outcome = &outcome
	return newGatewayRebindFinalHandoverProgressRecord(intent, previous, phase, handover, occurredAt)
}

func gatewayRebindFinalHandoverPredecessorIsInitial(observation gatewayRebindFinalHandoverObservation,
	plan gatewayRebindFinalHandoverPlan,
) bool {
	return observation.PredecessorObservationDigest == plan.PredecessorObservationDigest &&
		observation.PredecessorRunning == plan.PredecessorInitiallyRunning
}

func gatewayRebindFinalHandoverReadyForCutover(observation gatewayRebindFinalHandoverObservation,
	plan gatewayRebindFinalHandoverPlan, final gatewayRebindFinalContainerBinding,
) bool {
	return validGatewayRebindFinalHandoverObservationValue(observation) &&
		gatewayRebindFinalHandoverPredecessorIsInitial(observation, plan) &&
		observation.Stage == gatewayRebindHandoverContainerAbsent &&
		observation.Final == gatewayRebindHandoverContainerStopped && observation.FinalID == final.ID &&
		observation.ConfigVolumePresent && observation.DataVolumePresent && observation.IngressNetworkPresent &&
		observation.ConfigDigest == plan.FinalConfigDigest &&
		reflect.DeepEqual(observation.ApplicationNetworks, plan.ApplicationNetworks) &&
		observation.RoutesDigest == ""
}

func gatewayRebindFinalHandoverSuccessorIsServing(observation gatewayRebindFinalHandoverObservation,
	plan gatewayRebindFinalHandoverPlan, final gatewayRebindFinalContainerBinding,
) bool {
	return validGatewayRebindFinalHandoverObservationValue(observation) &&
		observation.Stage == gatewayRebindHandoverContainerAbsent &&
		observation.Final == gatewayRebindHandoverContainerRunning && observation.FinalID == final.ID &&
		!observation.PredecessorRunning && observation.PredecessorAddress != gatewayRebindPredecessorAddressAmbiguous &&
		observation.ConfigVolumePresent && observation.DataVolumePresent && observation.IngressNetworkPresent &&
		observation.ConfigDigest == plan.FinalConfigDigest && observation.RoutesDigest == plan.RoutePlanDigest &&
		validSHA256(observation.PredecessorStopDigest) &&
		reflect.DeepEqual(observation.ApplicationNetworks, plan.ApplicationNetworks)
}

func gatewayRebindFinalHandoverSuccessorIsAbsent(observation gatewayRebindFinalHandoverObservation,
	plan gatewayRebindFinalHandoverPlan, cutoverBegun bool, predecessorRoutesDigest string,
) bool {
	if !validGatewayRebindFinalHandoverObservationValue(observation) ||
		observation.Stage != gatewayRebindHandoverContainerAbsent ||
		observation.Final != gatewayRebindHandoverContainerAbsent || observation.FinalID != "" ||
		observation.ConfigVolumePresent || observation.DataVolumePresent || observation.IngressNetworkPresent ||
		observation.ConfigDigest != "" || observation.RoutesDigest != "" ||
		!reflect.DeepEqual(observation.ApplicationNetworks, plan.ApplicationNetworks) {
		return false
	}
	if !cutoverBegun {
		return observation.PredecessorObservationDigest == plan.PredecessorObservationDigest &&
			observation.PredecessorRunning == plan.PredecessorInitiallyRunning
	}
	return observation.PredecessorRunning && observation.PredecessorAddress == gatewayRebindPredecessorAddressPresent &&
		validSHA256(predecessorRoutesDigest) && observation.PredecessorRoutesDigest == predecessorRoutesDigest &&
		observation.PredecessorStopDigest == ""
}

func gatewayRebindFinalHandoverPredecessorRoutesDigest(value gatewayUpgradeGenerationSelection) (string, error) {
	if value.Store == nil || !value.Existing || value.PartialState || value.Retired || value.Aborted ||
		value.Journal.Phase != gatewayPhaseCommitted || !validGatewayV2RouteState(value.State) {
		return "", errors.New("invalid generated ingress rebind predecessor route state")
	}
	return canonicalDigest(value.State.Apps)
}

func validGatewayRebindFinalHandoverProgressContext(value gatewayRebindFinalHandoverContext,
	record gatewayRebindProgressRecord,
) bool {
	if record.Handover == nil || record.Handover.Plan == nil ||
		!validGatewayRebindFinalHandoverPlan(value, *record.Handover.Plan) ||
		(record.Handover.Final != nil &&
			!validGatewayRebindFinalContainerBinding(value, *record.Handover.Final)) {
		return false
	}
	handover := record.Handover
	switch record.Phase {
	case gatewayRebindProgressFinalHandoverIntent, gatewayRebindProgressFinalContainerBound,
		gatewayRebindProgressCutoverIntent, gatewayRebindProgressRollbackIntent:
		return true
	case gatewayRebindProgressSuccessorServing:
		return handover.Final != nil && handover.Serving != nil &&
			gatewayRebindFinalHandoverSuccessorIsServing(handover.Serving.Observation,
				*handover.Plan, *handover.Final)
	case gatewayRebindProgressHandoverCommitted:
		return handover.Final != nil && handover.Serving != nil && handover.Outcome != nil &&
			gatewayRebindFinalHandoverSuccessorIsServing(handover.Serving.Observation,
				*handover.Plan, *handover.Final) &&
			gatewayRebindFinalHandoverSuccessorIsServing(handover.Outcome.Observation,
				*handover.Plan, *handover.Final)
	case gatewayRebindProgressHandoverRolledBack:
		predecessorRoutesDigest, err := gatewayRebindFinalHandoverPredecessorRoutesDigest(value.Predecessor)
		return err == nil && handover.Rollback != nil && handover.Outcome != nil &&
			gatewayRebindFinalHandoverSuccessorIsAbsent(handover.Outcome.Observation,
				*handover.Plan, handover.Rollback.CutoverBegun, predecessorRoutesDigest)
	default:
		return false
	}
}
