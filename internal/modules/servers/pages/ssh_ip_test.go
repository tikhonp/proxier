package pages_test

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

func TestNewServerSeparateConnectionIP(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	page := h.Login.Get("/servers/new")
	mustContain(t, page.Body.String(), `name="ssh_ip"`, "Advanced IP configuration")
	f := h.Form()
	form := url.Values{
		"ip": {f.IP}, "ssh_ip": {"192.168.1.10"}, "ssh_port": {strconv.Itoa(f.SSHPort)},
		"root_password": {f.RootPassword}, "country": {h.Country},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	}
	rec := h.Login.Post("/servers/new/summary", form)
	mustContain(t, rec.Body.String(), "192.168.1.10", "A nl-1.hosts.proxier.test → "+f.IP)
	form.Set("country", "invalid")
	rec = h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("invalid form: %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), `name="ssh_ip" value="192.168.1.10"`)
	form.Set("country", h.Country)
	form.Set("ssh_ip", "not-an-ip")
	rec = h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("invalid management IP: %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Enter a valid IPv4 or IPv6 address for SSH")
	form.Set("ssh_ip", "192.168.1.10")
	h.Unreachable(f.IP, true)
	rec = h.Login.Post("/servers", form)
	if rec.Code != 303 {
		t.Fatalf("create: %d\n%s", rec.Code, rec.Body)
	}
	h.Drain()
	srvs, err := store.ListServers(bg, h.App.DB.R)
	if err != nil || len(srvs) != 1 || srvs[0].SSHIP != "192.168.1.10" || srvs[0].IP != f.IP || srvs[0].State != "active" {
		t.Fatalf("servers: %+v %v", srvs, err)
	}
}
