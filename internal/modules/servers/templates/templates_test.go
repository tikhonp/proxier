package templates_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
)

func TestCreateTemplate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, err := e.Svc.Create(ctx, "My stack", "my-stack", "A stack", "admin")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Svc.Draft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Revision != 1 || d.BasedOn != 0 || len(d.Files) != 2 || !strings.Contains(string(d.Files["manifest.yaml"]), `name: "My stack"`) ||
		!strings.Contains(string(d.Files["manifest.yaml"]), "dir: /opt/proxier/my-stack") {
		t.Fatalf("draft: rev %d based on %d, files %v", d.Revision, d.BasedOn, d.Files)
	}
	// the skeleton is a manifest that validates clean
	r, err := e.Svc.ValidateDraft(ctx, id, "admin", "admin")
	if err != nil || len(r.Findings) != 0 {
		t.Fatalf("the skeleton must validate with no findings: %v %v", r.Findings, err)
	}
	if ev := e.Recorded("template.created"); len(ev) != 1 || ev[0].Subject.Type != "template" || ev[0].Payload["slug"] != "my-stack" || ev[0].Actor != "admin" {
		t.Errorf("template.created: %+v", ev)
	}
	// a name with characters YAML would read as structure survives
	id2, err := e.Svc.Create(ctx, `Tricky: "quoted" # name`, "tricky", "line one\nline two: #", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := e.Svc.ValidateDraft(ctx, id2, "admin", "admin"); !r.OK() {
		t.Errorf("a name with YAML punctuation must still validate: %v", r.Findings)
	}
	// rules
	for _, c := range []struct{ name, slug, field string }{
		{"", "ok-slug", "name"}, {strings.Repeat("x", 81), "ok-slug", "name"},
		{"N", "A", "slug"}, {"N", "a", "slug"}, {"N", "1abc", "slug"}, {"N", "has_underscore", "slug"}, {"N", strings.Repeat("a", 41), "slug"},
	} {
		_, err := e.Svc.Create(ctx, c.name, c.slug, "", "admin")
		var fe store.FieldErrors
		if !errors.As(err, &fe) || fe[c.field] == "" {
			t.Errorf("%q/%q must be refused on %s: %v", c.name, c.slug, c.field, err)
		}
	}
	if _, err := e.Svc.Create(ctx, "Again", "my-stack", "", "admin"); !errors.Is(err, templates.ErrSlugTaken) {
		t.Errorf("a taken slug: %v", err)
	}
	if len(e.Recorded("template.created")) != 2 {
		t.Error("refused creates must record nothing")
	}
}
