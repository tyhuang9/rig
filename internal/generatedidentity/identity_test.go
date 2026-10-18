package generatedidentity

import "testing"

func TestGeneratedIdentityCanonicalGoldens(t *testing.T) {
	const appID = "11111111-1111-4111-8111-111111111111"
	description, err := DescribeInactiveCandidate(appID, "api", SlotBlue)
	if err != nil {
		t.Fatal(err)
	}
	if description.Slot != SlotGreen || description.NetworkName != "rig-a-11111111111141118111111111111111" ||
		description.ContainerName != "rig-a-11111111111141118111111111111111-c-14c2529eb449-green" ||
		description.NetworkAlias != "rig-c-14c2529eb449-green" {
		t.Fatalf("canonical generated identity changed: %#v", description)
	}
	network, err := DescribeAppNetwork(appID)
	if err != nil || network.Name != description.NetworkName {
		t.Fatalf("canonical network identity=%#v error=%v", network, err)
	}
	if first, err := InactiveSlot(""); err != nil || first != SlotBlue {
		t.Fatalf("first slot=%q error=%v", first, err)
	}
	if _, err := DescribeInactiveCandidate(appID, " api", SlotBlue); err == nil {
		t.Fatal("noncanonical component identity accepted")
	}
}
