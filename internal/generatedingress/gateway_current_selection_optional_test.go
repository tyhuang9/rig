package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

func TestOptionalGatewayCurrentSelectionPreservesFreshRepository(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Exercise the read-only helper before a Manager constructor can create
	// storage. Production callers still hold both writer locks around it.
	m := &Manager{options: Options{DataRoot: root, RebindCurrentStateRepository: appaccess.New(db)}}
	selection, snapshot, present, err := m.readOptionalGatewayCurrentSelectionLocked(context.Background())
	if err != nil || present || !reflect.DeepEqual(selection, gatewayCurrentSelection{}) ||
		!gatewayCurrentSelectionSQLAbsent(snapshot) {
		t.Fatalf("fresh real SQL did not preserve absence: present=%t err=%v", present, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "runtime", "generated-ingress")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only absence created protected storage: %v", err)
	}
	m.options.RebindCurrentStateRepository = nil
	if _, _, _, err := m.readOptionalGatewayCurrentSelectionLocked(context.Background()); err == nil {
		t.Fatal("missing SQL provider was treated as proof of absence")
	}
}

func TestOptionalGatewayCurrentSelectionUsesRealNativeAuthority(t *testing.T) {
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	f.manager.options.RebindCurrentStateRepository = f.repository
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	commands := len(f.runner.commands)
	release, err := f.manager.lockGatewayRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selection, snapshot, present, selectErr := f.manager.readOptionalGatewayCurrentSelectionLocked(context.Background())
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if selectErr != nil || !present || selection.Kind != gatewayCurrentSelectionUpgrade || selection.Upgrade == nil ||
		snapshot.CurrentSource == nil || gatewayCurrentAuthority(selection.Lineage) != *snapshot.CurrentSource {
		t.Fatalf("real native authority was lost: present=%t err=%v", present, selectErr)
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(before, after) || len(f.runner.commands) != commands {
		t.Fatal("current selection changed protected evidence or invoked Docker")
	}
}

func TestOptionalGatewayCurrentSelectionRequiresCompleteAbsence(t *testing.T) {
	profile := appaccess.GatewayProfileRevision{ID: "41414141-4141-4141-8141-414141414141", RevisionNumber: 1,
		Spec: appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.40.5", InterfaceID: "test-lan", PortStart: 8100, PortEnd: 8119}}
	var err error
	profile.SpecDigest, err = appaccess.GatewayProfileSpecDigest(profile.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*appaccess.GatewayRebindRecoverySnapshot)
	}{
		{"history", func(s *appaccess.GatewayRebindRecoverySnapshot) {
			s.History = []appaccess.GatewayRebindHistoryEntry{{}}
		}},
		{"active", func(s *appaccess.GatewayRebindRecoverySnapshot) { s.Active = &appaccess.GatewayRebindHistoryEntry{} }},
		{"phase", func(s *appaccess.GatewayRebindRecoverySnapshot) { s.Phase = appaccess.GatewayRebindPrepared }},
		{"current event", func(s *appaccess.GatewayRebindRecoverySnapshot) {
			s.CurrentDatabaseCommittedEvent = &appaccess.GatewayRebindEvent{}
		}},
		{"active event", func(s *appaccess.GatewayRebindRecoverySnapshot) {
			s.DatabaseCommittedEvent = &appaccess.GatewayRebindEvent{}
		}},
		{"transfers", func(s *appaccess.GatewayRebindRecoverySnapshot) {
			s.CurrentTransfers = []appaccess.GatewayRebindAllocationTransfer{{}}
		}},
		{"commit observed", func(s *appaccess.GatewayRebindRecoverySnapshot) { s.DatabaseCommitObserved = true }},
		{"rollback allowed", func(s *appaccess.GatewayRebindRecoverySnapshot) { s.RollbackAllowed = true }},
		{"invalid configured profile", func(s *appaccess.GatewayRebindRecoverySnapshot) {
			bad := profile
			bad.SpecDigest = ""
			s.CurrentProfile = &bad
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := appaccess.GatewayRebindRecoverySnapshot{}
			test.change(&value)
			m := &Manager{options: Options{DataRoot: t.TempDir(), RebindCurrentStateRepository: gatewayCurrentSelectionRepositoryFunc(
				func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) { return value, nil })}}
			if _, _, _, err := m.readOptionalGatewayCurrentSelectionLocked(context.Background()); err == nil {
				t.Fatal("partial SQL history was treated as absence")
			}
		})
	}
	m := &Manager{options: Options{DataRoot: t.TempDir(), RebindCurrentStateRepository: gatewayCurrentSelectionRepositoryFunc(
		func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
			return appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: &profile}, nil
		})}}
	if _, snapshot, present, err := m.readOptionalGatewayCurrentSelectionLocked(context.Background()); err != nil || present ||
		snapshot.CurrentProfile == nil || *snapshot.CurrentProfile != profile {
		t.Fatalf("configured preupgrade profile was treated as a current gateway: %v", err)
	}
}

func TestOptionalGatewayCurrentSelectionRejectsChangesAndOrphans(t *testing.T) {
	for _, failure := range []string{"first SQL error", "second SQL error", "second SQL change", "second cancellation", "orphan", "created directory", "replaced directory"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			protected := filepath.Join(root, "runtime", "generated-ingress")
			if failure == "replaced directory" || failure == "orphan" {
				if err := os.MkdirAll(protected, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "orphan" {
				if err := os.WriteFile(filepath.Join(protected, "gateway-current-routes.unknown.bundle"), []byte("orphan"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			repository := gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				reads++
				value := appaccess.GatewayRebindRecoverySnapshot{}
				if failure == "first SQL error" || (failure == "second SQL error" && reads == 2) {
					return value, errors.New("read failure")
				}
				if reads == 2 {
					switch failure {
					case "second SQL change":
						value.RollbackAllowed = true
					case "second cancellation":
						cancel()
					case "replaced directory":
						// Retain the old directory to prevent file-ID reuse.
						if err := os.Rename(protected, protected+"-retained"); err != nil {
							t.Fatal(err)
						}
						fallthrough
					case "created directory":
						if err := os.MkdirAll(protected, 0o700); err != nil {
							t.Fatal(err)
						}
					}
				}
				return value, nil
			})
			m := &Manager{options: Options{DataRoot: root, RebindCurrentStateRepository: repository}}
			if _, _, _, err := m.readOptionalGatewayCurrentSelectionLocked(ctx); err == nil {
				t.Fatal("unstable or orphan history was treated as absence")
			}
		})
	}
}

func TestOptionalGatewayCurrentSelectionRetainsTerminalRebind(t *testing.T) {
	// Protected terminal history plus a simulated SQL projection checks the
	// dispatch decision. Actual SQL commit and physical serving have other gates.
	f := newGatewayCurrentStateFixture(t)
	f.manager.mu = newContextMutex()
	if err := f.store.installBaseline(f.baseline); err != nil {
		t.Fatal(err)
	}
	repository := gatewayRebindProposalRepositoryForCurrentFixture(t, f)
	f.manager.options.RebindCurrentStateRepository = &repository
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := f.manager.lockGatewayRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	selection, _, present, err := f.manager.readOptionalGatewayCurrentSelectionLocked(context.Background())
	if err != nil || !present || selection.Kind != gatewayCurrentSelectionRebind || selection.Lineage != f.baseline.Lineage {
		t.Fatalf("terminal current authority was mistaken for absence: %v", err)
	}
	f.manager.options.RebindCurrentStateRepository = nil
	if _, _, _, err := f.manager.readOptionalGatewayCurrentSelectionLocked(context.Background()); err == nil {
		t.Fatal("protected receipt alone selected a current authority")
	}
}
