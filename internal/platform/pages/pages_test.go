package pages_test

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"golang.org/x/net/html"
)

func TestSignInFlow(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	form := func(pw, next string) url.Values {
		return url.Values{"username": {sitetest.Username}, "password": {pw}, "next": {next}}
	}
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/login", Form: form("nope nope nope", "")})
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "Wrong username or password.") || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec = s.Do(sitetest.Req{Method: "POST", Path: "/login", Form: form(sitetest.Password, "/settings/security")})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/security" {
		t.Fatalf("sign in: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = s.Do(sitetest.Req{Method: "POST", Path: "/login", Form: form(sitetest.Password, "https://evil.example/")})
	if rec.Header().Get("Location") != "/" {
		t.Errorf("open redirect: %q", rec.Header().Get("Location"))
	}
	// the dashboard shows the shell
	l := s.SignIn("")
	body := l.Get("/").Body.String()
	for _, want := range []string{"Dashboard", `class="hdr"`, `class="side pc-side"`, `id="keyline"`, "hx-headers"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
}

func TestSignInPageAnswersHead(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	rec := s.Do(sitetest.Req{Method: "HEAD", Path: "/login"})
	if rec.Code != 200 || rec.Header().Get("Location") != "" {
		t.Fatalf("HEAD /login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// an admin page still sends a HEAD to sign in, not to itself
	rec = s.Do(sitetest.Req{Method: "HEAD", Path: "/servers"})
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?next=%2Fservers" {
		t.Fatalf("HEAD /servers: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSignInLockoutPage(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	for range 5 {
		s.Do(sitetest.Req{Method: "POST", Path: "/login", Form: url.Values{"username": {"admin"}, "password": {"wrong wrong wrong"}}})
	}
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/login", Form: url.Values{"username": {"admin"}, "password": {sitetest.Password}}})
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "Too many attempts. Try again in 15 min.") {
		t.Fatalf("status %d\n%s", rec.Code, rec.Body.String())
	}
}

func TestSignInIgnoresRealIPFromUntrustedPeer(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/login", Addr: "203.0.113.9:555",
		Header: http.Header{"X-Real-IP": {"10.9.9.9"}},
		Form:   url.Values{"username": {"admin"}, "password": {sitetest.Password}}})
	if rec.Code != 303 {
		t.Fatalf("status %d", rec.Code)
	}
	var ip string
	if err := s.App.DB.R.GetContext(t.Context(), &ip, `SELECT created_ip FROM sessions`); err != nil {
		t.Fatal(err)
	}
	if ip != "203.0.113.9" {
		t.Errorf("session IP %q, want the socket address", ip)
	}

	// from a trusted peer the header counts
	s = sitetest.New(t, sitetest.Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}})
	s.Do(sitetest.Req{Method: "POST", Path: "/login", Addr: "203.0.113.9:555",
		Header: http.Header{"X-Real-IP": {"10.9.9.9"}},
		Form:   url.Values{"username": {"admin"}, "password": {sitetest.Password}}})
	_ = s.App.DB.R.GetContext(t.Context(), &ip, `SELECT created_ip FROM sessions`)
	if ip != "10.9.9.9" {
		t.Errorf("trusted peer: session IP %q", ip)
	}
}

func TestSignInPageLanguage(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	get := func(al string) string {
		return s.Do(sitetest.Req{Path: "/login", Header: http.Header{"Accept-Language": {al}}}).Body.String()
	}
	if !strings.Contains(get("ru-RU,ru;q=0.9"), "Вход") {
		t.Error("ru browser should see Russian")
	}
	if !strings.Contains(get("en-US"), "Sign in") {
		t.Error("en browser should see English")
	}
	// no match: the admin's language
	if err := s.App.Auth.SetLanguage(t.Context(), "ru"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(get("de"), "Вход") {
		t.Error("an unmatched browser should get the admin's language (ru)")
	}
	if !strings.Contains(get("en-US"), "Sign in") {
		t.Error("the browser's language wins over the admin's while signed out")
	}
	// signed in: the admin's language
	l := s.SignIn("")
	if !strings.Contains(l.Get("/", http.Header{"Accept-Language": {"en"}}).Body.String(), "Настройки") {
		t.Error("signed in, the admin's language applies")
	}
	// no admin at all: English
	n := sitetest.New(t, sitetest.Options{NoAdmin: true})
	if !strings.Contains(n.Do(sitetest.Req{Path: "/login", Header: http.Header{"Accept-Language": {"de"}}}).Body.String(), "Sign in") {
		t.Error("with no admin the page is English")
	}
}

func TestLanguageChangesInSettings(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	page := l.Get("/settings/general").Body.String()
	for _, want := range []string{`action="/settings/general/language"`, `<option value="en" selected>English</option>`, `<option value="ru">Русский</option>`} {
		if !strings.Contains(page, want) {
			t.Errorf("Settings → General lacks %s", want)
		}
	}
	rec := l.Post("/settings/general/language", url.Values{"lang": {"ru"}, "next": {"/settings/security"}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/general?language=saved" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	page = l.Get("/settings/general?language=saved").Body.String()
	for _, want := range []string{"Ваш язык", `<option value="ru" selected>Русский</option>`, "Сохранено."} {
		if !strings.Contains(page, want) {
			t.Errorf("after the change the page lacks %s", want)
		}
	}
	if a, _ := s.App.Auth.Admin(t.Context()); a.Language != "ru" {
		t.Errorf("admin language = %q", a.Language)
	}
	if rec := l.Post("/settings/general/language", url.Values{"lang": {"fr"}}); rec.Code != 400 {
		t.Errorf("unknown language: %d", rec.Code)
	}
}

// Settings → General is the only place that changes the language: the admin
// menu has no switch, so neither has the : pop-up, which lists the buttons
// on the page.
func TestNoLanguageSwitchOutsideSettings(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	for _, path := range []string{"/", "/settings/general", "/settings/security", "/jobs", "/activity"} {
		page := l.Get(path).Body.String()
		for _, bad := range []string{`data-action="me.language`, "/me/language", ">Русский</button>"} {
			if strings.Contains(page, bad) {
				t.Errorf("%s holds %s", path, bad)
			}
		}
		if !strings.Contains(page, `data-action="auth.sign_out"`) {
			t.Errorf("%s: the admin menu lost Sign out", path)
		}
	}
	if rec := l.Post("/me/language", url.Values{"lang": {"ru"}}); rec.Code == 303 {
		t.Errorf("the old switch still answers: %d", rec.Code)
	}
	if a, _ := s.App.Auth.Admin(t.Context()); a.Language != "en" {
		t.Error("the language changed outside Settings")
	}
}

// parse walks a page and fails on anything the CSP would block.
func checkNoInline(t *testing.T, name, page string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "style" {
				t.Errorf("%s: <style> element", name)
			}
			if n.Data == "script" {
				src := false
				for _, a := range n.Attr {
					if a.Key == "src" {
						src = true
					}
				}
				if !src {
					t.Errorf("%s: inline <script>", name)
				}
			}
			for _, a := range n.Attr {
				if a.Key == "style" || strings.HasPrefix(a.Key, "on") || strings.HasPrefix(a.Key, "hx-on") ||
					((a.Key == "href" || a.Key == "src") && strings.HasPrefix(strings.ToLower(a.Val), "javascript:")) {
					t.Errorf("%s: <%s %s=…>", name, n.Data, a.Key)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func TestPagesHaveNoInlineScriptOrStyle(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	s.SignIn("198.51.100.4:1")
	pages := map[string]string{
		"/": l.Get("/").Body.String(), "/settings/general": l.Get("/settings/general").Body.String(),
		"/settings/security": l.Get("/settings/security").Body.String(), "/404": l.Get("/nope").Body.String(),
		"/search": l.Get("/search?q=").Body.String(),
		"/login":  s.Do(sitetest.Req{Path: "/login?ended=idle&next=/settings"}).Body.String(),
	}
	for name, body := range pages {
		if len(body) < 50 {
			t.Errorf("%s: empty page", name)
		}
		checkNoInline(t, name, body)
	}
}

func TestSearchFindsPages(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	body := l.Get("/search?q=sec").Body.String()
	if !strings.Contains(body, `href="/settings/security"`) || !strings.Contains(body, "Security") || strings.Contains(body, "General") {
		t.Errorf("en:\n%s", body)
	}
	if all := l.Get("/search?q=").Body.String(); !strings.Contains(all, `href="/"`) || !strings.Contains(all, `href="/settings/general"`) {
		t.Errorf("empty query lists every page:\n%s", all)
	}
	_ = s.App.Auth.SetLanguage(t.Context(), "ru")
	if body := l.Get("/search?q=" + url.QueryEscape("безоп")).Body.String(); !strings.Contains(body, "Безопасность") {
		t.Errorf("ru:\n%s", body)
	}
}
