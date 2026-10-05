package generatedingress

import (
	"errors"
	"fmt"
)

const (
	gatewayRebindSuccessorIdentityVersion = "v2-rebind-1"
	gatewayRebindHostnameHexDigits        = 32
)

// gatewayRebindSuccessorIdentity is a separate protected identity format. It
// cannot be passed to the fixed-name v2 upgrade identity validator.
type gatewayRebindSuccessorIdentity struct {
	Version               string `json:"version"`
	Digest                string `json:"digest"`
	Generation            uint64 `json:"generation"`
	OperationID           string `json:"operationId"`
	ProfileRevisionID     string `json:"profileRevisionId"`
	ProfileRevisionNumber int64  `json:"profileRevisionNumber"`
	ProfileSpecDigest     string `json:"profileSpecDigest"`
	CaddyImageDigest      string `json:"caddyImageDigest"`
	FinalContainer        string `json:"finalContainer"`
	StageContainer        string `json:"stageContainer"`
	ConfigVolume          string `json:"configVolume"`
	DataVolume            string `json:"dataVolume"`
	IngressNetwork        string `json:"ingressNetwork"`
	FinalHostname         string `json:"finalHostname"`
	StageHostname         string `json:"stageHostname"`
	StageConfigFilename   string `json:"stageConfigFilename"`
	ActiveConfigFilename  string `json:"activeConfigFilename"`
}

func newGatewayRebindSuccessorIdentity(generation uint64, operationID string, profile GatewayRebindSuccessorProfile) (gatewayRebindSuccessorIdentity, error) {
	if generation == 0 || !validCanonicalUUID(operationID) ||
		!validGatewayProfileBinding(gatewayProfileBinding{
			RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
			SpecDigest: profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4,
			InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
		}) || !validCanonicalUUID(profile.OperationID) || !validSHA256(profile.RequestDigest) ||
		!validCanonicalUUID(profile.ApprovedBy) {
		return gatewayRebindSuccessorIdentity{}, errors.New("invalid generated ingress rebind successor identity input")
	}
	finalHostname, err := gatewayRebindSuccessorHostname(generation, operationID, "f")
	if err != nil {
		return gatewayRebindSuccessorIdentity{}, err
	}
	stageHostname, err := gatewayRebindSuccessorHostname(generation, operationID, "s")
	if err != nil {
		return gatewayRebindSuccessorIdentity{}, err
	}
	identity := gatewayRebindSuccessorIdentity{
		Version: gatewayRebindSuccessorIdentityVersion, Generation: generation, OperationID: operationID,
		ProfileRevisionID: profile.RevisionID, ProfileRevisionNumber: profile.RevisionNumber,
		ProfileSpecDigest: profile.SpecDigest, CaddyImageDigest: gatewayV2CaddyImageDigest,
		FinalContainer:       gatewayRebindSuccessorResourceName("rig-generated-caddy-v2", generation, operationID),
		StageContainer:       gatewayRebindSuccessorResourceName("rig-generated-caddy-v2-stage", generation, operationID),
		ConfigVolume:         gatewayRebindSuccessorResourceName("rig-generated-caddy-config-v2", generation, operationID),
		DataVolume:           gatewayRebindSuccessorResourceName("rig-generated-caddy-data-v2", generation, operationID),
		IngressNetwork:       gatewayRebindSuccessorResourceName("rig-generated-caddy-ingress-v2", generation, operationID),
		FinalHostname:        finalHostname,
		StageHostname:        stageHostname,
		StageConfigFilename:  gatewayV2StageConfigFilename,
		ActiveConfigFilename: gatewayV2ActiveConfigFile,
	}
	digest, err := gatewayRebindSuccessorIdentityDigest(identity)
	if err != nil {
		return gatewayRebindSuccessorIdentity{}, err
	}
	identity.Digest = digest
	return identity, nil
}

func gatewayRebindSuccessorResourceName(prefix string, generation uint64, operationID string) string {
	return fmt.Sprintf("%s-g%0*d-%s", prefix, gatewayV2GenerationDigits, generation, operationID)
}

func gatewayRebindSuccessorHostname(generation uint64, operationID, role string) (string, error) {
	if generation == 0 || !validCanonicalUUID(operationID) || (role != "f" && role != "s") {
		return "", errors.New("invalid generated ingress rebind successor hostname input")
	}
	// A JSON array is the canonical, unambiguous encoding of the documented
	// four-element tuple. The operation ID is canonical before this is called.
	value, err := canonicalDigest([]any{gatewayRebindSuccessorIdentityVersion, generation, operationID, role})
	if err != nil {
		return "", err
	}
	return "rig-v2r-" + role + "-" + value[:gatewayRebindHostnameHexDigits], nil
}

// gatewayRebindSuccessorIdentityDigest validates every deterministic field
// before hashing. A caller cannot turn a substituted name into a new valid
// identity by merely recomputing its digest.
func gatewayRebindSuccessorIdentityDigest(identity gatewayRebindSuccessorIdentity) (string, error) {
	if identity.Version != gatewayRebindSuccessorIdentityVersion || identity.Generation == 0 ||
		!validCanonicalUUID(identity.OperationID) || !validCanonicalUUID(identity.ProfileRevisionID) ||
		identity.ProfileRevisionNumber <= 0 || !validSHA256(identity.ProfileSpecDigest) ||
		identity.CaddyImageDigest != gatewayV2CaddyImageDigest ||
		identity.FinalContainer != gatewayRebindSuccessorResourceName("rig-generated-caddy-v2", identity.Generation, identity.OperationID) ||
		identity.StageContainer != gatewayRebindSuccessorResourceName("rig-generated-caddy-v2-stage", identity.Generation, identity.OperationID) ||
		identity.ConfigVolume != gatewayRebindSuccessorResourceName("rig-generated-caddy-config-v2", identity.Generation, identity.OperationID) ||
		identity.DataVolume != gatewayRebindSuccessorResourceName("rig-generated-caddy-data-v2", identity.Generation, identity.OperationID) ||
		identity.IngressNetwork != gatewayRebindSuccessorResourceName("rig-generated-caddy-ingress-v2", identity.Generation, identity.OperationID) ||
		identity.StageConfigFilename != gatewayV2StageConfigFilename || identity.ActiveConfigFilename != gatewayV2ActiveConfigFile {
		return "", errors.New("invalid generated ingress rebind successor identity")
	}
	finalHostname, err := gatewayRebindSuccessorHostname(identity.Generation, identity.OperationID, "f")
	if err != nil {
		return "", err
	}
	stageHostname, err := gatewayRebindSuccessorHostname(identity.Generation, identity.OperationID, "s")
	if err != nil {
		return "", err
	}
	if identity.FinalHostname != finalHostname || identity.StageHostname != stageHostname ||
		identity.FinalHostname == identity.StageHostname || len(identity.FinalHostname) > 63 || len(identity.StageHostname) > 63 {
		return "", errors.New("invalid generated ingress rebind successor hostname")
	}
	identity.Digest = ""
	return canonicalDigest(identity)
}

// validGatewayRebindSuccessorIdentity binds the rebind operation and the
// successor profile revision ID, number, and spec digest. It does not
// authenticate the configure operation ID, request digest, or approver;
// effect paths must compare those with the claim and protected intent.
func validGatewayRebindSuccessorIdentity(generation uint64, operationID string, profile GatewayRebindSuccessorProfile, identity gatewayRebindSuccessorIdentity) bool {
	expected, err := newGatewayRebindSuccessorIdentity(generation, operationID, profile)
	return err == nil && identity == expected
}
