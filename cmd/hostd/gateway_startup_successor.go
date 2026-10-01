package main

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

type lanDisableSuccessorStartupRepository interface {
	HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
	AcknowledgeAppAccessDisableSuccessor(context.Context, string, string, string, string, string,
		string, time.Time) (appaccess.AppAccessDisableSuccessorAck, bool, error)
}

type lanDisableSuccessorStartupAttester interface {
	AttestGatewayV2LANDisableSuccessor(context.Context, generatedingress.GatewayV2LANDisableRequest,
		generatedingress.GatewayV2LANGrantRequest, string,
		[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
		func(context.Context, generatedingress.GatewayV2LANDisableSuccessorObservation) error) error
}

// Historical successors are attested before the controller listener serves.
// Migration 030 cannot synthesize an old-port 404 when a later committed grant
// now owns that port, so each candidate gets a separate immutable proof.
func attestHistoricalLANDisableSuccessors(ctx context.Context, repository lanDisableSuccessorStartupRepository,
	ingress lanDisableSuccessorStartupAttester, snapshot appaccess.HostingGatewayStartupSnapshot,
) (appaccess.HostingGatewayStartupSnapshot, error) {
	if ctx == nil || repository == nil || ingress == nil {
		return appaccess.HostingGatewayStartupSnapshot{}, errors.New("historical LAN successor attestation requires startup dependencies")
	}
	for attempted := 0; attempted < len(snapshot.Disables.Claims); attempted++ {
		index, successor, relation, found := nextHistoricalLANDisableSuccessor(snapshot)
		if !found {
			return snapshot, nil
		}
		disable := snapshot.Disables.Claims[index]
		requests := lanDisableStartupClaims(snapshot.Disables)
		request := requests[index].Request
		successorRequest := gatewayLANGrantRequest(successor.Claim)
		var acknowledgment appaccess.AppAccessDisableSuccessorAck
		err := ingress.AttestGatewayV2LANDisableSuccessor(ctx, request, successorRequest, relation,
			lanGrantStartupClaims(snapshot.Grants), requests,
			func(lockCtx context.Context, proof generatedingress.GatewayV2LANDisableSuccessorObservation) error {
				if !reflect.DeepEqual(proof.Request, request) || proof.Successor != successorRequest ||
					proof.Relation != relation || proof.GatewayOperationID == "" ||
					proof.ProtectedStateDigest == "" || proof.ObservedAt.IsZero() {
					return appaccess.ErrInvalidStoredState
				}
				current, readErr := repository.HostingGatewayStartupSnapshot(lockCtx)
				if readErr != nil || !reflect.DeepEqual(current, snapshot) {
					return firstGatewayStartupError(readErr)
				}
				acknowledgment, _, readErr = repository.AcknowledgeAppAccessDisableSuccessor(lockCtx,
					disable.Claim.OperationID, proof.GatewayOperationID,
					successor.Claim.AttemptID, successor.Claim.Spec.AllocationID,
					relation, proof.ProtectedStateDigest, proof.ObservedAt)
				return readErr
			})
		if err != nil {
			return appaccess.HostingGatewayStartupSnapshot{}, err
		}
		confirmed, err := repository.HostingGatewayStartupSnapshot(ctx)
		if err != nil || index >= len(confirmed.Disables.Claims) {
			return appaccess.HostingGatewayStartupSnapshot{}, firstGatewayStartupError(err)
		}
		snapshot.Disables.Claims[index].SuccessorAck = &acknowledgment
		if !reflect.DeepEqual(confirmed, snapshot) {
			return appaccess.HostingGatewayStartupSnapshot{}, appaccess.ErrInvalidStoredState
		}
		snapshot = confirmed
	}
	if _, _, _, found := nextHistoricalLANDisableSuccessor(snapshot); found {
		return appaccess.HostingGatewayStartupSnapshot{}, appaccess.ErrInvalidStoredState
	}
	return snapshot, nil
}

func firstGatewayStartupError(err error) error {
	if err != nil {
		return err
	}
	return appaccess.ErrInvalidStoredState
}

func nextHistoricalLANDisableSuccessor(snapshot appaccess.HostingGatewayStartupSnapshot) (
	disableIndex int, successor appaccess.AppAccessGrantStartupClaim, relation string, found bool,
) {
	for index, disable := range snapshot.Disables.Claims {
		if disable.Claim.State != appaccess.AppAccessDisableCommitted || disable.Claim.Proof == nil ||
			disable.ProtectedClearAck != nil || disable.SuccessorAck != nil {
			continue
		}
		var sameApp *appaccess.AppAccessGrantStartupClaim
		for _, grant := range snapshot.Grants.Claims {
			if grant.Claim.State != appaccess.AppAccessGrantCommitted || grant.Claim.RetiredAt != nil ||
				grant.Allocation.State != appaccess.AllocationActive || grant.Allocation.ReleasedAt != nil ||
				grant.AppArchived || !grant.AccessHeadCurrent || !grant.ProfileHeadCurrent ||
				!grant.ApproverIsAdministrator || grant.DisableIntent != nil ||
				grant.Claim.Spec.AllocationID == disable.Claim.Spec.AllocationID ||
				grant.Claim.Spec.OwnerOperationID == disable.Claim.Spec.OwnerOperationID ||
				grant.Claim.Spec.GatewayProfileRevisionID != disable.Claim.Spec.GatewayProfileRevisionID ||
				grant.Claim.Spec.GatewayProfileRevisionNumber != disable.Claim.Spec.GatewayProfileRevisionNumber ||
				!grant.Claim.CreatedAt.After(disable.Claim.Proof.ObservedAt) {
				continue
			}
			if grant.Claim.Spec.AppID == disable.Claim.Spec.AppID &&
				(grant.Claim.Spec.AccessRevisionNumber <= disable.Claim.Spec.AccessRevisionNumber ||
					grant.Claim.Spec.AccessRevisionID == disable.Claim.Spec.AccessRevisionID) {
				continue
			}
			if grant.Claim.Spec.Port == disable.Claim.Spec.Port {
				return index, grant, generatedingress.GatewayV2LANSuccessorSamePort, true
			}
			if grant.Claim.Spec.AppID == disable.Claim.Spec.AppID {
				candidate := grant
				sameApp = &candidate
			}
		}
		if sameApp != nil {
			return index, *sameApp, generatedingress.GatewayV2LANSuccessorSameAppNewPort404, true
		}
	}
	return 0, appaccess.AppAccessGrantStartupClaim{}, "", false
}
