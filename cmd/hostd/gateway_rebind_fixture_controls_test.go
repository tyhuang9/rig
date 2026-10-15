//go:build live_docker

package main

import (
	"errors"
	"strings"
	"testing"
)

func TestLiveHostdRebindClosedDiagnosticControls(t *testing.T) {
	const secret = "token=do-not-disclose"
	knownRuntime := `{"time":"2026-10-15T00:00:00Z","level":"ERROR","msg":"runtime composition failed","error":"` + secret + `"}`
	knownGateway := `{"msg":"gateway startup inspection failed","error":"/private/path ` + secret + `"}`
	knownGatewayCheckpoint := `{"msg":"gateway startup inspection failed","startup_checkpoint":"upgrade_gateway_lock","error":"/private/path ` + secret + `"}`
	unknownGatewayCheckpoint := `{"msg":"gateway startup inspection failed","startup_checkpoint":"forged-checkpoint","error":"/private/path ` + secret + `"}`
	secondary := `{"msg":"owned gateway emergency stop could not be verified","error":"` + secret + `"}`
	cases := []struct {
		name      string
		stderr    string
		wantCode  string
		wantEmpty bool
	}{
		{name: "known exact message", stderr: knownRuntime, wantCode: "runtime_composition"},
		{name: "primary precedes emergency stop", stderr: knownGateway + "\n" + secondary, wantCode: "gateway_inspection"},
		{name: "known gateway checkpoint", stderr: knownGatewayCheckpoint, wantCode: "upgrade_gateway_lock"},
		{name: "unknown gateway checkpoint keeps broad code", stderr: unknownGatewayCheckpoint, wantCode: "gateway_inspection"},
		{name: "numeric gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":42,"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "object gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":{"value":"upgrade_gateway_lock"},"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "array gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":["upgrade_gateway_lock"],"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "boolean gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":true,"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "null gateway checkpoint keeps broad code", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":null,"error":"/private/path ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "malformed gateway checkpoint retains first primary", stderr: `{"msg":"gateway startup inspection failed","startup_checkpoint":42,"error":"/private/path ` + secret + `"}` + "\n" + knownRuntime, wantCode: "gateway_inspection"},
		{name: "gateway checkpoint retains first primary", stderr: knownGatewayCheckpoint + "\n" + knownRuntime, wantCode: "upgrade_gateway_lock"},
		{name: "primary follows emergency stop", stderr: secondary + "\n" + knownRuntime, wantCode: "runtime_composition"},
		{name: "first primary is retained", stderr: knownGateway + "\n" + knownRuntime, wantCode: "gateway_inspection"},
		{name: "checkpoint is ignored for another message", stderr: `{"msg":"runtime composition failed","startup_checkpoint":"upgrade_gateway_lock","error":"` + secret + `"}`, wantCode: "runtime_composition"},
		{name: "raw error cannot manufacture checkpoint", stderr: `{"msg":"gateway startup inspection failed","error":"upgrade_gateway_lock ` + secret + `"}`, wantCode: "gateway_inspection"},
		{name: "unknown message", stderr: `{"msg":"custom startup failed ` + secret + `","error":"` + secret + `"}`},
		{name: "injected known message", stderr: `{"msg":"runtime composition failed; ` + secret + `","error":"` + secret + `"}`},
		{name: "malformed JSON", stderr: `{"msg":"runtime composition failed","error":"` + secret},
		{name: "empty stderr", stderr: " \n\t", wantEmpty: true},
		{
			name:   "known message after byte limit",
			stderr: strings.Repeat("x", liveHostdRebindStartupDiagnosticMaxBytes) + "\n" + knownRuntime,
		},
		{
			name:   "known message after record limit",
			stderr: strings.Repeat(`{"msg":"unknown `+secret+`"}`+"\n", liveHostdRebindStartupDiagnosticMaxRecords) + knownRuntime,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := liveHostdRebindClosedDiagnostic(test.stderr)
			if test.wantEmpty {
				if got != "empty" {
					t.Fatalf("closed diagnostic=%q want empty", got)
				}
				return
			}
			if !strings.HasPrefix(got, "sha256:") {
				t.Fatalf("closed diagnostic omitted digest: %q", got)
			}
			if strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
				t.Fatalf("closed diagnostic exposed untrusted stderr: %q", got)
			}
			code := " startup=" + test.wantCode
			if test.wantCode == "" {
				if strings.Contains(got, " startup=") {
					t.Fatalf("closed diagnostic classified an untrusted record: %q", got)
				}
				return
			}
			if !strings.HasSuffix(got, code) {
				t.Fatalf("closed diagnostic=%q want code %q", got, test.wantCode)
			}
		})
	}

}

func TestLiveHostdRebindClosedDiagnosticCheckpointRoster(t *testing.T) {
	const secret = "token=do-not-disclose"
	for _, checkpoint := range gatewayStartupDiagnosticExpectedCheckpoints {
		t.Run(string(checkpoint), func(t *testing.T) {
			stderr := `{"msg":"gateway startup inspection failed","startup_checkpoint":"` + string(checkpoint) +
				`","error":"/private/path ` + secret + `"}`
			got := liveHostdRebindClosedDiagnostic(stderr)
			if !strings.HasPrefix(got, "sha256:") || strings.Contains(got, secret) || strings.Contains(got, "/private/path") {
				t.Fatalf("closed diagnostic exposed untrusted stderr: %q", got)
			}
			if want := " startup=" + string(checkpoint); !strings.HasSuffix(got, want) {
				t.Fatalf("closed diagnostic=%q want code %q", got, checkpoint)
			}
		})
	}
}

func TestLiveHostdRebindLegacyV1OwnershipControls(t *testing.T) {
	resources := liveHostdRebindLegacyV1Resources{
		containerID:      strings.Repeat("a", 64),
		networkID:        strings.Repeat("b", 64),
		configVolumeName: liveHostdRebindLegacyV1ConfigVolume,
	}
	containerLabels := map[string]string{
		"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.listener-isolation": "v1",
	}
	networkLabels := map[string]string{
		"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v1",
	}
	volumeLabels := map[string]string{
		"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1",
	}
	cases := []struct {
		name       string
		kind       string
		id         string
		nameID     string
		labels     map[string]string
		want       bool
		uncaptured bool
	}{
		{
			name: "exact captured container", kind: "container", id: resources.containerID,
			nameID: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, want: true,
		},
		{
			name: "exact captured network", kind: "network", id: resources.networkID,
			nameID: liveHostdRebindLegacyV1NetworkName, labels: networkLabels, want: true,
		},
		{
			name: "exact captured config volume", kind: "volume", id: liveHostdRebindLegacyV1ConfigVolume,
			nameID: liveHostdRebindLegacyV1ConfigVolume, labels: volumeLabels, want: true,
		},
		{
			name: "changed container identity", kind: "container", id: strings.Repeat("c", 64),
			nameID: liveHostdRebindLegacyV1ContainerName, labels: containerLabels,
		},
		{
			name: "foreign canonical lookalike", kind: "network", id: strings.Repeat("d", 64),
			nameID: liveHostdRebindLegacyV1NetworkName, labels: networkLabels,
		},
		{
			name: "foreign name with captured identity", kind: "container", id: resources.containerID,
			nameID: "foreign-caddy", labels: containerLabels,
		},
		{
			name: "malformed identity label", kind: "network", id: resources.networkID,
			nameID: liveHostdRebindLegacyV1NetworkName,
			labels: map[string]string{"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v2"},
		},
		{
			name: "missing container listener label", kind: "container", id: resources.containerID,
			nameID: liveHostdRebindLegacyV1ContainerName,
			labels: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"},
		},
		{
			name: "changed config volume name", kind: "volume", id: "other-volume",
			nameID: "other-volume", labels: volumeLabels,
		},
		{
			name: "zero capture denies removal", kind: "container", id: resources.containerID,
			nameID: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, uncaptured: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			captured := resources
			if test.uncaptured {
				captured = liveHostdRebindLegacyV1Resources{}
			}
			if got := captured.matches(test.kind, test.id, test.nameID, test.labels); got != test.want {
				t.Fatalf("legacy v1 ownership=%t want=%t", got, test.want)
			}
			identity, allowed := captured.removalIdentityFromInspection(test.kind, test.id, test.nameID, test.labels, test.labels, nil)
			wantIdentity := ""
			if test.want {
				wantIdentity = test.id
			}
			if allowed != test.want || identity != wantIdentity {
				t.Fatalf("removal authority=(%q, %t) want=(%q, %t)", identity, allowed, wantIdentity, test.want)
			}
		})
	}

	for _, test := range []struct {
		name       string
		kind       string
		id         string
		resource   string
		labels     map[string]string
		inspected  map[string]string
		inspectErr error
		want       liveHostdRebindLegacyV1CleanupDecision
	}{
		{
			name: "delegate exact captured resource", kind: "container", id: resources.containerID,
			resource: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, inspected: containerLabels,
			want: liveHostdRebindLegacyV1Delegate,
		},
		{
			name: "retain uncaptured legacy resource", kind: "container", id: strings.Repeat("e", 64),
			resource: liveHostdRebindLegacyV1ContainerName, labels: containerLabels, inspected: containerLabels,
			want: liveHostdRebindLegacyV1Retain,
		},
		{
			name: "retain replaced legacy network", kind: "network", id: strings.Repeat("f", 64),
			resource: liveHostdRebindLegacyV1NetworkName, labels: networkLabels, inspected: networkLabels,
			want: liveHostdRebindLegacyV1Retain,
		},
		{
			name: "retain missing inspected identity", kind: "volume", labels: volumeLabels,
			inspectErr: errors.New("identity unavailable"), want: liveHostdRebindLegacyV1Retain,
		},
		{
			name: "retain label inspection disagreement", kind: "container", id: resources.containerID,
			resource: liveHostdRebindLegacyV1ContainerName, labels: containerLabels,
			inspected: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"},
			want:      liveHostdRebindLegacyV1Retain,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resources.cleanupDecisionFromInspection(test.kind, test.id, test.resource, test.labels, test.inspected, test.inspectErr)
			if got != test.want {
				t.Fatalf("legacy cleanup decision=%d want=%d", got, test.want)
			}
			identity, allowed := resources.removalIdentityFromInspection(test.kind, test.id, test.resource, test.labels, test.inspected, test.inspectErr)
			wantAllowed := test.want == liveHostdRebindLegacyV1Delegate
			wantIdentity := ""
			if wantAllowed {
				wantIdentity = test.id
			}
			if allowed != wantAllowed || identity != wantIdentity {
				t.Fatalf("removal authority=(%q, %t) want=(%q, %t)", identity, allowed, wantIdentity, wantAllowed)
			}
		})
	}
}
