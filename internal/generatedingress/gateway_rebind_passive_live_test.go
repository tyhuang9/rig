package generatedingress

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveGatewayRebindCountFile = "/tmp/rig-gateway-rebind-request-count.json"

type liveGatewayRebindRequestCount struct {
	Total  uint64 `json:"total"`
	Routed uint64 `json:"routed"`
}

// TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests proves that
// static predecessor inspection does not forward requests to a hosted app.
// This observer-level gate does not attest a public entrypoint. The shared
// fixture enforces a disposable default local Linux Docker host.
func TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "71717171-7171-4171-8171-717171717171",
		planID:           "72727272-7272-4272-8272-727272727272",
		operationID:      "73737373-7373-4373-8373-737373737373",
		profileRevision:  "74747474-7474-4474-8474-747474747474",
		approvedBy:       "75757575-7575-4575-8575-757575757575",
		imageTag:         "rig-generated-gateway-v2-live:passive-rebind",
		applicationReply: "gateway-v2-passive-rebind",
		countRequests:    true,
	})
	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request,
		liveGatewayV2Authorizer(t, fixture.request))
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		liveGatewayV2LogOperationDiagnostic(t, fixture)
		failLiveIngress(t, "commit counted gateway-v2 upgrade", err)
	}
	state, journal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if journal.Phase != gatewayPhaseCommitted || !gatewayV2RequestMatchesState(fixture.request, state, journal) {
		t.Fatal("counted gateway-v2 upgrade was not durably committed")
	}
	appID := fixture.candidates[0].ContainerID
	baseline := liveGatewayRebindReadRequestCount(t, fixture, appID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, appID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted app route requests did not settle after the committed upgrade")
	}

	for attempt := 1; attempt <= 2; attempt++ {
		observation, inspectErr := fixture.ingress.inspectGatewayRebindDocker(fixture.ctx, fixture.source, state, journal)
		staticValid := inspectErr == nil && validGatewayRebindPredecessorDocker(fixture.source, state, journal, observation)
		proofsAbsent := !observation.Stage404Proven && !observation.StageHostPublicationProven &&
			!observation.Final404Proven && !observation.FinalRoutesProven && !observation.FinalHostPublicationProven
		clearGatewayV2DockerObservation(&observation)
		if !staticValid || !proofsAbsent {
			t.Fatalf("passive Docker inspection %d did not return static predecessor proof without serving proof flags", attempt)
		}
		got := liveGatewayRebindReadRequestCount(t, fixture, appID)
		if got.Routed != baseline.Routed {
			t.Fatalf("passive Docker inspection %d sent a routed application request", attempt)
		}
	}

	if !fixture.ingress.proveGatewayV2FinalRoutes(fixture.ctx, state, journal.Resources.FinalContainerID) {
		t.Fatal("serving route proof failed as the counter positive control")
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, appID); got.Routed <= baseline.Routed {
		t.Fatal("serving route proof did not increase the app request counter")
	}
}

func buildLiveGatewayCountedImage(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner,
	docker, root, dockerConfig, tag string, spec generatedruntime.CandidateSpec, reply string,
) string {
	t.Helper()
	server := fmt.Sprintf(`import { renameSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
const port = Number(process.env.RIG_RUNTIME_INTERNAL_PORT);
const reply = %q;
const countFile = %q;
let total = 0;
let routed = 0;
function persistCount() {
  const next = countFile + '.next';
  writeFileSync(next, JSON.stringify({total, routed}));
  renameSync(next, countFile);
}
function record(request) {
  total++;
  const localHealth = request.url === '/health' &&
    ['127.0.0.1', '::ffff:127.0.0.1'].includes(request.socket.remoteAddress);
  if (!localHealth) routed++;
  persistCount();
}
persistCount();
createServer((request, response) => {
  record(request);
  response.writeHead(200, {'content-type': 'text/plain'});
  response.end(reply);
}).listen(port, '0.0.0.0');
`, reply, liveGatewayRebindCountFile)
	return buildLiveGatewayImageWithServer(t, ctx, runner, docker, root, dockerConfig, tag, spec, reply, server)
}

func liveGatewayRebindReadRequestCount(t *testing.T, fixture *liveGatewayV2Fixture, containerID string) liveGatewayRebindRequestCount {
	t.Helper()
	if !validContainerID(containerID) {
		t.Fatal("counted application container has invalid identity")
	}
	result, err := runLiveDocker(fixture.ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "container", "exec", containerID, "cat", liveGatewayRebindCountFile)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("read counted application request file through Docker")
	}
	var count liveGatewayRebindRequestCount
	err = json.Unmarshal(result.Stdout, &count)
	clearLiveResult(&result)
	if err != nil || count.Total < count.Routed {
		t.Fatal("counted application request file is invalid")
	}
	return count
}
