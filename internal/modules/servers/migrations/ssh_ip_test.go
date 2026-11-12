package migrations_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

func TestSeparateSSHIPMigrationPreservesExistingServers(t *testing.T) {
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, dbtest.Platform, upTo{10}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO servers_templates (id, slug, name, created_at) VALUES (1, 't', 'T', ?)`, now)
	mustExec(t, d, insertServer, 1, "nl-1", "203.0.113.10", "provisioning", nil, nil, now)
	if err := db.MigrateUp(ctx, d, dbtest.Discard, source{}); err != nil {
		t.Fatal(err)
	}
	srv, err := store.GetServer(ctx, d.R, 1)
	if err != nil || srv.SSHIP != "" || srv.ConnectIP() != "203.0.113.10" {
		t.Fatalf("existing server after migration: %+v %v", srv, err)
	}
}
