package generations_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// generate runs Generate and fails on any error.
func generate(t *testing.T, h *rscriptstest.Harness, id int64, f generations.Form) int64 {
	t.Helper()
	gid, err := h.Mod.Generations.Generate(bg, id, f, events.ActorAdmin)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return gid
}

// refused runs Generate and returns its field errors.
func refused(t *testing.T, h *rscriptstest.Harness, id int64, f generations.Form) store.FieldErrors {
	t.Helper()
	_, err := h.Mod.Generations.Generate(bg, id, f, events.ActorAdmin)
	var fe store.FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe
}

func count(t *testing.T, h *rscriptstest.Harness, q string) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, q); err != nil {
		t.Fatal(err)
	}
	return n
}

func body(t *testing.T, h *rscriptstest.Harness, gid int64) string {
	t.Helper()
	b, err := h.Mod.Generations.Body(bg, gid)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// version reads a version's body.
func version(t *testing.T, h *rscriptstest.Harness, id int64, n int) string {
	t.Helper()
	v, err := h.Mod.Scripts.Version(bg, id, n)
	if err != nil {
		t.Fatal(err)
	}
	return v.Body
}

// differing are the lines of b that differ from a (same line count).
func differing(t *testing.T, a, b string) []string {
	t.Helper()
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(al) != len(bl) {
		t.Fatalf("%d lines, want %d", len(bl), len(al))
	}
	var out []string
	for i := range al {
		if al[i] != bl[i] {
			out = append(out, bl[i])
		}
	}
	return out
}

func TestOnlyChangedLinesDiffer(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.WithEnd(paramstest.Today()))
	f := form(t, h, id).Form
	f.RouterName, f.Values["lanNet"], f.Router.Mode = "Dacha", "10.40.1", generations.RouterNone
	gid := generate(t, h, id, f)
	got := differing(t, version(t, h, id, 1), body(t, h, gid))
	if len(got) != 1 || got[0] != `:local lanNet "10.40.1"` {
		t.Fatalf("differing lines: %q", got)
	}
	g, err := h.Mod.Generations.Get(bg, gid)
	if err != nil || len(g.Changed) != 1 || g.Changed[0] != "lanNet" || g.FileName != "fresh-router-Dacha-v1.rsc" || g.Router != nil || g.RouterID != 0 {
		t.Fatalf("generation: %+v %v", g, err)
	}
	if ev := h.Events("routerscript.generated"); len(ev) != 1 || ev[0].Subject != generations.Subject(gid) || ev[0].Payload["router"] != "Dacha" ||
		ev[0].Payload["script"] != "fresh-router" || ev[0].Payload["registered"] != false || ev[0].Payload["link"] != "" {
		t.Fatalf("generated: %+v", ev)
	}
}

func TestValuesWrittenAsLiterals(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.WithEnd(paramstest.Today()))
	f := form(t, h, id).Form
	f.RouterName, f.Router.Mode, f.Values["image"] = "Dacha", generations.RouterNone, `a"b$c`
	gid := generate(t, h, id, f)
	if got := differing(t, version(t, h, id, 1), body(t, h, gid)); len(got) != 1 || got[0] != `:local image "a\"b\$c"` {
		t.Fatalf("the literal: %q", got)
	}

	f.Values["image"] = "a\nb"
	if fe := refused(t, h, id, f); fe["param.image"] != "params.err.control" {
		t.Fatalf("a newline: %+v", fe)
	}
	if n := count(t, h, `SELECT count(*) FROM rscripts_generations`); n != 1 {
		t.Fatalf("%d generations", n)
	}
	if n := len(h.Events("routerscript.generated")); n != 1 {
		t.Fatalf("%d events", n)
	}
}

// realForm is the annotated script's form for "Parents": a new link in the
// subscription and a new router at host.
func realForm(t *testing.T, m *rscriptstest.Modules, id, sub int64, host string) generations.Form {
	t.Helper()
	f := form(t, m.Harness, id).Form
	f.RouterName = "Parents"
	f.Link = generations.LinkChoice{Mode: generations.LinkCreate, SubscriptionID: sub}
	f.Router.Mode, f.Router.Host = generations.RouterNew, host
	return f
}

// linkRows and routerRows count the links and routers of the real modules.
func linkRows(t *testing.T, m *rscriptstest.Modules) int {
	t.Helper()
	rows, err := m.Subs.Links.List(bg, links.Filter{State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func routerRows(t *testing.T, m *rscriptstest.Modules) int {
	t.Helper()
	rows, err := m.Routing.Routers.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestRequiredValueRefusesEverything(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotate(paramstest.Annotated(), "wanIface", "@required"))
	f := realForm(t, m, id, m.Subscription("Family"), "10.230.3.1")
	f.Values["wanIface"] = "  "
	fe := refused(t, m.Harness, id, f)
	if fe["param.wanIface"] != "params.err.required" || len(fe) != 1 {
		t.Fatalf("errors: %+v", fe)
	}
	if linkRows(t, m) != 0 || routerRows(t, m) != 0 || count(t, m.Harness, `SELECT count(*) FROM rscripts_generations`) != 0 {
		t.Fatal("something was created")
	}
	for _, typ := range []string{"link.created", "routing.router_added", "routerscript.generated"} {
		if n := len(m.Events(typ)); n != 0 {
			t.Errorf("%d %s", n, typ)
		}
	}
}

func TestCreateLinkForTheRouter(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotated())
	fam := m.Subscription("Family")
	f := realForm(t, m, id, fam, "")
	f.Router.Mode = generations.RouterNone
	gid := generate(t, m.Harness, id, f)

	list, err := m.Mod.Generations.Get(bg, gid)
	if err != nil || list.Link == nil || list.Link.Name != "Router — Parents" || list.Link.Subscription != "Family" || list.Link.State != "active" || !list.LinkCreated {
		t.Fatalf("the link: %+v %v", list.Link, err)
	}
	token, err := m.Subs.Links.Token(bg, list.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	url := m.Subs.Links.URL(token)
	if !strings.Contains(body(t, m.Harness, gid), `:local subUrl "`+url+`"`) || list.LinkChanged {
		t.Fatalf("subUrl isn't the link's URL %s", url)
	}
	created, generated := m.Events("link.created"), m.Events("routerscript.generated")
	if len(created) != 1 || len(generated) != 1 || created[0].Actor != "admin" || generated[0].Payload["link"] != "Router — Parents" || generated[0].Payload["link_created"] != true {
		t.Fatalf("events: %+v %+v", created, generated)
	}
	// one transaction: nothing was recorded between the two
	if generated[0].ID != created[0].ID+1 || !generated[0].Time.Equal(created[0].Time.Time) {
		t.Fatalf("not one transaction: %d %d", created[0].ID, generated[0].ID)
	}
}

func TestRegisterForRouting(t *testing.T) {
	m := rscriptstest.WithModules(t)
	b := paramstest.Replace(paramstest.Annotated(), `:local vpnList "to_vpn_list"`, `:local vpnList "my_list"`)
	b = paramstest.Replace(b, `:local dohForwarder "vpn-doh"`, `:local dohForwarder "my-doh"`)
	id := m.Script("fresh-router", b)
	f := realForm(t, m, id, 0, "10.230.3.1")
	f.Link.Mode = generations.LinkType
	v, err := m.Mod.Generations.Check(bg, id, f)
	if err != nil || v.Locked["vpnList"] != "router" || v.LockedValues["vpnList"] != "to_vpn_list" || v.LockedValues["dohForwarder"] != "vpn-doh" {
		t.Fatalf("locked to the new router's names: %+v %v", v.LockedValues, err)
	}
	f.Values["vpnList"] = "typed_list" // a locked field's post is ignored
	gid := generate(t, m.Harness, id, f)

	g, err := m.Mod.Generations.Get(bg, gid)
	if err != nil || g.Router == nil || g.Router.Name != "Parents" || g.Router.State != "awaiting" || g.Router.List != "Main" || !g.RouterRegistered {
		t.Fatalf("the router: %+v %v", g.Router, err)
	}
	rt, err := m.Routing.Routers.Get(bg, g.RouterID)
	if err != nil || rt.CreatedBy != "routerscripts" || rt.Conn.Host != "10.230.3.1" {
		t.Fatalf("routing's router: %+v %v", rt, err)
	}
	if ev := m.Events("routing.router_added"); len(ev) != 1 || ev[0].Payload["by"] != "routerscripts" || ev[0].Actor != "admin" {
		t.Fatalf("router_added: %+v", ev)
	}
	got := body(t, m.Harness, gid)
	if !strings.Contains(got, `:local vpnList "to_vpn_list"`) || !strings.Contains(got, `:local dohForwarder "vpn-doh"`) {
		t.Fatal("the router's names don't win over the version's defaults")
	}
	for _, val := range g.Values {
		if (val.Name == "vpnList" || val.Name == "dohForwarder") && (val.Source != "router" || !val.Changed) {
			t.Errorf("%s: %+v", val.Name, val)
		}
	}
}

func TestPortRefusalsRollBackTogether(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotated())
	fam := m.Subscription("Family")
	m.Link("Router — Parents", fam)
	f := realForm(t, m, id, fam, "http://10.230.3.1")
	fe := refused(t, m.Harness, id, f)
	if fe["link.name"] != "generations.err.link_taken" || fe["router.host"] != "routers.err.host" {
		t.Fatalf("both refusals: %+v", fe)
	}
	if linkRows(t, m) != 1 || routerRows(t, m) != 0 || count(t, m.Harness, `SELECT count(*) FROM rscripts_generations`) != 0 || len(m.Events("routing.router_added")) != 0 {
		t.Fatal("something was created")
	}

	// the router port refuses while the link port accepts: still nothing
	f.Link.Name = "Router — Parents 2"
	fe = refused(t, m.Harness, id, f)
	if len(fe) != 1 || fe["router.host"] != "routers.err.host" {
		t.Fatalf("the router's refusal: %+v", fe)
	}
	if linkRows(t, m) != 1 || len(m.Events("link.created")) != 1 {
		t.Fatal("the link outlived the rollback")
	}
}

func TestGenerateAgainReusesLinkAndRouter(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotate(paramstest.Annotated(), "image", "@secret"))
	f := realForm(t, m, id, m.Subscription("Family"), "")
	f.Values["image"], f.Values["lanNet"] = "registry.example/mihomo:1", "10.230.3"
	first := generate(t, m.Harness, id, f)

	v, err := m.Mod.Generations.Form(bg, id, 0, first)
	if err != nil {
		t.Fatal(err)
	}
	g1, _ := m.Mod.Generations.Get(bg, first)
	if v.Form.RouterName != "Parents" || v.Form.Link.Mode != generations.LinkExisting || v.Form.Link.LinkID != g1.LinkID ||
		v.Form.Router.Mode != generations.RouterExisting || v.Form.Router.RouterID != g1.RouterID || v.Form.Values["lanNet"] != "10.230.3" {
		t.Fatalf("the form again: %+v", v.Form)
	}
	if _, ok := v.Form.Values["image"]; ok || !v.Kept["image"] {
		t.Fatalf("the secret: %+v", v.Kept)
	}
	second := generate(t, m.Harness, id, v.Form)
	if linkRows(t, m) != 1 || routerRows(t, m) != 1 {
		t.Fatal("created twice")
	}
	g2, err := m.Mod.Generations.Get(bg, second)
	if err != nil || g2.LinkID != g1.LinkID || g2.RouterID != g1.RouterID || g2.LinkCreated || g2.RouterRegistered || g2.RouterName != "Parents" {
		t.Fatalf("second: %+v %v", g2, err)
	}
	if b := body(t, m.Harness, second); !strings.Contains(b, `:local image "registry.example/mihomo:1"`) || b != body(t, m.Harness, first) {
		t.Fatal("the second file isn't the first's")
	}
	// a retyped secret wins
	v.Form.Values["image"] = "registry.example/mihomo:2"
	third := generate(t, m.Harness, id, v.Form)
	if !strings.Contains(body(t, m.Harness, third), `"registry.example/mihomo:2"`) {
		t.Fatal("the retyped secret")
	}
}

func TestWithoutPorts(t *testing.T) {
	h := rscriptstest.New(t, rscriptstest.NoPorts())
	id := h.Script("fresh-router", paramstest.Annotated())
	f := form(t, h, id).Form
	f.RouterName, f.Values["vpnList"], f.Values["subUrl"] = "Dacha", "kids_list", "https://example.org/sub"
	v, err := h.Mod.Generations.Check(bg, id, f)
	if err != nil || len(v.Plan.Errors) != 0 || v.Locked["vpnList"] != "" || v.Locked["subUrl"] != "" {
		t.Fatalf("ordinary fields: %+v %+v %v", v.Locked, v.Plan.Errors, err)
	}
	gid := generate(t, h, id, f)
	got := body(t, h, gid)
	if !strings.Contains(got, `:local vpnList "kids_list"`) || !strings.Contains(got, `:local subUrl "https://example.org/sub"`) {
		t.Fatal("typed values")
	}
	g, err := h.Mod.Generations.Get(bg, gid)
	if err != nil || g.Router != nil || g.Link != nil || g.RouterID != 0 || g.LinkID != 0 {
		t.Fatalf("generation: %+v %v", g, err)
	}
	for _, val := range g.Values {
		if val.Name == "subUrl" && (!val.Secret || val.Value != "" || val.Source != "") {
			t.Errorf("subUrl: %+v", val)
		}
	}
	if b := h.Login.Get("/router-scripts/1/generate").Body.String(); strings.Contains(b, "Register for routing") || strings.Contains(b, "Create a link") {
		t.Error("the page shows the sections")
	}
}

func TestProxierKeyFilled(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.Annotated())
	f := form(t, h, id).Form
	f.RouterName, f.Router.Mode, f.Link.Mode = "Dacha", generations.RouterNone, generations.LinkType
	f.Values["proxierKey"] = "ssh-ed25519 AAAAtyped"
	gid := generate(t, h, id, f)
	line, _ := keyLine(t, h)
	if !strings.Contains(body(t, h, gid), `:local proxierKey "`+line+`"`) {
		t.Fatal("the key isn't filled")
	}
	g, err := h.Mod.Generations.Get(bg, gid)
	if err != nil || g.KeyChanged || !g.HasKey {
		t.Fatalf("generation: %+v %v", g, err)
	}
	for _, val := range g.Values {
		if val.Name == "proxierKey" && (val.Secret || val.Value != line || val.Source != "key" || !val.Changed) {
			t.Errorf("proxierKey: %+v", val)
		}
	}
	if err := h.App.SSH.Regenerate(bg, "admin"); err != nil {
		t.Fatal(err)
	}
	if g, _ := h.Mod.Generations.Get(bg, gid); !g.KeyChanged {
		t.Fatal("the key changed")
	}
	if page := h.Login.Get("/router-scripts/generations/1").Body.String(); !strings.Contains(page, "Proxier&#39;s SSH key changed after this generation") {
		t.Error("no key-changed band")
	}
}

func TestCurrentVersionIsTheDefault(t *testing.T) {
	h := rscriptstest.New(t)
	base := paramstest.WithEnd(paramstest.Today())
	id := h.Script("fresh-router", base)
	for _, tz := range []string{"Europe/Berlin", "Europe/Paris", "Europe/Rome"} {
		h.Publish(id, bytes.Replace(base, []byte("Europe/Moscow"), []byte(tz), 1))
	}
	if err := h.Mod.Scripts.MakeCurrent(bg, id, 3, "admin"); err != nil {
		t.Fatal(err)
	}
	v := form(t, h, id)
	if v.Form.Version != 3 || v.Form.Values["timeZone"] != "Europe/Paris" || len(v.Versions) != 4 {
		t.Fatalf("form on %d (%s)", v.Form.Version, v.Form.Values["timeZone"])
	}
	f := v.Form
	f.RouterName, f.Router.Mode = "Dacha", generations.RouterNone
	gid := generate(t, h, id, f)
	if g, _ := h.Mod.Generations.Get(bg, gid); g.Version != 3 || g.Current != 3 || g.FileName != "fresh-router-Dacha-v3.rsc" {
		t.Fatalf("generation: %+v", g)
	}
	// another version on request; an archived script can't generate
	if v, err := h.Mod.Generations.Form(bg, id, 4, 0); err != nil || v.Form.Version != 4 {
		t.Fatalf("v4: %v", err)
	}
	if err := h.Mod.Scripts.Archive(bg, id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Generations.Form(bg, id, 0, 0); !errors.Is(err, generations.ErrCantGenerate) || !errors.Is(err, scripts.ErrArchived) {
		t.Fatalf("archived: %v", err)
	}
	if _, err := h.Mod.Generations.Generate(bg, id, f, "admin"); !errors.Is(err, scripts.ErrArchived) {
		t.Fatalf("archived generate: %v", err)
	}
}

func TestSecretsNeverInPlainText(t *testing.T) {
	var log bytes.Buffer
	h := rscriptstest.New(t, rscriptstest.LogTo(&log))
	id := h.Script("fresh-router", paramstest.Annotate(paramstest.Annotated(), "image", "@secret"))
	f := form(t, h, id).Form
	f.RouterName, f.Values["image"], f.Values["lanNet"] = "Dacha", "s3cr3t-image-value", "10.40.1"
	f.Link = generations.LinkChoice{Mode: generations.LinkCreate, SubscriptionID: 1}
	gid := generate(t, h, id, f)
	g, _ := h.Mod.Generations.Get(bg, gid)
	l, _ := h.Links().Link(bg, g.LinkID)
	if l.URL == "" {
		t.Fatal("no link URL")
	}
	h.Login.Get("/router-scripts/generations/1")
	h.Login.Get("/router-scripts/generations/1/changes")
	h.Login.Get("/router-scripts/generations/1/download")
	for _, secret := range []string{"s3cr3t-image-value", l.URL} {
		for _, q := range []string{
			`SELECT count(*) FROM rscripts_generations WHERE instr(vals || changed || router_name || file_name || link_name || key_fingerprint, ?) > 0`,
			`SELECT count(*) FROM events WHERE instr(payload, ?) > 0`,
			`SELECT count(*) FROM jobs WHERE instr(payload, ?) > 0`,
		} {
			var n int
			if err := h.App.DB.R.Get(&n, q, secret); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%q in: %s", secret, q)
			}
		}
		if strings.Contains(log.String(), secret) {
			t.Errorf("%q in the log", secret)
		}
		if strings.Contains(h.Login.Get("/router-scripts/generations/1").Body.String(), secret) ||
			strings.Contains(h.Login.Get("/router-scripts/generations/1/changes").Body.String(), secret) {
			t.Errorf("%q on a page", secret)
		}
	}
	var vals string
	if err := h.App.DB.R.Get(&vals, `SELECT vals FROM rscripts_generations WHERE id = ?`, gid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(vals, `"lanNet":"10.40.1"`) || !strings.Contains(vals, `"wanIface":"ether1"`) || strings.Contains(vals, "subUrl") || strings.Contains(vals, `"image"`) {
		t.Fatalf("vals: %s", vals)
	}
	if !strings.Contains(body(t, h, gid), "s3cr3t-image-value") || !strings.Contains(body(t, h, gid), l.URL) {
		t.Fatal("the file lacks its secrets")
	}
}

func TestLinkChangedAfterGeneration(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotated())
	f := realForm(t, m, id, m.Subscription("Family"), "")
	f.Router.Mode = generations.RouterNone
	gid := generate(t, m.Harness, id, f)
	g, _ := m.Mod.Generations.Get(bg, gid)
	if g.LinkChanged || g.LinkGone {
		t.Fatalf("fresh: %+v", g)
	}
	if err := m.Subs.Links.RegenerateToken(bg, g.LinkID, "admin"); err != nil {
		t.Fatal(err)
	}
	if g, _ = m.Mod.Generations.Get(bg, gid); !g.LinkChanged || g.LinkGone || g.Link == nil {
		t.Fatalf("regenerated: %+v", g)
	}
	page := m.Login.Get("/router-scripts/generations/1").Body.String()
	if !strings.Contains(page, "Router — Parents&#39;s URL changed after this generation") {
		t.Error("no URL-changed band")
	}
	if err := m.Subs.Links.Delete(bg, g.LinkID, "admin"); err != nil {
		t.Fatal(err)
	}
	if g, _ = m.Mod.Generations.Get(bg, gid); !g.LinkChanged || !g.LinkGone || g.Link != nil {
		t.Fatalf("deleted: %+v", g)
	}
	page = m.Login.Get("/router-scripts/generations/1").Body.String()
	if !strings.Contains(page, "URL changed after this generation") || !strings.Contains(page, ">deleted<") && !strings.Contains(page, "· deleted") {
		t.Error("no deleted link on the page")
	}
}
