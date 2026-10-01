package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

type successorStartupRepositoryFake struct {
	read func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
	ack  func(context.Context, string, string, string, string, string, string, time.Time) (appaccess.AppAccessDisableSuccessorAck, bool, error)
}

func (f successorStartupRepositoryFake) HostingGatewayStartupSnapshot(ctx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
	return f.read(ctx)
}

func (f successorStartupRepositoryFake) AcknowledgeAppAccessDisableSuccessor(ctx context.Context,
	operationID, gatewayID, attemptID, allocationID, relation, digest string, observedAt time.Time,
) (appaccess.AppAccessDisableSuccessorAck, bool, error) {
	return f.ack(ctx, operationID, gatewayID, attemptID, allocationID, relation, digest, observedAt)
}

type successorStartupAttesterFake struct {
	attest func(context.Context, generatedingress.GatewayV2LANDisableRequest,
		generatedingress.GatewayV2LANGrantRequest, string,
		[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
		func(context.Context, generatedingress.GatewayV2LANDisableSuccessorObservation) error) error
}

func (f successorStartupAttesterFake) AttestGatewayV2LANDisableSuccessor(ctx context.Context,
	request generatedingress.GatewayV2LANDisableRequest, successor generatedingress.GatewayV2LANGrantRequest,
	relation string, grants []generatedingress.GatewayV2LANStartupClaim,
	disables []generatedingress.GatewayV2LANDisableStartupClaim,
	ack func(context.Context, generatedingress.GatewayV2LANDisableSuccessorObservation) error,
) error {
	return f.attest(ctx, request, successor, relation, grants, disables, ack)
}

func historicalSuccessorStartupSnapshot() (appaccess.HostingGatewayStartupSnapshot, time.Time) {
	observedAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	return appaccess.HostingGatewayStartupSnapshot{
		Disables: appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{{
			Claim: appaccess.AppAccessDisableClaim{
				OperationID: "old-disable", State: appaccess.AppAccessDisableCommitted,
				RequestDigest: "disable-request", SpecDigest: "disable-spec",
				Spec: appaccess.AppAccessDisableSpec{
					AppID: "old-app", AllocationID: "old-allocation", OwnerOperationID: "old-owner",
					AccessRevisionID: "old-revision", AccessRevisionNumber: 1,
					GatewayProfileRevisionID: "profile", GatewayProfileRevisionNumber: 1, Port: 8100,
				},
				Proof: &appaccess.AppAccessDisableProof{ObservedAt: observedAt},
			},
		}}},
		Grants: appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{{
			Claim: appaccess.AppAccessGrantClaim{
				AttemptID: "new-grant", RequestDigest: "grant-request",
				State: appaccess.AppAccessGrantCommitted, CreatedAt: observedAt.Add(time.Second),
				Spec: appaccess.AppAccessGrantSpec{
					AppID: "new-app", AllocationID: "new-allocation", OwnerOperationID: "new-owner",
					AccessRevisionID: "new-revision", AccessRevisionNumber: 1,
					GatewayProfileRevisionID: "profile", GatewayProfileRevisionNumber: 1, Port: 8100,
				},
			},
			Allocation:        appaccess.Allocation{State: appaccess.AllocationActive},
			AccessHeadCurrent: true, ProfileHeadCurrent: true, ApproverIsAdministrator: true,
		}}},
	}, observedAt.Add(2 * time.Second)
}

func cloneSuccessorStartupSnapshot(value appaccess.HostingGatewayStartupSnapshot) appaccess.HostingGatewayStartupSnapshot {
	value.Upgrades.Claims = append([]appaccess.GatewayUpgradeStartupClaim(nil), value.Upgrades.Claims...)
	value.Grants.Claims = append([]appaccess.AppAccessGrantStartupClaim(nil), value.Grants.Claims...)
	value.Disables.Claims = append([]appaccess.AppAccessDisableStartupClaim(nil), value.Disables.Claims...)
	return value
}

func TestAttestHistoricalLANDisableSuccessorAcknowledgesOnlyExactSnapshot(t *testing.T) {
	initial, observedAt := historicalSuccessorStartupSnapshot()
	stored := cloneSuccessorStartupSnapshot(initial)
	var calls []string
	reads := 0
	acknowledgment := appaccess.AppAccessDisableSuccessorAck{
		OperationID: "old-disable", GatewayOperationID: "gateway-operation",
		SuccessorAttemptID: "new-grant", SuccessorAllocationID: "new-allocation",
		Relation:                  generatedingress.GatewayV2LANSuccessorSamePort,
		FinalProtectedStateDigest: "final-digest", ObservedAt: observedAt,
		AcknowledgedAt: observedAt.Add(time.Second),
	}
	repository := successorStartupRepositoryFake{
		read: func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
			reads++
			calls = append(calls, "read")
			if reads == 1 && !reflect.DeepEqual(stored, initial) {
				t.Fatal("repository changed before the locked pre-ack snapshot read")
			}
			return cloneSuccessorStartupSnapshot(stored), nil
		},
		ack: func(_ context.Context, operationID, gatewayID, attemptID, allocationID, relation, digest string, at time.Time) (appaccess.AppAccessDisableSuccessorAck, bool, error) {
			calls = append(calls, "ack")
			if reads != 1 || operationID != acknowledgment.OperationID || gatewayID != acknowledgment.GatewayOperationID ||
				attemptID != acknowledgment.SuccessorAttemptID || allocationID != acknowledgment.SuccessorAllocationID ||
				relation != acknowledgment.Relation || digest != acknowledgment.FinalProtectedStateDigest || !at.Equal(observedAt) {
				t.Fatalf("ack input or ordering mismatch: reads=%d operation=%q gateway=%q attempt=%q allocation=%q relation=%q digest=%q at=%v",
					reads, operationID, gatewayID, attemptID, allocationID, relation, digest, at)
			}
			stored.Disables.Claims[0].SuccessorAck = &acknowledgment
			return acknowledgment, true, nil
		},
	}
	attester := successorStartupAttesterFake{attest: func(ctx context.Context,
		request generatedingress.GatewayV2LANDisableRequest, successor generatedingress.GatewayV2LANGrantRequest,
		relation string, grants []generatedingress.GatewayV2LANStartupClaim,
		disables []generatedingress.GatewayV2LANDisableStartupClaim,
		ack func(context.Context, generatedingress.GatewayV2LANDisableSuccessorObservation) error,
	) error {
		calls = append(calls, "attest")
		if len(grants) != 1 || len(disables) != 1 || !reflect.DeepEqual(request, disables[0].Request) ||
			request.OperationID != "old-disable" || request.Port != 8100 ||
			successor != grants[0].Request || successor.AttemptID != "new-grant" ||
			relation != generatedingress.GatewayV2LANSuccessorSamePort || disables[0].ClearAcknowledged {
			t.Fatalf("attester received wrong startup census: request=%#v successor=%#v relation=%q grants=%#v disables=%#v",
				request, successor, relation, grants, disables)
		}
		return ack(ctx, generatedingress.GatewayV2LANDisableSuccessorObservation{
			Request: request, Successor: successor, Relation: relation,
			GatewayOperationID:   acknowledgment.GatewayOperationID,
			ProtectedStateDigest: acknowledgment.FinalProtectedStateDigest, ObservedAt: observedAt,
		})
	}}
	got, err := attestHistoricalLANDisableSuccessors(context.Background(), repository, attester, initial)
	if err != nil || !reflect.DeepEqual(got, stored) ||
		!reflect.DeepEqual(calls, []string{"attest", "read", "ack", "read"}) || reads != 2 {
		t.Fatalf("startup attestation result=%#v error=%v calls=%v reads=%d", got, err, calls, reads)
	}
	if got.Disables.Claims[0].ProtectedClearAck != nil || got.Disables.Claims[0].SuccessorAck == nil {
		t.Fatalf("successor acknowledgment changed wrong snapshot fields: %#v", got.Disables.Claims[0])
	}
}

func TestAttestHistoricalLANDisableSuccessorFailsClosed(t *testing.T) {
	initial, observedAt := historicalSuccessorStartupSnapshot()
	for _, test := range []struct {
		name        string
		invalid     bool
		preChanged  bool
		ackError    bool
		postChanged bool
		wantReads   int
		wantAcks    int
	}{
		{name: "wrong attestation", invalid: true},
		{name: "snapshot changed before ack", preChanged: true, wantReads: 1},
		{name: "durable ack rejected", ackError: true, wantReads: 1, wantAcks: 1},
		{name: "snapshot changed after ack", postChanged: true, wantReads: 2, wantAcks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads, acks := 0, 0
			stored := cloneSuccessorStartupSnapshot(initial)
			repository := successorStartupRepositoryFake{
				read: func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
					reads++
					result := cloneSuccessorStartupSnapshot(stored)
					if test.preChanged && reads == 1 || test.postChanged && reads == 2 {
						result.Grants.Claims[0].AccessHeadCurrent = false
					}
					return result, nil
				},
				ack: func(_ context.Context, _, _, _, _, _, _ string, _ time.Time) (appaccess.AppAccessDisableSuccessorAck, bool, error) {
					acks++
					if test.ackError {
						return appaccess.AppAccessDisableSuccessorAck{}, false, errors.New("durable ack failed")
					}
					value := appaccess.AppAccessDisableSuccessorAck{OperationID: "old-disable", SuccessorAttemptID: "new-grant"}
					stored.Disables.Claims[0].SuccessorAck = &value
					return value, true, nil
				},
			}
			attester := successorStartupAttesterFake{attest: func(ctx context.Context,
				request generatedingress.GatewayV2LANDisableRequest, successor generatedingress.GatewayV2LANGrantRequest,
				relation string, _ []generatedingress.GatewayV2LANStartupClaim,
				_ []generatedingress.GatewayV2LANDisableStartupClaim,
				ack func(context.Context, generatedingress.GatewayV2LANDisableSuccessorObservation) error,
			) error {
				if test.invalid {
					successor.AttemptID = "wrong-successor"
				}
				return ack(ctx, generatedingress.GatewayV2LANDisableSuccessorObservation{
					Request: request, Successor: successor, Relation: relation,
					GatewayOperationID: "gateway-operation", ProtectedStateDigest: "final-digest", ObservedAt: observedAt,
				})
			}}
			got, err := attestHistoricalLANDisableSuccessors(context.Background(), repository, attester,
				cloneSuccessorStartupSnapshot(initial))
			if err == nil || !reflect.DeepEqual(got, appaccess.HostingGatewayStartupSnapshot{}) ||
				reads != test.wantReads || acks != test.wantAcks {
				t.Fatalf("failure must prevent startup: result=%#v error=%v reads=%d acks=%d", got, err, reads, acks)
			}
		})
	}
}
