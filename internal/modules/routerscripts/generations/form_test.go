package generations_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
)

var bg = context.Background()

// keyLine is Proxier's public key as an authorized_keys line.
func keyLine(t *testing.T, h *rscriptstest.Harness) (line, fp string) {
	t.Helper()
	line, fp, err := h.App.SSH.PublicKey(bg)
	if err != nil {
		t.Fatal(err)
	}
	return line, fp
}

// form is the first view of a script's generate form.
func form(t *testing.T, h *rscriptstest.Harness, id int64) generations.View {
	t.Helper()
	v, err := h.Mod.Generations.Form(bg, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFormFromTodaysScript(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.Annotated())
	v := form(t, h, id)
	if len(v.Parsed.Groups) != 8 || len(v.Parsed.Params) != 19 {
		t.Fatalf("%d groups, %d parameters", len(v.Parsed.Groups), len(v.Parsed.Params))
	}
	if g := v.Parsed.Groups[7]; g.Heading != "Proxier's public key: the proxier user logs in with it" || len(g.Items) != 1 || g.Items[0].Name() != "proxierKey" {
		t.Errorf("the key's group: %+v", g)
	}
	if g := v.Parsed.Groups[1]; g.Heading != "interface names" {
		t.Errorf("headings: %+v", g)
	}
	// the 18 of today's script with their defaults
	want := map[string]string{"lanNet": "10.230.1", "wanIface": "ether1", "timeZone": "Europe/Moscow", "containerNet": "192.168.89", "dohIP": "8.8.8.8", "image": "registry-1.docker.io/wiktorbgu/mihomo-mikrotik:latest"}
	for k, d := range want {
		if v.Form.Values[k] != d {
			t.Errorf("%s = %q, want %q", k, v.Form.Values[k], d)
		}
	}
	if _, ok := v.Form.Values["subUrl"]; ok {
		t.Error("a secret field is prefilled")
	}
	line, _ := keyLine(t, h)
	locks := map[string]string{"proxierKey": "key", "subUrl": "link", "vpnList": "router", "dohForwarder": "router"}
	for k, src := range locks {
		if v.Locked[k] != src {
			t.Errorf("%s locked to %q, want %q", k, v.Locked[k], src)
		}
	}
	if len(v.Locked) != 4 {
		t.Errorf("locked: %+v", v.Locked)
	}
	if v.LockedValues["proxierKey"] != line || v.LockedValues["vpnList"] != "to_vpn_list" || v.LockedValues["dohForwarder"] != "vpn-doh" {
		t.Errorf("locked values: %+v", v.LockedValues)
	}
	if v.Plan.Computed["vpnGateway"] != "192.168.89.2" {
		t.Errorf("computed: %+v", v.Plan.Computed)
	}
	if v.Form.Link.Mode != generations.LinkCreate || v.Form.Router.Mode != generations.RouterNew || v.Form.Router.Port != 22 || v.Form.Router.User != "proxier" {
		t.Errorf("choices: %+v %+v", v.Form.Link, v.Form.Router)
	}
	if len(v.Subscriptions) != 2 || len(v.Lists) != 2 || !v.Lists[0].Default {
		t.Errorf("the ports' choices: %+v %+v", v.Subscriptions, v.Lists)
	}

	// without ports: no sections, the fill fields ordinary (subUrl still secret)
	h = rscriptstest.New(t, rscriptstest.NoPorts())
	id = h.Script("fresh-router", paramstest.Annotated())
	v = form(t, h, id)
	if v.Form.Link.Mode != "" || v.Form.Router.Mode != "" || len(v.Locked) != 1 || v.Locked["proxierKey"] != "key" {
		t.Errorf("without ports: %+v %+v %+v", v.Form.Link, v.Form.Router, v.Locked)
	}
	if v.Form.Values["vpnList"] != "to_vpn_list" {
		t.Errorf("vpnList: %q", v.Form.Values["vpnList"])
	}
	if _, ok := v.Form.Values["subUrl"]; ok {
		t.Error("subUrl is prefilled")
	}
}

func TestHostDefaultFromLanNet(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.Annotated())
	v := form(t, h, id)
	if v.Plan.Host != "10.230.1.1" || v.Plan.HostAuto != "10.230.1.1" {
		t.Fatalf("default host %q", v.Plan.Host)
	}
	f := v.Form
	f.Values["lanNet"] = "10.40.1"
	c, err := h.Mod.Generations.Check(bg, id, f)
	if err != nil || c.Plan.Host != "10.40.1.1" {
		t.Fatalf("following lanNet: %q %v", c.Plan.Host, err)
	}
	f.Router.Host = "192.168.5.1"
	if c, _ := h.Mod.Generations.Check(bg, id, f); c.Plan.Host != "192.168.5.1" || c.Plan.HostAuto != "10.40.1.1" {
		t.Fatalf("a typed host wins: %q", c.Plan.Host)
	}
	f.Router.Host, f.Values["lanNet"] = "", "lan"
	if c, _ := h.Mod.Generations.Check(bg, id, f); c.Plan.Host != "" || c.Plan.Errors["router.host"] != "generations.err.host" {
		t.Fatalf("no usable lanNet: %q %+v", c.Plan.Host, c.Plan.Errors)
	}

	// a version without lanNet needs a host
	other := h.Script("other", paramstest.Replace(paramstest.WithEnd(paramstest.Today()), `:local lanNet "10.230.1"`, `:local lanBase "10.230.1"`))
	v = form(t, h, other)
	if v.Plan.Host != "" || v.Plan.Errors["router.host"] != "generations.err.host" {
		t.Fatalf("without lanNet: %q %+v", v.Plan.Host, v.Plan.Errors)
	}
}
