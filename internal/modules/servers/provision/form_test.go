package provision_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/geoip"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
)

var bg = context.Background()

// refused returns the field errors of a Create that was refused.
func refused(t *testing.T, err error) provision.FieldErrors {
	t.Helper()
	var fe provision.FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe
}

// nothingCreated checks that a refused form left no server, no job, no event
// and did not use up a number.
func nothingCreated(t *testing.T, h *serverstest.Harness) {
	t.Helper()
	for what, q := range map[string]string{
		"servers": `SELECT count(*) FROM servers_servers`,
		"jobs":    `SELECT count(*) FROM jobs`,
		"events":  `SELECT count(*) FROM events WHERE type = 'server.created'`,
		"numbers": `SELECT last_number FROM servers_locations WHERE code = 'nl'`,
	} {
		var n int
		if err := h.App.DB.R.Get(&n, q); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("a refused form left %d in %s", n, what)
		}
	}
}

func TestIPAlreadyUsedIsRefused(t *testing.T) {
	h := serverstest.NewHarness(t)
	first := h.Create(h.Form())
	h.Drain()
	name := h.Server(first).Name

	f := h.Form()
	f.RootPassword = "another-password"
	_, err := h.Mod.Provision.Create(bg, f, "admin")
	fe := refused(t, err)
	if m := fe["ip"]; m.Key != "servers.err.ip_used" || m.Args["name"] != name {
		t.Fatalf("ip error: %+v", fe)
	}
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM servers_servers`); err != nil || n != 1 {
		t.Fatalf("%d servers after the refused form (%v)", n, err)
	}
	// The live summary says it too, before anything is posted.
	if s := h.Mod.Provision.Summarize(bg, f, false); s.Errors["ip"].Key != "servers.err.ip_used" {
		t.Errorf("summary errors: %+v", s.Errors)
	}
	// A retired server gives its address back.
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET state = 'retired', health = NULL`); err != nil {
		t.Fatal(err)
	}
	if s := h.Mod.Provision.Summarize(bg, f, false); len(s.Errors) != 0 {
		t.Errorf("a retired server still holds the IP: %+v", s.Errors)
	}
}

func TestFormRefusals(t *testing.T) {
	h := serverstest.NewHarness(t)
	set := func(section string, kv map[string]string) {
		t.Helper()
		if err := h.App.Settings.Set(bg, "admin", section, kv); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func() (undo func())
		edit  func(f *provision.Form)
		field string
		key   string
	}{
		{name: "archived template", field: "template", key: "servers.err.template_archived",
			setup: func() func() {
				if err := h.Mod.Templates.Archive(bg, h.TemplateID, true, "admin"); err != nil {
					t.Fatal(err)
				}
				return func() { _ = h.Mod.Templates.Archive(bg, h.TemplateID, false, "admin") }
			}},
		{name: "missing version", field: "version", key: "servers.err.version_missing", edit: func(f *provision.Form) { f.Version = 99 }},
		{name: "missing template", field: "template", key: "servers.err.template_missing", edit: func(f *provision.Form) { f.TemplateID = 4242 }},
		{name: "missing location", field: "location", key: "servers.err.location_missing", edit: func(f *provision.Form) { f.LocationID = 4242 }},
		{name: "no location", field: "location", key: "servers.err.location_required", edit: func(f *provision.Form) { f.LocationID = 0 }},
		{name: "ipv6", field: "ip", key: "servers.err.ip_v4", edit: func(f *provision.Form) { f.IP = "2001:db8::1" }},
		{name: "not an ip", field: "ip", key: "servers.err.ip_v4", edit: func(f *provision.Form) { f.IP = "example.com" }},
		{name: "no ip", field: "ip", key: "servers.err.ip_required", edit: func(f *provision.Form) { f.IP = "" }},
		{name: "ssh port", field: "ssh_port", key: "servers.err.ssh_port", edit: func(f *provision.Form) { f.SSHPort = 70000 }},
		{name: "no password", field: "root_password", key: "servers.err.password_required", edit: func(f *provision.Form) { f.RootPassword = "" }},
		{name: "bad email", field: "param.letsencrypt_email", key: "servers.err.param_email", edit: func(f *provision.Form) { f.Params["letsencrypt_email"] = "not an address" }},
		{name: "email with a quote", field: "param.letsencrypt_email", key: "servers.err.param_email", edit: func(f *provision.Form) { f.Params["letsencrypt_email"] = "a'b@example.com" }},
		{name: "email with a dollar", field: "param.letsencrypt_email", key: "servers.err.param_email", edit: func(f *provision.Form) { f.Params["letsencrypt_email"] = "a$b@example.com" }},
		{name: "email with a backslash", field: "param.letsencrypt_email", key: "servers.err.param_email", edit: func(f *provision.Form) { f.Params["letsencrypt_email"] = `a\b@example.com` }},
		{name: "email with a name", field: "param.letsencrypt_email", key: "servers.err.param_email", edit: func(f *provision.Form) { f.Params["letsencrypt_email"] = "Me <me@example.com>" }},
		{name: "uncovered hostname", field: "dns", key: "servers.err.not_covered",
			setup: func() func() {
				set("servers", map[string]string{"servers.hostname_pattern": "{location}-{number}.hosts.example.org"})
				return func() {
					set("servers", map[string]string{"servers.hostname_pattern": "{location}-{number}.hosts.tikhonnnnn.com"})
				}
			}},
		{name: "no cloudflare", field: "dns", key: "servers.err.no_cloudflare",
			setup: func() func() {
				set("cloudflare", map[string]string{"cloudflare.api_token": ""})
				return func() { set("cloudflare", map[string]string{"cloudflare.api_token": serverstest.CFToken}) }
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				defer c.setup()()
			}
			f := h.Form()
			if c.edit != nil {
				c.edit(&f)
			}
			_, err := h.Mod.Provision.Create(bg, f, "admin")
			fe := refused(t, err)
			if fe[c.field].Key != c.key {
				t.Fatalf("errors: %+v, want %s on %s", fe, c.key, c.field)
			}
			nothingCreated(t, h)
		})
	}

	// Every problem is reported at once.
	f := h.Form()
	f.IP, f.SSHPort, f.RootPassword = "nope", 0, ""
	f.Params["letsencrypt_email"] = "x y"
	fe := refused(t, func() error { _, err := h.Mod.Provision.Create(bg, f, "admin"); return err }())
	for _, field := range []string{"ip", "root_password", "param.letsencrypt_email"} {
		if _, ok := fe[field]; !ok {
			t.Errorf("no error on %s: %+v", field, fe)
		}
	}
	// A good address with a name part is not an address; plain ones pass.
	f = h.Form()
	f.Params["letsencrypt_email"] = "admin@example.com"
	if _, err := h.Mod.Provision.Create(bg, f, "admin"); err != nil {
		t.Fatalf("a valid email was refused: %v", err)
	}
}

func TestLiveSummary(t *testing.T) {
	h := serverstest.NewHarness(t)
	f := h.Form()
	f.RootPassword = "the-secret-root-password"
	s := h.Mod.Provision.Summarize(bg, f, true)
	if len(s.Errors) != 0 {
		t.Fatalf("errors: %+v", s.Errors)
	}
	if s.Name != "nl-1" || s.ManagementHostname != "nl-1.hosts.tikhonnnnn.com" || s.ProxyHostname != s.ManagementHostname {
		t.Errorf("name and hosts: %+v", s)
	}
	if want := "A nl-1.hosts.tikhonnnnn.com → 127.0.0.1, DNS only, TTL 60"; s.DNSRecord != want {
		t.Errorf("DNS record %q, want %q", s.DNSRecord, want)
	}
	if len(s.Endpoints) != 1 || s.Endpoints[0] != "🇳🇱 Netherlands 1" {
		t.Errorf("endpoints %v", s.Endpoints)
	}
	// The password is neither needed nor echoed, and nothing is reserved.
	if strings.Contains(fmt.Sprintf("%+v", s), "the-secret-root-password") {
		t.Error("the summary holds the root password")
	}
	f.RootPassword = ""
	if s := h.Mod.Provision.Summarize(bg, f, true); s.Errors["root_password"].Key != "" {
		t.Errorf("the summary asked for the password: %+v", s.Errors)
	}
	var last int
	if err := h.App.DB.R.Get(&last, `SELECT last_number FROM servers_locations WHERE code = 'nl'`); err != nil || last != 0 {
		t.Errorf("the summary used up a number: %d %v", last, err)
	}
	// "if created now": the next number follows the highest ever used.
	h.Create(h.Form())
	h.Drain()
	f = h.Form()
	f.IP = "127.0.0.9"
	if s := h.Mod.Provision.Summarize(bg, f, true); s.Name != "nl-2" {
		t.Errorf("next name %q", s.Name)
	}
	// Errors that need no network show while typing.
	f.IP = "999.1.1.1"
	if s := h.Mod.Provision.Summarize(bg, f, true); s.Errors["ip"].Key != "servers.err.ip_v4" {
		t.Errorf("errors %+v", s.Errors)
	}
}

func TestCountrySuggestion(t *testing.T) {
	h := serverstest.NewHarness(t)
	var calls atomic.Int32
	answer := "NL"
	geo := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(answer + "\n"))
	}))
	t.Cleanup(geo.Close)
	old := geoip.Client
	geoip.Client = geo.Client()
	t.Cleanup(func() { geoip.Client = old })
	setURL := func(u string) {
		t.Helper()
		if err := h.App.Settings.Set(bg, "admin", "servers", map[string]string{"servers.ip_country_url": u}); err != nil {
			t.Fatal(err)
		}
	}
	setURL(geo.URL + "/{ip}")

	f := h.Form()
	f.LocationID = 0
	s := h.Mod.Provision.Summarize(bg, f, false)
	if s.SuggestedLocation != h.LocationID || s.SuggestedCountry != "NL" {
		t.Fatalf("an untouched location is preselected from the IP: %+v", s)
	}
	// The answer is remembered for the same IP.
	h.Mod.Provision.Summarize(bg, f, false)
	if calls.Load() != 1 {
		t.Errorf("%d lookups for one IP", calls.Load())
	}
	// A location the admin chose is never overridden.
	if s := h.Mod.Provision.Summarize(bg, f, true); s.SuggestedLocation != 0 {
		t.Errorf("a chosen location was overridden: %+v", s)
	}
	// Several locations of the country: the first by code.
	other, err := h.Mod.Store.CreateLocation(bg, "aa", "Amsterdam", "NL", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if s := h.Mod.Provision.Summarize(bg, f, false); s.SuggestedLocation != other {
		t.Errorf("want the first by code (aa): %+v", s)
	}
	// A country with no location suggests nothing.
	answer = "DE"
	f.IP = "127.0.0.7"
	if s := h.Mod.Provision.Summarize(bg, f, false); s.SuggestedLocation != 0 {
		t.Errorf("DE has no location: %+v", s)
	}
	// With the lookup off, nothing is asked.
	before := calls.Load()
	setURL("")
	f.IP = "127.0.0.8"
	if s := h.Mod.Provision.Summarize(bg, f, false); s.SuggestedLocation != 0 || calls.Load() != before {
		t.Errorf("the lookup is off but %+v, %d calls", s, calls.Load()-before)
	}
}

func TestCreateStartsProvisioning(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.Close() // the job fails at preflight and keeps its sealed password
	f := h.Form()
	f.Notes = "from a test"
	f.Params["letsencrypt_email"] = "admin@example.com"
	id := h.Create(f)
	h.Drain()

	srv := h.Server(id)
	if srv.Name != "nl-1" || srv.Number != 1 || srv.State != "failed" || srv.TemplateVersion != 1 || srv.Notes != "from a test" ||
		srv.ManagementHostname != "nl-1.hosts.tikhonnnnn.com" || srv.ProxyHostname != srv.ManagementHostname || srv.IP != "127.0.0.1" {
		t.Fatalf("server: %+v", srv)
	}
	if !strings.Contains(srv.Params, "admin@example.com") {
		t.Errorf("params %s", srv.Params)
	}
	j := h.Job(id)
	if j.Type != "servers.provision" || j.ResourceKey != "server:"+strconv.FormatInt(id, 10) || j.CreatedBy != "admin" {
		t.Fatalf("job: %+v", j)
	}
	if strings.Contains(string(j.Payload), f.RootPassword) {
		t.Fatalf("the payload holds the password: %s", j.Payload)
	}
	// The password waits sealed inside the job, under its AAD, and nowhere in plain text.
	var raw []byte
	if err := h.App.DB.R.Get(&raw, `SELECT secrets FROM jobs WHERE id = ?`, j.ID); err != nil || len(raw) == 0 {
		t.Fatalf("no sealed secrets: %v", err)
	}
	if strings.Contains(string(raw), f.RootPassword) {
		t.Error("the password is stored in plain text")
	}
	if got := h.JobSecrets(j.ID)["root_password"]; got != f.RootPassword {
		t.Errorf("the sealed password reads back as %q", got)
	}
	// server.created is recorded once, with the address and the version.
	ev := h.Events("server.created")
	if len(ev) != 1 || ev[0].Payload["ip"] != "127.0.0.1" || fmt.Sprint(ev[0].Payload["template_version"]) != "1" || ev[0].Actor != "admin" ||
		ev[0].Subject.String() != "server:"+strconv.FormatInt(id, 10) {
		t.Fatalf("events: %+v", ev)
	}
	if strings.Contains(fmt.Sprint(ev[0].Payload), f.RootPassword) {
		t.Error("the event holds the password")
	}
}

func TestConcurrentCreatesGetDistinctNames(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.StopJobs() // only the names matter; no job connects anywhere
	const n = 5
	var wg sync.WaitGroup
	ids := make([]int64, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := h.Form()
			f.IP = fmt.Sprintf("127.0.0.%d", 10+i)
			id, err := h.Mod.Provision.Create(bg, f, "admin")
			if err != nil {
				t.Errorf("create %d: %v", i, err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		seen[h.Server(id).Name] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[fmt.Sprintf("nl-%d", i)] {
			t.Errorf("names %v: nl-%d is missing", seen, i)
		}
	}
	if len(seen) != n {
		t.Errorf("duplicate names: %v", seen)
	}
}

func TestNamesAreNeverReused(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.Close() // provisioning fails at preflight
	f := h.Form()
	first := h.Create(f)
	h.Drain()
	if h.Server(first).Name != "nl-1" || h.Server(first).State != "failed" {
		t.Fatalf("first: %+v", h.Server(first))
	}
	// Retired by hand until 1g builds the flow, which re-runs this through it.
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET state = 'retired' WHERE id = ?`, first); err != nil {
		t.Fatal(err)
	}
	h.StopJobs()
	f.IP = "127.0.0.2"
	second := h.Create(f)
	if got := h.Server(second).Name; got != "nl-2" {
		t.Fatalf("the next server in nl is %s, not nl-2", got)
	}
}
