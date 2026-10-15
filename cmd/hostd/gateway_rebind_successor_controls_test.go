//go:build live_docker

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
)

// TestLiveHostdRebindPlanSuccessorProfile keeps the live Docker fixture's
// successor setup on the read-only path. The production admission checks the
// same synthetic identity against the current SQL authority before it claims
// any work; this test covers only fixture request construction.
func TestLiveHostdRebindPlanSuccessorProfile(t *testing.T) {
	target := apicontract.LANGatewayProfileSpec{
		InterfaceID: "private-ethernet", SelectedIpv4: "192.168.90.21", PortStart: 8000, PortEnd: 8002,
	}
	read := liveHostdRebindSuccessorProfileRead(t, target)

	t.Run("GET only preserves draft identity through proposal and commit", func(t *testing.T) {
		var observations sync.Mutex
		var ordinaryProfilePosts int
		var proposal struct {
			OperationID                    string                            `json:"operationId"`
			SuccessorProfileRevisionID     string                            `json:"successorProfileRevisionId"`
			SuccessorProfileRevisionNumber int64                             `json:"successorProfileRevisionNumber"`
			SuccessorProfileOperationID    string                            `json:"successorProfileOperationId"`
			SuccessorProfile               apicontract.LANGatewayProfileSpec `json:"successorProfile"`
		}
		fixture, closeServer := liveHostdRebindSuccessorControlFixture(t, func(w http.ResponseWriter, r *http.Request) {
			observations.Lock()
			defer observations.Unlock()
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/lan-gateway-profile":
				query := r.URL.Query()
				if query.Get("interfaceId") != target.InterfaceID || query.Get("selectedIpv4") != target.SelectedIpv4 ||
					query.Get("portStart") != "8000" || query.Get("portEnd") != "8002" || len(query) != 4 {
					t.Error("successor planner sent an unexpected profile query")
					http.Error(w, "invalid query", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(read); err != nil {
					t.Error("encode profile read")
				}
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/system/lan-gateway-profile":
				ordinaryProfilePosts++
				http.Error(w, "ordinary profile mutation is forbidden", http.StatusConflict)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/system/lan-gateway-rebind/proposal":
				if err := json.NewDecoder(r.Body).Decode(&proposal); err != nil {
					t.Error("decode rebind proposal")
					http.Error(w, "invalid proposal", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(liveHostdRebindReview{
					Version: 1, ProposalDigest: strings.Repeat("a", 64),
					ConfigureApprovalDigest: strings.Repeat("b", 64), RebindApprovalDigest: strings.Repeat("c", 64),
				}); err != nil {
					t.Error("encode rebind review")
				}
			default:
				http.Error(w, "unexpected request", http.StatusNotFound)
			}
		})
		defer closeServer()

		draft := liveHostdRebindPlanSuccessor(t, fixture, target)
		operationID := uuid.NewString()
		commit := liveHostdRebindPrepareRequest(t, fixture, operationID, draft)
		observations.Lock()
		defer observations.Unlock()
		if ordinaryProfilePosts != 0 {
			t.Fatal("successor planning attempted an ordinary profile mutation")
		}
		if _, err := uuid.Parse(draft.RevisionID); err != nil {
			t.Fatal("successor draft revision ID is not a UUID")
		}
		if _, err := uuid.Parse(draft.OperationID); err != nil {
			t.Fatal("successor draft operation ID is not a UUID")
		}
		if draft.RevisionID == draft.OperationID || draft.RevisionID == read.DesiredProfile.ID ||
			draft.RevisionID == read.DesiredProfile.OperationID || draft.OperationID == read.DesiredProfile.ID ||
			draft.OperationID == read.DesiredProfile.OperationID || draft.RevisionNumber != read.DesiredProfile.RevisionNumber+1 ||
			draft.Spec != target {
			t.Fatal("successor draft did not retain a fresh exact current-plus-one plan")
		}
		if proposal.OperationID != operationID || proposal.SuccessorProfileRevisionID != draft.RevisionID ||
			proposal.SuccessorProfileRevisionNumber != draft.RevisionNumber || proposal.SuccessorProfileOperationID != draft.OperationID ||
			proposal.SuccessorProfile != target {
			t.Fatal("rebind proposal changed the successor draft identity")
		}
		if commit["operationId"] != operationID || commit["successorProfileRevisionId"] != draft.RevisionID ||
			commit["successorProfileRevisionNumber"] != draft.RevisionNumber ||
			commit["successorProfileOperationId"] != draft.OperationID || commit["successorProfile"] != target {
			t.Fatal("rebind commit changed the reviewed successor draft identity")
		}
	})

	t.Run("rejects missing or inconsistent reads", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*apicontract.LANGatewayProfileRead)
		}{
			{name: "missing current profile", mutate: func(value *apicontract.LANGatewayProfileRead) { value.DesiredProfile = nil }},
			{name: "missing proposal", mutate: func(value *apicontract.LANGatewayProfileRead) { value.Proposal = nil }},
			{name: "current revision mismatch", mutate: func(value *apicontract.LANGatewayProfileRead) { value.ExpectedRevisionNumber++ }},
			{name: "zero current revision", mutate: func(value *apicontract.LANGatewayProfileRead) {
				value.DesiredProfile.RevisionNumber = 0
				value.ExpectedRevisionNumber = 0
			}},
			{name: "proposal target inconsistent", mutate: func(value *apicontract.LANGatewayProfileRead) { value.Proposal.Spec.SelectedIpv4 = "192.168.90.22" }},
			{name: "revision overflow", mutate: func(value *apicontract.LANGatewayProfileRead) {
				value.DesiredProfile.RevisionNumber = liveHostdRebindMaxRevisionNumber
				value.ExpectedRevisionNumber = liveHostdRebindMaxRevisionNumber
			}},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				candidate := read
				current := *read.DesiredProfile
				proposal := *read.Proposal
				candidate.DesiredProfile, candidate.Proposal = &current, &proposal
				test.mutate(&candidate)
				fixture, closeServer := liveHostdRebindSuccessorControlFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/api/v1/system/lan-gateway-profile" {
						http.Error(w, "unexpected request", http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(candidate); err != nil {
						t.Error("encode invalid profile read")
					}
				})
				defer closeServer()
				if _, err := liveHostdRebindPlanSuccessorProfile(fixture, target); err == nil {
					t.Fatal("inconsistent successor profile read was accepted")
				}
			})
		}
	})

	t.Run("fails closed for HTTP and decoding errors", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			status   int
			response string
		}{
			{name: "non-success status", status: http.StatusConflict, response: "{}"},
			{name: "invalid response", status: http.StatusOK, response: "{"},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				fixture, closeServer := liveHostdRebindSuccessorControlFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/api/v1/system/lan-gateway-profile" {
						http.Error(w, "unexpected request", http.StatusNotFound)
						return
					}
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(test.response))
				})
				defer closeServer()
				_, err := liveHostdRebindPlanSuccessorProfile(fixture, target)
				if err == nil {
					t.Fatal("invalid HTTP profile response was accepted")
				}
			})
		}
	})
}

func liveHostdRebindSuccessorProfileRead(t *testing.T, target apicontract.LANGatewayProfileSpec) apicontract.LANGatewayProfileRead {
	t.Helper()
	currentSpec := apicontract.LANGatewayProfileSpec{
		InterfaceID: "private-ethernet", SelectedIpv4: "192.168.90.20", PortStart: target.PortStart, PortEnd: target.PortEnd,
	}
	return apicontract.LANGatewayProfileRead{
		DesiredProfile: &apicontract.LANGatewayProfileRevision{
			ID: uuid.NewString(), RevisionNumber: 7, OperationID: uuid.NewString(), Spec: currentSpec,
		},
		ExpectedRevisionNumber: 7,
		Proposal:               &apicontract.LANGatewayProfileProposal{Spec: target},
	}
}

func liveHostdRebindSuccessorControlFixture(t *testing.T, handler http.HandlerFunc) (*liveHostdGatewayRebindFixture, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	return &liveHostdGatewayRebindFixture{
		ctx: context.Background(), listen: strings.TrimPrefix(server.URL, "http://"),
	}, server.Close
}
