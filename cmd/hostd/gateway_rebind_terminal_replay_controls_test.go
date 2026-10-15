//go:build live_docker

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
)

func TestLiveHostdRebindNormalReplayControls(t *testing.T) {
	t.Run("retains exact request bytes", func(t *testing.T) {
		fixture := &liveHostdGatewayRebindFixture{}
		operationID := "operation-retained"
		request := []byte(`{"operationId":"operation-retained","approval":"reviewed"}`)
		if err := fixture.retainCommitRequest(operationID, request); err != nil {
			t.Fatal(err)
		}
		request[0] = '['
		retained, ok := fixture.retainedCommitRequest(operationID)
		if !ok || !bytes.Equal(retained, []byte(`{"operationId":"operation-retained","approval":"reviewed"}`)) {
			t.Fatal("retained request changed after caller mutation")
		}
		retained[0] = '['
		readAgain, ok := fixture.retainedCommitRequest(operationID)
		if !ok || !bytes.Equal(readAgain, []byte(`{"operationId":"operation-retained","approval":"reviewed"}`)) {
			t.Fatal("retained request changed after reader mutation")
		}
		if err := fixture.retainCommitRequest(operationID, []byte(`{"operationId":"operation-retained","approval":"reviewed"}`)); err != nil {
			t.Fatal("unchanged retained request was rejected")
		}
		if err := fixture.retainCommitRequest(operationID, []byte(`{"operationId":"operation-retained","approval":"different"}`)); err == nil {
			t.Fatal("different retained request was accepted")
		}
		if err := fixture.retainCommitRequest(operationID, []byte(`{"operationId":"other","approval":"reviewed"}`)); err == nil {
			t.Fatal("mismatched embedded operation was accepted")
		}
	})

	for _, disposition := range []string{"commit", "abort"} {
		t.Run("replays reviewed "+disposition+" request without creation", func(t *testing.T) {
			operationID := "operation-" + disposition
			diagnostic := liveHostdRebindTerminalControlStatus(operationID, disposition)
			replay := liveHostdRebindTerminalControlReplay(diagnostic)
			request := []byte(`{"operationId":"` + operationID + `","version":1,"proposalDigest":"reviewed"}`)
			var calls []string
			var posted []byte
			var mu sync.Mutex
			fixture := liveHostdRebindTerminalControlFixture(t, func(writer http.ResponseWriter, requestIn *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, requestIn.Method+" "+requestIn.URL.Path)
				switch requestIn.Method + " " + requestIn.URL.Path {
				case http.MethodGet + " /api/v1/system/lan-gateway-rebind/" + operationID:
					_ = json.NewEncoder(writer).Encode(diagnostic)
				case http.MethodPost + " /api/v1/system/lan-gateway-rebind":
					posted, _ = io.ReadAll(requestIn.Body)
					_ = json.NewEncoder(writer).Encode(replay)
				default:
					writer.WriteHeader(http.StatusNotFound)
				}
			})
			if err := fixture.retainCommitRequest(operationID, request); err != nil {
				t.Fatal(err)
			}
			status, err := liveHostdRebindNormalReplay(fixture, operationID)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			captured, recordedCalls := append([]byte(nil), posted...), append([]string(nil), calls...)
			mu.Unlock()
			if !bytes.Equal(captured, request) || len(recordedCalls) != 2 || recordedCalls[0] != http.MethodGet+" /api/v1/system/lan-gateway-rebind/"+operationID ||
				recordedCalls[1] != http.MethodPost+" /api/v1/system/lan-gateway-rebind" || status.TerminalProof != "verified" ||
				status.CurrentProof != "verified" || status.Operation == nil || status.Operation.TerminalReceiptDigest != diagnostic.Operation.TerminalReceiptDigest ||
				status.TerminalDigest == status.Operation.TerminalReceiptDigest {
				t.Fatal("normal replay did not retain exact request and independent proof identities")
			}
		})
	}

	t.Run("missing retained request makes no post", func(t *testing.T) {
		operationID := "operation-missing"
		diagnostic := liveHostdRebindTerminalControlStatus(operationID, "commit")
		posts := 0
		var mu sync.Mutex
		fixture := liveHostdRebindTerminalControlFixture(t, func(writer http.ResponseWriter, request *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if request.Method == http.MethodPost {
				posts++
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(writer).Encode(diagnostic)
		})
		_, replayErr := liveHostdRebindNormalReplay(fixture, operationID)
		mu.Lock()
		postCount := posts
		mu.Unlock()
		if replayErr == nil || postCount != 0 {
			t.Fatal("missing request reached normal replay post")
		}
	})

	t.Run("does not upgrade a nonterminal diagnostic", func(t *testing.T) {
		operationID := "operation-active"
		diagnostic := liveHostdRebindTerminalControlStatus(operationID, "commit")
		diagnostic.Active, diagnostic.Historical = true, false
		posts := 0
		var mu sync.Mutex
		fixture := liveHostdRebindTerminalControlFixture(t, func(writer http.ResponseWriter, request *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if request.Method == http.MethodPost {
				posts++
			}
			_ = json.NewEncoder(writer).Encode(diagnostic)
		})
		if err := fixture.retainCommitRequest(operationID, []byte(`{"operationId":"operation-active"}`)); err != nil {
			t.Fatal(err)
		}
		_, replayErr := liveHostdRebindNormalReplay(fixture, operationID)
		mu.Lock()
		postCount := posts
		mu.Unlock()
		if replayErr == nil || postCount != 0 {
			t.Fatal("nonterminal diagnostic was upgraded")
		}
	})

	t.Run("replay failure cannot fall back to diagnostic", func(t *testing.T) {
		operationID := "operation-post-failure"
		diagnostic := liveHostdRebindTerminalControlStatus(operationID, "commit")
		var gets, posts int
		var mu sync.Mutex
		fixture := liveHostdRebindTerminalControlFixture(t, func(writer http.ResponseWriter, request *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if request.Method == http.MethodGet {
				gets++
				_ = json.NewEncoder(writer).Encode(diagnostic)
				return
			}
			posts++
			writer.WriteHeader(http.StatusInternalServerError)
		})
		if err := fixture.retainCommitRequest(operationID, []byte(`{"operationId":"operation-post-failure"}`)); err != nil {
			t.Fatal(err)
		}
		_, replayErr := liveHostdRebindNormalReplay(fixture, operationID)
		mu.Lock()
		getCount, postCount := gets, posts
		mu.Unlock()
		if replayErr == nil || getCount != 1 || postCount != 1 {
			t.Fatal("failed replay fell back to its diagnostic read")
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*liveHostdRebindStatus, *apicontract.LANGatewayRebindMutation)
	}{
		{name: "created", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) { replay.Created = true }},
		{name: "recovery required", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.RecoveryRequired = true
		}},
		{name: "fence retained", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.FenceReleased = false
		}},
		{name: "terminal unavailable", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.TerminalProof = "unavailable"
			replay.TerminalProofDigest = ""
		}},
		{name: "superseded terminal unavailable", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.TerminalProof = "unavailable"
			replay.TerminalProofDigest = ""
			replay.CurrentProof = "verified"
		}},
		{name: "current unavailable", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.CurrentProof = "unavailable"
			replay.CurrentAttestationDigest = ""
		}},
		{name: "missing terminal proof digest", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.TerminalProofDigest = ""
		}},
		{name: "malformed proof digest", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.TerminalProofDigest = strings.Repeat("A", 64)
		}},
		{name: "operation mismatch", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.Operation.OperationID = "other-operation"
		}},
		{name: "terminal receipt mismatch", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.Operation.TerminalReceiptDigest = strings.Repeat("9", 64)
		}},
		{name: "state sequence mismatch", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.Operation.StateSequence++
		}},
		{name: "disposition mismatch", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.Operation.TerminalDisposition = "abort"
			replay.Operation.StoredState = string(appaccess.GatewayRebindRolledBack)
			replay.Operation.DatabaseCommitObserved = false
		}},
		{name: "database observation mismatch", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.Operation.DatabaseCommitObserved = false
		}},
		{name: "current authority mismatch", mutate: func(_ *liveHostdRebindStatus, replay *apicontract.LANGatewayRebindMutation) {
			replay.SelectedCurrentAuthority.ProfileRevisionID = "other-profile"
		}},
	} {
		t.Run("rejects "+testCase.name, func(t *testing.T) {
			operationID := "operation-reject"
			diagnostic := liveHostdRebindTerminalControlStatus(operationID, "commit")
			replay := liveHostdRebindTerminalControlReplay(diagnostic)
			testCase.mutate(&diagnostic, &replay)
			fixture := liveHostdRebindTerminalControlFixture(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet {
					_ = json.NewEncoder(writer).Encode(diagnostic)
					return
				}
				_ = json.NewEncoder(writer).Encode(replay)
			})
			if err := fixture.retainCommitRequest(operationID, []byte(`{"operationId":"operation-reject"}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := liveHostdRebindNormalReplay(fixture, operationID); err == nil {
				t.Fatal("invalid normal replay was accepted")
			}
		})
	}
}

func TestLiveHostdRebindTerminalReceiptBindingControls(t *testing.T) {
	status := liveHostdRebindTerminalControlStatus("operation-receipt", "commit")
	status.TerminalProof, status.CurrentProof = "verified", "verified"
	status.TerminalDigest, status.CurrentDigest = strings.Repeat("e", 64), strings.Repeat("f", 64)
	receipt := liveHostdRebindTerminalReceipt{
		OperationID: status.Operation.OperationID, Disposition: appaccess.GatewayRebindDispositionCommit,
		Digest: status.Operation.TerminalReceiptDigest,
	}
	command := appaccess.GatewayRebindTransitionCommand{
		OperationID: status.Operation.OperationID, Sequence: status.Operation.StateSequence,
		NextState: appaccess.GatewayRebindCommitted, TerminalDisposition: appaccess.GatewayRebindDispositionCommit,
		TerminalReceiptDigest: receipt.Digest,
	}
	if !liveHostdRebindTerminalReceiptBound(status, receipt, command) {
		t.Fatal("matching terminal receipt binding was rejected")
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*liveHostdRebindTerminalReceipt, *appaccess.GatewayRebindTransitionCommand)
	}{
		{name: "protected receipt", mutate: func(receipt *liveHostdRebindTerminalReceipt, _ *appaccess.GatewayRebindTransitionCommand) {
			receipt.Digest = strings.Repeat("9", 64)
		}},
		{name: "sql receipt", mutate: func(_ *liveHostdRebindTerminalReceipt, command *appaccess.GatewayRebindTransitionCommand) {
			command.TerminalReceiptDigest = strings.Repeat("9", 64)
		}},
		{name: "sql sequence", mutate: func(_ *liveHostdRebindTerminalReceipt, command *appaccess.GatewayRebindTransitionCommand) {
			command.Sequence++
		}},
		{name: "sql disposition", mutate: func(_ *liveHostdRebindTerminalReceipt, command *appaccess.GatewayRebindTransitionCommand) {
			command.TerminalDisposition = appaccess.GatewayRebindDispositionAbort
		}},
	} {
		t.Run("rejects mismatched "+testCase.name, func(t *testing.T) {
			candidateReceipt, candidateCommand := receipt, command
			testCase.mutate(&candidateReceipt, &candidateCommand)
			if liveHostdRebindTerminalReceiptBound(status, candidateReceipt, candidateCommand) {
				t.Fatal("mismatched terminal receipt binding was accepted")
			}
		})
	}
}

func liveHostdRebindTerminalControlFixture(t *testing.T,
	handler http.HandlerFunc,
) *liveHostdGatewayRebindFixture {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &liveHostdGatewayRebindFixture{
		ctx: context.Background(), listen: strings.TrimPrefix(server.URL, "http://"),
		session: auth.Session{Token: "terminal-replay-control", CSRF: "terminal-replay-control"},
	}
}

func liveHostdRebindTerminalControlStatus(operationID, disposition string) liveHostdRebindStatus {
	operation := apicontract.LANGatewayRebindOperation{
		OperationID: operationID, StateSequence: 7, TerminalDisposition: disposition,
		TerminalReceiptDigest: strings.Repeat("c", 64),
	}
	authority := apicontract.LANGatewayRebindCurrentAuthority{
		ProfileRevisionID: "profile-current", ProfileRevisionNumber: 2,
		ProfileSpecDigest: strings.Repeat("d", 64),
	}
	switch disposition {
	case "commit":
		operation.StoredState, operation.DatabaseCommitObserved = string(appaccess.GatewayRebindCommitted), true
		authority.Kind, authority.OperationID = string(appaccess.GatewayRebindSourceGatewayRebind), operationID
		authority.TerminalReceiptDigest = operation.TerminalReceiptDigest
	case "abort":
		operation.StoredState = string(appaccess.GatewayRebindRolledBack)
		authority.Kind, authority.OperationID = string(appaccess.GatewayRebindSourceGatewayUpgrade), "predecessor-upgrade"
	}
	return liveHostdRebindStatus{
		Version: 1, OperationID: operationID, Availability: "available", Historical: true,
		TerminalProof: "unavailable", CurrentProof: "unavailable", OperationFence: "released",
		Operation: &operation, SelectedCurrent: &authority,
	}
}

func liveHostdRebindTerminalControlReplay(status liveHostdRebindStatus) apicontract.LANGatewayRebindMutation {
	operation, authority := *status.Operation, *status.SelectedCurrent
	return apicontract.LANGatewayRebindMutation{
		Created: false, RecoveryRequired: false, FenceReleased: true, Operation: operation,
		TerminalProof: "verified", TerminalProofDigest: strings.Repeat("a", 64),
		CurrentProof: "verified", CurrentAttestationDigest: strings.Repeat("b", 64),
		SelectedCurrentAuthority: &authority,
	}
}
