package controller

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

const operationGetControllerRecovery = "getControllerRecovery"

var errControllerRecoveryUnavailable = errors.New("controller recovery discovery is unavailable")

// ControllerRecoveryDiscovery is the normal-mode, read-only proof of an
// active gateway rebind. An empty operation ID is a validated absence result.
type ControllerRecoveryDiscovery interface {
	ActiveGatewayRebind(context.Context) (string, error)
}

// ControllerRecoverySnapshotService returns one fully validated durable
// gateway census without changing claims, acknowledgements, or recovery state.
type ControllerRecoverySnapshotService interface {
	HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
}

// ControllerRecoveryStartupRuntime is deliberately observation-only. The
// discovery endpoint cannot advance or retire protected recovery state.
type ControllerRecoveryStartupRuntime interface {
	InspectGatewayV2Startup(context.Context, []generatedingress.GatewayV2StartupClaim) (generatedingress.GatewayV2StartupInspection, error)
	InspectGatewayV2LANAccessStartup(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) (generatedingress.GatewayV2LANAccessStartupInspection, error)
}

type gatewayRebindOperationInspector interface {
	InspectGatewayRebindOperation(context.Context, generatedingress.GatewayRebindRecoveryReadRepository,
		string) (generatedingress.GatewayRebindOperationInspection, error)
}

type gatewayRebindControllerRecoveryDiscovery struct {
	dataRoot   string
	repository generatedingress.GatewayRebindRecoveryReadRepository
	inspector  gatewayRebindOperationInspector
}

// NewControllerRecoveryDiscovery constructs the normal-mode rebind locator
// reader. It does not create protected directories or recover an operation.
func NewControllerRecoveryDiscovery(dataRoot string,
	repository generatedingress.GatewayRebindRecoveryReadRepository,
	inspector gatewayRebindOperationInspector,
) ControllerRecoveryDiscovery {
	return &gatewayRebindControllerRecoveryDiscovery{
		dataRoot: dataRoot, repository: repository, inspector: inspector,
	}
}

func (d *gatewayRebindControllerRecoveryDiscovery) ActiveGatewayRebind(ctx context.Context) (string, error) {
	if d == nil || ctx == nil || d.repository == nil {
		return "", errControllerRecoveryUnavailable
	}
	first, err := generatedingress.InspectGatewayRebindStartupPresence(ctx, d.dataRoot, d.repository)
	if err != nil {
		return "", errControllerRecoveryUnavailable
	}
	if first.ActiveOperationID == "" {
		return "", nil
	}
	if d.inspector == nil || !validCanonicalUUID(first.ActiveOperationID) {
		return "", errControllerRecoveryUnavailable
	}
	inspection, err := d.inspector.InspectGatewayRebindOperation(ctx, d.repository, first.ActiveOperationID)
	if err != nil || generatedingress.ValidateGatewayRebindOperationInspection(inspection, first.ActiveOperationID) != nil ||
		inspection.Availability != generatedingress.GatewayRebindOperationAvailable || !inspection.Active {
		return "", errControllerRecoveryUnavailable
	}
	confirmed, err := generatedingress.InspectGatewayRebindStartupPresence(ctx, d.dataRoot, d.repository)
	if err != nil || !reflect.DeepEqual(first, confirmed) {
		return "", errControllerRecoveryUnavailable
	}
	return first.ActiveOperationID, nil
}

func (s *Server) getControllerRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdministrator(w, r, operationGetControllerRecovery, "lan_gateway_forbidden",
		"Administrator access is required to inspect controller recovery") {
		return
	}
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || r.URL.ForceQuery ||
		readLANGatewayRebindStatusBody(r) != nil {
		s.controllerRecoveryProblem(w, r, http.StatusBadRequest, "invalid_lan_gateway_request",
			"Controller recovery request is invalid")
		return
	}
	if s.RecoveryOnly && !s.validControllerRecoveryPin() {
		s.controllerRecoveryUnavailable(w, r)
		return
	}
	_, guard, err := s.bindLANGatewayRebindReadSession(r)
	if err != nil {
		s.controllerRecoveryProblem(w, r, http.StatusForbidden, "lan_gateway_forbidden",
			"Administrator access is required to inspect controller recovery")
		return
	}

	response := apicontract.ControllerRecovery{Version: 1, Mode: "normal"}
	if s.RecoveryOnly {
		response.Mode = "recovery_only"
		err = s.inspectPinnedControllerRecovery(r.Context())
		if err == nil {
			response.Recovery = &apicontract.ControllerRecoveryLocator{
				Kind: s.RecoveryKind, OperationID: s.RecoveryOperationID,
			}
		}
	} else {
		var operationID string
		if s.RecoveryDiscovery == nil {
			err = errControllerRecoveryUnavailable
		} else {
			operationID, err = s.RecoveryDiscovery.ActiveGatewayRebind(r.Context())
		}
		if err == nil && operationID != "" {
			if !validCanonicalUUID(operationID) {
				err = errControllerRecoveryUnavailable
			} else {
				response.Recovery = &apicontract.ControllerRecoveryLocator{
					Kind: RecoveryGatewayRebind, OperationID: operationID,
				}
			}
		}
	}
	if err == nil {
		err = guard(r.Context())
	}
	if err != nil {
		if errors.Is(err, appaccess.ErrApprovalRequired) {
			s.controllerRecoveryProblem(w, r, http.StatusForbidden, "lan_gateway_forbidden",
				"Administrator access is required to inspect controller recovery")
			return
		}
		s.controllerRecoveryUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) validControllerRecoveryPin() bool {
	if s == nil || !s.RecoveryOnly || !validCanonicalUUID(s.RecoveryOperationID) {
		return false
	}
	switch s.RecoveryKind {
	case RecoveryGatewayUpgrade, RecoveryGatewayRebind:
		return s.RecoveryAppID == "" && !s.RecoveryLANBatch &&
			s.RecoveryBatchHead == 0 && s.RecoveryBatchCount == 0
	case RecoveryLANGrant, RecoveryLANDisable:
		if !validCanonicalUUID(s.RecoveryAppID) {
			return false
		}
		if s.RecoveryLANBatch {
			return s.RecoveryBatchHead >= 0 && s.RecoveryBatchCount > 0 &&
				s.RecoveryBatchHead < s.RecoveryBatchCount && s.RecoveryBatchCount <= 64
		}
		return s.RecoveryBatchHead == 0 && s.RecoveryBatchCount == 0
	default:
		return false
	}
}

func (s *Server) inspectPinnedControllerRecovery(ctx context.Context) error {
	switch s.RecoveryKind {
	case RecoveryGatewayRebind:
		return s.inspectPinnedGatewayRebind(ctx)
	case RecoveryLANGrant, RecoveryLANDisable:
		return s.inspectPinnedLANRecovery(ctx)
	case RecoveryGatewayUpgrade:
		return s.inspectPinnedGatewayUpgrade(ctx)
	default:
		return errControllerRecoveryUnavailable
	}
}

func (s *Server) inspectPinnedGatewayRebind(ctx context.Context) error {
	if s.GatewayRebindRecoveries == nil || s.GatewayRebindRecoveryRuntime == nil {
		return errControllerRecoveryUnavailable
	}
	result, err := s.GatewayRebindRecoveryRuntime.InspectGatewayRebindOperation(
		ctx, s.GatewayRebindRecoveries, s.RecoveryOperationID)
	if err != nil || generatedingress.ValidateGatewayRebindOperationInspection(result, s.RecoveryOperationID) != nil ||
		result.Availability != generatedingress.GatewayRebindOperationAvailable {
		return errControllerRecoveryUnavailable
	}
	return nil
}

func (s *Server) inspectPinnedLANRecovery(ctx context.Context) error {
	if s.LANRecoveryHeads != nil {
		head, err := s.LANRecoveryHeads.ReadLANRecoveryHead(ctx)
		if err == nil {
			if s.validPinnedLANRecoveryHead(head) {
				return nil
			}
			return errControllerRecoveryUnavailable
		}
	}
	if s.RecoverySnapshots == nil || s.RecoveryStartupRuntime == nil {
		return errControllerRecoveryUnavailable
	}
	first, err := s.RecoverySnapshots.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !pinnedLANTerminal(first, s.RecoveryKind, s.RecoveryOperationID, s.RecoveryAppID) {
		return errControllerRecoveryUnavailable
	}
	upgrade, err := s.RecoveryStartupRuntime.InspectGatewayV2Startup(ctx, controllerGatewayStartupClaims(first.Upgrades))
	if err != nil || !validControllerGatewayStartupInspection(upgrade) {
		return errControllerRecoveryUnavailable
	}
	access, err := s.RecoveryStartupRuntime.InspectGatewayV2LANAccessStartup(ctx,
		generatedingress.GatewayLANGrantStartupClaims(first.Grants),
		generatedingress.GatewayLANDisableStartupClaims(first.Disables))
	if err != nil || !validControllerLANStartupInspection(access) {
		return errControllerRecoveryUnavailable
	}
	confirmed, err := s.RecoverySnapshots.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, confirmed) {
		return errControllerRecoveryUnavailable
	}
	return nil
}

func (s *Server) inspectPinnedGatewayUpgrade(ctx context.Context) error {
	if s.RecoverySnapshots == nil || s.RecoveryStartupRuntime == nil || s.GatewayUpgradeRuntime == nil {
		return errControllerRecoveryUnavailable
	}
	first, err := s.RecoverySnapshots.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return errControllerRecoveryUnavailable
	}
	claim, present := pinnedGatewayUpgradeClaim(first.Upgrades, s.RecoveryOperationID)
	if !present {
		return errControllerRecoveryUnavailable
	}
	startup, err := s.RecoveryStartupRuntime.InspectGatewayV2Startup(ctx, controllerGatewayStartupClaims(first.Upgrades))
	if err != nil || !validControllerGatewayStartupInspection(startup) ||
		(!gatewayUpgradeTerminal(claim.State) &&
			(startup.Disposition != generatedingress.GatewayV2StartupRecoveryOnly ||
				startup.OperationID != s.RecoveryOperationID)) {
		return errControllerRecoveryUnavailable
	}
	observed, err := s.GatewayUpgradeRuntime.ObserveGatewayV2Operation(ctx, s.RecoveryOperationID)
	if err != nil || observed.OperationID != s.RecoveryOperationID ||
		!validGatewayUpgradeAvailability(observed.Availability) {
		return errControllerRecoveryUnavailable
	}
	confirmed, err := s.RecoverySnapshots.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(first, confirmed) {
		return errControllerRecoveryUnavailable
	}
	return nil
}

func pinnedLANTerminal(snapshot appaccess.HostingGatewayStartupSnapshot, kind, operationID, appID string) bool {
	switch kind {
	case RecoveryLANGrant:
		for _, entry := range snapshot.Grants.Claims {
			if entry.Claim.AttemptID == operationID && entry.Claim.Spec.AppID == appID {
				return entry.Claim.State == appaccess.AppAccessGrantCommitted ||
					entry.Claim.State == appaccess.AppAccessGrantRolledBack
			}
		}
	case RecoveryLANDisable:
		for _, entry := range snapshot.Disables.Claims {
			if entry.Claim.OperationID == operationID && entry.Claim.Spec.AppID == appID {
				return entry.Claim.State == appaccess.AppAccessDisableCommitted
			}
		}
	}
	return false
}

func pinnedGatewayUpgradeClaim(snapshot appaccess.GatewayUpgradeStartupSnapshot,
	operationID string,
) (appaccess.GatewayProfileUpgradeClaim, bool) {
	for _, entry := range snapshot.Claims {
		if entry.Claim.OperationID == operationID {
			return entry.Claim, true
		}
	}
	return appaccess.GatewayProfileUpgradeClaim{}, false
}

func gatewayUpgradeTerminal(state appaccess.GatewayProfileUpgradeState) bool {
	return state == appaccess.GatewayProfileUpgradeCommitted || state == appaccess.GatewayProfileUpgradeRolledBack
}

func controllerGatewayStartupClaims(snapshot appaccess.GatewayUpgradeStartupSnapshot) []generatedingress.GatewayV2StartupClaim {
	claims := make([]generatedingress.GatewayV2StartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		claims = append(claims, generatedingress.GatewayV2StartupClaim{
			Request: generatedingress.GatewayV2UpgradeRequest{
				OperationID: entry.Claim.OperationID,
				Profile: generatedingress.GatewayV2ProfileBinding{
					RevisionID: entry.Profile.ID, RevisionNumber: entry.Profile.RevisionNumber,
					SelectedIPv4: entry.Profile.Spec.SelectedIPv4, InterfaceID: entry.Profile.Spec.InterfaceID,
					PortStart: entry.Profile.Spec.PortStart, PortEnd: entry.Profile.Spec.PortEnd,
					SpecDigest: entry.Profile.SpecDigest,
				},
				ApprovedBy: entry.Claim.ApprovedBy, ApprovedActionDigest: entry.ApprovedActionDigest,
			},
			State: entry.Claim.State, StateSequence: entry.Claim.StateSequence,
		})
	}
	return claims
}

func validControllerGatewayStartupInspection(value generatedingress.GatewayV2StartupInspection) bool {
	emptySource := value.CurrentGatewaySource == (appaccess.GatewayCurrentAuthorityRef{})
	switch value.Disposition {
	case generatedingress.GatewayV2StartupNormalV1:
		return value.OperationID == "" && emptySource
	case generatedingress.GatewayV2StartupNormalV2, generatedingress.GatewayV2StartupRecoveryOnly:
		if !validCanonicalUUID(value.OperationID) {
			return false
		}
		if emptySource {
			return true
		}
		return value.CurrentGatewaySource.OperationID == value.OperationID &&
			validControllerGatewayCurrentAuthority(value.CurrentGatewaySource)
	default:
		return false
	}
}

func validControllerGatewayCurrentAuthority(value appaccess.GatewayCurrentAuthorityRef) bool {
	if !validCanonicalUUID(value.OperationID) || !validCanonicalUUID(value.ProfileRevisionID) ||
		value.ProfileRevisionNumber <= 0 || !validLowerHex(value.ProfileSpecDigest, 64) {
		return false
	}
	switch value.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return value.TerminalReceiptDigest == ""
	case appaccess.GatewayRebindSourceGatewayRebind:
		return validLowerHex(value.TerminalReceiptDigest, 64)
	default:
		return false
	}
}

func validControllerLANStartupInspection(value generatedingress.GatewayV2LANAccessStartupInspection) bool {
	switch value.Disposition {
	case generatedingress.GatewayV2LANStartupNormal:
		return value.RecoveryKind == "" && value.OperationID == "" && value.AppID == "" && len(value.Recoveries) == 0
	case generatedingress.GatewayV2LANStartupRecoveryOnly:
	default:
		return false
	}
	if len(value.Recoveries) == 0 {
		return validControllerLANRecovery(value.RecoveryKind, value.OperationID, value.AppID)
	}
	if value.RecoveryKind != "" || value.OperationID != "" || value.AppID != "" || len(value.Recoveries) > 64 {
		return false
	}
	seen := make(map[string]struct{}, len(value.Recoveries))
	for _, recovery := range value.Recoveries {
		if !validControllerLANRecovery(recovery.Kind, recovery.OperationID, recovery.AppID) {
			return false
		}
		key := recovery.Kind + "\x00" + recovery.OperationID + "\x00" + recovery.AppID
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validControllerLANRecovery(kind, operationID, appID string) bool {
	return (kind == RecoveryLANGrant || kind == RecoveryLANDisable) &&
		validCanonicalUUID(operationID) && validCanonicalUUID(appID)
}

func (s *Server) controllerRecoveryUnavailable(w http.ResponseWriter, r *http.Request) {
	s.controllerRecoveryProblem(w, r, http.StatusServiceUnavailable, "lan_gateway_unavailable",
		"Controller recovery state could not be safely proved")
}

func (s *Server) controllerRecoveryProblem(w http.ResponseWriter, r *http.Request,
	status int, code, detail string,
) {
	s.handlerProblem(w, r, operationGetControllerRecovery, status, code, detail, 0)
}
