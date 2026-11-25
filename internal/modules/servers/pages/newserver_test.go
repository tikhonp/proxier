package pages_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

// routedGuard covers every hostname under proxier.test, as a routing list
// holding that suffix would.
type routedGuard struct{ asked []string }

func (g *routedGuard) Covering(_ context.Context, hostnames []string) ([]servers.RoutedName, error) {
	g.asked = append(g.asked, hostnames...)
	var out []servers.RoutedName
	for _, h := range hostnames {
		if strings.HasSuffix(h, ".proxier.test") {
			out = append(out, servers.RoutedName{Hostname: h, Domain: "proxier.test", Service: "my-sites", List: "Main"})
		}
	}
	return out, nil
}

func TestNewServerRefusedWhenRouted(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	g := &routedGuard{}
	h.Mod.SetRouting(g)
	f := h.Form()
	form := url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "root_password": {f.RootPassword}, "country": {h.Country},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "param.letsencrypt_email": {"me@example.com"},
	}
	const want = "nl-1.hosts.proxier.test is covered by proxier.test (my-sites) in Main: Proxier&#39;s checks would go into the tunnel. Take it out of the list first."

	// the live summary says it while the admin types
	rec := h.Login.Post("/servers/new/summary", url.Values{
		"ip": {"203.0.113.24"}, "country": {h.Country},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	})
	mustContain(t, rec.Body.String(), want)

	rec = h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), want)
	if srvs, _ := store.ListServers(bg, h.App.DB.R); len(srvs) != 0 {
		t.Fatalf("created anyway: %+v", srvs)
	}
	if len(g.asked) == 0 || g.asked[len(g.asked)-1] != "nl-1.hosts.proxier.test" {
		t.Errorf("asked %v", g.asked)
	}

	// without a guard nothing is checked
	h.Mod.SetRouting(nil)
	rec = h.Login.Post("/servers", form)
	if rec.Code != 303 {
		t.Fatalf("without a guard: %d\n%s", rec.Code, rec.Body)
	}
}

func TestNewServerTemplateVersionRefresh(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	for i := 2; i <= 6; i++ {
		h.PublishVersion(func(files map[string][]byte) {
			files["site/index.html"] = append(files["site/index.html"], []byte("<!-- revision -->")...)
		}, true)
	}
	form := url.Values{
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {"0:1"}, "country": {"NL"},
	}
	// Switching templates replaces the stale dropdown with every version and
	// selects the new template's default, even when both templates have v1.
	rec := h.Login.Post("/servers/new/summary", form)
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	mustContain(t, body, `id="srv-ver"`, `hx-swap-oob="true"`, `name="params_for" value="`+sid(h.TemplateID)+`:6"`)
	for i := 1; i <= 6; i++ {
		mustContain(t, body, `<option value="`+strconv.Itoa(i)+`"`)
	}
	mustContain(t, body, `<option value="6" selected`)

	// Explicitly selecting an older version of the same template is kept.
	form.Set("params_for", sid(h.TemplateID)+":6")
	rec = h.Login.Post("/servers/new/summary", form)
	mustContain(t, rec.Body.String(), `<option value="1" selected`, `name="params_for" value="`+sid(h.TemplateID)+`:1"`)

	// Ordinary edits leave the dropdown and parameter inputs untouched.
	form.Set("params_for", sid(h.TemplateID)+":1")
	rec = h.Login.Post("/servers/new/summary", form)
	mustNotContain(t, rec.Body.String(), `id="srv-ver"`, `id="params"`)

	// Apply provisions the version selected in the refreshed dropdown.
	f := h.Form()
	form.Set("ip", f.IP)
	form.Set("root_password", f.RootPassword)
	form.Set("version", "6")
	form.Set("param.letsencrypt_email", "me@example.com")
	rec = h.Login.Post("/servers", form)
	if rec.Code != 303 {
		t.Fatalf("create: %d\n%s", rec.Code, rec.Body)
	}
	srvs, err := store.ListServers(bg, h.App.DB.R)
	if err != nil || len(srvs) != 1 {
		t.Fatalf("servers: %v, %v", srvs, err)
	}
	if srvs[0].TemplateVersion != 6 {
		t.Fatalf("provisioned version %d, want 6", srvs[0].TemplateVersion)
	}
}

// The hostname typed in the form is the one checked, shown and kept.
func TestNewServerCustomHostname(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	g := &routedGuard{}
	h.Mod.SetRouting(g)
	f := h.Form()
	form := url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "root_password": {f.RootPassword}, "country": {h.Country},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "hostname": {"vpn.proxier.test"},
	}
	const want = "vpn.proxier.test is covered by proxier.test (my-sites) in Main"
	rec := h.Login.Post("/servers/new/summary", url.Values{
		"ip": {"203.0.113.24"}, "country": {h.Country}, "hostname": {"vpn.proxier.test"},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	})
	mustContain(t, rec.Body.String(), want)

	rec = h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), want)
	mustContain(t, rec.Body.String(), `name="hostname" value="vpn.proxier.test"`)

	form.Set("hostname", "vpn")
	h.Mod.SetRouting(nil)
	rec = h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), "Enter a full DNS name such as vpn.example.com.")

	form.Set("hostname", "vpn.proxier.test")
	if rec = h.Login.Post("/servers", form); rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	h.Drain()
	if srvs, _ := store.ListServers(bg, h.App.DB.R); len(srvs) != 1 || srvs[0].ManagementHostname != "vpn.proxier.test" {
		t.Fatalf("servers: %+v", srvs)
	}
}

// A typed hostname outside the Cloudflare zones: the summary names the record
// to create and checks it on request; the IP and Retire dialogs say the
// record is the member's.
func TestNewServerHostnameByHand(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	f := h.Form()
	sum := url.Values{
		"ip": {f.IP}, "country": {h.Country}, "hostname": {"vpn.example.org"},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	}
	body := h.Login.Post("/servers/new/summary", sum).Body.String()
	mustContain(t, body, "A vpn.example.org → 127.0.0.1", "Create this record at your DNS provider", `data-action="srv.check_dns"`, `hx-vals='{"check_dns": "1"}'`)
	mustNotContain(t, body, "proxier:nl-1", "does not point")

	sum.Set("check_dns", "1")
	mustContain(t, h.Login.Post("/servers/new/summary", sum).Body.String(), "vpn.example.org does not point to 127.0.0.1 yet (ns.fake answered nothing)")
	h.SetRecord("vpn.example.org", "127.0.0.1")
	mustContain(t, h.Login.Post("/servers/new/summary", sum).Body.String(), "The record is in place: ns.fake answered 127.0.0.1.")

	h.SetRecord("vpn.example.org")
	form := url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "root_password": {f.RootPassword}, "country": {h.Country},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "hostname": {"vpn.example.org"},
	}
	rec := h.Login.Post("/servers", form)
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), "Create the record A vpn.example.org → 127.0.0.1 at your DNS provider and check again.")

	h.SetRecord("vpn.example.org", "127.0.0.1")
	if rec = h.Login.Post("/servers", form); rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	h.Drain()
	srvs, _ := store.ListServers(bg, h.App.DB.R)
	if len(srvs) != 1 || srvs[0].State != "active" {
		t.Fatalf("servers: %+v", srvs)
	}
	base := "/servers/" + sid(srvs[0].ID)
	mustContain(t, page(t, h, base+"/ip"), "Point vpn.example.org at the new IP at your DNS provider", "Checks that vpn.example.org points to the new IP")
	mustContain(t, page(t, h, base+"/retire"), "The record of vpn.example.org was made by hand at another DNS provider")
}

func TestNewServerAccessAndSetup(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())

	rec := h.Login.Get("/servers/new")
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	for _, want := range []string{`name="access"`, `value="password"`, `value="key"`, `name="ssh_user"`, `name="setup"`, `value="minimal"`} {
		mustContain(t, rec.Body.String(), want)
	}

	mustNotContain(t, rec.Body.String(), `name="skip_dns"`)

	// Key access needs no root password in the live summary.
	f := h.Form()
	rec = h.Login.Post("/servers/new/summary", url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "country": {h.Country},
		"access": {"key"}, "ssh_user": {"root"}, "setup": {"minimal"},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	})
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustNotContain(t, rec.Body.String(), "Enter the root password")

	// A bad account is refused on Create and nothing is created.
	rec = h.Login.Post("/servers", url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "country": {h.Country},
		"access": {"key"}, "ssh_user": {"not a user"}, "setup": {"minimal"},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "param.letsencrypt_email": {"me@example.com"},
	})
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), "Use an account name")
	if srvs, _ := store.ListServers(bg, h.App.DB.R); len(srvs) != 0 {
		t.Fatalf("created anyway: %+v", srvs)
	}
}

// fieldAttrs finds a rendered control or disclosure by one identifying attribute.
func fieldAttrs(t *testing.T, body, tag, key, value string) map[string]string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			t.Fatalf("missing %s with %s=%q", tag, key, value)
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data != tag {
				continue
			}
			attrs := map[string]string{}
			for _, a := range tok.Attr {
				attrs[a.Key] = a.Val
			}
			if attrs[key] == value {
				return attrs
			}
		}
	}
}

func TestNewServerShowsRelevantOptions(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	body := page(t, h, "/servers/new")
	if _, open := fieldAttrs(t, body, "details", "class", "srv-options")["open"]; open {
		t.Fatal("default setup options should be collapsed")
	}
	if _, disabled := fieldAttrs(t, body, "input", "name", "ssh_user")["disabled"]; !disabled {
		t.Fatal("password access must not submit an SSH account")
	}
	if _, disabled := fieldAttrs(t, body, "input", "name", "root_password")["disabled"]; disabled {
		t.Fatal("password access needs its password field")
	}

	if err := h.App.Settings.Set(bg, "admin", "servers", map[string]string{
		"servers.default_access": "key", "servers.default_setup": "minimal",
	}); err != nil {
		t.Fatal(err)
	}
	body = page(t, h, "/servers/new")
	if _, open := fieldAttrs(t, body, "details", "class", "srv-options")["open"]; !open {
		t.Fatal("deploy-only default must be visible")
	}
	if _, disabled := fieldAttrs(t, body, "input", "name", "root_password")["disabled"]; !disabled {
		t.Fatal("key access must not submit a root password")
	}
	if _, disabled := fieldAttrs(t, body, "input", "name", "ssh_user")["disabled"]; disabled {
		t.Fatal("key access needs its SSH account field")
	}

	// A setup error must remain visible even when Full normally collapses.
	f := h.Form()
	rec := h.Login.Post("/servers", url.Values{
		"ip": {f.IP}, "country": {h.Country}, "access": {"key"}, "ssh_user": {"root"},
		"setup": {"full"}, "template": {sid(h.TemplateID)}, "version": {"1"},
		"param.letsencrypt_email": {"me@example.com"},
	})
	if rec.Code != 422 {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	if _, open := fieldAttrs(t, rec.Body.String(), "details", "class", "srv-options")["open"]; !open {
		t.Fatal("setup validation error is hidden")
	}

	form := url.Values{
		"ip": {f.IP}, "country": {h.Country}, "access": {"key"}, "setup": {"minimal"},
		"hostname": {"vpn.example.org"}, "skip_dns": {"1"},
		"template": {sid(h.TemplateID)}, "version": {"1"},
	}
	rec = h.Login.Post("/servers/new/summary", form)
	if rec.Code != 200 {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	if _, checked := fieldAttrs(t, rec.Body.String(), "input", "name", "skip_dns")["checked"]; !checked {
		t.Fatal("live summary lost the selected DNS bypass")
	}
	if _, disabled := fieldAttrs(t, rec.Body.String(), "button", "data-action", "srv.check_dns")["disabled"]; !disabled {
		t.Fatal("record check should be disabled while verification is skipped")
	}
	form.Set("hostname", "vpn.proxier.test")
	// Additional addresses start collapsed and open on populated values or errors.
	body = page(t, h, "/servers/new")
	if strings.Contains(body, `<details class="srv-options" open><summary>Additional addresses`) {
		t.Fatal("empty additional addresses section should start collapsed")
	}
	rec = h.Login.Post("/servers", url.Values{
		"ip": {f.IP}, "country": {h.Country}, "access": {"key"}, "ssh_user": {"proxier"},
		"setup": {"full"}, "add_ip": {"not-an-ip"}, "add_host": {"alt.example.com"},
		"template": {sid(h.TemplateID)}, "version": {"1"},
	})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `<details class="srv-options" open><summary>Additional addresses`) {
		t.Fatalf("additional address error should open section: %d", rec.Code)
	}
	rec = h.Login.Post("/servers/new/summary", form)
	if rec.Code != 200 {
		t.Fatalf("%d: %s", rec.Code, rec.Body)
	}
	mustNotContain(t, rec.Body.String(), `name="skip_dns"`)
}
