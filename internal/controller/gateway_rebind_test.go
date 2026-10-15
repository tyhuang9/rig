package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/generatedingress"
)

// A nil embedded implementation deliberately panics if a handler starts doing
// SQL work itself instead of passing the repository to the runtime boundary.
type rebindHandlerRepository struct {
	generatedingress.GatewayRebindCommitRepository
}

type rebindHandlerRuntime struct {
	review      generatedingress.GatewayRebindCommitReview
	result      generatedingress.GatewayRebindCommitResponse
	err         error
	reviewCalls int
	commitCalls int
	repository  generatedingress.GatewayRebindCommitRepository
	input       generatedingress.GatewayRebindProposalInput
	request     generatedingress.GatewayRebindCommitRequest
	actor       string
	onInspect   func()
	onCommit    func(context.Context, func(context.Context) error) error
}

func (f *rebindHandlerRuntime) InspectGatewayRebindCommitReview(_ context.Context,
	repository generatedingress.GatewayRebindProposalRepository, input generatedingress.GatewayRebindProposalInput,
) (generatedingress.GatewayRebindCommitReview, error) {
	f.reviewCalls++
	f.repository = repository.(generatedingress.GatewayRebindCommitRepository)
	f.input = input
	if f.onInspect != nil {
		f.onInspect()
	}
	return f.review, f.err
}

func (f *rebindHandlerRuntime) CommitGatewayRebind(ctx context.Context,
	repository generatedingress.GatewayRebindCommitRepository, request generatedingress.GatewayRebindCommitRequest,
	actor string, guard func(context.Context) error,
) (generatedingress.GatewayRebindCommitResponse, error) {
	f.commitCalls++
	f.repository, f.request, f.actor = repository, request, actor
	if f.onCommit != nil {
		if err := f.onCommit(ctx, guard); err != nil {
			return generatedingress.GatewayRebindCommitResponse{}, err
		}
	}
	return f.result, f.err
}

func newRebindHandlerFixture(t *testing.T, kind string) (*Server, *http.Request, *rebindHandlerRuntime, *rebindSessionAuthFake) {
	t.Helper()
	s, request, authentication := newRebindSessionFixture()
	var body string
	for _, fixture := range rebindRequestFixtures() {
		if fixture.name == kind {
			body = fixture.body
		}
	}
	if body == "" {
		t.Fatalf("missing request fixture %s", kind)
	}
	request.Body = io.NopCloser(strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	runtime := &rebindHandlerRuntime{result: rebindHandlerTerminalResult()}
	s.GeneratedRuntime, s.GatewayRebinds, s.GatewayRebindRuntime = true, &rebindHandlerRepository{}, runtime
	return s, request, runtime, authentication
}

func rebindHandlerTerminalResult() generatedingress.GatewayRebindCommitResponse {
	return generatedingress.GatewayRebindCommitResponse{
		Created: true, FenceReleased: true,
		Operation: generatedingress.GatewayRebindOperationStatus{
			OperationID: rebindRequestOperation, StoredState: appaccess.GatewayRebindCommitted,
			StateSequence: 4, DatabaseCommitObserved: true,
			TerminalDisposition: appaccess.GatewayRebindDispositionCommit, TerminalReceiptDigest: strings.Repeat("d", 64),
		},
		TerminalProof: generatedingress.GatewayRebindProofVerified, TerminalProofDigest: strings.Repeat("e", 64),
		CurrentProof: generatedingress.GatewayRebindProofVerified, CurrentAttestationDigest: strings.Repeat("f", 64),
		SelectedCurrentAuthority: &appaccess.GatewayCurrentAuthorityRef{
			Kind: appaccess.GatewayRebindSourceGatewayRebind, OperationID: rebindRequestOperation,
			ProfileRevisionID: rebindRequestRevision, ProfileRevisionNumber: 2,
			ProfileSpecDigest: strings.Repeat("b", 64), TerminalReceiptDigest: strings.Repeat("d", 64),
		},
	}
}

func invokeRebindHandler(s *Server, request *http.Request, kind string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	if kind == "proposal" {
		s.inspectLANGatewayRebind(response, request)
	} else {
		s.commitLANGatewayRebind(response, request)
	}
	return response
}

func TestLANGatewayRebindAdapterForwardsOriginalAuthority(t *testing.T) {
	s, request, runtime, _ := newRebindHandlerFixture(t, "commit")
	guardCalls := 0
	runtime.onCommit = func(ctx context.Context, guard func(context.Context) error) error {
		if guard == nil {
			t.Fatal("missing live request guard")
		}
		guardCalls++
		return guard(ctx)
	}
	response := invokeRebindHandler(s, request, "commit")
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" ||
		runtime.commitCalls != 1 || runtime.reviewCalls != 0 || guardCalls != 1 ||
		runtime.repository != s.GatewayRebinds || runtime.actor != "original-administrator" {
		t.Fatalf("response=%d calls=%d/%d guard=%d actor=%q", response.Code, runtime.commitCalls, runtime.reviewCalls, guardCalls, runtime.actor)
	}
	want := rebindRequestFixtures()[1].expected.(lanGatewayRebindCommitRequest)
	if !reflect.DeepEqual(runtime.request, generatedingress.GatewayRebindCommitRequest{
		Version: want.Version, Proposal: want.Proposal, ProposalDigest: want.ProposalDigest,
		ConfigureApprovalDigest: want.ConfigureApprovalDigest, RebindApprovalDigest: want.RebindApprovalDigest,
	}) {
		t.Fatal("runtime received different reviewed input")
	}
	var result lanGatewayRebindMutation
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Created ||
		result.Operation.OperationID != rebindRequestOperation || result.TerminalProof != "verified" ||
		result.CurrentProof != "verified" || result.SelectedCurrentAuthority == nil {
		t.Fatalf("unexpected curated response: %s", response.Body.String())
	}
	for _, forbidden := range []string{"original-session", "original-csrf", "original-administrator", "url", "ready"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response exposed %q", forbidden)
		}
	}
}

func TestLANGatewayRebindAdapterRefusesBeforeRuntime(t *testing.T) {
	for _, kind := range []string{"proposal", "commit"} {
		t.Run(kind, func(t *testing.T) {
			for _, fixture := range []struct {
				name   string
				status int
				mutate func(*Server, **http.Request)
			}{
				{"viewer", 403, func(_ *Server, r **http.Request) {
					*r = (*r).WithContext(context.WithValue((*r).Context(), principalKey{}, principal{user: auth.User{ID: "viewer", Role: "viewer"}}))
				}},
				{"missing_session", 403, func(_ *Server, r **http.Request) { (*r).Header.Del("Cookie") }},
				{"duplicate_csrf", 403, func(_ *Server, r **http.Request) { (*r).Header.Add("X-CSRF-Token", "original-csrf") }},
				{"runtime_disabled", 503, func(s *Server, _ **http.Request) { s.GeneratedRuntime = false }},
				{"recovery_only", 503, func(s *Server, _ **http.Request) { s.RecoveryOnly = true }},
				{"missing_runtime", 503, func(s *Server, _ **http.Request) { s.GatewayRebindRuntime = nil }},
				{"missing_repository", 503, func(s *Server, _ **http.Request) { s.GatewayRebinds = nil }},
				{"client_actor", 400, func(_ *Server, r **http.Request) {
					body, _ := io.ReadAll((*r).Body)
					(*r).Body = io.NopCloser(strings.NewReader(addRebindField(string(body), `"actorId":"injected"`)))
				}},
				{"oversized", 413, func(_ *Server, r **http.Request) {
					(*r).Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", maxGatewayRebindRequestBodyBytes+1)))
				}},
				{"cancelled", 403, func(_ *Server, r **http.Request) {
					ctx, cancel := context.WithCancel((*r).Context())
					cancel()
					*r = (*r).WithContext(ctx)
				}},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					s, request, runtime, _ := newRebindHandlerFixture(t, kind)
					fixture.mutate(s, &request)
					response := invokeRebindHandler(s, request, kind)
					if response.Code != fixture.status || response.Header().Get("Cache-Control") != "no-store" || runtime.reviewCalls != 0 || runtime.commitCalls != 0 {
						t.Fatalf("response=%d calls=%d/%d body=%s", response.Code, runtime.reviewCalls, runtime.commitCalls, response.Body.String())
					}
				})
			}
		})
	}
}

func TestLANGatewayRebindAdapterRechecksLiveSession(t *testing.T) {
	for _, kind := range []string{"proposal", "commit"} {
		t.Run(kind, func(t *testing.T) {
			s, request, runtime, authentication := newRebindHandlerFixture(t, kind)
			revoke := func() {
				authentication.authenticate = func(string) (auth.User, string, error) { return auth.User{}, "", errors.New("revoked-private-session") }
			}
			runtime.onInspect = revoke
			runtime.onCommit = func(ctx context.Context, guard func(context.Context) error) error {
				revoke()
				if err := guard(ctx); !errors.Is(err, appaccess.ErrApprovalRequired) {
					t.Fatalf("runtime guard accepted revoked session: %v", err)
				}
				// Even a misbehaving runtime returning success cannot disclose a
				// successful response after the request's live authority expired.
				return nil
			}
			response := invokeRebindHandler(s, request, kind)
			if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "revoked-private-session") || strings.Contains(response.Body.String(), "terminalProof") {
				t.Fatalf("expired response=%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindAdapterPreviewsCompleteCensus(t *testing.T) {
	s, request, runtime, _ := newRebindHandlerFixture(t, "proposal")
	input := rebindRequestFixtures()[0].expected.(generatedingress.GatewayRebindProposalInput)
	runtime.review = validRebindHandlerReview(t)
	response := invokeRebindHandler(s, request, "proposal")
	var result lanGatewayRebindReview
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		runtime.reviewCalls != 1 || runtime.commitCalls != 0 || runtime.repository != s.GatewayRebinds || !reflect.DeepEqual(runtime.input, input) ||
		json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Roster) != 2 || len(result.RuntimeHeads) != 1 ||
		result.Roster[1].AppID != runtime.review.Inspection.Roster[1].AppID || result.RuntimeHeads[0].AppID != runtime.review.Inspection.RuntimeHeads[0].AppID ||
		result.ProposalDigest != runtime.review.ProposalDigest || !reflect.DeepEqual(result.Spec, runtime.review.Inspection.Spec) ||
		strings.Contains(response.Body.String(), `"Inspection"`) || strings.Contains(response.Body.String(), `"SourceStateDigest"`) ||
		strings.Contains(response.Body.String(), `"entryDigest"`) || strings.Contains(response.Body.String(), `"predecessorTransferDigest"`) {
		t.Fatalf("preview=%d %s", response.Code, response.Body.String())
	}
}

func validRebindHandlerReview(t *testing.T) generatedingress.GatewayRebindCommitReview {
	t.Helper()
	input := rebindRequestFixtures()[0].expected.(generatedingress.GatewayRebindProposalInput)
	id := func(n int) string { return fmt.Sprintf("%08d-4444-4444-8444-444444444444", n) }
	digest := strings.Repeat("d", 64)
	checked := func(value string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	inspection := generatedingress.GatewayRebindProposalInspection{
		Spec: appaccess.GatewayRebindSpecV2{
			Version: 2, OperationID: input.OperationID,
			Predecessor: appaccess.GatewayRebindSourceRef{
				Lineage: appaccess.GatewayCurrentLineageRef{Kind: appaccess.GatewayRebindSourceGatewayUpgrade,
					OperationID: id(1), ProfileRevisionID: id(2), ProfileRevisionNumber: 1, ProfileSpecDigest: digest,
					ProtectedGeneration: 3, ProtectedIdentityDigest: digest, ProtectedJournalDigest: digest},
				SourceStateVersion: 2, SourceStateRevision: 0, SourceStateDigest: digest, PredecessorCheckpointDigest: digest,
			},
			SuccessorProtectedGeneration: 4, SuccessorProfileRevisionID: input.SuccessorProfileRevisionID,
			SuccessorProfileRevisionNumber: input.SuccessorProfileRevisionNumber,
			SuccessorProfileOperationID:    input.SuccessorProfileOperationID, SuccessorProfile: input.SuccessorProfile,
			RosterVersion: 2, RosterCount: 2, RuntimeHeadsVersion: 1, RuntimeHeadsCount: 1,
		},
		ProtectedGeneration: 4, PredecessorCheckpointDigest: digest,
		SourceStateVersion: 2, SourceStateRevision: 0, SourceStateDigest: digest,
		RuntimeHeads: []appaccess.GatewayRebindRuntimeHead{{AppID: id(30), DeploymentID: id(31), ReleaseID: id(32), Slot: "blue", Generation: 4, UpdatedAt: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}},
	}
	for i := 1; i <= 2; i++ {
		entry := appaccess.GatewayRebindRosterEntryV2{
			Version: 2, OperationID: input.OperationID, Ordinal: int64(i), AppID: id(10 + i), AllocationID: id(20 + i), Port: uint16(8100 + i),
			AllocationOwnerOperationID: id(3), AllocationState: appaccess.AllocationActive,
			AccessRevisionID: id(4), AccessRevisionNumber: 1, AccessSpecDigest: digest,
			GrantAttemptID: id(5), GrantStateSequence: 3, GrantProtectedStateDigest: digest,
			SourceProfileRevisionID: id(2), SourceProfileRevisionNumber: 1, SourceProfileSpecDigest: digest,
			ServingDeploymentID: id(6), ServingReleaseID: id(7), ServingSlot: "blue", RouteGeneration: 2,
		}
		entry.EntryDigest = checked(appaccess.GatewayRebindRosterEntryV2Digest(entry))
		inspection.Roster = append(inspection.Roster, entry)
	}
	inspection.Spec.RosterDigest = checked(appaccess.GatewayRebindRosterV2Digest(inspection.Roster))
	inspection.Spec.RuntimeHeadsDigest = checked(appaccess.GatewayRebindRuntimeHeadsV2Digest(input.OperationID, inspection.RuntimeHeads))
	inspection.SpecDigest = checked(appaccess.GatewayRebindSpecV2Digest(inspection.Spec))
	inspection.SuccessorProfileSpecDigest = checked(appaccess.GatewayProfileSpecDigest(input.SuccessorProfile))
	// This is the documented advisory wire format. No fixture claims or effects
	// are authorized by these digests; runtime tests cover their actual origin.
	payload, err := json.Marshal(struct {
		Version    int                                              `json:"version"`
		Purpose    string                                           `json:"purpose"`
		Inspection generatedingress.GatewayRebindProposalInspection `json:"inspection"`
	}{1, "lan_gateway_rebind_commit_review", inspection})
	if err != nil {
		t.Fatal(err)
	}
	review := generatedingress.GatewayRebindCommitReview{
		Version: 1, Inspection: inspection, ProposalDigest: fmt.Sprintf("%x", sha256.Sum256(payload)),
		ConfigureApprovalDigest: inspection.SuccessorProfileSpecDigest, RebindApprovalDigest: inspection.SpecDigest,
	}
	if err := generatedingress.ValidateGatewayRebindCommitReview(review); err != nil {
		t.Fatal(err)
	}
	return review
}

func TestLANGatewayRebindAdapterRejectsChangedReview(t *testing.T) {
	for name, mutate := range map[string]func(*generatedingress.GatewayRebindCommitReview){
		"version":         func(r *generatedingress.GatewayRebindCommitReview) { r.Version++ },
		"proposal_digest": func(r *generatedingress.GatewayRebindCommitReview) { r.ProposalDigest = strings.Repeat("a", 64) },
		"configure_digest": func(r *generatedingress.GatewayRebindCommitReview) {
			r.ConfigureApprovalDigest = strings.Repeat("a", 64)
		},
		"rebind_digest": func(r *generatedingress.GatewayRebindCommitReview) { r.RebindApprovalDigest = strings.Repeat("a", 64) },
		"roster":        func(r *generatedingress.GatewayRebindCommitReview) { r.Inspection.Roster[0].Port++ },
		"heads":         func(r *generatedingress.GatewayRebindCommitReview) { r.Inspection.RuntimeHeads[0].Generation++ },
		"redundant_checkpoint": func(r *generatedingress.GatewayRebindCommitReview) {
			r.Inspection.PredecessorCheckpointDigest = strings.Repeat("a", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, request, runtime, _ := newRebindHandlerFixture(t, "proposal")
			runtime.review = validRebindHandlerReview(t)
			mutate(&runtime.review)
			response := invokeRebindHandler(s, request, "proposal")
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "proposalDigest") {
				t.Fatalf("changed review=%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindAdapterSeparatesHistoricalAndCurrentProof(t *testing.T) {
	for _, kind := range []string{"active", "prepared", "successor_ready", "database_committed", "terminal_replay", "superseded", "rolled_back", "native_current"} {
		t.Run(kind, func(t *testing.T) {
			s, request, runtime, _ := newRebindHandlerFixture(t, "commit")
			status := http.StatusOK
			runtime.result.Created = false
			switch kind {
			case "active", "prepared", "successor_ready", "database_committed":
				state := appaccess.GatewayRebindState(kind)
				if kind == "active" {
					state = appaccess.GatewayRebindUnresolved
				}
				runtime.result = generatedingress.GatewayRebindCommitResponse{
					RecoveryRequired: true,
					Operation:        generatedingress.GatewayRebindOperationStatus{OperationID: rebindRequestOperation, StoredState: state, StateSequence: 5, DatabaseCommitObserved: state == appaccess.GatewayRebindDatabaseCommitted || state == appaccess.GatewayRebindUnresolved},
					TerminalProof:    generatedingress.GatewayRebindProofUnknown, CurrentProof: generatedingress.GatewayRebindProofUnknown,
				}
			case "superseded":
				runtime.result.TerminalProof, runtime.result.TerminalProofDigest = generatedingress.GatewayRebindProofUnavailable, ""
				runtime.result.SelectedCurrentAuthority.OperationID = rebindProfileOperation
			case "rolled_back":
				runtime.result.Created = true
				runtime.result.Operation.StoredState = appaccess.GatewayRebindRolledBack
				runtime.result.Operation.TerminalDisposition = appaccess.GatewayRebindDispositionAbort
				runtime.result.Operation.DatabaseCommitObserved = false
				status = http.StatusCreated
			case "native_current":
				runtime.result.SelectedCurrentAuthority.Kind = appaccess.GatewayRebindSourceGatewayUpgrade
				runtime.result.SelectedCurrentAuthority.TerminalReceiptDigest = ""
			}
			response := invokeRebindHandler(s, request, "commit")
			var result lanGatewayRebindMutation
			if response.Code != status || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
				result.Operation.StoredState != string(runtime.result.Operation.StoredState) || result.TerminalProof != string(runtime.result.TerminalProof) ||
				result.CurrentProof != string(runtime.result.CurrentProof) || result.RecoveryRequired != runtime.result.RecoveryRequired ||
				result.Operation.DatabaseCommitObserved != runtime.result.Operation.DatabaseCommitObserved || result.Created != runtime.result.Created {
				t.Fatalf("projection=%d %s", response.Code, response.Body.String())
			}
			if kind == "superseded" && (result.SelectedCurrentAuthority == nil || result.SelectedCurrentAuthority.OperationID == result.Operation.OperationID) {
				t.Fatal("historical operation replaced independently selected current authority")
			}
		})
	}
}

func TestLANGatewayRebindAdapterRejectsUnprovedSuccess(t *testing.T) {
	active := func(state appaccess.GatewayRebindState, databaseCommit bool) func(*generatedingress.GatewayRebindCommitResponse) {
		return func(r *generatedingress.GatewayRebindCommitResponse) {
			*r = generatedingress.GatewayRebindCommitResponse{
				RecoveryRequired: true,
				Operation:        generatedingress.GatewayRebindOperationStatus{OperationID: rebindRequestOperation, StoredState: state, StateSequence: 4, DatabaseCommitObserved: databaseCommit},
				TerminalProof:    generatedingress.GatewayRebindProofUnknown, CurrentProof: generatedingress.GatewayRebindProofUnknown,
			}
		}
	}
	for name, mutate := range map[string]func(*generatedingress.GatewayRebindCommitResponse){
		"prepared_database_commit":        active(appaccess.GatewayRebindPrepared, true),
		"successor_ready_database_commit": active(appaccess.GatewayRebindSuccessorReady, true),
		"missing_database_commit":         active(appaccess.GatewayRebindDatabaseCommitted, false),
		"unknown_terminal_proof": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.Created = false
			r.TerminalProof, r.TerminalProofDigest = generatedingress.GatewayRebindProofUnknown, ""
		},
		"unknown_current_proof": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.Created = false
			r.CurrentProof, r.CurrentAttestationDigest = generatedingress.GatewayRebindProofUnknown, ""
		},
		"invalid_current_kind": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.Kind = "private-unknown-kind"
		},
		"invalid_current_operation": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.OperationID = "private-invalid-operation"
		},
		"invalid_current_profile": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.ProfileRevisionID = ""
		},
		"invalid_current_revision": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.ProfileRevisionNumber = 0
		},
		"invalid_current_digest": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.ProfileSpecDigest = "bad"
		},
		"missing_current_receipt": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.TerminalReceiptDigest = ""
		},
		"native_current_receipt": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.SelectedCurrentAuthority.Kind = appaccess.GatewayRebindSourceGatewayUpgrade
		},
		"wrong_operation": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.Operation.OperationID = rebindProfileOperation
		},
		"zero_sequence":          func(r *generatedingress.GatewayRebindCommitResponse) { r.Operation.StateSequence = 0 },
		"unknown_state":          func(r *generatedingress.GatewayRebindCommitResponse) { r.Operation.StoredState = "ready" },
		"missing_commit_barrier": func(r *generatedingress.GatewayRebindCommitResponse) { r.Operation.DatabaseCommitObserved = false },
		"missing_receipt":        func(r *generatedingress.GatewayRebindCommitResponse) { r.Operation.TerminalReceiptDigest = "" },
		"terminal_unavailable": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.TerminalProof, r.TerminalProofDigest = generatedingress.GatewayRebindProofUnavailable, ""
		},
		"current_unavailable": func(r *generatedingress.GatewayRebindCommitResponse) {
			r.CurrentProof, r.CurrentAttestationDigest = generatedingress.GatewayRebindProofUnavailable, ""
		},
		"missing_current":        func(r *generatedingress.GatewayRebindCommitResponse) { r.SelectedCurrentAuthority = nil },
		"unreleased_fence":       func(r *generatedingress.GatewayRebindCommitResponse) { r.FenceReleased = false },
		"contradictory_recovery": func(r *generatedingress.GatewayRebindCommitResponse) { r.RecoveryRequired = true },
		"unknown_proof":          func(r *generatedingress.GatewayRebindCommitResponse) { r.CurrentProof = "ready" },
	} {
		t.Run(name, func(t *testing.T) {
			s, request, runtime, _ := newRebindHandlerFixture(t, "commit")
			mutate(&runtime.result)
			response := invokeRebindHandler(s, request, "commit")
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "terminalProof") {
				t.Fatalf("unproved success=%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLANGatewayRebindAdapterSanitizesRuntimeErrors(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		err    error
		status int
	}{
		{"approval", appaccess.ErrApprovalRequired, 403}, {"conflict", appaccess.ErrConflict, 409},
		{"not_found", appaccess.ErrNotFound, 503}, {"unknown", errors.New("sensitive-docker-config-and-credential"), 503},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s, request, runtime, _ := newRebindHandlerFixture(t, "commit")
			runtime.err = fixture.err
			response := invokeRebindHandler(s, request, "commit")
			if response.Code != fixture.status || response.Header().Get("Cache-Control") != "no-store" ||
				strings.Contains(response.Body.String(), "sensitive-") || strings.Contains(response.Body.String(), "terminalProof") {
				t.Fatalf("error=%d %s", response.Code, response.Body.String())
			}
		})
	}
}
