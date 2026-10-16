//go:build live_docker

package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/secretfile"
)

const liveHostdRebindNativeRoutePurpose = "hostd/generated-ingress/routes/v2"

type liveHostdRebindNativeLANBinding struct {
	GrantAttemptID        string `json:"grantAttemptId"`
	GrantRequestDigest    string `json:"grantRequestDigest"`
	OwnerOperationID      string `json:"ownerOperationId"`
	AccessRevisionID      string `json:"accessRevisionId"`
	AccessRevisionNumber  int64  `json:"accessRevisionNumber"`
	AccessSpecDigest      string `json:"accessSpecDigest"`
	AllocationID          string `json:"allocationId"`
	Port                  uint16 `json:"port"`
	ProfileRevisionID     string `json:"profileRevisionId"`
	ProfileRevisionNumber int64  `json:"profileRevisionNumber"`
	ProfileSpecDigest     string `json:"profileSpecDigest"`
	ApprovedBy            string `json:"approvedBy"`
}

// GatewayV2LANGrantRequest predates the protected-state JSON structs and uses
// encoding/json's exported Go field names. Keep this decoder separate from the
// lower-camel gatewayV2LANBinding wire shape above.
type liveHostdRebindNativeGrantRequest struct {
	AttemptID string `json:"AttemptID"`
}

type liveHostdRebindNativeDisable struct {
	OperationID      string                             `json:"operationId"`
	AppID            string                             `json:"appId"`
	AllocationID     string                             `json:"allocationId"`
	OwnerOperationID string                             `json:"ownerOperationId"`
	Port             uint16                             `json:"port"`
	AccessRevisionID string                             `json:"accessRevisionId"`
	SourceGrant      *liveHostdRebindNativeGrantRequest `json:"sourceGrant,omitempty"`
}

type liveHostdRebindNativeProtectedState struct {
	Version     int    `json:"version"`
	OperationID string `json:"operationId"`
	Apps        map[string]struct {
		LAN *liveHostdRebindNativeLANBinding `json:"lan,omitempty"`
	} `json:"apps"`
	Pending     json.RawMessage `json:"pending,omitempty"`
	LANRecovery *struct {
		Head  int `json:"head"`
		Items []struct {
			Kind    string                        `json:"kind"`
			AppID   string                        `json:"appId"`
			Disable *liveHostdRebindNativeDisable `json:"disable,omitempty"`
		} `json:"items"`
		LegacyPending json.RawMessage `json:"legacyPending,omitempty"`
	} `json:"lanRecovery,omitempty"`
}

type liveHostdRebindNativeBatchItemIdentity struct {
	Kind, OperationID, AppID, AllocationID, OwnerOperationID, AccessRevisionID, SourceGrantAttemptID string
	Port                                                                                             uint16
}

type liveHostdRebindNativeBatchIdentity struct {
	PlaintextDigest, UpgradeOperationID, LegacyPendingDigest string
	Head, Count                                              int
	Items                                                    []liveHostdRebindNativeBatchItemIdentity
}

func newLiveHostdGatewayRebindMigratedPendingLANBatchFixture(t *testing.T) (
	*liveHostdGatewayRebindFixture, legacyNativePendingLANBatch,
) {
	t.Helper()
	fixture := newLiveHostdGatewayRebindFixture(t)
	control := filepath.Join(fixture.dataRoot, "control.db")
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(control + suffix); err == nil || !os.IsNotExist(err) {
			t.Fatalf("closed current fixture retained SQLite sidecar %q: %v", suffix, err)
		}
	}

	source, err := sql.Open("sqlite", control)
	if err != nil {
		t.Fatal(err)
	}
	source.SetMaxOpenConns(1)
	if _, err := source.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	authority := liveHostdRebindReadNativeBatchAuthority(t, source, fixture)
	liveHostdRebindAssertNativeProtectedBaseline(t, fixture, authority)
	if err := source.Close(); err != nil {
		t.Fatal("close current fixture authority database:", err)
	}

	retained := filepath.Join(fixture.root, "current-public-authority.db")
	if err := os.Rename(control, retained); err != nil {
		t.Fatal("retain current public authority database:", err)
	}
	db := legacyPairCompositionDBThrough028At(t, control)
	batch := seedLegacyNativePendingLANBatch(t, db, authority)
	session, err := auth.New(db).NewSession(authority.actor.id)
	if err != nil {
		t.Fatal("create migrated fixture session:", err)
	}
	fixture.session = session
	for _, operationID := range batch.operationIDs {
		fixture.registerOperation(operationID)
	}
	if err := db.Close(); err != nil {
		t.Fatal("close migrated native batch database:", err)
	}
	return fixture, batch
}

func liveHostdRebindReadNativeProtectedState(t *testing.T,
	fixture *liveHostdGatewayRebindFixture,
) (liveHostdRebindNativeProtectedState, string) {
	t.Helper()
	path := filepath.Join(fixture.dataRoot, "runtime", "generated-ingress", "routes-v2.bundle")
	body, err := secretfile.ReadBounded(path, liveHostdRebindNativeRoutePurpose, 256<<10)
	if err != nil {
		t.Fatal("read purpose-bound native route state:", err)
	}
	defer clear(body)
	var state liveHostdRebindNativeProtectedState
	if len(body) == 0 || len(body) > 192<<10 || json.Unmarshal(body, &state) != nil ||
		state.Version != 2 || state.OperationID != fixture.upgradeID || len(state.Apps) == 0 {
		t.Fatal("purpose-bound native route state failed bounded identity decoding")
	}
	digest := sha256.Sum256(body)
	return state, fmt.Sprintf("%x", digest[:])
}

func liveHostdRebindAssertNativeProtectedBaseline(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	authority legacyNativeBatchAuthority,
) {
	t.Helper()
	state, _ := liveHostdRebindReadNativeProtectedState(t, fixture)
	if len(state.Pending) != 0 || state.LANRecovery != nil || len(state.Apps) != len(authority.access) {
		t.Fatalf("current public native route state is not a settled two-app baseline: apps=%d pending=%t batch=%t",
			len(state.Apps), len(state.Pending) != 0, state.LANRecovery != nil)
	}
	for _, access := range authority.access {
		app, ok := state.Apps[access.allocation.AppID]
		if !ok || app.LAN == nil || app.LAN.GrantAttemptID != access.grant.AttemptID ||
			app.LAN.GrantRequestDigest != access.grant.RequestDigest ||
			app.LAN.AllocationID != access.allocation.ID || app.LAN.Port != access.allocation.Port ||
			app.LAN.OwnerOperationID != access.allocation.OwnerOperationID ||
			app.LAN.AccessRevisionID != access.revision.ID ||
			app.LAN.ProfileRevisionID != authority.profile.ID {
			t.Fatalf("current public native route binding identity changed for app %q", access.allocation.AppID)
		}
	}
}

func liveHostdRebindReadNativeBatchIdentity(t *testing.T, fixture *liveHostdGatewayRebindFixture,
	batch legacyNativePendingLANBatch,
) liveHostdRebindNativeBatchIdentity {
	t.Helper()
	state, digest := liveHostdRebindReadNativeProtectedState(t, fixture)
	identity, err := liveHostdRebindNativeBatchIdentityFor(state, digest, batch)
	if err != nil {
		t.Fatal("validate protected native recovery batch:", err)
	}
	return identity
}

func liveHostdRebindNativeBatchIdentityFor(state liveHostdRebindNativeProtectedState, digest string,
	batch legacyNativePendingLANBatch,
) (liveHostdRebindNativeBatchIdentity, error) {
	if len(state.Pending) != 0 || state.LANRecovery == nil || state.LANRecovery.Head != 0 ||
		len(state.LANRecovery.Items) != len(batch.claims) || len(batch.claims) != 2 {
		return liveHostdRebindNativeBatchIdentity{}, errors.New("protected native recovery batch shape changed")
	}
	identity := liveHostdRebindNativeBatchIdentity{PlaintextDigest: digest,
		UpgradeOperationID: state.OperationID, Head: state.LANRecovery.Head,
		Count: len(state.LANRecovery.Items)}
	if len(state.LANRecovery.LegacyPending) != 0 {
		legacyDigest := sha256.Sum256(state.LANRecovery.LegacyPending)
		identity.LegacyPendingDigest = fmt.Sprintf("%x", legacyDigest[:])
	}
	seen := make(map[string]bool, len(batch.claims))
	for _, item := range state.LANRecovery.Items {
		if item.Kind != "lan_disable" || item.Disable == nil || item.Disable.SourceGrant == nil ||
			item.AppID != item.Disable.AppID {
			return liveHostdRebindNativeBatchIdentity{}, errors.New("protected native recovery batch contains a non-disable or incomplete item")
		}
		expected, ok := batch.claims[item.Disable.OperationID]
		if !ok || seen[item.Disable.OperationID] || item.AppID != expected.allocation.AppID ||
			item.Disable.AllocationID != expected.allocation.ID ||
			item.Disable.OwnerOperationID != expected.allocation.OwnerOperationID ||
			item.Disable.AccessRevisionID != expected.revision.ID || item.Disable.Port != expected.allocation.Port ||
			item.Disable.SourceGrant.AttemptID != expected.grant.AttemptID {
			return liveHostdRebindNativeBatchIdentity{}, errors.New("protected native recovery item identity changed")
		}
		seen[item.Disable.OperationID] = true
		identity.Items = append(identity.Items, liveHostdRebindNativeBatchItemIdentity{
			Kind: item.Kind, OperationID: item.Disable.OperationID, AppID: item.AppID,
			AllocationID: item.Disable.AllocationID, OwnerOperationID: item.Disable.OwnerOperationID,
			AccessRevisionID:     item.Disable.AccessRevisionID,
			SourceGrantAttemptID: item.Disable.SourceGrant.AttemptID, Port: item.Disable.Port,
		})
	}
	if len(seen) != 2 || identity.Items[0].OperationID != batch.operationIDs[0] ||
		identity.Items[1].OperationID != batch.operationIDs[1] {
		return liveHostdRebindNativeBatchIdentity{}, errors.New("protected native recovery batch order changed")
	}
	return identity, nil
}

func liveHostdRebindAssertSameNativeBatch(t *testing.T,
	before, after liveHostdRebindNativeBatchIdentity,
) {
	t.Helper()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("fresh process changed the protected native batch: before_head=%d after_head=%d before_count=%d after_count=%d",
			before.Head, after.Head, before.Count, after.Count)
	}
}

func TestLiveHostdRebindNativeBatchIdentityControls(t *testing.T) {
	authority := deterministicLegacyNativeBatchAuthority(t)
	batch := legacyNativePendingLANBatch{claims: make(map[string]legacyNativeBatchAccessRow, 2)}
	for _, access := range authority.access {
		batch.operationIDs = append(batch.operationIDs, access.disableOperation)
		batch.claims[access.disableOperation] = access
	}
	sort.Slice(batch.operationIDs, func(i, j int) bool {
		return batch.claims[batch.operationIDs[i]].allocation.AppID <
			batch.claims[batch.operationIDs[j]].allocation.AppID
	})
	state := func() liveHostdRebindNativeProtectedState {
		items := make([]map[string]any, 0, 2)
		for _, operationID := range batch.operationIDs {
			access := batch.claims[operationID]
			items = append(items, map[string]any{"kind": "lan_disable", "appId": access.allocation.AppID,
				"disable": map[string]any{"operationId": operationID, "appId": access.allocation.AppID,
					"allocationId": access.allocation.ID, "ownerOperationId": access.allocation.OwnerOperationID,
					"port": access.allocation.Port, "accessRevisionId": access.revision.ID,
					"sourceGrant": map[string]any{"AttemptID": access.grant.AttemptID}}})
		}
		body, err := json.Marshal(map[string]any{"version": 2, "operationId": authority.upgrade.OperationID,
			"apps":        map[string]any{"fixture": map[string]any{}},
			"lanRecovery": map[string]any{"head": 0, "items": items}})
		if err != nil {
			t.Fatal(err)
		}
		var result liveHostdRebindNativeProtectedState
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if identity, err := liveHostdRebindNativeBatchIdentityFor(state(), strings.Repeat("a", 64), batch); err != nil ||
		identity.Head != 0 || identity.Count != 2 || len(identity.Items) != 2 {
		t.Fatalf("valid native batch identity rejected: head=%d count=%d items=%d error=%v",
			identity.Head, identity.Count, len(identity.Items), err)
	}
	for name, mutate := range map[string]func(*liveHostdRebindNativeProtectedState){
		"advanced head": func(value *liveHostdRebindNativeProtectedState) { value.LANRecovery.Head = 1 },
		"wrong kind":    func(value *liveHostdRebindNativeProtectedState) { value.LANRecovery.Items[0].Kind = "lan_grant" },
		"duplicate operation": func(value *liveHostdRebindNativeProtectedState) {
			value.LANRecovery.Items[1].Disable.OperationID = value.LANRecovery.Items[0].Disable.OperationID
		},
		"reordered items": func(value *liveHostdRebindNativeProtectedState) {
			value.LANRecovery.Items[0], value.LANRecovery.Items[1] = value.LANRecovery.Items[1], value.LANRecovery.Items[0]
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := state()
			mutate(&value)
			if _, err := liveHostdRebindNativeBatchIdentityFor(value, strings.Repeat("a", 64), batch); err == nil {
				t.Fatal("invalid native batch identity was accepted")
			}
		})
	}
}

func liveHostdRebindReadNativeBatchAuthority(t *testing.T, db *sql.DB,
	fixture *liveHostdGatewayRebindFixture,
) legacyNativeBatchAuthority {
	t.Helper()
	expected := legacyNativeBatchExtractionExpectation{actorID: fixture.user.ID,
		upgradeOperationID: fixture.upgradeID,
		apps: []legacyNativeBatchExpectedApp{
			{appID: fixture.app.id, disableOperation: uuid.NewString()},
			{appID: fixture.secondApp.id, disableOperation: uuid.NewString()},
		}}
	return readLegacyNativeBatchAuthority(t, fixture.ctx, db, expected)
}

func liveHostdRebindAssertMigratedNativeBatchSQL(t *testing.T,
	fixture *liveHostdGatewayRebindFixture, batch legacyNativePendingLANBatch,
) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(fixture.dataRoot, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	authority := legacyNativeBatchAuthority{upgrade: appaccess.GatewayProfileUpgradeClaim{OperationID: fixture.upgradeID}}
	// The shared assertion uses the exact immutable identities retained in the
	// batch and reads the full current snapshot; only the source upgrade ID is
	// needed from the original authority after the live child has been killed.
	assertLegacyNativePendingLANBatch(t, db, authority, batch)
}
