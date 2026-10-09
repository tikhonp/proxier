package scripts_test

import (
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
)

func TestPublishTodaysScriptNeedsTheTick(t *testing.T) {
	h := rscriptstest.New(t)
	id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "fresh-router", Body: string(paramstest.Today())}, "admin")
	_, err := h.Mod.Scripts.Publish(ctx, id, 1, "first", false, "admin")
	var pe *scripts.PublishError
	if !errors.As(err, &pe) || pe.Blocked || len(pe.Findings) != 1 || pe.Findings[0].Key != "params.warn.no_end" {
		t.Fatalf("without the tick: %v", err)
	}
	if n := count(t, h, `SELECT count(*) FROM rscripts_versions`); n != 0 {
		t.Fatalf("%d versions", n)
	}
	n, err := h.Mod.Scripts.Publish(ctx, id, 1, " first ", true, "admin")
	if err != nil || n != 1 {
		t.Fatalf("with the tick: %d %v", n, err)
	}
	s, _ := h.Mod.Scripts.Get(ctx, id)
	if s.Current != 1 {
		t.Errorf("current: %d", s.Current)
	}
	v, err := h.Mod.Scripts.Version(ctx, id, 1)
	if err != nil || v.Body != string(paramstest.Today()) || v.Notes != "first" || !v.Current || v.PublishedBy != "admin" || len(v.SHA256) != 64 {
		t.Fatalf("v1: %+v %v", v, err)
	}
	if len(v.Warnings) != 1 || v.Warnings[0].Key != "params.warn.no_end" || v.Warnings[0].Line != 66 {
		t.Errorf("stored warnings: %+v", v.Warnings)
	}
	evs := h.Events("routerscript.version_published")
	if len(evs) != 1 || evs[0].Payload["version"] != float64(1) || evs[0].Payload["parameters"] != float64(19) || evs[0].Payload["warnings"] != float64(1) {
		t.Errorf("event: %+v", evs)
	}
	if _, err := h.Mod.Scripts.Draft(ctx, id); !errors.Is(err, scripts.ErrNoDraft) {
		t.Errorf("the draft stayed: %v", err)
	}
	if _, err := h.Mod.Scripts.Edit(ctx, id, scripts.Details{Name: "fresh-router", Slug: "other"}, "admin"); err == nil {
		t.Error("the slug isn't fixed")
	}
}

func TestPublishWithEndMarkerHasNoWarning(t *testing.T) {
	h := rscriptstest.New(t)
	id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "fresh-router", Body: string(paramstest.WithEnd(paramstest.Today()))}, "admin")
	n, err := h.Mod.Scripts.Publish(ctx, id, 1, "", false, "admin")
	if err != nil || n != 1 {
		t.Fatalf("publish: %d %v", n, err)
	}
	v, _ := h.Mod.Scripts.Version(ctx, id, 0)
	if v.Number != 1 || len(v.Warnings) != 0 {
		t.Errorf("v1: %+v", v)
	}
	var raw string
	_ = h.App.DB.R.Get(&raw, `SELECT warnings FROM rscripts_versions`)
	if raw != "[]" {
		t.Errorf("warnings column: %q", raw)
	}
	if evs := h.Events("routerscript.version_published"); evs[0].Payload["parameters"] != float64(18) || evs[0].Payload["warnings"] != float64(0) {
		t.Errorf("event: %+v", evs[0].Payload)
	}
}

func TestPublishBlockedByErrors(t *testing.T) {
	h := rscriptstest.New(t)
	end := paramstest.WithEnd(paramstest.Today())
	for name, body := range map[string][]byte{
		"duplicate": paramstest.Replace(end, ":local wanIface \"ether1\"\n", ":local wanIface \"ether1\"\n:local lanNet \"10.1.1\"\n"),
		"unknown":   paramstest.Annotate(end, "subUrl", "@fil subscription-link"),
	} {
		id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: name, Body: string(body)}, "admin")
		_, err := h.Mod.Scripts.Publish(ctx, id, 1, "", true, "admin")
		var pe *scripts.PublishError
		if !errors.As(err, &pe) || !pe.Blocked || len(pe.Findings) != 1 || pe.Findings[0].Severity != params.Error {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := count(t, h, `SELECT count(*) FROM rscripts_versions`) + len(h.Events("routerscript.version_published")); n != 0 {
		t.Errorf("written: %d", n)
	}
	// notes over 500 characters
	id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "ok", Body: string(end)}, "admin")
	long := make([]byte, 501)
	for i := range long {
		long[i] = 'n'
	}
	var fe store.FieldErrors
	if _, err := h.Mod.Scripts.Publish(ctx, id, 1, string(long), false, "admin"); !errors.As(err, &fe) || fe["notes"] != "scripts.err.notes" {
		t.Errorf("notes: %v", err)
	}
}

func TestGoneParameterWarnsOnPublish(t *testing.T) {
	h := rscriptstest.New(t)
	end := paramstest.WithEnd(paramstest.Today())
	id := h.Script("fresh-router", end)
	h.Publish(id, paramstest.Replace(end, `"10.230.1"`, `"10.230.2"`))
	if n := h.Publish(id, paramstest.Replace(end, `"10.230.1"`, `"10.230.3"`)); n != 3 {
		t.Fatalf("v%d", n)
	}
	d, _ := h.Mod.Scripts.EditDraft(ctx, id, "admin")
	rev, _ := h.Mod.Scripts.SaveDraft(ctx, id, d.Revision, string(paramstest.Replace(end, ":local vethName \"vless\"\n", "")), "admin")
	_, fs, err := h.Mod.Scripts.Report(ctx, id)
	if err != nil || len(fs) != 1 || fs[0].Key != "params.warn.gone" || fs[0].Args["name"] != "vethName" || fs[0].Args["version"] != 3 {
		t.Fatalf("report: %+v %v", fs, err)
	}
	_, err = h.Mod.Scripts.Publish(ctx, id, rev, "", false, "admin")
	var pe *scripts.PublishError
	if !errors.As(err, &pe) || pe.Blocked || len(pe.Findings) != 1 || pe.Findings[0].Key != "params.warn.gone" {
		t.Fatalf("without the tick: %v", err)
	}
	n, err := h.Mod.Scripts.Publish(ctx, id, rev, "drop vethName", true, "admin")
	if err != nil || n != 4 {
		t.Fatalf("v4: %d %v", n, err)
	}
	v, _ := h.Mod.Scripts.Version(ctx, id, 4)
	if len(v.Warnings) != 1 || v.Warnings[0].Args["name"] != "vethName" {
		t.Errorf("stored: %+v", v.Warnings)
	}
}

func TestPublishStaleDraft(t *testing.T) {
	h := rscriptstest.New(t)
	id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "s", Body: string(paramstest.WithEnd(paramstest.Today()))}, "admin")
	// the publish page was opened at revision 1; another tab saved meanwhile
	if _, err := h.Mod.Scripts.SaveDraft(ctx, id, 1, "# PARAMETERS\n:local a \"1\"\n# END PARAMETERS\n", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Scripts.Publish(ctx, id, 1, "", true, "admin"); !errors.Is(err, scripts.ErrDraftChanged) {
		t.Fatalf("stale: %v", err)
	}
	if n := count(t, h, `SELECT count(*) FROM rscripts_versions`); n != 0 {
		t.Errorf("%d versions", n)
	}
	if s, _ := h.Mod.Scripts.Get(ctx, id); s.Current != 0 {
		t.Errorf("current %d", s.Current)
	}
}
