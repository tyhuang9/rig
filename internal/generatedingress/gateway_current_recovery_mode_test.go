package generatedingress

import (
	"reflect"
	"testing"
)

func TestGatewayCurrentRecoveryModePreservesMarkers(t *testing.T) {
	for _, test := range []struct {
		name  string
		state gatewayCurrentRouteState
		want  GatewayCurrentRecoveryMode
	}{
		{"stable", gatewayCurrentRouteState{}, GatewayCurrentRecoveryStable},
		{"legacy route", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{}}, GatewayCurrentRecoveryRoute},
		{"route", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{Kind: gatewayV2PendingRouteSwitch}}, GatewayCurrentRecoveryRoute},
		{"grant", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANGrant}}, GatewayCurrentRecoveryLAN},
		{"withdrawal", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANWithdrawal}}, GatewayCurrentRecoveryLAN},
		{"disable", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANDisable}}, GatewayCurrentRecoveryLAN},
		{"batch", gatewayCurrentRouteState{LANRecovery: &gatewayCurrentLANRecoveryBatch{Items: []gatewayCurrentLANRecoveryItem{{}}}}, GatewayCurrentRecoveryLANBatch},
		{"completed batch", gatewayCurrentRouteState{LANRecovery: &gatewayCurrentLANRecoveryBatch{Head: 1, Items: []gatewayCurrentLANRecoveryItem{{}}, LegacyPending: &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANDisable}}}, GatewayCurrentRecoveryLANBatchDone},
		{"unknown pending", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{Kind: "future"}}, ""},
		{"empty batch", gatewayCurrentRouteState{LANRecovery: &gatewayCurrentLANRecoveryBatch{}}, ""},
		{"negative head", gatewayCurrentRouteState{LANRecovery: &gatewayCurrentLANRecoveryBatch{Head: -1, Items: []gatewayCurrentLANRecoveryItem{{}}}}, ""},
		{"overrun head", gatewayCurrentRouteState{LANRecovery: &gatewayCurrentLANRecoveryBatch{Head: 2, Items: []gatewayCurrentLANRecoveryItem{{}}}}, ""},
		{"crossed markers", gatewayCurrentRouteState{Pending: &gatewayCurrentPendingRoute{}, LANRecovery: &gatewayCurrentLANRecoveryBatch{Items: []gatewayCurrentLANRecoveryItem{{}}}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := cloneGatewayCurrentRouteState(test.state)
			// The clone normalizes a nil Apps map; match that normalization in
			// the input so this assertion concerns only classifier mutation.
			test.state = cloneGatewayCurrentRouteState(test.state)
			mode, err := gatewayCurrentRecoveryMode(test.state)
			if mode != test.want || (err != nil) != (test.want == "") || !reflect.DeepEqual(before, test.state) {
				t.Fatalf("mode=%s want=%s error=%v mutated=%t", mode, test.want, err, !reflect.DeepEqual(before, test.state))
			}
		})
	}
}
