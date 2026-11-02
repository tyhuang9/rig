//go:build linux || windows

package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGeneratedRouteActivationCreatorMinimumHelperExactRoster(t *testing.T) {
	// This is an isolated minimal-schema projection of the private reader contract.
	// Authentic migrated 041 provenance is exercised by the cold-runtime chain.
	const deploymentID = "20000000-0000-4000-8000-000000000047"
	receipts := map[string]string{
		"api":      "21000000-0000-4000-8000-000000000047",
		"frontend": "22000000-0000-4000-8000-000000000047",
		"worker":   "23000000-0000-4000-8000-000000000047",
	}
	artifacts := map[string]string{
		"api":      "31000000-0000-4000-8000-000000000047",
		"frontend": "32000000-0000-4000-8000-000000000047",
		"worker":   "33000000-0000-4000-8000-000000000047",
	}
	containerIDs := map[string]string{"api": strings.Repeat("1", 64),
		"frontend": strings.Repeat("2", 64), "worker": strings.Repeat("3", 64)}
	attempts := map[string]int{"api": 6, "frontend": 1, "worker": 3}
	migration := newGeneratedMigrationFixture(t)

	newControl := func(t *testing.T, foreignJob bool) (*sql.DB, []generatedruntimestate.RouteActivationComponent) {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "creator-helper.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		for _, statement := range []string{
			`CREATE TABLE generated_runtime_components(
				deployment_id TEXT NOT NULL,
				component_name TEXT NOT NULL,
				PRIMARY KEY(deployment_id,component_name)
			)`,
			`CREATE TABLE generated_candidate_intents(
				id TEXT NOT NULL PRIMARY KEY,
				deployment_id TEXT NOT NULL,
				component_name TEXT NOT NULL,
				creator_job_id TEXT NOT NULL,
				creator_worker_attempt INTEGER NOT NULL,
				UNIQUE(deployment_id,component_name)
			)`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		roster := make([]generatedruntimestate.RouteActivationComponent, 0, 3)
		for _, component := range []string{"api", "frontend", "worker"} {
			jobID := migration.claim.JobID()
			if foreignJob && component == "worker" {
				jobID = "24000000-0000-4000-8000-000000000047"
			}
			if _, err := db.Exec(`INSERT INTO generated_runtime_components(
				deployment_id,component_name) VALUES(?,?)`, deploymentID, component); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO generated_candidate_intents(
				id,deployment_id,component_name,creator_job_id,creator_worker_attempt
			) VALUES(?,?,?,?,?)`, receipts[component], deploymentID, component, jobID, attempts[component]); err != nil {
				t.Fatal(err)
			}
			roster = append(roster, generatedruntimestate.RouteActivationComponent{
				DeploymentID: deploymentID, ComponentName: component,
				Slot: "blue", ImageArtifactID: artifacts[component],
				ContainerName: "rig-helper-" + component, ContainerID: containerIDs[component],
				CandidateReceiptID: receipts[component], CandidatePolicyDigest: strings.Repeat("a", 64),
			})
		}
		canonical, ok := canonicalGeneratedRouteActivationRoster(roster)
		if !ok || len(canonical) != 3 {
			t.Fatalf("creator helper roster=%#v", roster)
		}
		return db, canonical
	}
	newClaim := func(t *testing.T, attempt int) deploymentexecution.Claim {
		t.Helper()
		claim, err := deploymentexecution.NewClaim(migration.binding, migration.claim.JobID(),
			migration.claim.ApplicationID(), attempt)
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	read := func(t *testing.T, db *sql.DB, roster []generatedruntimestate.RouteActivationComponent,
		claim deploymentexecution.Claim,
	) (string, int, error) {
		t.Helper()
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		jobID, attempt, readErr := readGeneratedRouteActivationCreatorTx(
			context.Background(), tx, deploymentID, roster, claim)
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		return jobID, attempt, readErr
	}

	t.Run("helper projection minimum need not be lexicographically first", func(t *testing.T) {
		db, roster := newControl(t, false)
		jobID, attempt, err := read(t, db, roster, newClaim(t, 8))
		if err != nil || jobID != migration.claim.JobID() || attempt != 1 {
			t.Fatalf("creator=%s/%d error=%v", jobID, attempt, err)
		}
		var components, receipts int
		if err := db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM generated_runtime_components),
			(SELECT COUNT(*) FROM generated_candidate_intents)`).Scan(&components, &receipts); err != nil {
			t.Fatal(err)
		}
		if components != 3 || receipts != 3 {
			t.Fatalf("read-only creator helper changed fixture components=%d receipts=%d", components, receipts)
		}
	})

	t.Run("missing receipt refuses", func(t *testing.T) {
		db, roster := newControl(t, false)
		roster[1].CandidateReceiptID = "25000000-0000-4000-8000-000000000047"
		if _, _, err := read(t, db, roster, newClaim(t, 8)); !errors.Is(err, generatedruntimestate.ErrConflict) {
			t.Fatalf("missing receipt error=%v", err)
		}
	})

	t.Run("foreign creator job refuses", func(t *testing.T) {
		db, roster := newControl(t, true)
		if _, _, err := read(t, db, roster, newClaim(t, 8)); !errors.Is(err, generatedruntimestate.ErrConflict) {
			t.Fatalf("foreign creator error=%v", err)
		}
	})

	t.Run("current attempt before roster creator refuses", func(t *testing.T) {
		db, roster := newControl(t, false)
		if _, _, err := read(t, db, roster, newClaim(t, 5)); !errors.Is(err, generatedruntimestate.ErrConflict) {
			t.Fatalf("early current attempt error=%v", err)
		}
	})
}
