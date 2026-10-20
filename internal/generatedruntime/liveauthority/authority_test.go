//go:build live_docker

package liveauthority

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestAuthorityBindsConcreteScopedNetworkExecution(t *testing.T) {
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority := New(t, db, directories.WorkingDirectory)
	first := generatedruntime.CandidateSpec{
		AppID: uuid.NewString(), ReleaseID: uuid.NewString(), DeploymentID: uuid.NewString(), ArtifactID: uuid.NewString(),
		DeploymentPlanRevisionID: uuid.NewString(), ComponentName: "web", Role: generatedruntime.RoleServer,
		ImageContentID: "sha256:" + strings.Repeat("6", 64), BuildDefinitionDigest: strings.Repeat("7", 64),
	}
	boundContext, first := authority.BindCandidate(t, context.Background(), first)
	execution, err := authority.Repository().PinGeneratedNetwork(boundContext, first.Network)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckBeforeCreate(); err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckObservation(); err != nil {
		t.Fatal(err)
	}
	execution.Leave()
	authority.FinishCandidate(t, first.DeploymentID)

	second := first
	second.DeploymentID, second.ArtifactID = uuid.NewString(), uuid.NewString()
	second.ActiveSlot = generatedruntime.SlotBlue
	_, second = authority.BindCandidate(t, context.Background(), second)
	if second.Network.Claim.JobID() == first.Network.Claim.JobID() || second.Network.DeploymentID != second.DeploymentID {
		t.Fatal("replacement did not receive its own exact immutable deployment claim")
	}
	authority.RejectCandidate(t, second.DeploymentID, first.DeploymentID)
}

func TestAuthorityBindsAndFinishesExplicitAggregateHistory(t *testing.T) {
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority := New(t, db, directories.WorkingDirectory)
	spec := generatedruntime.CandidateSpec{
		AppID: uuid.NewString(), ReleaseID: uuid.NewString(), DeploymentID: uuid.NewString(), ArtifactID: uuid.NewString(),
		DeploymentPlanRevisionID: uuid.NewString(), ComponentName: "api", Role: generatedruntime.RoleServer,
		ImageContentID: "sha256:" + strings.Repeat("6", 64), BuildDefinitionDigest: strings.Repeat("7", 64),
	}
	bundleRef := "preexisting:" + spec.DeploymentPlanRevisionID
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, spec.AppID, spec.AppID, spec.AppID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,1,?,'generated_node','preexisting-count-two','1',?,'local',0,?,?,2,0,'',?,datetime('now'),'accepted',?,datetime('now'))`, spec.DeploymentPlanRevisionID, spec.AppID, bundleRef, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), authority.actorID, authority.actorID); err != nil {
		t.Fatal(err)
	}
	_, spec = authority.BindCandidateAggregate(t, context.Background(), spec, []string{"frontend", "api"})
	var componentCount, pendingCount int
	var detector, retainedBundleRef string
	if err := db.QueryRow(`SELECT component_count,detector,bundle_ref FROM deployment_plan_revisions WHERE id=?`, spec.DeploymentPlanRevisionID).Scan(&componentCount, &detector, &retainedBundleRef); err != nil || componentCount != 2 || detector != "preexisting-count-two" || retainedBundleRef != bundleRef {
		t.Fatalf("pre-existing accepted plan changed: count=%d detector=%q bundle=%q error=%v", componentCount, detector, retainedBundleRef, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='pending' AND component_name IN ('api','frontend')`, spec.DeploymentID).Scan(&pendingCount); err != nil || pendingCount != 2 {
		t.Fatalf("persist exact pending direct-live aggregate: count=%d error=%v", pendingCount, err)
	}
	authority.FinishCandidateWithSyntheticUnstartedComponents(t, spec.DeploymentID, []string{"frontend"})
	var activeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='active'`, spec.DeploymentID).Scan(&activeCount); err != nil || activeCount != 2 {
		t.Fatalf("finish exact direct-live aggregate: active=%d error=%v", activeCount, err)
	}
	replacement := spec
	replacement.DeploymentID, replacement.ArtifactID = uuid.NewString(), uuid.NewString()
	replacement.ActiveSlot = generatedruntime.SlotBlue
	_, replacement = authority.BindCandidateAggregate(t, context.Background(), replacement, []string{"api", "frontend"})
	var previousDeploymentID, previousSlot string
	if err := db.QueryRow(`SELECT previous_active_deployment_id,previous_active_slot FROM generated_runtime_deployments WHERE deployment_id=?`, replacement.DeploymentID).Scan(&previousDeploymentID, &previousSlot); err != nil || previousDeploymentID != spec.DeploymentID || previousSlot != "blue" {
		t.Fatalf("replacement did not retain exact aggregate predecessor: deployment=%q slot=%q error=%v", previousDeploymentID, previousSlot, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='pending' AND component_name IN ('api','frontend')`, replacement.DeploymentID).Scan(&pendingCount); err != nil || pendingCount != 2 {
		t.Fatalf("persist replacement pending aggregate: count=%d error=%v", pendingCount, err)
	}
	authority.RejectCandidate(t, replacement.DeploymentID, spec.DeploymentID)
}

func TestCanonicalCandidateComponentsRequiresExactCandidateMembership(t *testing.T) {
	spec := generatedruntime.CandidateSpec{ComponentName: "api"}
	components, ok := canonicalCandidateComponents(spec, []string{"frontend", "api"})
	if !ok || !reflect.DeepEqual(components, []string{"api", "frontend"}) {
		t.Fatal("exact aggregate membership was not canonicalized")
	}
	for _, invalid := range [][]string{{"frontend"}, {"api", "api"}, {"", "api"}, {" api", "frontend"}, {"api\n", "frontend"}, nil} {
		if _, ok := canonicalCandidateComponents(spec, invalid); ok {
			t.Fatalf("invalid aggregate membership was accepted: %#v", invalid)
		}
	}
}
