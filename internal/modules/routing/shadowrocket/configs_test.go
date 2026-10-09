package shadowrocket_test

import (
	"errors"
	"regexp"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

func TestCreateConfig(t *testing.T) {
	h, id := setup(t)
	c, err := h.Mod.Shadowrocket.Get(bg(), id)
	if err != nil || c.Name != "iphone" || c.List != "Main" || c.Policy != "PROXY" || !c.Enabled || c.Version != 1 || c.Rules != 2 {
		t.Fatalf("config: %+v %v", c, err)
	}
	u, _ := h.Mod.Shadowrocket.URL(bg(), id)
	if !regexp.MustCompile(`^http://proxier\.test/r/[A-Za-z0-9_-]{43}/iphone\.conf$`).MatchString(u) {
		t.Errorf("url %q", u)
	}
	v, err := h.Mod.Shadowrocket.Version(bg(), id, 1)
	if err != nil || v.Content != base {
		t.Errorf("version 1: %+v %v", v, err)
	}
	ev := h.Events("routing.shadowrocket_created")
	if len(ev) != 1 || ev[0].Subject.String() != "shadowrocket:1" || ev[0].Payload["name"] != "iphone" || ev[0].Payload["list"] != "Main" {
		t.Errorf("events: %+v", ev)
	}
	// the name is cleaned: trimmed, lower case
	id2, err := h.Mod.Shadowrocket.Create(bg(), shadowrocket.New{Name: " iPad ", ListID: 1, Policy: "Proxy-Group-A", Base: base}, "admin")
	if c, _ := h.Mod.Shadowrocket.Get(bg(), id2); err != nil || c.Name != "ipad" {
		t.Errorf("ipad: %+v %v", c, err)
	}
	for _, tc := range []struct {
		n     shadowrocket.New
		field string
		key   string
	}{
		{shadowrocket.New{Name: "iphone", ListID: 1, Policy: "PROXY", Base: base}, "name", "shadowrocket.err.name_taken"},
		{shadowrocket.New{Name: "my phone", ListID: 1, Policy: "PROXY", Base: base}, "name", "shadowrocket.err.name"},
		{shadowrocket.New{Name: "-x", ListID: 1, Policy: "PROXY", Base: base}, "name", "shadowrocket.err.name"},
		{shadowrocket.New{Name: "x", ListID: 1, Policy: "A,B", Base: base}, "policy", "shadowrocket.err.policy"},
		{shadowrocket.New{Name: "x", ListID: 1, Policy: " ", Base: base}, "policy", "shadowrocket.err.policy"},
		{shadowrocket.New{Name: "x", ListID: 1, Policy: "PROXY", Base: "[General]\n"}, "base", "shadowrocket.err.no_rule"},
		{shadowrocket.New{Name: "x", ListID: 9, Policy: "PROXY", Base: base}, "list", "shadowrocket.err.list"},
	} {
		_, err := h.Mod.Shadowrocket.Create(bg(), tc.n, "admin")
		var fe store.FieldErrors
		if !errors.As(err, &fe) || fe[tc.field] != tc.key {
			t.Errorf("%+v: %v", tc.n, err)
		}
	}
	if n := len(h.Events("routing.shadowrocket_created")); n != 2 {
		t.Errorf("%d created events", n)
	}
}

func TestRegenerateToken(t *testing.T) {
	h, id := setup(t)
	old := pathOf(t, h, id)
	if rec := fetch(h, "GET", old); rec.Code != 200 {
		t.Fatalf("before: %d", rec.Code)
	}
	if err := h.Mod.Shadowrocket.RegenerateToken(bg(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	cur := pathOf(t, h, id)
	if cur == old {
		t.Fatal("the same token")
	}
	if rec := fetch(h, "GET", old); rec.Code != 404 {
		t.Errorf("old URL: %d", rec.Code)
	}
	if rec := fetch(h, "GET", cur); rec.Code != 200 {
		t.Errorf("new URL: %d", rec.Code)
	}
	ev := h.Events("routing.shadowrocket_updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "token" {
		t.Errorf("events: %+v", ev)
	}
}

func TestDisabledConfig(t *testing.T) {
	h, id := setup(t)
	p := pathOf(t, h, id)
	if err := h.Mod.Shadowrocket.SetEnabled(bg(), id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := fetch(h, "GET", p); rec.Code != 404 {
		t.Errorf("disabled: %d", rec.Code)
	}
	if rec := fetch(h, "HEAD", p); rec.Code != 404 {
		t.Errorf("disabled HEAD: %d", rec.Code)
	}
	// disabling again records nothing
	if err := h.Mod.Shadowrocket.SetEnabled(bg(), id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Shadowrocket.SetEnabled(bg(), id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := fetch(h, "GET", p); rec.Code != 200 {
		t.Errorf("enabled: %d", rec.Code)
	}
	ev := h.Events("routing.shadowrocket_updated")
	if len(ev) != 2 || ev[0].Payload["changes"] != "disabled" || ev[1].Payload["changes"] != "enabled" {
		t.Errorf("events: %+v", ev)
	}
	if fetches(t, h) != 1 {
		t.Errorf("%d fetches recorded", fetches(t, h))
	}
	// Edit: another list and policy from the next fetch; an edit that changes nothing records nothing
	parents := h.List("Parents")
	if ch, err := h.Mod.Shadowrocket.Edit(bg(), id, parents, "Proxy-Group-A", "admin"); err != nil || !ch {
		t.Fatalf("edit: %v %v", ch, err)
	}
	if ch, err := h.Mod.Shadowrocket.Edit(bg(), id, parents, " Proxy-Group-A ", "admin"); err != nil || ch {
		t.Errorf("edit again: %v %v", ch, err)
	}
	ev = h.Events("routing.shadowrocket_updated")
	if len(ev) != 3 || ev[2].Payload["changes"] != "list, policy" || ev[2].Payload["to"] != "Parents" || ev[2].Payload["policy"] != "Proxy-Group-A" {
		t.Errorf("edit events: %+v", ev)
	}
	if body := fetch(h, "GET", p).Body.String(); !contains(body, `routing list "Parents"`) || contains(body, "anthropic.com") {
		t.Errorf("after the edit:\n%s", body)
	}
	// delete: the URL 404s, versions and fetches go
	if err := h.Mod.Shadowrocket.Delete(bg(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := fetch(h, "GET", p); rec.Code != 404 {
		t.Errorf("deleted: %d", rec.Code)
	}
	if fetches(t, h) != 0 {
		t.Error("fetches survived the delete")
	}
	if ev := h.Events("routing.shadowrocket_deleted"); len(ev) != 1 || ev[0].Payload["name"] != "iphone" {
		t.Errorf("deleted: %+v", ev)
	}
	if _, err := h.Mod.Shadowrocket.Get(bg(), id); !errors.Is(err, shadowrocket.ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func TestListDeleteMovesConfigs(t *testing.T) {
	h, _ := setup(t)
	parents := h.List("Parents")
	ipad, err := h.Mod.Shadowrocket.Create(bg(), shadowrocket.New{Name: "ipad", ListID: parents, Policy: "PROXY", Base: base}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := h.Mod.Lists.Targets(bg(), parents)
	if err != nil || len(ts) != 1 || ts[0].Kind != "shadowrocket" || ts[0].Name != "ipad" || ts[0].State != "enabled" || !ts[0].LastFetch.IsZero() {
		t.Fatalf("targets: %+v %v", ts, err)
	}
	if err := h.Mod.Lists.Delete(bg(), parents, 1, "admin"); err != nil {
		t.Fatal(err)
	}
	c, err := h.Mod.Shadowrocket.Get(bg(), ipad)
	if err != nil || c.ListID != 1 || c.List != "Main" {
		t.Errorf("moved: %+v %v", c, err)
	}
	ev := h.Events("routing.shadowrocket_updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "list" || ev[0].Payload["to"] != "Main" {
		t.Errorf("events: %+v", ev)
	}
	cs, _ := h.Mod.Shadowrocket.ByList(bg(), 1)
	if len(cs) != 2 {
		t.Errorf("Main's configs: %+v", cs)
	}
}
