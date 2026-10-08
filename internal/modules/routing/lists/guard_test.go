package lists_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
)

func TestGuardRefusesAdd(t *testing.T) {
	h := routingtest.New(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	id := h.Custom("hosts", "hosts.tikhonnnnn.com")
	ok := h.Custom("ok", "example.com")
	parents := h.List("Parents")
	before := len(h.Events(""))
	marks := len(h.Marks.Changes())

	err := h.Mod.Lists.Add(context.Background(), 1, []int64{ok, id}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" || ge.Hostname != "nl-1.hosts.tikhonnnnn.com" || ge.Domain != "hosts.tikhonnnnn.com" ||
		ge.Service != "hosts" || ge.List != "Main" {
		t.Fatalf("add: %v", err)
	}
	if got := order(t, h, 1); got != "" {
		t.Errorf("Main holds %s", got)
	}
	all := h.Events("")
	if len(all) != before+1 || all[len(all)-1].Type != "routing.list_refused_server_hostname" {
		t.Fatalf("events after the refusal: %+v", all[before:])
	}
	e := all[len(all)-1]
	if e.Subject.String() != "routing_list:1" || e.Payload["domain"] != "hosts.tikhonnnnn.com" || e.Payload["server"] != "nl-1" ||
		e.Payload["hostname"] != "nl-1.hosts.tikhonnnnn.com" || e.Payload["service"] != "hosts" {
		t.Errorf("refusal: %+v", e)
	}
	if len(h.Marks.Changes()) != marks {
		t.Error("a refused add marked")
	}
	// the list's warnings show it
	w, err := h.Mod.Lists.Warnings(ctx(h), 1)
	if err != nil || len(w) != 1 || w[0].Kind != "refused" || !strings.Contains(w[0].Text, "nl-1.hosts.tikhonnnnn.com") {
		t.Errorf("warnings: %+v %v", w, err)
	}
	if w, _ := h.Mod.Lists.Warnings(ctx(h), parents); len(w) != 0 {
		t.Errorf("Parents warnings: %+v", w)
	}
	// once the server is gone, the name is allowed
	h.Servers.Remove(1)
	if err := h.Mod.Lists.Add(context.Background(), 1, []int64{id}, "admin"); err != nil {
		t.Errorf("without the server: %v", err)
	}
}

func TestGuardRefusesCustomSave(t *testing.T) {
	h := routingtest.New(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	mine := h.Custom("mine", "example.com")
	h.List("Main", mine)
	parents := h.List("Parents", mine)

	_, err := h.Mod.Services.SaveCustom(context.Background(), mine, services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{
		{Domain: "example.com"}, {Domain: "tikhonnnnn.com"},
	}}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" || ge.Domain != "tikhonnnnn.com" || ge.List != "Main" || ge.Service != "mine" {
		t.Fatalf("save: %v", err)
	}
	rows, _ := h.Mod.Services.CustomRows(context.Background(), mine)
	if len(rows) != 1 || rows[0].Domain != "example.com" {
		t.Errorf("rows after the refusal: %+v", rows)
	}
	ev := h.Events("routing.list_refused_server_hostname")
	if len(ev) != 2 || ev[0].Subject.String() != "routing_list:1" || ev[1].Subject.String() != "routing_list:"+itoa(parents) {
		t.Errorf("refusals: %+v", ev)
	}
	// an exact name equal to another host is refused too; a sibling isn't
	h.Servers.Put(2, "de-1", "de-1.example.net")
	if _, err := h.Mod.Services.SaveCustom(context.Background(), mine, services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{
		{Domain: "de-1.example.net", Exact: true},
	}}, "admin"); !errors.As(err, &ge) || ge.Server != "de-1" {
		t.Errorf("exact: %v", err)
	}
	if _, err := h.Mod.Services.SaveCustom(context.Background(), mine, services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{
		{Domain: "api.tikhonnnnn.com"}, {Domain: "de-2.example.net", Exact: true},
	}}, "admin"); err != nil {
		t.Errorf("siblings: %v", err)
	}
	// a service in no list is never refused
	free := h.Custom("free", "tikhonnnnn.com")
	if rows, _ := h.Mod.Services.CustomRows(context.Background(), free); len(rows) != 1 {
		t.Errorf("free: %+v", rows)
	}
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n < 10 {
		return digits[n : n+1]
	}
	return itoa(n/10) + digits[n%10:n%10+1]
}

func TestGuardLeavesRefreshedNameOut(t *testing.T) {
	h := routingtest.New(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.tikhonnnnn.com")
	h.Up.V2fly("foo", "foo.com\nfull:api.tikhonnnnn.com\n")
	foo := h.Upstream("foo")
	other := h.Custom("other", "full:www.tikhonnnnn.com")
	main := h.List("Main", foo, other)

	// a refresh (3c) accepts a snapshot that brings tikhonnnnn.com
	h.Exec(`UPDATE routing_snapshots SET suffix = 'foo.com' || char(10) || 'tikhonnnnn.com', suffix_count = 2
		WHERE service_id = ? AND status = 'accepted'`, foo)
	v, err := h.Mod.Lists.View(context.Background(), main, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := v.Members[0].Owned
	if strings.Join(o.Suffix, " ") != "foo.com" || strings.Join(o.Exact, " ") != "api.tikhonnnnn.com" || len(o.Dropped) != 1 ||
		o.Dropped[0].Reason != "guarded" || o.Dropped[0].By != "nl-1" {
		t.Fatalf("foo: %+v", o)
	}
	// it covers nothing: other's exact name stays
	if got := owner(t, h, main, "www.tikhonnnnn.com", true); got != "other" {
		t.Errorf("www.tikhonnnnn.com owned by %q", got)
	}
	w, err := h.Mod.Lists.Warnings(ctx(h), main)
	if err != nil || len(w) != 1 || w[0].Kind != "guarded" ||
		w[0].Text != "v2fly:foo has tikhonnnnn.com, which covers nl-1.hosts.tikhonnnnn.com (nl-1): left out of what targets get." {
		t.Errorf("warnings: %+v %v", w, err)
	}
	if n := len(h.Events("routing.list_refused_server_hostname")); n != 0 {
		t.Errorf("a computed state recorded %d events", n)
	}
}

func TestCoveringForProvisioning(t *testing.T) {
	h := routingtest.New(t)
	sites := h.Custom("my-sites", "tikhonnnnn.com", "example.com")
	exact := h.Custom("exact", "full:nl-5.hosts.tikhonnnnn.com", "full:de-1.example.net")
	h.Up.V2fly("anthropic", "anthropic.com\n")
	first := h.Upstream("anthropic")
	h.List("Main", first, sites)
	h.List("Parents", exact)

	got, err := h.Mod.Guard().Covering(context.Background(), []string{"nl-3.hosts.tikhonnnnn.com", "de-1.example.net", "fi-1.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hostname != "nl-3.hosts.tikhonnnnn.com" || got[0].Domain != "tikhonnnnn.com" || got[0].Service != "my-sites" ||
		got[0].List != "Main" || got[1].Hostname != "de-1.example.net" || got[1].Domain != "de-1.example.net" || got[1].List != "Parents" {
		t.Fatalf("covering: %+v", got)
	}
	// exact names cover only an equal hostname; any listed name counts, even one not installed
	got, _ = h.Mod.Lists.Covering(context.Background(), []string{"nl-5.hosts.tikhonnnnn.com"})
	if len(got) != 1 || got[0].List != "Main" {
		t.Errorf("nl-5: %+v", got)
	}
	h.Exec(`DELETE FROM routing_list_services WHERE service_id = ?`, sites)
	got, _ = h.Mod.Lists.Covering(context.Background(), []string{"nl-5.hosts.tikhonnnnn.com", "x.nl-5.hosts.tikhonnnnn.com"})
	if len(got) != 1 || got[0].Service != "exact" || got[0].List != "Parents" || got[0].Hostname != "nl-5.hosts.tikhonnnnn.com" {
		t.Errorf("exact: %+v", got)
	}
}
