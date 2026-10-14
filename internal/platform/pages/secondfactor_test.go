package pages_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/auth/authtest"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// code is what an authenticator app shows for a base32 secret at t.
func code(secret string, t time.Time) string {
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		panic(err)
	}
	m := hmac.New(sha1.New, raw)
	_ = binary.Write(m, binary.BigEndian, uint64(t.Unix()/30))
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

func field(t *testing.T, page, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no %s field in\n%s", name, page)
	}
	return m[1]
}

// turnOnApp sets the authenticator app up through Settings → Security and
// returns its secret.
func turnOnApp(t *testing.T, s *sitetest.Site, l *sitetest.Login) string {
	t.Helper()
	page := l.Post("/settings/security/totp/setup", url.Values{"password": {sitetest.Password}}).Body.String()
	secret := regexp.MustCompile(`class="totp-secret">([A-Z2-7]+)<`).FindStringSubmatch(page)
	if secret == nil {
		t.Fatalf("no secret shown:\n%s", page)
	}
	rec := l.Post("/settings/security/totp/enable", url.Values{"token": {field(t, page, "token")}, "code": {code(secret[1], s.Now)}})
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings/security?totp=on" {
		t.Fatalf("turn on: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	s.Advance(30 * time.Second)
	return secret[1]
}

func TestAuthenticatorAppSetupInSettings(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	page := l.Get("/settings/security").Body.String()
	for _, want := range []string{"Authenticator app", "Passkeys", `action="/settings/security/totp/setup"`, "Set up…"} {
		if !strings.Contains(page, want) {
			t.Errorf("Security lacks %q", want)
		}
	}
	rec := l.Post("/settings/security/totp/setup", url.Values{"password": {"wrong wrong wrong"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "The current password is wrong.") || strings.Contains(rec.Body.String(), `class="qr"`) {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	page = l.Post("/settings/security/totp/setup", url.Values{"password": {sitetest.Password}}).Body.String()
	if !strings.Contains(page, `class="qr"`) || !strings.Contains(page, `autocomplete="one-time-code"`) {
		t.Fatalf("setup page:\n%s", page)
	}
	token := field(t, page, "token")
	rec = l.Post("/settings/security/totp/enable", url.Values{"token": {token}, "code": {"000000"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Wrong code.") || field(t, rec.Body.String(), "token") != token {
		t.Fatalf("wrong code: %d", rec.Code)
	}
	checkNoInline(t, "setup", rec.Body.String())

	secret := regexp.MustCompile(`class="totp-secret">([A-Z2-7]+)<`).FindStringSubmatch(page)[1]
	if rec := l.Post("/settings/security/totp/enable", url.Values{"token": {token}, "code": {code(secret, s.Now)}}); rec.Code != 303 {
		t.Fatalf("turn on: %d", rec.Code)
	}
	page = l.Get("/settings/security?totp=on").Body.String()
	for _, want := range []string{"The authenticator app is on.", `action="/settings/security/totp/disable"`} {
		if !strings.Contains(page, want) {
			t.Errorf("after turning on, Security lacks %q", want)
		}
	}
	if rec := l.Post("/settings/security/totp/disable", url.Values{"password": {"wrong wrong wrong"}}); rec.Code != 422 {
		t.Errorf("turned off with a wrong password: %d", rec.Code)
	}
	if rec := l.Post("/settings/security/totp/disable", url.Values{"password": {sitetest.Password}}); rec.Header().Get("Location") != "/settings/security?totp=off" {
		t.Errorf("turn off: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSignInAsksForTheCode(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	secret := turnOnApp(t, s, s.SignIn(""))
	signIn := func() *httptest.ResponseRecorder {
		return s.Do(sitetest.Req{Method: "POST", Path: "/login",
			Form: url.Values{"username": {sitetest.Username}, "password": {sitetest.Password}, "next": {"/settings"}}})
	}
	rec := signIn()
	page := rec.Body.String()
	if rec.Code != 200 || len(rec.Result().Cookies()) != 0 || !strings.Contains(page, `action="/login/code"`) || !strings.Contains(page, "proxier manage reset-2fa") {
		t.Fatalf("password step: %d\n%s", rec.Code, page)
	}
	checkNoInline(t, "code step", page)
	pending := field(t, page, "pending")
	code1 := func(c string) *httptest.ResponseRecorder {
		return s.Do(sitetest.Req{Method: "POST", Path: "/login/code", Form: url.Values{"pending": {pending}, "code": {c}, "next": {"/settings"}}})
	}
	if rec := code1("000000"); rec.Code != 401 || !strings.Contains(rec.Body.String(), "Wrong code.") || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong code: %d", rec.Code)
	}
	rec = code1(code(secret, s.Now))
	if rec.Code != 303 || rec.Header().Get("Location") != "/settings" || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("right code: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	pending = field(t, signIn().Body.String(), "pending")
	s.Advance(auth.PendingTTL)
	if rec := code1(code(secret, s.Now)); rec.Code != 401 || !strings.Contains(rec.Body.String(), "The sign-in ran out.") || !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Errorf("expired: %d\n%s", rec.Code, rec.Body.String())
	}
}

const origin = "http://proxier.test"

// addPasskey adds a through Settings → Security as the page's script does.
func addPasskey(t *testing.T, l *sitetest.Login, a *authtest.Authenticator, name string) *httptest.ResponseRecorder {
	t.Helper()
	rec := l.Post("/settings/security/passkeys/options", url.Values{"name": {name}, "password": {sitetest.Password}})
	if rec.Code != 200 {
		t.Fatalf("options: %d %s", rec.Code, rec.Body.String())
	}
	var o struct {
		Token   string          `json:"token"`
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"token": o.Token, "credential": json.RawMessage(a.Create(o.Options))})
	return l.Site.Do(sitetest.Req{Method: "POST", Path: "/settings/security/passkeys", Body: body, ContentType: "application/json",
		Header: http.Header{"X-CSRF-Token": {l.CSRF}}, Cookies: []*http.Cookie{l.Cookie}})
}

func signInWithPasskey(t *testing.T, s *sitetest.Site, a *authtest.Authenticator, next string) *httptest.ResponseRecorder {
	t.Helper()
	rec := s.Do(sitetest.Req{Method: "POST", Path: "/login/passkey/options"})
	var o struct {
		Token   string          `json:"token"`
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &o); rec.Code != 200 || err != nil {
		t.Fatalf("options: %d %v", rec.Code, err)
	}
	body, _ := json.Marshal(map[string]any{"token": o.Token, "next": next, "credential": json.RawMessage(a.Get(o.Options))})
	return s.Do(sitetest.Req{Method: "POST", Path: "/login/passkey", Body: body, ContentType: "application/json"})
}

func TestPasskeyAddedInSettingsSignsIn(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	login := s.Do(sitetest.Req{Path: "/login"}).Body.String()
	if strings.Contains(login, "passkey.js") || strings.Contains(login, "data-passkey-signin") || strings.Contains(login, "webauthn") {
		t.Error("the sign-in page offers passkeys before one exists")
	}

	a := authtest.New(t, origin)
	if rec := addPasskey(t, l, a, "MacBook"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"redirect":"/settings/security?passkey=added"`) {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	page := l.Get("/settings/security?passkey=added").Body.String()
	for _, want := range []string{"Passkey added.", "MacBook", "never used", `action="/settings/security/passkeys/1/remove"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Security lacks %q", want)
		}
	}
	checkNoInline(t, "security with a passkey", page)

	login = s.Do(sitetest.Req{Path: "/login"}).Body.String()
	for _, want := range []string{"passkey.js", "data-passkey-signin", `autocomplete="username webauthn"`, "Sign in with a passkey"} {
		if !strings.Contains(login, want) {
			t.Errorf("the sign-in page lacks %q", want)
		}
	}
	checkNoInline(t, "login with passkeys", login)

	rec := signInWithPasskey(t, s, a, "/settings")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"redirect":"/settings"`) || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("sign in: %d %s", rec.Code, rec.Body.String())
	}
	if rec := signInWithPasskey(t, s, a, "//evil.example"); !strings.Contains(rec.Body.String(), `"redirect":"/"`) {
		t.Errorf("open redirect: %s", rec.Body.String())
	}

	if rec := l.Post("/settings/security/passkeys/1/remove", url.Values{"password": {"wrong wrong wrong"}}); rec.Code != 422 {
		t.Errorf("removed with a wrong password: %d", rec.Code)
	}
	if rec := l.Post("/settings/security/passkeys/1/remove", url.Values{"password": {sitetest.Password}}); rec.Header().Get("Location") != "/settings/security?passkey=removed" {
		t.Fatalf("remove: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = signInWithPasskey(t, s, a, "/")
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "That passkey wasn't accepted.") || len(rec.Result().Cookies()) != 0 {
		t.Errorf("removed passkey: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPasskeyFromAnotherOriginIsNotAdded(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	a := authtest.New(t, "http://evil.example")
	if rec := addPasskey(t, s.SignIn(""), a, "Key"); rec.Code != 422 {
		t.Errorf("status %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecondFactorChangesNeedTheCSRFToken(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	for _, path := range []string{
		"/settings/security/totp/setup", "/settings/security/totp/enable", "/settings/security/totp/disable",
		"/settings/security/passkeys/options", "/settings/security/passkeys", "/settings/security/passkeys/1/remove",
	} {
		rec := s.Do(sitetest.Req{Method: "POST", Path: path, Cookies: []*http.Cookie{l.Cookie}, Form: url.Values{"password": {sitetest.Password}}})
		if rec.Code != 403 {
			t.Errorf("%s without a token: %d", path, rec.Code)
		}
	}
}
