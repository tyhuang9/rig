package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestInspectGatewayRebindSuccessorPreflightAcceptsMissingOrDriftedPredecessor(t *testing.T) {
	for _, test := range []struct {
		name        string
		predecessor *hostNetworkCandidate
	}{
		{name: "predecessor absent"},
		{name: "predecessor DHCP drifted", predecessor: &hostNetworkCandidate{
			InterfaceID: "rebind-predecessor", IPv4: "192.168.96.99",
			Prefix: netip.MustParsePrefix("192.168.96.0/24"),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			reads, claim := gatewayRebindSuccessorTestReads(t, fixture)
			if test.predecessor != nil {
				original := reads.network.candidates
				reads.network.candidates = func() ([]hostNetworkCandidate, error) {
					values, err := original()
					return append(values, *test.predecessor), err
				}
			}
			beforeDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			beforeArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
			if err != nil {
				t.Fatal(err)
			}
			beforeCommands := len(fixture.runner.commands)

			result, err := fixture.manager.inspectGatewayRebindSuccessorPreflight(
				context.Background(), fixture.repository, reads, nil,
			)
			if err != nil {
				t.Fatalf("preflight failed: %v", err)
			}
			want := GatewayRebindSuccessorPreflight{
				RebindOperationID:  claim.Claim.Spec.OperationID,
				ClaimRequestDigest: claim.Claim.RequestDigest,
				Profile: GatewayRebindSuccessorProfile{
					RevisionID:     claim.Claim.Spec.SuccessorProfileRevisionID,
					RevisionNumber: claim.Claim.Spec.SuccessorProfileRevisionNumber,
					OperationID:    claim.Claim.Spec.SuccessorProfileOperationID,
					RequestDigest:  claim.Claim.SuccessorProfileRequestDigest,
					SpecDigest:     claim.Claim.ConfigureApproval.SpecDigest,
					SelectedIPv4:   claim.Claim.Spec.SuccessorProfile.SelectedIPv4,
					InterfaceID:    claim.Claim.Spec.SuccessorProfile.InterfaceID,
					PortStart:      claim.Claim.Spec.SuccessorProfile.PortStart,
					PortEnd:        claim.Claim.Spec.SuccessorProfile.PortEnd,
					ApprovedBy:     claim.Claim.ConfigureApproval.ActorID,
				},
				Network: GatewayRebindSuccessorNetworkPlan{
					Subnet: "10.0.0.32/28", GatewayIPv4: "10.0.0.33", ContainerIPv4: "10.0.0.34",
				},
			}
			if !reflect.DeepEqual(result, want) {
				t.Fatalf("result=%+v, want %+v", result, want)
			}

			afterDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			afterArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
			if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) ||
				!sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) ||
				len(fixture.runner.commands) != beforeCommands {
				t.Fatalf("preflight mutated state: database=%t artifacts=%t commands=%d err=%v",
					reflect.DeepEqual(beforeDatabase, afterDatabase),
					sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts),
					len(fixture.runner.commands)-beforeCommands, err)
			}
		})
	}
}

func TestInspectGatewayRebindSuccessorPreflightRejectsMissingOrAmbiguousSuccessor(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(gatewayRebindSuccessorPreflightReads, appaccess.GatewayRebindStartupClaim) gatewayRebindSuccessorPreflightReads
	}{
		{name: "missing", mutate: func(reads gatewayRebindSuccessorPreflightReads, _ appaccess.GatewayRebindStartupClaim) gatewayRebindSuccessorPreflightReads {
			reads.network.candidates = func() ([]hostNetworkCandidate, error) { return nil, nil }
			return reads
		}},
		{name: "ambiguous", mutate: func(reads gatewayRebindSuccessorPreflightReads, claim appaccess.GatewayRebindStartupClaim) gatewayRebindSuccessorPreflightReads {
			selected := gatewayRebindSuccessorCandidate(claim)
			reads.network.candidates = func() ([]hostNetworkCandidate, error) {
				return []hostNetworkCandidate{selected, {
					InterfaceID: "other-interface", IPv4: selected.IPv4, Prefix: selected.Prefix,
				}}, nil
			}
			return reads
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			reads, claim := gatewayRebindSuccessorTestReads(t, fixture)
			result, err := fixture.manager.inspectGatewayRebindSuccessorPreflight(
				context.Background(), fixture.repository, test.mutate(reads, claim), nil,
			)
			if !IsCode(err, DiagnosticIngressDrift) || result != (GatewayRebindSuccessorPreflight{}) {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestInspectGatewayRebindSuccessorPreflightRejectsObservationAndSQLiteDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindPredecessorFixture, *gatewayRebindSuccessorPreflightReads) func()
	}{
		{name: "candidate drift", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.network.candidates
			reads.network.candidates = func() ([]hostNetworkCandidate, error) {
				calls++
				values, err := original()
				if calls == 2 {
					values = append(values, hostNetworkCandidate{
						InterfaceID: "unrelated", IPv4: "192.168.120.8",
						Prefix: netip.MustParsePrefix("192.168.120.0/24"),
					})
				}
				return values, err
			}
			return nil
		}},
		{name: "host drift", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.network.host
			reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
				calls++
				value, err := original()
				if calls == 2 {
					value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("192.168.121.0/24"))
				}
				return value, err
			}
			return nil
		}},
		{name: "Docker drift", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.network.docker
			reads.network.docker = func(ctx context.Context) ([]netip.Prefix, error) {
				calls++
				value, err := original(ctx)
				if calls == 2 {
					value = append(value, netip.MustParsePrefix("192.168.122.0/24"))
				}
				return value, err
			}
			return nil
		}},
		{name: "Docker unstable", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.network.docker
			reads.network.docker = func(ctx context.Context) ([]netip.Prefix, error) {
				calls++
				if calls == 2 {
					return nil, errors.New("inventory changed")
				}
				return original(ctx)
			}
			return nil
		}},
		{name: "Docker ID replacement with same prefix", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			reads.dockerIDs = func(context.Context) ([]string, error) {
				calls++
				if calls <= 2 {
					return []string{strings.Repeat("a", 64)}, nil
				}
				return []string{strings.Repeat("b", 64)}, nil
			}
			return nil
		}},
		{name: "Docker ID changed during inventory read", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			reads.dockerIDs = func(context.Context) ([]string, error) {
				calls++
				if calls == 1 {
					return []string{strings.Repeat("a", 64)}, nil
				}
				return []string{strings.Repeat("b", 64)}, nil
			}
			return nil
		}},
		{name: "Docker ID inventory malformed", mutate: func(_ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			reads.dockerIDs = func(context.Context) ([]string, error) {
				return []string{"not-a-network-id"}, nil
			}
			return nil
		}},
		{name: "SQLite drift", mutate: func(fixture *gatewayRebindPredecessorFixture, _ *gatewayRebindSuccessorPreflightReads) func() {
			return func() {
				if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, gatewayRebindTestAdministrator); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixture(t)
			reads, _ := gatewayRebindSuccessorTestReads(t, fixture)
			checkpoint := test.mutate(&fixture, &reads)
			result, err := fixture.manager.inspectGatewayRebindSuccessorPreflight(
				context.Background(), fixture.repository, reads, checkpoint,
			)
			if err == nil || result != (GatewayRebindSuccessorPreflight{}) {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestInspectGatewayRebindSuccessorPreflightCancellationAndLockReleaseFailClosed(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		reads, _ := gatewayRebindSuccessorTestReads(t, fixture)
		ctx, cancel := context.WithCancel(context.Background())
		result, err := fixture.manager.inspectGatewayRebindSuccessorPreflight(ctx, fixture.repository, reads, cancel)
		if !IsCode(err, DiagnosticCancelled) || result != (GatewayRebindSuccessorPreflight{}) {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	})

	t.Run("release failure", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		reads, _ := gatewayRebindSuccessorTestReads(t, fixture)
		originalAcquire := managerAcquireGatewayOSLock
		acquired := false
		released := false
		managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
			acquired = true
			return func() error {
				released = true
				return errors.New("injected release failure")
			}, nil
		}
		t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })

		result, err := fixture.manager.inspectGatewayRebindSuccessorPreflight(
			context.Background(), fixture.repository, reads, nil,
		)
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (GatewayRebindSuccessorPreflight{}) || !acquired || !released {
			t.Fatalf("result=%+v error=%v acquired=%t released=%t", result, err, acquired, released)
		}
		if !fixture.manager.mu.TryLock() {
			t.Fatal("Manager lock remained held")
		}
		fixture.manager.mu.Unlock()
	})
}

func gatewayRebindSuccessorTestReads(t *testing.T,
	fixture gatewayRebindPredecessorFixture,
) (gatewayRebindSuccessorPreflightReads, appaccess.GatewayRebindStartupClaim) {
	t.Helper()
	snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || len(snapshot.Claims) != 1 {
		t.Fatalf("startup snapshot claims=%d err=%v", len(snapshot.Claims), err)
	}
	claim := snapshot.Claims[0]
	candidate := gatewayRebindSuccessorCandidate(claim)
	return gatewayRebindSuccessorPreflightReads{
		network: gatewayV2NetworkPlanReads{
			candidates: func() ([]hostNetworkCandidate, error) {
				return []hostNetworkCandidate{candidate}, nil
			},
			host: func() (gatewayV2HostNetworkSnapshot, error) {
				return gatewayV2HostNetworkSnapshot{
					Routes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/28")},
				}, nil
			},
			docker: func(context.Context) ([]netip.Prefix, error) {
				return []netip.Prefix{netip.MustParsePrefix("10.0.0.16/28")}, nil
			},
		},
		dockerIDs: func(context.Context) ([]string, error) {
			return []string{strings.Repeat("a", 64)}, nil
		},
	}, claim
}

func gatewayRebindSuccessorCandidate(claim appaccess.GatewayRebindStartupClaim) hostNetworkCandidate {
	selected := netip.MustParseAddr(claim.Claim.Spec.SuccessorProfile.SelectedIPv4)
	return hostNetworkCandidate{
		InterfaceID: claim.Claim.Spec.SuccessorProfile.InterfaceID,
		IPv4:        selected.String(), Prefix: netip.PrefixFrom(selected, 24).Masked(),
	}
}
