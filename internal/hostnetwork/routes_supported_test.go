//go:build linux || windows

package hostnetwork

import "testing"

func TestCurrentIPv4NetworkSnapshotSmoke(t *testing.T) {
	snapshot, err := CurrentIPv4NetworkSnapshot()
	if err != nil {
		t.Fatalf("CurrentIPv4NetworkSnapshot: %v", err)
	}
	if !snapshot.complete {
		t.Fatal("CurrentIPv4NetworkSnapshot returned an incomplete snapshot without error")
	}
	if err := validateIPv4NetworkSnapshot(snapshot); err != nil {
		t.Fatalf("CurrentIPv4NetworkSnapshot returned invalid data: %v", err)
	}
}
