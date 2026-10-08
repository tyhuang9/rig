package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// gatewayRebindRuntimeCleanupBoundary is deliberately small. The live adapter
// supplies fresh Docker and durable-store observations; this boundary lets the
// denial cases exercise the same stop/remove ordering without a Docker daemon.
type gatewayRebindRuntimeCleanupBoundary interface {
	Snapshot(context.Context) (gatewayRebindRuntimeCleanupSnapshot, error)
	Effect(context.Context, ...string) error
}

type gatewayRebindRuntimeCleanupAuthority struct {
	OperationID        string
	TerminalDigest     string
	SQLDigest          string
	ProtectedDigest    string
	RuntimeHeadsDigest string
}

type gatewayRebindRuntimeCleanupContainer struct {
	Present        bool
	ID             string
	ImageID        string
	Running        bool
	Labels         map[string]string
	ListenerAbsent bool
}

type gatewayRebindRuntimeCleanupVolume struct {
	Present    bool
	Name       string
	CreatedAt  string
	Mountpoint string
	Labels     map[string]string
	Consumers  int
}

type gatewayRebindRuntimeCleanupNetwork struct {
	Present bool
	ID      string
	Labels  map[string]string
	IPAM    []networkIPAM
	Members int
}

type gatewayRebindRuntimeCleanupSnapshot struct {
	Authority    gatewayRebindRuntimeCleanupAuthority
	StagePresent bool
	Final        gatewayRebindRuntimeCleanupContainer
	Config       gatewayRebindRuntimeCleanupVolume
	Data         gatewayRebindRuntimeCleanupVolume
	Ingress      gatewayRebindRuntimeCleanupNetwork
}

type gatewayRebindRuntimeCleanupPlan struct {
	Authority   gatewayRebindRuntimeCleanupAuthority
	FinalID     string
	ImageID     string
	FinalLabels map[string]string
	Config      gatewayRebindRuntimeCleanupVolume
	Data        gatewayRebindRuntimeCleanupVolume
	Ingress     gatewayRebindRuntimeCleanupNetwork
}

func validGatewayRebindRuntimeCleanupAuthority(value gatewayRebindRuntimeCleanupAuthority) bool {
	return value.OperationID != "" && value.TerminalDigest != "" && value.SQLDigest != "" &&
		value.ProtectedDigest != "" && value.RuntimeHeadsDigest != ""
}

func validGatewayRebindRuntimeCleanupSnapshot(value gatewayRebindRuntimeCleanupSnapshot,
	plan gatewayRebindRuntimeCleanupPlan,
) bool {
	if !validGatewayRebindRuntimeCleanupAuthority(plan.Authority) || value.Authority != plan.Authority || value.StagePresent {
		return false
	}
	if value.Final.Present && (value.Final.ID != plan.FinalID || value.Final.ImageID != plan.ImageID || !reflect.DeepEqual(value.Final.Labels, plan.FinalLabels)) {
		return false
	}
	if (!value.Final.Present || !value.Final.Running) && !value.Final.ListenerAbsent {
		return false
	}
	if value.Config.Present && (value.Config.Name != plan.Config.Name || value.Config.CreatedAt != plan.Config.CreatedAt ||
		value.Config.Mountpoint != plan.Config.Mountpoint || !reflect.DeepEqual(value.Config.Labels, plan.Config.Labels) || value.Config.Consumers != 0) {
		return false
	}
	if value.Data.Present && (value.Data.Name != plan.Data.Name || value.Data.CreatedAt != plan.Data.CreatedAt ||
		value.Data.Mountpoint != plan.Data.Mountpoint || !reflect.DeepEqual(value.Data.Labels, plan.Data.Labels) || value.Data.Consumers != 0) {
		return false
	}
	return !value.Ingress.Present || (value.Ingress.ID == plan.Ingress.ID &&
		reflect.DeepEqual(value.Ingress.Labels, plan.Ingress.Labels) && reflect.DeepEqual(value.Ingress.IPAM, plan.Ingress.IPAM) &&
		value.Ingress.Members == 0)
}

// cleanupGatewayRebindRuntimeTerminal performs only one exact resource effect
// after each fresh stable proof. An uncertain command acknowledgement never
// advances to the next resource.
func cleanupGatewayRebindRuntimeTerminal(ctx context.Context, boundary gatewayRebindRuntimeCleanupBoundary,
	plan gatewayRebindRuntimeCleanupPlan,
) error {
	if ctx == nil || boundary == nil || !validGatewayRebindRuntimeCleanupAuthority(plan.Authority) ||
		plan.FinalID == "" || plan.ImageID == "" || plan.Config.Name == "" || plan.Data.Name == "" || plan.Ingress.ID == "" {
		return errors.New("typed runtime cleanup lacks exact authority")
	}
	for step := 0; step < 6; step++ {
		snapshot, err := boundary.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("typed runtime cleanup snapshot failed: %w", err)
		}
		if !validGatewayRebindRuntimeCleanupSnapshot(snapshot, plan) {
			return errors.New("typed runtime cleanup observations are uncertain")
		}
		var args []string
		switch {
		case snapshot.Final.Present && snapshot.Final.Running:
			args = []string{"container", "stop", "--time", "10", plan.FinalID}
		case snapshot.Final.Present:
			args = []string{"container", "rm", plan.FinalID}
		case snapshot.Config.Present:
			args = []string{"volume", "rm", plan.Config.Name}
		case snapshot.Data.Present:
			args = []string{"volume", "rm", plan.Data.Name}
		case snapshot.Ingress.Present:
			args = []string{"network", "rm", plan.Ingress.ID}
		default:
			return nil
		}
		if err := boundary.Effect(ctx, args...); err != nil {
			return fmt.Errorf("typed runtime cleanup effect acknowledgement is uncertain: %w", err)
		}
	}
	return errors.New("typed runtime cleanup exceeded its exact effect bound")
}

type gatewayRebindRuntimeCleanupFake struct {
	snapshot gatewayRebindRuntimeCleanupSnapshot
	effects  [][]string
	fail     error
	drift    func(*gatewayRebindRuntimeCleanupSnapshot)
}

func (f *gatewayRebindRuntimeCleanupFake) Snapshot(context.Context) (gatewayRebindRuntimeCleanupSnapshot, error) {
	return f.snapshot, nil
}

func (f *gatewayRebindRuntimeCleanupFake) Effect(_ context.Context, args ...string) error {
	f.effects = append(f.effects, append([]string(nil), args...))
	if f.fail != nil {
		return f.fail
	}
	switch {
	case reflect.DeepEqual(args, []string{"container", "stop", "--time", "10", f.snapshot.Final.ID}):
		f.snapshot.Final.Running = false
		f.snapshot.Final.ListenerAbsent = true
	case reflect.DeepEqual(args, []string{"container", "rm", f.snapshot.Final.ID}):
		f.snapshot.Final = gatewayRebindRuntimeCleanupContainer{ListenerAbsent: true}
	case reflect.DeepEqual(args, []string{"volume", "rm", f.snapshot.Config.Name}):
		f.snapshot.Config = gatewayRebindRuntimeCleanupVolume{}
	case reflect.DeepEqual(args, []string{"volume", "rm", f.snapshot.Data.Name}):
		f.snapshot.Data = gatewayRebindRuntimeCleanupVolume{}
	case reflect.DeepEqual(args, []string{"network", "rm", f.snapshot.Ingress.ID}):
		f.snapshot.Ingress = gatewayRebindRuntimeCleanupNetwork{}
	default:
		return errors.New("unexpected exact cleanup effect")
	}
	if f.drift != nil {
		f.drift(&f.snapshot)
		f.drift = nil
	}
	return nil
}

func newGatewayRebindRuntimeCleanupFixture() (gatewayRebindRuntimeCleanupPlan, *gatewayRebindRuntimeCleanupFake) {
	authority := gatewayRebindRuntimeCleanupAuthority{OperationID: "operation", TerminalDigest: "terminal", SQLDigest: "sql", ProtectedDigest: "protected", RuntimeHeadsDigest: "heads"}
	labels := map[string]string{"managed": "typed", "operation": "operation"}
	plan := gatewayRebindRuntimeCleanupPlan{Authority: authority, FinalID: "final-id", ImageID: "image-id", FinalLabels: labels,
		Config:  gatewayRebindRuntimeCleanupVolume{Name: "config", CreatedAt: "created", Mountpoint: "/volumes/config", Labels: labels},
		Data:    gatewayRebindRuntimeCleanupVolume{Name: "data", CreatedAt: "created", Mountpoint: "/volumes/data", Labels: labels},
		Ingress: gatewayRebindRuntimeCleanupNetwork{ID: "network-id", Labels: labels, IPAM: []networkIPAM{{Subnet: "192.0.2.0/24", Gateway: "192.0.2.1"}}}}
	fake := &gatewayRebindRuntimeCleanupFake{snapshot: gatewayRebindRuntimeCleanupSnapshot{Authority: authority,
		Final:  gatewayRebindRuntimeCleanupContainer{Present: true, ID: plan.FinalID, ImageID: plan.ImageID, Running: true, Labels: labels},
		Config: plan.Config, Data: plan.Data, Ingress: plan.Ingress}}
	fake.snapshot.Config.Present, fake.snapshot.Data.Present, fake.snapshot.Ingress.Present = true, true, true
	return plan, fake
}

func TestGatewayRebindRuntimeCleanupRejectsUnownedResources(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(*gatewayRebindRuntimeCleanupFake)
	}{
		{name: "crossed final ID", apply: func(f *gatewayRebindRuntimeCleanupFake) { f.snapshot.Final.ID = "other" }},
		{name: "crossed final labels", apply: func(f *gatewayRebindRuntimeCleanupFake) {
			f.snapshot.Final.Labels = map[string]string{"managed": "other"}
		}},
		{name: "volume consumer", apply: func(f *gatewayRebindRuntimeCleanupFake) { f.snapshot.Config.Consumers = 1 }},
		{name: "network member", apply: func(f *gatewayRebindRuntimeCleanupFake) { f.snapshot.Ingress.Members = 1 }},
		{name: "absent final listener remains live", apply: func(f *gatewayRebindRuntimeCleanupFake) { f.snapshot.Final = gatewayRebindRuntimeCleanupContainer{} }},
		{name: "SQL drift", apply: func(f *gatewayRebindRuntimeCleanupFake) { f.snapshot.Authority.SQLDigest = "other" }},
		{name: "protected drift", apply: func(f *gatewayRebindRuntimeCleanupFake) { f.snapshot.Authority.ProtectedDigest = "other" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			plan, fake := newGatewayRebindRuntimeCleanupFixture()
			mutate.apply(fake)
			if err := cleanupGatewayRebindRuntimeTerminal(context.Background(), fake, plan); err == nil || len(fake.effects) != 0 {
				t.Fatalf("unowned resource reached effect boundary: effects=%v err=%v", fake.effects, err)
			}
		})
	}
}

func TestGatewayRebindRuntimeCleanupStopsOnUncertainEffects(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*gatewayRebindRuntimeCleanupFake)
	}{
		{name: "stop acknowledgement", setup: func(f *gatewayRebindRuntimeCleanupFake) { f.fail = errors.New("lost stop acknowledgement") }},
		{name: "post stop authority drift", setup: func(f *gatewayRebindRuntimeCleanupFake) {
			f.drift = func(snapshot *gatewayRebindRuntimeCleanupSnapshot) { snapshot.Authority.RuntimeHeadsDigest = "other" }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, fake := newGatewayRebindRuntimeCleanupFixture()
			test.setup(fake)
			if err := cleanupGatewayRebindRuntimeTerminal(context.Background(), fake, plan); err == nil || len(fake.effects) != 1 ||
				!reflect.DeepEqual(fake.effects[0], []string{"container", "stop", "--time", "10", plan.FinalID}) || !fake.snapshot.Config.Present || !fake.snapshot.Data.Present || !fake.snapshot.Ingress.Present {
				t.Fatalf("uncertain acknowledgement advanced cleanup: effects=%v err=%v", fake.effects, err)
			}
		})
	}
}

func TestGatewayRebindRuntimeCleanupRemovesOnlyExactResourcesInOrder(t *testing.T) {
	plan, fake := newGatewayRebindRuntimeCleanupFixture()
	if err := cleanupGatewayRebindRuntimeTerminal(context.Background(), fake, plan); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"container", "stop", "--time", "10", plan.FinalID}, {"container", "rm", plan.FinalID},
		{"volume", "rm", plan.Config.Name}, {"volume", "rm", plan.Data.Name}, {"network", "rm", plan.Ingress.ID}}
	if !reflect.DeepEqual(fake.effects, want) || fake.snapshot.Final.Present || fake.snapshot.Config.Present || fake.snapshot.Data.Present || fake.snapshot.Ingress.Present {
		t.Fatalf("exact cleanup order=%v", fake.effects)
	}
}
