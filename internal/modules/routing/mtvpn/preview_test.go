package mtvpn_test

import (
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

func TestDedupByTagFirstSpellingWins(t *testing.T) {
	h := routingtest.New(t)
	list := h.Up.File("l.txt", "v2fly:anthropic\niplist:anthropic\nopenai\n")
	im := preview(t, h, "services:\n  - anthropic\nservice_lists:\n  - "+list+"\n")
	if len(im.Rows) != 2 {
		t.Fatalf("rows: %+v", im.Rows)
	}
	r := im.Rows[0]
	if r.Selector != "v2fly:anthropic" || r.Tag != "anthropic" || r.From != "services" || r.Status != mtvpn.StatusNew || !r.Include {
		t.Errorf("row: %+v", r)
	}
	if im.Rows[1].Tag != "openai" || im.Rows[1].From != "l.txt" {
		t.Errorf("second: %+v", im.Rows[1])
	}
	// an existing service is reused, even with another selector
	h.Up.Site("main", "ai", "anthropic.com", "anthropic.com")
	h.Upstream("iplist:anthropic.com")
	im = preview(t, h, "services:\n  - anthropic.com\n")
	if r := im.Rows[0]; r.Status != mtvpn.StatusExists || r.ExistingAs != "iplist:anthropic.com" {
		t.Errorf("exists: %+v", r)
	}
}

func TestNamedURLRow(t *testing.T) {
	h := routingtest.New(t)
	u := h.Up.File("list.txt", "example.com\n")
	im := preview(t, h, "services:\n  - mine="+u+"\n  - not a selector\n")
	if len(im.Rows) != 2 {
		t.Fatalf("rows: %+v", im.Rows)
	}
	if r := im.Rows[0]; r.Tag != "mine" || !r.URL || r.Selector != "mine="+u || r.Convert || !r.Include {
		t.Errorf("url row: %+v", r)
	}
	if r := im.Rows[1]; r.Status != mtvpn.StatusInvalid || r.Problem == "" || r.Include {
		t.Errorf("invalid row: %+v", r)
	}
	if im.Importable() != 1 {
		t.Errorf("importable %d", im.Importable())
	}
}

func TestFailingServiceList(t *testing.T) {
	h := routingtest.New(t)
	bad := h.Up.File("gone.txt", "x\n")
	h.Up.Fail("/files/gone.txt", 404)
	empty := h.Up.File("empty.txt", "# nothing\n")
	im := preview(t, h, "services:\n  - anthropic\nservice_lists:\n  - "+bad+"\n  - ~/lists/local.txt\n  - "+empty+"\n")
	if len(im.Rows) != 1 || im.Rows[0].Tag != "anthropic" {
		t.Errorf("rows: %+v", im.Rows)
	}
	want := []mtvpn.ListFailure{{URL: bad, Error: "HTTP 404"}, {URL: "~/lists/local.txt", Error: "mtvpn.err.list_path"}, {URL: empty, Error: "mtvpn.err.list_empty"}}
	if len(im.ListFailures) != 3 {
		t.Fatalf("failures: %+v", im.ListFailures)
	}
	for i, f := range want {
		if im.ListFailures[i] != f {
			t.Errorf("failure %d: %+v", i, im.ListFailures[i])
		}
	}
	// a base that isn't a URL, or has no [Rule]: no config offered
	im = preview(t, h, "shadowrocket_base: ~/base.conf\n")
	if im.Shadowrocket.Offered || im.Shadowrocket.Problem != "mtvpn.base.path" {
		t.Errorf("path base: %+v", im.Shadowrocket)
	}
	b := h.Up.File("norule.conf", "[General]\n")
	im = preview(t, h, "shadowrocket_base: "+b+"\n")
	if im.Shadowrocket.Offered || im.Shadowrocket.Problem != "mtvpn.base.no_rule" {
		t.Errorf("no rule: %+v", im.Shadowrocket)
	}
	// an empty file and a broken one
	if _, err := h.Mod.Import.Preview(bg(), " \n", "admin"); !errors.As(err, new(store.FieldErrors)) {
		t.Errorf("empty: %v", err)
	}
	if _, err := h.Mod.Import.Preview(bg(), "services\n", "admin"); !errors.As(err, new(*mtvpn.ParseError)) {
		t.Errorf("broken: %v", err)
	}
}

func TestUnknownKeysIgnored(t *testing.T) {
	h := routingtest.New(t)
	im := preview(t, h, "foo: bar\nservices:\n  - anthropic\n")
	if len(im.Ignored) != 1 || im.Ignored[0] != "foo" {
		t.Errorf("ignored: %+v", im.Ignored)
	}
	var row store.Import
	if err := h.App.DB.R.Get(&row, `SELECT * FROM routing_imports WHERE id = ?`, im.ID); err != nil {
		t.Fatal(err)
	}
	if row.Ignored != "foo" || contains(row.Rows+row.Shadowrocket+row.ListFailures+row.Base, "bar") {
		t.Errorf("stored: %+v", row)
	}
}
