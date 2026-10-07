package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

const goodPassword = "correct horse battery"

type env struct {
	t     *testing.T
	s     *auth.Service
	db    *db.DB
	st    *settings.Store
	now   time.Time
	slept []time.Duration
	ctx   context.Context
}

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t)
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ev := events.NewCatalog()
	if err := ev.Declare(append(auth.Events, settings.ChangedEvent)...); err != nil {
		t.Fatal(err)
	}
	st := settings.New(d, v, ev)
	if err := st.Register(auth.SecuritySection, settings.Section{Name: "general", Module: "platform", Fields: []settings.Field{
		{Key: "general.language", Kind: settings.Enum, Default: "en", Options: []string{"en", "ru"}},
	}}); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: d, st: st, now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), ctx: context.Background()}
	e.s = auth.New(d, v, ev, st)
	e.s.Params = auth.Params{Memory: 1024, Time: 1, Threads: 1}
	e.s.Now = func() time.Time { return e.now }
	e.s.Sleep = func(_ context.Context, d time.Duration) { e.slept = append(e.slept, d) }
	if err := e.s.CreateAdmin(e.ctx, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.R.GetContext(e.ctx, &n, q, args...); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) events(typ string) int {
	return e.count(`SELECT COUNT(*) FROM events WHERE type = ?`, typ)
}

func (e *env) fail(ip string) error {
	_, err := e.s.SignIn(e.ctx, "admin", "wrong password!!", ip, "ua")
	return err
}

func (e *env) signIn(ip string) auth.SignInResult {
	e.t.Helper()
	r, err := e.s.SignIn(e.ctx, "admin", goodPassword, ip, "Mozilla/5.0 Chrome/120 Safari/537 (Macintosh)")
	if err != nil {
		e.t.Fatalf("sign in: %v", err)
	}
	return r
}

func TestWrongPasswordRecordsFailureAndNoSession(t *testing.T) {
	e := newEnv(t)
	if err := e.fail("1.1.1.1"); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("err = %v", err)
	}
	if e.events("auth.sign_in_failed") != 1 || e.count(`SELECT COUNT(*) FROM sessions`) != 0 {
		t.Error("want one failure event and no session")
	}
}

func TestUnknownUserLooksLikeWrongPassword(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.SignIn(e.ctx, "nobody", goodPassword, "1.1.1.1", "")
	if !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	_ = e.fail("1.1.1.2")
	if len(e.slept) != 2 || e.slept[0] != auth.FailureFloor || e.slept[1] != auth.FailureFloor {
		t.Errorf("both failures must be padded to the floor, slept %v", e.slept)
	}
}

func TestFourFailuresThenSuccessSignsIn(t *testing.T) {
	e := newEnv(t)
	for range 4 {
		_ = e.fail("1.1.1.1")
	}
	e.signIn("1.1.1.1")
	// the count restarts after a success
	for range 4 {
		_ = e.fail("1.1.1.1")
	}
	e.signIn("1.1.1.1")
}

func TestFifthFailureLocksTheIP(t *testing.T) {
	e := newEnv(t)
	for range 5 {
		_ = e.fail("1.1.1.1")
	}
	if e.events("auth.locked") != 1 {
		t.Fatal("want one auth.locked")
	}
	_, err := e.s.SignIn(e.ctx, "admin", goodPassword, "1.1.1.1", "")
	var le *auth.LockedError
	if !errors.As(err, &le) || !le.Until.After(e.now) {
		t.Fatalf("err = %v", err)
	}
	if e.events("auth.locked") != 1 {
		t.Error("a refused attempt must not lock again")
	}
}

func TestLockoutExpires(t *testing.T) {
	e := newEnv(t)
	for range 5 {
		_ = e.fail("1.1.1.1")
	}
	e.advance(15*time.Minute + time.Second)
	e.signIn("1.1.1.1")
}

func TestFailuresAreCountedPerIP(t *testing.T) {
	e := newEnv(t)
	for range 3 {
		_ = e.fail("1.1.1.1")
		_ = e.fail("2.2.2.2")
	}
	e.signIn("1.1.1.1")
	e.signIn("2.2.2.2")
}

func TestLockedAttemptsAreNotRecorded(t *testing.T) {
	e := newEnv(t)
	for range 5 {
		_ = e.fail("1.1.1.1")
	}
	before := e.count(`SELECT COUNT(*) FROM sign_in_attempts`)
	e.advance(time.Minute)
	_ = e.fail("1.1.1.1")
	if e.count(`SELECT COUNT(*) FROM sign_in_attempts`) != before {
		t.Error("an attempt during a lockout was recorded")
	}
	e.advance(14*time.Minute + 5*time.Second) // 15 min after the 5th failure, not after the refused one
	e.signIn("1.1.1.1")
}

func TestLockoutFollowsSettings(t *testing.T) {
	e := newEnv(t)
	if err := e.st.Set(e.ctx, "admin", "security", map[string]string{"security.lockout_failures": "3"}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = e.fail("1.1.1.1")
	}
	var le *auth.LockedError
	if _, err := e.s.SignIn(e.ctx, "admin", goodPassword, "1.1.1.1", ""); !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewIPMeansNoSuccessInThirtyDays(t *testing.T) {
	e := newEnv(t)
	if !e.signIn("1.1.1.1").NewIP {
		t.Error("first sign-in must be a new IP")
	}
	e.advance(10 * 24 * time.Hour)
	if e.signIn("1.1.1.1").NewIP {
		t.Error("10 days later is not new")
	}
	e.advance(40 * 24 * time.Hour)
	if !e.signIn("1.1.1.1").NewIP {
		t.Error("40 days later is new")
	}
}

func TestSessionTokenIsStoredOnlyAsLookup(t *testing.T) {
	e := newEnv(t)
	r := e.signIn("1.1.1.1")
	if len(r.Session.Token) != 43 {
		t.Fatalf("token %q", r.Session.Token)
	}
	var blob []byte
	if err := e.db.R.GetContext(e.ctx, &blob, `SELECT token_hash FROM sessions`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), r.Session.Token) || len(blob) != 32 {
		t.Error("the token must be stored only as its lookup hash")
	}
}

func TestIdleSessionExpires(t *testing.T) {
	e := newEnv(t)
	r := e.signIn("1.1.1.1")
	e.advance(auth.IdleTimeout + time.Minute)
	_, err := e.s.Authenticate(e.ctx, r.Session.Token, "1.1.1.1")
	var xe *auth.ExpiredError
	if !errors.As(err, &xe) || xe.Reason != "idle" {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionEndsAtThirtyDays(t *testing.T) {
	e := newEnv(t)
	r := e.signIn("1.1.1.1")
	for range 29 {
		e.advance(24 * time.Hour)
		if _, err := e.s.Authenticate(e.ctx, r.Session.Token, "1.1.1.1"); err != nil {
			t.Fatalf("day: %v", err)
		}
	}
	e.advance(24*time.Hour + time.Minute)
	_, err := e.s.Authenticate(e.ctx, r.Session.Token, "1.1.1.1")
	var xe *auth.ExpiredError
	if !errors.As(err, &xe) || xe.Reason != "absolute" {
		t.Fatalf("err = %v", err)
	}
}

func TestLastSeenIsThrottled(t *testing.T) {
	e := newEnv(t)
	r := e.signIn("1.1.1.1")
	e.advance(30 * time.Second)
	if _, err := e.s.Authenticate(e.ctx, r.Session.Token, "9.9.9.9"); err != nil {
		t.Fatal(err)
	}
	var ip string
	_ = e.db.R.GetContext(e.ctx, &ip, `SELECT last_ip FROM sessions`)
	if ip != "1.1.1.1" {
		t.Errorf("last seen written within a minute: %s", ip)
	}
	e.advance(31 * time.Second)
	_, _ = e.s.Authenticate(e.ctx, r.Session.Token, "9.9.9.9")
	_ = e.db.R.GetContext(e.ctx, &ip, `SELECT last_ip FROM sessions`)
	if ip != "9.9.9.9" {
		t.Errorf("last seen not refreshed after a minute: %s", ip)
	}
}

func TestSignOutOneSession(t *testing.T) {
	e := newEnv(t)
	a, b := e.signIn("1.1.1.1"), e.signIn("2.2.2.2")
	if err := e.s.SignOut(e.ctx, a.Session.ID, "settings"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Authenticate(e.ctx, a.Session.Token, "1.1.1.1"); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("ended session: %v", err)
	}
	if _, err := e.s.Authenticate(e.ctx, b.Session.Token, "2.2.2.2"); err != nil {
		t.Errorf("other session: %v", err)
	}
	_ = e.s.SignOut(e.ctx, a.Session.ID, "settings") // again: changes nothing
	if e.events("auth.signed_out") != 1 {
		t.Error("want exactly one auth.signed_out")
	}
}

func TestSignOutEverywhere(t *testing.T) {
	e := newEnv(t)
	e.signIn("1.1.1.1")
	e.signIn("2.2.2.2")
	n, err := e.s.SignOutEverywhere(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if got, _ := e.s.Sessions(e.ctx); len(got) != 0 {
		t.Error("sessions remain")
	}
	if e.events("auth.signed_out_everywhere") != 1 {
		t.Error("want the event")
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	e := newEnv(t)
	laptop, phone := e.signIn("1.1.1.1"), e.signIn("2.2.2.2")
	if err := e.s.ChangePassword(e.ctx, laptop.Session.ID, "wrong wrong wrong", "a brand new password"); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Fatalf("wrong old password: %v", err)
	}
	if err := e.s.ChangePassword(e.ctx, laptop.Session.ID, goodPassword, "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Authenticate(e.ctx, phone.Session.Token, "2.2.2.2"); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("phone: %v", err)
	}
	if _, err := e.s.Authenticate(e.ctx, laptop.Session.Token, "1.1.1.1"); err != nil {
		t.Errorf("laptop: %v", err)
	}
	if e.events("auth.password_changed") != 1 {
		t.Error("want the event")
	}
}

func TestResetPasswordEndsSessionsAndKeepsLockout(t *testing.T) {
	e := newEnv(t)
	r := e.signIn("2.2.2.2")
	for range 5 {
		_ = e.fail("1.1.1.1")
	}
	n, err := e.s.ResetPassword(e.ctx, "another long password")
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if _, err := e.s.Authenticate(e.ctx, r.Session.Token, "2.2.2.2"); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session survived: %v", err)
	}
	var le *auth.LockedError
	if _, err := e.s.SignIn(e.ctx, "admin", "another long password", "1.1.1.1", ""); !errors.As(err, &le) {
		t.Errorf("lockout must still apply: %v", err)
	}
	if _, err := e.s.SignIn(e.ctx, "admin", "another long password", "3.3.3.3", ""); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestSetSameLanguageRecordsNothing(t *testing.T) {
	e := newEnv(t)
	if err := e.s.SetLanguage(e.ctx, "en"); err != nil {
		t.Fatal(err)
	}
	if e.events("admin.language_changed") != 0 {
		t.Error("same language recorded an event")
	}
	_ = e.s.SetLanguage(e.ctx, "ru")
	if a, _ := e.s.Admin(e.ctx); a.Language != "ru" || e.events("admin.language_changed") != 1 {
		t.Error("language not changed")
	}
}

func TestCreateAdminOnlyOnce(t *testing.T) {
	e := newEnv(t)
	if err := e.s.CreateAdmin(e.ctx, "other", goodPassword); !errors.Is(err, auth.ErrAdminExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestPasswordLength(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		pw   string
		want error
	}{
		{strings.Repeat("a", 11), auth.ErrPasswordTooShort},
		{strings.Repeat("a", 12), nil},
		{strings.Repeat("я", 12), nil}, // 24 bytes, 12 characters
		{strings.Repeat("я", 11), auth.ErrPasswordTooShort},
		{strings.Repeat("a", 1025), auth.ErrPasswordTooLong},
	}
	for _, c := range cases {
		_, err := e.s.ResetPassword(e.ctx, c.pw)
		if (c.want == nil && err != nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%d bytes: err = %v, want %v", len(c.pw), err, c.want)
		}
	}
}

func TestHashAndVerify(t *testing.T) {
	p := auth.Params{Memory: 1024, Time: 1, Threads: 1}
	h, err := auth.Hash(p, "secret")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("hash %q, %v", h, err)
	}
	if ok, _ := auth.Verify(h, "secret"); !ok {
		t.Error("right password refused")
	}
	if ok, _ := auth.Verify(h, "Secret"); ok {
		t.Error("wrong password accepted")
	}
	for _, bad := range []string{"", "plain", strings.Replace(h, "argon2id", "argon2i", 1), "$argon2id$v=19$m=999999999,t=1,p=1$AAAA$AAAA", h[:len(h)-3] + "!!!"} {
		if ok, err := auth.Verify(bad, "secret"); ok || err == nil {
			t.Errorf("tampered %q: ok=%v err=%v", bad, ok, err)
		}
	}
}

func TestBrowserName(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120.0 Safari/537.36":                  "Chrome on macOS",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605 Version/17.0 Mobile/15E148 Safari/604.1": "Safari on iOS",
		"Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0":                                         "Firefox on Linux",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0 Safari/537.36 Edg/120.0":                                 "Edge on Windows",
		"": "Unknown browser",
	}
	for ua, want := range cases {
		if got := auth.BrowserName(ua); got != want {
			t.Errorf("%q = %q, want %q", ua, got, want)
		}
	}
}

func TestCSRFTokenIsTiedToTheSession(t *testing.T) {
	e := newEnv(t)
	a, b := e.signIn("1.1.1.1"), e.signIn("2.2.2.2")
	if !e.s.CheckCSRF(a.Session.Token, a.Session.CSRF) {
		t.Error("own token refused")
	}
	if e.s.CheckCSRF(a.Session.Token, b.Session.CSRF) || e.s.CheckCSRF(a.Session.Token, "") {
		t.Error("foreign or empty token accepted")
	}
}
