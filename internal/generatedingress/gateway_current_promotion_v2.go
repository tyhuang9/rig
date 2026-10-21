package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

// GatewayCurrentV2PromotionRepository is deliberately read-only. The caller
// owns the effects lease and both gateway locks; this unit creates only one
// protected baseline and performs no Docker or Caddy mutation.
type GatewayCurrentV2PromotionRepository interface {
	HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
	CheckGatewayRebindFence(context.Context) error
	Head(context.Context, string) (applifecycle.Head, error)
	RebindExcluded(context.Context) (bool, error)
}

type gatewayCurrentV2Preparation struct {
	Selection gatewayCurrentV2Selection
	Created   bool
}

type gatewayCurrentV2PromotionAuthority struct {
	Snapshot appaccess.HostingGatewayStartupSnapshot
	Heads    []applifecycle.Head
}

type gatewayCurrentV2PhysicalOriginReader func(context.Context, gatewayCurrentSelection) (
	gatewayRebindAdmissionPredecessorProof, error,
)

func gatewayCurrentPromotionSourceApps(selection gatewayCurrentSelection) (
	map[string]gatewayCurrentAppRoute, error,
) {
	result := make(map[string]gatewayCurrentAppRoute)
	switch selection.Kind {
	case gatewayCurrentSelectionRebind:
		if selection.State == nil || !validGatewayCurrentRouteState(*selection.State) ||
			selection.State.Pending != nil || selection.State.LANRecovery != nil {
			return nil, errors.New("generated ingress current V2 promotion rebind source is invalid")
		}
		for appID, app := range selection.State.Apps {
			result[appID] = cloneGatewayCurrentAppRoute(app)
		}
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || !validGatewayV2RouteState(selection.Upgrade.State) ||
			selection.Upgrade.State.Pending != nil {
			return nil, errors.New("generated ingress current V2 promotion upgrade source is invalid")
		}
		for appID, app := range selection.Upgrade.State.Apps {
			cloned := cloneGatewayV2AppRoute(app)
			var lan *gatewayCurrentLANBinding
			if cloned.LAN != nil {
				lan = &gatewayCurrentLANBinding{Raw: *cloned.LAN}
			}
			result[appID] = gatewayCurrentAppRoute{Route: cloned.Route, LAN: lan}
		}
	default:
		return nil, errors.New("generated ingress current V2 promotion source is invalid")
	}
	return result, nil
}

func gatewayCurrentV2PromotionLANConsentMatches(source gatewayCurrentSelection,
	snapshot appaccess.HostingGatewayStartupSnapshot,
) bool {
	claims, err := validateGatewayV2LANAccessStartupClaims(GatewayLANGrantStartupClaims(snapshot.Grants),
		GatewayLANDisableStartupClaims(snapshot.Disables))
	if err != nil {
		return false
	}
	if source.Kind == gatewayCurrentSelectionRebind {
		if source.State == nil {
			return false
		}
		inspection, err := gatewayCurrentLANStartupCensus(*source.State, claims)
		return err == nil && inspection.Disposition == GatewayV2LANStartupNormal
	}
	if source.Kind != gatewayCurrentSelectionUpgrade || source.Upgrade == nil {
		return false
	}
	active := 0
	for appID, app := range source.Upgrade.State.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, *app.LAN)
		claim, ok := claims.grants.byAttempt[request.AttemptID]
		if !ok || claim.Request != request || claim.State != appaccess.AppAccessGrantCommitted ||
			claim.RequiresRecovery || claim.DisableIntentOperationID != "" || claim.CurrentBinding == nil {
			return false
		}
		if _, disabled := claims.byAllocation[request.AllocationID]; disabled {
			return false
		}
		active++
	}
	current := 0
	for _, claim := range claims.grants.byAttempt {
		if claim.CurrentBinding != nil {
			current++
		}
	}
	return active == current
}

func gatewayCurrentV2PromotionSnapshotMatches(selection gatewayCurrentSelection,
	snapshot appaccess.HostingGatewayStartupSnapshot,
) bool {
	if !gatewayCurrentRouteMutationSnapshotReady(snapshot.Rebind) || snapshot.ActiveRebindApprovalAuthority != nil ||
		!snapshot.ActiveRebindApprovalsAuthorizeServing() || !gatewayCurrentV2PromotionLANConsentMatches(selection, snapshot) {
		return false
	}
	for _, upgrade := range snapshot.Upgrades.Claims {
		if upgrade.Claim.State != appaccess.GatewayProfileUpgradeCommitted &&
			upgrade.Claim.State != appaccess.GatewayProfileUpgradeRolledBack {
			return false
		}
	}
	apps, err := gatewayCurrentPromotionSourceApps(selection)
	return err == nil && gatewayServingRuntimeComponentsMatch(apps, snapshot)
}

func readGatewayCurrentV2PromotionAuthority(ctx context.Context,
	repository GatewayCurrentV2PromotionRepository, selection gatewayCurrentSelection,
) (gatewayCurrentV2PromotionAuthority, error) {
	if ctx == nil || ctx.Err() != nil || repository == nil {
		return gatewayCurrentV2PromotionAuthority{}, errors.New("generated ingress current V2 promotion authority is unavailable")
	}
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || repository.CheckGatewayRebindFence(ctx) != nil {
		return gatewayCurrentV2PromotionAuthority{}, errors.New("generated ingress current V2 promotion is fenced")
	}
	excluded, err := repository.RebindExcluded(ctx)
	if err != nil || excluded || !gatewayCurrentV2PromotionSnapshotMatches(selection, snapshot) {
		return gatewayCurrentV2PromotionAuthority{}, errors.New("generated ingress current V2 promotion authority is invalid")
	}
	apps, err := gatewayCurrentPromotionSourceApps(selection)
	if err != nil || len(apps) != len(snapshot.RuntimeHeads) {
		return gatewayCurrentV2PromotionAuthority{}, errors.New("generated ingress current V2 promotion runtime census is invalid")
	}
	ids := make([]string, 0, len(apps))
	for appID := range apps {
		ids = append(ids, appID)
	}
	sort.Strings(ids)
	heads := make([]applifecycle.Head, 0, len(ids))
	for _, appID := range ids {
		head, headErr := repository.Head(ctx, appID)
		if headErr != nil || head.ApplicationID != appID || head.Revision != 0 || head.DesiredState != nil || head.UpdatedAt != nil {
			return gatewayCurrentV2PromotionAuthority{}, errors.New("generated ingress current V2 lifecycle head is not uninitialized")
		}
		heads = append(heads, head)
	}
	if ctx.Err() != nil {
		return gatewayCurrentV2PromotionAuthority{}, errors.New("generated ingress current V2 promotion was cancelled")
	}
	return gatewayCurrentV2PromotionAuthority{Snapshot: snapshot, Heads: heads}, nil
}

func (m *Manager) validateGatewayCurrentV2PromotionLANHistoryLocked(ctx context.Context,
	authority gatewayCurrentV2PromotionAuthority,
) error {
	claims, err := validateGatewayV2LANAccessStartupClaims(
		GatewayLANGrantStartupClaims(authority.Snapshot.Grants),
		GatewayLANDisableStartupClaims(authority.Snapshot.Disables))
	if err != nil {
		return err
	}
	return m.validateGatewayCurrentLANRetainedHistoryCurrentV2AwareLocked(ctx, claims, authority.Snapshot.Rebind)
}

func gatewayCurrentV2BaselineFor(selection gatewayCurrentSelection,
	authority gatewayCurrentV2PromotionAuthority,
) (gatewayCurrentRouteStateV2, error) {
	apps, err := gatewayCurrentPromotionSourceApps(selection)
	if err != nil || len(apps) != len(authority.Snapshot.RuntimeHeads) {
		return gatewayCurrentRouteStateV2{}, errors.New("generated ingress current V2 baseline census is invalid")
	}
	byApp := make(map[string]appaccess.GatewayRebindRuntimeHead, len(authority.Snapshot.RuntimeHeads))
	for _, head := range authority.Snapshot.RuntimeHeads {
		byApp[head.AppID] = head
	}
	origin, err := gatewayCurrentExpectedOriginV2(selection)
	if err != nil {
		return gatewayCurrentRouteStateV2{}, err
	}
	value := gatewayCurrentRouteStateV2{Version: gatewayCurrentRouteStateVersionV2, Revision: 1,
		Lineage: selection.Lineage, SourceV1: origin.SourceV1, SourceUpgrade: origin.SourceUpgrade,
		Apps: make(map[string]gatewayCurrentAppRouteV2, len(apps))}
	switch selection.Kind {
	case gatewayCurrentSelectionRebind:
		source := *selection.State
		value.Profile, value.Identity, value.Network = source.Profile, source.Identity, source.Network
		value.TransferManifestDigest = source.TransferManifestDigest
	case gatewayCurrentSelectionUpgrade:
		source := selection.Upgrade.State
		identity := source.Identity
		value.Profile, value.Network = source.Profile, source.Network
		value.Identity = gatewayCurrentIdentity{Kind: gatewayCurrentIdentityUpgrade, Upgrade: &identity}
		value.TransferManifestDigest, err = appaccess.GatewayRebindTransferManifestDigest(nil)
	default:
		err = errors.New("generated ingress current V2 baseline source is invalid")
	}
	if err != nil {
		return gatewayCurrentRouteStateV2{}, err
	}
	for appID, app := range apps {
		head, ok := byApp[appID]
		if !ok || string(app.Route.Slot) != head.Slot {
			return gatewayCurrentRouteStateV2{}, errors.New("generated ingress current V2 baseline runtime head is invalid")
		}
		value.Apps[appID] = gatewayCurrentAppRouteV2{
			Route: cloneGatewayCurrentAppRoute(app).Route,
			Runtime: gatewayCurrentRuntimePinV2{DeploymentID: head.DeploymentID, ReleaseID: head.ReleaseID,
				Slot: generatedidentity.Slot(head.Slot), ActiveGeneration: head.Generation},
			Traffic:   gatewayCurrentTrafficServingV2,
			Authority: gatewayCurrentAppliedAuthorityV2{DesiredMode: gatewayCurrentDesiredUninitializedV2},
			LAN:       cloneGatewayCurrentAppRoute(app).LAN,
		}
	}
	value.Digest, err = gatewayCurrentRouteStateDigestV2(value)
	if err != nil || !validGatewayCurrentRouteStateV2(value) {
		return gatewayCurrentRouteStateV2{}, errors.New("generated ingress current V2 baseline is invalid")
	}
	projection, err := projectGatewayCurrentStateV2(value)
	if err != nil || len(projection.DormantApps) != 0 {
		return gatewayCurrentRouteStateV2{}, errors.New("generated ingress current V2 baseline projection is invalid")
	}
	actual, err := gatewayCurrentPhysicalConfigBytesV2(value)
	if err != nil {
		return gatewayCurrentRouteStateV2{}, err
	}
	defer clear(actual)
	var expected []byte
	if selection.Kind == gatewayCurrentSelectionRebind {
		expected, err = gatewayCurrentPhysicalConfigBytes(*selection.State)
	} else {
		expected, err = expectedGatewayV2FinalConfig(selection.Upgrade.State)
	}
	defer clear(expected)
	if err != nil || !bytes.Equal(actual, expected) {
		return gatewayCurrentRouteStateV2{}, errors.New("generated ingress current V2 baseline changes physical config")
	}
	return value, nil
}

func gatewayCurrentV2PreparedBaselineMatches(value gatewayCurrentRouteStateV2,
	selection gatewayCurrentSelection, authority gatewayCurrentV2PromotionAuthority,
) bool {
	want, err := gatewayCurrentV2BaselineFor(selection, authority)
	return err == nil && reflect.DeepEqual(value, want)
}

// prepareGatewayCurrentV2Locked requires the caller's live effects scope and
// both gateway locks. It performs read-only physical observation and one
// create-exclusive protected write. A post-create failure retains that file.
func (m *Manager) prepareGatewayCurrentV2Locked(ctx context.Context,
	repository GatewayCurrentV2PromotionRepository,
) (gatewayCurrentV2Preparation, error) {
	return m.prepareGatewayCurrentV2WithPhysicalLocked(ctx, repository,
		func(readCtx context.Context, selection gatewayCurrentSelection) (
			gatewayRebindAdmissionPredecessorProof, error,
		) {
			return m.readGatewayRebindAdmissionPredecessorWithInspectLocked(
				readCtx, selection, m.inspectGatewayRebindDocker)
		})
}

func (m *Manager) prepareGatewayCurrentV2WithPhysicalLocked(ctx context.Context,
	repository GatewayCurrentV2PromotionRepository, readPhysical gatewayCurrentV2PhysicalOriginReader,
) (gatewayCurrentV2Preparation, error) {
	invalid := func() (gatewayCurrentV2Preparation, error) {
		return gatewayCurrentV2Preparation{}, errors.New("generated ingress current V2 preparation failed")
	}
	if m == nil || ctx == nil || repository == nil || readPhysical == nil || ctx.Err() != nil ||
		m.gatewayRebindAdmissionBlocked() {
		return invalid()
	}
	leave, err := deploymenteffects.EnterScope(ctx, m.options.WorkingDirectory)
	if err != nil {
		return invalid()
	}
	defer leave()

	selected, selectedSQL, err := m.readGatewayCurrentProtectedSelectionLocked(ctx)
	if err != nil {
		return invalid()
	}
	var source gatewayCurrentSelection
	if selected.Legacy != nil && selected.V2 == nil {
		source = *selected.Legacy
	} else if selected.V2 != nil && selected.Legacy == nil {
		source = selected.V2.Source
	} else {
		return invalid()
	}
	before, err := readGatewayCurrentV2PromotionAuthority(ctx, repository, source)
	if err != nil || m.validateGatewayCurrentV2PromotionLANHistoryLocked(ctx, before) != nil ||
		!reflect.DeepEqual(before.Snapshot.Rebind, selectedSQL) || m.gatewayRebindAdmissionBlocked() {
		return invalid()
	}
	firstPhysical, err := readPhysical(ctx, source)
	if err != nil {
		return invalid()
	}
	confirmed, confirmedSQL, err := m.readGatewayCurrentProtectedSelectionLocked(ctx)
	middle, authorityErr := readGatewayCurrentV2PromotionAuthority(ctx, repository, source)
	if err != nil || authorityErr != nil || m.validateGatewayCurrentV2PromotionLANHistoryLocked(ctx, middle) != nil ||
		ctx.Err() != nil || m.gatewayRebindAdmissionBlocked() ||
		!reflect.DeepEqual(before, middle) || !reflect.DeepEqual(selectedSQL, confirmedSQL) ||
		!sameGatewayCurrentProtectedSelection(selected, confirmed) {
		return invalid()
	}
	secondPhysical, err := readPhysical(ctx, source)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(firstPhysical, secondPhysical) {
		return invalid()
	}

	created := false
	if selected.V2 == nil {
		baseline, buildErr := gatewayCurrentV2BaselineFor(source, before)
		origin := gatewayCurrentRouteOriginForStateV2(baseline)
		store, storeErr := newGatewayCurrentRouteStateV2StoreForOrigin(m.options.DataRoot, source.Lineage, origin)
		if buildErr != nil || storeErr != nil || store.installBaseline(baseline) != nil {
			return invalid()
		}
		created = true
	} else if selected.V2.State.Revision != 1 ||
		!gatewayCurrentV2PreparedBaselineMatches(selected.V2.State, source, before) {
		return invalid()
	}
	finalPhysical, err := readPhysical(ctx, source)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(firstPhysical, finalPhysical) {
		return invalid()
	}

	final, finalSQL, err := m.readGatewayCurrentProtectedSelectionLocked(ctx)
	after, authorityErr := readGatewayCurrentV2PromotionAuthority(ctx, repository, source)
	if err != nil || authorityErr != nil || m.validateGatewayCurrentV2PromotionLANHistoryLocked(ctx, after) != nil ||
		final.V2 == nil || final.Legacy != nil || ctx.Err() != nil ||
		m.gatewayRebindAdmissionBlocked() || !reflect.DeepEqual(before, after) ||
		!reflect.DeepEqual(selectedSQL, finalSQL) || !gatewayCurrentV2PreparedBaselineMatches(final.V2.State, source, after) {
		return invalid()
	}
	return gatewayCurrentV2Preparation{Selection: *final.V2, Created: created}, nil
}

func sameGatewayCurrentProtectedSelection(left, right gatewayCurrentProtectedSelection) bool {
	if (left.Legacy == nil) != (right.Legacy == nil) || (left.V2 == nil) != (right.V2 == nil) {
		return false
	}
	if left.Legacy != nil {
		return sameGatewayCurrentLegacySelection(*left.Legacy, *right.Legacy)
	}
	return left.V2.Lineage == right.V2.Lineage &&
		sameGatewayCurrentRouteOriginDescriptorV2(left.V2.Origin, right.V2.Origin) &&
		sameGatewayCurrentLegacySelection(left.V2.Source, right.V2.Source) &&
		reflect.DeepEqual(left.V2.State, right.V2.State) && reflect.DeepEqual(left.V2.Projection, right.V2.Projection)
}
