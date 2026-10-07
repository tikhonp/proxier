package templates_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

func TestMakeDefault(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("dflt")
	e.publish(id, "one")
	e.publish(id, "two")
	changed := func() int { return len(e.Recorded("template.default_version_changed")) }
	if changed() != 1 { // the first publish made v1 the default
		t.Fatalf("after two publishes: %d events", changed())
	}
	if err := e.Svc.MakeDefault(ctx, id, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	if changed() != 2 {
		t.Fatalf("making v2 the default: %d events", changed())
	}
	ev := e.Recorded("template.default_version_changed")[0]
	if ev.Payload["from"] != float64(1) || ev.Payload["to"] != float64(2) {
		t.Errorf("payload %v", ev.Payload)
	}
	if err := e.Svc.MakeDefault(ctx, id, 2, "admin"); err != nil || changed() != 2 {
		t.Errorf("the same version again: %v, %d events", err, changed())
	}
	if err := e.Svc.MakeDefault(ctx, id, 9, "admin"); !errors.Is(err, templates.ErrNotFound) {
		t.Errorf("a version that does not exist: %v", err)
	}
	vs, _ := e.Svc.Versions(ctx, id)
	if len(vs) != 2 || vs[0].Number != 2 || !vs[0].Default || vs[1].Default {
		t.Errorf("versions: %+v", vs)
	}
}

func TestDraftFromOldVersionPublishesNext(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("revert")
	e.publish(id, "one")
	e.publish(id, "two")
	v1, _ := e.Svc.Version(ctx, id, 1)

	// a draft exists: the new one replaces it, and its revision moves on
	d, _ := e.Svc.EditDraft(ctx, id, "admin")
	if d.BasedOn != 2 {
		t.Fatalf("the edit draft is based on %d", d.BasedOn)
	}
	rev, err := e.Svc.DraftFromVersion(ctx, id, 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if rev != d.Revision+1 {
		t.Errorf("revision %d after %d", rev, d.Revision)
	}
	got, _ := e.Svc.Draft(ctx, id)
	if got.BasedOn != 1 || got.Source.Kind != "version" || got.Source.Version != 1 {
		t.Errorf("draft: based on %d, source %+v", got.BasedOn, got.Source)
	}
	if string(got.Files["compose.yaml"]) != string(v1.Files["compose.yaml"]) {
		t.Error("the draft does not hold version 1's files")
	}
	if _, err := e.Svc.SaveDraft(ctx, id, d.Revision, got.Files, "admin", "admin"); !errors.Is(err, templates.ErrDraftChanged) {
		t.Errorf("a tab that still has the replaced draft: %v", err)
	}

	n, err := e.Svc.Publish(ctx, id, rev, "back to one", false, "admin")
	if err != nil || n != 3 {
		t.Fatalf("publish: %d %v", n, err)
	}
	if _, err := e.Svc.Draft(ctx, id); !errors.Is(err, templates.ErrNoDraft) {
		t.Errorf("the draft survived publishing: %v", err)
	}
	if _, err := e.Svc.DraftFromVersion(ctx, id, 7, "admin"); !errors.Is(err, templates.ErrNotFound) {
		t.Errorf("a version that does not exist: %v", err)
	}
}

func TestDiscardDraft(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("discard")
	if err := e.Svc.DiscardDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.Draft(ctx, id); !errors.Is(err, templates.ErrNoDraft) {
		t.Errorf("draft after discard: %v", err)
	}
	if len(e.Recorded("template.draft_discarded")) != 1 {
		t.Error("no event")
	}
	if err := e.Svc.DiscardDraft(ctx, id, "admin"); !errors.Is(err, templates.ErrNoDraft) || len(e.Recorded("template.draft_discarded")) != 1 {
		t.Errorf("discarding nothing: %v", err)
	}
}

func TestDeleteTemplateInUse(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("used")
	e.publish(id, "one")
	e.addServer(id, 1, "retired")
	if err := e.Svc.Delete(ctx, id, "admin"); !errors.Is(err, templates.ErrInUse) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetTemplate(ctx, e.DB.R, id); err != nil {
		t.Errorf("the template is gone: %v", err)
	}
	if len(e.Recorded("template.deleted")) != 0 {
		t.Error("a refused delete recorded an event")
	}
	info, _ := e.Svc.Get(ctx, id)
	if info.Servers != 1 || info.ServersByVersion[1]["retired"] != 1 {
		t.Errorf("servers by version: %+v", info.ServersByVersion)
	}
}

func TestDeleteUnusedTemplate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("unused")
	e.publish(id, "one")
	if _, err := e.Svc.EditDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := e.Svc.Delete(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTemplate(ctx, e.DB.R, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("template: %v", err)
	}
	var n int
	_ = e.DB.R.GetContext(ctx, &n, `SELECT (SELECT count(*) FROM servers_template_versions) + (SELECT count(*) FROM servers_template_files) + (SELECT count(*) FROM servers_template_drafts) + (SELECT count(*) FROM servers_draft_files)`)
	if n != 0 {
		t.Errorf("%d rows of the template are left", n)
	}
	evs := e.Recorded("template.deleted")
	if len(evs) != 1 || evs[0].Payload["slug"] != "unused" {
		t.Errorf("events: %+v", evs)
	}
}

func TestArchive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("old")
	e.create("new")
	names := func(archived bool) string {
		l, err := e.Svc.List(ctx, archived)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, x := range l {
			s = append(s, x.Slug)
		}
		return strings.Join(s, ",")
	}
	if err := e.Svc.Archive(ctx, id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if names(false) != "new" || names(true) != "new,old" {
		t.Errorf("list: %q / %q", names(false), names(true))
	}
	if n, _ := e.Svc.ArchivedCount(ctx); n != 1 {
		t.Errorf("archived count %d", n)
	}
	if err := e.Svc.Archive(ctx, id, true, "admin"); err != nil || len(e.Recorded("template.archived")) != 1 {
		t.Errorf("archiving twice: %v, %d events", err, len(e.Recorded("template.archived")))
	}
	if err := e.Svc.Archive(ctx, id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if names(false) != "new,old" || len(e.Recorded("template.unarchived")) != 1 {
		t.Errorf("after unarchive: %q, %d events", names(false), len(e.Recorded("template.unarchived")))
	}
}

func TestSlugFrozenAfterPublish(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("before")
	e.create("taken")
	if err := e.Svc.Rename(ctx, id, "Renamed", "after", "new text", "admin"); err != nil {
		t.Fatalf("rename before the first version: %v", err)
	}
	tpl, _ := store.GetTemplate(ctx, e.DB.R, id)
	if tpl.Slug != "after" || tpl.Name != "Renamed" || tpl.Description != "new text" {
		t.Errorf("template: %+v", tpl)
	}
	if err := e.Svc.Rename(ctx, id, "Renamed", "taken", "new text", "admin"); !errors.Is(err, templates.ErrSlugTaken) {
		t.Errorf("a taken slug: %v", err)
	}
	if err := e.Svc.Rename(ctx, id, "Renamed", "after", "new text", "admin"); err != nil || len(e.Recorded("template.changed")) != 1 {
		t.Errorf("renaming to the same values: %v, %d events", err, len(e.Recorded("template.changed")))
	}
	e.publish(id, "one")
	if err := e.Svc.Rename(ctx, id, "Renamed", "again", "new text", "admin"); !errors.Is(err, templates.ErrSlugFrozen) {
		t.Errorf("the slug after the first version: %v", err)
	}
	if err := e.Svc.Rename(ctx, id, "Third name", "after", "new text", "admin"); err != nil {
		t.Errorf("the name after the first version: %v", err)
	}
	if err := e.Svc.Rename(ctx, id, "", "after", "x", "admin"); err == nil {
		t.Error("an empty name was accepted")
	}
}
