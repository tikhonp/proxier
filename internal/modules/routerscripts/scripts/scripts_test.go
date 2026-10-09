package scripts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
)

var ctx = context.Background()

func count(t *testing.T, h *rscriptstest.Harness, q string, args ...any) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, q, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateScript(t *testing.T) {
	h := rscriptstest.New(t)
	body := string(paramstest.Today())
	if got := scripts.SuggestSlug("fresh-router.rsc"); got != "fresh-router" {
		t.Errorf("slug suggestion: %q", got)
	}
	if got := scripts.SuggestSlug("  Router: Dacha (v2)  "); got != "router-dacha-v2" {
		t.Errorf("slug suggestion: %q", got)
	}
	id, err := h.Mod.Scripts.Create(ctx, scripts.New{Name: "fresh-router.rsc", Body: body, Description: " bootstrap "}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.Mod.Scripts.Get(ctx, id)
	if err != nil || s.Name != "fresh-router.rsc" || s.Slug != "fresh-router" || s.Description != "bootstrap" || s.Current != 0 || s.Archived {
		t.Fatalf("script: %+v %v", s, err)
	}
	d, err := h.Mod.Scripts.Draft(ctx, id)
	if err != nil || d.Body != body || d.BasedOn != 0 || d.Revision != 1 || d.UpdatedBy != "admin" {
		t.Errorf("draft: %+v %v", d, err)
	}
	evs := h.Events("routerscript.created")
	if len(evs) != 1 || evs[0].Payload["name"] != "fresh-router.rsc" || evs[0].Subject.String() != "router_script:1" || evs[0].Actor != "admin" {
		t.Errorf("events: %+v", evs)
	}
	// the page: a paste gets LF, an upload is kept byte for byte and wins
	crlf := strings.ReplaceAll(body, "\n", "\r\n")
	res := h.Login.PostMultipart("/router-scripts", map[string][]string{"name": {"pasted"}, "body": {crlf}}, nil)
	if res.Code != 303 {
		t.Fatalf("paste: %d %s", res.Code, res.Body.String())
	}
	var pasted string
	_ = h.App.DB.R.Get(&pasted, `SELECT d.body FROM rscripts_drafts d JOIN rscripts_scripts s ON s.id = d.script_id WHERE s.name = 'pasted'`)
	if pasted != body {
		t.Error("a pasted body did not get LF")
	}
}

func TestScriptRules(t *testing.T) {
	h := rscriptstest.New(t)
	h.Script("fresh-router", paramstest.Today())
	ok := scripts.New{Name: "other", Body: "x"}
	cases := []struct {
		name  string
		n     scripts.New
		field string
		key   string
	}{
		{"empty name", scripts.New{Name: "  ", Slug: "x", Body: "x"}, "name", "scripts.err.name"},
		{"61 characters", scripts.New{Name: strings.Repeat("я", 61), Slug: "x", Body: "x"}, "name", "scripts.err.name"},
		{"taken name", scripts.New{Name: "fresh-router", Slug: "x", Body: "x"}, "name", "scripts.err.name_taken"},
		{"bad slug", scripts.New{Name: "a", Slug: "Fresh_Router", Body: "x"}, "slug", "scripts.err.slug"},
		{"slug starting with a dash", scripts.New{Name: "a", Slug: "-a", Body: "x"}, "slug", "scripts.err.slug"},
		{"taken slug", scripts.New{Name: "a", Slug: "fresh-router", Body: "x"}, "slug", "scripts.err.slug_taken"},
		{"long description", scripts.New{Name: "a", Body: "x", Description: strings.Repeat("d", 501)}, "description", "scripts.err.description"},
		{"empty body", scripts.New{Name: "a", Body: ""}, "body", "scripts.err.body_empty"},
		{"NUL", scripts.New{Name: "a", Body: "a\x00b"}, "body", "scripts.err.body_binary"},
		{"not UTF-8", scripts.New{Name: "a", Body: "a\xffb"}, "body", "scripts.err.body_binary"},
		{"over 512 KiB", scripts.New{Name: "a", Body: strings.Repeat("x", 512<<10+1)}, "body", "scripts.err.body_large"},
	}
	for _, c := range cases {
		_, err := h.Mod.Scripts.Create(ctx, c.n, "admin")
		var fe store.FieldErrors
		if !errors.As(err, &fe) || fe[c.field] != c.key {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if n := count(t, h, `SELECT count(*) FROM rscripts_scripts`); n != 1 {
		t.Errorf("%d scripts", n)
	}
	if n := len(h.Events("routerscript.created")); n != 1 {
		t.Errorf("%d created events", n)
	}
	// exactly 60 characters and 512 KiB are fine
	if _, err := h.Mod.Scripts.Create(ctx, scripts.New{Name: strings.Repeat("я", 60), Body: strings.Repeat("x", 512<<10), Slug: "big"}, "admin"); err != nil {
		t.Errorf("at the limits: %v", err)
	}
	if _, err := h.Mod.Scripts.Create(ctx, ok, "admin"); err != nil {
		t.Errorf("ok: %v", err)
	}
}

func TestEditDetails(t *testing.T) {
	h := rscriptstest.New(t)
	id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "draft-only", Body: "x"}, "admin")
	changed, err := h.Mod.Scripts.Edit(ctx, id, scripts.Details{Name: "draft-only", Slug: "draft-only"}, "admin")
	if err != nil || changed || len(h.Events("routerscript.changed")) != 0 {
		t.Fatalf("nothing changed: %v %v", changed, err)
	}
	// before a version the slug can change
	if changed, err := h.Mod.Scripts.Edit(ctx, id, scripts.Details{Name: "Draft only", Slug: "draft", Description: "d"}, "admin"); err != nil || !changed {
		t.Fatalf("edit: %v", err)
	}
	evs := h.Events("routerscript.changed")
	if len(evs) != 1 || evs[0].Payload["fields"] != "name, slug, description" {
		t.Errorf("changed: %+v", evs)
	}
	sid := h.Script("fresh-router", paramstest.Today())
	if _, err := h.Mod.Scripts.Edit(ctx, sid, scripts.Details{Name: "fresh-router", Slug: "fresh"}, "admin"); err == nil {
		t.Error("the slug changed after v1")
	} else if fe, ok := err.(store.FieldErrors); !ok || fe["slug"] != "scripts.err.slug_fixed" {
		t.Errorf("slug after v1: %v", err)
	}
	if _, err := h.Mod.Scripts.Edit(ctx, sid, scripts.Details{Name: "fresh", Slug: "fresh-router", Description: "x"}, "admin"); err != nil {
		t.Errorf("name after v1: %v", err)
	}
	if evs := h.Events("routerscript.changed"); len(evs) != 2 || evs[1].Payload["fields"] != "name, description" {
		t.Errorf("changed after v1: %+v", evs)
	}
	if s, _ := h.Mod.Scripts.Get(ctx, sid); s.Slug != "fresh-router" || s.Name != "fresh" {
		t.Errorf("script: %+v", s)
	}
}
