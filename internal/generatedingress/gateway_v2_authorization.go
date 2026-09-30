package generatedingress

import (
	"context"
	"errors"
)

// GatewayV2UpgradeAuthorizer is a trusted callback supplied by the
// authenticated controller. The Manager invokes it only while both gateway
// writer locks are held and passes the complete immutable request by value.
type GatewayV2UpgradeAuthorizer func(context.Context, GatewayV2UpgradeRequest) error

type gatewayV2UpgradeAuthorizationDenied struct{}

func (*gatewayV2UpgradeAuthorizationDenied) Error() string {
	return "generated ingress gateway upgrade authorization denied"
}

func isGatewayV2UpgradeAuthorizationDenied(err error) bool {
	var denied *gatewayV2UpgradeAuthorizationDenied
	return errors.As(err, &denied)
}

func authorizeGatewayV2Upgrade(ctx context.Context, request GatewayV2UpgradeRequest, authorize GatewayV2UpgradeAuthorizer) error {
	if ctx == nil || authorize == nil || authorize(ctx, request) != nil {
		return &gatewayV2UpgradeAuthorizationDenied{}
	}
	return nil
}

// gatewayV2MutationAuthorizationGate is shared by the stage and transfer
// adapters during one UpgradeGatewayV2 invocation. The second authorization
// runs at the exact first Docker mutation after read-only preflight. A denial
// is memoized so every later mutation remains blocked.
type gatewayV2MutationAuthorizationGate struct {
	request   GatewayV2UpgradeRequest
	authorize GatewayV2UpgradeAuthorizer
	attempted bool
	allowed   bool
}

func (g *gatewayV2MutationAuthorizationGate) beforeMutation(ctx context.Context) error {
	if g == nil || g.authorize == nil {
		return &gatewayV2UpgradeAuthorizationDenied{}
	}
	if g.attempted {
		if g.allowed {
			return nil
		}
		return &gatewayV2UpgradeAuthorizationDenied{}
	}
	g.attempted = true
	if authorizeGatewayV2Upgrade(ctx, g.request, g.authorize) != nil {
		return &gatewayV2UpgradeAuthorizationDenied{}
	}
	g.allowed = true
	return nil
}

type authorizedGatewayV2UpgradeDriver struct {
	gatewayV2UpgradeDriver
	gate *gatewayV2MutationAuthorizationGate
}

func (d authorizedGatewayV2UpgradeDriver) createIngressNetwork(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (string, error) {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return "", err
	}
	return d.gatewayV2UpgradeDriver.createIngressNetwork(ctx, state, journal)
}

func (d authorizedGatewayV2UpgradeDriver) createVolume(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, role string,
) (gatewayV1VolumeIdentity, error) {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return gatewayV1VolumeIdentity{}, err
	}
	return d.gatewayV2UpgradeDriver.createVolume(ctx, state, journal, role)
}

func (d authorizedGatewayV2UpgradeDriver) createStageContainer(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (string, error) {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return "", err
	}
	return d.gatewayV2UpgradeDriver.createStageContainer(ctx, state, journal)
}

func (d authorizedGatewayV2UpgradeDriver) copyStageConfig(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, contents []byte,
) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		clear(contents)
		return err
	}
	return d.gatewayV2UpgradeDriver.copyStageConfig(ctx, state, journal, contents)
}

func (d authorizedGatewayV2UpgradeDriver) startStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2UpgradeDriver.startStage(ctx, state, journal)
}

func (d authorizedGatewayV2UpgradeDriver) stopStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2UpgradeDriver.stopStage(ctx, state, journal)
}

func (d authorizedGatewayV2UpgradeDriver) removeStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2UpgradeDriver.removeStage(ctx, state, journal)
}

type authorizedGatewayV2TransferDriver struct {
	gatewayV2TransferDriver
	gate *gatewayV2MutationAuthorizationGate
}

func (d authorizedGatewayV2TransferDriver) copyFinalConfigToStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, contents []byte,
) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		clear(contents)
		return err
	}
	return d.gatewayV2TransferDriver.copyFinalConfigToStage(ctx, state, journal, contents)
}

func (d authorizedGatewayV2TransferDriver) stopStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.stopStage(ctx, state, journal)
}

func (d authorizedGatewayV2TransferDriver) removeStage(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.removeStage(ctx, state, journal)
}

func (d authorizedGatewayV2TransferDriver) createFinal(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (string, error) {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return "", err
	}
	return d.gatewayV2TransferDriver.createFinal(ctx, state, journal)
}

func (d authorizedGatewayV2TransferDriver) stopV1(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.stopV1(ctx, source, state, journal)
}

func (d authorizedGatewayV2TransferDriver) startV1(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.startV1(ctx, source, state, journal)
}

func (d authorizedGatewayV2TransferDriver) startFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.startFinal(ctx, state, journal)
}

func (d authorizedGatewayV2TransferDriver) stopFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.stopFinal(ctx, state, journal)
}

func (d authorizedGatewayV2TransferDriver) removeFinal(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if err := d.gate.beforeMutation(ctx); err != nil {
		return err
	}
	return d.gatewayV2TransferDriver.removeFinal(ctx, state, journal)
}

var _ gatewayV2UpgradeDriver = authorizedGatewayV2UpgradeDriver{}
var _ gatewayV2TransferDriver = authorizedGatewayV2TransferDriver{}
