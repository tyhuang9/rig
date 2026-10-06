package controller

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

type effectiveRecoveryRuntime struct {
	*lanGrantRuntimeFake
	proof generatedingress.GatewayV2LANEffectiveBindingProof
}

func (r *effectiveRecoveryRuntime) WithGatewayV2LANCommitRecovery(ctx context.Context,
	request generatedingress.GatewayV2LANGrantRequest,
	fn func(context.Context, generatedingress.GatewayV2LANGrantObservation) error,
) error {
	observed := r.observation(request, generatedingress.GatewayV2LANGrantWithdrawnPendingReconciliation)
	observed.EffectiveBinding = r.proof
	if err := fn(ctx, observed); err != nil {
		return err
	}
	r.quarantined = false
	return nil
}

type effectiveRecoveryAuthorization struct {
	LANAppGrantService
	alter func(*appaccess.AppAccessGrantAuthorization)
}

func (s effectiveRecoveryAuthorization) AuthorizeAppAccessGrant(ctx context.Context,
	input appaccess.AppAccessGrantAuthorizationInput,
) (appaccess.AppAccessGrantAuthorization, error) {
	value, err := s.LANAppGrantService.AuthorizeAppAccessGrant(ctx, input)
	if err == nil {
		s.alter(&value)
	}
	return value, err
}

// The raw claim and authorization checks use SQLite. Successor DTOs model the
// controller boundary; these tests do not establish SQL transfer or Docker proof.
func TestLANGrantCommittedRecoveryUsesEffectiveAuthority(t *testing.T) {
	for _, name := range []string{"two transfers", "native replay", "missing proof", "wrong source", "wrong observed operation", "wrong tip", "wrong receipt", "broken chain", "changed authorized claim", "raw operation substitution", "demoted approver"} {
		t.Run(name, func(t *testing.T) {
			f := newLANAccessFixture(t)
			revision := f.approveForGrant(t)
			runtime := &effectiveRecoveryRuntime{lanGrantRuntimeFake: &lanGrantRuntimeFake{commit: true, operationID: uuid.NewString()}}
			attempt := uuid.NewString()
			path := "/api/v1/apps/" + f.appID + "/lan-access/grants"
			created := relayAuthenticatedRequest(f.grantHandler(runtime.lanGrantRuntimeFake, false, ""), http.MethodPost, path, grantBody(revision, attempt))
			if created.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", created.Code, created.Body.String())
			}
			before, err := f.repository.AppAccessGrantClaim(context.Background(), attempt)
			if err != nil || before.Proof == nil {
				t.Fatalf("committed claim: %v", err)
			}
			profile := f.profile
			var chain []appaccess.GatewayRebindAllocationTransfer
			var predecessor *string
			var lineage appaccess.GatewayCurrentLineageRef
			for i := 0; i < 2; i++ {
				profile.ID, profile.OperationID = uuid.NewString(), uuid.NewString()
				profile.RevisionNumber++
				profile.Spec.SelectedIPv4 = "192.168.60.20"
				profile.Spec.InterfaceID = "8/Successor LAN"
				profile.SpecDigest, err = appaccess.GatewayProfileSpecDigest(profile.Spec)
				if err != nil {
					t.Fatal(err)
				}
				lineage = appaccess.GatewayCurrentLineageRef{Kind: appaccess.GatewayRebindSourceGatewayRebind,
					OperationID: uuid.NewString(), ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
					ProfileSpecDigest: profile.SpecDigest, ProtectedGeneration: uint64(i + 1),
					ProtectedIdentityDigest: strings.Repeat("c", 64), ProtectedIntentDigest: strings.Repeat("d", 64),
					TerminalReceiptDigest: strings.Repeat("e", 64)}
				transfer := appaccess.GatewayRebindAllocationTransfer{Version: appaccess.GatewayRebindTransferVersionV1,
					OperationID: lineage.OperationID, Ordinal: 1, AppID: f.appID, AllocationID: before.Spec.AllocationID,
					GrantAttemptID: attempt, SourceBindingDigest: strings.Repeat("a", 64), RosterEntryDigest: strings.Repeat("b", 64),
					SourceProfileRevisionID: f.profile.ID, SourceProfileRevisionNumber: f.profile.RevisionNumber, SourceProfileSpecDigest: f.profile.SpecDigest,
					PredecessorTransferDigest: predecessor, SuccessorProfileRevisionID: profile.ID,
					SuccessorProfileRevisionNumber: profile.RevisionNumber, SuccessorProfileSpecDigest: profile.SpecDigest,
					TerminalReceiptDigest: lineage.TerminalReceiptDigest}
				transfer.TransferDigest, err = appaccess.GatewayRebindAllocationTransferDigest(transfer)
				if err != nil {
					t.Fatal(err)
				}
				chain = append(chain, transfer)
				tip := transfer.TransferDigest
				predecessor = &tip
			}
			runtime.operationID, runtime.quarantined = lineage.OperationID, true
			runtime.proof = generatedingress.GatewayV2LANEffectiveBindingProof{
				EffectiveProfile: generatedingress.GatewayV2ProfileBinding{RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber,
					SpecDigest: profile.SpecDigest, SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
					PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd},
				ProtectedLineage: lineage, TransferChainTipDigest: *predecessor, TerminalReceiptDigest: lineage.TerminalReceiptDigest}
			var service LANAppGrantService = effectiveRecoveryAuthorization{LANAppGrantService: f.repository,
				alter: func(value *appaccess.AppAccessGrantAuthorization) {
					value.EffectiveProfile = profile
					value.CurrentGatewaySource = appaccess.GatewayCurrentAuthorityRef{Kind: lineage.Kind, OperationID: lineage.OperationID,
						ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
						ProfileSpecDigest: profile.SpecDigest, TerminalReceiptDigest: lineage.TerminalReceiptDigest}
					value.TransferChain = append([]appaccess.GatewayRebindAllocationTransfer(nil), chain...)
					value.TransferChainTipDigest, value.TerminalReceiptDigest = *predecessor, lineage.TerminalReceiptDigest
					if name == "changed authorized claim" {
						copy := *value.Claim.Proof
						copy.GatewayOperationID = lineage.OperationID
						value.Claim.Proof = &copy
					}
				}}
			switch name {
			case "native replay":
				service = f.repository
				runtime.operationID, runtime.proof = before.Proof.GatewayOperationID, generatedingress.GatewayV2LANEffectiveBindingProof{}
			case "missing proof":
				runtime.proof = generatedingress.GatewayV2LANEffectiveBindingProof{}
			case "wrong source":
				runtime.proof.ProtectedLineage.OperationID = uuid.NewString()
			case "wrong observed operation":
				runtime.operationID = uuid.NewString()
			case "wrong tip":
				runtime.proof.TransferChainTipDigest = strings.Repeat("f", 64)
			case "wrong receipt":
				runtime.proof.TerminalReceiptDigest = strings.Repeat("f", 64)
			case "broken chain":
				chain[1].PredecessorTransferDigest = nil
			case "raw operation substitution":
				runtime.operationID = before.Proof.GatewayOperationID
			case "demoted approver":
				if _, err := f.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, f.actorID); err != nil {
					t.Fatal(err)
				}
			}
			server := &Server{Auth: controllerAuthFake{user: auth.User{ID: f.actorID, Role: "administrator"}},
				Apps: f.applications, AppAccess: f.repository, AppGrants: service, LANGrantRuntime: runtime,
				GeneratedRuntime: true, Logger: relayTestLogger(), RecoveryOnly: true, RecoveryKind: RecoveryLANGrant,
				RecoveryOperationID: attempt, RecoveryAppID: f.appID}
			response := relayAuthenticatedRequest(server.Handler(), http.MethodPost, path, grantBody(revision, attempt))
			wantOK := name == "two transfers" || name == "native replay"
			if gotOK := response.Code == http.StatusOK; gotOK != wantOK || runtime.quarantined == wantOK {
				t.Fatalf("recovery: status=%d quarantined=%t body=%s", response.Code, runtime.quarantined, response.Body.String())
			}
			after, err := f.repository.AppAccessGrantClaim(context.Background(), attempt)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("recovery rewrote immutable committed claim or proof")
			}
		})
	}
}
