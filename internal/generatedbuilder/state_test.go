package generatedbuilder

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityStrictRoundTripPreservesIndependentNames(t *testing.T) {
	identity := Identity{Schema: StateSchema,
		BuilderName: "rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa",
		NodeName:    "rig-node-bbbbbbbbbbbbbbbbbbbbbbbb",
		NetworkName: "rig-buildnet-cccccccccccccccccccccccc"}
	body, err := EncodeIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeIdentity(body)
	if err != nil || decoded != identity {
		t.Fatalf("decoded identity = %#v, %v", decoded, err)
	}
	if got := ContainerName(decoded); got != "rig-buildkitd-bbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("derived container name = %q", got)
	}
	generated, err := NewIdentity()
	if err != nil || !ValidIdentity(generated) {
		t.Fatalf("generated identity = %#v, %v", generated, err)
	}

	for name, mutate := range map[string]func(*Identity){
		"schema":        func(value *Identity) { value.Schema-- },
		"short builder": func(value *Identity) { value.BuilderName = "rig-buildkit-short" },
		"node prefix":   func(value *Identity) { value.NodeName = "rig-buildkit-bbbbbbbbbbbbbbbbbbbbbbbb" },
		"network case":  func(value *Identity) { value.NetworkName = "rig-buildnet-CCCCCCCCCCCCCCCCCCCCCCCC" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := identity
			mutate(&invalid)
			if ValidIdentity(invalid) {
				t.Fatalf("invalid identity was accepted: %#v", invalid)
			}
			if _, err := EncodeIdentity(invalid); err == nil {
				t.Fatal("invalid identity was encoded")
			}
		})
	}

	for name, body := range map[string][]byte{
		"unknown field":         []byte(`{"schema":2,"builderName":"rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa","nodeName":"rig-node-bbbbbbbbbbbbbbbbbbbbbbbb","networkName":"rig-buildnet-cccccccccccccccccccccccc","extra":true}`),
		"case-folded duplicate": []byte(`{"schema":2,"Schema":2,"builderName":"rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa","nodeName":"rig-node-bbbbbbbbbbbbbbbbbbbbbbbb","networkName":"rig-buildnet-cccccccccccccccccccccccc"}`),
		"trailing value":        append(append([]byte(nil), body...), []byte(` {}`)...),
		"oversized":             []byte(`{"schema":2,"builderName":"rig-buildkit-` + strings.Repeat("a", int(IdentityMaximumBytes)) + `"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeIdentity(body); err == nil {
				t.Fatal("invalid identity JSON was accepted")
			}
		})
	}
}

func TestObservationDecodersAreStrictAndBounded(t *testing.T) {
	image := Image{ID: "sha256:" + strings.Repeat("a", 64), Labels: map[string]string{"owner": "external"}}
	body, err := json.Marshal(image)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeImage(body)
	if err != nil || !reflect.DeepEqual(decoded, image) {
		t.Fatalf("decoded image = %#v, %v", decoded, err)
	}
	for name, invalid := range map[string][]byte{
		"unknown field":         []byte(`{"Id":"sha256:` + strings.Repeat("a", 64) + `","Labels":{},"Raw":"private"}`),
		"case-folded duplicate": []byte(`{"Id":"sha256:` + strings.Repeat("a", 64) + `","id":"sha256:` + strings.Repeat("a", 64) + `","Labels":{}}`),
		"trailing value":        append(append([]byte(nil), body...), []byte(` null`)...),
		"oversized":             []byte(`{"Id":"sha256:` + strings.Repeat("a", 64) + `","Labels":{"padding":"` + strings.Repeat("x", int(ObservationMaximumBytes)) + `"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeImage(invalid); err == nil {
				t.Fatal("invalid image observation was accepted")
			}
		})
	}
}

func TestNetworkMatcherPreservesWriterCompatibility(t *testing.T) {
	identity := Identity{Schema: StateSchema,
		BuilderName: "rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa",
		NodeName:    "rig-node-bbbbbbbbbbbbbbbbbbbbbbbb",
		NetworkName: "rig-buildnet-cccccccccccccccccccccccc"}
	network := Network{Name: identity.NetworkName, Driver: "bridge", Scope: "local",
		Labels: NetworkLabels(identity), Options: map[string]string{
			"com.docker.network.bridge.enable_icc": "false", "external.option": "retained"}}
	network.Labels["external.label"] = "retained"
	if !MatchesNetwork(network, identity) {
		t.Fatal("writer-compatible unrelated network metadata was rejected")
	}
	network.Internal = true
	if MatchesNetwork(network, identity) {
		t.Fatal("internal generated-builder network was accepted")
	}
}
