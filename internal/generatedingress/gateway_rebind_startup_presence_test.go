package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

type startupPresenceRepositoryFunc func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)

func (f startupPresenceRepositoryFunc) GatewayRebindRecoverySnapshot(ctx context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
	return f(ctx)
}

func emptyStartupPresenceRepository(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
	return appaccess.GatewayRebindRecoverySnapshot{}, nil
}

func TestGatewayRebindStartupPresenceLeavesFreshStorageAbsent(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
	if _, err := os.Lstat(protectedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly has protected storage: %v", err)
	}
	presence, err := InspectGatewayRebindStartupPresence(context.Background(), root, appaccess.New(db))
	if err != nil || !reflect.DeepEqual(presence, GatewayRebindStartupPresence{}) {
		t.Fatalf("fresh real repository must prove absence without Docker: %#v, %v", presence, err)
	}
	if _, err := os.Lstat(protectedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only probe created protected storage: %v", err)
	}
}

func TestGatewayRebindStartupPresenceLeavesNativeGatewayWithoutRebindAbsent(t *testing.T) {
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.CurrentProfile == nil || snapshot.CurrentSource == nil ||
		len(snapshot.History) != 0 || snapshot.Active != nil {
		t.Fatal("fixture must have a valid native gateway and no rebind history")
	}
	before, err := readGatewayHistorySnapshot(f.manager.store)
	if err != nil {
		t.Fatal(err)
	}
	commands := len(f.runner.commands)
	presence, err := InspectGatewayRebindStartupPresence(context.Background(), f.manager.options.DataRoot, f.repository)
	if err != nil || !reflect.DeepEqual(presence, GatewayRebindStartupPresence{}) {
		t.Fatalf("valid native gateway must retain its ordinary startup path: %#v, %v", presence, err)
	}
	after, err := readGatewayHistorySnapshot(f.manager.store)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		t.Fatal("native presence inspection changed protected evidence")
	}
	if len(f.runner.commands) != commands {
		t.Fatal("native presence inspection issued Docker commands")
	}
	confirmed, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(snapshot, confirmed) {
		t.Fatal("native presence inspection changed SQL authority")
	}
}

func TestGatewayRebindStartupPresenceRetainsPreparedSQLAuthority(t *testing.T) {
	f := newGatewayRebindPredecessorFixture(t)
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.CurrentSource == nil {
		t.Fatal("fixture must have a real prepared SQL operation")
	}
	before, err := readGatewayHistorySnapshot(f.manager.store)
	if err != nil {
		t.Fatal(err)
	}
	commands := len(f.runner.commands)
	presence, err := InspectGatewayRebindStartupPresence(context.Background(), f.manager.options.DataRoot, f.repository)
	if err != nil || !presence.Present || presence.ProtectedArtifacts ||
		presence.ActiveOperationID != f.proposal.Spec.OperationID || presence.ActivePhase != appaccess.GatewayRebindPrepared ||
		!reflect.DeepEqual(presence.SelectedCurrentAuthority, snapshot.CurrentSource) {
		t.Fatalf("prepared SQL-only attempt must remain visible for recovery: %#v, %v", presence, err)
	}
	after, err := readGatewayHistorySnapshot(f.manager.store)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		t.Fatal("presence inspection changed protected evidence")
	}
	if len(f.runner.commands) != commands {
		t.Fatal("presence inspection issued Docker commands")
	}
	if err := f.repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, appaccess.ErrGatewayRebindActive) {
		t.Fatal("presence inspection released the prepared rebind fence")
	}
}

func TestGatewayRebindStartupPresenceRejectsOrphanArtifacts(t *testing.T) {
	const operationID = "41414141-4141-4141-8141-414141414141"
	currentName, _ := gatewayCurrentRouteStateName(1, operationID)
	intentName, _ := gatewayRebindProtectedIntentName(1, operationID)
	checkpointName, _ := gatewayRebindPredecessorCheckpointName(1, operationID)
	terminalName, _ := gatewayRebindFinalHandoverTerminalName(1, operationID)
	for _, name := range []string{
		currentName, intentName, checkpointName, terminalName,
		strings.Replace(intentName, ".v1.", ".v2.", 1),
		"gateway-current-routes.unknown.bundle", "gateway-rebind-unknown.bundle", strings.ToUpper(currentName),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
			if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(protectedRoot, name)
			content := []byte("untrusted orphan evidence")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectGatewayRebindStartupPresence(context.Background(), root,
				startupPresenceRepositoryFunc(emptyStartupPresenceRepository)); err == nil {
				t.Fatal("orphan or unknown reserved artifact was treated as absence")
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(content, after) {
				t.Fatal("refused orphan evidence was changed or removed")
			}
		})
	}
}

func TestGatewayRebindStartupPresenceRejectsUnsafeParentAndArtifactTypes(t *testing.T) {
	for _, level := range []string{"runtime file", "ingress file", "artifact directory"} {
		t.Run(level, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "runtime")
			if level != "runtime file" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "generated-ingress")
			}
			if level == "artifact directory" {
				name, _ := gatewayCurrentRouteStateName(1, "41414141-4141-4141-8141-414141414141")
				if err := os.MkdirAll(filepath.Join(path, name), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectGatewayRebindStartupPresence(context.Background(), root,
				startupPresenceRepositoryFunc(emptyStartupPresenceRepository)); err == nil {
				t.Fatal("unsafe parent or reserved artifact type was accepted")
			}
		})
	}
}

func TestGatewayRebindStartupPresenceDoesNotRepairDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission preservation is verified on Linux")
	}
	root := t.TempDir()
	protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
	if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(protectedRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	// A read-only probe may reject a permission policy, but must never repair it.
	_, _ = InspectGatewayRebindStartupPresence(context.Background(), root,
		startupPresenceRepositoryFunc(emptyStartupPresenceRepository))
	after, err := os.Lstat(protectedRoot)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("presence probe changed directory identity or permissions")
	}
}

func TestGatewayRebindStartupPresenceRejectsSymlinkParentsAndArtifacts(t *testing.T) {
	for _, kind := range []string{"parent", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			root, target := t.TempDir(), t.TempDir()
			path := filepath.Join(root, "runtime")
			if kind == "artifact" {
				path = filepath.Join(path, "generated-ingress")
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				name, _ := gatewayCurrentRouteStateName(1, "41414141-4141-4141-8141-414141414141")
				path = filepath.Join(path, name)
				target = filepath.Join(target, "evidence")
				if err := os.WriteFile(target, []byte("linked evidence"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlink unavailable on this host: %v", err)
			}
			if _, err := InspectGatewayRebindStartupPresence(context.Background(), root,
				startupPresenceRepositoryFunc(emptyStartupPresenceRepository)); err == nil {
				t.Fatal("symbolic link crossed read-only presence validation")
			}
		})
	}
}

func TestGatewayRebindStartupPresenceKeepsTerminalCurrentHistoryVisible(t *testing.T) {
	// The existing protected fixture and its SQL projection simulate an already
	// validated committed source. This checks presence dispatch only; the real
	// repository's terminal validation and Docker recovery have separate gates.
	f := newGatewayCurrentStateFixture(t)
	if err := f.store.installBaseline(f.baseline); err != nil {
		t.Fatal(err)
	}
	repository := gatewayRebindProposalRepositoryForCurrentFixture(t, f)
	if repository.snapshot.Active != nil || len(repository.snapshot.History) == 0 {
		t.Fatal("fixture must retain a current rebind without an active operation")
	}
	presence, err := InspectGatewayRebindStartupPresence(context.Background(), f.manager.options.DataRoot, &repository)
	if err != nil || !presence.Present || !presence.ProtectedArtifacts ||
		presence.ActiveOperationID != "" || presence.ActivePhase != "" ||
		!reflect.DeepEqual(presence.SelectedCurrentAuthority, repository.snapshot.CurrentSource) {
		t.Fatalf("terminal history was mistaken for a fresh no-op: %#v, %v", presence, err)
	}
}

func TestGatewayRebindStartupPresenceRejectsRepositoryFailureAndCancellation(t *testing.T) {
	for _, failure := range []string{"SQL read", "cancel during read"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repository := startupPresenceRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				if failure == "SQL read" {
					return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("injected unreadable SQL")
				}
				cancel()
				return appaccess.GatewayRebindRecoverySnapshot{}, nil
			})
			if _, err := InspectGatewayRebindStartupPresence(ctx, t.TempDir(), repository); err == nil {
				t.Fatal("failed or cancelled SQL observation proved absence")
			}
		})
	}
}

func TestGatewayRebindStartupPresenceRejectsArtifactAppearingDuringSQLConfirmation(t *testing.T) {
	root := t.TempDir()
	reads := 0
	repository := startupPresenceRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		reads++
		if reads == 2 {
			protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
			if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			name, _ := gatewayCurrentRouteStateName(1, "41414141-4141-4141-8141-414141414141")
			if err := os.WriteFile(filepath.Join(protectedRoot, name), []byte("new orphan"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return appaccess.GatewayRebindRecoverySnapshot{}, nil
	})
	if _, err := InspectGatewayRebindStartupPresence(context.Background(), root, repository); err == nil {
		t.Fatal("new protected evidence crossed the absence decision")
	}
}

func TestGatewayRebindStartupPresenceRejectsEmptyDirectoryChangeDuringSQLConfirmation(t *testing.T) {
	for _, change := range []string{"created", "replaced"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			protectedRoot := filepath.Join(root, "runtime", "generated-ingress")
			if change == "replaced" {
				if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			repository := startupPresenceRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				reads++
				if reads == 2 {
					if change == "replaced" {
						// Preserve the original directory so the filesystem cannot
						// reuse its identity for the replacement during this test.
						if err := os.Rename(protectedRoot, protectedRoot+"-original"); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				return appaccess.GatewayRebindRecoverySnapshot{}, nil
			})
			if _, err := InspectGatewayRebindStartupPresence(context.Background(), root, repository); err == nil {
				t.Fatal("changed protected directory identity was treated as stable absence")
			}
		})
	}
}
