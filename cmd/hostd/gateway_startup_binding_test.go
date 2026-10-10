package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

// These tests exercise the startup adapter's projection contract. The DTOs
// simulate a validated SQL snapshot; protected lineage validation is a separate
// runtime responsibility and is not established by these mapping tests.
func TestLANStartupMappingPreservesRawProfileAcrossTransfers(t *testing.T) {
	grant, disable := startupBindingMappingFixture()
	profile, source, chain := startupBindingMappingAuthority()
	grant.EffectiveProfile, grant.CurrentGatewaySource, grant.TransferChain = profile, source, chain
	grant.TransferChainTipDigest, grant.TerminalReceiptDigest = chain[1].TransferDigest, source.TerminalReceiptDigest
	disable.EffectiveProfile, disable.CurrentGatewaySource, disable.TransferChain = profile, source, chain
	disable.TransferChainTipDigest, disable.TerminalReceiptDigest = grant.TransferChainTipDigest, grant.TerminalReceiptDigest
	grants := lanGrantStartupClaims(appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{grant}})
	disables := lanDisableStartupClaims(appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{disable}})
	if len(grants) != 1 || len(disables) != 1 {
		t.Fatal("startup mapping lost claims")
	}
	if grants[0].Request.GatewayProfileRevisionID != "raw-profile-a" || grants[0].Request.GatewayProfileRevisionNumber != 1 ||
		grants[0].Request.GatewayProfileSpecDigest != grant.Profile.SpecDigest ||
		disables[0].Request.GatewayProfileRevisionID != "raw-profile-a" || disables[0].Request.GatewayProfileRevisionNumber != 1 ||
		disables[0].Request.GatewayProfileSpecDigest != disable.Profile.SpecDigest ||
		disables[0].Request.SourceGrant == nil || *disables[0].Request.SourceGrant != grants[0].Request {
		t.Fatal("effective authority replaced immutable raw request provenance")
	}
	for _, projection := range []*generatedingress.GatewayV2LANStartupBindingProjection{grants[0].CurrentBinding, disables[0].CurrentBinding} {
		if projection == nil || projection.EffectiveProfile.RevisionID != "effective-profile-c" ||
			projection.EffectiveProfile.SelectedIPv4 != profile.Spec.SelectedIPv4 ||
			projection.GatewaySource != source || !reflect.DeepEqual(projection.TransferChain, chain) ||
			projection.TransferChainTipDigest != chain[1].TransferDigest || projection.TerminalReceiptDigest != source.TerminalReceiptDigest {
			t.Fatal("effective binding lost its complete authority or transfer chain")
		}
	}
	if grants[0].RetainedBinding != nil || disables[0].RetainedBinding != nil {
		t.Fatal("current authority acquired a historical projection")
	}
	grants[0].CurrentBinding.TransferChain[0].TransferDigest = "changed"
	*grants[0].CurrentBinding.TransferChain[1].PredecessorTransferDigest = "changed"
	if chain[0].TransferDigest == "changed" || *chain[1].PredecessorTransferDigest == "changed" ||
		disables[0].CurrentBinding.TransferChain[0].TransferDigest == "changed" ||
		*disables[0].CurrentBinding.TransferChain[1].PredecessorTransferDigest == "changed" {
		t.Fatal("startup projection shares mutable transfer evidence with another snapshot")
	}
}

func TestLANStartupMappingKeepsRetainedHistorySeparate(t *testing.T) {
	grant, disable := startupBindingMappingFixture()
	profile, source, chain := startupBindingMappingAuthority()
	retiredAt := time.Unix(10, 0).UTC()
	grant.Claim.RetiredAt, grant.Claim.RetiredByDisableOperationID = &retiredAt, disable.Claim.OperationID
	grant.EffectiveProfile = appaccess.GatewayProfileRevision{}
	grant.RetainedEffectiveProfile, grant.RetainedGatewaySource, grant.RetainedTransferChain = profile, source, chain
	grant.RetainedTransferChainTipDigest, grant.RetainedTerminalReceiptDigest = chain[1].TransferDigest, source.TerminalReceiptDigest
	disable.Claim.State = appaccess.AppAccessDisableCommitted
	disable.SourceGrant = &grant.Claim
	disable.EffectiveProfile = appaccess.GatewayProfileRevision{}
	disable.RetainedEffectiveProfile, disable.RetainedGatewaySource, disable.RetainedTransferChain = profile, source, chain
	disable.RetainedTransferChainTipDigest, disable.RetainedTerminalReceiptDigest = grant.RetainedTransferChainTipDigest, grant.RetainedTerminalReceiptDigest
	g := lanGrantStartupClaims(appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{grant}})[0]
	d := lanDisableStartupClaims(appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{disable}})[0]
	if g.CurrentBinding != nil || d.CurrentBinding != nil || g.RetainedBinding == nil || d.RetainedBinding == nil ||
		g.RetainedBinding.GatewaySource != source || !reflect.DeepEqual(g.RetainedBinding, d.RetainedBinding) {
		t.Fatal("retained transfer evidence was lost or promoted to current authority")
	}
}

func TestLANStartupMappingPreservesPartialAndMixedEvidence(t *testing.T) {
	grant, disable := startupBindingMappingFixture()
	// Preserve contradictory and partial input for the runtime's fail-closed
	// validator. Silently dropping either side would hide a malformed snapshot.
	grant.CurrentGatewaySource.OperationID = "partial-current"
	grant.RetainedTerminalReceiptDigest = "partial-retained"
	disable.CurrentGatewaySource = grant.CurrentGatewaySource
	disable.RetainedTerminalReceiptDigest = grant.RetainedTerminalReceiptDigest
	g := lanGrantStartupClaims(appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{grant}})[0]
	d := lanDisableStartupClaims(appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{disable}})[0]
	for _, pair := range [][2]*generatedingress.GatewayV2LANStartupBindingProjection{{g.CurrentBinding, g.RetainedBinding}, {d.CurrentBinding, d.RetainedBinding}} {
		if pair[0] == nil || pair[1] == nil || pair[0].GatewaySource.OperationID != "partial-current" ||
			pair[1].TerminalReceiptDigest != "partial-retained" {
			t.Fatal("mapping silently discarded malformed authority evidence")
		}
	}
}

func TestLANStartupMappingDoesNotPromoteRawProfilesToAuthority(t *testing.T) {
	grant, disable := startupBindingMappingFixture()
	grant.Claim.State = appaccess.AppAccessGrantPrepared
	g := lanGrantStartupClaims(appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{grant}})[0]
	d := lanDisableStartupClaims(appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{disable}})[0]
	if g.CurrentBinding != nil || g.RetainedBinding != nil || d.CurrentBinding != nil || d.RetainedBinding != nil {
		t.Fatal("raw-only legacy snapshot acquired invented effective authority")
	}
}

func startupBindingMappingFixture() (appaccess.AppAccessGrantStartupClaim, appaccess.AppAccessDisableStartupClaim) {
	profile := appaccess.GatewayProfileRevision{ID: "raw-profile-a", RevisionNumber: 1, SpecDigest: strings.Repeat("a", 64)}
	claim := appaccess.AppAccessGrantClaim{AttemptID: "grant-a", State: appaccess.AppAccessGrantCommitted,
		Spec: appaccess.AppAccessGrantSpec{AppID: "application", AllocationID: "allocation", AccessRevisionID: "access",
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber, GatewayProfileSpecDigest: profile.SpecDigest}}
	grant := appaccess.AppAccessGrantStartupClaim{Claim: claim, Profile: profile, EffectiveProfile: profile,
		AccessHeadCurrent: true, ProfileHeadCurrent: true, ApproverIsAdministrator: true}
	disable := appaccess.AppAccessDisableStartupClaim{Profile: profile, EffectiveProfile: profile, SourceGrant: &claim,
		AccessHeadCurrent: true, ProfileHeadCurrent: true, ApproverIsAdministrator: true,
		Claim: appaccess.AppAccessDisableClaim{OperationID: "disable-a", State: appaccess.AppAccessDisablePrepared,
			Spec: appaccess.AppAccessDisableSpec{AppID: "application", AllocationID: "allocation", AccessRevisionID: "access",
				GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}}}
	return grant, disable
}

func startupBindingMappingAuthority() (appaccess.GatewayProfileRevision, appaccess.GatewayCurrentAuthorityRef, []appaccess.GatewayRebindAllocationTransfer) {
	profile := appaccess.GatewayProfileRevision{ID: "effective-profile-c", RevisionNumber: 3,
		SpecDigest: strings.Repeat("c", 64), Spec: appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.1.12", InterfaceID: "ethernet", PortStart: 8100, PortEnd: 8119}}
	source := appaccess.GatewayCurrentAuthorityRef{Kind: appaccess.GatewayRebindSourceGatewayRebind, OperationID: "rebind-c",
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
		TerminalReceiptDigest: strings.Repeat("d", 64)}
	first := strings.Repeat("1", 64)
	chain := []appaccess.GatewayRebindAllocationTransfer{
		{SourceProfileRevisionID: "raw-profile-a", SuccessorProfileRevisionID: "effective-profile-b", TransferDigest: first},
		{SourceProfileRevisionID: "raw-profile-a", SuccessorProfileRevisionID: profile.ID, PredecessorTransferDigest: &first, TransferDigest: strings.Repeat("2", 64)},
	}
	return profile, source, chain
}
