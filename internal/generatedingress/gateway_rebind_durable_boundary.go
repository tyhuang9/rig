package generatedingress

import (
	"context"
	"errors"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindDurableBoundary identifies a completed durable readback or
// release boundary in the supported rebind coordinator. It is diagnostic only:
// observing a boundary grants no authority and cannot change a coordinator
// result. The interface is internal to this module and is never selected by a
// command-line, environment or HTTP option.
type GatewayRebindDurableBoundary string

const (
	GatewayRebindBoundarySQLClaimReadback            GatewayRebindDurableBoundary = "sql_claim_readback"
	GatewayRebindBoundaryProtectedCheckpointReadback GatewayRebindDurableBoundary = "protected_checkpoint_readback"
	GatewayRebindBoundaryProtectedIntentReadback     GatewayRebindDurableBoundary = "protected_intent_readback"
	GatewayRebindBoundaryProtectedProgressReadback   GatewayRebindDurableBoundary = "protected_progress_readback"
	GatewayRebindBoundaryProtectedTerminalInstalled  GatewayRebindDurableBoundary = "protected_terminal_installed"
	GatewayRebindBoundaryProtectedTerminalReadback   GatewayRebindDurableBoundary = "protected_terminal_readback"
	GatewayRebindBoundarySQLTransitionReadback       GatewayRebindDurableBoundary = "sql_transition_readback"
	GatewayRebindBoundaryCurrentAttestation          GatewayRebindDurableBoundary = "current_attestation"
	GatewayRebindBoundaryFinalCensusReadback         GatewayRebindDurableBoundary = "final_census_readback"
	GatewayRebindBoundaryFenceClearReadback          GatewayRebindDurableBoundary = "fence_clear_readback"
	GatewayRebindBoundaryEffectsRelease              GatewayRebindDurableBoundary = "effects_release"
	GatewayRebindBoundaryGatewayRelease              GatewayRebindDurableBoundary = "gateway_release"
)

type GatewayRebindDurableBoundaryEvent struct {
	Boundary            GatewayRebindDurableBoundary
	OperationID         string
	SQLPhase            appaccess.GatewayRebindState
	ProtectedPhase      string
	ProtectedGeneration uint64
	ProtectedSequence   uint64
	Digest              string
	Disposition         appaccess.GatewayRebindTerminalDisposition
}

// GatewayRebindDurableBoundaryObserver is an optional capability of the exact
// CommandRunner supplied to New. A callback may pause the caller for controlled
// crash acceptance. It receives no mutable coordinator object and returns no
// value, so it cannot authorize, replace or report success for an operation.
type GatewayRebindDurableBoundaryObserver interface {
	ObserveGatewayRebindDurableBoundary(context.Context, GatewayRebindDurableBoundaryEvent)
}

type GatewayRebindTerminalReleaseKind string

const (
	GatewayRebindTerminalReleaseEffects GatewayRebindTerminalReleaseKind = "effects"
	GatewayRebindTerminalReleaseGateway GatewayRebindTerminalReleaseKind = "gateway"
)

// GatewayRebindTerminalReleaseFault is an optional acceptance capability of
// the exact CommandRunner supplied to New. Manager invokes it only after the
// real release function has been called exactly once. Its error is additive;
// it cannot skip a release or convert a real release error into success.
type GatewayRebindTerminalReleaseFault interface {
	GatewayRebindTerminalReleaseFault(GatewayRebindTerminalReleaseKind) error
}

func (m *Manager) observeGatewayRebindDurableBoundary(ctx context.Context,
	event GatewayRebindDurableBoundaryEvent,
) {
	if m == nil || m.runner == nil {
		return
	}
	if observer, ok := m.runner.(GatewayRebindDurableBoundaryObserver); ok {
		observer.ObserveGatewayRebindDurableBoundary(ctx, event)
	}
}

func (m *Manager) observeGatewayRebindTerminalReadback(ctx context.Context,
	receipt gatewayRebindTerminalReceiptV2,
) {
	m.observeGatewayRebindDurableBoundary(ctx, GatewayRebindDurableBoundaryEvent{
		Boundary: GatewayRebindBoundaryProtectedTerminalReadback, OperationID: receipt.OperationID,
		ProtectedPhase: receipt.ProtectedPhase, ProtectedGeneration: receipt.Generation,
		ProtectedSequence: receipt.ProtectedRecordSequence, Digest: receipt.Digest,
		Disposition: receipt.Disposition,
	})
}

func (m *Manager) observeGatewayRebindTerminalInstalled(ctx context.Context,
	receipt gatewayRebindTerminalReceiptV2,
) {
	m.observeGatewayRebindDurableBoundary(ctx, GatewayRebindDurableBoundaryEvent{
		Boundary: GatewayRebindBoundaryProtectedTerminalInstalled, OperationID: receipt.OperationID,
		ProtectedPhase: receipt.ProtectedPhase, ProtectedGeneration: receipt.Generation,
		ProtectedSequence: receipt.ProtectedRecordSequence, Digest: receipt.Digest,
		Disposition: receipt.Disposition,
	})
}

func gatewayRebindTerminalDurableBoundaryEvent(receipt gatewayRebindTerminalReceiptV2,
	phase appaccess.GatewayRebindState,
) GatewayRebindDurableBoundaryEvent {
	return GatewayRebindDurableBoundaryEvent{
		OperationID: receipt.OperationID, SQLPhase: phase, ProtectedPhase: receipt.ProtectedPhase,
		ProtectedGeneration: receipt.Generation, ProtectedSequence: receipt.ProtectedRecordSequence,
		Digest: receipt.Digest, Disposition: receipt.Disposition,
	}
}

func (m *Manager) observeGatewayRebindFinalCensusReadback(ctx context.Context,
	receipt gatewayRebindTerminalReceiptV2, phase appaccess.GatewayRebindState,
) GatewayRebindDurableBoundaryEvent {
	event := gatewayRebindTerminalDurableBoundaryEvent(receipt, phase)
	event.Boundary = GatewayRebindBoundaryFinalCensusReadback
	m.observeGatewayRebindDurableBoundary(ctx, event)
	return event
}

func (m *Manager) observeGatewayRebindFenceClearReadback(ctx context.Context,
	event GatewayRebindDurableBoundaryEvent,
) {
	event.Boundary = GatewayRebindBoundaryFenceClearReadback
	m.observeGatewayRebindDurableBoundary(ctx, event)
	// Release instrumentation is armed only by the fresh, successful final
	// census and fence readbacks for this exact operation.
	m.gatewayRebindTerminalReleaseEvent = event
}

func (m *Manager) releaseGatewayRebindTerminalBoundary(ctx context.Context,
	kind GatewayRebindTerminalReleaseKind, release func() error, event GatewayRebindDurableBoundaryEvent,
) error {
	if release == nil {
		return nil
	}
	realErr := release()
	// Several current-mode startup paths share the same ordered release helper.
	// Only a proved terminal rebind installs an operation event; keep acceptance
	// observation and fault injection scoped to that event.
	if realErr != nil || event == (GatewayRebindDurableBoundaryEvent{}) {
		return realErr
	}
	if !validGatewayRebindTerminalReleaseEvent(event) {
		return errors.New("invalid gateway rebind terminal release event")
	}
	var boundary GatewayRebindDurableBoundary
	switch kind {
	case GatewayRebindTerminalReleaseEffects:
		boundary = GatewayRebindBoundaryEffectsRelease
	case GatewayRebindTerminalReleaseGateway:
		boundary = GatewayRebindBoundaryGatewayRelease
	default:
		return errors.New("invalid gateway rebind terminal release kind")
	}
	event.Boundary = boundary
	m.observeGatewayRebindDurableBoundary(ctx, event)
	var injected error
	if m != nil {
		if fault, ok := m.runner.(GatewayRebindTerminalReleaseFault); ok {
			injected = fault.GatewayRebindTerminalReleaseFault(kind)
		}
	}
	return errors.Join(realErr, injected)
}

func validGatewayRebindTerminalReleaseEvent(event GatewayRebindDurableBoundaryEvent) bool {
	if event.Boundary != GatewayRebindBoundaryFenceClearReadback ||
		!validCanonicalUUID(event.OperationID) || event.ProtectedPhase == "" ||
		event.ProtectedGeneration == 0 || event.ProtectedSequence == 0 || !validSHA256(event.Digest) {
		return false
	}
	return (event.SQLPhase == appaccess.GatewayRebindCommitted &&
		event.Disposition == appaccess.GatewayRebindDispositionCommit) ||
		(event.SQLPhase == appaccess.GatewayRebindRolledBack &&
			event.Disposition == appaccess.GatewayRebindDispositionAbort)
}
