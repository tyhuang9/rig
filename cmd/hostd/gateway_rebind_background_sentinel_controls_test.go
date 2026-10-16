//go:build live_docker

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/controllerrelay"
	"github.com/hostd/hostd/internal/database"
)

func TestLiveHostdRebindBackgroundSentinelControls(t *testing.T) {
	t.Run("new sentinel preserves exact durable expiry", func(t *testing.T) {
		fixture := &liveHostdGatewayRebindFixture{dataRoot: t.TempDir()}
		fixture.user.ID = "background-sentinel-control-owner"
		db, err := database.Open(fixture.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
			VALUES(?,?,'fixture-hash','administrator',?,?)`, fixture.user.ID, "background-sentinel-control",
			"2024-01-02T03:04:05Z", "2024-01-02T03:04:05Z")
		closeErr := db.Close()
		if err != nil || closeErr != nil {
			t.Fatal("could not initialize the sentinel control owner")
		}
		sentinel := liveHostdRebindSeedBackgroundSentinels(t, fixture)
		got := liveHostdRebindReadBackgroundSentinel(t, fixture, sentinel)
		expectedTime, expectedErr := time.Parse(time.RFC3339Nano, sentinel.autoLeaseExpires)
		storedTime, storedErr := time.Parse(time.RFC3339Nano, got.autoExpiry)
		if expectedErr != nil || storedErr != nil || !expectedTime.Equal(storedTime) {
			t.Fatal("seeded and stored lease expiry instants differ")
		}
		if got.autoExpiry != sentinel.autoLeaseExpires {
			t.Fatal("fresh untouched sentinel fails exact expiry comparison before any child starts")
		}
		liveHostdRebindAssertBackgroundSentinelUnchanged(t, fixture, sentinel)
		for _, testCase := range []struct {
			name   string
			mutate func(*liveHostdRebindBackgroundReadback)
		}{
			{name: "disabled", mutate: func(v *liveHostdRebindBackgroundReadback) { v.autoStatus.Enabled = false }},
			{name: "lease fence", mutate: func(v *liveHostdRebindBackgroundReadback) { v.autoStatus.LeaseFence++ }},
			{name: "lease token", mutate: func(v *liveHostdRebindBackgroundReadback) { v.autoToken = "private-token" }},
			{name: "lease expiry", mutate: func(v *liveHostdRebindBackgroundReadback) { v.autoExpiry = "private-expiry" }},
			{name: "same instant different representation", mutate: func(v *liveHostdRebindBackgroundReadback) { v.autoExpiry = storedTime.Format(time.RFC3339Nano) }},
			{name: "relay epoch", mutate: func(v *liveHostdRebindBackgroundReadback) { v.relay.Epoch++ }},
			{name: "relay fence", mutate: func(v *liveHostdRebindBackgroundReadback) { v.relay.Fence++ }},
			{name: "relay state", mutate: func(v *liveHostdRebindBackgroundReadback) { v.relay.State = controllerrelay.SessionNeedsAttention }},
			{name: "keys", mutate: func(v *liveHostdRebindBackgroundReadback) { v.keys = 1 }},
			{name: "credentials", mutate: func(v *liveHostdRebindBackgroundReadback) { v.credentials = 1 }},
			{name: "credential issues", mutate: func(v *liveHostdRebindBackgroundReadback) { v.issues = 1 }},
		} {
			t.Run("rejects "+testCase.name, func(t *testing.T) {
				changed := got
				testCase.mutate(&changed)
				if liveHostdRebindBackgroundReadbackUnchanged(changed, sentinel) {
					t.Fatal("changed sentinel was accepted")
				}
				diagnostic := liveHostdRebindBackgroundUnchangedDiagnostic(changed, sentinel)
				if !strings.Contains(diagnostic, "=false") || strings.Contains(diagnostic, "private-") ||
					strings.Contains(diagnostic, sentinel.autoLeaseToken) || strings.Contains(diagnostic, sentinel.autoApplicationID) {
					t.Fatal("sentinel failure diagnostic omitted the mismatch or disclosed private fields")
				}
			})
		}
	})
}
