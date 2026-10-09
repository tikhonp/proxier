package scripts_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
)

const insertGeneration = `INSERT INTO rscripts_generations (script_id, version, router_name, file_name, created_at, created_by)
	VALUES (?, ?, 'Dacha', 'x.rsc', '2026-10-09T12:00:00.000Z', 'admin')`

func TestMakeCurrent(t *testing.T) {
	h := rscriptstest.New(t)
	end := paramstest.WithEnd(paramstest.Today())
	id := h.Script("fresh-router", end)
	for i := 2; i <= 4; i++ {
		h.Publish(id, paramstest.Replace(end, `"usb1"`, `"usb`+string(rune('0'+i))+`"`))
	}
	if err := h.Mod.Scripts.MakeCurrent(ctx, id, 3, "admin"); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.Mod.Scripts.Get(ctx, id); s.Current != 3 {
		t.Errorf("current: %d", s.Current)
	}
	evs := h.Events("routerscript.current_changed")
	if len(evs) != 1 || evs[0].Payload["from"] != float64(4) || evs[0].Payload["to"] != float64(3) {
		t.Errorf("event: %+v", evs)
	}
	vs, _ := h.Mod.Scripts.Versions(ctx, id)
	if len(vs) != 4 || vs[0].Number != 4 || vs[0].Current || !vs[1].Current || vs[0].Body != "" {
		t.Errorf("versions: %+v", vs)
	}
	if v, _ := h.Mod.Scripts.Version(ctx, id, 0); v.Number != 3 || !strings.Contains(v.Body, `"usb3"`) {
		t.Errorf("the current version: v%d", v.Number)
	}
	if err := h.Mod.Scripts.MakeCurrent(ctx, id, 3, "admin"); err != nil || len(h.Events("routerscript.current_changed")) != 1 {
		t.Errorf("again: %v", err)
	}
	if err := h.Mod.Scripts.MakeCurrent(ctx, id, 9, "admin"); !errors.Is(err, scripts.ErrNoVersion) {
		t.Errorf("v9: %v", err)
	}
	// the next publish makes v5 current
	if n := h.Publish(id, end); n != 5 {
		t.Errorf("next: v%d", n)
	}
	if s, _ := h.Mod.Scripts.Get(ctx, id); s.Current != 5 {
		t.Errorf("after publishing: %d", s.Current)
	}
	// the draft made by Edit is based on the current version
	if err := h.Mod.Scripts.MakeCurrent(ctx, id, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.Mod.Scripts.EditDraft(ctx, id, "admin"); d.BasedOn != 2 || !strings.Contains(d.Body, `"usb2"`) {
		t.Errorf("edit after make current: %+v", d.BasedOn)
	}
}

func TestDiff(t *testing.T) {
	h := rscriptstest.New(t)
	end := paramstest.WithEnd(paramstest.Today())
	id := h.Script("fresh-router", end)
	h.Publish(id, paramstest.Replace(end, `"10.230.1"`, `"10.40.1"`))
	d, err := h.Mod.Scripts.Diff(ctx, id, "1", "2")
	if err != nil || !strings.Contains(d, "-:local lanNet \"10.230.1\"") || !strings.Contains(d, "+:local lanNet \"10.40.1\"") ||
		!strings.Contains(d, "fresh-router-v1.rsc") || strings.Count(d, "\n-") != 1 {
		t.Errorf("v1 → v2: %q %v", d, err)
	}
	if d, _ := h.Mod.Scripts.Diff(ctx, id, "2", "1"); !strings.Contains(d, "+:local lanNet \"10.230.1\"") {
		t.Errorf("v2 → v1: %q", d)
	}
	// the draft against its base
	dr, _ := h.Mod.Scripts.EditDraft(ctx, id, "admin")
	if _, err := h.Mod.Scripts.SaveDraft(ctx, id, dr.Revision, strings.Replace(string(end), "ether1", "ether9", 1), "admin"); err != nil {
		t.Fatal(err)
	}
	d, err = h.Mod.Scripts.Diff(ctx, id, "2", scripts.DraftRef)
	if err != nil || !strings.Contains(d, "+:local wanIface \"ether9\"") || !strings.Contains(d, "fresh-router-draft.rsc") {
		t.Errorf("draft: %q %v", d, err)
	}
	// equal bodies say so
	if d, err := h.Mod.Scripts.Diff(ctx, id, "2", "2"); err != nil || d != "" {
		t.Errorf("equal: %q %v", d, err)
	}
	if _, err := h.Mod.Scripts.Diff(ctx, id, "7", "2"); !errors.Is(err, scripts.ErrNoVersion) {
		t.Errorf("v7: %v", err)
	}
	if _, err := h.Mod.Scripts.Diff(ctx, id, "x", "2"); !errors.Is(err, scripts.ErrNoVersion) {
		t.Errorf("x: %v", err)
	}
}

func TestArchiveAndDelete(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.WithEnd(paramstest.Today()))
	other := h.Script("other", []byte("# no block\n"))
	if err := h.Mod.Scripts.Archive(ctx, id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Scripts.Archive(ctx, id, true, "admin"); err != nil || len(h.Events("routerscript.archived")) != 1 {
		t.Errorf("archive twice: %v", err)
	}
	list, _ := h.Mod.Scripts.List(ctx, false)
	if len(list) != 1 || list[0].ID != other {
		t.Errorf("list: %+v", list)
	}
	if arch, _ := h.Mod.Scripts.List(ctx, true); len(arch) != 1 || arch[0].ID != id {
		t.Errorf("archived: %+v", arch)
	}
	if n, _ := h.Mod.Scripts.ArchivedCount(ctx); n != 1 {
		t.Errorf("archived count: %d", n)
	}
	if hits, _ := h.Mod.Search(context.Background(), "fresh", 10); len(hits) != 0 {
		t.Errorf("search found an archived script: %+v", hits)
	}
	if err := h.Mod.Scripts.Archive(ctx, id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if evs := h.Events("routerscript.archived"); len(evs) != 2 || evs[0].Payload["archived"] != true || evs[1].Payload["archived"] != false {
		t.Errorf("archived events: %+v", evs)
	}
	// a generation keeps the script
	if _, err := h.Mod.Scripts.EditDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Exec(insertGeneration, id, 1)
	if err := h.Mod.Scripts.Delete(ctx, id, "admin"); !errors.Is(err, scripts.ErrHasGenerations) {
		t.Fatalf("with a generation: %v", err)
	}
	if list, _ := h.Mod.Scripts.List(ctx, false); len(list) != 2 || list[0].Generations != 1 {
		t.Errorf("generation counted: %+v", list)
	}
	if vs, _ := h.Mod.Scripts.Versions(ctx, id); vs[0].Generations != 1 {
		t.Errorf("version's generations: %+v", vs)
	}
	h.Exec(`DELETE FROM rscripts_generations`)
	if err := h.Mod.Scripts.Delete(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, h, `SELECT count(*) FROM rscripts_scripts WHERE id = ?`, id) + count(t, h, `SELECT count(*) FROM rscripts_versions WHERE script_id = ?`, id) +
		count(t, h, `SELECT count(*) FROM rscripts_drafts WHERE script_id = ?`, id); n != 0 {
		t.Errorf("rows left: %d", n)
	}
	evs := h.Events("routerscript.deleted")
	if len(evs) != 1 || evs[0].Payload["name"] != "fresh-router" {
		t.Errorf("deleted: %+v", evs)
	}
	if _, err := h.Mod.Scripts.Get(ctx, id); !errors.Is(err, scripts.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
}
