package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// everyMember lists the signed-in routes that every member reaches whatever
// their grant: their own sign-in, language and sessions, and the pages that
// show only what the grant reaches. Anything signed in that isn't here must
// check an area.
var everyMember = []string{
	"POST /logout",
	"GET /",
	"GET /search",
	"GET /settings",
	"GET /settings/general",
	"POST /settings/general/language",
	"GET /settings/security",
	"POST /settings/security/password",
	"POST /settings/security/totp/setup",
	"POST /settings/security/totp/enable",
	"POST /settings/security/totp/disable",
	"POST /settings/security/passkeys/options",
	"POST /settings/security/passkeys",
	"POST /settings/security/passkeys/:id/remove",
	"POST /settings/security/sessions/:id/sign-out",
	"POST /settings/security/sessions/sign-out-everywhere",
	"GET /settings/notifications",
	"POST /settings/notifications/:type",
	"POST /settings/notifications/telegram/detect",
	"POST /settings/notifications/telegram/chat",
	"POST /settings/notifications/telegram/test",
	"POST /notifications/:id/retry",
}

type route struct{ method, path string }

func (r route) String() string { return r.method + " " + r.path }

var params = regexp.MustCompile(`:[a-z_]+|\*`)

// url fills a route's parameters: a grant check comes before any of them is
// read.
func (r route) url() string { return params.ReplaceAllString(r.path, "1") }

// routes are the app's routes a browser can ask for.
func routes(t *testing.T, s *sitetest.Site) []route {
	t.Helper()
	var out []route
	for _, ri := range s.Handler.(*echo.Echo).Router().Routes() {
		switch ri.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			out = append(out, route{ri.Method, ri.Path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// do asks for r as l, through htmx: the refusal is then the fragment.
func do(l *sitetest.Login, r route) *httptest.ResponseRecorder {
	req := sitetest.Req{Method: r.method, Path: r.url(), Cookies: []*http.Cookie{l.Cookie},
		Header: http.Header{"X-Csrf-Token": {l.CSRF}, "Hx-Request": {"true"}}}
	if r.method != http.MethodGet {
		req.Form = map[string][]string{}
	}
	return l.Site.Do(req)
}

// signedIn reports whether a route sends a visitor without a session to sign in.
func signedIn(s *sitetest.Site, r route) bool {
	req := sitetest.Req{Method: r.method, Path: r.url()}
	if r.method != http.MethodGet {
		req.Form = map[string][]string{}
	}
	rec := s.Do(req)
	return rec.Code == http.StatusSeeOther && strings.HasPrefix(rec.Header().Get("Location"), "/login")
}

var forbiddenArea = regexp.MustCompile(`data-area="([a-z]+)"`)

// TestEverySignedInRouteChecksAnArea walks every route of the app with every
// module: a member with no access gets 403 from each signed-in route that
// isn't every member's, and the refusal names an area.
func TestEverySignedInRouteChecksAnArea(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: modules()})
	nobody := s.Join("nobody", access.Grant{}, "")

	areas := map[string]access.Area{}
	var open int
	for _, r := range routes(t, s) {
		if !signedIn(s, r) {
			open++
			continue
		}
		if slices.Contains(everyMember, r.String()) {
			continue
		}
		rec := do(nobody, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d for a member with no access, want 403 (or list it in everyMember)", r, rec.Code)
			continue
		}
		m := forbiddenArea.FindStringSubmatch(rec.Body.String())
		if m == nil || !access.Area(m[1]).Valid() {
			t.Errorf("%s: the refusal names no area: %q", r, rec.Body.String())
			continue
		}
		areas[r.String()] = access.Area(m[1])
	}
	all := map[string]bool{}
	for _, r := range routes(t, s) {
		all[r.String()] = true
	}
	for _, r := range everyMember {
		if !all[r] {
			t.Errorf("everyMember lists %s, which is no route", r)
		}
	}
	if open == 0 || len(areas) < 200 {
		t.Fatalf("walked %d open and %d guarded routes: the walk found too little", open, len(areas))
	}
	for _, a := range access.Areas() {
		n := 0
		for _, got := range areas {
			if got == a {
				n++
			}
		}
		if n == 0 {
			t.Errorf("no route checks %s", a)
		}
	}
}

// A member at servers: view sees the list of servers in service and nothing
// else of servers; one at routerscripts: own makes generations but doesn't
// write or publish scripts; one at routing: own changes no shared service
// and imports nothing.
func TestLowLevelsReachOnlyTheirRoutes(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{Modules: modules()})
	nobody := s.Join("nobody", access.Grant{}, "")
	low := s.Join("low", access.Grant{}.With(access.Servers, access.LevelView).With(access.RouterScripts, access.LevelOwn).
		With(access.Routing, access.LevelOwn), "")

	reached := map[access.Area][]string{}
	for _, r := range routes(t, s) {
		if !signedIn(s, r) || slices.Contains(everyMember, r.String()) {
			continue
		}
		m := forbiddenArea.FindStringSubmatch(do(nobody, r).Body.String())
		if m == nil {
			continue // the walk above reports it
		}
		a := access.Area(m[1])
		if a != access.Servers && a != access.RouterScripts && a != access.Routing {
			continue
		}
		if do(low, r).Code != http.StatusForbidden {
			reached[a] = append(reached[a], r.String())
		}
	}
	if want := []string{"GET /servers"}; !slices.Equal(reached[access.Servers], want) {
		t.Errorf("servers: view reaches %v, want %v", reached[access.Servers], want)
	}
	for _, r := range reached[access.RouterScripts] {
		writes := strings.HasPrefix(r, "POST ") && !strings.Contains(r, "/generate") && !strings.Contains(r, "/fetch-url")
		if writes || strings.HasSuffix(r, "/new") || strings.Contains(r, "/draft") || strings.Contains(r, "/publish") || strings.Contains(r, "/agent") {
			t.Errorf("routerscripts: own reaches %s, which needs manage_all", r)
		}
	}
	if len(reached[access.RouterScripts]) == 0 {
		t.Error("routerscripts: own reaches nothing")
	}
	shared := regexp.MustCompile(`/routing/import|/routing/services/:id/(switch|refresh)|^POST /routing/services/:id/snapshots/|/routing/lists/:id/refresh|^POST /routing/catalog|^POST /routing/services$`)
	for _, r := range reached[access.Routing] {
		if shared.MatchString(r) {
			t.Errorf("routing: own reaches %s, which needs manage_all", r)
		}
	}
	if !slices.Contains(reached[access.Routing], "POST /routing/lists") {
		t.Errorf("routing: own can't make a list: %v", reached[access.Routing])
	}
}
