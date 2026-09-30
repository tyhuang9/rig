package controller

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/hostnetwork"
)

const (
	operationGetLANGatewayProfile       = "getLANGatewayProfile"
	operationConfigureLANGatewayProfile = "configureLANGatewayProfile"
	maxGatewayProfileRequestBodyBytes   = 16 << 10
)

var gatewayProposalQueryFields = [...]string{"interfaceId", "selectedIpv4", "portStart", "portEnd"}

func (s *Server) getLANGatewayProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetLANGatewayProfile, "lan_gateway_forbidden", "Administrator access is required") {
		return
	}
	if s.GatewayProfiles == nil {
		s.gatewayProfileProblem(w, r, operationGetLANGatewayProfile, appaccess.ErrInvalidStoredState)
		return
	}

	result := apicontract.LANGatewayProfileRead{Candidates: make([]apicontract.LANGatewayCandidate, 0)}
	current, err := s.GatewayProfiles.CurrentGatewayProfile(r.Context())
	switch {
	case err == nil:
		profile := contractLANGatewayProfileRevision(current)
		result.DesiredProfile = &profile
		result.ExpectedRevisionNumber = current.RevisionNumber
	case errors.Is(err, appaccess.ErrNotFound):
		// Revision zero is the explicit compare-and-swap value for an empty head.
	case err != nil:
		s.gatewayProfileProblem(w, r, operationGetLANGatewayProfile, err)
		return
	}

	candidates, err := s.currentGatewayCandidates()
	if err != nil {
		s.gatewayProfileProblem(w, r, operationGetLANGatewayProfile, err)
		return
	}
	for _, candidate := range candidates {
		result.Candidates = append(result.Candidates, apicontract.LANGatewayCandidate{
			InterfaceID:  candidate.InterfaceID,
			Name:         candidate.Name,
			SelectedIpv4: candidate.IPv4,
			Prefix:       candidate.Prefix.String(),
		})
	}

	proposal, requested, err := gatewayProfileProposal(r, candidates)
	if err != nil {
		s.gatewayProfileProblem(w, r, operationGetLANGatewayProfile, err)
		return
	}
	if requested {
		result.Proposal = &proposal
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) configureLANGatewayProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationConfigureLANGatewayProfile, "lan_gateway_forbidden", "Administrator access is required") {
		return
	}
	if s.GatewayProfiles == nil {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, appaccess.ErrInvalidStoredState)
		return
	}

	body, err := readConfigureLANGatewayProfileRequest(r)
	if err != nil || !validCanonicalUUID(body.OperationID) || body.ExpectedRevisionNumber < 0 || !validLowerHex(body.ApprovalDigest, 64) {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, appaccess.ErrInvalidInput)
		return
	}
	spec, err := appGatewayProfileSpec(body.Spec)
	if err != nil {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, err)
		return
	}
	digest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.ApprovalDigest), []byte(digest)) != 1 {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, appaccess.ErrApprovalRequired)
		return
	}
	actor := r.Context().Value(principalKey{}).(principal).user.ID
	input := appaccess.ConfigureGatewayInput{
		OperationID: body.OperationID, ExpectedRevisionNumber: body.ExpectedRevisionNumber, Spec: spec,
		Approval: appaccess.Approval{Action: appaccess.ActionConfigureGateway, SpecDigest: digest, ActorID: actor},
	}
	if s.respondToGatewayProfileReplay(w, r, input) {
		return
	}

	candidates, err := s.currentGatewayCandidates()
	if err != nil {
		if s.respondToGatewayProfileReplay(w, r, input) {
			return
		}
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, err)
		return
	}
	if _, err = hostnetwork.Select(candidates, spec.InterfaceID, spec.SelectedIPv4); err != nil {
		if s.respondToGatewayProfileReplay(w, r, input) {
			return
		}
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, err)
		return
	}
	profile, created, err := s.GatewayProfiles.ConfigureGatewayProfile(r.Context(), input)
	if err != nil {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, apicontract.LANGatewayProfileMutation{Profile: contractLANGatewayProfileRevision(profile), Created: created})
}

func (s *Server) respondToGatewayProfileReplay(w http.ResponseWriter, r *http.Request, input appaccess.ConfigureGatewayInput) bool {
	profile, err := s.GatewayProfiles.ReplayGatewayProfile(r.Context(), input)
	if errors.Is(err, appaccess.ErrNotFound) {
		return false
	}
	if err != nil {
		s.gatewayProfileProblem(w, r, operationConfigureLANGatewayProfile, err)
	} else {
		writeJSON(w, http.StatusOK, apicontract.LANGatewayProfileMutation{Profile: contractLANGatewayProfileRevision(profile), Created: false})
	}
	return true
}

func (s *Server) currentGatewayCandidates() ([]hostnetwork.Candidate, error) {
	if s.GatewayCandidates != nil {
		return s.GatewayCandidates()
	}
	return hostnetwork.CurrentCandidates()
}

func gatewayProfileProposal(r *http.Request, candidates []hostnetwork.Candidate) (apicontract.LANGatewayProfileProposal, bool, error) {
	query := r.URL.Query()
	for key := range query {
		known := false
		for _, expected := range gatewayProposalQueryFields {
			if key == expected {
				known = true
				break
			}
		}
		if !known {
			return apicontract.LANGatewayProfileProposal{}, false, appaccess.ErrInvalidInput
		}
	}
	requested := len(query) != 0
	values := make(map[string]string, len(gatewayProposalQueryFields))
	for _, field := range gatewayProposalQueryFields {
		items, present := query[field]
		if !requested && !present {
			continue
		}
		if !present || len(items) != 1 || items[0] == "" {
			return apicontract.LANGatewayProfileProposal{}, false, appaccess.ErrInvalidInput
		}
		values[field] = items[0]
	}
	if !requested {
		return apicontract.LANGatewayProfileProposal{}, false, nil
	}
	portStart, err := strconv.ParseUint(values["portStart"], 10, 16)
	if err != nil {
		return apicontract.LANGatewayProfileProposal{}, false, appaccess.ErrInvalidInput
	}
	portEnd, err := strconv.ParseUint(values["portEnd"], 10, 16)
	if err != nil {
		return apicontract.LANGatewayProfileProposal{}, false, appaccess.ErrInvalidInput
	}
	spec := appaccess.GatewayProfileSpec{
		SelectedIPv4: values["selectedIpv4"],
		InterfaceID:  values["interfaceId"],
		PortStart:    uint16(portStart),
		PortEnd:      uint16(portEnd),
	}
	digest, err := appaccess.GatewayProfileSpecDigest(spec)
	if err != nil {
		return apicontract.LANGatewayProfileProposal{}, false, err
	}
	if _, err := hostnetwork.Select(candidates, spec.InterfaceID, spec.SelectedIPv4); err != nil {
		return apicontract.LANGatewayProfileProposal{}, false, err
	}
	return apicontract.LANGatewayProfileProposal{Spec: contractLANGatewayProfileSpec(spec), ApprovalDigest: digest}, true, nil
}

func appGatewayProfileSpec(value apicontract.LANGatewayProfileSpec) (appaccess.GatewayProfileSpec, error) {
	if value.PortStart < 0 || value.PortStart > int(^uint16(0)) || value.PortEnd < 0 || value.PortEnd > int(^uint16(0)) {
		return appaccess.GatewayProfileSpec{}, appaccess.ErrInvalidInput
	}
	spec := appaccess.GatewayProfileSpec{
		SelectedIPv4: value.SelectedIpv4,
		InterfaceID:  value.InterfaceID,
		PortStart:    uint16(value.PortStart),
		PortEnd:      uint16(value.PortEnd),
	}
	if _, err := appaccess.GatewayProfileSpecDigest(spec); err != nil {
		return appaccess.GatewayProfileSpec{}, err
	}
	return spec, nil
}

func contractLANGatewayProfileSpec(value appaccess.GatewayProfileSpec) apicontract.LANGatewayProfileSpec {
	return apicontract.LANGatewayProfileSpec{
		SelectedIpv4: value.SelectedIPv4,
		InterfaceID:  value.InterfaceID,
		PortStart:    int(value.PortStart),
		PortEnd:      int(value.PortEnd),
	}
}

func contractLANGatewayProfileRevision(value appaccess.GatewayProfileRevision) apicontract.LANGatewayProfileRevision {
	return apicontract.LANGatewayProfileRevision{
		ID:             value.ID,
		RevisionNumber: value.RevisionNumber,
		OperationID:    value.OperationID,
		Spec:           contractLANGatewayProfileSpec(value.Spec),
		SpecDigest:     value.SpecDigest,
		ApprovedBy:     value.ApprovedBy,
		ApprovedAt:     contractTime(value.ApprovedAt),
	}
}

func (s *Server) gatewayProfileProblem(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, appaccess.ErrInvalidInput):
		s.handlerProblem(w, r, operation, http.StatusUnprocessableEntity, "invalid_lan_gateway_request", "Invalid LAN gateway profile request", 0)
	case errors.Is(err, hostnetwork.ErrUnavailable), errors.Is(err, hostnetwork.ErrAmbiguous):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_interface_changed", "Selected LAN interface or address is no longer uniquely available", 0)
	case errors.Is(err, appaccess.ErrConflict):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_conflict", "LAN gateway desired state changed or is currently pinned", 0)
	case errors.Is(err, appaccess.ErrIdempotencyMismatch):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_replay_conflict", "LAN gateway operation identifier was already used for different input", 0)
	case errors.Is(err, appaccess.ErrApprovalRequired):
		s.handlerProblem(w, r, operation, http.StatusConflict, "lan_gateway_approval_mismatch", "Confirmation does not approve this exact LAN gateway profile", 0)
	case errors.Is(err, appaccess.ErrInvalidStoredState):
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "lan_gateway_unavailable", "LAN gateway profile state is unavailable", 0)
	default:
		s.handlerProblem(w, r, operation, http.StatusServiceUnavailable, "lan_gateway_unavailable", "LAN gateway profile or host interface discovery is unavailable", 0)
	}
}

func readConfigureLANGatewayProfileRequest(r *http.Request) (apicontract.ConfigureLANGatewayProfileRequest, error) {
	defer r.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayProfileRequestBodyBytes+1))
	if err != nil || len(payload) > maxGatewayProfileRequestBodyBytes {
		return apicontract.ConfigureLANGatewayProfileRequest{}, appaccess.ErrInvalidInput
	}
	fields, err := decodeRequiredGatewayObject(payload, "operationId", "expectedRevisionNumber", "spec", "approvalDigest")
	if err != nil {
		return apicontract.ConfigureLANGatewayProfileRequest{}, err
	}
	var result apicontract.ConfigureLANGatewayProfileRequest
	if err = decodeRequiredGatewayScalar(fields["operationId"], &result.OperationID); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(fields["expectedRevisionNumber"], &result.ExpectedRevisionNumber); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(fields["approvalDigest"], &result.ApprovalDigest); err != nil {
		return result, err
	}
	specFields, err := decodeRequiredGatewayObject(fields["spec"], "selectedIpv4", "interfaceId", "portStart", "portEnd")
	if err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(specFields["selectedIpv4"], &result.Spec.SelectedIpv4); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(specFields["interfaceId"], &result.Spec.InterfaceID); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(specFields["portStart"], &result.Spec.PortStart); err != nil {
		return result, err
	}
	if err = decodeRequiredGatewayScalar(specFields["portEnd"], &result.Spec.PortEnd); err != nil {
		return result, err
	}
	return result, nil
}

func decodeRequiredGatewayObject(payload []byte, required ...string) (map[string]json.RawMessage, error) {
	allowed := make(map[string]struct{}, len(required))
	for _, field := range required {
		allowed[field] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, appaccess.ErrInvalidInput
	}
	fields := make(map[string]json.RawMessage, len(required))
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return nil, appaccess.ErrInvalidInput
		}
		if _, ok = allowed[name]; !ok {
			return nil, appaccess.ErrInvalidInput
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, appaccess.ErrInvalidInput
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return nil, appaccess.ErrInvalidInput
		}
		fields[name] = raw
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, appaccess.ErrInvalidInput
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return nil, appaccess.ErrInvalidInput
		}
	}
	return fields, nil
}

func decodeRequiredGatewayScalar(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(trimmed, destination) != nil {
		return appaccess.ErrInvalidInput
	}
	return nil
}
