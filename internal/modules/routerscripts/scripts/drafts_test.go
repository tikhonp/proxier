package scripts_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestSaveDraftRevisions(t *testing.T) {
	h := rscriptstest.New(t)
	id, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "s", Body: "a\n"}, "admin")
	rev, err := h.Mod.Scripts.SaveDraft(ctx, id, 1, "b\n", "admin")
	if err != nil || rev != 2 {
		t.Fatalf("save: %d %v", rev, err)
	}
	evs := h.Events("routerscript.draft_saved")
	if len(evs) != 1 || evs[0].Payload["based_on"] != float64(0) {
		t.Errorf("draft_saved: %+v", evs)
	}
	if rev, err := h.Mod.Scripts.SaveDraft(ctx, id, 2, "b\n", "admin"); err != nil || rev != 2 {
		t.Errorf("same body: %d %v", rev, err)
	}
	if n := len(h.Events("routerscript.draft_saved")); n != 1 {
		t.Errorf("the same body recorded: %d", n)
	}
	if _, err := h.Mod.Scripts.SaveDraft(ctx, id, 1, "c\n", "admin"); !errors.Is(err, scripts.ErrDraftChanged) {
		t.Errorf("stale: %v", err)
	}
	if d, _ := h.Mod.Scripts.Draft(ctx, id); d.Body != "b\n" || d.Revision != 2 {
		t.Errorf("after a stale save: %+v", d)
	}
	if _, err := h.Mod.Scripts.SaveDraft(ctx, id, 2, "", "admin"); err == nil {
		t.Error("an empty body was saved")
	}
}

func TestEditAndDiscardDraft(t *testing.T) {
	h := rscriptstest.New(t)
	body := paramstest.WithEnd(paramstest.Today())
	id := h.Script("fresh-router", body)
	if _, err := h.Mod.Scripts.Draft(ctx, id); !errors.Is(err, scripts.ErrNoDraft) {
		t.Fatalf("a draft after publishing: %v", err)
	}
	d, err := h.Mod.Scripts.EditDraft(ctx, id, "admin")
	if err != nil || d.Body != string(body) || d.BasedOn != 1 || d.Revision != 1 {
		t.Fatalf("edit: %+v %v", d, err)
	}
	rev, _ := h.Mod.Scripts.SaveDraft(ctx, id, 1, "changed\n", "admin")
	// Edit opens the existing draft
	if d, err := h.Mod.Scripts.EditDraft(ctx, id, "admin"); err != nil || d.Body != "changed\n" || d.Revision != rev {
		t.Errorf("existing: %+v %v", d, err)
	}
	if err := h.Mod.Scripts.DiscardDraft(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	evs := h.Events("routerscript.draft_discarded")
	if len(evs) != 1 || evs[0].Payload["based_on"] != float64(1) {
		t.Errorf("discarded: %+v", evs)
	}
	if _, err := h.Mod.Scripts.Draft(ctx, id); !errors.Is(err, scripts.ErrNoDraft) {
		t.Errorf("the draft stayed: %v", err)
	}
	if s, _ := h.Mod.Scripts.Get(ctx, id); s.Current != 1 {
		t.Errorf("current: %d", s.Current)
	}
	// a script with no version keeps its draft
	nid, _ := h.Mod.Scripts.Create(ctx, scripts.New{Name: "new", Body: "x"}, "admin")
	if err := h.Mod.Scripts.DiscardDraft(ctx, nid, "admin"); !errors.Is(err, scripts.ErrNoVersion) {
		t.Errorf("discard without a version: %v", err)
	}
	if _, err := h.Mod.Scripts.Draft(ctx, nid); err != nil {
		t.Errorf("the draft went: %v", err)
	}
	if n := len(h.Events("routerscript.draft_discarded")); n != 1 {
		t.Errorf("%d discarded events", n)
	}
}

func TestEditorLineEndings(t *testing.T) {
	h := rscriptstest.New(t)
	lf := string(paramstest.WithEnd(paramstest.Today()))
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	// posted CRLF becomes LF for an LF draft
	if got := scripts.Normalize(crlf, lf); got != lf {
		t.Error("CRLF posted for an LF body")
	}
	if got := scripts.Normalize(lf, crlf); got != crlf {
		t.Error("LF posted for a CRLF body")
	}
	if got := scripts.Normalize(crlf, crlf); got != crlf {
		t.Error("CRLF posted for a CRLF body")
	}
	// a CRLF upload stays CRLF when saved from the editor; unchanged, no revision
	res := h.Login.PostMultipart("/router-scripts", map[string][]string{"name": {"crlf"}},
		map[string]sitetest.File{"file": {Name: "fresh-router.rsc", Data: []byte(crlf)}})
	if res.Code != 303 {
		t.Fatalf("upload: %d %s", res.Code, res.Body.String())
	}
	d, _ := h.Mod.Scripts.Draft(ctx, 1)
	if d.Body != crlf {
		t.Fatal("the upload was not kept byte for byte")
	}
	// the browser posts the text area with CRLF
	res = h.Login.PostMultipart("/router-scripts/1/draft", map[string][]string{"revision": {"1"}, "body": {crlf}}, nil)
	if res.Code != 303 {
		t.Fatalf("save: %d", res.Code)
	}
	if d, _ := h.Mod.Scripts.Draft(ctx, 1); d.Body != crlf || d.Revision != 1 {
		t.Errorf("unchanged save: revision %d, CRLF kept %v", d.Revision, d.Body == crlf)
	}
	edited := strings.Replace(crlf, `"10.230.1"`, `"10.40.1"`, 1)
	h.Login.PostMultipart("/router-scripts/1/draft", map[string][]string{"revision": {"1"}, "body": {edited}}, nil)
	if d, _ := h.Mod.Scripts.Draft(ctx, 1); d.Body != edited || d.Revision != 2 {
		t.Errorf("edited save: revision %d, CRLF kept %v", d.Revision, d.Body == edited)
	}
	// an LF draft gets LF from a CRLF post
	if _, err := h.Mod.Scripts.Create(ctx, scripts.New{Name: "lf", Body: lf}, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Login.PostMultipart("/router-scripts/2/draft", map[string][]string{"revision": {"1"}, "body": {strings.Replace(crlf, "ether1", "ether2", 1)}}, nil)
	if d, _ := h.Mod.Scripts.Draft(ctx, 2); d.Body != strings.Replace(lf, "ether1", "ether2", 1) {
		t.Error("an LF draft got CRLF")
	}
}
