package applifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

const deploymentAuthorityAppID = "11111111-1111-4111-8111-111111111111"

type deploymentAuthorityHeadResult struct {
	head Head
	err  error
}

type deploymentAuthorityHeads struct {
	results      []deploymentAuthorityHeadResult
	applications []string
	onRead       func()
}

func (h *deploymentAuthorityHeads) Head(_ context.Context, applicationID string) (Head, error) {
	h.applications = append(h.applications, applicationID)
	if h.onRead != nil {
		h.onRead()
	}
	if len(h.results) == 0 {
		return Head{}, errors.New("unexpected desired lifecycle head read")
	}
	result := h.results[0]
	h.results = h.results[1:]
	return result.head, result.err
}

func TestDeploymentAuthoritySnapshotStrictlyClassifiesDesiredHead(t *testing.T) {
	updated := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	running, stopped, malformed := Running, Stopped, State("paused")
	tests := []struct {
		name       string
		result     deploymentAuthorityHeadResult
		wantError  error
		wantLegacy bool
		wantState  State
		wantRev    int64
	}{
		{name: "legacy uninitialized", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID}}, wantLegacy: true},
		{name: "authored running", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &running, Revision: 7, UpdatedAt: &updated}}, wantState: Running, wantRev: 7},
		{name: "authored stopped", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &stopped, Revision: 7, UpdatedAt: &updated}}, wantError: ErrDeploymentNotAuthorized},
		{name: "missing", result: deploymentAuthorityHeadResult{err: ErrApplicationNotFound}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "read failure", result: deploymentAuthorityHeadResult{err: errors.New("storage failed")}, wantError: ErrDeploymentAuthorityUnavailable},
		{name: "foreign application", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: "different"}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "negative revision", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &running, Revision: -1, UpdatedAt: &updated}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "null state positive revision", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, Revision: 1, UpdatedAt: &updated}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "null state timestamp", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, UpdatedAt: &updated}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "running revision zero", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &running, UpdatedAt: &updated}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "running missing timestamp", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &running, Revision: 1}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "stopped missing timestamp", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &stopped, Revision: 1}}, wantError: ErrDeploymentAuthorityInvalid},
		{name: "unknown state", result: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &malformed, Revision: 1, UpdatedAt: &updated}}, wantError: ErrDeploymentAuthorityInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			heads := &deploymentAuthorityHeads{results: []deploymentAuthorityHeadResult{test.result}}
			authority, err := NewDeploymentAuthority(heads)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := authority.Snapshot(context.Background(), deploymentAuthorityAppID)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error=%v want=%v", err, test.wantError)
			}
			if test.wantError != nil {
				return
			}
			state, hasState := snapshot.DesiredState()
			if snapshot.ApplicationID() != deploymentAuthorityAppID || snapshot.Revision() != test.wantRev ||
				snapshot.IsLegacyUninitialized() != test.wantLegacy || hasState != (test.wantState != "") || state != test.wantState {
				t.Fatalf("snapshot app=%q revision=%d legacy=%t state=%q hasState=%t",
					snapshot.ApplicationID(), snapshot.Revision(), snapshot.IsLegacyUninitialized(), state, hasState)
			}
		})
	}
}

func TestDeploymentAuthorityRevalidationRequiresExactValue(t *testing.T) {
	updated := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	running, stopped := Running, Stopped
	runningHead := func(revision int64) Head {
		return Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &running, Revision: revision, UpdatedAt: &updated}
	}
	legacyHead := Head{ApplicationID: deploymentAuthorityAppID}
	tests := []struct {
		name      string
		initial   Head
		current   deploymentAuthorityHeadResult
		wantError error
	}{
		{name: "running unchanged", initial: runningHead(4), current: deploymentAuthorityHeadResult{head: runningHead(4)}},
		{name: "legacy unchanged", initial: legacyHead, current: deploymentAuthorityHeadResult{head: legacyHead}},
		{name: "running revision drift", initial: runningHead(4), current: deploymentAuthorityHeadResult{head: runningHead(5)}, wantError: ErrDeploymentAuthorityChanged},
		{name: "running to stopped", initial: runningHead(4), current: deploymentAuthorityHeadResult{head: Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &stopped, Revision: 5, UpdatedAt: &updated}}, wantError: ErrDeploymentAuthorityChanged},
		{name: "running to legacy", initial: runningHead(4), current: deploymentAuthorityHeadResult{head: legacyHead}, wantError: ErrDeploymentAuthorityChanged},
		{name: "legacy to running", initial: legacyHead, current: deploymentAuthorityHeadResult{head: runningHead(1)}, wantError: ErrDeploymentAuthorityChanged},
		{name: "read failure", initial: runningHead(4), current: deploymentAuthorityHeadResult{err: errors.New("storage failed")}, wantError: ErrDeploymentAuthorityUnavailable},
		{name: "missing", initial: runningHead(4), current: deploymentAuthorityHeadResult{err: ErrApplicationNotFound}, wantError: ErrDeploymentAuthorityInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			heads := &deploymentAuthorityHeads{results: []deploymentAuthorityHeadResult{{head: test.initial}, test.current}}
			authority, err := NewDeploymentAuthority(heads)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := authority.Snapshot(context.Background(), deploymentAuthorityAppID)
			if err != nil {
				t.Fatal(err)
			}
			if err := authority.Revalidate(context.Background(), snapshot); !errors.Is(err, test.wantError) {
				t.Fatalf("revalidation error=%v want=%v", err, test.wantError)
			}
			if len(heads.applications) != 2 || heads.applications[0] != deploymentAuthorityAppID || heads.applications[1] != deploymentAuthorityAppID {
				t.Fatalf("head reads=%v", heads.applications)
			}
		})
	}
}

func TestDeploymentAuthorityPreservesCancellation(t *testing.T) {
	running := Running
	updated := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	valid := Head{ApplicationID: deploymentAuthorityAppID, DesiredState: &running, Revision: 1, UpdatedAt: &updated}
	heads := &deploymentAuthorityHeads{results: []deploymentAuthorityHeadResult{{err: context.Canceled}}}
	authority, err := NewDeploymentAuthority(heads)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Snapshot(context.Background(), deploymentAuthorityAppID); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancellation=%v", err)
	}
	if err := authority.Revalidate(context.Background(), DeploymentAuthoritySnapshot{}); !errors.Is(err, ErrDeploymentAuthorityInvalid) {
		t.Fatalf("zero snapshot error=%v", err)
	}
	t.Run("already canceled does not read", func(t *testing.T) {
		heads := &deploymentAuthorityHeads{results: []deploymentAuthorityHeadResult{{head: valid}}}
		authority, err := NewDeploymentAuthority(heads)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := authority.Snapshot(ctx, deploymentAuthorityAppID); !errors.Is(err, context.Canceled) {
			t.Fatalf("already-canceled snapshot error=%v", err)
		}
		if len(heads.applications) != 0 {
			t.Fatalf("already-canceled snapshot read heads=%v", heads.applications)
		}
		if _, err := authority.Snapshot(ctx, ""); !errors.Is(err, ErrDeploymentAuthorityInvalid) {
			t.Fatalf("invalid input did not take priority: %v", err)
		}
	})
	t.Run("successful snapshot read cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		heads := &deploymentAuthorityHeads{
			results: []deploymentAuthorityHeadResult{{head: valid}},
			onRead:  cancel,
		}
		authority, err := NewDeploymentAuthority(heads)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authority.Snapshot(ctx, deploymentAuthorityAppID); !errors.Is(err, context.Canceled) {
			t.Fatalf("successful canceled read error=%v", err)
		}
	})
	t.Run("successful revalidation read cancels", func(t *testing.T) {
		heads := &deploymentAuthorityHeads{results: []deploymentAuthorityHeadResult{{head: valid}, {head: valid}}}
		authority, err := NewDeploymentAuthority(heads)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := authority.Snapshot(context.Background(), deploymentAuthorityAppID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		heads.onRead = cancel
		if err := authority.Revalidate(ctx, snapshot); !errors.Is(err, context.Canceled) {
			t.Fatalf("successful canceled revalidation error=%v", err)
		}
	})
}

func TestDeploymentAuthorityReadGuardNeverAuthorsIntent(t *testing.T) {
	fixture := newLifecycleFixture(t)
	authority, err := NewDeploymentAuthority(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := authority.Snapshot(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.IsLegacyUninitialized() || snapshot.Revision() != 0 {
		t.Fatalf("legacy snapshot=%#v", snapshot)
	}
	if err := authority.Revalidate(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	assertLifecycleHeadAndHistory(t, fixture.db, fixture.appID, 0, 0)
}
