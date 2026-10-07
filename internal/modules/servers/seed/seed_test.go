package seed_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestSeedIsCreatedOnce(t *testing.T) {
	ctx := context.Background()
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	q := s.App.DB.R

	list, err := store.ListTemplates(ctx, q)
	if err != nil || len(list) != 1 {
		t.Fatalf("first start: %d templates, %v", len(list), err)
	}
	tpl := list[0]
	if tpl.Slug != "vless-xhttp" || tpl.Name != "VLESS XHTTP behind nginx" || tpl.DefaultVersion != 1 {
		t.Fatalf("template: %+v", tpl)
	}
	vs, _ := store.ListVersions(ctx, q, tpl.ID)
	if len(vs) != 1 || vs[0].Number != 1 || vs[0].Notes != "Converted from servers-templates/proxy" || vs[0].PublishedBy != "system" {
		t.Fatalf("versions: %+v", vs)
	}
	files, _ := store.VersionFiles(ctx, q, vs[0].ID)
	if len(files) != len(seed.Files()) || string(files[".env"]) != string(seed.Files()[".env"]) {
		t.Errorf("the seed's files were not all stored: %d of %d", len(files), len(seed.Files()))
	}
	if vs[0].Warnings == "[]" {
		t.Error("the two :latest warnings must be stored with the version")
	}
	// recorded by the system
	for _, typ := range []string{"template.created", "template.version_published", "template.default_version_changed"} {
		l, _ := events.List(ctx, q, events.Filter{Type: typ})
		if len(l) != 1 || l[0].Actor != "system" {
			t.Errorf("%s: %+v", typ, l)
		}
	}

	// a restart (Migrate runs on every start) doesn't duplicate it
	if err := s.App.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ = store.ListTemplates(ctx, q); len(list) != 1 {
		t.Fatalf("after a restart: %d templates", len(list))
	}

	// deleting it doesn't bring it back
	if _, err := s.App.DB.W.ExecContext(ctx, `DELETE FROM servers_templates`); err != nil {
		t.Fatal(err)
	}
	if err := s.App.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ = store.ListTemplates(ctx, q); len(list) != 0 {
		t.Fatalf("after deleting the seed and restarting: %d templates", len(list))
	}
}

func TestSeedIsNotCreatedOverExistingTemplates(t *testing.T) {
	// An instance that already has a template of its own (no marker yet) gets
	// no seed: the seed is for a first start.
	ctx := context.Background()
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	if _, err := s.App.DB.W.ExecContext(ctx, `DELETE FROM servers_meta; DELETE FROM servers_templates;
		INSERT INTO servers_templates (slug, name, created_at) VALUES ('mine', 'Mine', '2026-10-07T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := s.App.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := store.ListTemplates(ctx, s.App.DB.R)
	if len(list) != 1 || list[0].Slug != "mine" {
		t.Errorf("templates: %+v", list)
	}
}
