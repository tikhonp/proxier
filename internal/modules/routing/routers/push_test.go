package routers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestImportErrorKeepsEarlierTags(t *testing.T) {
	h := routingtest.New(t)
	// a fills the first file; b and c share the second
	a := h.Custom("a")
	setCustom(t, h, a, "a", customNames("a", 1990)...)
	b := h.Custom("b", "b-old.com")
	c := h.Custom("c", "c-old.com")
	h.List("Main", a, b, c)
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	oldC := applied(t, h, id)["c"]

	setCustom(t, h, a, "a", customNames("a", 1999)...)
	setCustom(t, h, b, "b", "b-old.com", "b-new.com")
	setCustom(t, h, c, "c", "c-old.com", "c-new.com")
	n := len(r.Imports())
	r.FailAtBlock(n+2, 2, "syntax error (line 9 column 12)")
	h.Advance(31 * time.Second)
	h.Settle()

	if imp := r.Imports(); len(imp) != n+2 {
		t.Fatalf("two files pushed: %v", imp[n:])
	}
	ops, _ := routeros.ParseScript(r.Import(n + 2))
	if len(ops) != 2 || ops[0].Tag != "b" || ops[1].Tag != "c" {
		t.Fatalf("the second file holds b and c: %+v", ops)
	}
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "push" || !strings.Contains(ev[0].Payload["error"].(string), "syntax error (line 9 column 12)") {
		t.Fatalf("failed at push: %+v", ev)
	}
	if len(r.Files()) != 0 {
		t.Fatalf("the failed file is deleted: %v", r.Files())
	}
	got := applied(t, h, id)
	if got["a"].Hash != routeros.EntriesHash(entries(customNames("a", 1999)...)) {
		t.Fatal("a (first file) keeps its new applied state")
	}
	if got["b"].Hash != routeros.EntriesHash(entries("b-old.com", "b-new.com")) {
		t.Fatal("b ran before the failing line: recorded after the re-read")
	}
	if got["c"] != oldC {
		t.Fatalf("c keeps its old applied state: %+v", got["c"])
	}
	if s := lastSync(t, h, id); s.State != "failed" || s.Step != "push" {
		t.Fatalf("row: %+v", s)
	}
	// the retry re-plans and pushes c only
	h.Advance(5 * time.Minute)
	h.Settle()
	ops, _ = routeros.ParseScript(r.Import(n + 3))
	if len(ops) != 1 || ops[0].Tag != "c" || lastSync(t, h, id).State != "done" {
		t.Fatalf("retry: %+v", ops)
	}
}

func TestVerifyCatchesMismatch(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("openai", "a.openai.com\nb.openai.com\nc.openai.com\nd.openai.com\ne.openai.com\n")
	h.List("Main", h.Upstream("v2fly:openai"))
	r, id := h.Router("Home", 0)
	r.DropListAdds("openai", 2)
	syncNow(t, h, id)
	ev := h.Events("routing.router_sync_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "verify" || ev[0].Payload["error"] != "openai: 3 names on the router, 5 expected." {
		t.Fatalf("verify: %+v", ev)
	}
	if s := lastSync(t, h, id); s.State != "failed" || s.Step != "verify" {
		t.Fatalf("row: %+v", s)
	}
	// the applied state written after the file stays; the retry pushes again
	if _, ok := applied(t, h, id)["openai"]; !ok {
		t.Fatal("applied after its file")
	}
	h.Advance(5 * time.Minute)
	h.Settle()
	if s := lastSync(t, h, id); s.State != "done" || s.Updated != 1 {
		t.Fatalf("retry: %+v", s)
	}
}

func TestBigTagSplitsAcrossFiles(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("big", lines("big", 6000))
	h.List("Main", h.Upstream("v2fly:big"))
	r, id := h.Router("Home", 0)
	syncNow(t, h, id)
	if imp := r.Imports(); len(imp) != 3 {
		t.Fatalf("three files: %v", imp)
	}
	for i := 1; i <= 3; i++ {
		ops, err := routeros.ParseScript(r.Import(i))
		if err != nil || len(ops) != 1 || len(ops[0].Entries) != 2000 || (ops[0].Kind == "continue") != (i > 1) {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	if len(r.Names("big")) != 6000 {
		t.Fatalf("installed: %d", len(r.Names("big")))
	}
	if s := lastSync(t, h, id); s.State != "done" || s.Added != 1 {
		t.Fatalf("verified: %+v", s)
	}
	if a := applied(t, h, id)["big"]; a.Suffix != 6000 {
		t.Fatalf("applied: %+v", a)
	}
}
