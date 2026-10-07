package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayCurrentTerminalStopPurpose = "hostd/generated-ingress/terminal-owned-stop/v1"

// Terminal-only withdrawal deliberately contains no current route state. A
// corrupt or missing bundle cannot become serving authority by reconstruction.
type gatewayCurrentTerminalStopTarget struct {
	Version int
	Purpose string
	Intent  gatewayRebindProtectedIntentV2
	Facts   gatewayFinalOwnershipFacts
	Digest  string
}

func gatewayCurrentTerminalStopTargetDigest(target gatewayCurrentTerminalStopTarget) (string, error) {
	target.Digest = ""
	return canonicalDigest(target)
}

func validGatewayCurrentTerminalStopTarget(target gatewayCurrentTerminalStopTarget) bool {
	if target.Version != 1 || target.Purpose != gatewayCurrentTerminalStopPurpose ||
		!validGatewayFinalOwnershipFacts(target.Facts) || !validGatewayRebindProtectedIntentV2(target.Intent) ||
		target.Facts.Terminal.Format != gatewayRebindAttemptTerminalTypedV2 ||
		target.Facts.Terminal.ProtectedIntentDigest != target.Intent.Digest ||
		target.Facts.Lineage.OperationID != target.Intent.OperationID ||
		target.Facts.Lineage.ProtectedGeneration != target.Intent.Generation ||
		target.Facts.Network != gatewayV2NetworkPlan(target.Intent.Network) {
		return false
	}
	digest, err := gatewayCurrentTerminalStopTargetDigest(target)
	return err == nil && digest == target.Digest
}

type gatewayCurrentTerminalStopDriver interface {
	stopGatewayCurrentTerminalTarget(context.Context, gatewayCurrentTerminalStopTarget, func(context.Context) error) error
}

func gatewayCurrentTerminalStopTargets(history gatewayRebindProtectedIntentHistory) ([]gatewayCurrentTerminalStopTarget, error) {
	invalid := errors.New("generated ingress terminal withdrawal ownership is invalid")
	ids := map[string]bool{}
	for _, entry := range history.Terminals {
		if entry.Receipt.Disposition != gatewayRebindFinalHandoverTerminalCommit {
			continue
		}
		final := entry.Receipt.Resources.FinalContainer
		if final == nil || ids[final.ID] {
			return nil, invalid
		}
		ids[final.ID] = true
	}
	var result []gatewayCurrentTerminalStopTarget
	for _, entry := range history.TerminalsV2 {
		if entry.Receipt.Disposition != appaccess.GatewayRebindDispositionCommit {
			continue
		}
		terminal, err := newGatewayRebindAttemptTerminalViewV2(entry.Receipt)
		if err != nil || terminal.Resources.FinalContainer == nil || ids[terminal.Resources.FinalContainer.ID] {
			return nil, invalid
		}
		ids[terminal.Resources.FinalContainer.ID] = true
		lineage, err := gatewayRebindCurrentLineageV2(entry.Receipt)
		if err != nil {
			return nil, invalid
		}
		var intent *gatewayRebindProtectedIntentV2
		for _, candidate := range history.IntentsV2 {
			if candidate.Intent.Digest == terminal.ProtectedIntentDigest {
				if intent != nil {
					return nil, invalid
				}
				copy := candidate.Intent
				intent = &copy
			}
		}
		if intent == nil {
			return nil, invalid
		}
		target := gatewayCurrentTerminalStopTarget{Version: 1, Purpose: gatewayCurrentTerminalStopPurpose,
			Intent: *intent, Facts: gatewayFinalOwnershipFacts{Lineage: lineage, Terminal: terminal,
				Network: gatewayV2NetworkPlan(intent.Network), LocalHostPort: gatewayRebindRetainedHandoverLocalPort(history.Progress, terminal.Generation, terminal.OperationID)}}
		target.Digest, err = gatewayCurrentTerminalStopTargetDigest(target)
		if err != nil || !validGatewayCurrentTerminalStopTarget(target) {
			return nil, invalid
		}
		result = append(result, target)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Facts.Lineage.ProtectedGeneration < result[j].Facts.Lineage.ProtectedGeneration
	})
	return result, nil
}

// Called only under the emergency effects lease and gateway locks. The original
// enumeration error remains the caller's result even if every exact ID stops.
func (m *Manager) stopGatewayCurrentTerminalFallbackLocked(ctx context.Context,
	stateTargets []gatewayCurrentOwnedStopTarget,
) (verified, stopped int, census gatewayCurrentOwnedStopHistoryCensus) {
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentTerminalStopDriver)
	if !ok {
		return 0, 0, census
	}
	readHistory := func() (gatewayRebindProtectedIntentHistory, error) {
		history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err == nil {
			return history, nil
		}
		return m.scanGatewayCurrentTypedWithdrawalHistoryLocked(readGatewayRebindProtectedPresenceForTerminalWithdrawal)
	}
	presence, presenceErr := readGatewayRebindProtectedPresenceForTerminalWithdrawal(m.options.DataRoot)
	history, err := readHistory()
	if err != nil || presenceErr != nil {
		return 0, 0, census
	}
	census, _ = gatewayCurrentOwnedStopCensus(history, presence)
	targets, err := gatewayCurrentTerminalStopTargets(history)
	files, filesErr := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || filesErr != nil {
		return 0, 0, census
	}
	guard := func(effectCtx context.Context) error {
		if effectCtx == nil || effectCtx.Err() != nil {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		fresh, err := readHistory()
		freshTargets, targetErr := gatewayCurrentTerminalStopTargets(fresh)
		freshFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
		freshPresence, presenceErr := readGatewayRebindProtectedPresenceForTerminalWithdrawal(m.options.DataRoot)
		if err != nil || targetErr != nil || filesErr != nil || presenceErr != nil ||
			!sameGatewayRebindProtectedPresence(presence, freshPresence) || !sameGatewayRebindCurrentHistory(history, fresh) ||
			!sameGatewayHistorySnapshot(files, freshFiles) || !reflect.DeepEqual(targets, freshTargets) || effectCtx.Err() != nil {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		return nil
	}
	known := make(map[string]bool, len(stateTargets))
	for _, target := range stateTargets {
		known[target.FinalContainer.ID] = true
	}
	for _, target := range targets {
		if known[target.Facts.Terminal.Resources.FinalContainer.ID] {
			continue
		}
		verified++
		if guard(ctx) == nil && driver.stopGatewayCurrentTerminalTarget(ctx, target, guard) == nil && guard(ctx) == nil {
			stopped++
		}
	}
	if guard(ctx) != nil {
		return verified, 0, census
	}
	return verified, stopped, census
}
