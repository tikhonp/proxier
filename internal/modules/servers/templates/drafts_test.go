package templates_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

func TestStaleSaveIsRefused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("stale")
	// two tabs open the same draft at revision 1
	tabA, _ := e.Svc.Draft(ctx, id)
	tabB, _ := e.Svc.Draft(ctx, id)
	a := map[string][]byte{"manifest.yaml": tabA.Files["manifest.yaml"], "compose.yaml": []byte("a")}
	b := map[string][]byte{"manifest.yaml": tabB.Files["manifest.yaml"], "compose.yaml": []byte("b")}
	rev, err := e.Svc.SaveDraft(ctx, id, tabA.Revision, a, "admin", "admin")
	if err != nil || rev != 2 {
		t.Fatalf("first save: %d %v", rev, err)
	}
	events := len(e.Recorded("template.draft_saved"))
	if _, err := e.Svc.SaveDraft(ctx, id, tabB.Revision, b, "admin", "admin"); !errors.Is(err, templates.ErrDraftChanged) {
		t.Fatalf("the second tab's save: %v", err)
	}
	d, _ := e.Svc.Draft(ctx, id)
	if string(d.Files["compose.yaml"]) != "a" || d.Revision != 2 {
		t.Errorf("the refused save changed the draft: rev %d, compose %q", d.Revision, d.Files["compose.yaml"])
	}
	if len(e.Recorded("template.draft_saved")) != events {
		t.Error("a refused save recorded an event")
	}
	// after reloading, the save goes through
	if rev, err := e.Svc.SaveDraft(ctx, id, d.Revision, b, "admin", "admin"); err != nil || rev != 3 {
		t.Errorf("save after reload: %d %v", rev, err)
	}
	if _, err := e.Svc.SaveDraft(ctx, 999, 1, a, "admin", "admin"); !errors.Is(err, templates.ErrNoDraft) {
		t.Errorf("a template without draft: %v", err)
	}
}

func TestNoOpSaveRecordsNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("noop")
	d, _ := e.Svc.Draft(ctx, id)
	before := len(e.Recorded("template.draft_saved"))
	rev, err := e.Svc.SaveDraft(ctx, id, d.Revision, d.Files, "admin", "admin")
	if err != nil || rev != d.Revision {
		t.Fatalf("a save without changes keeps the revision: %d %v", rev, err)
	}
	if len(e.Recorded("template.draft_saved")) != before {
		t.Error("a save that changes no file recorded an event")
	}
	// a change records one event naming the file, with who made it
	changed := map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"], "compose.yaml": []byte("x")}
	if _, err := e.Svc.SaveDraft(ctx, id, d.Revision, changed, "agent", "admin"); err != nil {
		t.Fatal(err)
	}
	ev := e.Recorded("template.draft_saved")
	if len(ev) != before+1 || ev[0].Payload["by"] != "agent" || ev[0].Payload["changed"] != float64(1) {
		t.Errorf("event: %+v", ev)
	}
	if f, _ := ev[0].Payload["files"].([]any); len(f) != 1 || f[0] != "compose.yaml" {
		t.Errorf("files: %v", ev[0].Payload["files"])
	}
	// removing a file counts as a change too
	d, _ = e.Svc.Draft(ctx, id)
	if rev, _ := e.Svc.SaveDraft(ctx, id, d.Revision, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"]}, "admin", "admin"); rev != d.Revision+1 {
		t.Errorf("deleting a file: revision %d", rev)
	}
}

func TestValidateDraftRecordsItsResult(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.create("validate")
	d, _ := e.Svc.Draft(ctx, id)
	_, _ = e.Svc.SaveDraft(ctx, id, d.Revision, map[string][]byte{"manifest.yaml": d.Files["manifest.yaml"]}, "admin", "admin")
	r, err := e.Svc.ValidateDraft(ctx, id, "agent", "admin")
	if err != nil || r.OK() {
		t.Fatalf("a manifest listing a missing file must have errors: %v %v", r.Findings, err)
	}
	ev := e.Recorded("template.draft_validated")
	if len(ev) != 1 || ev[0].Payload["by"] != "agent" || ev[0].Payload["errors"] != float64(len(r.Errors())) {
		t.Errorf("event: %+v", ev)
	}
	if _, err := e.Svc.ValidateDraft(ctx, 999, "admin", "admin"); !errors.Is(err, templates.ErrNoDraft) {
		t.Errorf("no draft: %v", err)
	}
}
