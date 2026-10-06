package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindOwnedResourceInspection contains only purpose-bound resource
// identities retained in a validated terminal receipt. Cleanup and old-NIC
// return code must use these identities and reattest ownership; names alone
// are never mutation authority.
type GatewayRebindOwnedResourceInspection struct {
	ImageID                       string
	IngressNetworkName            string
	IngressNetworkID              string
	IngressNetworkOwnershipDigest string
	ConfigVolumeName              string
	ConfigVolumeOwnershipDigest   string
	DataVolumeName                string
	DataVolumeOwnershipDigest     string
	StageContainerID              string
	StageContainerOwnershipDigest string
	FinalContainerID              string
	FinalContainerOwnershipDigest string
	ApplicationNetworks           []GatewayRebindApplicationNetworkInspection
}

type GatewayRebindApplicationNetworkInspection struct {
	Name string
	ID   string
}

type GatewayRebindRetainedOperationInspection struct {
	Generation             uint64
	OperationID            string
	Disposition            appaccess.GatewayRebindTerminalDisposition
	TerminalReceiptDigest  string
	PredecessorOperationID string
	Resources              GatewayRebindOwnedResourceInspection
}

// GatewayRebindCurrentInspection keeps active recovery direction separate
// from the SQL-selected current lineage. A committed current rebind can be
// valid while a later prepared operation remains active.
type GatewayRebindCurrentInspection struct {
	SelectedCurrentAuthority appaccess.GatewayCurrentAuthorityRef
	CurrentStateVersion      uint64
	CurrentStateRevision     uint64
	CurrentStateDigest       string
	ActiveOperationID        string
	ActivePhase              appaccess.GatewayRebindState
	FenceReleased            bool
	Retained                 []GatewayRebindRetainedOperationInspection
}

type gatewayRebindCurrentInspectionRepository interface {
	GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)
	CheckGatewayRebindFence(context.Context) error
}

// InspectGatewayRebindCurrent is a read-only, lock-held cleanup and startup
// seam. It works while a SQL fence is active, so it takes the raw gateway lock
// and reports fence status rather than using ordinary lockGateway admission.
func (m *Manager) InspectGatewayRebindCurrent(ctx context.Context,
	repository gatewayRebindCurrentInspectionRepository,
) (result GatewayRebindCurrentInspection, resultErr error) {
	if m == nil || ctx == nil || repository == nil {
		return GatewayRebindCurrentInspection{}, &Error{Code: DiagnosticValidationFailed}
	}
	release, err := m.lockGatewayRawForInspection(ctx)
	if err != nil {
		return GatewayRebindCurrentInspection{}, err
	}
	defer releaseGatewayLock(release, &resultErr)
	return m.inspectGatewayRebindCurrentLocked(ctx, repository)
}

func (m *Manager) inspectGatewayRebindCurrentLocked(ctx context.Context,
	repository gatewayRebindCurrentInspectionRepository,
) (GatewayRebindCurrentInspection, error) {
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.CurrentSource == nil {
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	result := GatewayRebindCurrentInspection{SelectedCurrentAuthority: *snapshot.CurrentSource}
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil {
			return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
		}
		result.CurrentStateVersion = 2
		result.CurrentStateDigest, err = canonicalDigest(selection.Upgrade.State)
	case gatewayCurrentSelectionRebind:
		if selection.State == nil {
			return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
		}
		result.CurrentStateVersion = selection.State.Version
		result.CurrentStateRevision = selection.State.Revision
		result.CurrentStateDigest = selection.State.Digest
	default:
		err = errors.New("generated ingress current selection kind is invalid")
	}
	if err != nil {
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	if snapshot.Active != nil {
		result.ActiveOperationID, err = gatewayRebindHistoryOperationID(*snapshot.Active)
		if err != nil || result.ActiveOperationID == "" || snapshot.Phase == "" {
			return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
		}
		result.ActivePhase = snapshot.Phase
	} else if snapshot.Phase != "" || snapshot.DatabaseCommittedEvent != nil ||
		snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed {
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	fenceErr := repository.CheckGatewayRebindFence(ctx)
	switch {
	case fenceErr == nil:
		result.FenceReleased = !m.gatewayRebindAdmissionBlocked()
	case errors.Is(fenceErr, appaccess.ErrGatewayRebindActive):
		result.FenceReleased = false
	default:
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	result.Retained = make([]GatewayRebindRetainedOperationInspection, 0, len(history.Terminals)+len(history.TerminalsV2))
	for _, terminal := range history.Terminals {
		value, err := gatewayRebindRetainedOperationInspection(terminal.Receipt)
		if err != nil {
			return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
		}
		result.Retained = append(result.Retained, value)
	}
	for _, terminal := range history.TerminalsV2 {
		value, err := gatewayRebindRetainedOperationInspectionV2(terminal.Receipt)
		if err != nil {
			return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
		}
		result.Retained = append(result.Retained, value)
	}
	sort.Slice(result.Retained, func(i, j int) bool { return result.Retained[i].Generation < result.Retained[j].Generation })
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, confirmed) || ctx.Err() != nil {
		return GatewayRebindCurrentInspection{}, gatewayRebindProposalError(ctx)
	}
	return result, nil
}

func gatewayRebindRetainedOperationInspectionV2(receipt gatewayRebindTerminalReceiptV2) (
	GatewayRebindRetainedOperationInspection, error,
) {
	if !validGatewayRebindTerminalReceiptV2(receipt) {
		return GatewayRebindRetainedOperationInspection{}, errors.New("typed retained receipt is invalid")
	}
	result := GatewayRebindRetainedOperationInspection{
		Generation: receipt.Generation, OperationID: receipt.OperationID, Disposition: receipt.Disposition,
		TerminalReceiptDigest: receipt.Digest, PredecessorOperationID: receipt.Predecessor.Lineage.OperationID,
	}
	if receipt.RollbackProof != nil {
		owned := receipt.RollbackProof.Owned
		result.Resources.ImageID = owned.ImageID
		if owned.IngressNetwork != nil {
			result.Resources.IngressNetworkName = receipt.SuccessorIdentity.IngressNetwork
			result.Resources.IngressNetworkID = owned.IngressNetwork.ID
			result.Resources.IngressNetworkOwnershipDigest = owned.IngressNetwork.OwnershipDigest
		}
		if owned.ConfigVolume != nil {
			result.Resources.ConfigVolumeName = owned.ConfigVolume.Name
			result.Resources.ConfigVolumeOwnershipDigest = owned.ConfigVolume.OwnershipDigest
		}
		if owned.DataVolume != nil {
			result.Resources.DataVolumeName = owned.DataVolume.Name
			result.Resources.DataVolumeOwnershipDigest = owned.DataVolume.OwnershipDigest
		}
		if owned.StageContainer != nil {
			result.Resources.StageContainerID = owned.StageContainer.ID
			result.Resources.StageContainerOwnershipDigest = owned.StageContainer.OwnershipDigest
		}
		if owned.FinalContainer != nil {
			result.Resources.FinalContainerID = owned.FinalContainer.ID
			result.Resources.FinalContainerOwnershipDigest = owned.FinalContainer.OwnershipDigest
		}
		for _, network := range owned.ApplicationNetworks {
			result.Resources.ApplicationNetworks = append(result.Resources.ApplicationNetworks,
				GatewayRebindApplicationNetworkInspection{Name: network.Name, ID: network.ID})
		}
		return result, nil
	}
	if receipt.Resources == nil {
		return result, nil
	}
	resources := *receipt.Resources
	result.Resources = GatewayRebindOwnedResourceInspection{
		ImageID: resources.ImageID, IngressNetworkName: receipt.SuccessorIdentity.IngressNetwork,
		IngressNetworkID:              resources.IngressNetwork.ID,
		IngressNetworkOwnershipDigest: resources.IngressNetwork.OwnershipDigest,
		ConfigVolumeName:              resources.ConfigVolume.Name, ConfigVolumeOwnershipDigest: resources.ConfigVolume.OwnershipDigest,
		DataVolumeName: resources.DataVolume.Name, DataVolumeOwnershipDigest: resources.DataVolume.OwnershipDigest,
		StageContainerID: resources.StageContainer.ID, StageContainerOwnershipDigest: resources.StageContainer.OwnershipDigest,
	}
	if resources.FinalContainer != nil {
		result.Resources.FinalContainerID = resources.FinalContainer.ID
		result.Resources.FinalContainerOwnershipDigest = resources.FinalContainer.OwnershipDigest
	}
	for _, network := range resources.ApplicationNetworks {
		result.Resources.ApplicationNetworks = append(result.Resources.ApplicationNetworks,
			GatewayRebindApplicationNetworkInspection{Name: network.Name, ID: network.ID})
	}
	sort.Slice(result.Resources.ApplicationNetworks, func(i, j int) bool {
		if result.Resources.ApplicationNetworks[i].Name != result.Resources.ApplicationNetworks[j].Name {
			return result.Resources.ApplicationNetworks[i].Name < result.Resources.ApplicationNetworks[j].Name
		}
		return result.Resources.ApplicationNetworks[i].ID < result.Resources.ApplicationNetworks[j].ID
	})
	return result, nil
}

func gatewayRebindHistoryOperationID(entry appaccess.GatewayRebindHistoryEntry) (string, error) {
	switch {
	case entry.Claim.SpecVersion == 1 && entry.Claim.Legacy != nil && entry.Claim.V2 == nil:
		return entry.Claim.Legacy.Spec.OperationID, nil
	case entry.Claim.SpecVersion == appaccess.GatewayRebindSpecVersionV2 && entry.Claim.Legacy == nil && entry.Claim.V2 != nil:
		return entry.Claim.V2.Spec.OperationID, nil
	default:
		return "", errors.New("generated ingress rebind claim record is invalid")
	}
}

func gatewayRebindRetainedOperationInspection(receipt gatewayRebindFinalHandoverTerminalReceipt) (
	GatewayRebindRetainedOperationInspection, error,
) {
	if !validGatewayRebindFinalHandoverTerminalReceiptValue(receipt) {
		return GatewayRebindRetainedOperationInspection{}, errors.New("generated ingress retained receipt is invalid")
	}
	disposition := appaccess.GatewayRebindDispositionAbort
	if receipt.Disposition == gatewayRebindFinalHandoverTerminalCommit {
		disposition = appaccess.GatewayRebindDispositionCommit
	}
	resources := GatewayRebindOwnedResourceInspection{
		ImageID:                       receipt.Resources.ImageID,
		IngressNetworkName:            receipt.SuccessorIdentity.IngressNetwork,
		IngressNetworkID:              receipt.Resources.IngressNetwork.ID,
		IngressNetworkOwnershipDigest: receipt.Resources.IngressNetwork.OwnershipDigest,
		ConfigVolumeName:              receipt.Resources.ConfigVolume.Name,
		ConfigVolumeOwnershipDigest:   receipt.Resources.ConfigVolume.OwnershipDigest,
		DataVolumeName:                receipt.Resources.DataVolume.Name,
		DataVolumeOwnershipDigest:     receipt.Resources.DataVolume.OwnershipDigest,
		StageContainerID:              receipt.Resources.StageContainer.ID,
		StageContainerOwnershipDigest: receipt.Resources.StageContainer.OwnershipDigest,
	}
	if receipt.Resources.FinalContainer != nil {
		resources.FinalContainerID = receipt.Resources.FinalContainer.ID
		resources.FinalContainerOwnershipDigest = receipt.Resources.FinalContainer.OwnershipDigest
	}
	resources.ApplicationNetworks = make([]GatewayRebindApplicationNetworkInspection, 0,
		len(receipt.Resources.ApplicationNetworks))
	for _, network := range receipt.Resources.ApplicationNetworks {
		resources.ApplicationNetworks = append(resources.ApplicationNetworks,
			GatewayRebindApplicationNetworkInspection{Name: network.Name, ID: network.ID})
	}
	sort.Slice(resources.ApplicationNetworks, func(i, j int) bool {
		if resources.ApplicationNetworks[i].Name != resources.ApplicationNetworks[j].Name {
			return resources.ApplicationNetworks[i].Name < resources.ApplicationNetworks[j].Name
		}
		return resources.ApplicationNetworks[i].ID < resources.ApplicationNetworks[j].ID
	})
	return GatewayRebindRetainedOperationInspection{
		Generation: receipt.Generation, OperationID: receipt.OperationID, Disposition: disposition,
		TerminalReceiptDigest: receipt.Digest, PredecessorOperationID: receipt.Predecessor.OperationID,
		Resources: resources,
	}, nil
}
