package generatedingress

import (
	"errors"
	"math"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindSuccessorIntentVersion = 1
	gatewayRebindInitialSourceKind      = "migration-026-upgrade"
)

// gatewayRebindSuccessorIntent is a canonical, read-only model for the first
// rebind successor. Its source is a committed fixed-name v2 upgrade, which
// may occupy any protected history generation after earlier retired attempts;
// a later rebind needs a distinct prior-rebind receipt reader
// and cannot be represented by this source kind. This type does not persist an
// artifact or authorize a Docker, network, or database effect.
type gatewayRebindSuccessorIntent struct {
	Version            int                                  `json:"version"`
	Digest             string                               `json:"digest"`
	Claim              gatewayRebindSuccessorClaimBinding   `json:"claim"`
	Predecessor        gatewayRebindSuccessorSourceBinding  `json:"predecessor"`
	SuccessorProfile   gatewayRebindSuccessorIntentProfile  `json:"successorProfile"`
	Roster             []appaccess.GatewayRebindRosterEntry `json:"roster"`
	RosterEntryDigests []string                             `json:"rosterEntryDigests"`
	Network            gatewayRebindSuccessorIntentNetwork  `json:"network"`
	NetworkDigest      string                               `json:"networkDigest"`
	Identity           gatewayRebindSuccessorIdentity       `json:"identity"`
}

type gatewayRebindSuccessorClaimBinding struct {
	OperationID                   string             `json:"operationId"`
	RequestDigest                 string             `json:"requestDigest"`
	SpecDigest                    string             `json:"specDigest"`
	RebindApproval                appaccess.Approval `json:"rebindApproval"`
	ConfigureApproval             appaccess.Approval `json:"configureApproval"`
	SuccessorProfileRequestDigest string             `json:"successorProfileRequestDigest"`
	RosterDigest                  string             `json:"rosterDigest"`
	RosterCount                   int64              `json:"rosterCount"`
}

type gatewayRebindSuccessorSourceBinding struct {
	Kind           string                    `json:"kind"`
	Generation     uint64                    `json:"generation"`
	OperationID    string                    `json:"operationId"`
	Profile        gatewayProfileBinding     `json:"profile"`
	StateDigest    string                    `json:"stateDigest"`
	JournalDigest  string                    `json:"journalDigest"`
	IdentityDigest string                    `json:"identityDigest"`
	Resources      gatewayV2ResourceBindings `json:"resources"`
}

// These tagged projections pin the protected JSON shape independently of the
// public advisory preflight result's Go field names.
type gatewayRebindSuccessorIntentProfile struct {
	RevisionID     string `json:"revisionId"`
	RevisionNumber int64  `json:"revisionNumber"`
	OperationID    string `json:"operationId"`
	RequestDigest  string `json:"requestDigest"`
	SpecDigest     string `json:"specDigest"`
	SelectedIPv4   string `json:"selectedIpv4"`
	InterfaceID    string `json:"interfaceId"`
	PortStart      uint16 `json:"portStart"`
	PortEnd        uint16 `json:"portEnd"`
	ApprovedBy     string `json:"approvedBy"`
}

type gatewayRebindSuccessorIntentNetwork struct {
	Subnet        string `json:"subnet"`
	GatewayIPv4   string `json:"gatewayIpv4"`
	ContainerIPv4 string `json:"containerIpv4"`
}

// newGatewayRebindInitialSuccessorIntent models only the initial committed
// migration-026 predecessor and its next-generation rebind successor. Inputs
// must be freshly attested under the future writer's lock chain. Only a fresh
// locked history scan can prove the selection is the current committed head.
// This pure
// constructor checks their internal bindings but cannot establish freshness,
// administrator identity, protected-file provenance, or stable host/Docker
// inventory by itself.
func newGatewayRebindInitialSuccessorIntent(snapshot appaccess.GatewayRebindStartupSnapshot,
	predecessor gatewayUpgradeGenerationSelection,
	preflight GatewayRebindSuccessorPreflight,
) (gatewayRebindSuccessorIntent, error) {
	if predecessor.Generation == math.MaxUint64 {
		return gatewayRebindSuccessorIntent{}, errors.New("invalid initial gateway rebind successor intent input")
	}
	return newGatewayRebindInitialSuccessorIntentAtGeneration(snapshot, predecessor, preflight, predecessor.Generation+1)
}

// newGatewayRebindInitialSuccessorIntentAtGeneration retains the committed
// fixed-name predecessor while allowing a strict history scanner to reserve a
// later, globally unused successor generation. The protected writer must
// supply that reservation; this constructor does not discover history.
func newGatewayRebindInitialSuccessorIntentAtGeneration(snapshot appaccess.GatewayRebindStartupSnapshot,
	predecessor gatewayUpgradeGenerationSelection,
	preflight GatewayRebindSuccessorPreflight,
	successorGeneration uint64,
) (gatewayRebindSuccessorIntent, error) {
	invalid := errors.New("invalid initial gateway rebind successor intent input")
	state, journal := predecessor.State, predecessor.Journal
	if predecessor.Store == nil || !predecessor.Existing || predecessor.PartialState ||
		predecessor.Retired || predecessor.Aborted || successorGeneration == 0 ||
		successorGeneration <= predecessor.Generation ||
		predecessor.Store.generation != predecessor.Generation ||
		predecessor.operationID != state.OperationID ||
		(predecessor.Store.operationID != "" && predecessor.Store.operationID != state.OperationID) ||
		(predecessor.Generation > 0 && predecessor.Store.operationID == "") {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	if len(snapshot.Claims) != 1 || snapshot.CurrentProfile == nil ||
		!validGatewayV2RouteState(state) || !validGatewayMigrationJournal(journal) ||
		journal.Phase != gatewayPhaseCommitted || !journalMatchesV2Plan(journal, state) ||
		!gatewayRebindPredecessorMatches(snapshot, state, journal) {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	entry := snapshot.Claims[0]
	claim := entry.Claim
	spec := claim.Spec
	specDigest, err := appaccess.GatewayRebindSpecDigest(spec)
	if err != nil || claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		!claim.CreatedAt.Equal(claim.UpdatedAt) || claim.RebindApprovedAt.After(claim.CreatedAt) ||
		claim.ConfigureApprovedAt.After(claim.CreatedAt) ||
		claim.RebindApproval.Action != appaccess.ActionRebindGateway ||
		claim.RebindApproval.SpecDigest != specDigest || !validCanonicalUUID(claim.RebindApproval.ActorID) ||
		claim.ConfigureApproval.Action != appaccess.ActionConfigureGateway ||
		!validCanonicalUUID(claim.ConfigureApproval.ActorID) {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	configureDigest, err := appaccess.GatewayProfileSpecDigest(spec.SuccessorProfile)
	if err != nil || claim.ConfigureApproval.SpecDigest != configureDigest {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	claimDigest, err := canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpec `json:"spec"`
		Rebind    appaccess.Approval          `json:"rebindApproval"`
		Configure appaccess.Approval          `json:"configureApproval"`
	}{spec, claim.RebindApproval, claim.ConfigureApproval})
	if err != nil || claim.RequestDigest != claimDigest {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	profileRequestDigest, err := canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{spec.PredecessorProfileRevisionNumber, spec.SuccessorProfile, claim.ConfigureApproval})
	if err != nil || claim.SuccessorProfileRequestDigest != profileRequestDigest {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	profile := GatewayRebindSuccessorProfile{
		RevisionID: spec.SuccessorProfileRevisionID, RevisionNumber: spec.SuccessorProfileRevisionNumber,
		OperationID: spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
		SpecDigest: configureDigest, SelectedIPv4: spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: spec.SuccessorProfile.InterfaceID, PortStart: spec.SuccessorProfile.PortStart,
		PortEnd: spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
	}
	if preflight.RebindOperationID != spec.OperationID || preflight.ClaimRequestDigest != claim.RequestDigest ||
		preflight.Profile != profile ||
		!validGatewayV2NetworkPlan(gatewayV2NetworkPlan(preflight.Network)) ||
		!gatewayV2NetworkExcludesSelectedLAN(gatewayProfileBinding{
			RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
			SpecDigest: profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4,
			InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
		}, gatewayV2NetworkPlan(preflight.Network)) {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	if spec.RosterCount != int64(len(entry.Roster)) || len(entry.Roster) != len(entry.GrantBindings) {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	roster := make([]appaccess.GatewayRebindRosterEntry, len(entry.Roster))
	copy(roster, entry.Roster)
	entryDigests := make([]string, 0, len(roster))
	for index, item := range roster {
		itemDigest, err := appaccess.GatewayRebindRosterEntryDigest(item)
		if err != nil || item.Ordinal != int64(index+1) || item.OperationID != spec.OperationID ||
			item.EntryDigest != itemDigest || item.Port < profile.PortStart || item.Port > profile.PortEnd {
			return gatewayRebindSuccessorIntent{}, invalid
		}
		entryDigests = append(entryDigests, itemDigest)
	}
	rosterDigest, err := appaccess.GatewayRebindRosterDigest(roster)
	if err != nil || rosterDigest != spec.RosterDigest {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	stateDigest, err := canonicalDigest(state)
	if err != nil {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	journalDigest, err := canonicalDigest(journal)
	if err != nil {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	network := gatewayRebindSuccessorIntentNetwork(preflight.Network)
	networkDigest, err := canonicalDigest(struct {
		Version int                                 `json:"version"`
		Action  string                              `json:"action"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
	}{1, "rebind-successor-network", network})
	if err != nil {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	identity, err := newGatewayRebindSuccessorIdentity(successorGeneration, spec.OperationID, profile)
	if err != nil {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	intent := gatewayRebindSuccessorIntent{
		Version: gatewayRebindSuccessorIntentVersion,
		Claim: gatewayRebindSuccessorClaimBinding{
			OperationID: spec.OperationID, RequestDigest: claim.RequestDigest, SpecDigest: specDigest,
			RebindApproval: claim.RebindApproval, ConfigureApproval: claim.ConfigureApproval,
			SuccessorProfileRequestDigest: claim.SuccessorProfileRequestDigest,
			RosterDigest:                  spec.RosterDigest, RosterCount: spec.RosterCount,
		},
		Predecessor: gatewayRebindSuccessorSourceBinding{
			Kind: gatewayRebindInitialSourceKind, Generation: predecessor.Generation,
			OperationID: spec.PredecessorUpgradeOperationID, Profile: state.Profile,
			StateDigest: stateDigest, JournalDigest: journalDigest,
			IdentityDigest: state.Identity.Digest, Resources: journal.Resources,
		},
		SuccessorProfile: gatewayRebindSuccessorIntentProfile(profile),
		Roster:           roster, RosterEntryDigests: entryDigests,
		Network: network, NetworkDigest: networkDigest, Identity: identity,
	}
	intent.Digest, err = gatewayRebindInitialSuccessorIntentDigest(intent)
	if err != nil {
		return gatewayRebindSuccessorIntent{}, invalid
	}
	return intent, nil
}

func gatewayRebindInitialSuccessorIntentDigest(intent gatewayRebindSuccessorIntent) (string, error) {
	intent.Digest = ""
	return canonicalDigest(intent)
}

// validGatewayRebindInitialSuccessorIntent compares a candidate with the
// canonical construction from the exact attested inputs. A recomputed digest
// cannot make a substituted field or legacy v2 identity acceptable.
func validGatewayRebindInitialSuccessorIntent(snapshot appaccess.GatewayRebindStartupSnapshot,
	predecessor gatewayUpgradeGenerationSelection,
	preflight GatewayRebindSuccessorPreflight, candidate gatewayRebindSuccessorIntent,
) bool {
	if predecessor.Generation == math.MaxUint64 {
		return false
	}
	return validGatewayRebindInitialSuccessorIntentAtGeneration(
		snapshot, predecessor, preflight, predecessor.Generation+1, candidate,
	)
}

func validGatewayRebindInitialSuccessorIntentAtGeneration(snapshot appaccess.GatewayRebindStartupSnapshot,
	predecessor gatewayUpgradeGenerationSelection,
	preflight GatewayRebindSuccessorPreflight, successorGeneration uint64,
	candidate gatewayRebindSuccessorIntent,
) bool {
	expected, err := newGatewayRebindInitialSuccessorIntentAtGeneration(snapshot, predecessor, preflight, successorGeneration)
	return err == nil && reflect.DeepEqual(candidate, expected)
}
