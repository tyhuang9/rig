package apps

import (
	"database/sql"
	"testing"
)

func TestDesiredLifecycleInitializationFollowsApplicationTransaction(t *testing.T) {
	t.Run("successful application gets exactly one uninitialized head", func(t *testing.T) {
		db := openTestDB(t)
		application, err := New(db).Create("Lifecycle App", "", "C:/apps/lifecycle", "")
		if err != nil {
			t.Fatal(err)
		}
		var state, updatedAt sql.NullString
		var revision, count int64
		if err := db.QueryRow(`SELECT desired_state,revision_number,updated_at
			FROM application_desired_lifecycle_heads WHERE app_id=?`, application.ID).
			Scan(&state, &revision, &updatedAt); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_heads
			WHERE app_id=?`, application.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if state.Valid || revision != 0 || updatedAt.Valid || count != 1 {
			t.Fatalf("head state=%#v revision=%d updated=%#v count=%d", state, revision, updatedAt, count)
		}
	})

	t.Run("source failure rolls back application and lifecycle head", func(t *testing.T) {
		db := openTestDB(t)
		source := Source{
			Type:            SourceGitHub,
			ConnectionID:    "0123456789abcdef0123456789abcdef",
			InstallationID:  3,
			RepositoryID:    7,
			RepositoryOwner: "owner",
			RepositoryName:  "repo",
			TrackedBranch:   "main",
			TrackedRef:      "refs/heads/main",
			ComposePath:     "compose.yaml",
			ResolvedSHA:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}
		if _, err := New(db).CreateWithSource("Lifecycle Rollback", "", "", source); err == nil {
			t.Fatal("application with missing source connection succeeded")
		}
		var applications, heads int
		if err := db.QueryRow(`SELECT COUNT(*) FROM applications WHERE slug='lifecycle-rollback'`).Scan(&applications); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_heads h
			JOIN applications a ON a.id=h.app_id WHERE a.slug='lifecycle-rollback'`).Scan(&heads); err != nil {
			t.Fatal(err)
		}
		if applications != 0 || heads != 0 {
			t.Fatalf("rolled-back applications=%d lifecycle heads=%d", applications, heads)
		}
	})
}
