package database

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const lanGatewayRebindReconcileAuditMigration = "036_lan_gateway_rebind_reconcile_audit.sql"

func TestLANGatewayRebindReconcileAuditMigrationIsMirroredAndResourceScoped(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + lanGatewayRebindReconcileAuditMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations",
		lanGatewayRebindReconcileAuditMigration))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, public) {
		t.Fatal("public and embedded migration 036 differ")
	}

	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "036_")); err != nil {
		t.Fatalf("migrate through 035: %v", err)
	}
	var auditTableBefore string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='audit_events'`).Scan(&auditTableBefore); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		action, resourceType, resourceID string
	}{
		{action: "reconcile_lan_gateway", resourceType: "lan_gateway_rebind", resourceID: "rebind-a"},
		{action: "future_rebind_action", resourceType: "lan_gateway_rebind", resourceID: "rebind-b"},
		{action: "ordinary_action", resourceType: "application", resourceID: "application-a"},
		{action: "upgrade_generated_ingress", resourceType: "lan_gateway_upgrade", resourceID: "upgrade-a"},
	} {
		if _, err := db.Exec(`INSERT INTO audit_events(
			action,resource_type,resource_id,metadata_json,created_at
		) VALUES(?,?,?,'{}','2026-10-12T12:00:00.000000000Z')`,
			row.action, row.resourceType, row.resourceID); err != nil {
			t.Fatal(err)
		}
	}

	only036 := fstest.MapFS{"migrations/" + lanGatewayRebindReconcileAuditMigration: {Data: embedded}}
	for pass := 1; pass <= 2; pass++ {
		if err := migrateFS(db, only036); err != nil {
			t.Fatalf("apply 036 pass %d: %v", pass, err)
		}
	}
	var auditTableAfter string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='audit_events'`).Scan(&auditTableAfter); err != nil {
		t.Fatal(err)
	}
	if auditTableAfter != auditTableBefore {
		t.Fatal("migration 036 rebuilt or changed the audit table")
	}

	for _, resourceID := range []string{"rebind-a", "rebind-b"} {
		assertSQLRejectedWith(t, db, "update rebind audit", "rebind audit events are immutable",
			`UPDATE audit_events SET metadata_json='{"changed":true}' WHERE resource_id=?`, resourceID)
		assertSQLRejectedWith(t, db, "delete rebind audit", "rebind audit events are retained",
			`DELETE FROM audit_events WHERE resource_id=?`, resourceID)
	}
	assertSQLRejectedWith(t, db, "preserved upgrade audit update", "upgrade audit events are immutable",
		`UPDATE audit_events SET metadata_json='{"changed":true}' WHERE resource_id='upgrade-a'`)
	assertSQLRejectedWith(t, db, "preserved upgrade audit delete", "upgrade audit events are retained",
		`DELETE FROM audit_events WHERE resource_id='upgrade-a'`)

	if _, err := db.Exec(`UPDATE audit_events SET metadata_json='{"changed":true}'
		WHERE resource_id='application-a'`); err != nil {
		t.Fatalf("unrelated audit update: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE resource_id='application-a'`); err != nil {
		t.Fatalf("unrelated audit delete: %v", err)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE resource_type='lan_gateway_rebind'`).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("retained rebind audits=%d error=%v", retained, err)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
		lanGatewayRebindReconcileAuditMigration).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration 036 applications=%d error=%v", applied, err)
	}
}
