package pages_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

var bg = context.Background()

func sid(id int64) string { return strconv.FormatInt(id, 10) }

// provisioned makes an active server and returns the harness and its id.
func provisioned(t *testing.T) (*serverstest.Harness, int64) {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	id := h.Provisioned()
	if h.Server(id).State != "active" {
		t.Fatalf("not active: %s %s", h.Server(id).FailedStep, h.Server(id).FailedError)
	}
	return h, id
}

func page(t *testing.T, h *serverstest.Harness, path string) string {
	t.Helper()
	rec := h.Login.Get(path)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d\n%s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// excerpt keeps a failure readable: the middle of a page is the part that matters.
func excerpt(body string) string {
	if i := strings.Index(body, "<main"); i >= 0 {
		body = body[i:]
	}
	if j := strings.Index(body, "</main>"); j >= 0 {
		body = body[:j]
	}
	if len(body) > 1500 {
		body = body[:1500] + "…"
	}
	return body
}

func mustContain(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("the page lacks %q:\n%s", w, excerpt(body))
		}
	}
}

func mustNotContain(t *testing.T, body string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(body, w) {
			t.Errorf("the page has %q", w)
		}
	}
}

func TestServerPageStates(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())

	// Provisioning: the steps, the live log and Cancel; no Retry.
	reached, release := h.VPS.Hold(remote.OpDockerInstall)
	id := h.Create(h.Form())
	<-reached
	body := page(t, h, "/servers/"+sid(id))
	mustContain(t, body, "Provisioning", "Preflight", "Install access", "Base bootstrap", `data-stream="/jobs/`, `data-reload-on-done`, "Cancel provisioning", "nl-1", "127.0.0.1")
	mustNotContain(t, body, ">Retry<", "Activate anyway")
	// The log has the lines so far.
	mustContain(t, body, "Operating system debian-12")
	release()
	h.Drain()

	// Failed at the smoke test: step, error, Retry, Activate anyway, Retire.
	h2 := serverstest.NewHarness(t, serverstest.StubProxy())
	h2.StallSmoke(true)
	id2 := h2.Create(h2.Form())
	h2.Drain()
	body = page(t, h2, "/servers/"+sid(id2))
	mustContain(t, body, "Failed", "Stopped at Smoke test", "the proxy test through main failed", ">Retry<", "Activate anyway", "Retire", "disabled", "/servers/"+sid(id2)+"/retry", "/servers/"+sid(id2)+"/activate")
	mustNotContain(t, body, "Cancel provisioning")

	// Failed earlier: Activate anyway is not offered, and the handler refuses it.
	h3 := serverstest.NewHarness(t, serverstest.StubProxy())
	h3.VPS.ComposeUpFails = "port is already allocated"
	id3 := h3.Create(h3.Form())
	h3.Drain()
	body = page(t, h3, "/servers/"+sid(id3))
	mustContain(t, body, "Stopped at Install", "port is already allocated", ">Retry<")
	mustNotContain(t, body, "Activate anyway")
	if rec := h3.Login.Post("/servers/"+sid(id3)+"/activate", nil); rec.Code != 409 {
		t.Fatalf("activate after an install failure: %d", rec.Code)
	}
	if h3.Server(id3).State != "failed" {
		t.Error("the refused activation changed the server")
	}
	// Activate anyway after a smoke test failure works through the page.
	if rec := h2.Login.Post("/servers/"+sid(id2)+"/activate", nil); rec.Code != 303 {
		t.Fatalf("activate: %d\n%s", rec.Code, rec.Body)
	}
	body = page(t, h2, "/servers/"+sid(id2))
	mustContain(t, body, "Unknown", "Endpoints", "🇳🇱 Netherlands 1")
	mustNotContain(t, body, ">Retry<", "Activate anyway")

	// Active: identity, endpoints, notes, events, tabs.
	h4, id4 := provisioned(t)
	body = page(t, h4, "/servers/"+sid(id4))
	mustContain(t, body, "Unknown", "nl-1.hosts.tikhonnnnn.com", "VLESS XHTTP behind nginx", "v1", "Endpoints", "🇳🇱 Netherlands 1", "Reveal", "Notes",
		"/jobs?subject=server:"+sid(id4), "/activity?subject=server:"+sid(id4), "is active", "Recent events")
	mustNotContain(t, body, "Cancel provisioning", ">Retry<")
	// The password and the generated credentials are nowhere on the page.
	eps, _ := store.Endpoints(bg, h4.App.DB.R, id4)
	ep, _ := sealed.OpenEndpoint(h4.App.Vault, eps[0])
	mustNotContain(t, body, serverstest.DefaultRootPassword, ep.Credential, ep.Params["path"])

	if rec := h4.Login.Get("/servers/99999"); rec.Code != 404 {
		t.Errorf("a missing server: %d", rec.Code)
	}
}

func TestRetryPage(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	f := h.Form()
	f.RootPassword = "wrong-password"
	id := h.Create(f)
	h.Drain()
	body := page(t, h, "/servers/"+sid(id)+"/retry")
	mustContain(t, body, `name="root_password"`, "Retry nl-1", "authentication failed", "Keeps template version v1")
	mustNotContain(t, body, "overwrite_dns")
	// Without the password the form comes back with the error.
	rec := h.Login.Post("/servers/"+sid(id)+"/retry", nil)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Enter the root password.") {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	rec = h.Login.Post("/servers/"+sid(id)+"/retry", url.Values{"root_password": {serverstest.DefaultRootPassword}})
	if rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	h.Drain()
	if h.Server(id).State != "active" {
		t.Fatalf("not active: %s", h.Server(id).FailedError)
	}

	// A DNS conflict offers Overwrite with the stored error.
	h2 := serverstest.NewHarness(t, serverstest.StubProxy())
	h2.CF.AddRecord("tikhonnnnn.com", "nl-1.hosts.tikhonnnnn.com", "203.0.113.9", "")
	id2 := h2.Create(h2.Form())
	h2.Drain()
	body = page(t, h2, "/servers/"+sid(id2)+"/retry")
	mustContain(t, body, "overwrite_dns", "203.0.113.9", "Overwrite the existing DNS record")
	mustNotContain(t, body, `name="root_password"`)
	if rec := h2.Login.Post("/servers/"+sid(id2)+"/retry", url.Values{"overwrite_dns": {"1"}}); rec.Code != 303 {
		t.Fatalf("%d", rec.Code)
	}
	h2.Drain()
	if h2.Server(id2).State != "active" {
		t.Fatalf("not active: %s", h2.Server(id2).FailedError)
	}
	// An active server has nothing to retry: the page goes back to it.
	if rec := h2.Login.Get("/servers/" + sid(id2) + "/retry"); rec.Code != 303 {
		t.Errorf("retry form of an active server: %d", rec.Code)
	}
}

func TestEndpointURIReveal(t *testing.T) {
	h, id := provisioned(t)
	eps, _ := store.Endpoints(bg, h.App.DB.R, id)
	ep, err := sealed.OpenEndpoint(h.App.Vault, eps[0])
	if err != nil {
		t.Fatal(err)
	}
	body := page(t, h, "/servers/"+sid(id))
	mustContain(t, body, "vless://••••", "hx-get=\"/servers/"+sid(id)+"/endpoints/main/uri\"")
	mustNotContain(t, body, ep.Credential, ep.Params["path"], "vless://"+ep.Credential)

	rec := h.Login.Get("/servers/" + sid(id) + "/endpoints/main/uri")
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q", cc)
	}
	mustContain(t, rec.Body.String(), "vless://"+ep.Credential+"@nl-1.hosts.tikhonnnnn.com:443", "type=xhttp", "security=tls", "data-copy=", "Copy")
	if rec := h.Login.Get("/servers/" + sid(id) + "/endpoints/nope/uri"); rec.Code != 404 {
		t.Errorf("an unknown endpoint: %d", rec.Code)
	}
	// Revealing records nothing: it is not a change.
	if n := len(h.Events("server.created")) + len(h.Events("server.activated")); n != 2 {
		t.Errorf("events changed: %d", n)
	}
}

func TestEndpointQR(t *testing.T) {
	h, id := provisioned(t)
	rec := h.Login.Get("/servers/" + sid(id) + "/endpoints/main/qr")
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	mustContain(t, body, "<dialog", `data-autoopen`, "<svg", `class="qr"`, "🇳🇱 Netherlands 1", "QR code of the connection")
	mustNotContain(t, body, "style=", "<script")
	// the URI itself is only inside the drawing, not as text
	eps, _ := store.Endpoints(bg, h.App.DB.R, id)
	ep, _ := sealed.OpenEndpoint(h.App.Vault, eps[0])
	mustNotContain(t, body, ep.Credential)
}

func TestServerNotes(t *testing.T) {
	h, id := provisioned(t)
	post := func(notes string) int {
		return h.Login.Post("/servers/"+sid(id)+"/notes", url.Values{"notes": {notes}}).Code
	}
	if c := post("renews on the 5th\r\nplan: 1 vCPU"); c != 303 {
		t.Fatalf("%d", c)
	}
	if got := h.Server(id).Notes; got != "renews on the 5th\nplan: 1 vCPU" {
		t.Fatalf("notes %q", got)
	}
	ev := h.Events("server.notes_changed")
	if len(ev) != 1 || ev[0].Actor != "admin" || ev[0].Subject.String() != "server:"+sid(id) {
		t.Fatalf("events: %+v", ev)
	}
	// The same notes again change nothing and record nothing.
	if c := post("renews on the 5th\nplan: 1 vCPU"); c != 303 {
		t.Fatalf("%d", c)
	}
	if n := len(h.Events("server.notes_changed")); n != 1 {
		t.Fatalf("%d events after an unchanged save", n)
	}
	mustContain(t, page(t, h, "/servers/"+sid(id)+"?saved=notes"), "renews on the 5th", "Notes saved.")
	// Too long: refused, shown on the page, nothing saved.
	rec := h.Login.Post("/servers/"+sid(id)+"/notes", url.Values{"notes": {strings.Repeat("x", 10001)}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "at most 10 000") {
		t.Fatalf("%d", rec.Code)
	}
	if h.Server(id).Notes != "renews on the 5th\nplan: 1 vCPU" {
		t.Error("a refused save changed the notes")
	}
	// Clearing them is a change.
	post("")
	if n := len(h.Events("server.notes_changed")); n != 2 {
		t.Errorf("%d events after clearing", n)
	}
}

func TestNewLocationFromForm(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	rec := h.Login.Post("/servers/new/location", url.Values{
		"ip": {"203.0.113.24"}, "location": {sid(h.LocationID)}, "template": {sid(h.TemplateID)},
		"nl_code": {"de"}, "nl_name": {"Germany"}, "nl_country": {"DE"},
	})
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	var id int64
	if err := h.App.DB.R.Get(&id, `SELECT id FROM servers_locations WHERE code = 'de'`); err != nil {
		t.Fatalf("the location was not created: %v", err)
	}
	body := rec.Body.String()
	mustContain(t, body, `id="loc-field"`, `<option value="`+sid(id)+`" selected>`, "🇩🇪 de — Germany", `name="location_auto" value="0"`)
	mustNotContain(t, body, `<option value="`+sid(h.LocationID)+`" selected>`)

	// A refusal comes back inside the field, mini form open, nothing created.
	rec = h.Login.Post("/servers/new/location", url.Values{"nl_code": {"x"}, "nl_name": {""}, "nl_country": {""}, "location": {"0"}})
	if rec.Code != 422 {
		t.Fatalf("%d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "<details", "open", "role=\"alert\"")
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM servers_locations`); err != nil || n != 2 {
		t.Errorf("%d locations (%v)", n, err)
	}
}

func TestNewServerForm(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	body := page(t, h, "/servers/new")
	mustContain(t, body, `hx-post="/servers/new/summary"`, `hx-params="not root_password"`, `name="root_password"`, `type="password"`,
		"Will be created", "Let&#39;s Encrypt email", "VLESS XHTTP behind nginx", "v1 · default", "🇳🇱 nl — Netherlands", `name="location_auto"`)
	// The one place to choose from is preselected, and the name is known.
	mustContain(t, body, `selected>🇳🇱 nl`, "nl-1")

	// The live summary: name, host, record, endpoint; the password is not posted by the form
	// and is ignored when it is.
	rec := h.Login.Post("/servers/new/summary", url.Values{
		"ip": {"203.0.113.24"}, "root_password": {"must-not-be-read"}, "location": {sid(h.LocationID)}, "location_auto": {"0"},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {sid(h.TemplateID) + ":1"},
	})
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	s := rec.Body.String()
	mustContain(t, s, "nl-1", "nl-1.hosts.tikhonnnnn.com", "A nl-1.hosts.tikhonnnnn.com → 203.0.113.24", "Netherlands 1")
	mustNotContain(t, s, "must-not-be-read", `id="params"`) // same template and version: the fields stay as they are

	// Another version asks for its own fields, out of band.
	rec = h.Login.Post("/servers/new/summary", url.Values{
		"ip": {"203.0.113.24"}, "location": {sid(h.LocationID)}, "location_auto": {sid(h.LocationID)},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "params_for": {"0:0"},
	})
	mustContain(t, rec.Body.String(), `id="params"`, `hx-swap-oob="true"`, "Let&#39;s Encrypt email")

	// Create: refused with the fields marked and the password not echoed.
	rec = h.Login.Post("/servers", url.Values{"ip": {"nope"}, "location": {sid(h.LocationID)}, "template": {sid(h.TemplateID)}, "root_password": {"typed-secret"}})
	if rec.Code != 422 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	mustContain(t, rec.Body.String(), "Enter an IPv4 address")
	mustNotContain(t, rec.Body.String(), "typed-secret")
}

func TestCreateServerFromForm(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	f := h.Form()
	rec := h.Login.Post("/servers", url.Values{
		"ip": {f.IP}, "ssh_port": {strconv.Itoa(f.SSHPort)}, "root_password": {f.RootPassword}, "location": {sid(h.LocationID)},
		"template": {sid(h.TemplateID)}, "version": {"1"}, "notes": {"made by a test"}, "param.letsencrypt_email": {"me@example.com"},
	})
	if rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/servers/") {
		t.Fatalf("redirected to %q", loc)
	}
	h.Drain()
	srvs, _ := store.ListServers(bg, h.App.DB.R)
	if len(srvs) != 1 || srvs[0].State != "active" || srvs[0].Notes != "made by a test" {
		t.Fatalf("servers: %+v", srvs)
	}
	if !strings.Contains(srvs[0].Params, "me@example.com") {
		t.Errorf("params %s", srvs[0].Params)
	}
	// The password is nowhere in the page of the new server.
	mustNotContain(t, page(t, h, "/servers/"+sid(srvs[0].ID)), f.RootPassword)
}

func TestServersListSearchAndDashboard(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	// empty
	mustContain(t, page(t, h, "/servers"), "No servers yet.", "New server")
	h.StallSmoke(true)
	id := h.Create(h.Form())
	h.Drain()
	body := page(t, h, "/servers")
	mustContain(t, body, "nl-1", "Failed", "127.0.0.1", "nl-1.hosts.tikhonnnnn.com", "VLESS XHTTP behind nginx v1", `href="/servers/`+sid(id)+`"`)
	// search finds it by name, IP and hostname
	for _, q := range []string{"nl-1", "127.0.0", "hosts.tikh"} {
		mustContain(t, page(t, h, "/search?q="+url.QueryEscape(q)), `href="/servers/`+sid(id)+`"`)
	}
	// the dashboard names the failed server and its step
	dash := page(t, h, "/")
	mustContain(t, dash, "Servers", "nl-1", "Smoke test", `href="/servers/`+sid(id)+`"`)
	// retired servers are hidden until asked for
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET state = 'retired'`); err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, page(t, h, "/servers"), `href="/servers/`+sid(id)+`"`)
	mustContain(t, page(t, h, "/servers?retired=1"), `href="/servers/`+sid(id)+`"`)
}

func TestCancelProvisioningFromThePage(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	reached, release := h.VPS.Hold(remote.OpDockerInstall)
	id := h.Create(h.Form())
	<-reached
	rec := h.Login.Post("/servers/"+sid(id)+"/cancel", nil)
	if rec.Code != 303 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	release()
	h.Drain()
	srv := h.Server(id)
	if srv.State != "failed" || srv.FailedError != "cancelled" {
		t.Fatalf("server: %s %q at %q", srv.State, srv.FailedError, srv.FailedStep)
	}
	// Nothing runs any more, so a second cancel is refused.
	if rec := h.Login.Post("/servers/"+sid(id)+"/cancel", nil); rec.Code != 409 {
		t.Errorf("%d", rec.Code)
	}
}

func TestPreviewForAServer(t *testing.T) {
	h, id := provisioned(t)
	eps, _ := store.Endpoints(bg, h.App.DB.R, id)
	ep, _ := sealed.OpenEndpoint(h.App.Vault, eps[0])

	// the editor's picker lists the template's active servers
	editor := page(t, h, "/templates/"+sid(h.TemplateID)+"/edit")
	mustContain(t, editor, `name="server"`, `<option value="`+sid(id)+`">nl-1</option>`, "Sample server")

	d, err := h.Mod.Templates.Draft(bg, h.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"revision": {strconv.Itoa(d.Revision)}, "server": {sid(id)}}
	for p, b := range d.Files {
		form.Set("file["+p+"]", string(b))
	}
	rec := h.Login.Post("/templates/"+sid(h.TemplateID)+"/draft/preview", form)
	if rec.Code != 200 {
		t.Fatalf("%d\n%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	mustContain(t, body, "Rendered for nl-1 (127.0.0.1)", "Secrets are shown as •••", "Reveal secrets", "nl-1.hosts.tikhonnnnn.com", "•••")
	mustNotContain(t, body, ep.Credential, ep.Params["path"], "Sample server")

	// Reveal shows the real values.
	form.Set("reveal", "1")
	rec = h.Login.Post("/templates/"+sid(h.TemplateID)+"/draft/preview", form)
	body = rec.Body.String()
	mustContain(t, body, "with its secrets", ep.Credential)
	mustNotContain(t, body, "Reveal secrets")
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("a revealed preview may be cached: %q", rec.Header().Get("Cache-Control"))
	}

	// A server that does not exist, or is not active, is refused.
	form.Set("server", "4242")
	mustContain(t, h.Login.Post("/templates/"+sid(h.TemplateID)+"/draft/preview", form).Body.String(), "not an active server of this template")
}
