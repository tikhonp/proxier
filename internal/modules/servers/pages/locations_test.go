package pages_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

func TestLocationsPage(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	l := s.SignIn("")
	mod, _ := s.App.Module("servers")
	st := mod.(*servers.Module).Store

	// signed out, the page is behind the sign-in
	if rec := s.Do(sitetest.Req{Path: "/locations"}); rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound && rec.Code != 401 {
		t.Errorf("signed out: %d", rec.Code)
	}

	// empty
	body := l.Get("/locations").Body.String()
	for _, want := range []string{"No locations yet.", "New location", `name="code"`, `name="country"`, "Netherlands"} {
		if !strings.Contains(body, want) {
			t.Errorf("the empty page lacks %q", want)
		}
	}

	// a bad add shows inline errors and keeps what was typed
	rec := l.Post("/locations", url.Values{"code": {"N"}, "name": {"Holland"}, "country": {""}})
	body = rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "Use 2–5 lower-case letters, like nl.") || !strings.Contains(body, "Choose a country.") ||
		!strings.Contains(body, `value="Holland"`) || !strings.Contains(body, `value="N"`) {
		t.Fatalf("bad add: %d\n%s", rec.Code, body)
	}
	if locs, _ := st.Locations(t.Context()); len(locs) != 0 {
		t.Fatal("a refused add stored something")
	}

	// a good add redirects, and the list shows it with its flag and names
	rec = l.Post("/locations", url.Values{"code": {"nl"}, "name": {"Netherlands"}, "country": {"NL"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/locations?saved=created" {
		t.Fatalf("good add: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	body = l.Get("/locations?saved=created").Body.String()
	for _, want := range []string{"Location added.", "🇳🇱", ">nl<", ">Netherlands<", "none", "Edit", "Delete"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %q:\n%s", want, body)
		}
	}
	// the same code again
	rec = l.Post("/locations", url.Values{"code": {"nl"}, "name": {"Again"}, "country": {"NL"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "This code is already used.") {
		t.Errorf("a taken code: %d", rec.Code)
	}

	locs, _ := st.Locations(t.Context())
	id := itoa(locs[0].ID)

	// edit: the row turns into a form; a bad save keeps it open with the error
	body = l.Get("/locations?edit=" + id).Body.String()
	if !strings.Contains(body, `action="/locations/`+id+`"`) || !strings.Contains(body, `value="Netherlands"`) {
		t.Errorf("edit row:\n%s", body)
	}
	rec = l.Post("/locations/"+id, url.Values{"name": {""}, "country": {"NL"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Use 1–40 characters.") ||
		!strings.Contains(rec.Body.String(), `action="/locations/`+id+`"`) {
		t.Errorf("bad edit: %d", rec.Code)
	}
	rec = l.Post("/locations/"+id, url.Values{"name": {"Holland"}, "country": {"NL"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d", rec.Code)
	}
	if locs, _ = st.Locations(t.Context()); locs[0].Name != "Holland" || locs[0].Code != "nl" {
		t.Errorf("stored: %+v", locs[0])
	}
	if rec := l.Post("/locations/999", url.Values{"name": {"X"}, "country": {"NL"}}); rec.Code != 404 {
		t.Errorf("unknown id: %d", rec.Code)
	}

	// delete needs the CSRF token, and is refused while servers exist
	if rec := s.Do(sitetest.Req{Method: "POST", Path: "/locations/" + id + "/delete", Cookies: []*http.Cookie{l.Cookie}, Form: url.Values{}}); rec.Code != 403 {
		t.Errorf("delete without CSRF: %d", rec.Code)
	}
	if _, err := s.App.DB.W.ExecContext(t.Context(), `INSERT INTO servers_templates (id, slug, name, created_at) VALUES (50, 'x', 'X', '2026-10-07T00:00:00.000Z');
		INSERT INTO servers_servers (location_id, number, name, ip, management_hostname, proxy_hostname, state, template_id, template_version, created_at, retired_at)
		VALUES (`+id+`, 1, 'nl-1', '203.0.113.1', 'h', 'h', 'retired', 50, 1, '2026-10-07T00:00:00.000Z', '2026-10-07T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	rec = l.Post("/locations/"+id+"/delete", nil)
	body = rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, "has servers") {
		t.Errorf("delete with a retired server: %d", rec.Code)
	}
	// with a server the row offers no Delete button at all
	if strings.Contains(l.Get("/locations").Body.String(), "/locations/"+id+"/delete") {
		t.Error("a location with servers must not offer Delete")
	}
	// without servers it goes
	if _, err := s.App.DB.W.ExecContext(t.Context(), `DELETE FROM servers_servers`); err != nil {
		t.Fatal(err)
	}
	rec = l.Post("/locations/"+id+"/delete", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/locations?saved=deleted" {
		t.Fatalf("delete: %d", rec.Code)
	}
	if locs, _ = st.Locations(t.Context()); len(locs) != 0 {
		t.Errorf("still there: %+v", locs)
	}
	if rec := l.Post("/locations/"+id+"/delete", nil); rec.Code != 404 {
		t.Errorf("deleting twice: %d", rec.Code)
	}
}

func TestLocationsPageInRussian(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{servers.New()}})
	l := s.SignIn("")
	mod, _ := s.App.Module("servers")
	if _, err := mod.(*servers.Module).Store.CreateLocation(t.Context(), "nl", "Netherlands", "NL", "admin"); err != nil {
		t.Fatal(err)
	}
	// signed in, the admin's language applies
	l.Post("/settings/general/language", url.Values{"lang": {"ru"}})
	body := l.Get("/locations").Body.String()
	for _, want := range []string{"Локации", "Нидерланды", "Изменить", "Удалить"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Russian page lacks %q", want)
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
