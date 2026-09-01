package generatedingress

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestStateStoreRejectsPlaintextBeyondProtectedBundleLimit(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := routeState{Version: stateVersion, Active: make(map[string]routeRecord, maxStateApps)}
	for index := 0; index < maxStateApps; index++ {
		appID := fmt.Sprintf("11111111-1111-4111-8111-%012x", index+1)
		state.Active[appID] = routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
			endpoint(strings.Repeat("a", 63)+"1", "server", "n"+strings.Repeat("a", 94), "a"+strings.Repeat("b", 94), 3000, 'a'),
			endpoint(strings.Repeat("a", 63)+"2", "static", "n"+strings.Repeat("a", 94), "a"+strings.Repeat("c", 94), 4173, 'b'),
		}}
	}
	if err := store.save(state); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v", err)
	}
}
