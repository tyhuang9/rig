package controller

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

const maxGatewayRebindRequestBodyBytes = 16 << 10

// These private request values carry review input, never an actor or physical
// proof. Admission must independently reconstruct and authorize the proposal
// under the ingress writer locks.
type lanGatewayRebindCommitRequest struct {
	Version                 int
	Proposal                generatedingress.GatewayRebindProposalInput
	ProposalDigest          string
	ConfigureApprovalDigest string
	RebindApprovalDigest    string
}

type lanGatewayRebindReconcileRequest struct {
	Version                int
	OperationID            string
	Action                 string
	RecoveryDigest         string
	RecoveryApprovalDigest string
}

func readLANGatewayRebindProposalRequest(r *http.Request) (generatedingress.GatewayRebindProposalInput, error) {
	fields, err := readLANGatewayRebindFields(r, "operationId", "successorProfileRevisionId", "successorProfileRevisionNumber", "successorProfileOperationId", "successorProfile")
	if err != nil {
		return generatedingress.GatewayRebindProposalInput{}, err
	}
	return decodeLANGatewayRebindProposal(fields)
}

func readLANGatewayRebindCommitRequest(r *http.Request) (lanGatewayRebindCommitRequest, error) {
	fields, err := readLANGatewayRebindFields(r, "operationId", "successorProfileRevisionId", "successorProfileRevisionNumber", "successorProfileOperationId", "successorProfile", "version", "proposalDigest", "configureApprovalDigest", "rebindApprovalDigest")
	if err != nil {
		return lanGatewayRebindCommitRequest{}, err
	}
	var result lanGatewayRebindCommitRequest
	for name, target := range map[string]any{
		"version": &result.Version, "proposalDigest": &result.ProposalDigest,
		"configureApprovalDigest": &result.ConfigureApprovalDigest, "rebindApprovalDigest": &result.RebindApprovalDigest,
	} {
		if err := decodeRequiredGatewayScalar(fields[name], target); err != nil {
			return lanGatewayRebindCommitRequest{}, err
		}
	}
	if result.Version != 1 || !validLowerHex(result.ProposalDigest, 64) ||
		!validLowerHex(result.ConfigureApprovalDigest, 64) || !validLowerHex(result.RebindApprovalDigest, 64) {
		return lanGatewayRebindCommitRequest{}, appaccess.ErrInvalidInput
	}
	result.Proposal, err = decodeLANGatewayRebindProposal(fields)
	if err != nil {
		return lanGatewayRebindCommitRequest{}, err
	}
	return result, nil
}

// operationID comes from the matched route. The JSON body cannot override it,
// supply an actor, or choose rollback/forward recovery policy.
func readLANGatewayRebindReconcileRequest(r *http.Request, operationID string) (lanGatewayRebindReconcileRequest, error) {
	fields, err := readLANGatewayRebindFields(r, "version", "action", "recoveryDigest", "recoveryApprovalDigest")
	if err != nil {
		return lanGatewayRebindReconcileRequest{}, err
	}
	var result lanGatewayRebindReconcileRequest
	for name, target := range map[string]any{
		"version": &result.Version, "action": &result.Action,
		"recoveryDigest": &result.RecoveryDigest, "recoveryApprovalDigest": &result.RecoveryApprovalDigest,
	} {
		if err := decodeRequiredGatewayScalar(fields[name], target); err != nil {
			return lanGatewayRebindReconcileRequest{}, err
		}
	}
	if !validCanonicalUUID(operationID) || result.Version != 1 || result.Action != "reconcile" ||
		!validLowerHex(result.RecoveryDigest, 64) || !validLowerHex(result.RecoveryApprovalDigest, 64) {
		return lanGatewayRebindReconcileRequest{}, appaccess.ErrInvalidInput
	}
	result.OperationID = operationID
	return result, nil
}

func readLANGatewayRebindFields(r *http.Request, required ...string) (map[string]json.RawMessage, error) {
	if r == nil || r.Body == nil {
		return nil, appaccess.ErrInvalidInput
	}
	defer r.Body.Close()
	if r.Method != http.MethodPost || r.URL == nil || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		return nil, appaccess.ErrInvalidInput
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return nil, appaccess.ErrInvalidInput
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayRebindRequestBodyBytes+1))
	if len(payload) > maxGatewayRebindRequestBodyBytes {
		return nil, &http.MaxBytesError{Limit: maxGatewayRebindRequestBodyBytes}
	}
	if err != nil || len(payload) == 0 || !utf8.Valid(payload) {
		return nil, appaccess.ErrInvalidInput
	}
	return decodeRequiredGatewayObject(payload, required...)
}

func decodeLANGatewayRebindProposal(fields map[string]json.RawMessage) (generatedingress.GatewayRebindProposalInput, error) {
	var result generatedingress.GatewayRebindProposalInput
	for name, target := range map[string]any{
		"operationId": &result.OperationID, "successorProfileRevisionId": &result.SuccessorProfileRevisionID,
		"successorProfileRevisionNumber": &result.SuccessorProfileRevisionNumber, "successorProfileOperationId": &result.SuccessorProfileOperationID,
	} {
		if err := decodeRequiredGatewayScalar(fields[name], target); err != nil {
			return generatedingress.GatewayRebindProposalInput{}, err
		}
	}
	if !validCanonicalUUID(result.OperationID) || !validCanonicalUUID(result.SuccessorProfileRevisionID) ||
		!validCanonicalUUID(result.SuccessorProfileOperationID) || result.OperationID == result.SuccessorProfileOperationID ||
		result.SuccessorProfileRevisionNumber <= 0 {
		return generatedingress.GatewayRebindProposalInput{}, appaccess.ErrInvalidInput
	}
	profileFields, err := decodeRequiredGatewayObject(fields["successorProfile"], "interfaceId", "selectedIpv4", "portStart", "portEnd")
	if err != nil {
		return generatedingress.GatewayRebindProposalInput{}, err
	}
	var profile apicontract.LANGatewayProfileSpec
	for name, target := range map[string]any{
		"interfaceId": &profile.InterfaceID, "selectedIpv4": &profile.SelectedIpv4,
		"portStart": &profile.PortStart, "portEnd": &profile.PortEnd,
	} {
		if err := decodeRequiredGatewayScalar(profileFields[name], target); err != nil {
			return generatedingress.GatewayRebindProposalInput{}, err
		}
	}
	result.SuccessorProfile, err = appGatewayProfileSpec(profile)
	if err != nil {
		return generatedingress.GatewayRebindProposalInput{}, err
	}
	return result, nil
}
