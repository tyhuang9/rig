package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func TestLiveCrossStoreAdmissionChildInputJSONRoundTripPreservesPreparedInspection(t *testing.T) {
	_, input, _ := newGatewayRebindCoordinatorFixture(t)
	if len(input.Inspection.Roster) != 1 || input.Inspection.Roster[0].EntryDigest == "" {
		t.Fatal("real SQL proposal fixture has no derived roster digest")
	}

	decoded := liveCrossStoreAdmissionChildDecodedInput(t, input)
	restored, err := liveCrossStoreAdmissionChildInput(decoded)
	if err != nil || !reflect.DeepEqual(restored, input) {
		t.Fatal("child JSON did not restore the exact prepared inspection")
	}

	t.Run("rejects missing manifest", func(t *testing.T) {
		value := liveCrossStoreAdmissionChildDecodedInput(t, input)
		value.Input.Inspection.Roster = nil
		if _, err := liveCrossStoreAdmissionChildInput(value); err == nil {
			t.Fatal("missing roster manifest was accepted")
		}
	})
	t.Run("rejects count mismatch", func(t *testing.T) {
		value := liveCrossStoreAdmissionChildDecodedInput(t, input)
		value.Input.Inspection.Spec.RosterCount++
		liveCrossStoreAdmissionChildRefreshSpecDigest(t, &value.Input)
		if _, err := liveCrossStoreAdmissionChildInput(value); err == nil {
			t.Fatal("wrong roster count was accepted")
		}
	})
	t.Run("rejects mutated manifest", func(t *testing.T) {
		value := liveCrossStoreAdmissionChildDecodedInput(t, input)
		value.Input.Inspection.Roster[0].AppID = uuid.NewString()
		if _, err := liveCrossStoreAdmissionChildInput(value); err == nil {
			t.Fatal("mutated roster manifest was accepted")
		}
	})
	t.Run("rejects aggregate digest mismatch", func(t *testing.T) {
		value := liveCrossStoreAdmissionChildDecodedInput(t, input)
		value.Input.Inspection.Spec.RosterDigest = strings.Repeat("a", 64)
		liveCrossStoreAdmissionChildRefreshSpecDigest(t, &value.Input)
		if _, err := liveCrossStoreAdmissionChildInput(value); err == nil {
			t.Fatal("wrong approved roster digest was accepted")
		}
	})
	t.Run("accepts canonical and rejects reordered manifest", func(t *testing.T) {
		canonical := liveCrossStoreAdmissionChildTwoEntryInput(t, input)
		value := liveCrossStoreAdmissionChildDecodedInput(t, canonical)
		restored, err := liveCrossStoreAdmissionChildInput(value)
		if err != nil || !reflect.DeepEqual(restored, canonical) {
			t.Fatal("canonical two-entry manifest did not round trip")
		}
		value.Input.Inspection.Roster[0], value.Input.Inspection.Roster[1] =
			value.Input.Inspection.Roster[1], value.Input.Inspection.Roster[0]
		if _, err := liveCrossStoreAdmissionChildInput(value); err == nil {
			t.Fatal("reordered roster manifest was accepted")
		}
	})
}

func liveCrossStoreAdmissionChildDecodedInput(t *testing.T, input gatewayRebindCommitInput) liveCrossStoreAdmissionChild {
	t.Helper()
	body, err := json.Marshal(liveCrossStoreAdmissionChild{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	var value liveCrossStoreAdmissionChild
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func liveCrossStoreAdmissionChildRefreshSpecDigest(t *testing.T, input *gatewayRebindCommitInput) {
	t.Helper()
	digest, err := appaccess.GatewayRebindSpecV2Digest(input.Inspection.Spec)
	if err != nil {
		t.Fatal(err)
	}
	input.Inspection.SpecDigest = digest
}

func liveCrossStoreAdmissionChildTwoEntryInput(t *testing.T, input gatewayRebindCommitInput) gatewayRebindCommitInput {
	t.Helper()
	roster := append([]appaccess.GatewayRebindRosterEntryV2(nil), input.Inspection.Roster...)
	second := roster[0]
	second.Ordinal = 2
	second.AppID = uuid.NewString()
	second.AllocationID = uuid.NewString()
	second.Port++
	second.AllocationOwnerOperationID = uuid.NewString()
	second.AccessRevisionID = uuid.NewString()
	second.GrantAttemptID = uuid.NewString()
	second.ServingDeploymentID = uuid.NewString()
	second.ServingReleaseID = uuid.NewString()
	second.EntryDigest = ""
	digest, err := appaccess.GatewayRebindRosterEntryV2Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	second.EntryDigest = digest
	roster = append(roster, second)
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(roster)
	if err != nil {
		t.Fatal(err)
	}
	input.Inspection.Roster = roster
	input.Inspection.Spec.RosterCount = int64(len(roster))
	input.Inspection.Spec.RosterDigest = rosterDigest
	liveCrossStoreAdmissionChildRefreshSpecDigest(t, &input)
	return input
}

func TestLiveGatewayRebindRuntimeLoopbackSnapshotUsesZeroHeadOperatorState(t *testing.T) {
	fixture, _, _ := newGatewayRebindCoordinatorFixture(t)
	ctx := context.Background()
	loopbackID := uuid.NewString()
	if _, err := fixture.db.ExecContext(ctx, `INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, loopbackID, "loopback-"+loopbackID, "Loopback-only fixture app"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.CurrentAppAccess(ctx, loopbackID); !errors.Is(err, appaccess.ErrNotFound) {
		t.Fatalf("zero access head CurrentAppAccess error=%v", err)
	}
	snapshot, err := liveGatewayRebindRuntimeLoopbackSnapshot(ctx, fixture.repository, loopbackID)
	if err != nil || !reflect.DeepEqual(snapshot, appaccess.AppAccessOperatorSnapshot{}) {
		t.Fatalf("zero access head operator snapshot=%#v error=%v", snapshot, err)
	}
	profile, err := fixture.repository.CurrentGatewayProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
		AppID: loopbackID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	}); err != nil || !created {
		t.Fatalf("reserve unexpected loopback LAN state: created=%t error=%v", created, err)
	}
	if _, err := liveGatewayRebindRuntimeLoopbackSnapshot(ctx, fixture.repository, loopbackID); err == nil {
		t.Fatal("reserved loopback-only application was accepted")
	}
}

func TestLiveGatewayRebindRuntimeLANBodyRejectsUnexpectedResponses(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		expected string
		want     bool
	}{
		{name: "accepts exact body", status: http.StatusOK, body: "application", expected: "application", want: true},
		{name: "rejects wrong body", status: http.StatusOK, body: "other", expected: "application"},
		{name: "rejects redirect", status: http.StatusFound, body: "redirect", expected: "application"},
		{name: "rejects non-ok", status: http.StatusNotFound, body: "application", expected: "application"},
		{name: "rejects overlimit body", status: http.StatusOK, body: strings.Repeat("x", 4097), expected: strings.Repeat("x", 4097)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			host, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.ParseUint(rawPort, 10, 16)
			if err != nil {
				t.Fatal(err)
			}
			if got := liveGatewayRebindRuntimeLANBody(context.Background(), host, uint16(port), test.expected); got != test.want {
				t.Fatalf("accepted=%t status=%d", got, test.status)
			}
		})
	}
}
