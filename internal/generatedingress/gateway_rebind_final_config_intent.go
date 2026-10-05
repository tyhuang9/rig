package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindFinalConfigIntentVersion = 1
	gatewayRebindFinalConfigPlanVersion   = 1
	gatewayRebindFinalConfigPlanContext   = "hostd/generated-ingress/rebind/final-route-plan/v1"
)

// gatewayRebindFinalConfigRoutePlan retains each predecessor application
// route and its raw LAN binding. EffectiveSuccessorProfile is separate: the
// approved successor profile must never be spliced into an old grant merely
// to satisfy the v2 route-state validator.
type gatewayRebindFinalConfigRoutePlan struct {
	Version                   int                                    `json:"version"`
	Context                   string                                 `json:"context"`
	Predecessor               gatewayRebindSuccessorSourceBinding    `json:"predecessor"`
	EffectiveSuccessorProfile gatewayRebindSuccessorIntentProfile    `json:"effectiveSuccessorProfile"`
	SuccessorIdentity         gatewayRebindSuccessorIdentity         `json:"successorIdentity"`
	Routes                    []gatewayRebindFinalConfigRouteBinding `json:"routes"`
	RouteMapDigest            string                                 `json:"routeMapDigest"`
	Digest                    string                                 `json:"digest"`
}

type gatewayRebindFinalConfigRouteBinding struct {
	AppID               string                              `json:"appId"`
	Route               routeRecord                         `json:"route"`
	SourceLAN           *gatewayV2LANBinding                `json:"sourceLan,omitempty"`
	RuntimeHead         gatewayRebindFinalConfigRuntimeHead `json:"runtimeHead"`
	SourceBindingDigest string                              `json:"sourceBindingDigest"`
}

type gatewayRebindFinalConfigRuntimeHead struct {
	AppID        string `json:"appId"`
	DeploymentID string `json:"deploymentId"`
	ReleaseID    string `json:"releaseId"`
	Slot         string `json:"slot"`
	Generation   int64  `json:"generation"`
	UpdatedAt    string `json:"updatedAt"`
}

type gatewayRebindFinalConfigIntentBinding struct {
	Version                    int                               `json:"version"`
	RoutePlan                  gatewayRebindFinalConfigRoutePlan `json:"routePlan"`
	ContentDigest              string                            `json:"contentDigest"`
	ContentLength              int64                             `json:"contentLength"`
	Destination                string                            `json:"destination"`
	SuccessorIdentityDigest    string                            `json:"successorIdentityDigest"`
	ProtectedIntentDigest      string                            `json:"protectedIntentDigest"`
	ProtectedPredecessorDigest string                            `json:"protectedPredecessorDigest"`
	SequenceTenDigest          string                            `json:"sequenceTenDigest"`
	PriorProgressDigest        string                            `json:"priorProgressDigest"`
}

type gatewayRebindFinalConfigIntentDriver interface {
	gatewayRebindStageServingAttestor
}

type managerGatewayRebindFinalConfigIntentDriver struct{ manager *Manager }

func (d managerGatewayRebindFinalConfigIntentDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	return (managerGatewayRebindStageStartDriver{manager: d.manager}).inspect(ctx, intent)
}

func (d managerGatewayRebindFinalConfigIntentDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return (managerGatewayRebindStageStartDriver{manager: d.manager}).inspectImage(ctx)
}

func (d managerGatewayRebindFinalConfigIntentDriver) configVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	return (managerGatewayRebindStageStartDriver{manager: d.manager}).configVolumeInventory(ctx, intent, stage, expected)
}

func (d managerGatewayRebindFinalConfigIntentDriver) liveConfig(ctx context.Context, id string) ([]byte, error) {
	return (managerGatewayRebindStageStartDriver{manager: d.manager}).liveConfig(ctx, id)
}

func (d managerGatewayRebindFinalConfigIntentDriver) hostProbe(ctx context.Context, address string,
	port uint16, host, path string,
) gatewayV2HostProbeResult {
	return (managerGatewayRebindStageStartDriver{manager: d.manager}).hostProbe(ctx, address, port, host, path)
}

func (d managerGatewayRebindFinalConfigIntentDriver) containerProbe(ctx context.Context, id, address string,
	port uint16, host, challenge string,
) bool {
	return (managerGatewayRebindStageStartDriver{manager: d.manager}).containerProbe(ctx, id, address, port, host, challenge)
}

func gatewayRebindFinalConfigRoutePlanFor(intent gatewayRebindProtectedIntent,
	predecessor gatewayUpgradeGenerationSelection, heads []appaccess.GatewayRebindRuntimeHead,
) (gatewayRebindFinalConfigRoutePlan, error) {
	invalid := errors.New("invalid generated ingress rebind final route plan input")
	if !validGatewayRebindProtectedIntent(intent) || predecessor.Generation != intent.Intent.Predecessor.Generation ||
		predecessor.operationID != intent.Intent.Predecessor.OperationID || !predecessor.Existing ||
		predecessor.PartialState || predecessor.Retired || predecessor.Aborted ||
		!validGatewayV2RouteState(predecessor.State) || !validGatewayMigrationJournal(predecessor.Journal) ||
		predecessor.Journal.Phase != gatewayPhaseCommitted ||
		!journalMatchesV2Plan(predecessor.Journal, predecessor.State) || predecessor.State.Pending != nil ||
		predecessor.State.LANRecovery != nil || predecessor.State.Profile != intent.Intent.Predecessor.Profile ||
		predecessor.State.Identity.Digest != intent.Intent.Predecessor.IdentityDigest ||
		predecessor.State.OperationID != intent.Intent.Predecessor.OperationID ||
		len(predecessor.State.Apps) != len(heads) {
		return gatewayRebindFinalConfigRoutePlan{}, invalid
	}
	stateDigest, err := canonicalDigest(predecessor.State)
	if err != nil || stateDigest != intent.Intent.Predecessor.StateDigest {
		return gatewayRebindFinalConfigRoutePlan{}, invalid
	}
	journalDigest, err := canonicalDigest(predecessor.Journal)
	if err != nil || journalDigest != intent.Intent.Predecessor.JournalDigest {
		return gatewayRebindFinalConfigRoutePlan{}, invalid
	}
	roster := make(map[string]appaccess.GatewayRebindRosterEntry, len(intent.Intent.Roster))
	for _, entry := range intent.Intent.Roster {
		if _, duplicate := roster[entry.AppID]; duplicate {
			return gatewayRebindFinalConfigRoutePlan{}, invalid
		}
		roster[entry.AppID] = entry
	}
	appIDs := make([]string, 0, len(predecessor.State.Apps))
	for appID := range predecessor.State.Apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	routes := make([]gatewayRebindFinalConfigRouteBinding, 0, len(appIDs))
	lanCount := 0
	for index, appID := range appIDs {
		if index >= len(heads) || heads[index].AppID != appID || !validCanonicalUUID(heads[index].AppID) ||
			!validCanonicalUUID(heads[index].DeploymentID) || !validGatewayRebindRuntimeReleaseID(heads[index].ReleaseID) ||
			(heads[index].Slot != "blue" && heads[index].Slot != "green") || heads[index].Generation <= 0 ||
			heads[index].UpdatedAt.IsZero() || heads[index].UpdatedAt.UTC() != heads[index].UpdatedAt {
			return gatewayRebindFinalConfigRoutePlan{}, invalid
		}
		app := cloneGatewayV2AppRoute(predecessor.State.Apps[appID])
		if string(app.Route.Slot) != heads[index].Slot {
			return gatewayRebindFinalConfigRoutePlan{}, invalid
		}
		entry, listed := roster[appID]
		if app.LAN == nil {
			if listed {
				return gatewayRebindFinalConfigRoutePlan{}, invalid
			}
		} else {
			lanCount++
			if !listed || entry.GrantAttemptID != app.LAN.GrantAttemptID ||
				entry.AllocationOwnerOperationID != app.LAN.OwnerOperationID ||
				entry.AccessRevisionID != app.LAN.AccessRevisionID ||
				entry.AccessRevisionNumber != app.LAN.AccessRevisionNumber ||
				entry.AccessSpecDigest != app.LAN.AccessSpecDigest || entry.AllocationID != app.LAN.AllocationID ||
				entry.Port != app.LAN.Port || entry.ServingDeploymentID != heads[index].DeploymentID ||
				entry.ServingReleaseID != heads[index].ReleaseID || entry.ServingSlot != heads[index].Slot ||
				entry.RouteGeneration != heads[index].Generation ||
				app.LAN.ProfileRevisionID != intent.Intent.Predecessor.Profile.RevisionID ||
				app.LAN.ProfileRevisionNumber != intent.Intent.Predecessor.Profile.RevisionNumber ||
				app.LAN.ProfileSpecDigest != intent.Intent.Predecessor.Profile.SpecDigest ||
				app.LAN.Port < intent.Intent.SuccessorProfile.PortStart ||
				app.LAN.Port > intent.Intent.SuccessorProfile.PortEnd {
				return gatewayRebindFinalConfigRoutePlan{}, invalid
			}
		}
		runtimeHead := gatewayRebindFinalConfigRuntimeHead{
			AppID: appID, DeploymentID: heads[index].DeploymentID, ReleaseID: heads[index].ReleaseID,
			Slot: heads[index].Slot, Generation: heads[index].Generation,
			UpdatedAt: heads[index].UpdatedAt.Format(time.RFC3339Nano),
		}
		binding := gatewayRebindFinalConfigRouteBinding{
			AppID: appID, Route: app.Route, SourceLAN: app.LAN, RuntimeHead: runtimeHead,
		}
		binding.SourceBindingDigest, err = gatewayRebindFinalConfigSourceBindingDigest(binding)
		if err != nil {
			return gatewayRebindFinalConfigRoutePlan{}, invalid
		}
		routes = append(routes, binding)
	}
	if lanCount != len(roster) {
		return gatewayRebindFinalConfigRoutePlan{}, invalid
	}
	plan := gatewayRebindFinalConfigRoutePlan{
		Version: gatewayRebindFinalConfigPlanVersion, Context: gatewayRebindFinalConfigPlanContext,
		Predecessor: intent.Intent.Predecessor, EffectiveSuccessorProfile: intent.Intent.SuccessorProfile,
		SuccessorIdentity: intent.Intent.Identity, Routes: routes,
	}
	plan.RouteMapDigest, err = gatewayRebindFinalConfigRouteMapDigest(plan.Routes)
	if err != nil {
		return gatewayRebindFinalConfigRoutePlan{}, invalid
	}
	plan.Digest, err = gatewayRebindFinalConfigRoutePlanDigest(plan)
	if err != nil || !validGatewayRebindFinalConfigRoutePlanValue(intent, plan) {
		return gatewayRebindFinalConfigRoutePlan{}, invalid
	}
	return plan, nil
}

func gatewayRebindFinalConfigSourceBindingDigest(value gatewayRebindFinalConfigRouteBinding) (string, error) {
	value.SourceBindingDigest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalConfigRouteMapDigest(routes []gatewayRebindFinalConfigRouteBinding) (string, error) {
	return canonicalDigest(struct {
		Version int                                    `json:"version"`
		Context string                                 `json:"context"`
		Routes  []gatewayRebindFinalConfigRouteBinding `json:"routes"`
	}{gatewayRebindFinalConfigPlanVersion, gatewayRebindFinalConfigPlanContext, routes})
}

func gatewayRebindFinalConfigRoutePlanDigest(value gatewayRebindFinalConfigRoutePlan) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

// gatewayRebindFinalConfigRoutePlanMatchesPredecessor binds protected sequence
// eleven to the scanner's retained predecessor app map. SQL freshness is a
// separate coordinator proof; this check prevents a self-consistent rehash
// from omitting a loopback-only predecessor route in durable history.
func gatewayRebindFinalConfigRoutePlanMatchesPredecessor(intent gatewayRebindProtectedIntent,
	predecessor gatewayUpgradeGenerationSelection, value gatewayRebindFinalConfigRoutePlan,
) bool {
	heads := make([]appaccess.GatewayRebindRuntimeHead, 0, len(value.Routes))
	for _, route := range value.Routes {
		updatedAt, err := time.Parse(time.RFC3339Nano, route.RuntimeHead.UpdatedAt)
		if err != nil {
			return false
		}
		heads = append(heads, appaccess.GatewayRebindRuntimeHead{
			AppID: route.RuntimeHead.AppID, DeploymentID: route.RuntimeHead.DeploymentID,
			ReleaseID: route.RuntimeHead.ReleaseID, Slot: route.RuntimeHead.Slot,
			Generation: route.RuntimeHead.Generation, UpdatedAt: updatedAt,
		})
	}
	expected, err := gatewayRebindFinalConfigRoutePlanFor(intent, predecessor, heads)
	return err == nil && reflect.DeepEqual(expected, value)
}

func validGatewayRebindFinalConfigRoutePlanValue(intent gatewayRebindProtectedIntent,
	value gatewayRebindFinalConfigRoutePlan,
) bool {
	if !validGatewayRebindProtectedIntent(intent) || value.Version != gatewayRebindFinalConfigPlanVersion ||
		value.Context != gatewayRebindFinalConfigPlanContext || value.Predecessor != intent.Intent.Predecessor ||
		value.EffectiveSuccessorProfile != intent.Intent.SuccessorProfile ||
		value.SuccessorIdentity != intent.Intent.Identity || len(value.Routes) > maxStateApps ||
		len(value.Routes) < len(intent.Intent.Roster) || !validSHA256(value.RouteMapDigest) ||
		!validSHA256(value.Digest) {
		return false
	}
	roster := make(map[string]appaccess.GatewayRebindRosterEntry, len(intent.Intent.Roster))
	for _, entry := range intent.Intent.Roster {
		roster[entry.AppID] = entry
	}
	ports := make(map[uint16]string)
	allocations := make(map[string]struct{})
	revisions := make(map[string]struct{})
	attempts := make(map[string]struct{})
	lanCount := 0
	for index, route := range value.Routes {
		if !validAppID(route.AppID) || (index > 0 && value.Routes[index-1].AppID >= route.AppID) ||
			route.RuntimeHead.AppID != route.AppID || !validCanonicalUUID(route.RuntimeHead.DeploymentID) ||
			!validGatewayRebindRuntimeReleaseID(route.RuntimeHead.ReleaseID) ||
			(route.RuntimeHead.Slot != "blue" && route.RuntimeHead.Slot != "green") ||
			route.RuntimeHead.Generation <= 0 || string(route.Route.Slot) != route.RuntimeHead.Slot {
			return false
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, route.RuntimeHead.UpdatedAt)
		if err != nil || updatedAt.IsZero() || updatedAt.UTC().Format(time.RFC3339Nano) != route.RuntimeHead.UpdatedAt {
			return false
		}
		app := gatewayV2AppRoute{Route: route.Route, LAN: route.SourceLAN}
		if !validGatewayV2AppRoute(route.AppID, value.Predecessor.Profile, app,
			ports, allocations, revisions, attempts) {
			return false
		}
		entry, listed := roster[route.AppID]
		if route.SourceLAN == nil {
			if listed {
				return false
			}
		} else {
			lanCount++
			if !listed || entry.GrantAttemptID != route.SourceLAN.GrantAttemptID ||
				entry.AllocationOwnerOperationID != route.SourceLAN.OwnerOperationID ||
				entry.AccessRevisionID != route.SourceLAN.AccessRevisionID ||
				entry.AccessRevisionNumber != route.SourceLAN.AccessRevisionNumber ||
				entry.AccessSpecDigest != route.SourceLAN.AccessSpecDigest ||
				entry.AllocationID != route.SourceLAN.AllocationID || entry.Port != route.SourceLAN.Port ||
				entry.ServingDeploymentID != route.RuntimeHead.DeploymentID ||
				entry.ServingReleaseID != route.RuntimeHead.ReleaseID ||
				entry.ServingSlot != route.RuntimeHead.Slot ||
				entry.RouteGeneration != route.RuntimeHead.Generation ||
				route.SourceLAN.Port < value.EffectiveSuccessorProfile.PortStart ||
				route.SourceLAN.Port > value.EffectiveSuccessorProfile.PortEnd {
				return false
			}
		}
		digest, err := gatewayRebindFinalConfigSourceBindingDigest(route)
		if err != nil || digest != route.SourceBindingDigest {
			return false
		}
	}
	if lanCount != len(roster) {
		return false
	}
	routeDigest, err := gatewayRebindFinalConfigRouteMapDigest(value.Routes)
	if err != nil || routeDigest != value.RouteMapDigest {
		return false
	}
	digest, err := gatewayRebindFinalConfigRoutePlanDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindRuntimeReleaseID(value string) bool {
	if validCanonicalUUID(value) {
		return true
	}
	if len(value) != 32 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func gatewayRebindFinalConfigBytes(intent gatewayRebindProtectedIntent,
	plan gatewayRebindFinalConfigRoutePlan,
) ([]byte, error) {
	if !validGatewayRebindFinalConfigRoutePlanValue(intent, plan) {
		return nil, errors.New("invalid generated ingress rebind final config plan")
	}
	routes := make(map[string]routeRecord, len(plan.Routes))
	assignments := make(map[uint16]caddyV2LANAssignment, len(intent.Intent.Roster))
	for _, binding := range plan.Routes {
		routes[binding.AppID] = binding.Route
		if binding.SourceLAN != nil {
			assignments[binding.SourceLAN.Port] = caddyV2LANAssignment{
				AppID: binding.AppID, AllocationID: binding.SourceLAN.AllocationID,
				AccessRevisionID:     binding.SourceLAN.AccessRevisionID,
				AccessRevisionNumber: binding.SourceLAN.AccessRevisionNumber,
				AccessSpecDigest:     binding.SourceLAN.AccessSpecDigest,
			}
		}
	}
	probe, err := gatewayRebindStageConfigProbeToken(intent)
	if err != nil {
		return nil, errors.New("invalid generated ingress rebind final config probe")
	}
	body, err := buildCaddyConfigV2(routes,
		net.JoinHostPort(intent.Intent.Network.ContainerIPv4,
			strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{
			SelectedIPv4: plan.EffectiveSuccessorProfile.SelectedIPv4,
			PortStart:    plan.EffectiveSuccessorProfile.PortStart,
			PortEnd:      plan.EffectiveSuccessorProfile.PortEnd, ProbeToken: probe,
		}, assignments)
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes {
		clear(body)
		return nil, errors.New("invalid generated ingress rebind final config")
	}
	return body, nil
}

func gatewayRebindFinalConfigIntentBindingFor(intent gatewayRebindProtectedIntent,
	predecessor gatewayUpgradeGenerationSelection, heads []appaccess.GatewayRebindRuntimeHead,
	previous gatewayRebindProgressRecord,
) (gatewayRebindFinalConfigIntentBinding, error) {
	if !validGatewayRebindProgressRecord(previous) || previous.Sequence != 10 ||
		previous.Phase != gatewayRebindProgressStageServing || previous.Stage == nil ||
		previous.Stage.StageServing == nil || previous.Stage.FinalConfigIntent != nil ||
		previous.ProtectedIntentDigest != intent.Digest {
		return gatewayRebindFinalConfigIntentBinding{}, errors.New("invalid generated ingress rebind final config binding input")
	}
	plan, err := gatewayRebindFinalConfigRoutePlanFor(intent, predecessor, heads)
	if err != nil {
		return gatewayRebindFinalConfigIntentBinding{}, err
	}
	body, err := gatewayRebindFinalConfigBytes(intent, plan)
	if err != nil {
		return gatewayRebindFinalConfigIntentBinding{}, err
	}
	defer clear(body)
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil {
		return gatewayRebindFinalConfigIntentBinding{}, errors.New("invalid generated ingress rebind protected predecessor")
	}
	digest := sha256.Sum256(body)
	value := gatewayRebindFinalConfigIntentBinding{
		Version: gatewayRebindFinalConfigIntentVersion, RoutePlan: plan,
		ContentDigest: hex.EncodeToString(digest[:]), ContentLength: int64(len(body)),
		Destination:             "/config/" + intent.Intent.Identity.ActiveConfigFilename,
		SuccessorIdentityDigest: intent.Intent.Identity.Digest,
		ProtectedIntentDigest:   intent.Digest, ProtectedPredecessorDigest: predecessorDigest,
		SequenceTenDigest: previous.Digest, PriorProgressDigest: previous.Digest,
	}
	if !validGatewayRebindFinalConfigIntentBinding(intent, previous, value) {
		return gatewayRebindFinalConfigIntentBinding{}, errors.New("invalid generated ingress rebind final config binding input")
	}
	return value, nil
}

func validGatewayRebindFinalConfigIntentBindingValue(value gatewayRebindFinalConfigIntentBinding) bool {
	return value.Version == gatewayRebindFinalConfigIntentVersion && validSHA256(value.ContentDigest) &&
		value.ContentLength > 0 && value.ContentLength <= gatewayV2MaxConfigBytes &&
		value.Destination == "/config/"+gatewayV2ActiveConfigFile && validSHA256(value.SuccessorIdentityDigest) &&
		validSHA256(value.ProtectedIntentDigest) && validSHA256(value.ProtectedPredecessorDigest) &&
		validSHA256(value.SequenceTenDigest) && validSHA256(value.PriorProgressDigest)
}

func validGatewayRebindFinalConfigIntentBinding(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindFinalConfigIntentBinding,
) bool {
	if !validGatewayRebindFinalConfigIntentBindingValue(value) || !validGatewayRebindProtectedIntent(intent) ||
		!validGatewayRebindProgressRecord(previous) || previous.Sequence != 10 ||
		previous.Phase != gatewayRebindProgressStageServing || previous.Stage == nil ||
		previous.Stage.StageServing == nil || previous.Stage.FinalConfigIntent != nil ||
		value.ProtectedIntentDigest != intent.Digest || value.SuccessorIdentityDigest != intent.Intent.Identity.Digest ||
		value.SequenceTenDigest != previous.Digest || value.PriorProgressDigest != previous.Digest ||
		!validGatewayRebindFinalConfigRoutePlanValue(intent, value.RoutePlan) {
		return false
	}
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil || value.ProtectedPredecessorDigest != predecessorDigest {
		return false
	}
	body, err := gatewayRebindFinalConfigBytes(intent, value.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(body)
	digest := sha256.Sum256(body)
	return value.ContentDigest == hex.EncodeToString(digest[:]) && value.ContentLength == int64(len(body))
}

type gatewayRebindFinalConfigIntentAttestation struct {
	Serving      gatewayRebindStageServingAttestation
	RuntimeHeads []appaccess.GatewayRebindRuntimeHead
	Binding      gatewayRebindFinalConfigIntentBinding
}

// prepareGatewayRebindSuccessorFinalConfigIntent is private and appends only
// sequence eleven. It writes no container config, does not reload or start a
// container, does not probe an application response, and changes no route or
// SQLite state.
func (m *Manager) prepareGatewayRebindSuccessorFinalConfigIntent(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindFinalConfigIntentDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindFinalConfigIntentDriver,
	occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	if m == nil || ctx == nil || repository == nil || inspectDocker == nil || driver == nil ||
		reads.network.candidates == nil || reads.network.host == nil || reads.network.docker == nil ||
		reads.dockerIDs == nil || !validGatewayRebindProgressTime(occurredAt) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRaw(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			return &Error{Code: DiagnosticRouteUnresolved}
		}
		return err
	}
	defer func() {
		if releaseErr := releaseGateway(); releaseErr != nil {
			resultErr = gatewayRebindEffectBoundaryError(ctx)
		}
		if releaseErr := releaseEffects(); releaseErr != nil {
			resultErr = gatewayRebindEffectBoundaryError(ctx)
		}
	}()

	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 10 || len(history.Progress) > 11 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.StageServing == nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[9].Record
	heads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindFinalConfigIntentBindingFor(intent, history.Predecessor, heads, previous)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 11 {
		if stage.FinalConfigIntent == nil || !reflect.DeepEqual(*stage.FinalConfigIntent, binding) ||
			m.attestGatewayRebindFinalConfigIntentLocked(ctx, repository, reads, inspectDocker, driver,
				intent, *stage, binding, 11) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.FinalConfigIntent != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	store, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 11)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindFinalConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 10)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindFinalConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 10)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindFinalConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 10)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindFinalConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 10)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	record, err := newGatewayRebindFinalConfigIntentProgress(intent, previous, binding, occurredAt)
	if err != nil || store.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindFinalConfigIntentLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *record.Stage, binding, 11)
}

func (m *Manager) attestGatewayRebindFinalConfigIntentLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindFinalConfigIntentDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindFinalConfigIntentBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindFinalConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindFinalConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	finalAnchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !gatewayRebindEffectBoundaryAnchorMatchesObservation(finalAnchor,
		gatewayRebindEffectBoundaryObservation{database: first.Serving.Anchor.database,
			predecessor: first.Serving.Anchor.predecessor, intent: first.Serving.Anchor.intent,
			source: first.Serving.Anchor.source, databaseDigest: first.Serving.Anchor.databaseDigest,
			sourceDigest: first.Serving.Anchor.sourceDigest, progressCount: first.Serving.Anchor.progressCount,
			progressDigest: first.Serving.Anchor.progressDigest}) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	finalHeads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil || !reflect.DeepEqual(first.RuntimeHeads, finalHeads) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (m *Manager) readGatewayRebindFinalConfigIntentAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindFinalConfigIntentDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindFinalConfigIntentBinding, progressCount uint64,
) (gatewayRebindFinalConfigIntentAttestation, error) {
	if progressCount != 10 && progressCount != 11 {
		return gatewayRebindFinalConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) || len(history.Progress) < 10 ||
		!reflect.DeepEqual(history.Intents[0].Intent, intent) || history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindFinalConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[9].Record
	if !validGatewayRebindFinalConfigIntentBinding(intent, previous, binding) ||
		(progressCount == 10 && stage.FinalConfigIntent != nil) ||
		(progressCount == 11 && (stage.FinalConfigIntent == nil || !reflect.DeepEqual(*stage.FinalConfigIntent, binding))) {
		return gatewayRebindFinalConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	serving, err := m.readGatewayRebindStageServingAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, stage.StageServing, progressCount)
	if err != nil {
		return gatewayRebindFinalConfigIntentAttestation{}, err
	}
	heads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil {
		return gatewayRebindFinalConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expected, err := gatewayRebindFinalConfigIntentBindingFor(intent, serving.Anchor.predecessor, heads, previous)
	if err != nil || !reflect.DeepEqual(expected, binding) || ctx.Err() != nil {
		return gatewayRebindFinalConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindFinalConfigIntentAttestation{
		Serving: serving, RuntimeHeads: heads, Binding: binding,
	}, nil
}

var _ gatewayRebindFinalConfigIntentDriver = managerGatewayRebindFinalConfigIntentDriver{}
