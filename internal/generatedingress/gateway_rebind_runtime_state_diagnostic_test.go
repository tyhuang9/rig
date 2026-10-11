package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// liveGatewayRebindRuntimeFailureStateDiagnostic is deliberately a small,
// finite failure report. It is read-only and never substitutes for the error
// returned by the coordinator.
type liveGatewayRebindRuntimeFailureStateDiagnostic struct {
	Failure         liveGatewayRebindRuntimeFailureDiagnostic
	SQLPhase        string
	TerminalReceipt string
	CurrentArtifact string
	Authority       string
}

const (
	liveGatewayRebindRuntimeDiagnosticUnavailable = "unavailable"
	liveGatewayRebindRuntimeDiagnosticAbsent      = "absent"
	liveGatewayRebindRuntimeDiagnosticPresent     = "present"
	liveGatewayRebindRuntimeDiagnosticCommit      = "commit"
	liveGatewayRebindRuntimeDiagnosticAbort       = "abort"
	liveGatewayRebindRuntimeDiagnosticPredecessor = "predecessor"
	liveGatewayRebindRuntimeDiagnosticSuccessor   = "successor"
	liveGatewayRebindRuntimeDiagnosticOther       = "other"
)

type liveGatewayRebindRuntimeDiagnosticReadPair struct {
	firstSQL, secondSQL                   appaccess.GatewayRebindRecoverySnapshot
	firstSQLErr, secondSQLErr             error
	firstProtected, secondProtected       gatewayRebindProtectedPresenceSnapshot
	firstProtectedErr, secondProtectedErr error
	firstReceipt, secondReceipt           string
	firstReceiptErr, secondReceiptErr     error
	globalErr                             error
}

type liveGatewayRebindRuntimeDiagnosticReader func(context.Context) (liveGatewayRebindRuntimeDiagnosticReadPair, error)

// failureStateDiagnostic freezes the existing stage/runner result before any
// durable read. The bounded inspection lock is the only synchronization it
// takes and remains available after a process fail-stop.
func (f liveGatewayRebindRuntimeFixture) failureStateDiagnostic(
	parent context.Context, original error,
) liveGatewayRebindRuntimeFailureStateDiagnostic {
	return f.failureStateDiagnosticWithReader(parent, original, f.collectFailureStateDiagnosticReads)
}

func (f liveGatewayRebindRuntimeFixture) failureStateDiagnosticWithReader(parent context.Context, original error,
	reader liveGatewayRebindRuntimeDiagnosticReader,
) (result liveGatewayRebindRuntimeFailureStateDiagnostic) {
	// Capture the bounded stage/runner result before the lock or any durable
	// read. A diagnostic reader may never revise this original failure record.
	frozen := f.failureDiagnostic(original)
	result = liveGatewayRebindRuntimeFailureStateDiagnostic{
		Failure:         frozen,
		SQLPhase:        liveGatewayRebindRuntimeDiagnosticUnavailable,
		TerminalReceipt: liveGatewayRebindRuntimeDiagnosticUnavailable,
		CurrentArtifact: liveGatewayRebindRuntimeDiagnosticUnavailable,
		Authority:       liveGatewayRebindRuntimeDiagnosticUnavailable,
	}
	if f.manager == nil || f.repository == nil || parent == nil || reader == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	release, err := f.manager.lockGatewayRawForInspection(ctx)
	if err != nil {
		return result
	}
	// A successful lock must be released even if cancellation races immediately
	// afterward. A failed release invalidates every status.
	defer func() {
		if releaseErr := release(); releaseErr != nil || ctx.Err() != nil {
			result = liveGatewayRebindRuntimeFailureStateDiagnostic{Failure: frozen,
				SQLPhase: liveGatewayRebindRuntimeDiagnosticUnavailable, TerminalReceipt: liveGatewayRebindRuntimeDiagnosticUnavailable,
				CurrentArtifact: liveGatewayRebindRuntimeDiagnosticUnavailable, Authority: liveGatewayRebindRuntimeDiagnosticUnavailable}
		}
	}()
	if ctx.Err() != nil {
		return result
	}
	pair, readErr := reader(ctx)
	if readErr != nil || ctx.Err() != nil {
		return result
	}
	return f.failureStateDiagnosticFromReads(frozen, pair)
}

func (f liveGatewayRebindRuntimeFixture) collectFailureStateDiagnosticReads(
	ctx context.Context,
) (liveGatewayRebindRuntimeDiagnosticReadPair, error) {
	pair := liveGatewayRebindRuntimeDiagnosticReadPair{}
	pair.firstSQL, pair.firstSQLErr = f.repository.GatewayRebindRecoverySnapshot(ctx)
	pair.firstProtected, pair.firstProtectedErr = readGatewayRebindProtectedPresenceReadOnly(f.manager.options.DataRoot)
	if pair.firstProtectedErr == nil {
		pair.firstReceipt, pair.firstReceiptErr = f.readTerminalReceiptDiagnostic()
	}
	pair.secondSQL, pair.secondSQLErr = f.repository.GatewayRebindRecoverySnapshot(ctx)
	pair.secondProtected, pair.secondProtectedErr = readGatewayRebindProtectedPresenceReadOnly(f.manager.options.DataRoot)
	if pair.secondProtectedErr == nil {
		pair.secondReceipt, pair.secondReceiptErr = f.readTerminalReceiptDiagnostic()
	}
	return pair, nil
}

func (f liveGatewayRebindRuntimeFixture) failureStateDiagnosticFromReads(
	failure liveGatewayRebindRuntimeFailureDiagnostic, pair liveGatewayRebindRuntimeDiagnosticReadPair,
) liveGatewayRebindRuntimeFailureStateDiagnostic {
	result := liveGatewayRebindRuntimeFailureStateDiagnostic{
		Failure:         failure,
		SQLPhase:        liveGatewayRebindRuntimeDiagnosticUnavailable,
		TerminalReceipt: liveGatewayRebindRuntimeDiagnosticUnavailable,
		CurrentArtifact: liveGatewayRebindRuntimeDiagnosticUnavailable,
		Authority:       liveGatewayRebindRuntimeDiagnosticUnavailable,
	}
	if pair.globalErr != nil {
		return result
	}
	if pair.firstSQLErr == nil && pair.secondSQLErr == nil && reflect.DeepEqual(pair.firstSQL, pair.secondSQL) {
		result.SQLPhase = f.failureStateDiagnosticSQLPhase(pair.firstSQL)
		result.Authority = f.failureStateDiagnosticAuthority(pair.firstSQL)
	}
	if pair.firstProtectedErr == nil && pair.secondProtectedErr == nil &&
		sameGatewayRebindProtectedPresence(pair.firstProtected, pair.secondProtected) {
		result.CurrentArtifact = f.failureStateDiagnosticCurrentArtifact(pair.firstProtected)
		firstReceipt, secondReceipt := liveGatewayRebindRuntimeDiagnosticReceiptStatus(pair.firstReceipt),
			liveGatewayRebindRuntimeDiagnosticReceiptStatus(pair.secondReceipt)
		if pair.firstReceiptErr == nil && pair.secondReceiptErr == nil && firstReceipt == secondReceipt {
			result.TerminalReceipt = firstReceipt
		}
	}
	return result
}

func liveGatewayRebindRuntimeDiagnosticReceiptStatus(value string) string {
	switch value {
	case liveGatewayRebindRuntimeDiagnosticAbsent, liveGatewayRebindRuntimeDiagnosticCommit,
		liveGatewayRebindRuntimeDiagnosticAbort:
		return value
	default:
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
}

func (f liveGatewayRebindRuntimeFixture) failureStateDiagnosticSQLPhase(snapshot appaccess.GatewayRebindRecoverySnapshot) string {
	if snapshot.Active == nil {
		if snapshot.Phase == "" && snapshot.DatabaseCommittedEvent == nil && !snapshot.DatabaseCommitObserved && !snapshot.RollbackAllowed {
			return liveGatewayRebindRuntimeDiagnosticAbsent
		}
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
	operationID, err := gatewayRebindHistoryOperationID(*snapshot.Active)
	if err != nil || operationID != f.inspection.Spec.OperationID {
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
	switch snapshot.Phase {
	case appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady,
		appaccess.GatewayRebindDatabaseCommitted, appaccess.GatewayRebindCommitted,
		appaccess.GatewayRebindRolledBack, appaccess.GatewayRebindUnresolved:
		return string(snapshot.Phase)
	default:
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
}

func (f liveGatewayRebindRuntimeFixture) failureStateDiagnosticAuthority(
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) string {
	if snapshot.CurrentSource == nil || !gatewayCurrentProfileMatchesAuthority(snapshot.CurrentProfile, *snapshot.CurrentSource) {
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
	authority := *snapshot.CurrentSource
	switch authority.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		if authority == gatewayCurrentAuthority(f.inspection.Spec.Predecessor.Lineage) {
			return liveGatewayRebindRuntimeDiagnosticPredecessor
		}
		return liveGatewayRebindRuntimeDiagnosticOther
	case appaccess.GatewayRebindSourceGatewayRebind:
		// This reports only the exact SQL-selected successor operation/profile.
		// Terminal lineage validation remains the responsibility of the normal
		// recovery path and is deliberately not claimed by this diagnostic.
		if authority.OperationID == f.inspection.Spec.OperationID &&
			authority.ProfileRevisionID == f.inspection.Spec.SuccessorProfileRevisionID &&
			authority.ProfileRevisionNumber == f.inspection.Spec.SuccessorProfileRevisionNumber &&
			authority.ProfileSpecDigest == f.inspection.SuccessorProfileSpecDigest {
			return liveGatewayRebindRuntimeDiagnosticSuccessor
		}
		return liveGatewayRebindRuntimeDiagnosticOther
	default:
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
}

func (f liveGatewayRebindRuntimeFixture) failureStateDiagnosticCurrentArtifact(value gatewayRebindProtectedPresenceSnapshot) string {
	// The status is presence for this fixture operation's validated current
	// artifact, never a comparison with an earlier route baseline.
	if value.files == nil {
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
	return liveGatewayRebindRuntimeDiagnosticCurrentArtifactFor(value,
		f.inspection.Spec.SuccessorProtectedGeneration, f.inspection.Spec.OperationID)
}

func liveGatewayRebindRuntimeDiagnosticCurrentArtifactFor(value gatewayRebindProtectedPresenceSnapshot,
	generation uint64, operationID string,
) string {
	if generation == 0 || !validCanonicalUUID(operationID) {
		return liveGatewayRebindRuntimeDiagnosticUnavailable
	}
	for name := range value.files {
		fileGeneration, fileOperationID, relevant, err := parseGatewayCurrentRoutePresenceName(name)
		if err != nil {
			return liveGatewayRebindRuntimeDiagnosticUnavailable
		} else if relevant && fileGeneration == generation && fileOperationID == operationID {
			return liveGatewayRebindRuntimeDiagnosticPresent
		}
	}
	return liveGatewayRebindRuntimeDiagnosticAbsent
}

// readTerminalReceiptDiagnostic validates only the fixture operation's exact
// terminal artifact. It intentionally does not claim that a complete lineage
// scan succeeded.
func (f liveGatewayRebindRuntimeFixture) readTerminalReceiptDiagnostic() (string, error) {
	generation, operationID := f.inspection.Spec.SuccessorProtectedGeneration, f.inspection.Spec.OperationID
	if generation == 0 || !validCanonicalUUID(operationID) {
		return "", errors.New("invalid diagnostic terminal identity")
	}
	directory, present, _, err := inspectStateStoreReadOnly(f.manager.options.DataRoot)
	if err != nil {
		return "", err
	}
	if !present {
		return liveGatewayRebindRuntimeDiagnosticAbsent, nil
	}
	name, purpose := gatewayRebindTerminalNameV2(generation, operationID)
	path := filepath.Join(directory.root, name)
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return liveGatewayRebindRuntimeDiagnosticAbsent, nil
		}
		return "", err
	}
	store := &gatewayRebindTerminalStoreV2{directory: directory, dataRoot: f.manager.options.DataRoot,
		generation: generation, operationID: operationID, path: path, purpose: purpose}
	receipt, err := store.load()
	if err != nil {
		return "", err
	}
	switch receipt.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		return liveGatewayRebindRuntimeDiagnosticCommit, nil
	case appaccess.GatewayRebindDispositionAbort:
		return liveGatewayRebindRuntimeDiagnosticAbort, nil
	default:
		return "", errors.New("invalid diagnostic terminal disposition")
	}
}

func TestGatewayRebindRuntimeFailureStateDiagnosticClassifiesStableReads(t *testing.T) {
	profile := &appaccess.GatewayProfileRevision{ID: "11111111-1111-4111-8111-111111111111", RevisionNumber: 3,
		SpecDigest: strings.Repeat("a", 64)}
	predecessorOperation := "44444444-4444-4444-8444-444444444444"
	inspection := GatewayRebindProposalInspection{Spec: appaccess.GatewayRebindSpecV2{
		OperationID: "22222222-2222-4222-8222-222222222222", SuccessorProfileRevisionID: "33333333-3333-4333-8333-333333333333",
		SuccessorProfileRevisionNumber: 4, SuccessorProtectedGeneration: 1,
		Predecessor: appaccess.GatewayRebindSourceRef{Lineage: appaccess.GatewayCurrentLineageRef{
			Kind: appaccess.GatewayRebindSourceGatewayUpgrade, OperationID: predecessorOperation,
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
		}},
	}, SuccessorProfileSpecDigest: strings.Repeat("b", 64)}
	fixture := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: liveGatewayRebindSourceFixture{profile: *profile}, inspection: inspection}
	predecessor := appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: profile, CurrentSource: &appaccess.GatewayCurrentAuthorityRef{
		Kind: appaccess.GatewayRebindSourceGatewayUpgrade, OperationID: predecessorOperation,
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
	}}
	successorProfile := &appaccess.GatewayProfileRevision{ID: inspection.Spec.SuccessorProfileRevisionID,
		RevisionNumber: inspection.Spec.SuccessorProfileRevisionNumber, SpecDigest: inspection.SuccessorProfileSpecDigest}
	successor := appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: successorProfile, CurrentSource: &appaccess.GatewayCurrentAuthorityRef{
		Kind: appaccess.GatewayRebindSourceGatewayRebind, OperationID: inspection.Spec.OperationID,
		ProfileRevisionID: successorProfile.ID, ProfileRevisionNumber: successorProfile.RevisionNumber, ProfileSpecDigest: successorProfile.SpecDigest,
	}}
	currentName, _ := gatewayCurrentRouteStateName(1, inspection.Spec.OperationID)
	markerPath := filepath.Join(t.TempDir(), "validated-current")
	if err := os.WriteFile(markerPath, []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerInfo, err := os.Stat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	protectedWithCurrent := gatewayRebindProtectedPresenceSnapshot{files: map[string]gatewayHistoryFileFingerprint{currentName: {info: markerInfo}}}
	protectedEmpty := gatewayRebindProtectedPresenceSnapshot{files: map[string]gatewayHistoryFileFingerprint{}}
	for _, test := range []struct {
		name      string
		pair      liveGatewayRebindRuntimeDiagnosticReadPair
		phase     string
		receipt   string
		current   string
		authority string
	}{
		{name: "stable predecessor absence", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQL: predecessor, secondSQL: predecessor,
			firstProtected: protectedEmpty, secondProtected: protectedEmpty, firstReceipt: liveGatewayRebindRuntimeDiagnosticAbsent,
			secondReceipt: liveGatewayRebindRuntimeDiagnosticAbsent}, phase: liveGatewayRebindRuntimeDiagnosticAbsent,
			receipt: liveGatewayRebindRuntimeDiagnosticAbsent, current: liveGatewayRebindRuntimeDiagnosticAbsent,
			authority: liveGatewayRebindRuntimeDiagnosticPredecessor},
		{name: "stable successor commit", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQL: successor, secondSQL: successor,
			firstProtected: protectedWithCurrent, secondProtected: protectedWithCurrent, firstReceipt: liveGatewayRebindRuntimeDiagnosticCommit,
			secondReceipt: liveGatewayRebindRuntimeDiagnosticCommit}, phase: liveGatewayRebindRuntimeDiagnosticAbsent,
			receipt: liveGatewayRebindRuntimeDiagnosticCommit, current: liveGatewayRebindRuntimeDiagnosticPresent,
			authority: liveGatewayRebindRuntimeDiagnosticSuccessor},
		{name: "same profile foreign predecessor operation is other", pair: liveGatewayRebindRuntimeDiagnosticReadPair{
			firstSQL: appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: profile, CurrentSource: &appaccess.GatewayCurrentAuthorityRef{
				Kind: appaccess.GatewayRebindSourceGatewayUpgrade, OperationID: "55555555-5555-4555-8555-555555555555",
				ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
			}},
			secondSQL: appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: profile, CurrentSource: &appaccess.GatewayCurrentAuthorityRef{
				Kind: appaccess.GatewayRebindSourceGatewayUpgrade, OperationID: "55555555-5555-4555-8555-555555555555",
				ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
			}}, firstProtected: protectedEmpty, secondProtected: protectedEmpty, firstReceipt: liveGatewayRebindRuntimeDiagnosticAbsent,
			secondReceipt: liveGatewayRebindRuntimeDiagnosticAbsent}, phase: liveGatewayRebindRuntimeDiagnosticAbsent,
			receipt: liveGatewayRebindRuntimeDiagnosticAbsent, current: liveGatewayRebindRuntimeDiagnosticAbsent,
			authority: liveGatewayRebindRuntimeDiagnosticOther},
		{name: "sql drift leaves protected result", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQL: predecessor, secondSQL: successor,
			firstProtected: protectedWithCurrent, secondProtected: protectedWithCurrent, firstReceipt: liveGatewayRebindRuntimeDiagnosticAbort,
			secondReceipt: liveGatewayRebindRuntimeDiagnosticAbort}, phase: liveGatewayRebindRuntimeDiagnosticUnavailable,
			receipt: liveGatewayRebindRuntimeDiagnosticAbort, current: liveGatewayRebindRuntimeDiagnosticPresent,
			authority: liveGatewayRebindRuntimeDiagnosticUnavailable},
		{name: "protected drift leaves sql result", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQL: successor, secondSQL: successor,
			firstProtected: protectedEmpty, secondProtected: protectedWithCurrent, firstReceipt: liveGatewayRebindRuntimeDiagnosticAbsent,
			secondReceipt: liveGatewayRebindRuntimeDiagnosticCommit}, phase: liveGatewayRebindRuntimeDiagnosticAbsent,
			receipt: liveGatewayRebindRuntimeDiagnosticUnavailable, current: liveGatewayRebindRuntimeDiagnosticUnavailable,
			authority: liveGatewayRebindRuntimeDiagnosticSuccessor},
		{name: "sql error leaves protected result", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQLErr: errors.New("sql"),
			firstProtected: protectedEmpty, secondProtected: protectedEmpty, firstReceipt: liveGatewayRebindRuntimeDiagnosticAbsent,
			secondReceipt: liveGatewayRebindRuntimeDiagnosticAbsent}, phase: liveGatewayRebindRuntimeDiagnosticUnavailable,
			receipt: liveGatewayRebindRuntimeDiagnosticAbsent, current: liveGatewayRebindRuntimeDiagnosticAbsent,
			authority: liveGatewayRebindRuntimeDiagnosticUnavailable},
		{name: "protected error leaves sql result", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQL: predecessor, secondSQL: predecessor,
			firstProtectedErr: errors.New("protected")}, phase: liveGatewayRebindRuntimeDiagnosticAbsent,
			receipt: liveGatewayRebindRuntimeDiagnosticUnavailable, current: liveGatewayRebindRuntimeDiagnosticUnavailable,
			authority: liveGatewayRebindRuntimeDiagnosticPredecessor},
		{name: "unknown receipt is unavailable", pair: liveGatewayRebindRuntimeDiagnosticReadPair{firstSQL: predecessor, secondSQL: predecessor,
			firstProtected: protectedEmpty, secondProtected: protectedEmpty, firstReceipt: "untrusted", secondReceipt: "untrusted"},
			phase: liveGatewayRebindRuntimeDiagnosticAbsent, receipt: liveGatewayRebindRuntimeDiagnosticUnavailable,
			current: liveGatewayRebindRuntimeDiagnosticAbsent, authority: liveGatewayRebindRuntimeDiagnosticPredecessor},
		{name: "cancelled collection is globally unavailable", pair: liveGatewayRebindRuntimeDiagnosticReadPair{globalErr: context.Canceled},
			phase: liveGatewayRebindRuntimeDiagnosticUnavailable, receipt: liveGatewayRebindRuntimeDiagnosticUnavailable,
			current: liveGatewayRebindRuntimeDiagnosticUnavailable, authority: liveGatewayRebindRuntimeDiagnosticUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := fixture.failureStateDiagnosticFromReads(liveGatewayRebindRuntimeFailureDiagnostic{}, test.pair)
			if value.SQLPhase != test.phase || value.TerminalReceipt != test.receipt || value.CurrentArtifact != test.current || value.Authority != test.authority {
				t.Fatalf("state diagnostic=%#v", value)
			}
		})
	}
	active := &appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
		SpecVersion: appaccess.GatewayRebindSpecVersionV2, V2: &appaccess.GatewayRebindClaimV2{Spec: inspection.Spec},
	}}
	for _, phase := range []appaccess.GatewayRebindState{appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady,
		appaccess.GatewayRebindDatabaseCommitted, appaccess.GatewayRebindCommitted, appaccess.GatewayRebindRolledBack,
		appaccess.GatewayRebindUnresolved} {
		if value := fixture.failureStateDiagnosticSQLPhase(appaccess.GatewayRebindRecoverySnapshot{Active: active, Phase: phase}); value != string(phase) {
			t.Fatalf("phase %q classified %q", phase, value)
		}
	}
	if value := fixture.failureStateDiagnosticSQLPhase(appaccess.GatewayRebindRecoverySnapshot{Active: active, Phase: "unexpected"}); value != liveGatewayRebindRuntimeDiagnosticUnavailable {
		t.Fatalf("unknown phase=%q", value)
	}
	foreign := *active
	foreignClaim := *foreign.Claim.V2
	foreignClaim.Spec.OperationID = "66666666-6666-4666-8666-666666666666"
	foreign.Claim.V2 = &foreignClaim
	if value := fixture.failureStateDiagnosticSQLPhase(appaccess.GatewayRebindRecoverySnapshot{Active: &foreign, Phase: appaccess.GatewayRebindPrepared}); value != liveGatewayRebindRuntimeDiagnosticUnavailable {
		t.Fatalf("foreign active phase=%q", value)
	}
}

func TestGatewayRebindRuntimeFailureStateDiagnosticInspectionAllowsFailStopWithoutEffects(t *testing.T) {
	fixture, input, physical := newGatewayRebindCoordinatorFixture(t)
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository, input, physical); err != nil {
		t.Fatal(err)
	}
	beforeSQL, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeProtected, err := readGatewayRebindProtectedPresenceReadOnly(fixture.manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.gatewayRebindFailStopLatch().Store(true)
	beforeFailStop := fixture.manager.gatewayRebindFailStopLatch().Load()
	beforeBarrier := fixture.manager.gatewayRebindCommitBarrier.Load()
	value := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: liveGatewayRebindSourceFixture{
		repository: fixture.repository, profile: *beforeSQL.CurrentProfile,
	}, manager: fixture.manager, inspection: input.Inspection}.failureStateDiagnostic(context.Background(), errors.New("original failure"))
	if value.SQLPhase != liveGatewayRebindRuntimeDiagnosticAbsent || value.TerminalReceipt != liveGatewayRebindRuntimeDiagnosticCommit ||
		value.CurrentArtifact != liveGatewayRebindRuntimeDiagnosticPresent || value.Authority != liveGatewayRebindRuntimeDiagnosticSuccessor ||
		value.Failure.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown || value.Failure.FirstCoordinatorMethod != liveGatewayRebindRuntimeCoordinatorMethodUnknown ||
		!fixture.manager.gatewayRebindFailStopLatch().Load() ||
		fixture.manager.gatewayRebindFailStopLatch().Load() != beforeFailStop || fixture.manager.gatewayRebindCommitBarrier.Load() != beforeBarrier {
		t.Fatalf("fail-stop inspection diagnostic=%#v", value)
	}
	afterSQL, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	afterProtected, err := readGatewayRebindProtectedPresenceReadOnly(fixture.manager.options.DataRoot)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterSQL) || !sameGatewayRebindProtectedPresence(beforeProtected, afterProtected) ||
		physical.commitCalls != 1 || physical.attestCalls != 1 || physical.abortCalls != 0 || physical.withdrawCalls != 0 {
		t.Fatalf("inspection mutated durable state: sql=%t protected=%t calls=%d/%d/%d/%d err=%v",
			reflect.DeepEqual(beforeSQL, afterSQL), sameGatewayRebindProtectedPresence(beforeProtected, afterProtected), physical.commitCalls,
			physical.attestCalls, physical.abortCalls, physical.withdrawCalls, err)
	}
}

func TestLiveGatewayRebindRuntimeCoordinatorDiagnosticKeepsFirstOutcome(t *testing.T) {
	driver := &liveGatewayRebindRuntimeRollbackDriver{}
	driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodReconcileSuccess, nil)
	driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodConfirmForward, nil)
	driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodAttestCurrent, nil)
	value := driver.coordinatorDiagnosticSnapshot()
	if !value.ReconcileOK || !value.ForwardOK || !value.AttestationOK || value.FirstFailure ||
		value.FirstMethod != liveGatewayRebindRuntimeCoordinatorMethodUnknown {
		t.Fatalf("successful coordinator outcomes changed: %#v", value)
	}
	driver = &liveGatewayRebindRuntimeRollbackDriver{}
	driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodConfirmForward, errors.New("forward"))
	driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodAttestCurrent, errors.New("attest"))
	if value := driver.coordinatorDiagnosticSnapshot(); !value.FirstFailure || value.FirstMethod != liveGatewayRebindRuntimeCoordinatorMethodConfirmForward {
		t.Fatalf("first coordinator error changed: %#v", value)
	}
	driver = &liveGatewayRebindRuntimeRollbackDriver{}
	driver.recordCoordinatorOutcome("invalid", errors.New("unknown"))
	if value := driver.coordinatorDiagnosticSnapshot(); !value.FirstFailure || value.FirstMethod != liveGatewayRebindRuntimeCoordinatorMethodUnknown {
		t.Fatalf("unknown method was not clamped: %#v", value)
	}
}

func TestGatewayRebindRuntimeFailureStateDiagnosticFreezesBeforeBoundedRead(t *testing.T) {
	fixture, _, _ := newGatewayRebindCoordinatorFixture(t)
	driver := &liveGatewayRebindRuntimeRollbackDriver{}
	driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodReconcileSuccess, nil)
	runner := &liveGatewayRebindRuntimeDiagnosticRunner{runner: &gatewayRebindTypedStageDiagnosticRunnerStub{}}
	if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"network", "ls"}}); err != nil {
		t.Fatal(err)
	}
	parentDeadline := time.Now().Add(time.Second)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	defer cancelParent()
	value := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: liveGatewayRebindSourceFixture{repository: fixture.repository},
		manager: fixture.manager, diagnosticDriver: driver, diagnosticRunner: runner}.failureStateDiagnosticWithReader(
		parent, errors.New("original"),
		func(ctx context.Context) (liveGatewayRebindRuntimeDiagnosticReadPair, error) {
			deadline, ok := ctx.Deadline()
			if !ok || deadline.After(parentDeadline) || ctx.Err() != nil {
				t.Fatalf("bounded read deadline changed: present=%t deadline=%s parent=%s err=%v", ok, deadline, parentDeadline, ctx.Err())
			}
			driver.recordCoordinatorOutcome(liveGatewayRebindRuntimeCoordinatorMethodConfirmForward, errors.New("later"))
			if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"container", "ls"}}); err != nil {
				t.Fatal(err)
			}
			return liveGatewayRebindRuntimeDiagnosticReadPair{}, nil
		})
	if value.Failure.FirstCoordinatorFailure || !value.Failure.ReconcileSucceeded || value.Failure.ForwardConfirmed ||
		value.Failure.LatestCommand != "network_ls" {
		t.Fatalf("durable reads altered frozen recorder: %#v", value.Failure)
	}
	cancelled := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: liveGatewayRebindSourceFixture{repository: fixture.repository}, manager: fixture.manager}.
		failureStateDiagnosticWithReader(context.Background(), errors.New("original"), func(context.Context) (liveGatewayRebindRuntimeDiagnosticReadPair, error) {
			return liveGatewayRebindRuntimeDiagnosticReadPair{}, context.Canceled
		})
	if cancelled.SQLPhase != liveGatewayRebindRuntimeDiagnosticUnavailable || cancelled.TerminalReceipt != liveGatewayRebindRuntimeDiagnosticUnavailable ||
		cancelled.CurrentArtifact != liveGatewayRebindRuntimeDiagnosticUnavailable || cancelled.Authority != liveGatewayRebindRuntimeDiagnosticUnavailable {
		t.Fatalf("cancelled read fabricated state: %#v", cancelled)
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	cancelledParent := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: liveGatewayRebindSourceFixture{repository: fixture.repository}, manager: fixture.manager}.
		failureStateDiagnosticWithReader(parent, errors.New("original"), func(context.Context) (liveGatewayRebindRuntimeDiagnosticReadPair, error) {
			called = true
			return liveGatewayRebindRuntimeDiagnosticReadPair{}, nil
		})
	if called || cancelledParent.SQLPhase != liveGatewayRebindRuntimeDiagnosticUnavailable ||
		cancelledParent.TerminalReceipt != liveGatewayRebindRuntimeDiagnosticUnavailable ||
		cancelledParent.CurrentArtifact != liveGatewayRebindRuntimeDiagnosticUnavailable ||
		cancelledParent.Authority != liveGatewayRebindRuntimeDiagnosticUnavailable {
		t.Fatalf("parent cancellation did not invalidate the diagnostic: called=%t value=%#v", called, cancelledParent)
	}
}

func TestGatewayRebindRuntimeFailureDiagnosticContextUsesFixtureContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "fixture")
	fixtureContext := liveGatewayRebindRuntimeFixture{liveGatewayRebindSourceFixture: liveGatewayRebindSourceFixture{
		fixture: &liveGatewayV2Fixture{ctx: ctx},
	}}
	if fixtureContext.failureDiagnosticContext() != ctx {
		t.Fatal("failure diagnostic did not retain the fixture context")
	}
	if value := (liveGatewayRebindRuntimeFixture{}).failureDiagnosticContext(); value != nil {
		t.Fatalf("missing fixture context=%v", value)
	}
}
