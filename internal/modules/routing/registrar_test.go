package routing_test

import (
	"context"
	"errors"
	"html"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routerostest"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

var errRollback = errors.New("the caller rolls back")

// registration is a fake router's connection as the port takes it.
func registration(name string, r *routerostest.Router) routing.RouterRegistration {
	c := routingtest.Conn(r, nil)
	return routing.RouterRegistration{Name: name, Host: c.Host, Port: c.Port}
}

func TestRegisterRunsInTheCallersTransaction(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	port := h.Mod.RouterRegistrar()
	reg := routing.RouterRegistration{Name: "Parents", Host: "10.230.3.1"}

	err := h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := port.Register(ctx, tx, reg, "admin"); err != nil {
			t.Fatal(err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
	if list, _ := port.Routers(ctx); len(list) != 0 || len(h.Events("routing.router_added")) != 0 {
		t.Fatalf("a rolled-back router: %+v", list)
	}

	var got routing.RegisteredRouter
	if err := h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		got, err = port.Register(ctx, tx, reg, "admin")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Parents" || got.List != "Main" || got.State != "awaiting" || got.Host != "10.230.3.1:22" || got.Jump != "" ||
		got.AddressList != "to_vpn_list" || got.Forwarder != "vpn-doh" || !got.AwaitingUntil.Equal(h.Clock().Add(routers.AwaitingFor)) {
		t.Fatalf("registered: %+v", got)
	}
	rt, err := h.Mod.Routers.Get(ctx, got.ID)
	if err != nil || !rt.Awaiting() || rt.CreatedBy != "routerscripts" || rt.Conn.User != "proxier" {
		t.Fatalf("router: %+v %v", rt, err)
	}
	ev := h.Events("routing.router_added")
	if len(ev) != 1 || ev[0].Actor != "admin" || ev[0].Payload["by"] != "routerscripts" || ev[0].Payload["list"] != "Main" {
		t.Fatalf("router_added: %+v", ev)
	}

	// a taken name and a bad host, keyed as Add router's
	err = h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := port.Register(ctx, tx, routing.RouterRegistration{Name: "Parents", Host: "http://x", Port: 70000, ListID: 999}, "admin")
		return err
	})
	var fe routing.FieldErrors
	if !errors.As(err, &fe) || fe["name"] != "routers.err.name_taken" || fe["host"] != "routers.err.host" || fe["port"] != "routers.err.port" || fe["list"] != "routers.err.list" {
		t.Fatalf("field errors: %v", err)
	}
	if list, _ := port.Routers(ctx); len(list) != 1 || len(h.Events("routing.router_added")) != 1 {
		t.Fatalf("something was written: %+v", list)
	}
}

func TestRegistrarReads(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	port := h.Mod.RouterRegistrar()
	kids := h.List("Kids")
	h.List("Main")

	lists, err := port.Lists(ctx)
	if err != nil || len(lists) != 2 || lists[0].Name != "Main" || !lists[0].Default || lists[1].ID != kids || lists[1].Default {
		t.Fatalf("lists: %+v %v", lists, err)
	}

	// a pinned jump host (an active router through it) and an unpinned one
	_, viaJump := h.Router("Office", 0, routingtest.ViaJump())
	key := h.Key()
	fake := routerostest.New(t, key)
	var id int64
	if err := h.App.DB.Write(ctx, func(tx *sqlx.Tx) error {
		reg := registration("Dacha", fake)
		reg.JumpHost, reg.JumpPort, reg.JumpUser = "dacha-pi", 2222, "admin"
		reg.Tailnet = true
		r, err := port.Register(ctx, tx, reg, "admin")
		id = r.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	jumps, err := port.JumpHosts(ctx)
	if err != nil || len(jumps) != 2 {
		t.Fatalf("jump hosts: %+v %v", jumps, err)
	}
	if j := jumps[0]; j.Host != "127.0.0.1" || j.User != "pi" || !j.Pinned {
		t.Errorf("the office's jump host is pinned: %+v", j)
	}
	if j := jumps[1]; j.Host != "dacha-pi" || j.Port != 2222 || j.User != "admin" || !j.Tailnet || j.Pinned {
		t.Errorf("dacha-pi isn't: %+v", j)
	}

	// KeyCommands is the add page's text
	line, _, _ := h.App.SSH.PublicKey(ctx)
	r, err := port.Router(ctx, viaJump)
	if err != nil || r.KeyCommands != routeros.KeyCommands("proxier", line) || r.Jump == "" || r.State != "active" {
		t.Fatalf("office: %+v %v", r, err)
	}
	if body := h.Login.Get("/routing/routers/new").Body.String(); !strings.Contains(html.UnescapeString(body), r.KeyCommands) {
		t.Error("the add page shows other commands")
	}

	// the probe takes Dacha from awaiting to active (without its jump host:
	// a fake one isn't running there)
	h.Exec(`UPDATE routing_routers SET jump_host = '', jump_port = 22, jump_user = '', tailnet = 0 WHERE id = ?`, id)
	if r, _ := port.Router(ctx, id); r.State != "awaiting" || !r.ConnectedAt.IsZero() {
		t.Fatalf("awaiting: %+v", r)
	}
	h.StartJobs()
	h.RunSchedule(routers.JobProbeRound)
	h.Settle()
	r, err = port.Router(ctx, id)
	if err != nil || r.State != "active" || r.ConnectedAt.IsZero() {
		t.Fatalf("active after the probe: %+v %v", r, err)
	}

	// removing ones aren't listed; removed ones are gone
	h.Exec(`UPDATE routing_routers SET state = 'removing' WHERE id = ?`, viaJump)
	all, err := port.Routers(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "Dacha" {
		t.Fatalf("routers: %+v %v", all, err)
	}
	if _, err := h.Mod.Routers.Remove(ctx, id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := port.Router(ctx, id); !errors.Is(err, routing.ErrRouterNotFound) {
		t.Fatalf("removed: %v", err)
	}
}
