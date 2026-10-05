package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestInspectGatewayRebindPreclaimSuccessorPreflightAcceptsReadOnlyProposal(t *testing.T) {
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
			fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
			reads, snapshot := gatewayRebindPreclaimSuccessorTestReads(t, fixture)
			if test.predecessor != nil {
				candidate := *test.predecessor
				original := reads.network.candidates
				reads.network.candidates = func() ([]hostNetworkCandidate, error) {
					values, err := original()
					return append(values, candidate), err
				}
			}
			beforeArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
			if err != nil {
				t.Fatal(err)
			}
			beforeCommands := len(fixture.runner.commands)

			result, err := fixture.manager.inspectGatewayRebindPreclaimSuccessorPreflight(
				context.Background(), fixture.repository, fixture.proposal, reads, nil)
			if err != nil {
				t.Fatalf("preflight failed: %v", err)
			}
			claim := snapshot.ProposedClaim
			want := GatewayRebindSuccessorPreflight{
				RebindOperationID:  claim.Spec.OperationID,
				ClaimRequestDigest: claim.RequestDigest,
				Profile: GatewayRebindSuccessorProfile{
					RevisionID:     claim.Spec.SuccessorProfileRevisionID,
					RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
					OperationID:    claim.Spec.SuccessorProfileOperationID,
					RequestDigest:  claim.SuccessorProfileRequestDigest,
					SpecDigest:     claim.ConfigureApproval.SpecDigest,
					SelectedIPv4:   claim.Spec.SuccessorProfile.SelectedIPv4,
					InterfaceID:    claim.Spec.SuccessorProfile.InterfaceID,
					PortStart:      claim.Spec.SuccessorProfile.PortStart,
					PortEnd:        claim.Spec.SuccessorProfile.PortEnd,
					ApprovedBy:     claim.ConfigureApproval.ActorID,
				},
				Network: GatewayRebindSuccessorNetworkPlan{
					Subnet: "10.0.0.32/28", GatewayIPv4: "10.0.0.33", ContainerIPv4: "10.0.0.34",
				},
			}
			if !reflect.DeepEqual(result, want) {
				t.Fatalf("result=%+v, want %+v", result, want)
			}
			afterDatabase, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
			if err != nil || !reflect.DeepEqual(snapshot, afterDatabase) {
				t.Fatalf("database changed: %v", err)
			}
			afterArtifacts, err := readGatewayHistorySnapshot(fixture.manager.store)
			if err != nil || !sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) ||
				len(fixture.runner.commands) != beforeCommands {
				t.Fatalf("preflight mutated protected/Docker state: artifacts=%t commands=%d err=%v",
					sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts),
					len(fixture.runner.commands)-beforeCommands, err)
			}
			var claimCount int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims`).Scan(&claimCount); err != nil || claimCount != 0 {
				t.Fatalf("preflight persisted claim: count=%d err=%v", claimCount, err)
			}
		})
	}
}

func TestInspectGatewayRebindPreclaimSuccessorPreflightMatchesPreparedClaim(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	reads, snapshot := gatewayRebindPreclaimSuccessorTestReads(t, fixture)
	preclaim, err := fixture.manager.inspectGatewayRebindPreclaimSuccessorPreflight(
		context.Background(), fixture.repository, fixture.proposal, reads, nil)
	if err != nil {
		t.Fatalf("preclaim preflight: %v", err)
	}
	insertPreparedGatewayRebindPreclaim(t, &fixture, snapshot)
	postclaim, err := fixture.manager.inspectGatewayRebindSuccessorPreflight(
		context.Background(), fixture.repository, reads, nil)
	if err != nil {
		t.Fatalf("prepared-claim preflight: %v", err)
	}
	if !reflect.DeepEqual(preclaim, postclaim) {
		t.Fatalf("preclaim=%+v, postclaim=%+v", preclaim, postclaim)
	}
}

func TestInspectGatewayRebindPreclaimSuccessorPreflightRejectsInvalidProposalAndNetwork(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *gatewayRebindPredecessorFixture, *gatewayRebindSuccessorPreflightReads)
		code   DiagnosticCode
	}{
		{name: "existing claim", mutate: func(t *testing.T, f *gatewayRebindPredecessorFixture, _ *gatewayRebindSuccessorPreflightReads) {
			snapshot, err := f.repository.GatewayRebindPreclaimSnapshot(context.Background(), f.proposal)
			if err != nil {
				t.Fatal(err)
			}
			insertPreparedGatewayRebindPreclaim(t, f, snapshot)
		}, code: DiagnosticRouteUnresolved},
		{name: "approval changed", mutate: func(_ *testing.T, f *gatewayRebindPredecessorFixture, _ *gatewayRebindSuccessorPreflightReads) {
			f.proposal.ConfigureApproval.SpecDigest = strings.Repeat("0", 64)
		}, code: DiagnosticRouteUnresolved},
		{name: "missing successor", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) {
			reads.network.candidates = func() ([]hostNetworkCandidate, error) { return nil, nil }
		}, code: DiagnosticIngressDrift},
		{name: "ambiguous successor", mutate: func(_ *testing.T, f *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) {
			selected := gatewayRebindPreclaimSuccessorCandidate(f.proposal.Spec)
			reads.network.candidates = func() ([]hostNetworkCandidate, error) {
				return []hostNetworkCandidate{selected, {
					InterfaceID: "other-interface", IPv4: selected.IPv4, Prefix: selected.Prefix,
				}}, nil
			}
		}, code: DiagnosticIngressDrift},
		{name: "network space exhausted", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) {
			reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
				return gatewayV2HostNetworkSnapshot{Routes: gatewayV2PrivateNetworkPools[:]}, nil
			}
		}, code: DiagnosticIngressDrift},
		{name: "malformed Docker identity", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) {
			reads.dockerIDs = func(context.Context) ([]string, error) { return []string{"bad-id"}, nil }
		}, code: DiagnosticIngressDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
			reads, _ := gatewayRebindPreclaimSuccessorTestReads(t, fixture)
			test.mutate(t, &fixture, &reads)
			result, err := fixture.manager.inspectGatewayRebindPreclaimSuccessorPreflight(
				context.Background(), fixture.repository, fixture.proposal, reads, nil)
			if !IsCode(err, test.code) || result != (GatewayRebindSuccessorPreflight{}) {
				t.Fatalf("result=%+v error=%v, want %s", result, err, test.code)
			}
		})
	}
}

func TestInspectGatewayRebindPreclaimSuccessorPreflightRejectsDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *gatewayRebindPredecessorFixture, *gatewayRebindSuccessorPreflightReads) func()
		code   DiagnosticCode
	}{
		{name: "candidate", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.network.candidates
			reads.network.candidates = func() ([]hostNetworkCandidate, error) {
				calls++
				values, err := original()
				if calls == 2 {
					values = append(values, hostNetworkCandidate{InterfaceID: "unrelated", IPv4: "192.168.120.8", Prefix: netip.MustParsePrefix("192.168.120.0/24")})
				}
				return values, err
			}
			return nil
		}},
		{name: "host prefixes", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
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
		{name: "Docker prefixes", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
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
		{name: "Docker identity replacement", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
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
		{name: "Docker identity changed during inventory", mutate: func(_ *testing.T, _ *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			reads.dockerIDs = func(context.Context) ([]string, error) {
				calls++
				if calls == 1 {
					return []string{strings.Repeat("a", 64)}, nil
				}
				return []string{strings.Repeat("b", 64)}, nil
			}
			return nil
		}, code: DiagnosticIngressDrift},
		{name: "SQLite approval at checkpoint", mutate: func(t *testing.T, f *gatewayRebindPredecessorFixture, _ *gatewayRebindSuccessorPreflightReads) func() {
			return func() { demoteGatewayRebindPreclaimAdministrator(t, f) }
		}},
		{name: "SQLite approval after second inventory", mutate: func(t *testing.T, f *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.dockerIDs
			reads.dockerIDs = func(ctx context.Context) ([]string, error) {
				calls++
				ids, err := original(ctx)
				if calls == 4 {
					demoteGatewayRebindPreclaimAdministrator(t, f)
				}
				return ids, err
			}
			return nil
		}},
		{name: "claim inserted after second inventory", mutate: func(t *testing.T, f *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			snapshot, err := f.repository.GatewayRebindPreclaimSnapshot(context.Background(), f.proposal)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			original := reads.dockerIDs
			reads.dockerIDs = func(ctx context.Context) ([]string, error) {
				calls++
				ids, err := original(ctx)
				if calls == 4 {
					insertPreparedGatewayRebindPreclaim(t, f, snapshot)
				}
				return ids, err
			}
			return nil
		}},
		{name: "SQLite runtime head after second inventory", mutate: func(t *testing.T, f *gatewayRebindPredecessorFixture, reads *gatewayRebindSuccessorPreflightReads) func() {
			calls := 0
			original := reads.dockerIDs
			reads.dockerIDs = func(ctx context.Context) ([]string, error) {
				calls++
				ids, err := original(ctx)
				if calls == 4 {
					if _, err := f.db.Exec(`DROP TRIGGER generated_runtime_active_head_valid_update`); err != nil {
						t.Fatal(err)
					}
					if _, err := f.db.Exec(`UPDATE generated_runtime_active_heads SET generation=generation+1 WHERE app_id=?`, f.proposal.Roster[0].AppID); err != nil {
						t.Fatal(err)
					}
				}
				return ids, err
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
			reads, _ := gatewayRebindPreclaimSuccessorTestReads(t, fixture)
			checkpoint := test.mutate(t, &fixture, &reads)
			result, err := fixture.manager.inspectGatewayRebindPreclaimSuccessorPreflight(
				context.Background(), fixture.repository, fixture.proposal, reads, checkpoint)
			code := test.code
			if code == "" {
				code = DiagnosticRouteUnresolved
			}
			if !IsCode(err, code) || result != (GatewayRebindSuccessorPreflight{}) {
				t.Fatalf("result=%+v error=%v, want %s", result, err, code)
			}
		})
	}
}

func TestInspectGatewayRebindPreclaimSuccessorPreflightCancellationAndLockRelease(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		reads, _ := gatewayRebindPreclaimSuccessorTestReads(t, fixture)
		ctx, cancel := context.WithCancel(context.Background())
		result, err := fixture.manager.inspectGatewayRebindPreclaimSuccessorPreflight(
			ctx, fixture.repository, fixture.proposal, reads, cancel)
		if !IsCode(err, DiagnosticCancelled) || result != (GatewayRebindSuccessorPreflight{}) {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	})
	t.Run("release failure", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
		reads, _ := gatewayRebindPreclaimSuccessorTestReads(t, fixture)
		originalAcquire := managerAcquireGatewayOSLock
		acquired, released := false, false
		managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
			acquired = true
			return func() error {
				released = true
				return errors.New("injected release failure")
			}, nil
		}
		t.Cleanup(func() { managerAcquireGatewayOSLock = originalAcquire })
		result, err := fixture.manager.inspectGatewayRebindPreclaimSuccessorPreflight(
			context.Background(), fixture.repository, fixture.proposal, reads, nil)
		if !IsCode(err, DiagnosticRouteUnresolved) || result != (GatewayRebindSuccessorPreflight{}) || !acquired || !released {
			t.Fatalf("result=%+v error=%v acquired=%t released=%t", result, err, acquired, released)
		}
		if !fixture.manager.mu.TryLock() {
			t.Fatal("Manager lock remained held")
		}
		fixture.manager.mu.Unlock()
	})
}

func gatewayRebindPreclaimSuccessorTestReads(t *testing.T, fixture gatewayRebindPredecessorFixture) (
	gatewayRebindSuccessorPreflightReads, appaccess.GatewayRebindPreclaimSnapshot,
) {
	t.Helper()
	snapshot, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), fixture.proposal)
	if err != nil {
		t.Fatal(err)
	}
	candidate := gatewayRebindPreclaimSuccessorCandidate(snapshot.ProposedClaim.Spec)
	return gatewayRebindSuccessorPreflightReads{
		network: gatewayV2NetworkPlanReads{
			candidates: func() ([]hostNetworkCandidate, error) { return []hostNetworkCandidate{candidate}, nil },
			host: func() (gatewayV2HostNetworkSnapshot, error) {
				return gatewayV2HostNetworkSnapshot{Routes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/28")}}, nil
			},
			docker: func(context.Context) ([]netip.Prefix, error) {
				return []netip.Prefix{netip.MustParsePrefix("10.0.0.16/28")}, nil
			},
		},
		dockerIDs: func(context.Context) ([]string, error) { return []string{strings.Repeat("a", 64)}, nil },
	}, snapshot
}

func gatewayRebindPreclaimSuccessorCandidate(spec appaccess.GatewayRebindSpec) hostNetworkCandidate {
	selected := netip.MustParseAddr(spec.SuccessorProfile.SelectedIPv4)
	return hostNetworkCandidate{
		InterfaceID: spec.SuccessorProfile.InterfaceID,
		IPv4:        selected.String(), Prefix: netip.PrefixFrom(selected, 24).Masked(),
	}
}

func demoteGatewayRebindPreclaimAdministrator(t *testing.T, fixture *gatewayRebindPredecessorFixture) {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, gatewayRebindTestAdministrator); err != nil {
		t.Fatal(err)
	}
}

func insertPreparedGatewayRebindPreclaim(t *testing.T, fixture *gatewayRebindPredecessorFixture,
	snapshot appaccess.GatewayRebindPreclaimSnapshot,
) {
	t.Helper()
	claim := snapshot.ProposedClaim
	now := time.Now().UTC()
	claim.RebindApprovedAt = now
	claim.ConfigureApprovedAt = now
	claim.CreatedAt = now
	claim.UpdatedAt = now
	insertGatewayRebindPredecessorClaim(t, fixture.db, claim)
	for _, entry := range snapshot.Proposal.Roster {
		insertGatewayRebindPredecessorRoster(t, fixture.db, entry)
	}
}
