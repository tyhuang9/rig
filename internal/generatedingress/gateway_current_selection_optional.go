package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// readOptionalGatewayCurrentSelectionLocked preserves the fresh/pre-upgrade
// paths. Absence is proved separately from a current selection and always
// requires the injected SQL reader; missing files cannot prove missing SQL.
// The caller holds the Manager and gateway OS locks. The absence branch never
// creates protected storage; the selected branch uses the existing protected
// store constructors. Neither chooses a generation from receipt ordering.
func (m *Manager) readOptionalGatewayCurrentSelectionLocked(ctx context.Context) (
	gatewayCurrentSelection, appaccess.GatewayRebindRecoverySnapshot, bool, error,
) {
	invalid := errors.New("generated ingress optional current gateway selection is unresolved")
	if m == nil || ctx == nil || ctx.Err() != nil || m.options.RebindCurrentStateRepository == nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
	}
	repository := m.options.RebindCurrentStateRepository
	first, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || ctx.Err() != nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
	}
	if first.CurrentSource != nil {
		selection, confirmed, err := m.readGatewayCurrentSelectionLocked(ctx)
		if err != nil || !reflect.DeepEqual(first, confirmed) {
			return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
		}
		return selection, confirmed, true, nil
	}
	if !gatewayCurrentSelectionSQLAbsent(first) {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
	}
	before, err := readGatewayRebindProtectedPresenceReadOnly(m.options.DataRoot)
	if err != nil || before.present || ctx.Err() != nil {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, confirmed) {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
	}
	after, err := readGatewayRebindProtectedPresenceReadOnly(m.options.DataRoot)
	if err != nil || after.present || ctx.Err() != nil || !sameGatewayRebindProtectedPresence(before, after) {
		return gatewayCurrentSelection{}, appaccess.GatewayRebindRecoverySnapshot{}, false, invalid
	}
	return gatewayCurrentSelection{}, first, false, nil
}

func gatewayCurrentSelectionSQLAbsent(value appaccess.GatewayRebindRecoverySnapshot) bool {
	if value.CurrentSource != nil || len(value.History) != 0 || value.Active != nil || value.Phase != "" ||
		value.DatabaseCommittedEvent != nil || value.CurrentDatabaseCommittedEvent != nil ||
		len(value.CurrentTransfers) != 0 || value.DatabaseCommitObserved || value.RollbackAllowed {
		return false
	}
	if value.CurrentProfile == nil {
		return true
	}
	profile := value.CurrentProfile
	return validGatewayProfileBinding(gatewayProfileBinding{
		RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
		SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
		PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd,
	})
}
