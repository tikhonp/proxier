package templates_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

func TestPublishWithErrorsCreatesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("errors")
	d, _ := e.Svc.Draft(ctx, id)
	rev, _ := e.Svc.SaveDraft(ctx, id, d.Revision, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"]}, "admin", "admin")
	_, err := e.Svc.Publish(ctx, id, rev, "notes", true, "admin")
	var ve *templates.ValidationError
	if !errors.As(err, &ve) || ve.Report.OK() {
		t.Fatalf("want a validation error, got %v", err)
	}
	if n, _ := store.LatestVersion(ctx, e.DB.R, id); n != 0 {
		t.Errorf("a version %d was created", n)
	}
	after, err := e.Svc.Draft(ctx, id)
	if err != nil || after.Revision != rev || len(after.Files) != 1 {
		t.Errorf("the draft must stay as it was: %+v %v", after, err)
	}
	if len(e.Recorded("template.version_published")) != 0 {
		t.Error("a failed publish recorded an event")
	}
}

func TestPublishWarningsNeedConfirmation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("warn")
	d, _ := e.Svc.Draft(ctx, id)
	files := map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"],
		"compose.yaml": []byte(strings.Replace(string(d.Files["compose.yaml"]), "nginx:stable-alpine", "nginx:latest", 1))}
	rev, _ := e.Svc.SaveDraft(ctx, id, d.Revision, files, "admin", "admin")
	_, err := e.Svc.Publish(ctx, id, rev, "n", false, "admin")
	var we *templates.WarningsError
	if !errors.As(err, &we) || len(we.Report.Warnings()) != 1 {
		t.Fatalf("want a warnings error, got %v", err)
	}
	if n, _ := store.LatestVersion(ctx, e.DB.R, id); n != 0 {
		t.Fatal("unconfirmed warnings created a version")
	}
	v, err := e.Svc.Publish(ctx, id, rev, "n", true, "admin")
	if err != nil || v != 1 {
		t.Fatalf("confirmed: %d %v", v, err)
	}
	// the warnings are kept with the version, and the event says how many
	got, _ := e.Svc.Version(ctx, id, 1)
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0].Message, "nginx:latest") {
		t.Errorf("stored warnings: %+v", got.Warnings)
	}
	ev := e.Recorded("template.version_published")
	if len(ev) != 1 || ev[0].Payload["warnings"] != float64(1) || ev[0].Payload["version"] != float64(1) {
		t.Errorf("event: %+v", ev)
	}
}

func TestPublishThenEditAgain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("again")
	d, _ := e.Svc.Draft(ctx, id)
	v, err := e.Svc.Publish(ctx, id, d.Revision, "first", false, "admin")
	if err != nil || v != 1 {
		t.Fatalf("publish: %d %v", v, err)
	}
	if _, err := e.Svc.Draft(ctx, id); !errors.Is(err, templates.ErrNoDraft) {
		t.Fatalf("the draft must be gone: %v", err)
	}
	var rows int
	_ = e.DB.R.GetContext(ctx, &rows, `SELECT count(*) FROM servers_draft_files WHERE template_id = ?`, id)
	if rows != 0 {
		t.Errorf("%d draft files left", rows)
	}
	pub, err := e.Svc.Version(ctx, id, 1)
	if err != nil || pub.Notes != "first" || string(pub.Files["compose.yaml"]) != string(d.Files["compose.yaml"]) || pub.PublishedBy != "admin" {
		t.Fatalf("version 1: %+v %v", pub, err)
	}
	// editing again makes a draft from version 1 with its files, recorded once
	nd, err := e.Svc.EditDraft(ctx, id, "admin")
	if err != nil || nd.BasedOn != 1 || nd.Revision != 1 || nd.Source.Kind != "version" || nd.Source.Version != 1 ||
		string(nd.Files["manifest.yaml"]) != string(d.Files["manifest.yaml"]) {
		t.Fatalf("new draft: %+v %v", nd, err)
	}
	again, _ := e.Svc.EditDraft(ctx, id, "admin")
	if again.Revision != nd.Revision || len(e.Recorded("template.draft_saved")) != 1 {
		t.Error("editing an existing draft must change nothing")
	}
	// publishing the new draft is version 2, based on 1
	rev, _ := e.Svc.SaveDraft(ctx, id, nd.Revision, map[string][]byte{"manifest.yaml": nd.Files["manifest.yaml"], "compose.yaml": append(nd.Files["compose.yaml"], '#')}, "admin", "admin")
	if v, err := e.Svc.Publish(ctx, id, rev, "second", false, "admin"); err != nil || v != 2 {
		t.Fatalf("second publish: %d %v", v, err)
	}
	// a publish with an old revision is refused, with a changed draft in between
	nd, _ = e.Svc.EditDraft(ctx, id, "admin")
	if _, err := e.Svc.Publish(ctx, id, nd.Revision+7, "x", false, "admin"); !errors.Is(err, templates.ErrDraftChanged) {
		t.Errorf("stale revision: %v", err)
	}
	if _, err := e.Svc.Version(ctx, id, 9); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown version: %v", err)
	}
}

func TestFirstVersionBecomesDefault(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("default")
	if tpl, _ := store.GetTemplate(ctx, e.DB.R, id); tpl.DefaultVersion != 0 {
		t.Fatalf("before publishing: default %d", tpl.DefaultVersion)
	}
	e.publish(id, "one")
	tpl, _ := store.GetTemplate(ctx, e.DB.R, id)
	if tpl.DefaultVersion != 1 {
		t.Fatalf("default %d, want 1", tpl.DefaultVersion)
	}
	ev := e.Recorded("template.default_version_changed")
	if len(ev) != 1 || ev[0].Payload["from"] != float64(0) || ev[0].Payload["to"] != float64(1) {
		t.Errorf("event: %+v", ev)
	}
	// the second one doesn't take over: the admin decides (Make default)
	e.publish(id, "two")
	tpl, _ = store.GetTemplate(ctx, e.DB.R, id)
	if tpl.DefaultVersion != 1 || len(e.Recorded("template.default_version_changed")) != 1 {
		t.Errorf("version 2 changed the default: %d", tpl.DefaultVersion)
	}
}

func TestVersionNumbersHaveNoGaps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("gaps")
	other := e.create("other")
	for want := 1; want <= 4; want++ {
		if got := e.publish(id, "n"); got != want {
			t.Errorf("version %d, want %d", got, want)
		}
		if want == 2 {
			e.publish(other, "x") // another template has its own numbers
		}
	}
	rows, err := store.ListVersions(ctx, e.DB.R, id)
	if err != nil || len(rows) != 4 {
		t.Fatalf("%d versions, %v", len(rows), err)
	}
	for i, r := range rows {
		if r.Number != 4-i {
			t.Errorf("row %d has number %d", i, r.Number)
		}
	}
	// a failed publish in between burns no number
	d, _ := e.Svc.EditDraft(ctx, id, "admin")
	_, _ = e.Svc.SaveDraft(ctx, id, d.Revision, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"]}, "admin", "admin")
	d, _ = e.Svc.Draft(ctx, id)
	if _, err := e.Svc.Publish(ctx, id, d.Revision, "bad", true, "admin"); err == nil {
		t.Fatal("a draft with a missing file must not publish")
	}
	_, _ = e.Svc.SaveDraft(ctx, id, d.Revision, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"], "compose.yaml": []byte("services:\n  w:\n    image: nginx:1\n")}, "admin", "admin")
	d, _ = e.Svc.Draft(ctx, id)
	if v, err := e.Svc.Publish(ctx, id, d.Revision, "ok", false, "admin"); err != nil || v != 5 {
		t.Errorf("after a failed publish: %d %v", v, err)
	}
}
