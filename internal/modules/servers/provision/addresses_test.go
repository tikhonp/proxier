package provision_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// publishAlt adds a second endpoint bound to address 2: the template's files
// ignore addresses, which is fine for the endpoint test.
func publishAlt(h *serverstest.Harness) {
	h.PublishVersion(func(files map[string][]byte) {
		raw := string(files["manifest.yaml"])
		alt := `  - key: alt
    type: vless-xhttp-tls
    address: 2
    port: 443
    credential: "{{ .Gen.client_uuid }}"
    params:
      path: "{{ .Gen.xhttp_path }}"
      sni: "{{ .Address.Hostname }}"
      mode: stream-up
      fp: chrome
      alpn: h2
`
		files["manifest.yaml"] = []byte(strings.Replace(raw, "\nchecks:", "\n"+alt+"\nchecks:", 1))
	}, true)
}

func TestSecondAddressGetsItsEndpoint(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	publishAlt(h)

	f := h.Form()
	f.Addresses = []provision.FormAddress{{IP: "127.0.0.2", Hostname: "alt.example.org"}}
	id := h.Create(f)
	h.Drain()
	active(t, h, id)

	addrs, err := store.ServerAddresses(bg, h.App.DB.R, id)
	if err != nil || len(addrs) != 1 || addrs[0].IP != "127.0.0.2" || addrs[0].Hostname != "alt.example.org" || addrs[0].Position != 2 {
		t.Fatalf("addresses %+v %v", addrs, err)
	}
	eps, err := store.Endpoints(bg, h.App.DB.R, id)
	if err != nil || len(eps) != 2 {
		t.Fatalf("endpoints %+v %v", eps, err)
	}
	for _, e := range eps {
		if e.Key == "alt" && (e.Host != "alt.example.org" || e.Port != 443) {
			t.Errorf("alt endpoint %+v", e)
		}
	}
}

func TestAddingAndRemovingAnAddressRedeploys(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	publishAlt(h)
	id := h.Create(h.Form())
	h.Drain()
	active(t, h, id)
	if eps, _ := store.Endpoints(bg, h.App.DB.R, id); len(eps) != 1 || eps[0].Key != "main" {
		t.Fatalf("endpoints before: %+v", eps)
	}

	if err := h.Mod.Deploy.AddAddress(bg, id, "127.0.0.2", "alt.example.org", "admin"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{Force: true}, "", "admin"); err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	h.Drain()
	eps, err := store.Endpoints(bg, h.App.DB.R, id)
	if err != nil || len(eps) != 2 {
		t.Fatalf("endpoints after add %+v %v", eps, err)
	}

	addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id)
	if len(addrs) != 1 {
		t.Fatalf("addresses %+v", addrs)
	}
	if err := h.Mod.Deploy.RemoveAddress(bg, id, addrs[0].ID, "admin"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{Force: true}, "", "admin"); err != nil {
		t.Fatalf("redeploy after remove: %v", err)
	}
	h.Drain()
	if eps, _ := store.Endpoints(bg, h.App.DB.R, id); len(eps) != 1 || eps[0].Key != "main" {
		t.Fatalf("endpoints after remove: %+v", eps)
	}
	if addrs, _ := store.ServerAddresses(bg, h.App.DB.R, id); len(addrs) != 0 {
		t.Fatalf("addresses after remove: %+v", addrs)
	}
	if ev := h.Events("server.address_added"); len(ev) != 1 {
		t.Errorf("address_added events: %+v", ev)
	}
	if ev := h.Events("server.address_removed"); len(ev) != 1 {
		t.Errorf("address_removed events: %+v", ev)
	}
}
