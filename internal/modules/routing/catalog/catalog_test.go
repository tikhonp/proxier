package catalog_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

type entryRow struct {
	Source  string `db:"source"`
	Kind    string `db:"kind"`
	Name    string `db:"name"`
	Group   string `db:"grp"`
	Sites   int    `db:"sites"`
	Domains int    `db:"domains"`
}

func TestCatalogRows(t *testing.T) {
	h := routingtest.New(t)
	fixture(h)
	refreshCatalog(t, h)

	var entries []entryRow
	if err := h.App.DB.R.Select(&entries, `SELECT e.source, e.kind, e.name, e.grp, e.sites, e.domains FROM routing_catalog_entries e
		JOIN routing_catalog_sources s ON s.source = e.source AND s.generation = e.generation ORDER BY e.source, e.kind, e.name`); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, strings.Join([]string{e.Source, e.Kind, e.Name, e.Group, itoa(e.Sites), itoa(e.Domains)}, "|"))
	}
	want := []string{
		"iplist:beta|group|apple||1|1", "iplist:beta|site|apple-dns.net|apple|0|1",
		"iplist:main|group|apple||2|3", "iplist:main|group|video||1|2",
		"iplist:main|site|apple.com|apple|0|2", "iplist:main|site|itunes.com|apple|0|1", "iplist:main|site|youtube.com|video|0|2",
		"iplist:russia|group|media||1|1", "iplist:russia|site|kinopoisk.ru|media|0|1",
		"v2fly|list|apple||0|3", "v2fly|list|apple-cn||0|2", "v2fly|list|netflix||0|2", "v2fly|list|pineapple||0|1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("entries:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	var domains []string
	if err := h.App.DB.R.Select(&domains, `SELECT d.source || '|' || d.name || '|' || d.domain || '|' || d.exact || '|' || d.attrs
		FROM routing_catalog_domains d JOIN routing_catalog_sources s ON s.source = d.source AND s.generation = d.generation
		WHERE d.name IN ('apple', 'apple-cn', 'apple.com') ORDER BY 1`); err != nil {
		t.Fatal(err)
	}
	wantD := []string{
		"iplist:main|apple.com|apple.com|0|", "iplist:main|apple.com|icloud.com|0|",
		"v2fly|apple-cn|apple.cn|0|", "v2fly|apple|apple.com|0|", "v2fly|apple|icloud.com|0|", "v2fly|apple|www.apple.com|1|@cn",
	}
	if !slices.Equal(domains, wantD) {
		t.Errorf("domains:\n%s\nwant\n%s", strings.Join(domains, "\n"), strings.Join(wantD, "\n"))
	}
	var includes []string
	if err := h.App.DB.R.Select(&includes, `SELECT list || '>' || included || '|' || filter FROM routing_catalog_includes`); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(includes, []string{"apple-cn>apple|@cn"}) {
		t.Errorf("includes: %v", includes)
	}
	st, _ := h.Mod.Catalog.Status(bg)
	if st[0].Source != "v2fly" || st[0].Entries != 4 || st[1].Source != "iplist:main" || st[1].Entries != 5 {
		t.Errorf("status: %+v", st)
	}
	var rev string
	_ = h.App.DB.R.Get(&rev, `SELECT revision FROM routing_catalog_sources WHERE source = 'v2fly'`)
	if rev != "1111111111111111111111111111111111111111" {
		t.Errorf("revision %q", rev)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func fmtInt(n int) string { return strconv.Itoa(n) }

func TestLikelyCause(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", names("nf", 0, 212))
	h.Up.V2fly("netflix-cdn", names("nf", 0, 128))
	h.Up.V2fly("misc", names("nf", 128, 130))
	id := h.Upstream("v2fly:netflix")
	main := h.List("Main", id)
	refreshCatalog(t, h) // the catalog still has netflix with every name

	var removed []string
	for i := 0; i < 131; i++ {
		removed = append(removed, strings.TrimSpace(names("nf", i, i+1)))
	}
	all, _, err := h.Mod.Catalog.WhereAre(bg, removed, "")
	if err != nil || len(all) != 3 || all[0].Selector != "v2fly:netflix" || all[0].Names != 131 {
		t.Fatalf("without except: %+v %v", all, err)
	}
	hold, at, err := h.Mod.Catalog.WhereAre(bg, removed, "v2fly:netflix")
	if err != nil || len(hold) != 2 || hold[0].Selector != "v2fly:netflix-cdn" || hold[0].Names != 128 || hold[1].Names != 2 || at.IsZero() {
		t.Fatalf("holders: %+v %v %v", hold, at, err)
	}

	h.Up.V2fly("netflix", names("nf", 131, 212))
	if _, err := h.Mod.Refresh.Refresh(bg, id, false, "admin"); err != nil {
		t.Fatal(err)
	}
	body := h.Login.Get("/routing/services/" + fmtInt(int(id))).Body.String()
	for _, s := range []string{"Likely cause: 128 of the 131 removed names are now in v2fly:netflix-cdn (catalog of 8 Oct).",
		`href="/routing/services/add?lists=` + fmtInt(int(main)) + `&amp;selector=v2fly%3Anetflix-cdn"`, ">Add v2fly:netflix-cdn…<"} {
		if !contains(body, s) {
			t.Errorf("service page: no %q", s)
		}
	}
	w, _, _ := h.Mod.Refresh.Waiting(bg, id)
	if body := h.Login.Get("/routing/services/" + fmtInt(int(id)) + "/snapshots/" + fmtInt(int(w.ID))).Body.String(); !contains(body, "Likely cause: 128 of the 131") {
		t.Error("the diff page has no likely cause")
	}

	// no holder with at least half: no line
	h.Up.V2fly("netflix-cdn", names("nf", 0, 60))
	h.Up.Commit("3333333333333333333333333333333333333333")
	refreshCatalog(t, h)
	if body := h.Login.Get("/routing/services/" + fmtInt(int(id))).Body.String(); contains(body, "Likely cause") {
		t.Error("a likely cause under half")
	}
	// a URL service never has one
	u := h.Up.File("l.txt", names("u", 0, 40))
	uid := h.Upstream(u)
	h.Up.File("l.txt", names("u", 30, 40))
	_, _ = h.Mod.Refresh.Refresh(bg, uid, false, "admin")
	if body := h.Login.Get("/routing/services/" + fmtInt(int(uid))).Body.String(); contains(body, "Likely cause") || !contains(body, "held back") {
		t.Error("a URL service: no band or a likely cause")
	}
}
