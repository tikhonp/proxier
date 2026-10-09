package pages_test

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/scripts"
	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// genPost is the generate form for Dacha on v1: a new link in "Me", a new
// router through dacha-pi, lanNet 10.40.1.
func genPost() url.Values {
	return url.Values{
		"version": {"1"}, "router_name": {"Dacha"}, "link_mode": {"create"}, "link_sub": {"2"},
		"router_mode": {"new"}, "router_jump": {"dacha-pi:22"}, "router_port": {"22"}, "router_user": {"proxier"},
		"p.lanNet": {"10.40.1"},
	}
}

func contains(t *testing.T, name, page string, want ...string) {
	t.Helper()
	text := html.UnescapeString(page)
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("%s: no %q", name, w)
		}
	}
}

func TestGenerateFormPage(t *testing.T) {
	h := rscriptstest.New(t)
	id := h.Script("fresh-router", paramstest.Annotate(paramstest.WithEnd(paramstest.Today()), "subUrl", "@fill subscription-link"))
	h.Routers().Jumps = []routing.JumpHost{{Host: "dacha-pi", Port: 22, User: "admin", Tailnet: true}}
	old := h.Links().Add("Router — Old", "Family", "disabled")

	res := h.Login.Get("/router-scripts/1/generate")
	page := res.Body.String()
	if res.Code != http.StatusOK {
		t.Fatalf("form: %d", res.Code)
	}
	contains(t, "form", page, "v1 · current", `name="router_name"`, "Register for routing", "Register a new router", "Use a router already in Routing",
		"Don't register", "Main · default", "dacha-pi:22 as admin · tailnet", "Another jump host…", "Subscription link", "Create a link",
		"Use an existing link", "Type a URL", "Family · 3 servers", "Router — Old · Family · disabled", "# interface names",
		"from the link above", "192.168.89.2", `($containerNet . ".2")`, "10.230.1.1 · lanNet + .1", "Generate", "On generate")
	checkNoInline(t, "form", page)

	// the summary follows the fields and swaps the computed values and the host
	res = h.Login.Post("/router-scripts/1/generate/summary", genPost())
	sum := res.Body.String()
	contains(t, "summary", sum, "Create link Router — Dacha in Me", "Register router Dacha, awaiting setup, following Main, at 10.40.1.1 through dacha-pi",
		"Write fresh-router-Dacha-v1.rsc · 2 lines differ from v1: lanNet, subUrl", "Every value is a valid RouterOS literal.",
		"Proxier hasn't confirmed this jump host's key", "The file holds the router's subscription link", `id="cv-vpnGateway"`,
		`id="router-host-field" hx-swap-oob="true"`, "10.40.1.1 · lanNet + .1", `id="pf-subUrl" hx-swap-oob="true"`)
	if strings.Contains(sum, "<html") {
		t.Error("the summary is a whole page")
	}
	f := genPost()
	f.Set("link_mode", "existing")
	f.Set("link_id", strconv.FormatInt(old, 10))
	f.Set("router_mode", "existing")
	contains(t, "existing", h.Login.Post("/router-scripts/1/generate/summary", f).Body.String(), "Use link Router — Old",
		"The link is disabled: the router's mihomo gets the stub entry until you enable it.", "Router: Choose a router.")
	f.Set("link_mode", "type")
	f.Set("router_mode", "none")
	f.Set("lock.subUrl", "link")
	if s := h.Login.Post("/router-scripts/1/generate/summary", f).Body.String(); !strings.Contains(s, "Not registered for routing") || !strings.Contains(s, `id="pf-subUrl" hx-swap-oob`) {
		t.Error("type a URL: the subUrl field isn't swapped back")
	}

	// errors inline with 422, keeping what was typed
	f = genPost()
	f.Set("router_name", "")
	f.Set("p.lanNet", "10.99.9")
	f.Set("p.wanIface", "bad\"value")
	res = h.Login.Post("/router-scripts/1/generate", f)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused: %d", res.Code)
	}
	contains(t, "refused", res.Body.String(), `value="10.99.9"`, "Type the router's name", "Nothing was created")
	if n := len(h.Events("routerscript.generated")); n != 0 {
		t.Fatalf("%d generated", n)
	}

	// generated: the generation page
	res = h.Login.Post("/router-scripts/1/generate", genPost())
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/router-scripts/generations/1" {
		t.Fatalf("generate: %d %s", res.Code, res.Header().Get("Location"))
	}

	// archived or versionless scripts are refused
	if err := h.Mod.Scripts.Archive(ctx, id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if res := h.Login.Get("/router-scripts/1/generate"); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "archived") {
		t.Errorf("archived: %d", res.Code)
	}
	if res := h.Login.Post("/router-scripts/1/generate", genPost()); res.Code != http.StatusConflict {
		t.Errorf("archived post: %d", res.Code)
	}
	if _, err := h.Mod.Scripts.Create(ctx, scripts.New{Name: "empty", Body: "# PARAMETERS\n:local a \"1\"\n"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if res := h.Login.Get("/router-scripts/2/generate"); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "no version") {
		t.Errorf("versionless: %d", res.Code)
	}
}

// dacha generates Dacha (a new link in Family, a new router, lanNet
// 10.40.1, a secret image) through the service.
func dacha(t *testing.T, h *rscriptstest.Harness) int64 {
	t.Helper()
	id := h.Script("fresh-router", paramstest.Annotate(paramstest.Annotated(), "image", "@secret"))
	v, err := h.Mod.Generations.Form(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := v.Form
	f.RouterName, f.Values["lanNet"], f.Values["image"] = "Dacha", "10.40.1", "s3cr3t-image"
	f.Link.SubscriptionID = 1
	gid, err := h.Mod.Generations.Generate(ctx, id, f, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return gid
}

func TestGenerationPage(t *testing.T) {
	h := rscriptstest.New(t)
	dacha(t, h)
	res := h.Login.Get("/router-scripts/generations/1")
	page := res.Body.String()
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page: %d %q", res.Code, res.Header().Get("Cache-Control"))
	}
	contains(t, "page", page, "generation #1", "<h1>Dacha</h1>", "fresh-router v1 · generated", "by admin", "Download .rsc",
		`href="/router-scripts/1/generate?from=1"`, "Generate again…", `href="/routing/routers/1"`, "awaiting setup · Main",
		`href="/links/1"`, "Router — Dacha", "Family · active", "fresh-router-Dacha-v1.rsc", "4 lines differ from v1: lanNet, subUrl, image, proxierKey",
		"View changes", "Show all 19", "•••", "· secret", "Proxier's public key", "from the link", "Generated Dacha from fresh-router v1")
	if strings.Contains(page, "s3cr3t-image") || strings.Contains(page, "proxier.test/s/") {
		t.Error("a secret on the page")
	}
	changedAt, allAt := strings.Index(page, ">lanNet<"), strings.Index(page, "<details")
	if changedAt < 0 || allAt < 0 || changedAt > allAt || strings.Count(page, ">wanIface<") != 1 {
		t.Error("the changed values come first, every value in the details")
	}
	checkNoInline(t, "generation", page)

	res = h.Login.Get("/router-scripts/generations/1/changes")
	changes := res.Body.String()
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("changes: %d", res.Code)
	}
	contains(t, "changes", changes, `"10.40.1"`, `:local subUrl "•••"`, `:local image "•••"`, "secret values shown as •••")
	if strings.Contains(changes, "s3cr3t-image") || strings.Contains(changes, "proxier.test/s/") || strings.Contains(changes, "files....t") {
		t.Error("a secret in the changes")
	}
	checkNoInline(t, "changes", changes)
	if split := h.Login.Get("/router-scripts/generations/1/changes?view=split").Body.String(); !strings.Contains(split, "split") {
		t.Error("no split view")
	}

	// the router and the link as they are now
	h.Routers().SetState(1, "removed", time.Time{})
	h.Links().Delete(1)
	page = h.Login.Get("/router-scripts/generations/1").Body.String()
	contains(t, "gone", page, "Router — Dacha", "· deleted", "removed")
	if res := h.Login.Get("/router-scripts/generations/9"); res.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", res.Code)
	}
}

func TestDownloadNeedsASession(t *testing.T) {
	h := rscriptstest.New(t)
	gid := dacha(t, h)
	res := h.Site.Do(sitetest.Req{Path: "/router-scripts/generations/1/download"})
	if res.Code != http.StatusSeeOther || !strings.HasPrefix(res.Header().Get("Location"), "/login") {
		t.Fatalf("without a session: %d %s", res.Code, res.Header().Get("Location"))
	}
	before := len(h.Events(""))
	res = h.Login.Get("/router-scripts/generations/1/download")
	want, err := h.Mod.Generations.Body(ctx, gid)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || res.Body.String() != string(want) || !strings.Contains(res.Body.String(), "s3cr3t-image") {
		t.Fatalf("download: %d", res.Code)
	}
	if cd := res.Header().Get("Content-Disposition"); cd != `attachment; filename="fresh-router-Dacha-v1.rsc"` {
		t.Errorf("disposition %q", cd)
	}
	if res.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/plain; charset=utf-8") {
		t.Errorf("headers: %v", res.Header())
	}
	if after := len(h.Events("")); after != before {
		t.Errorf("the download recorded %d events", after-before)
	}
}

func TestGenerationsListed(t *testing.T) {
	h := rscriptstest.New(t)
	dacha(t, h)
	v, err := h.Mod.Generations.Form(ctx, 1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := v.Form
	f.RouterName, f.Router.Mode, f.Link.Mode = "Parents", generations.RouterNone, generations.LinkType
	if _, err := h.Mod.Generations.Generate(ctx, 1, f, "admin"); err != nil {
		t.Fatal(err)
	}
	f.RouterName, f.Router.Mode = "Office", generations.RouterNew
	if _, err := h.Mod.Generations.Generate(ctx, 1, f, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Routers().SetState(2, "active", time.Now())

	page := h.Login.Get("/router-scripts/1").Body.String()
	contains(t, "script page", page, `href="/router-scripts/generations/1"`, `href="/router-scripts/generations/3"`, "awaiting setup", "not registered",
		"Generate for a new router", `href="/router-scripts/1/generate"`)
	if strings.Index(page, ">Office<") > strings.Index(page, ">Dacha<") {
		t.Error("newest first")
	}
	if !strings.Contains(page, "st-unknown") || !strings.Contains(page, "st-ok") {
		t.Error("the routers' markers")
	}
	contains(t, "list", h.Login.Get("/router-scripts").Body.String(), "3 · 1 awaiting setup")
	contains(t, "version", h.Login.Get("/router-scripts/1/versions/1").Body.String(), "Generate from this version", `/router-scripts/1/generate?version=1`)

	lctx := i18n.WithLocalizer(context.Background(), h.App.I18n.Localizer(i18n.EN, nil))
	hits, err := h.Mod.Search(lctx, "DACH", 10)
	if err != nil || len(hits) != 1 || hits[0].Label != "Dacha" || hits[0].Meta != "generation · fresh-router v1" || hits[0].Href != "/router-scripts/generations/1" {
		t.Errorf("search: %+v %v", hits, err)
	}
	hits, _ = h.Mod.Search(lctx, "fresh", 10)
	if len(hits) != 2 || hits[1].Label != "Generate for a new router · fresh-router" || hits[1].Href != "/router-scripts/1/generate" {
		t.Errorf("the action: %+v", hits)
	}
	names, err := h.Mod.NameSubjects(lctx, "generation", []string{"1", "9"})
	if err != nil || len(names) != 1 || names["1"].Label != "Dacha · fresh-router v1" || names["1"].Href != "/router-scripts/generations/1" {
		t.Errorf("subjects: %+v %v", names, err)
	}
	if body := h.Login.Get("/activity?subject=generation:1").Body.String(); !strings.Contains(body, `href="/router-scripts/generations/1"`) {
		t.Error("Activity doesn't link the generation")
	}
}
