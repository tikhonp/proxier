package auth_test

import (
	"bytes"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/auth"
)

const ua = "Mozilla/5.0 Chrome/120 Safari/537 (Macintosh)"

func (e *env) lastPayload(typ string) string {
	e.t.Helper()
	var p string
	if err := e.db.R.GetContext(e.ctx, &p, `SELECT payload FROM events WHERE type = ? ORDER BY id DESC LIMIT 1`, typ); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// enableTOTP turns the app on from session 1 and returns its secret.
func (e *env) enableTOTP() string {
	e.t.Helper()
	setup, err := e.s.BeginTOTP(e.ctx, 1, goodPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.s.EnableTOTP(e.ctx, 1, setup.Token, auth.CodeAt(setup.Secret, e.now)); err != nil {
		e.t.Fatal(err)
	}
	// the code used to turn it on can't sign in
	e.advance(30 * time.Second)
	return setup.Secret
}

func (e *env) pending(ip string) string {
	e.t.Helper()
	r := e.signIn(ip)
	if r.Pending == "" || r.Session.Token != "" {
		e.t.Fatalf("want a pending sign-in, got %+v", r)
	}
	return r.Pending
}

func TestWithoutTOTPThePasswordAloneSignsIn(t *testing.T) {
	e := newEnv(t)
	if r := e.signIn("1.1.1.1"); r.Pending != "" || r.Session.Token == "" {
		t.Fatalf("got %+v", r)
	}
	if p := e.lastPayload("auth.signed_in"); !strings.Contains(p, `"method":"password"`) {
		t.Errorf("payload %s", p)
	}
}

func TestPasswordWithTOTPWaitsForTheCode(t *testing.T) {
	e := newEnv(t)
	secret := e.enableTOTP()
	p := e.pending("1.1.1.1")
	if e.count(`SELECT COUNT(*) FROM sessions`) != 0 || e.count(`SELECT COUNT(*) FROM sign_in_attempts WHERE success = 1`) != 0 {
		t.Fatal("a right password alone made a session or a success")
	}
	r, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "1.1.1.1", ua)
	if err != nil || r.Session.Token == "" {
		t.Fatalf("code: %v %+v", err, r)
	}
	if pl := e.lastPayload("auth.signed_in"); !strings.Contains(pl, `"method":"password+code"`) {
		t.Errorf("payload %s", pl)
	}
	if _, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "1.1.1.1", ua); !errors.Is(err, auth.ErrSignInExpired) {
		t.Errorf("a finished sign-in was used again: %v", err)
	}
}

func TestACodeSignsInOnce(t *testing.T) {
	e := newEnv(t)
	secret := e.enableTOTP()
	code := auth.CodeAt(secret, e.now)
	if _, err := e.s.SignInCode(e.ctx, e.pending("1.1.1.1"), code, "1.1.1.1", ua); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SignInCode(e.ctx, e.pending("1.1.1.1"), code, "1.1.1.1", ua); !errors.Is(err, auth.ErrWrongCode) {
		t.Errorf("the same code signed in twice: %v", err)
	}
}

func TestWrongCodeIsAFailedAttempt(t *testing.T) {
	e := newEnv(t)
	e.enableTOTP()
	p := e.pending("1.1.1.1")
	e.slept = nil
	if _, err := e.s.SignInCode(e.ctx, p, "000000", "1.1.1.1", ua); !errors.Is(err, auth.ErrWrongCode) {
		t.Fatalf("err = %v", err)
	}
	if pl := e.lastPayload("auth.sign_in_failed"); !strings.Contains(pl, `"stage":"code"`) || strings.Contains(pl, "000000") {
		t.Errorf("payload %s", pl)
	}
	if len(e.slept) != 1 || e.slept[0] != auth.FailureFloor {
		t.Errorf("a wrong code must be padded like a wrong password, slept %v", e.slept)
	}
}

// A right password must not reset the failure count, or the code could be
// guessed forever.
func TestWrongPasswordsAndCodesLockTogether(t *testing.T) {
	e := newEnv(t)
	secret := e.enableTOTP()
	_ = e.fail("1.1.1.1")
	_ = e.fail("1.1.1.1")
	p := e.pending("1.1.1.1")
	for range 3 {
		_, _ = e.s.SignInCode(e.ctx, p, "000000", "1.1.1.1", ua)
	}
	if e.events("auth.locked") != 1 {
		t.Fatalf("auth.locked = %d after 2 wrong passwords and 3 wrong codes", e.events("auth.locked"))
	}
	var locked *auth.LockedError
	if _, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "1.1.1.1", ua); !errors.As(err, &locked) {
		t.Errorf("the right code during a lockout: %v", err)
	}
	if _, err := e.s.SignIn(e.ctx, "admin", goodPassword, "1.1.1.1", ua); !errors.As(err, &locked) {
		t.Errorf("the right password during a lockout: %v", err)
	}
}

func TestPendingSignInRunsOutOfTriesAndTime(t *testing.T) {
	e := newEnv(t)
	if err := e.st.Set(e.ctx, "admin", "security", map[string]string{"security.lockout_failures": "20"}); err != nil {
		t.Fatal(err)
	}
	secret := e.enableTOTP()

	p := e.pending("1.1.1.1")
	for i := range auth.MaxCodeTries {
		if _, err := e.s.SignInCode(e.ctx, p, "000000", "1.1.1.1", ua); !errors.Is(err, auth.ErrWrongCode) {
			t.Fatalf("try %d: %v", i+1, err)
		}
	}
	if _, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "1.1.1.1", ua); !errors.Is(err, auth.ErrSignInExpired) {
		t.Errorf("after %d wrong codes: %v", auth.MaxCodeTries, err)
	}

	p = e.pending("1.1.1.1")
	e.advance(auth.PendingTTL)
	if _, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "1.1.1.1", ua); !errors.Is(err, auth.ErrSignInExpired) {
		t.Errorf("after %s: %v", auth.PendingTTL, err)
	}

	p = e.pending("1.1.1.1")
	if _, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "2.2.2.2", ua); !errors.Is(err, auth.ErrSignInExpired) {
		t.Errorf("from another IP: %v", err)
	}
}

func TestTOTPTurnedOffMeanwhileEndsThePendingSignIn(t *testing.T) {
	e := newEnv(t)
	secret := e.enableTOTP()
	p := e.pending("1.1.1.1")
	if err := e.s.DisableTOTP(e.ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SignInCode(e.ctx, p, auth.CodeAt(secret, e.now), "1.1.1.1", ua); !errors.Is(err, auth.ErrSignInExpired) {
		t.Errorf("err = %v", err)
	}
}

func TestTOTPSetupNeedsThePasswordAndAValidCode(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.BeginTOTP(e.ctx, 1, "wrong password!!"); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	setup, err := e.s.BeginTOTP(e.ctx, 1, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(setup.URI, "otpauth://totp/Proxier:admin?") || !strings.Contains(setup.URI, "secret="+setup.Secret) {
		t.Errorf("URI %s", setup.URI)
	}
	if err := e.s.EnableTOTP(e.ctx, 1, setup.Token, "000000"); !errors.Is(err, auth.ErrWrongCode) {
		t.Errorf("wrong code: %v", err)
	}
	code := auth.CodeAt(setup.Secret, e.now)
	if err := e.s.EnableTOTP(e.ctx, 2, setup.Token, code); err == nil {
		t.Error("a setup from another session was accepted")
	}
	tampered := []byte(setup.Token)
	tampered[5] ^= 1
	if err := e.s.EnableTOTP(e.ctx, 1, string(tampered), code); err == nil {
		t.Error("a tampered setup was accepted")
	}
	if on, _ := e.s.TOTPEnabled(e.ctx); on || e.events("auth.totp_enabled") != 0 {
		t.Fatal("the app is on after refused setups")
	}
	if err := e.s.EnableTOTP(e.ctx, 1, setup.Token, code); err != nil {
		t.Fatal(err)
	}
	if on, _ := e.s.TOTPEnabled(e.ctx); !on || e.events("auth.totp_enabled") != 1 {
		t.Fatal("the app is not on")
	}
	var stored []byte
	if err := e.db.R.GetContext(e.ctx, &stored, `SELECT secret FROM totp`); err != nil {
		t.Fatal(err)
	}
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if bytes.Contains(stored, raw) {
		t.Error("the secret is stored in plain text")
	}
	if _, err := e.s.BeginTOTP(e.ctx, 1, goodPassword); !errors.Is(err, auth.ErrTOTPOn) {
		t.Errorf("a second setup: %v", err)
	}
}

func TestTOTPSetupRunsOut(t *testing.T) {
	e := newEnv(t)
	setup, err := e.s.BeginTOTP(e.ctx, 1, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	e.advance(auth.TOTPSetupTTL + time.Second)
	if err := e.s.EnableTOTP(e.ctx, 1, setup.Token, auth.CodeAt(setup.Secret, e.now)); !errors.Is(err, auth.ErrSetupExpired) {
		t.Errorf("err = %v", err)
	}
}

func TestDisablingTOTPNeedsThePassword(t *testing.T) {
	e := newEnv(t)
	e.enableTOTP()
	if err := e.s.DisableTOTP(e.ctx, "wrong password!!"); !errors.Is(err, auth.ErrWrongCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if err := e.s.DisableTOTP(e.ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := e.s.DisableTOTP(e.ctx, goodPassword); !errors.Is(err, auth.ErrTOTPOff) {
		t.Errorf("turning it off twice: %v", err)
	}
	if e.events("auth.totp_disabled") != 1 {
		t.Errorf("auth.totp_disabled = %d", e.events("auth.totp_disabled"))
	}
	if r := e.signIn("1.1.1.1"); r.Session.Token == "" {
		t.Error("the password alone doesn't sign in after turning the app off")
	}
}

func TestResetSecondFactorRemovesEverythingAndEndsSessions(t *testing.T) {
	e := newEnv(t)
	if r, err := e.s.ResetSecondFactor(e.ctx); err != nil || r != (auth.SecondFactorReset{}) || e.events("auth.second_factor_reset") != 0 {
		t.Fatalf("with nothing to remove: %+v %v", r, err)
	}
	secret := e.enableTOTP()
	_, _ = e.s.SignInCode(e.ctx, e.pending("1.1.1.1"), auth.CodeAt(secret, e.now), "1.1.1.1", ua)
	e.db.W.MustExec(`INSERT INTO passkeys (credential_id, name, credential, created_at) VALUES (x'01', 'Phone', '{}', '')`)
	r, err := e.s.ResetSecondFactor(e.ctx)
	if err != nil || r != (auth.SecondFactorReset{TOTP: true, Passkeys: 1, Sessions: 1}) {
		t.Fatalf("reset: %+v %v", r, err)
	}
	if e.count(`SELECT COUNT(*) FROM totp`)+e.count(`SELECT COUNT(*) FROM passkeys`) != 0 {
		t.Error("something is left")
	}
	if e.count(`SELECT COUNT(*) FROM sessions WHERE end_reason = 'second_factor_reset'`) != 1 {
		t.Error("the session didn't end")
	}
	if p := e.lastPayload("auth.second_factor_reset"); !strings.Contains(p, `"via":"cli"`) {
		t.Errorf("payload %s", p)
	}
	if r := e.signIn("1.1.1.1"); r.Session.Token == "" {
		t.Error("the password alone doesn't sign in after the reset")
	}
}
