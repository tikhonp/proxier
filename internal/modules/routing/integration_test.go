package routing_test

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
)

// custom makes a custom service with these suffix names on the real module.
func custom(t *testing.T, rt *routing.Module, tag string, names ...string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := rt.Services.CreateCustom(ctx, services.Custom{Name: tag, Tag: tag}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	var rows []services.DomainRow
	for _, n := range names {
		rows = append(rows, services.DomainRow{Domain: n})
	}
	if _, err := rt.Services.SaveCustom(ctx, id, services.Edit{Name: tag, Tag: tag, Rows: rows}, "admin"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRealProvisioningRefusedByList(t *testing.T) {
	h, rt := routingtest.WithServers(t)
	sites := custom(t, rt, "my-sites", "tikhonnnnn.com")
	if err := rt.Lists.Add(context.Background(), 1, []int64{sites}, "admin"); err != nil {
		t.Fatal(err)
	}
	// two servers of nl came and went: the next one is nl-3
	if _, err := h.App.DB.W.Exec(`UPDATE servers_locations SET last_number = 2 WHERE id = ?`, h.LocationID); err != nil {
		t.Fatal(err)
	}
	f := h.Form()
	_, err := h.Mod.Provision.Create(context.Background(), f, "admin")
	var fe provision.FieldErrors
	if !errors.As(err, &fe) || fe["routing"].Key != "servers.err.routed" || fe["routing"].Args["list"] != "Main" ||
		fe["routing"].Args["service"] != "my-sites" || fe["routing"].Args["host"] != "nl-3.hosts.tikhonnnnn.com" {
		t.Fatalf("create: %v", err)
	}
	rec := h.Login.Post("/servers", url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "root_password": {f.RootPassword}, "location": {strconv.FormatInt(h.LocationID, 10)},
		"template": {strconv.FormatInt(h.TemplateID, 10)}, "version": {"1"}, "param.letsencrypt_email": {"me@example.com"},
	})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "nl-3.hosts.tikhonnnnn.com is covered by tikhonnnnn.com (my-sites) in Main") {
		t.Fatalf("the form: %d", rec.Code)
	}

	// out of the list, the server can be made
	if err := rt.Lists.Remove(context.Background(), 1, sites, "admin"); err != nil {
		t.Fatal(err)
	}
	id := h.Create(f)
	h.Drain()
	if s := h.Server(id); s.Name != "nl-3" || s.State != "active" {
		t.Fatalf("server: %s %s %s", s.Name, s.State, s.FailedError)
	}
}

func TestRealGuardNamesServer(t *testing.T) {
	h, rt := routingtest.WithServers(t)
	nl1 := h.Provisioned()
	if s := h.Server(nl1); s.State != "active" {
		t.Fatalf("nl-1: %s", s.State)
	}
	mine := custom(t, rt, "mine", "example.com")
	if err := rt.Lists.Add(context.Background(), 1, []int64{mine}, "admin"); err != nil {
		t.Fatal(err)
	}
	_, err := rt.Services.SaveCustom(context.Background(), mine, services.Edit{Name: "mine", Tag: "mine", Rows: []services.DomainRow{
		{Domain: "example.com"}, {Domain: "tikhonnnnn.com"},
	}}, "admin")
	var ge *lists.GuardError
	if !errors.As(err, &ge) || ge.Server != "nl-1" || ge.Hostname != "nl-1.hosts.tikhonnnnn.com" || ge.List != "Main" {
		t.Fatalf("save: %v", err)
	}
	ev := h.Events("routing.list_refused_server_hostname")
	if len(ev) != 1 || ev[0].Payload["server"] != "nl-1" {
		t.Errorf("refusals: %+v", ev)
	}
	// the editor says so, naming nl-1
	rec := h.Login.Post("/routing/services/"+strconv.FormatInt(mine, 10)+"/edit", url.Values{
		"name": {"mine"}, "tag": {"mine"}, "rows": {"1"}, "domain.1": {"hosts.tikhonnnnn.com"}, "match.1": {"suffix"}, "note.1": {""},
	})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "the hostname of nl-1") {
		t.Errorf("editor: %d", rec.Code)
	}
}
