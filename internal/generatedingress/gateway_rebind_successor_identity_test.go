package generatedingress

import (
	"math"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindSuccessorIdentityTestOperation = "11111111-1111-4111-8111-111111111111"
	gatewayRebindSuccessorIdentityTestProfileID = "22222222-2222-4222-8222-222222222222"
	gatewayRebindSuccessorIdentityTestSpec      = "9af7b194082076361b806355b9bd29c71ccc773306367be46ac0885de44893d6"
)

func gatewayRebindSuccessorIdentityTestProfile() GatewayRebindSuccessorProfile {
	return GatewayRebindSuccessorProfile{
		RevisionID: gatewayRebindSuccessorIdentityTestProfileID, RevisionNumber: 7,
		OperationID: "33333333-3333-4333-8333-333333333333", RequestDigest: strings.Repeat("a", 64),
		SpecDigest: gatewayRebindSuccessorIdentityTestSpec, SelectedIPv4: "192.168.97.8",
		InterfaceID: "rebind-successor", PortStart: 8100, PortEnd: 8119,
		ApprovedBy: "44444444-4444-4444-8444-444444444444",
	}
}

func TestGatewayRebindSuccessorIdentityGoldenVector(t *testing.T) {
	profile := gatewayRebindSuccessorIdentityTestProfile()
	// Independent SHA-256 vectors over the exact compact JSON tuples
	// ["v2-rebind-1",1,"11111111-1111-4111-8111-111111111111","f"] and
	// ["v2-rebind-1",1,"11111111-1111-4111-8111-111111111111","s"].
	const finalHostname = "rig-v2r-f-8a58c57ca57aa081cc7023b2c459fe0b"
	const stageHostname = "rig-v2r-s-6fa7e7a7e82188e5b424ee16291d3f6a"
	const identityDigest = "eef8f04cdf3759dc7dee1a7ee5e32ccc5ac1c19114d740643a84886ad867af3f"
	const generation = "-g00000000000000000001-" + gatewayRebindSuccessorIdentityTestOperation
	identity, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version != gatewayRebindSuccessorIdentityVersion ||
		identity.FinalContainer != "rig-generated-caddy-v2"+generation ||
		identity.StageContainer != "rig-generated-caddy-v2-stage"+generation ||
		identity.ConfigVolume != "rig-generated-caddy-config-v2"+generation ||
		identity.DataVolume != "rig-generated-caddy-data-v2"+generation ||
		identity.IngressNetwork != "rig-generated-caddy-ingress-v2"+generation ||
		identity.FinalHostname != finalHostname || identity.StageHostname != stageHostname ||
		identity.Digest != identityDigest || identity.CaddyImageDigest != gatewayV2CaddyImageDigest ||
		identity.StageConfigFilename != "stage.json" || identity.ActiveConfigFilename != "active.json" ||
		identity.ProfileRevisionID != profile.RevisionID || identity.ProfileRevisionNumber != profile.RevisionNumber ||
		identity.ProfileSpecDigest != profile.SpecDigest {
		t.Fatalf("successor identity departed from golden vector: %+v", identity)
	}
	if len(identity.FinalHostname) != 42 || len(identity.StageHostname) != 42 ||
		identity.FinalHostname == identity.StageHostname ||
		!validGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile, identity) {
		t.Fatal("successor hostnames or binding invalid")
	}
	second, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil || second != identity {
		t.Fatalf("successor identity was nondeterministic: identity=%+v second=%+v err=%v", identity, second, err)
	}
}

func TestGatewayRebindSuccessorIdentityGenerationAndScope(t *testing.T) {
	profile := gatewayRebindSuccessorIdentityTestProfile()
	first, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		generation uint64
		operation  string
		profile    GatewayRebindSuccessorProfile
	}{
		{"later generation", 2, gatewayRebindSuccessorIdentityTestOperation, profile},
		{"different operation", 1, "55555555-5555-4555-8555-555555555555", profile},
		{"different profile revision", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			changed := profile
			changed.RevisionID = "66666666-6666-4666-8666-666666666666"
			return changed
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			other, err := newGatewayRebindSuccessorIdentity(test.generation, test.operation, test.profile)
			if err != nil || other.Digest == first.Digest ||
				validGatewayRebindSuccessorIdentity(test.generation, test.operation, test.profile, first) {
				t.Fatalf("successor identity did not bind scope: other=%+v err=%v", other, err)
			}
		})
	}
	last, err := newGatewayRebindSuccessorIdentity(math.MaxUint64, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil || !strings.Contains(last.FinalContainer, "-g18446744073709551615-") ||
		len(last.FinalHostname) != 42 || len(last.StageHostname) != 42 ||
		!validGatewayRebindSuccessorIdentity(math.MaxUint64, gatewayRebindSuccessorIdentityTestOperation, profile, last) {
		t.Fatalf("maximum generation failed: identity=%+v err=%v", last, err)
	}
}

func TestGatewayRebindSuccessorIdentityRejectsMutationsEvenWithRecomputedDigest(t *testing.T) {
	profile := gatewayRebindSuccessorIdentityTestProfile()
	original, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		mutate func(*gatewayRebindSuccessorIdentity)
	}{
		{"legacy version", func(v *gatewayRebindSuccessorIdentity) { v.Version = gatewayV2IdentityVersion }},
		{"other version", func(v *gatewayRebindSuccessorIdentity) { v.Version = "v2-rebind-2" }},
		{"zero generation", func(v *gatewayRebindSuccessorIdentity) { v.Generation = 0 }},
		{"other generation", func(v *gatewayRebindSuccessorIdentity) { v.Generation = 2 }},
		{"other operation", func(v *gatewayRebindSuccessorIdentity) { v.OperationID = "55555555-5555-4555-8555-555555555555" }},
		{"profile ID", func(v *gatewayRebindSuccessorIdentity) { v.ProfileRevisionID = "66666666-6666-4666-8666-666666666666" }},
		{"profile number", func(v *gatewayRebindSuccessorIdentity) { v.ProfileRevisionNumber++ }},
		{"profile digest", func(v *gatewayRebindSuccessorIdentity) { v.ProfileSpecDigest = strings.Repeat("b", 64) }},
		{"image", func(v *gatewayRebindSuccessorIdentity) { v.CaddyImageDigest = "sha256:" + strings.Repeat("c", 64) }},
		{"final fixed name", func(v *gatewayRebindSuccessorIdentity) { v.FinalContainer = gatewayV2ContainerName }},
		{"stage", func(v *gatewayRebindSuccessorIdentity) { v.StageContainer += "-copy" }},
		{"swapped containers", func(v *gatewayRebindSuccessorIdentity) {
			v.FinalContainer, v.StageContainer = v.StageContainer, v.FinalContainer
		}},
		{"config volume", func(v *gatewayRebindSuccessorIdentity) { v.ConfigVolume = gatewayV2ConfigVolumeName }},
		{"data volume", func(v *gatewayRebindSuccessorIdentity) { v.DataVolume = gatewayV2DataVolumeName }},
		{"swapped volumes", func(v *gatewayRebindSuccessorIdentity) {
			v.ConfigVolume, v.DataVolume = v.DataVolume, v.ConfigVolume
		}},
		{"ingress network", func(v *gatewayRebindSuccessorIdentity) { v.IngressNetwork = gatewayV2NetworkName }},
		{"final hostname", func(v *gatewayRebindSuccessorIdentity) { v.FinalHostname += "a" }},
		{"stage hostname", func(v *gatewayRebindSuccessorIdentity) { v.StageHostname += "a" }},
		{"swapped hostnames", func(v *gatewayRebindSuccessorIdentity) {
			v.FinalHostname, v.StageHostname = v.StageHostname, v.FinalHostname
		}},
		{"stage config", func(v *gatewayRebindSuccessorIdentity) { v.StageConfigFilename = "other.json" }},
		{"active config", func(v *gatewayRebindSuccessorIdentity) { v.ActiveConfigFilename = "other.json" }},
		{"digest", func(v *gatewayRebindSuccessorIdentity) { v.Digest = strings.Repeat("d", 64) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			test.mutate(&changed)
			if changed.Digest == original.Digest {
				changed.Digest = ""
				recomputed, err := canonicalDigest(changed)
				if err != nil {
					t.Fatal(err)
				}
				changed.Digest = recomputed
			}
			if validGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile, changed) {
				t.Fatal("mutated identity was accepted")
			}
			if test.name != "profile ID" && test.name != "profile number" && test.name != "profile digest" && test.name != "digest" {
				if _, err := gatewayRebindSuccessorIdentityDigest(changed); err == nil {
					t.Fatal("noncanonical resource or format was digestible")
				}
			}
		})
	}
}

func TestGatewayRebindSuccessorIdentityRejectsChangedProfileAndInvalidInputs(t *testing.T) {
	profile := gatewayRebindSuccessorIdentityTestProfile()
	identity, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil {
		t.Fatal(err)
	}
	changed := profile
	changed.SelectedIPv4 = "192.168.97.9"
	changed.SpecDigest, err = appaccess.GatewayProfileSpecDigest(appaccess.GatewayProfileSpec{
		SelectedIPv4: changed.SelectedIPv4, InterfaceID: changed.InterfaceID,
		PortStart: changed.PortStart, PortEnd: changed.PortEnd,
	})
	if err != nil || validGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, changed, identity) {
		t.Fatalf("old identity accepted self-consistent changed profile: err=%v", err)
	}
	for _, test := range []struct {
		name       string
		generation uint64
		operation  string
		profile    GatewayRebindSuccessorProfile
	}{
		{"zero generation", 0, gatewayRebindSuccessorIdentityTestOperation, profile},
		{"empty operation", 1, "", profile},
		{"uppercase operation", 1, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", profile},
		{"zero profile", 1, gatewayRebindSuccessorIdentityTestOperation, GatewayRebindSuccessorProfile{}},
		{"uppercase revision ID", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			value := profile
			value.RevisionID = "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
			return value
		}()},
		{"wrong profile spec digest", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			value := profile
			value.SpecDigest = strings.Repeat("b", 64)
			return value
		}()},
		{"invalid port range", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			value := profile
			value.PortEnd = value.PortStart - 1
			return value
		}()},
		{"missing configure operation", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			value := profile
			value.OperationID = ""
			return value
		}()},
		{"missing configure digest", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			value := profile
			value.RequestDigest = ""
			return value
		}()},
		{"missing configure approver", 1, gatewayRebindSuccessorIdentityTestOperation, func() GatewayRebindSuccessorProfile {
			value := profile
			value.ApprovedBy = ""
			return value
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newGatewayRebindSuccessorIdentity(test.generation, test.operation, test.profile); err == nil {
				t.Fatal("invalid successor identity input was accepted")
			}
		})
	}
}

func TestGatewayRebindSuccessorIdentityDoesNotAuthenticateConfigureApproval(t *testing.T) {
	profile := gatewayRebindSuccessorIdentityTestProfile()
	identity, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*GatewayRebindSuccessorProfile)
	}{
		{"configure operation", func(value *GatewayRebindSuccessorProfile) {
			value.OperationID = "55555555-5555-4555-8555-555555555555"
		}},
		{"configure request digest", func(value *GatewayRebindSuccessorProfile) {
			value.RequestDigest = strings.Repeat("b", 64)
		}},
		{"configure approver", func(value *GatewayRebindSuccessorProfile) {
			value.ApprovedBy = "66666666-6666-4666-8666-666666666666"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := profile
			test.mutate(&changed)
			rebuilt, err := newGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, changed)
			if err != nil || rebuilt != identity ||
				!validGatewayRebindSuccessorIdentity(1, gatewayRebindSuccessorIdentityTestOperation, changed, identity) {
				t.Fatalf("configure approval unexpectedly entered identity: rebuilt=%+v err=%v", rebuilt, err)
			}
		})
	}
}

func TestGatewayRebindSuccessorHostnameRejectsInvalidTuple(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation uint64
		operation  string
		role       string
	}{
		{"zero generation", 0, gatewayRebindSuccessorIdentityTestOperation, "f"},
		{"noncanonical operation", 1, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "f"},
		{"invalid role", 1, gatewayRebindSuccessorIdentityTestOperation, "final"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := gatewayRebindSuccessorHostname(test.generation, test.operation, test.role); err == nil {
				t.Fatal("invalid hostname tuple was accepted")
			}
		})
	}
}
