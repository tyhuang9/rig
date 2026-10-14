package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindStartupPreparation struct {
	ingress *generatedingress.Manager
	current generatedingress.GatewayRebindCurrentInspection
}

type gatewayRebindStartupDisposition uint8

const (
	// The zero value is deliberately refusing. A caller must receive explicit
	// validated proof before it can enter either normal or recovery-only mode.
	gatewayRebindStartupRefuse gatewayRebindStartupDisposition = iota
	gatewayRebindStartupNormal
	gatewayRebindStartupRecoveryOnly
)

// gatewayRebindRecoveryPin is immutable advisory dispatch data. It contains
// no effect authority: every recovery request rebuilds and revalidates its SQL,
// protected and physical evidence under the normal lease and lock ordering.
type gatewayRebindRecoveryPin struct {
	OperationID            string
	SQLPhase               appaccess.GatewayRebindState
	Policy                 appaccess.GatewayRebindRecoveryPolicy
	DatabaseCommittedEvent *appaccess.GatewayRebindEvent
	Protected              appaccess.GatewayRebindProtectedRecoveryEvidence
	SelectedCurrent        appaccess.GatewayRebindSelectedCurrentEvidence
}

type gatewayRebindStartupDecision struct {
	disposition gatewayRebindStartupDisposition
	preparation gatewayRebindStartupPreparation
	recoveryPin gatewayRebindRecoveryPin
}

// Each recovery callback owns and releases its deployment-effects and gateway
// leases. Preparation must finish before ordinary startup admission acquires
// its lease. These closures bind every operation to the same manager/repository.
type gatewayRebindStartupRecovery struct {
	inspect func(context.Context) (generatedingress.GatewayRebindCurrentInspection, error)
	retire  func(context.Context) (bool, error)
	restore func(context.Context) (bool, error)
	recover func(context.Context) (generatedingress.GatewayRebindStartupRecoveryResult, error)
}

func prepareGatewayRebindStartup(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories,
) (gatewayRebindStartupDecision, error) {
	return prepareGatewayRebindStartupWithRunner(ctx, cfg, db, dockerExecutable, directories,
		runtimeprocess.ExecRunner{})
}

func prepareGatewayRebindStartupWithRunner(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories, runner runtimeprocess.CommandRunner,
) (gatewayRebindStartupDecision, error) {
	refuse := func(err error) (gatewayRebindStartupDecision, error) {
		return gatewayRebindStartupDecision{disposition: gatewayRebindStartupRefuse}, err
	}
	if ctx == nil || db == nil {
		return refuse(errors.New("gateway startup dependencies are required"))
	}
	repository := appaccess.New(db)
	presence, err := generatedingress.InspectGatewayRebindStartupPresence(ctx, cfg.DataRoot, repository)
	if err != nil {
		return refuse(fmt.Errorf("inspect gateway rebind startup presence: %w", err))
	}
	if !presence.Present {
		return gatewayRebindStartupDecision{disposition: gatewayRebindStartupNormal}, nil
	}
	if !cfg.GeneratedRuntime {
		return refuse(errors.New("generated runtime is required to recover retained gateway rebind history"))
	}
	ingress, err := newGatewayStartupIngressWithRunner(cfg, dockerExecutable, directories,
		rebindFenceCheck(db), repository, runner)
	if err != nil {
		return refuse(err)
	}
	inspected, err := ingress.InspectGatewayRebindCurrent(ctx, repository)
	if err != nil || !gatewayRebindStartupPresenceMatches(presence, inspected) {
		return refuse(errors.New("gateway rebind startup evidence changed during preparation"))
	}
	if inspected.ActivePhase == appaccess.GatewayRebindUnresolved {
		pin, pinErr := gatewayRebindRecoveryPinForInspection(inspected)
		if pinErr != nil {
			return refuse(pinErr)
		}
		return gatewayRebindStartupDecision{disposition: gatewayRebindStartupRecoveryOnly,
			preparation: gatewayRebindStartupPreparation{ingress: ingress, current: inspected}, recoveryPin: pin}, nil
	}
	current, err := prepareGatewayRebindRecovery(ctx, presence, gatewayRebindStartupRecovery{
		inspect: func(ctx context.Context) (generatedingress.GatewayRebindCurrentInspection, error) {
			return ingress.InspectGatewayRebindCurrent(ctx, repository)
		},
		retire: func(ctx context.Context) (bool, error) {
			return ingress.RetireGatewayCurrentPredecessorsStartup(ctx, repository)
		},
		restore: func(ctx context.Context) (bool, error) {
			return ingress.RestoreGatewayCurrentServingStartup(ctx, repository)
		},
		recover: func(ctx context.Context) (generatedingress.GatewayRebindStartupRecoveryResult, error) {
			return ingress.RecoverGatewayRebindStartup(ctx, repository)
		},
	})
	if err != nil {
		return refuse(err)
	}
	return gatewayRebindStartupDecision{disposition: gatewayRebindStartupNormal,
		preparation: gatewayRebindStartupPreparation{ingress: ingress, current: current}}, nil
}

func gatewayRebindStartupPresenceMatches(presence generatedingress.GatewayRebindStartupPresence,
	current generatedingress.GatewayRebindCurrentInspection,
) bool {
	return presence.Present && presence.SelectedCurrentAuthority != nil &&
		current.SelectedCurrentAuthority == *presence.SelectedCurrentAuthority &&
		current.ActiveOperationID == presence.ActiveOperationID && current.ActivePhase == presence.ActivePhase
}

func gatewayRebindRecoveryPinForInspection(current generatedingress.GatewayRebindCurrentInspection) (
	gatewayRebindRecoveryPin, error,
) {
	if current.ActiveOperationID == "" || current.ActiveSpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		current.ActivePhase != appaccess.GatewayRebindUnresolved || current.FenceReleased ||
		current.ActiveRecoveryPolicy == "" ||
		current.ActiveProtectedEvidence.OperationID != current.ActiveOperationID ||
		current.ActiveProtectedEvidence.Generation == 0 || current.ActiveProtectedEvidence.ScanDigest == "" ||
		current.SelectedCurrentEvidence.Lineage.OperationID == "" ||
		gatewayRebindAuthorityForEvidence(current.SelectedCurrentEvidence) != current.SelectedCurrentAuthority ||
		current.ActiveDatabaseCommitObserved != (current.ActiveDatabaseCommittedEvent != nil) {
		return gatewayRebindRecoveryPin{}, errors.New("gateway rebind recovery pin is incomplete")
	}
	if current.ActiveDatabaseCommittedEvent != nil &&
		(current.ActiveDatabaseCommittedEvent.OperationID != current.ActiveOperationID ||
			current.ActiveDatabaseCommittedEvent.State != appaccess.GatewayRebindDatabaseCommitted ||
			current.ActiveDatabaseCommittedEvent.Sequence <= 0 ||
			current.ActiveDatabaseCommittedEvent.CreatedAt.IsZero()) {
		return gatewayRebindRecoveryPin{}, errors.New("gateway rebind committed event does not match its recovery pin")
	}
	switch current.ActiveRecoveryPolicy {
	case appaccess.GatewayRebindRecoveryEvidenceDecides:
		return gatewayRebindRecoveryPin{}, errors.New("unresolved gateway rebind requires a fixed recovery direction")
	case appaccess.GatewayRebindRecoveryForwardOnly:
		if current.ActiveProtectedEvidence.FixedDisposition == appaccess.GatewayRebindDispositionAbort ||
			(!current.ActiveDatabaseCommitObserved &&
				current.ActiveProtectedEvidence.FixedDisposition != appaccess.GatewayRebindDispositionCommit) {
			return gatewayRebindRecoveryPin{}, errors.New("gateway rebind forward-only pin has no immutable commit evidence")
		}
	case appaccess.GatewayRebindRecoveryRollbackOnly:
		if current.ActiveDatabaseCommitObserved || !current.ActiveRollbackAllowed ||
			current.ActiveProtectedEvidence.FixedDisposition != appaccess.GatewayRebindDispositionAbort {
			return gatewayRebindRecoveryPin{}, errors.New("gateway rebind rollback-only pin is contradictory")
		}
	default:
		return gatewayRebindRecoveryPin{}, errors.New("gateway rebind recovery policy is invalid")
	}
	pin := gatewayRebindRecoveryPin{OperationID: current.ActiveOperationID, SQLPhase: current.ActivePhase,
		Policy: current.ActiveRecoveryPolicy, Protected: current.ActiveProtectedEvidence,
		SelectedCurrent: current.SelectedCurrentEvidence}
	if current.ActiveDatabaseCommittedEvent != nil {
		committed := *current.ActiveDatabaseCommittedEvent
		pin.DatabaseCommittedEvent = &committed
	}
	return pin, nil
}

func gatewayRebindAuthorityForEvidence(value appaccess.GatewayRebindSelectedCurrentEvidence) appaccess.GatewayCurrentAuthorityRef {
	return appaccess.GatewayCurrentAuthorityRef{Kind: value.Lineage.Kind, OperationID: value.Lineage.OperationID,
		ProfileRevisionID:     value.Lineage.ProfileRevisionID,
		ProfileRevisionNumber: value.Lineage.ProfileRevisionNumber,
		ProfileSpecDigest:     value.Lineage.ProfileSpecDigest,
		TerminalReceiptDigest: value.Lineage.TerminalReceiptDigest}
}

// Called with ordinary startup admission held. Pin preparation only here:
// subsequent ordinary-route recovery and LAN batch retirement may legitimately
// change protected state and remain governed by their existing checks.
func inspectGatewayStartupAfterPreparation(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories, decision gatewayRebindStartupDecision,
) (gatewayStartup, error) {
	return inspectGatewayStartupAfterPreparationWithRunner(ctx, cfg, db, dockerExecutable, directories,
		decision, runtimeprocess.ExecRunner{})
}

func inspectGatewayStartupAfterPreparationWithRunner(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories, decision gatewayRebindStartupDecision,
	runner runtimeprocess.CommandRunner,
) (gatewayStartup, error) {
	if ctx == nil || db == nil {
		return gatewayStartup{}, errors.New("gateway startup dependencies are required")
	}
	if decision.disposition != gatewayRebindStartupNormal {
		return gatewayStartup{}, errors.New("normal gateway startup requires an explicit normal rebind decision")
	}
	prepared := decision.preparation
	fenceCheck := rebindFenceCheck(db)
	if err := fenceCheck(ctx); err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect gateway rebind fence after preparation: %w", err)
	}
	repository := appaccess.New(db)
	var ingress *generatedingress.Manager
	if prepared.ingress == nil {
		presence, err := generatedingress.InspectGatewayRebindStartupPresence(ctx, cfg.DataRoot, repository)
		if err != nil || presence.Present {
			return gatewayStartup{}, errors.New("gateway rebind history changed before startup admission")
		}
	} else {
		if !cfg.GeneratedRuntime {
			return gatewayStartup{}, errors.New("prepared gateway manager is required")
		}
		current, err := prepared.ingress.InspectGatewayRebindCurrent(ctx, repository)
		if err != nil || !reflect.DeepEqual(current, prepared.current) {
			return gatewayStartup{}, errors.New("current gateway changed before startup admission")
		}
		ingress = prepared.ingress
	}
	return inspectGatewayStartupUsingIngressWithRunner(ctx, cfg, db, dockerExecutable, directories,
		fenceCheck, ingress, runner)
}

func prepareGatewayRebindRecovery(ctx context.Context, presence generatedingress.GatewayRebindStartupPresence,
	recovery gatewayRebindStartupRecovery,
) (generatedingress.GatewayRebindCurrentInspection, error) {
	refuse := func() (generatedingress.GatewayRebindCurrentInspection, error) {
		return generatedingress.GatewayRebindCurrentInspection{}, errors.New("gateway rebind startup preparation could not be verified")
	}
	if ctx == nil || ctx.Err() != nil || !presence.Present || presence.SelectedCurrentAuthority == nil ||
		recovery.inspect == nil || recovery.retire == nil || recovery.restore == nil || recovery.recover == nil {
		return refuse()
	}
	before, err := recovery.inspect(ctx)
	if err != nil {
		return generatedingress.GatewayRebindCurrentInspection{}, fmt.Errorf("inspect current gateway before recovery: %w", err)
	}
	if ctx.Err() != nil || before.SelectedCurrentAuthority != *presence.SelectedCurrentAuthority ||
		before.ActiveOperationID != presence.ActiveOperationID || before.ActivePhase != presence.ActivePhase {
		return refuse()
	}
	active := before.ActiveOperationID != ""
	current := before
	var result generatedingress.GatewayRebindStartupRecoveryResult
	if active {
		if before.ActiveSpecVersion != appaccess.GatewayRebindSpecVersionV2 || before.FenceReleased {
			return refuse()
		}
		switch before.ActivePhase {
		case appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady, appaccess.GatewayRebindDatabaseCommitted:
		default:
			return refuse()
		}
		result, err = recovery.recover(ctx)
		if err != nil {
			return generatedingress.GatewayRebindCurrentInspection{}, fmt.Errorf("recover gateway rebind before admission: %w", err)
		}
		current, err = recovery.inspect(ctx)
		if err != nil || ctx.Err() != nil || !gatewayRebindStartupTerminalInspection(current) ||
			!gatewayRebindStartupRecoveryMatches(before, result, current) {
			return refuse()
		}
	}
	if ctx.Err() != nil || !gatewayRebindStartupTerminalInspection(current) {
		return refuse()
	}
	switch current.CurrentRecoveryMode {
	case generatedingress.GatewayCurrentRecoveryNative:
		if current.SelectedCurrentAuthority.Kind != appaccess.GatewayRebindSourceGatewayUpgrade {
			return refuse()
		}
		// A native predecessor has no committed rebind ancestor to retire.
		// Active ABORT recovery has already validated its exact terminal result.
		return current, nil
	case generatedingress.GatewayCurrentRecoveryStable, generatedingress.GatewayCurrentRecoveryRoute,
		generatedingress.GatewayCurrentRecoveryLAN, generatedingress.GatewayCurrentRecoveryLANBatch,
		generatedingress.GatewayCurrentRecoveryLANBatchDone:
		if current.SelectedCurrentAuthority.Kind != appaccess.GatewayRebindSourceGatewayRebind {
			return refuse()
		}
	default:
		return refuse()
	}

	// Retirement owns its leases and must finish before any stable or dedicated
	// recovery path can proceed. Pin a fresh full inspection after lease release;
	// the physical effect guards independently reject later predecessor revival.
	handled, err := recovery.retire(ctx)
	if err != nil {
		return generatedingress.GatewayRebindCurrentInspection{}, fmt.Errorf("retire gateway predecessors before admission: %w", err)
	}
	if !handled || ctx.Err() != nil {
		return refuse()
	}
	retired, err := recovery.inspect(ctx)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(current, retired) {
		return refuse()
	}
	switch current.CurrentRecoveryMode {
	case generatedingress.GatewayCurrentRecoveryRoute, generatedingress.GatewayCurrentRecoveryLAN,
		generatedingress.GatewayCurrentRecoveryLANBatch:
		return retired, nil
	case generatedingress.GatewayCurrentRecoveryStable, generatedingress.GatewayCurrentRecoveryLANBatchDone:
		handled, err = recovery.restore(ctx)
		if err != nil {
			return generatedingress.GatewayRebindCurrentInspection{}, fmt.Errorf("restore current gateway before admission: %w", err)
		}
		if !handled || ctx.Err() != nil {
			return refuse()
		}
	}
	// Completed batches remain owned by dedicated LAN retirement. An active
	// attempt already supplied its verified terminal result before retirement.
	terminalRecovery := !active && current.CurrentRecoveryMode == generatedingress.GatewayCurrentRecoveryStable
	if terminalRecovery {
		result, err = recovery.recover(ctx)
		if err != nil {
			return generatedingress.GatewayRebindCurrentInspection{}, fmt.Errorf("recover gateway rebind before admission: %w", err)
		}
	}
	after, err := recovery.inspect(ctx)
	if err != nil {
		return generatedingress.GatewayRebindCurrentInspection{}, fmt.Errorf("inspect current gateway after recovery: %w", err)
	}
	if ctx.Err() != nil || !gatewayRebindStartupTerminalInspection(after) || !reflect.DeepEqual(current, after) {
		return refuse()
	}
	if (active || terminalRecovery) && !gatewayRebindStartupRecoveryMatches(before, result, after) {
		return refuse()
	}
	return after, nil
}

func gatewayRebindStartupTerminalInspection(current generatedingress.GatewayRebindCurrentInspection) bool {
	return current.FenceReleased && current.ActiveOperationID == "" && current.ActivePhase == "" &&
		current.ActiveSpecVersion == 0 && current.ActiveDatabaseCommittedEvent == nil &&
		!current.ActiveDatabaseCommitObserved && !current.ActiveRollbackAllowed &&
		current.ActiveRecoveryPolicy == "" &&
		current.ActiveProtectedEvidence == (appaccess.GatewayRebindProtectedRecoveryEvidence{})
}

func gatewayRebindStartupRecoveryMatches(before generatedingress.GatewayRebindCurrentInspection,
	result generatedingress.GatewayRebindStartupRecoveryResult, after generatedingress.GatewayRebindCurrentInspection,
) bool {
	if !result.RebindHistoryPresent || !result.FenceReleased || result.SelectedCurrentAuthority == nil ||
		*result.SelectedCurrentAuthority != after.SelectedCurrentAuthority {
		return false
	}
	if after.SelectedCurrentAuthority.Kind == appaccess.GatewayRebindSourceGatewayRebind {
		if after.CurrentRecoveryMode != generatedingress.GatewayCurrentRecoveryStable ||
			result.CurrentStateVersion != after.CurrentStateVersion || result.CurrentStateRevision != after.CurrentStateRevision ||
			result.CurrentStateDigest != after.CurrentStateDigest || !gatewayStartupDigest(result.CurrentAttestationDigest) {
			return false
		}
	} else if after.SelectedCurrentAuthority.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		after.CurrentRecoveryMode != generatedingress.GatewayCurrentRecoveryNative ||
		result.CurrentStateVersion != 0 || result.CurrentStateRevision != 0 ||
		result.CurrentStateDigest != "" || result.CurrentAttestationDigest != "" {
		return false
	}
	if before.ActiveOperationID == "" {
		return !result.Recovered && result.ActiveOperationID == "" && result.InitialActivePhase == "" &&
			result.FinalActivePhase == "" && result.Disposition == "" && result.TerminalReceiptDigest == ""
	}
	if !result.Recovered || result.ActiveOperationID != before.ActiveOperationID ||
		result.InitialActivePhase != before.ActivePhase || !gatewayStartupDigest(result.TerminalReceiptDigest) {
		return false
	}
	switch result.FinalActivePhase {
	case appaccess.GatewayRebindCommitted:
		if result.Disposition != appaccess.GatewayRebindDispositionCommit ||
			after.SelectedCurrentAuthority.OperationID != before.ActiveOperationID ||
			after.SelectedCurrentAuthority.TerminalReceiptDigest != result.TerminalReceiptDigest {
			return false
		}
	case appaccess.GatewayRebindRolledBack:
		if result.Disposition != appaccess.GatewayRebindDispositionAbort ||
			before.ActivePhase == appaccess.GatewayRebindDatabaseCommitted || before.ActiveDatabaseCommitObserved ||
			before.ActiveDatabaseCommittedEvent != nil ||
			before.ActiveProtectedEvidence.FixedDisposition == appaccess.GatewayRebindDispositionCommit ||
			after.SelectedCurrentAuthority != before.SelectedCurrentAuthority ||
			after.CurrentRecoveryMode != before.CurrentRecoveryMode ||
			after.CurrentStateVersion != before.CurrentStateVersion ||
			after.CurrentStateRevision != before.CurrentStateRevision ||
			after.CurrentStateDigest != before.CurrentStateDigest {
			return false
		}
	default:
		return false
	}
	for _, retained := range after.Retained {
		if retained.OperationID == before.ActiveOperationID && retained.Disposition == result.Disposition &&
			retained.TerminalReceiptDigest == result.TerminalReceiptDigest {
			return true
		}
	}
	return false
}

func gatewayStartupDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}
