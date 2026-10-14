package auth_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/auth/authtest"
)

func newPasskeyEnv(t *testing.T) *env {
	e := newEnv(t)
	if err := e.s.EnablePasskeys(&url.URL{Scheme: "https", Host: "proxier.example.com"}); err != nil {
		t.Fatal(err)
	}
	return e
}

// add registers a from session 1.
func (e *env) add(a *authtest.Authenticator, name string) error {
	e.t.Helper()
	opts, token, err := e.s.BeginPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, name, goodPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.s.FinishPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, token, a.Create(opts))
}

func (e *env) passkeySignIn(a *authtest.Authenticator, ip string) (auth.SignInResult, error) {
	e.t.Helper()
	opts, token, err := e.s.BeginPasskeySignIn()
	if err != nil {
		e.t.Fatal(err)
	}
	return e.s.FinishPasskeySignIn(e.ctx, token, a.Get(opts), ip, ua)
}

func TestPasskeySignsInAlone(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, " MacBook "); err != nil {
		t.Fatal(err)
	}
	if p := e.lastPayload("auth.passkey_added"); !strings.Contains(p, `"name":"MacBook"`) {
		t.Errorf("payload %s", p)
	}
	if on, _ := e.s.HasPasskeys(e.ctx); !on {
		t.Fatal("no passkey")
	}
	a.Count = 1
	r, err := e.passkeySignIn(a, "1.1.1.1")
	if err != nil || r.Session.Token == "" || r.Pending != "" {
		t.Fatalf("sign in: %v %+v", err, r)
	}
	if p := e.lastPayload("auth.signed_in"); !strings.Contains(p, `"method":"passkey"`) {
		t.Errorf("payload %s", p)
	}
	keys, err := e.s.Passkeys(e.ctx, 1)
	if err != nil || len(keys) != 1 || keys[0].Name != "MacBook" || keys[0].LastUsedAt.IsZero() {
		t.Fatalf("passkeys %+v %v", keys, err)
	}
}

func TestPasskeySignsInPastTheAuthenticatorApp(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, "Phone"); err != nil {
		t.Fatal(err)
	}
	e.enableTOTP()
	if r, err := e.passkeySignIn(a, "1.1.1.1"); err != nil || r.Session.Token == "" {
		t.Fatalf("sign in: %v %+v", err, r)
	}
}

func TestPasskeyWithoutUserVerificationIsRefused(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	a.Flags = authtest.FlagUP
	if err := e.add(a, "Key"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("added without UV: %v", err)
	}
	a.Flags = authtest.FlagUP | authtest.FlagUV
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	a.Flags = authtest.FlagUP
	if _, err := e.passkeySignIn(a, "1.1.1.1"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("signed in without UV: %v", err)
	}
	if p := e.lastPayload("auth.sign_in_failed"); !strings.Contains(p, `"stage":"passkey"`) {
		t.Errorf("payload %s", p)
	}
}

func TestPasskeyFromAnotherSiteIsRefused(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	a.Origin = "https://proxier.example.com.evil.example"
	if err := e.add(a, "Key"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("added from another origin: %v", err)
	}
	a.Origin = "https://proxier.example.com"
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	a.Origin = "https://evil.example"
	if _, err := e.passkeySignIn(a, "1.1.1.1"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("origin: %v", err)
	}
	a.Origin, a.RPID = "https://proxier.example.com", "evil.example"
	if _, err := e.passkeySignIn(a, "1.1.1.1"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("RP ID: %v", err)
	}
}

func TestUnknownOrRemovedPasskeyIsRefused(t *testing.T) {
	e := newPasskeyEnv(t)
	a, stranger := authtest.New(t, "https://proxier.example.com"), authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, "Phone"); err != nil {
		t.Fatal(err)
	}
	stranger.User = a.User
	if _, err := e.passkeySignIn(stranger, "1.1.1.1"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("unknown passkey: %v", err)
	}
	keys, _ := e.s.Passkeys(e.ctx, 1)
	if err := e.s.RemovePasskey(e.ctx, 1, keys[0].ID, "wrong password!!"); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Errorf("removed with a wrong password: %v", err)
	}
	if err := e.s.RemovePasskey(e.ctx, 1, keys[0].ID, goodPassword); err != nil {
		t.Fatal(err)
	}
	if p := e.lastPayload("auth.passkey_removed"); !strings.Contains(p, `"name":"Phone"`) {
		t.Errorf("payload %s", p)
	}
	if err := e.s.RemovePasskey(e.ctx, 1, keys[0].ID, goodPassword); !errors.Is(err, auth.ErrNoPasskey) {
		t.Errorf("removed twice: %v", err)
	}
	if _, err := e.passkeySignIn(a, "1.1.1.1"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("removed passkey: %v", err)
	}
	if e.events("auth.sign_in_failed") != 2 {
		t.Errorf("auth.sign_in_failed = %d, want 2", e.events("auth.sign_in_failed"))
	}
}

func TestPasskeyCounterGoingBackIsRefused(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	a.Count = 10
	if _, err := e.passkeySignIn(a, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	a.Count = 3
	if _, err := e.passkeySignIn(a, "1.1.1.1"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("counter went back: %v", err)
	}
}

func TestPasskeyPromptIsSingleUseAndRunsOut(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	opts, token, _ := e.s.BeginPasskeySignIn()
	body := a.Get(opts)
	if _, err := e.s.FinishPasskeySignIn(e.ctx, token, body, "1.1.1.1", ua); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FinishPasskeySignIn(e.ctx, token, body, "1.1.1.1", ua); !errors.Is(err, auth.ErrCeremonyExpired) {
		t.Errorf("replayed: %v", err)
	}
	if _, err := e.s.FinishPasskeySignIn(e.ctx, "nope", body, "1.1.1.1", ua); !errors.Is(err, auth.ErrCeremonyExpired) {
		t.Errorf("unknown token: %v", err)
	}
	opts, token, _ = e.s.BeginPasskeySignIn()
	e.advance(auth.CeremonyTTL)
	if _, err := e.s.FinishPasskeySignIn(e.ctx, token, a.Get(opts), "1.1.1.1", ua); !errors.Is(err, auth.ErrCeremonyExpired) {
		t.Errorf("after %s: %v", auth.CeremonyTTL, err)
	}
}

func TestPromptsDontCrossOver(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	opts, token, err := e.s.BeginPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, "Key", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.FinishPasskey(e.ctx, auth.Session{ID: 2, MemberID: 1}, token, a.Create(opts)); !errors.Is(err, auth.ErrCeremonyExpired) {
		t.Errorf("finished from another session: %v", err)
	}
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	opts, token, _ = e.s.BeginPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, "Again", goodPassword)
	if _, err := e.s.FinishPasskeySignIn(e.ctx, token, a.Get(opts), "1.1.1.1", ua); !errors.Is(err, auth.ErrCeremonyExpired) {
		t.Errorf("an add prompt signed in: %v", err)
	}
	opts, token, _ = e.s.BeginPasskeySignIn()
	if err := e.s.FinishPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, token, a.Create(opts)); !errors.Is(err, auth.ErrCeremonyExpired) {
		t.Errorf("a sign-in prompt added a passkey: %v", err)
	}
}

func TestTheSamePasskeyIsAddedOnce(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	if err := e.add(a, "Key again"); !errors.Is(err, auth.ErrPasskeyRejected) {
		t.Errorf("added twice: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM passkeys`); n != 1 {
		t.Errorf("%d passkeys", n)
	}
}

func TestAddingAPasskeyNeedsThePasswordAndAName(t *testing.T) {
	e := newPasskeyEnv(t)
	if _, _, err := e.s.BeginPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, "Key", "wrong password!!"); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	for _, name := range []string{"", "   ", strings.Repeat("я", auth.MaxPasskeyName+1)} {
		if _, _, err := e.s.BeginPasskey(e.ctx, auth.Session{ID: 1, MemberID: 1}, name, goodPassword); !errors.Is(err, auth.ErrPasskeyName) {
			t.Errorf("name %q: %v", name, err)
		}
	}
}

func TestLockedIPCantUseAPasskey(t *testing.T) {
	e := newPasskeyEnv(t)
	a := authtest.New(t, "https://proxier.example.com")
	if err := e.add(a, "Key"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		_ = e.fail("1.1.1.1")
	}
	var locked *auth.LockedError
	if _, err := e.passkeySignIn(a, "1.1.1.1"); !errors.As(err, &locked) {
		t.Errorf("err = %v", err)
	}
	if _, err := e.passkeySignIn(a, "2.2.2.2"); err != nil {
		t.Errorf("another IP: %v", err)
	}
}

func TestPasskeysNeedABaseURL(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.s.BeginPasskeySignIn(); !errors.Is(err, auth.ErrPasskeysOff) {
		t.Errorf("err = %v", err)
	}
}

// A passkey signs in as the member who added it, and only that member lists
// or removes it.
func TestPasskeySignsInAsItsMember(t *testing.T) {
	e := newPasskeyEnv(t)
	anna := e.addMember("anna")
	sess := auth.Session{ID: 2, MemberID: anna}
	a := authtest.New(t, "https://proxier.example.com")
	opts, token, err := e.s.BeginPasskey(e.ctx, sess, "Anna's phone", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.FinishPasskey(e.ctx, sess, token, a.Create(opts)); err != nil {
		t.Fatal(err)
	}
	a.Count = 1
	r, err := e.passkeySignIn(a, "1.1.1.1")
	if err != nil || r.Session.MemberID != anna {
		t.Fatalf("sign in: %v %+v", err, r.Session)
	}
	if mine, _ := e.s.Passkeys(e.ctx, 1); len(mine) != 0 {
		t.Errorf("member 1 lists anna's passkey: %+v", mine)
	}
	theirs, _ := e.s.Passkeys(e.ctx, anna)
	if len(theirs) != 1 {
		t.Fatalf("anna's passkeys: %+v", theirs)
	}
	if err := e.s.RemovePasskey(e.ctx, 1, theirs[0].ID, goodPassword); !errors.Is(err, auth.ErrNoPasskey) {
		t.Errorf("member 1 removed anna's passkey: %v", err)
	}
}
