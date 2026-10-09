package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestGatewayRebindStageConfigurationDigestPreservesFinalProgress(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	manager := fixture.predecessor.manager
	if err := manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		fake, gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatal(err)
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 {
		t.Fatalf("expected complete final-copy history: count=%d error=%v", len(history.Progress), err)
	}
	initialDigest, err := gatewayRebindStageContainerConfigurationDigest(fixture.intent, *history.Progress[4].Record.Stage)
	if err != nil || !validSHA256(initialDigest) {
		t.Fatalf("original pre-create configuration rejected: %v", err)
	}
	observed, err := fake.inspect(context.Background(), fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []int{10, 11, 12} {
		t.Run(fmt.Sprintf("sequence_%d", sequence), func(t *testing.T) {
			entry := history.Progress[sequence-1]
			stage := *entry.Record.Stage
			if !validGatewayRebindStageIntent(stage) || stage.StageContainer == nil {
				t.Fatal("fixture omitted valid retained stage authority")
			}
			before, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := os.ReadFile(entry.Store.path)
			if err != nil {
				t.Fatal(err)
			}
			digest, digestErr := gatewayRebindStageContainerConfigurationDigest(fixture.intent, stage)
			if digestErr != nil || digest != initialDigest || digest != stage.StageContainer.ConfigurationDigest {
				t.Errorf("retained create configuration rejected at sequence %d: error=%v digest_matches=%t",
					sequence, digestErr, digest == stage.StageContainer.ConfigurationDigest)
			}
			if got := classifyGatewayRebindStageRuntime(fixture.intent, stage, observed); got != gatewayRebindStageRuntimeServing {
				t.Errorf("exact serving stage rejected at sequence %d: class=%d", sequence, got)
			}
			after, err := json.Marshal(stage)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("configuration projection mutated retained input")
			}
			afterFile, err := os.ReadFile(entry.Store.path)
			if err != nil || !bytes.Equal(retained, afterFile) {
				t.Fatal("configuration projection changed protected progress history")
			}
		})
	}
	stage := *history.Progress[11].Record.Stage
	t.Run("changed_create_configuration", func(t *testing.T) {
		changed := stage
		changed.ObservedDockerImageID = strings.Repeat("e", 64)
		digest, err := gatewayRebindStageContainerConfigurationDigest(fixture.intent, changed)
		if err != nil || digest == stage.StageContainer.ConfigurationDigest {
			t.Fatalf("changed immutable image did not change configuration digest: %v", err)
		}
		if classifyGatewayRebindStageRuntime(fixture.intent, changed, observed) != gatewayRebindStageRuntimeInvalid {
			t.Fatal("changed immutable image accepted as original stage")
		}
	})
	t.Run("replaced_binding", func(t *testing.T) {
		changed := stage
		binding := *stage.StageContainer
		binding.ConfigurationDigest = strings.Repeat("f", 64)
		changed.StageContainer = &binding
		if classifyGatewayRebindStageRuntime(fixture.intent, changed, observed) != gatewayRebindStageRuntimeInvalid {
			t.Fatal("replacement configuration binding accepted")
		}
	})
}
