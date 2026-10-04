package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// Sequence seven validates immutable history by regenerating this empty-route
// Caddy JSON. A future config builder change must preserve these v1 bytes for
// existing records and introduce an explicit format version for new records.
func TestGatewayRebindStageConfigV1Bytes(t *testing.T) {
	body, err := buildCaddyConfigV2(map[string]routeRecord{}, "172.28.0.3:8080",
		caddyV2Profile{SelectedIPv4: "192.168.44.10", PortStart: 8100, PortEnd: 8101,
			ProbeToken: strings.Repeat("a", 64)}, map[uint16]caddyV2LANAssignment{})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	const wantLength = 1091
	const wantDigest = "474f1cced60d289b2def43dfcf6bc4cf1a0d689ded6adae9a11a7c8fb9240b52"
	if len(body) != wantLength || hex.EncodeToString(sum[:]) != wantDigest {
		t.Fatalf("v1 config byte contract: length=%d digest=%x", len(body), sum)
	}
}

func TestGatewayRebindStageConfigProbeTokenV1(t *testing.T) {
	const protectedIntentDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const operationID = "11111111-1111-4111-8111-111111111111"
	const identityDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const networkDigest = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	got, err := gatewayRebindStageConfigProbeTokenV1(protectedIntentDigest, operationID,
		identityDigest, networkDigest)
	if err != nil {
		t.Fatal(err)
	}
	const want = "5267f120384aa4f276ef297ee785b0e9e5486cba509e8902e68fa2dfc72cc2ba"
	if got != want {
		t.Fatalf("v1 probe token contract: got %s", got)
	}

	fixture := newGatewayRebindEffectBoundaryFixture(t)
	actual, err := gatewayRebindStageConfigProbeToken(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := gatewayRebindStageConfigProbeTokenV1(fixture.intent.Digest,
		fixture.intent.OperationID, fixture.intent.Intent.Identity.Digest,
		fixture.intent.Intent.NetworkDigest)
	if err != nil || actual != expected {
		t.Fatalf("probe token binding mismatch: error=%v", err)
	}
}

type gatewayRebindStageConfigIntentFake struct {
	*gatewayRebindStageContainerFake
	empty          bool
	imageDrift     bool
	inventoryErr   error
	inventoryCalls int
}

func (f *gatewayRebindStageConfigIntentFake) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	if f.imageDrift {
		return imageInspection{ID: "sha256:" + strings.Repeat("f", 64), OS: "linux",
			RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}, true, nil
	}
	return f.gatewayRebindStageContainerFake.inspectImage(ctx)
}

func (f *gatewayRebindStageConfigIntentFake) configVolumeEmpty(_ context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) (bool, error) {
	f.inventoryCalls++
	if !reflect.DeepEqual(intent, f.intent) || stage.StageContainer == nil || stage.ConfigVolume == nil ||
		stage.StageContainer.ID != f.containerID || stage.ConfigVolume.Name != intent.Intent.Identity.ConfigVolume {
		return false, errors.New("unexpected config volume inventory input")
	}
	return f.empty, f.inventoryErr
}

func TestGatewayRebindStageConfigIsDeterministicProbeAnd404Only(t *testing.T) {
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	first, err := gatewayRebindStageConfigBytes(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first)
	second, err := gatewayRebindStageConfigBytes(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(second)
	if !bytes.Equal(first, second) || !json.Valid(first) || bytes.Contains(first, []byte("reverse_proxy")) {
		t.Fatalf("stage config deterministic=%t valid=%t contains_proxy=%t",
			bytes.Equal(first, second), json.Valid(first), bytes.Contains(first, []byte("reverse_proxy")))
	}
	var config caddyConfig
	if err := json.Unmarshal(first, &config); err != nil {
		t.Fatal(err)
	}
	local, exists := config.Apps.HTTP.Servers["generated"]
	if !exists || !reflect.DeepEqual(local.Listen, []string{net.JoinHostPort(fixture.intent.Intent.Network.ContainerIPv4,
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10))}) ||
		len(local.Routes) != 1 || local.Routes[0].Handle[0].Handler != "static_response" ||
		local.Routes[0].Handle[0].StatusCode != 404 {
		t.Fatalf("local server is not 404-only: %#v", local)
	}
	wantServers := int(fixture.intent.Intent.SuccessorProfile.PortEnd-fixture.intent.Intent.SuccessorProfile.PortStart) + 2
	if len(config.Apps.HTTP.Servers) != wantServers {
		t.Fatalf("server count=%d want=%d", len(config.Apps.HTTP.Servers), wantServers)
	}
	probe, err := gatewayRebindStageConfigProbeToken(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	for port := fixture.intent.Intent.SuccessorProfile.PortStart; ; port++ {
		server := config.Apps.HTTP.Servers[lanServerName(port)]
		challenge := gatewayV2PortChallenge(probe, port)
		if len(server.Routes) != 2 || len(server.Routes[0].Match) != 1 ||
			!reflect.DeepEqual(server.Listen, []string{net.JoinHostPort(fixture.intent.Intent.Network.ContainerIPv4,
				strconv.FormatUint(uint64(port), 10))}) ||
			!reflect.DeepEqual(server.Routes[0].Match[0].Host,
				[]string{fixture.intent.Intent.SuccessorProfile.SelectedIPv4}) ||
			!reflect.DeepEqual(server.Routes[0].Match[0].Path,
				[]string{"/.well-known/rig-gateway/" + challenge}) ||
			server.Routes[0].Handle[0].Handler != "static_response" ||
			server.Routes[0].Handle[0].StatusCode != 404 ||
			server.Routes[0].Handle[0].Body != "rig-gateway-v2:"+challenge ||
			server.Routes[1].Handle[0].Handler != "static_response" || server.Routes[1].Handle[0].StatusCode != 404 {
			t.Fatalf("LAN port %d is not approved-IP probe plus 404: %#v", port, server)
		}
		if port == fixture.intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
}

func TestGatewayRebindExactEmptyConfigVolumeArchive(t *testing.T) {
	for _, test := range []struct {
		name    string
		archive func(*testing.T) []byte
		valid   bool
	}{
		{name: "empty dot root", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir, Mode: 0o755}})
		}, valid: true},
		{name: "empty slash root", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}})
		}, valid: true},
		{name: "stage config exists", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}, {Name: "stage.json", Typeflag: tar.TypeReg, Size: 2}})
		}},
		{name: "foreign file", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}, {Name: "default.json", Typeflag: tar.TypeReg, Size: 2}})
		}},
		{name: "symlink", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}, {Name: "stage.json", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"}})
		}},
		{name: "disguised root", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: "foo/..", Typeflag: tar.TypeDir}})
		}},
		{name: "traversal root", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: "../", Typeflag: tar.TypeDir}})
		}},
		{name: "absolute root", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: "/", Typeflag: tar.TypeDir}})
		}},
		{name: "duplicate root", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}, {Name: "./", Typeflag: tar.TypeDir}})
		}},
		{name: "root regular payload", archive: func(t *testing.T) []byte {
			return gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeReg, Size: 1}})
		}},
		{name: "malformed", archive: func(*testing.T) []byte { return []byte("not a tar") }},
		{name: "empty bytes", archive: func(*testing.T) []byte { return nil }},
		{name: "oversized", archive: func(*testing.T) []byte { return make([]byte, defaultOutputLimit+512) }},
		{name: "root header only", archive: func(t *testing.T) []byte {
			value := gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}})
			return append([]byte(nil), value[:512]...)
		}},
		{name: "one trailer block only", archive: func(t *testing.T) []byte {
			value := gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}})
			return append([]byte(nil), value[:1024]...)
		}},
		{name: "non-block-aligned trailer", archive: func(t *testing.T) []byte {
			value := gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}})
			return append(append([]byte(nil), value...), 0)
		}},
		{name: "trailing nonzero", archive: func(t *testing.T) []byte {
			value := gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}})
			return append(value, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := gatewayRebindExactEmptyConfigVolumeArchive(test.archive(t))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t error=%v", test.valid, err)
			}
		})
	}
}

func TestGatewayRebindStageConfigIntentAppendsAndFreshManagerReplays(t *testing.T) {
	fixture, fake, reads, records := installGatewayRebindStageConfigContainerProgress(t)
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 6 {
		t.Fatalf("base history=%d error=%v", len(history.Progress), err)
	}
	baseBytes := make([][]byte, len(history.Progress))
	for index, entry := range history.Progress {
		baseBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := false
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7),
		func() { checkpoint = true }); err != nil {
		t.Fatalf("prepare stage config intent: %v", err)
	}
	if !checkpoint || fake.inventoryCalls != 6 || fake.createCalls != 0 {
		t.Fatalf("checkpoint=%t inventory=%d creates=%d", checkpoint, fake.inventoryCalls, fake.createCalls)
	}
	history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 7 {
		t.Fatalf("sequence-seven history=%d error=%v", len(history.Progress), err)
	}
	for index := range baseBytes {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !bytes.Equal(body, baseBytes[index]) ||
			history.Progress[index].Record.Digest != records[index].Digest {
			t.Fatalf("sequence %d changed: read=%v", index+1, readErr)
		}
	}
	expected, err := gatewayRebindStageConfigIntentBindingFor(fixture.intent, records[5])
	actual := history.Progress[6].Record
	if err != nil || actual.Phase != gatewayRebindProgressStageConfigIntent || actual.Stage == nil ||
		actual.Stage.StageConfigIntent == nil || *actual.Stage.StageConfigIntent != expected {
		t.Fatalf("stage config binding=%#v expected=%#v error=%v", actual, expected, err)
	}
	allBytes := make([][]byte, len(history.Progress))
	for index, entry := range history.Progress {
		allBytes[index], err = os.ReadFile(entry.Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	after, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("SQLite changed: equal=%t error=%v", reflect.DeepEqual(before, after), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(context.Background(),
		fixture.predecessor.repository, gatewayRebindStageContainerReads(fixture, fake.gatewayRebindStageContainerFake),
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil); err != nil {
		t.Fatalf("fresh Manager replay: %v", err)
	}
	if fake.inventoryCalls != 8 || fake.createCalls != 0 {
		t.Fatalf("replay inventory=%d creates=%d", fake.inventoryCalls, fake.createCalls)
	}
	replayed, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(replayed.Progress) != len(history.Progress) {
		t.Fatalf("replayed history=%d error=%v", len(replayed.Progress), err)
	}
	for index, entry := range replayed.Progress {
		body, readErr := os.ReadFile(entry.Store.path)
		if readErr != nil || !bytes.Equal(body, allBytes[index]) ||
			!reflect.DeepEqual(entry.Record, history.Progress[index].Record) {
			t.Fatalf("fresh replay changed sequence %d bytes: %v", index+1, readErr)
		}
	}
	if err := restarted.stageGatewayRebindSuccessorContainerWithDriver(context.Background(),
		fixture.predecessor.repository, gatewayRebindStageContainerReads(fixture, fake.gatewayRebindStageContainerFake),
		gatewayRebindStageNetworkInspect(t, fixture), fake.gatewayRebindStageContainerFake,
		gatewayRebindProgressTimestamp(8), nil); err == nil {
		t.Fatal("sequence-six writer accepted sequence-seven history")
	}
	if fake.createCalls != 0 {
		t.Fatalf("older writer created after sequence seven: %d", fake.createCalls)
	}
}

func TestGatewayRebindStageConfigIntentFailsClosedOnInventoryAndDrift(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*gatewayRebindStageConfigIntentFake)
		checkpoint func(*gatewayRebindStageConfigIntentFake) func()
	}{
		{name: "config volume is not empty", configure: func(fake *gatewayRebindStageConfigIntentFake) { fake.empty = false }},
		{name: "inventory error", configure: func(fake *gatewayRebindStageConfigIntentFake) {
			fake.inventoryErr = errors.New("inventory unavailable")
		}},
		{name: "container starts at checkpoint", checkpoint: func(fake *gatewayRebindStageConfigIntentFake) func() {
			return func() { fake.running = true }
		}},
		{name: "foreign config appears at checkpoint", checkpoint: func(fake *gatewayRebindStageConfigIntentFake) func() {
			return func() { fake.empty = false }
		}},
		{name: "extra owned container at checkpoint", checkpoint: func(fake *gatewayRebindStageConfigIntentFake) func() {
			return func() { fake.extraOwned = true }
		}},
		{name: "pinned image drifts at checkpoint", checkpoint: func(fake *gatewayRebindStageConfigIntentFake) func() {
			return func() { fake.imageDrift = true }
		}},
		{name: "stopped container identity drifts at checkpoint", checkpoint: func(fake *gatewayRebindStageConfigIntentFake) func() {
			return func() { fake.containerID = strings.Repeat("f", 64) }
		}},
		{name: "configured network drifts at checkpoint", checkpoint: func(fake *gatewayRebindStageConfigIntentFake) func() {
			return func() { fake.wrongNetwork = true }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindStageConfigContainerProgress(t)
			if test.configure != nil {
				test.configure(fake)
			}
			var checkpoint func()
			if test.checkpoint != nil {
				checkpoint = test.checkpoint(fake)
			}
			err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
				context.Background(), fixture.predecessor.repository, reads,
				gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), checkpoint)
			if err == nil {
				t.Fatal("stage config intent unexpectedly succeeded")
			}
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 6 || fake.createCalls != 0 {
				t.Fatalf("rejection mutated state: progress=%d creates=%d scan=%v",
					len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func TestGatewayRebindStageConfigIntentRejectsHostAndPredecessorDrift(t *testing.T) {
	t.Run("host topology at checkpoint", func(t *testing.T) {
		fixture, fake, reads, _ := installGatewayRebindStageConfigContainerProgress(t)
		original := reads.network.host
		drift := false
		reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
			value, err := original()
			if drift {
				value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("10.99.0.0/16"))
			}
			return value, err
		}
		err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7),
			func() { drift = true })
		assertGatewayRebindStageConfigIntentRejected(t, fixture, fake, err)
	})

	t.Run("predecessor Docker", func(t *testing.T) {
		fixture, fake, reads, _ := installGatewayRebindStageConfigContainerProgress(t)
		inspect := func(context.Context, routeState, gatewayV2RouteState,
			gatewayMigrationJournal,
		) (gatewayV2DockerObservation, error) {
			observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
			observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
			return observation, nil
		}
		err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
			context.Background(), fixture.predecessor.repository, reads, inspect, fake,
			gatewayRebindProgressTimestamp(7), nil)
		assertGatewayRebindStageConfigIntentRejected(t, fixture, fake, err)
	})

	t.Run("prepared claim", func(t *testing.T) {
		fixture, fake, reads, _ := installGatewayRebindStageConfigContainerProgress(t)
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`,
			strings.Repeat("9", 64)); err != nil {
			t.Fatal(err)
		}
		err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
			context.Background(), fixture.predecessor.repository, reads,
			gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), nil)
		assertGatewayRebindStageConfigIntentRejected(t, fixture, fake, err)
	})
}

func TestGatewayRebindStageConfigIntentRejectsNonmonotonicTimeAndReplayDrift(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindStageConfigContainerProgress(t)
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(6), nil); err == nil {
		t.Fatal("nonmonotonic sequence-seven time accepted")
	}
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads,
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(7), nil); err != nil {
		t.Fatalf("install valid sequence seven: %v", err)
	}
	fake.empty = false
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(context.Background(),
		fixture.predecessor.repository, gatewayRebindStageContainerReads(fixture, fake.gatewayRebindStageContainerFake),
		gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(8), nil); err == nil {
		t.Fatal("fresh Manager replay accepted nonempty config volume")
	}
	history, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 7 || fake.createCalls != 0 {
		t.Fatalf("replay drift changed state: progress=%d creates=%d error=%v", len(history.Progress), fake.createCalls, err)
	}
}

func TestGatewayRebindStageConfigIntentFreshReplayRejectsHighRiskDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *gatewayRebindEffectBoundaryFixture, *gatewayRebindStageConfigIntentFake,
			*gatewayRebindSuccessorPreflightReads, *gatewayRebindDockerInspector)
	}{
		{name: "prepared claim", mutate: func(t *testing.T, fixture *gatewayRebindEffectBoundaryFixture,
			_ *gatewayRebindStageConfigIntentFake, _ *gatewayRebindSuccessorPreflightReads, _ *gatewayRebindDockerInspector,
		) {
			if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER lan_gateway_rebind_claim_immutable_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.predecessor.db.Exec(`UPDATE lan_gateway_rebind_claims SET request_digest=?`, strings.Repeat("9", 64)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "predecessor Docker", mutate: func(t *testing.T, fixture *gatewayRebindEffectBoundaryFixture,
			_ *gatewayRebindStageConfigIntentFake, _ *gatewayRebindSuccessorPreflightReads, inspect *gatewayRebindDockerInspector,
		) {
			*inspect = func(context.Context, routeState, gatewayV2RouteState,
				gatewayMigrationJournal,
			) (gatewayV2DockerObservation, error) {
				observation := gatewayRebindFixtureDockerObservation(t, fixture.predecessor)
				observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
				return observation, nil
			}
		}},
		{name: "pinned image", mutate: func(_ *testing.T, _ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigIntentFake, _ *gatewayRebindSuccessorPreflightReads, _ *gatewayRebindDockerInspector,
		) {
			fake.imageDrift = true
		}},
		{name: "host topology", mutate: func(_ *testing.T, _ *gatewayRebindEffectBoundaryFixture,
			_ *gatewayRebindStageConfigIntentFake, reads *gatewayRebindSuccessorPreflightReads, _ *gatewayRebindDockerInspector,
		) {
			original := reads.network.host
			reads.network.host = func() (gatewayV2HostNetworkSnapshot, error) {
				value, err := original()
				value.Interfaces = append(value.Interfaces, netip.MustParsePrefix("10.99.0.0/16"))
				return value, err
			}
		}},
		{name: "stopped container ID", mutate: func(_ *testing.T, _ *gatewayRebindEffectBoundaryFixture,
			fake *gatewayRebindStageConfigIntentFake, _ *gatewayRebindSuccessorPreflightReads, _ *gatewayRebindDockerInspector,
		) {
			fake.containerID = strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, records := installGatewayRebindStageConfigContainerProgress(t)
			binding, err := gatewayRebindStageConfigIntentBindingFor(fixture.intent, records[5])
			if err != nil {
				t.Fatal(err)
			}
			seventh, err := newGatewayRebindStageConfigIntentProgress(fixture.intent, records[5], binding,
				gatewayRebindProgressTimestamp(7))
			if err != nil {
				t.Fatal(err)
			}
			store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
				fixture.intent.Generation, fixture.intent.OperationID, 7)
			if err != nil || store.installExact(context.Background(), seventh) != nil {
				t.Fatalf("install sequence seven: %v", err)
			}
			inspect := gatewayRebindStageNetworkInspect(t, fixture)
			test.mutate(t, &fixture, fake, &reads, &inspect)
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			err = restarted.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(context.Background(),
				fixture.predecessor.repository, reads, inspect, fake, gatewayRebindProgressTimestamp(8), nil)
			if err == nil {
				t.Fatal("fresh Manager replay accepted drift")
			}
			history, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 7 || fake.createCalls != 0 {
				t.Fatalf("replay drift changed state: progress=%d creates=%d scan=%v",
					len(history.Progress), fake.createCalls, scanErr)
			}
		})
	}
}

func installGatewayRebindStageConfigContainerProgress(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindStageConfigIntentFake, gatewayRebindSuccessorPreflightReads, []gatewayRebindProgressRecord,
) {
	t.Helper()
	fixture := newGatewayRebindEffectBoundaryFixture(t)
	records := installGatewayRebindStageContainerDataProgress(t, fixture)
	container, reads := gatewayRebindStageContainerTestDriver(fixture, *records[4].Stage)
	container.found = true
	ownership, err := gatewayRebindStageContainerOwnershipDigest(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(fixture.intent, *records[4].Stage)
	if err != nil {
		t.Fatal(err)
	}
	sixth, err := newGatewayRebindStageContainerProgress(fixture.intent, records[4], gatewayRebindStageContainerBinding{
		ID: container.containerID, OwnershipDigest: ownership, ConfigurationDigest: configuration,
	}, gatewayRebindProgressTimestamp(6))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 6)
	if err != nil || store.installExact(context.Background(), sixth) != nil {
		t.Fatalf("install container progress: %v", err)
	}
	container.stage = *sixth.Stage
	fake := &gatewayRebindStageConfigIntentFake{gatewayRebindStageContainerFake: container, empty: true}
	return fixture, fake, reads, append(records, sixth)
}

func assertGatewayRebindStageConfigIntentRejected(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	fake *gatewayRebindStageConfigIntentFake, err error,
) {
	t.Helper()
	if err == nil {
		t.Fatal("stage config intent unexpectedly succeeded")
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 6 || fake.createCalls != 0 {
		t.Fatalf("rejection mutated state: progress=%d creates=%d scan=%v",
			len(history.Progress), fake.createCalls, scanErr)
	}
}

func gatewayRebindTestTar(t *testing.T, headers []tar.Header) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for index := range headers {
		header := headers[index]
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg && header.Size > 0 {
			if _, err := writer.Write(bytes.Repeat([]byte{'x'}, int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type gatewayRebindStageConfigIntentRunner struct {
	requests []runtimeprocess.CommandRequest
	result   runtimeprocess.CommandResult
	err      error
}

func (r *gatewayRebindStageConfigIntentRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	result := r.result
	result.Stdout = append([]byte(nil), r.result.Stdout...)
	result.Stderr = append([]byte(nil), r.result.Stderr...)
	return result, r.err
}

func TestGatewayRebindStageConfigIntentArchiveCommandUsesPinnedContainerID(t *testing.T) {
	_, intent, records := gatewayRebindProgressThroughContainer(t)
	validArchive := gatewayRebindTestTar(t, []tar.Header{{Name: ".", Typeflag: tar.TypeDir}})
	for _, test := range []struct {
		name   string
		result runtimeprocess.CommandResult
		err    error
		valid  bool
	}{
		{name: "exact empty archive", result: runtimeprocess.CommandResult{Stdout: validArchive}, valid: true},
		{name: "truncated output", result: runtimeprocess.CommandResult{Stdout: validArchive, StdoutTruncated: true}},
		{name: "successful command stderr", result: runtimeprocess.CommandResult{Stdout: validArchive, Stderr: []byte("warning")}},
		{name: "Docker error", err: errors.New("Docker unavailable")},
		{name: "foreign file", result: runtimeprocess.CommandResult{Stdout: gatewayRebindTestTar(t,
			[]tar.Header{{Name: ".", Typeflag: tar.TypeDir}, {Name: "stage.json", Typeflag: tar.TypeReg, Size: 2}})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &gatewayRebindStageConfigIntentRunner{result: test.result, err: test.err}
			manager := &Manager{runner: runner, options: Options{
				DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Second,
				OutputLimit: defaultOutputLimit,
			}}
			driver := managerGatewayRebindStageConfigIntentDriver{manager: manager}
			empty, err := driver.configVolumeEmpty(context.Background(), intent, *records[5].Stage)
			if (err == nil && empty) != test.valid {
				t.Fatalf("empty=%t valid=%t error=%v", empty, test.valid, err)
			}
			if len(runner.requests) != 1 || !reflect.DeepEqual(runner.requests[0].Args, []string{
				"container", "cp", records[5].Stage.StageContainer.ID + ":/config/.", "-",
			}) {
				t.Fatalf("archive command=%#v", runner.requests)
			}
		})
	}
	var _ gatewayRebindStageContainerAttestor = managerGatewayRebindStageConfigIntentDriver{}
}
