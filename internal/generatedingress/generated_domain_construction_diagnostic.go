package generatedingress

import (
	"errors"
	"fmt"
)

// generatedDomainConstructionStage and generatedDomainConstructionReason are
// deliberately closed. They identify the private construction boundary that
// refused publication without rendering the observed Docker, database, path,
// or command error that led to the refusal.
type generatedDomainConstructionStage string

const (
	generatedDomainConstructionStageStartupObservation    generatedDomainConstructionStage = "startup_observation"
	generatedDomainConstructionStageInitialClassification generatedDomainConstructionStage = "initial_classification"
	generatedDomainConstructionStageStableConstruction    generatedDomainConstructionStage = "stable_construction"
	generatedDomainConstructionStageStableFinal           generatedDomainConstructionStage = "stable_final"
	generatedDomainConstructionStageEmptyConstruction     generatedDomainConstructionStage = "empty_construction"
	generatedDomainConstructionStageEmptyReclassification generatedDomainConstructionStage = "empty_reclassification"
	generatedDomainConstructionStageEmptyFinalLocked      generatedDomainConstructionStage = "empty_final_locked"
	generatedDomainConstructionStageEmptyFinalReleased    generatedDomainConstructionStage = "empty_final_released"
	generatedDomainConstructionStageColdConstruction      generatedDomainConstructionStage = "cold_construction"
	generatedDomainConstructionStageManifest              generatedDomainConstructionStage = "manifest"
	generatedDomainConstructionStageJoin                  generatedDomainConstructionStage = "join"
)

type generatedDomainConstructionReason string

const (
	generatedDomainConstructionReasonInput                   generatedDomainConstructionReason = "input"
	generatedDomainConstructionReasonPhysicalIdentity        generatedDomainConstructionReason = "physical_identity"
	generatedDomainConstructionReasonPathIdentity            generatedDomainConstructionReason = "path_identity"
	generatedDomainConstructionReasonDatabase                generatedDomainConstructionReason = "database"
	generatedDomainConstructionReasonProtectedState          generatedDomainConstructionReason = "protected_state"
	generatedDomainConstructionReasonGatewayExpectation      generatedDomainConstructionReason = "gateway_expectation"
	generatedDomainConstructionReasonDockerCensus            generatedDomainConstructionReason = "docker_census"
	generatedDomainConstructionReasonDockerDaemon            generatedDomainConstructionReason = "docker_daemon"
	generatedDomainConstructionReasonDockerAttestation       generatedDomainConstructionReason = "docker_attestation"
	generatedDomainConstructionReasonDockerImage             generatedDomainConstructionReason = "docker_image"
	generatedDomainConstructionReasonDockerContainer         generatedDomainConstructionReason = "docker_container"
	generatedDomainConstructionReasonDockerVolume            generatedDomainConstructionReason = "docker_volume"
	generatedDomainConstructionReasonDockerNetwork           generatedDomainConstructionReason = "docker_network"
	generatedDomainConstructionReasonDockerRestartConfig     generatedDomainConstructionReason = "docker_restart_config"
	generatedDomainConstructionReasonDockerLiveConfig        generatedDomainConstructionReason = "docker_live_config"
	generatedDomainConstructionReasonDockerStaticResponse    generatedDomainConstructionReason = "docker_static_response"
	generatedDomainConstructionReasonStability               generatedDomainConstructionReason = "stability"
	generatedDomainConstructionReasonClassification          generatedDomainConstructionReason = "classification"
	generatedDomainConstructionReasonDisposition             generatedDomainConstructionReason = "disposition"
	generatedDomainConstructionReasonRecovery                generatedDomainConstructionReason = "recovery"
	generatedDomainConstructionReasonStateStore              generatedDomainConstructionReason = "state_store"
	generatedDomainConstructionReasonManifest                generatedDomainConstructionReason = "manifest"
	generatedDomainConstructionReasonProcessLatch            generatedDomainConstructionReason = "process_latch"
	generatedDomainConstructionReasonFence                   generatedDomainConstructionReason = "fence"
	generatedDomainConstructionReasonAssembly                generatedDomainConstructionReason = "assembly"
	generatedDomainConstructionReasonColdAccounting          generatedDomainConstructionReason = "cold_accounting"
	generatedDomainConstructionReasonColdGroupReconstruction generatedDomainConstructionReason = "cold_group_reconstruction"
	generatedDomainConstructionReasonColdBuilderAttestation  generatedDomainConstructionReason = "cold_builder_attestation"
	generatedDomainConstructionReasonColdReservation         generatedDomainConstructionReason = "cold_reservation"
	generatedDomainConstructionReasonColdOwnership           generatedDomainConstructionReason = "cold_ownership"
	generatedDomainConstructionReasonColdHead                generatedDomainConstructionReason = "cold_head"
	generatedDomainConstructionReasonColdGroupAttestation    generatedDomainConstructionReason = "cold_group_attestation"
	generatedDomainConstructionReasonColdManifest            generatedDomainConstructionReason = "cold_manifest"
	generatedDomainConstructionReasonLock                    generatedDomainConstructionReason = "lock"
	generatedDomainConstructionReasonRelease                 generatedDomainConstructionReason = "release"
	generatedDomainConstructionReasonFinalValidation         generatedDomainConstructionReason = "final_validation"
)

type generatedDomainConstructionDiagnostic struct {
	stage  generatedDomainConstructionStage
	reason generatedDomainConstructionReason
	causes []error
}

func (e *generatedDomainConstructionDiagnostic) Error() string {
	return fmt.Sprintf("generated domain construction was refused: stage=%s reason=%s", e.stage, e.reason)
}

func (e *generatedDomainConstructionDiagnostic) Unwrap() []error {
	return append([]error(nil), e.causes...)
}

func newGeneratedDomainConstructionDiagnostic(stage generatedDomainConstructionStage,
	reason generatedDomainConstructionReason, causes ...error,
) error {
	if !validGeneratedDomainConstructionStage(stage) || !validGeneratedDomainConstructionReason(reason) {
		return errGeneratedDomainConstructionRefused
	}
	filtered := make([]error, 0, len(causes))
	for _, cause := range causes {
		if cause != nil {
			filtered = append(filtered, cause)
		}
	}
	return &generatedDomainConstructionDiagnostic{stage: stage, reason: reason, causes: filtered}
}

func validGeneratedDomainConstructionStage(stage generatedDomainConstructionStage) bool {
	switch stage {
	case generatedDomainConstructionStageStartupObservation,
		generatedDomainConstructionStageInitialClassification,
		generatedDomainConstructionStageStableConstruction,
		generatedDomainConstructionStageStableFinal,
		generatedDomainConstructionStageEmptyConstruction,
		generatedDomainConstructionStageEmptyReclassification,
		generatedDomainConstructionStageEmptyFinalLocked,
		generatedDomainConstructionStageEmptyFinalReleased,
		generatedDomainConstructionStageColdConstruction,
		generatedDomainConstructionStageManifest,
		generatedDomainConstructionStageJoin:
		return true
	default:
		return false
	}
}

// generatedDomainConstructionBoundaryDiagnostic decorates a construction
// boundary that already returned its source error. Existing closed diagnostics
// pass through unchanged, so the first precise stage remains authoritative and
// no previously collapsed source error becomes unwrap-visible.
func generatedDomainConstructionBoundaryDiagnostic(stage generatedDomainConstructionStage,
	reason generatedDomainConstructionReason, source error,
) error {
	if source == nil {
		return nil
	}
	if diagnostic, ok := source.(*generatedDomainConstructionDiagnostic); ok && diagnostic != nil &&
		validGeneratedDomainConstructionStage(diagnostic.stage) &&
		validGeneratedDomainConstructionReason(diagnostic.reason) {
		return source
	}
	var diagnostic *generatedDomainConstructionDiagnostic
	if errors.As(source, &diagnostic) && diagnostic != nil &&
		validGeneratedDomainConstructionStage(diagnostic.stage) &&
		validGeneratedDomainConstructionReason(diagnostic.reason) {
		stage, reason = diagnostic.stage, diagnostic.reason
	}
	return newGeneratedDomainConstructionDiagnostic(stage, reason, source)
}

func validGeneratedDomainConstructionReason(reason generatedDomainConstructionReason) bool {
	switch reason {
	case generatedDomainConstructionReasonInput,
		generatedDomainConstructionReasonPhysicalIdentity,
		generatedDomainConstructionReasonPathIdentity,
		generatedDomainConstructionReasonDatabase,
		generatedDomainConstructionReasonProtectedState,
		generatedDomainConstructionReasonGatewayExpectation,
		generatedDomainConstructionReasonDockerCensus,
		generatedDomainConstructionReasonDockerDaemon,
		generatedDomainConstructionReasonDockerAttestation,
		generatedDomainConstructionReasonDockerImage,
		generatedDomainConstructionReasonDockerContainer,
		generatedDomainConstructionReasonDockerVolume,
		generatedDomainConstructionReasonDockerNetwork,
		generatedDomainConstructionReasonDockerRestartConfig,
		generatedDomainConstructionReasonDockerLiveConfig,
		generatedDomainConstructionReasonDockerStaticResponse,
		generatedDomainConstructionReasonStability,
		generatedDomainConstructionReasonClassification,
		generatedDomainConstructionReasonDisposition,
		generatedDomainConstructionReasonRecovery,
		generatedDomainConstructionReasonStateStore,
		generatedDomainConstructionReasonManifest,
		generatedDomainConstructionReasonProcessLatch,
		generatedDomainConstructionReasonFence,
		generatedDomainConstructionReasonAssembly,
		generatedDomainConstructionReasonColdAccounting,
		generatedDomainConstructionReasonColdGroupReconstruction,
		generatedDomainConstructionReasonColdBuilderAttestation,
		generatedDomainConstructionReasonColdReservation,
		generatedDomainConstructionReasonColdOwnership,
		generatedDomainConstructionReasonColdHead,
		generatedDomainConstructionReasonColdGroupAttestation,
		generatedDomainConstructionReasonColdManifest,
		generatedDomainConstructionReasonLock,
		generatedDomainConstructionReasonRelease,
		generatedDomainConstructionReasonFinalValidation:
		return true
	default:
		return false
	}
}

func generatedDomainConstructionReasonFromError(err error,
	fallback generatedDomainConstructionReason,
) generatedDomainConstructionReason {
	var diagnostic *generatedDomainConstructionDiagnostic
	if errors.As(err, &diagnostic) && diagnostic != nil &&
		validGeneratedDomainConstructionReason(diagnostic.reason) {
		return diagnostic.reason
	}
	return fallback
}

// generatedDomainStartupObservationDiagnostic replaces a startup boundary
// that already collapsed its source error. Only the pre-existing public
// sentinel remains unwrap-visible; nested private codes are copied by value.
func generatedDomainStartupObservationDiagnostic(reason generatedDomainConstructionReason,
	sentinel, source error,
) error {
	return newGeneratedDomainConstructionDiagnostic(generatedDomainConstructionStageStartupObservation,
		generatedDomainConstructionReasonFromError(source, reason), sentinel)
}

// generatedDomainConstructionFinalDiagnostic replaces a factory boundary
// that already returned only errGeneratedDomainConstructionRefused. It does
// not make the underlying observation error unwrap-visible.
func generatedDomainConstructionFinalDiagnostic(stage generatedDomainConstructionStage,
	reason generatedDomainConstructionReason, source error,
) error {
	return newGeneratedDomainConstructionDiagnostic(stage,
		generatedDomainConstructionReasonFromError(source, reason), errGeneratedDomainConstructionRefused)
}

// generatedDomainDockerConstructionDiagnostic decorates a Docker reader error
// at the same private boundary that previously returned that error directly.
// The original cause remains unwrap-visible there; the enclosing startup
// observation deliberately replaces it with its pre-existing startup sentinel.
func generatedDomainDockerConstructionDiagnostic(reason generatedDomainConstructionReason, cause error) error {
	return newGeneratedDomainConstructionDiagnostic(generatedDomainConstructionStageStartupObservation,
		generatedDomainConstructionReasonFromError(cause, reason), cause)
}

// generatedDomainDockerCollapsedDiagnostic labels a Docker predicate that
// already replaced its source error with fixed text. It copies a nested closed
// reason but keeps only that pre-existing fixed error unwrap-visible.
func generatedDomainDockerCollapsedDiagnostic(reason generatedDomainConstructionReason,
	source, collapsed error,
) error {
	return newGeneratedDomainConstructionDiagnostic(generatedDomainConstructionStageStartupObservation,
		generatedDomainConstructionReasonFromError(source, reason), collapsed)
}
