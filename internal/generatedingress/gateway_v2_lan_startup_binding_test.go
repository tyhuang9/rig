package generatedingress

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2LANStartupBindingValidatesRawAndEffectiveClaims(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	var raw GatewayV2LANGrantRequest
	var projection GatewayV2LANStartupBindingProjection
	for appID, app := range fixture.baseline.Apps {
		if app.LAN == nil {
			continue
		}
		raw = gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw)
		projection = GatewayV2LANStartupBindingProjection{
			EffectiveProfile:       GatewayV2ProfileBinding(fixture.baseline.Profile),
			GatewaySource:          gatewayCurrentAuthority(fixture.baseline.Lineage),
			TransferChain:          []appaccess.GatewayRebindAllocationTransfer{*app.LAN.Transfer},
			TransferChainTipDigest: app.LAN.Transfer.TransferDigest,
			TerminalReceiptDigest:  fixture.receipt.Digest,
		}
		break
	}
	if raw.AppID == "" {
		t.Fatal("fixture has no transferred grant")
	}
	nativeLineage, err := gatewayUpgradeCurrentLineage(fixture.history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	native := GatewayV2LANStartupBindingProjection{EffectiveProfile: GatewayV2ProfileBinding(fixture.predecessor.Profile),
		GatewaySource: gatewayCurrentAuthority(nativeLineage)}
	if !validGatewayV2LANStartupGrantProjection(native, raw) {
		t.Fatal("exact native upgrade projection refused")
	}
	nativeRebindRaw := raw
	nativeRebindRaw.GatewayProfileRevisionID = projection.EffectiveProfile.RevisionID
	nativeRebindRaw.GatewayProfileRevisionNumber = projection.EffectiveProfile.RevisionNumber
	nativeRebindRaw.GatewayProfileSpecDigest = projection.EffectiveProfile.SpecDigest
	nativeRebind := projection
	nativeRebind.TransferChain, nativeRebind.TransferChainTipDigest = nil, ""
	if !validGatewayV2LANStartupGrantProjection(nativeRebind, nativeRebindRaw) {
		t.Fatal("native grant on rebound gateway refused")
	}
	noSource := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, nativeRebindRaw),
		State: appaccess.AppAccessDisableWithdrawing, StateSequence: 2, CurrentBinding: &nativeRebind}
	noSource.Request.SourceGrant = nil
	if !validGatewayV2LANStartupDisableBindings(noSource) {
		t.Fatal("in-flight no-source disable with exact authority refused")
	}
	noSource.CurrentBinding = &projection
	if validGatewayV2LANStartupDisableBindings(noSource) {
		t.Fatal("no-source disable accepted a transfer chain")
	}
	claim := gatewayV2LANStartupClaim(raw, appaccess.AppAccessGrantCommitted, 4)
	claim.CurrentBinding = &projection
	if _, err := validateGatewayV2LANStartupClaims([]GatewayV2LANStartupClaim{claim}); err != nil {
		t.Fatalf("valid transferred claim refused: %v", err)
	}
	disable := GatewayV2LANDisableStartupClaim{Request: disableRequestForGrant(t, raw),
		State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: &projection}
	claim.DisableIntentOperationID = disable.Request.OperationID
	if _, err := validateGatewayV2LANAccessStartupClaims([]GatewayV2LANStartupClaim{claim}, []GatewayV2LANDisableStartupClaim{disable}); err != nil {
		t.Fatalf("exact source-grant projection refused: %v", err)
	}
	retainedGrant, retainedDisable := claim, disable
	retainedGrant.CurrentBinding, retainedGrant.RetainedBinding = nil, &projection
	retainedDisable.CurrentBinding, retainedDisable.RetainedBinding = nil, &projection
	retainedDisable.State, retainedDisable.StateSequence = appaccess.AppAccessDisableCommitted, 3
	if _, err := validateGatewayV2LANAccessStartupClaims([]GatewayV2LANStartupClaim{retainedGrant}, []GatewayV2LANDisableStartupClaim{retainedDisable}); err != nil {
		t.Fatalf("exact retained history refused: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*GatewayV2LANStartupBindingProjection)
	}{
		{"invalid profile", func(p *GatewayV2LANStartupBindingProjection) { p.EffectiveProfile.SelectedIPv4 = "127.0.0.1" }},
		{"source profile", func(p *GatewayV2LANStartupBindingProjection) { p.GatewaySource.ProfileRevisionNumber++ }},
		{"unknown source", func(p *GatewayV2LANStartupBindingProjection) { p.GatewaySource.Kind = "unknown" }},
		{"missing receipt", func(p *GatewayV2LANStartupBindingProjection) { p.TerminalReceiptDigest = "" }},
		{"missing chain", func(p *GatewayV2LANStartupBindingProjection) { p.TransferChain = nil; p.TransferChainTipDigest = "" }},
		{"wrong tip", func(p *GatewayV2LANStartupBindingProjection) { p.TransferChainTipDigest = strings.Repeat("f", 64) }},
		{"different app", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].AppID = "31313131-3131-4131-8131-313131313131"
		}},
		{"different allocation", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].AllocationID = "32323232-3232-4232-8232-323232323232"
		}},
		{"different grant", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].GrantAttemptID = "33333333-3333-4333-8333-333333333333"
		}},
		{"different raw profile", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].SourceProfileRevisionID = "34343434-3434-4434-8434-343434343434"
		}},
		{"different terminal", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].TerminalReceiptDigest = strings.Repeat("e", 64)
		}},
		{"different operation", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].OperationID = "35353535-3535-4535-8535-353535353535"
		}},
		{"different successor", func(p *GatewayV2LANStartupBindingProjection) {
			p.TransferChain[0].SuccessorProfileRevisionID = "36363636-3636-4636-8636-363636363636"
		}},
		{"unexpected predecessor", func(p *GatewayV2LANStartupBindingProjection) {
			previous := strings.Repeat("d", 64)
			p.TransferChain[0].PredecessorTransferDigest = &previous
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := projection
			value.TransferChain = append([]appaccess.GatewayRebindAllocationTransfer(nil), projection.TransferChain...)
			test.mutate(&value)
			// Recompute a mutated transfer so semantic refusals are not merely
			// stale checksum refusals. This is projected evidence, not live SQL.
			if len(value.TransferChain) > 0 && !reflect.DeepEqual(value.TransferChain, projection.TransferChain) {
				digest, err := appaccess.GatewayRebindAllocationTransferDigest(value.TransferChain[0])
				if err != nil {
					t.Fatal(err)
				}
				value.TransferChain[0].TransferDigest, value.TransferChainTipDigest = digest, digest
			}
			bad := claim
			bad.CurrentBinding = &value
			if _, err := validateGatewayV2LANStartupClaims([]GatewayV2LANStartupClaim{bad}); err == nil {
				t.Fatal("invalid effective claim accepted")
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*GatewayV2LANStartupClaim, *GatewayV2LANDisableStartupClaim)
	}{
		{"mixed grant", func(g *GatewayV2LANStartupClaim, d *GatewayV2LANDisableStartupClaim) {
			g.RetainedBinding = g.CurrentBinding
		}},
		{"mixed disable", func(g *GatewayV2LANStartupClaim, d *GatewayV2LANDisableStartupClaim) {
			d.RetainedBinding = d.CurrentBinding
		}},
		{"retained live grant", func(g *GatewayV2LANStartupClaim, d *GatewayV2LANDisableStartupClaim) {
			g.RetainedBinding, g.CurrentBinding = g.CurrentBinding, nil
		}},
		{"missing disable projection", func(g *GatewayV2LANStartupClaim, d *GatewayV2LANDisableStartupClaim) { d.CurrentBinding = nil }},
		{"prepared grant authority", func(g *GatewayV2LANStartupClaim, d *GatewayV2LANDisableStartupClaim) {
			g.State, g.StateSequence = appaccess.AppAccessGrantPrepared, 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, d := claim, disable
			test.mutate(&g, &d)
			if _, err := validateGatewayV2LANAccessStartupClaims([]GatewayV2LANStartupClaim{g}, []GatewayV2LANDisableStartupClaim{d}); err == nil {
				t.Fatal("mixed or missing authority accepted")
			}
		})
	}

	// A second transfer retains the original raw profile and links to the
	// first digest. This validates chain input only, not a physical A→B→C run.
	repeated := projection
	next := projection.TransferChain[0]
	previous := next.TransferDigest
	next.PredecessorTransferDigest = &previous
	next.OperationID = "37373737-3737-4737-8737-373737373737"
	next.SuccessorProfileRevisionID = "38383838-3838-4838-8838-383838383838"
	next.SuccessorProfileRevisionNumber++
	next.TerminalReceiptDigest = strings.Repeat("c", 64)
	next.TransferDigest, err = appaccess.GatewayRebindAllocationTransferDigest(next)
	if err != nil {
		t.Fatal(err)
	}
	repeated.TransferChain = append(append([]appaccess.GatewayRebindAllocationTransfer(nil), projection.TransferChain...), next)
	repeated.EffectiveProfile.RevisionID, repeated.EffectiveProfile.RevisionNumber = next.SuccessorProfileRevisionID, next.SuccessorProfileRevisionNumber
	repeated.GatewaySource.OperationID = next.OperationID
	repeated.GatewaySource.ProfileRevisionID, repeated.GatewaySource.ProfileRevisionNumber = next.SuccessorProfileRevisionID, next.SuccessorProfileRevisionNumber
	repeated.GatewaySource.TerminalReceiptDigest, repeated.TerminalReceiptDigest = next.TerminalReceiptDigest, next.TerminalReceiptDigest
	repeated.TransferChainTipDigest = next.TransferDigest
	if !validGatewayV2LANStartupGrantProjection(repeated, raw) {
		t.Fatal("valid repeated transfer chain refused")
	}
	repeated.TransferChain[0], repeated.TransferChain[1] = repeated.TransferChain[1], repeated.TransferChain[0]
	if validGatewayV2LANStartupGrantProjection(repeated, raw) {
		t.Fatal("reordered transfer chain accepted")
	}
}
