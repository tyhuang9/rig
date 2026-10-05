package generatedingress

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindProtectedIntentVersionV2        = 2
	gatewayRebindProtectedIntentPurposeV2        = "hostd/generated-ingress/rebind/successor-intent/v2"
	gatewayRebindProtectedIntentFilenamePrefixV2 = "gateway-rebind-successor-intent.v2.g"
)

// gatewayRebindProtectedIntentV2 is the durable typed-source successor intent.
// It never represents a prior rebind as migration-026 and contains no journal
// field. Its predecessor is the exact create-only checkpoint source reference.
type gatewayRebindProtectedIntentV2 struct {
	Version                  int                                      `json:"version"`
	Purpose                  string                                   `json:"purpose"`
	Generation               uint64                                   `json:"generation"`
	OperationID              string                                   `json:"operationId"`
	DatabaseDigest           string                                   `json:"databaseDigest"`
	Claim                    appaccess.GatewayRebindClaimV2           `json:"claim"`
	Predecessor              appaccess.GatewayRebindSourceRef         `json:"predecessor"`
	SuccessorProfile         gatewayRebindSuccessorIntentProfile      `json:"successorProfile"`
	Roster                   []appaccess.GatewayRebindRosterEntryV2   `json:"roster"`
	RosterEntryDigests       []string                                 `json:"rosterEntryDigests"`
	Network                  gatewayRebindSuccessorIntentNetwork      `json:"network"`
	NetworkDigest            string                                   `json:"networkDigest"`
	NetworkObservation       gatewayRebindSuccessorNetworkObservation `json:"networkObservation"`
	NetworkObservationDigest string                                   `json:"networkObservationDigest"`
	Identity                 gatewayRebindSuccessorIdentity           `json:"identity"`
	Digest                   string                                   `json:"digest"`
}

type gatewayRebindProtectedIntentV2Store struct {
	directory   *stateStore
	generation  uint64
	operationID string
	path        string
	purpose     string
}

type gatewayRebindProtectedIntentV2Selection struct {
	Store      *gatewayRebindProtectedIntentV2Store
	Generation uint64
	Intent     gatewayRebindProtectedIntentV2
	Existing   bool
}

func newGatewayRebindProtectedIntentV2(claim appaccess.GatewayRebindClaimV2,
	roster []appaccess.GatewayRebindRosterEntryV2, checkpoint gatewayRebindPredecessorCheckpoint,
	network gatewayRebindSuccessorNetworkObservation,
) (gatewayRebindProtectedIntentV2, error) {
	invalid := errors.New("invalid generated ingress typed rebind protected intent input")
	if !validGatewayRebindPredecessorCheckpoint(checkpoint) || claim.Spec != (appaccess.GatewayRebindSpecV2{}) &&
		!reflect.DeepEqual(claim.Spec.Predecessor, checkpoint.sourceRef()) {
		return gatewayRebindProtectedIntentV2{}, invalid
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(claim.Spec)
	profileDigest, profileErr := appaccess.GatewayProfileSpecDigest(claim.Spec.SuccessorProfile)
	requestDigest, requestErr := canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpecV2 `json:"spec"`
		Rebind    appaccess.Approval            `json:"rebindApproval"`
		Configure appaccess.Approval            `json:"configureApproval"`
	}{claim.Spec, claim.RebindApproval, claim.ConfigureApproval})
	profileRequestDigest, profileRequestErr := canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{claim.Spec.Predecessor.Lineage.ProfileRevisionNumber, claim.Spec.SuccessorProfile, claim.ConfigureApproval})
	if err != nil || profileErr != nil || requestErr != nil || profileRequestErr != nil ||
		claim.Spec.OperationID != checkpoint.OperationID || checkpoint.Generation == 0 ||
		claim.Spec.SuccessorProtectedGeneration != checkpoint.Generation ||
		claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		claim.RequestDigest != requestDigest || claim.SuccessorProfileRequestDigest != profileRequestDigest ||
		claim.RebindApproval.Action != appaccess.ActionRebindGateway || claim.RebindApproval.SpecDigest != specDigest ||
		claim.ConfigureApproval.Action != appaccess.ActionConfigureGateway ||
		claim.ConfigureApproval.SpecDigest != profileDigest || claim.RebindApprovedAt.After(claim.CreatedAt) ||
		claim.ConfigureApprovedAt.After(claim.CreatedAt) || !claim.CreatedAt.Equal(claim.UpdatedAt) {
		return gatewayRebindProtectedIntentV2{}, invalid
	}
	rosterCopy := append([]appaccess.GatewayRebindRosterEntryV2(nil), roster...)
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(rosterCopy)
	if err != nil || rosterDigest != claim.Spec.RosterDigest || int64(len(rosterCopy)) != claim.Spec.RosterCount {
		return gatewayRebindProtectedIntentV2{}, invalid
	}
	entryDigests := make([]string, len(rosterCopy))
	for index := range rosterCopy {
		digest, digestErr := appaccess.GatewayRebindRosterEntryV2Digest(rosterCopy[index])
		if digestErr != nil || rosterCopy[index].OperationID != claim.Spec.OperationID ||
			rosterCopy[index].Ordinal != int64(index+1) || rosterCopy[index].EntryDigest != digest {
			return gatewayRebindProtectedIntentV2{}, invalid
		}
		entryDigests[index] = digest
	}
	profile := gatewayRebindSuccessorIntentProfile{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
		SpecDigest: profileDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
	}
	if network.OperationID != claim.Spec.OperationID || network.ClaimRequestDigest != claim.RequestDigest ||
		network.ProfileSpecDigest != profile.SpecDigest ||
		!gatewayRebindNetworkObservationMatchesIntent(network, gatewayRebindSuccessorIntent{
			Version: gatewayRebindSuccessorIntentVersion,
			Claim: gatewayRebindSuccessorClaimBinding{OperationID: claim.Spec.OperationID,
				RequestDigest: claim.RequestDigest},
			SuccessorProfile: profile, Network: network.Plan,
		}) {
		return gatewayRebindProtectedIntentV2{}, invalid
	}
	plan := network.Plan
	networkDigest, err := canonicalDigest(struct {
		Version int                                 `json:"version"`
		Action  string                              `json:"action"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
	}{1, "rebind-successor-network", plan})
	observationDigest, observationErr := canonicalDigest(network)
	identity, identityErr := newGatewayRebindSuccessorIdentity(checkpoint.Generation, claim.Spec.OperationID,
		GatewayRebindSuccessorProfile(profile))
	databaseDigest, databaseErr := canonicalDigest(struct {
		Claim  appaccess.GatewayRebindClaimV2         `json:"claim"`
		Roster []appaccess.GatewayRebindRosterEntryV2 `json:"roster"`
	}{claim, rosterCopy})
	if err != nil || observationErr != nil || identityErr != nil || databaseErr != nil {
		return gatewayRebindProtectedIntentV2{}, invalid
	}
	value := gatewayRebindProtectedIntentV2{
		Version: gatewayRebindProtectedIntentVersionV2, Purpose: gatewayRebindProtectedIntentPurposeV2,
		Generation: checkpoint.Generation, OperationID: claim.Spec.OperationID, DatabaseDigest: databaseDigest,
		Claim: claim, Predecessor: checkpoint.sourceRef(), SuccessorProfile: profile,
		Roster: rosterCopy, RosterEntryDigests: entryDigests, Network: plan, NetworkDigest: networkDigest,
		NetworkObservation: network, NetworkObservationDigest: observationDigest, Identity: identity,
	}
	value.Digest, err = gatewayRebindProtectedIntentV2Digest(value)
	if err != nil || !validGatewayRebindProtectedIntentV2(value) {
		return gatewayRebindProtectedIntentV2{}, invalid
	}
	return value, nil
}

func gatewayRebindProtectedIntentV2Digest(value gatewayRebindProtectedIntentV2) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindProtectedIntentV2(value gatewayRebindProtectedIntentV2) bool {
	if value.Version != gatewayRebindProtectedIntentVersionV2 || value.Purpose != gatewayRebindProtectedIntentPurposeV2 ||
		value.Generation == 0 || value.Generation != value.Claim.Spec.SuccessorProtectedGeneration ||
		value.Generation <= value.Predecessor.Lineage.ProtectedGeneration ||
		value.OperationID != value.Claim.Spec.OperationID || value.Claim.Spec.Predecessor != value.Predecessor ||
		value.Claim.State != appaccess.GatewayRebindPrepared || value.Claim.StateSequence != 1 ||
		len(value.Roster) != len(value.RosterEntryDigests) || int64(len(value.Roster)) != value.Claim.Spec.RosterCount ||
		value.NetworkObservation.OperationID != value.OperationID ||
		value.NetworkObservation.ClaimRequestDigest != value.Claim.RequestDigest ||
		value.NetworkObservation.ProfileSpecDigest != value.SuccessorProfile.SpecDigest ||
		value.NetworkObservation.Plan != value.Network || value.Identity.Generation != value.Generation ||
		value.Identity.OperationID != value.OperationID || value.Identity.ProfileRevisionID != value.SuccessorProfile.RevisionID ||
		value.Identity.ProfileRevisionNumber != value.SuccessorProfile.RevisionNumber ||
		value.Identity.ProfileSpecDigest != value.SuccessorProfile.SpecDigest {
		return false
	}
	if !gatewayRebindNetworkObservationMatchesIntent(value.NetworkObservation, gatewayRebindSuccessorIntent{
		Version: gatewayRebindSuccessorIntentVersion,
		Claim: gatewayRebindSuccessorClaimBinding{OperationID: value.OperationID,
			RequestDigest: value.Claim.RequestDigest},
		SuccessorProfile: value.SuccessorProfile, Network: value.Network,
	}) {
		return false
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(value.Claim.Spec)
	profileDigest, profileErr := appaccess.GatewayProfileSpecDigest(value.Claim.Spec.SuccessorProfile)
	requestDigest, requestErr := canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpecV2 `json:"spec"`
		Rebind    appaccess.Approval            `json:"rebindApproval"`
		Configure appaccess.Approval            `json:"configureApproval"`
	}{value.Claim.Spec, value.Claim.RebindApproval, value.Claim.ConfigureApproval})
	profileRequestDigest, profileRequestErr := canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{value.Predecessor.Lineage.ProfileRevisionNumber, value.Claim.Spec.SuccessorProfile,
		value.Claim.ConfigureApproval})
	if err != nil || profileErr != nil || requestErr != nil || profileRequestErr != nil ||
		value.Claim.RequestDigest != requestDigest ||
		value.Claim.SuccessorProfileRequestDigest != profileRequestDigest ||
		value.Claim.RebindApproval.Action != appaccess.ActionRebindGateway ||
		value.Claim.RebindApproval.SpecDigest != specDigest ||
		value.Claim.ConfigureApproval.Action != appaccess.ActionConfigureGateway ||
		value.Claim.ConfigureApproval.SpecDigest != profileDigest ||
		!validCanonicalUUID(value.Claim.RebindApproval.ActorID) ||
		!validCanonicalUUID(value.Claim.ConfigureApproval.ActorID) ||
		value.Claim.RebindApprovedAt.After(value.Claim.CreatedAt) ||
		value.Claim.ConfigureApprovedAt.After(value.Claim.CreatedAt) ||
		!value.Claim.CreatedAt.Equal(value.Claim.UpdatedAt) {
		return false
	}
	expectedProfile := gatewayRebindSuccessorIntentProfile{
		RevisionID:     value.Claim.Spec.SuccessorProfileRevisionID,
		RevisionNumber: value.Claim.Spec.SuccessorProfileRevisionNumber,
		OperationID:    value.Claim.Spec.SuccessorProfileOperationID,
		RequestDigest:  value.Claim.SuccessorProfileRequestDigest,
		SpecDigest:     profileDigest,
		SelectedIPv4:   value.Claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID:    value.Claim.Spec.SuccessorProfile.InterfaceID,
		PortStart:      value.Claim.Spec.SuccessorProfile.PortStart,
		PortEnd:        value.Claim.Spec.SuccessorProfile.PortEnd,
		ApprovedBy:     value.Claim.ConfigureApproval.ActorID,
	}
	if !reflect.DeepEqual(value.SuccessorProfile, expectedProfile) {
		return false
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(value.Roster)
	if err != nil || rosterDigest != value.Claim.Spec.RosterDigest {
		return false
	}
	for index, entry := range value.Roster {
		digest, digestErr := appaccess.GatewayRebindRosterEntryV2Digest(entry)
		if digestErr != nil || digest != entry.EntryDigest || digest != value.RosterEntryDigests[index] ||
			entry.Ordinal != int64(index+1) || entry.OperationID != value.OperationID {
			return false
		}
	}
	networkDigest, err := canonicalDigest(struct {
		Version int                                 `json:"version"`
		Action  string                              `json:"action"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
	}{1, "rebind-successor-network", value.Network})
	observationDigest, observationErr := canonicalDigest(value.NetworkObservation)
	identity, identityErr := newGatewayRebindSuccessorIdentity(value.Generation, value.OperationID,
		GatewayRebindSuccessorProfile(value.SuccessorProfile))
	databaseDigest, databaseErr := canonicalDigest(struct {
		Claim  appaccess.GatewayRebindClaimV2         `json:"claim"`
		Roster []appaccess.GatewayRebindRosterEntryV2 `json:"roster"`
	}{value.Claim, value.Roster})
	digest, digestErr := gatewayRebindProtectedIntentV2Digest(value)
	return err == nil && observationErr == nil && identityErr == nil && databaseErr == nil && digestErr == nil &&
		value.NetworkDigest == networkDigest && value.NetworkObservationDigest == observationDigest &&
		reflect.DeepEqual(value.Identity, identity) && value.DatabaseDigest == databaseDigest && value.Digest == digest
}

func newGatewayRebindProtectedIntentV2Store(dataRoot string, generation uint64,
	operationID string,
) (*gatewayRebindProtectedIntentV2Store, error) {
	if generation == 0 || !validCanonicalUUID(operationID) {
		return nil, errors.New("invalid generated ingress typed rebind protected intent store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindProtectedIntentV2Name(generation, operationID)
	return &gatewayRebindProtectedIntentV2Store{directory: directory, generation: generation,
		operationID: operationID, path: filepath.Join(directory.root, name), purpose: purpose}, nil
}

func gatewayRebindProtectedIntentV2Name(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayRebindProtectedIntentFilenamePrefixV2 + token + ".bundle",
		gatewayRebindProtectedIntentPurposeV2 + "/" + scope
}

func (s *gatewayRebindProtectedIntentV2Store) installExact(value gatewayRebindProtectedIntentV2) error {
	if s == nil || s.directory == nil || value.Generation != s.generation || value.OperationID != s.operationID ||
		!validGatewayRebindProtectedIntentV2(value) {
		return errors.New("invalid generated ingress typed rebind protected intent install")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayRebindProtectedIntentBytes); err != nil {
		return err
	}
	installed, err := s.load()
	// The digest covers every serialized field. Compare that durable identity
	// after strict decode/validation instead of Go's in-memory time.Location
	// pointers, which do not survive a JSON round trip byte-for-byte.
	if err != nil || installed.Digest != value.Digest {
		return fmt.Errorf("generated ingress typed rebind protected intent readback mismatch: %v installed=%s expected=%s",
			err, installed.Digest, value.Digest)
	}
	return nil
}

func (s *gatewayRebindProtectedIntentV2Store) load() (gatewayRebindProtectedIntentV2, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindProtectedIntentV2{}, errors.New("invalid generated ingress typed rebind protected intent store")
	}
	var value gatewayRebindProtectedIntentV2
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayRebindProtectedIntentBytes, &value); err != nil {
		return gatewayRebindProtectedIntentV2{}, errors.New("invalid generated ingress typed rebind protected intent")
	}
	// EntryDigest is deliberately excluded from the shared roster entry's
	// canonical JSON because it is the digest of that entry. The protected
	// envelope carries the ordered digests separately, so restore those
	// derived fields before validating the decoded typed roster.
	if len(value.Roster) != len(value.RosterEntryDigests) {
		return gatewayRebindProtectedIntentV2{}, errors.New("invalid generated ingress typed rebind protected intent")
	}
	for index := range value.Roster {
		value.Roster[index].EntryDigest = value.RosterEntryDigests[index]
	}
	if value.Generation != s.generation || value.OperationID != s.operationID || !validGatewayRebindProtectedIntentV2(value) {
		return gatewayRebindProtectedIntentV2{}, errors.New("invalid generated ingress typed rebind protected intent")
	}
	return value, nil
}
