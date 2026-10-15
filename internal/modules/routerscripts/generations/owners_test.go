package generations_test

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var ownAll = access.Grant{}.With(access.RouterScripts, access.LevelOwn).With(access.Subscriptions, access.LevelOwn).
	With(access.Routing, access.LevelOwn)

func genPath(id int64) string { return "/router-scripts/generations/" + strconv.FormatInt(id, 10) }

// generateAs generates "Parents" for w with a new link in sub and no router.
func generateAs(t *testing.T, m *rscriptstest.Modules, w access.Who, id, sub int64) (int64, error) {
	t.Helper()
	v, err := m.Mod.Generations.Form(bg, w, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := v.Form
	f.RouterName, f.Router.Mode = "Parents "+strconv.FormatInt(w.Member, 10), generations.RouterNone
	f.Link = generations.LinkChoice{Mode: generations.LinkCreate, SubscriptionID: sub}
	return m.Mod.Generations.Generate(bg, w, id, f, events.MemberActor(w.Member))
}

// Router scripts offer a member only their own subscriptions, make the
// link and the generation theirs, and keep another member's generation out
// of reach: 404 at own, readable without changes at view_all.
func TestGenerationsBelongToWhoGenerates(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotated())
	anna := m.Site.Join("anna", ownAll, "") // member 2
	bob := m.Site.Join("bob", ownAll, "")   // member 3
	annaW, bobW := access.Who{Member: 2, Grant: ownAll}, access.Who{Member: 3, Grant: ownAll}
	annaSub, bobSub := m.SubscriptionOf(2, "Anna home"), m.SubscriptionOf(3, "Bob home")

	page := anna.Get("/router-scripts/" + strconv.FormatInt(id, 10) + "/generate").Body.String()
	if !strings.Contains(page, "Anna home") || strings.Contains(page, "Bob home") {
		t.Error("the generate form offers another member's subscription")
	}
	if _, err := generateAs(t, m, annaW, id, bobSub); !errors.As(err, new(store.FieldErrors)) {
		t.Fatalf("a link in bob's subscription: %v", err)
	}
	if n := len(m.Events("link.created")); n != 0 {
		t.Fatalf("%d links created from a refused generation", n)
	}
	gid, err := generateAs(t, m, annaW, id, annaSub)
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.Mod.Generations.Get(bg, gid, access.Owner(2))
	if err != nil || g.OwnerID != 2 {
		t.Fatalf("anna's generation: %+v %v", g, err)
	}
	if l, err := m.Subs.Links.Get(bg, g.LinkID, access.Owner(2)); err != nil || l.OwnerID != 2 {
		t.Errorf("the link it made isn't anna's: %v", err)
	}
	bobGen, err := generateAs(t, m, bobW, id, bobSub)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{genPath(gid), genPath(gid) + "/download", genPath(gid) + "/changes"} {
		if rec := bob.Get(path); rec.Code != http.StatusNotFound {
			t.Errorf("bob GET %s: %d, want 404", path, rec.Code)
		}
	}
	if rec := bob.Post(genPath(gid)+"/fetch-url", nil); rec.Code != http.StatusNotFound {
		t.Errorf("bob making a fetch URL for anna's generation: %d", rec.Code)
	}
	if rec := anna.Get("/router-scripts/" + strconv.FormatInt(id, 10) + "/generate?from=" + strconv.FormatInt(bobGen, 10)); rec.Code != http.StatusNotFound {
		t.Errorf("anna generating again from bob's: %d", rec.Code)
	}
	script := anna.Get("/router-scripts/" + strconv.FormatInt(id, 10)).Body.String()
	if !strings.Contains(script, "Parents 2") || strings.Contains(script, "Parents 3") {
		t.Error("the script page lists another member's generation")
	}

	viewer := m.Site.Join("vera", access.PresetViewer.Grant(), "")
	rec := viewer.Get(genPath(gid))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `data-action="rs.fetch_create"`) || strings.Contains(rec.Body.String(), `data-action="rs.gen_again"`) {
		t.Errorf("the viewer on anna's generation: %d, or offered a change", rec.Code)
	}
	if rec := viewer.Post(genPath(gid)+"/fetch-url", nil); rec.Code != http.StatusForbidden {
		t.Errorf("the viewer making a fetch URL: %d, want 403", rec.Code)
	}
	if n := len(m.Events("routerscript.fetch_url_created")); n != 0 {
		t.Errorf("%d fetch URLs made by others", n)
	}
	if !strings.Contains(anna.Get(genPath(gid)).Body.String(), `data-action="rs.fetch_create"`) {
		t.Error("anna isn't offered a fetch URL for her own")
	}
}

// A removed member's generations go to the heir; a second delivery moves
// nothing.
func TestRemovedMembersGenerationsGoToTheHeir(t *testing.T) {
	m := rscriptstest.WithModules(t)
	id := m.Script("fresh-router", paramstest.Annotated())
	m.Site.Join("anna", ownAll, "")
	gid, err := generateAs(t, m, access.Who{Member: 2, Grant: ownAll}, id, m.SubscriptionOf(2, "Anna home"))
	if err != nil {
		t.Fatal(err)
	}
	// The notifier asks who owns a generation: anna, then her heir.
	if owner, ok, err := m.Mod.Owner(bg, generations.Subject(gid)); err != nil || !ok || owner != 2 {
		t.Fatalf("owner of anna's generation: %d %v %v", owner, ok, err)
	}
	if _, ok, _ := m.Mod.Owner(bg, events.Subject{Type: "router_script", ID: strconv.FormatInt(id, 10)}); ok {
		t.Error("a script has an owner")
	}
	if _, err := m.App.Auth.RemoveMember(bg, 1, 2); err != nil {
		t.Fatal(err)
	}
	removed, err := events.List(bg, m.App.DB.R, events.Filter{Type: "member.removed"})
	if err != nil || len(removed) != 1 {
		t.Fatalf("member.removed: %v %v", removed, err)
	}
	handover := m.Mod.Generations.HandOverSubscriber()
	for range 2 {
		if err := m.App.DB.Write(bg, func(tx *sqlx.Tx) error { return handover.Handle(bg, tx, removed[0]) }); err != nil {
			t.Fatal(err)
		}
	}
	if ev := m.Events("routerscript.handed_over"); len(ev) != 1 || ev[0].Payload["generations"] != float64(1) || ev[0].Payload["from"] != "anna" {
		t.Fatalf("handed_over: %+v", ev)
	}
	if g, err := m.Mod.Generations.Get(bg, gid, access.Owner(1)); err != nil || g.OwnerID != 1 {
		t.Errorf("the generation isn't the heir's: %v", err)
	}
	if owner, ok, _ := m.Mod.Owner(bg, generations.Subject(gid)); !ok || owner != 1 {
		t.Errorf("the heir isn't told about the generation: %d %v", owner, ok)
	}
}
