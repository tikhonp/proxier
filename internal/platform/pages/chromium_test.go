package pages_test

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// TestPasskeyInRealChromium adds a passkey on Settings → Security and signs in
// with it, from the button and from autofill, through passkey.js and
// Chromium's virtual authenticator. It needs a running Chromium:
// PROXIER_TEST_CHROMIUM_URL ("ws://127.0.0.1:9222"). WebAuthn needs a secure
// context, and plain http is one only on localhost, so the page is served on
// PROXIER_TEST_PAGE_PORT (18123) and opened as localhost; when Chromium is a
// container, forward that port inside it to this machine:
// docker exec -d <container> socat TCP4-LISTEN:18123,fork,reuseaddr TCP4:host.docker.internal:18123
func TestPasskeyInRealChromium(t *testing.T) {
	raw := os.Getenv("PROXIER_TEST_CHROMIUM_URL")
	if raw == "" {
		t.Skip("PROXIER_TEST_CHROMIUM_URL is not set")
	}
	port := os.Getenv("PROXIER_TEST_PAGE_PORT")
	if port == "" {
		port = "18123"
	}
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		t.Fatal(err)
	}
	base := &url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", port)}
	s := sitetest.New(t, sitetest.Options{BaseURL: base})
	srv := &http.Server{Handler: s.Handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	l := s.SignIn("")

	actx, cancel := remote.NewAllocator(context.Background(), raw)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	step := func(d time.Duration, actions ...chromedp.Action[chromedp.Void]) error {
		sctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return chromedp.Do(sctx, actions...)
	}

	var location, rows string
	err = chromedp.Do(ctx,
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			if _, err := cdp.Call(ctx, t, webauthn.Enable, webauthn.EnableParams{}); err != nil {
				return err
			}
			if _, err := cdp.Call(ctx, t, webauthn.AddVirtualAuthenticator, webauthn.AddVirtualAuthenticatorParams{Options: &webauthn.VirtualAuthenticatorOptions{
				Protocol: webauthn.AuthenticatorProtocolCtap2, Ctap2version: webauthn.Ctap2versionCtap21,
				Transport: webauthn.AuthenticatorTransportInternal, HasResidentKey: true, HasUserVerification: true,
				IsUserVerified: true, AutomaticPresenceSimulation: true,
			}}); err != nil {
				return err
			}
			_, err := cdp.Call(ctx, t, network.SetCookie, network.SetCookieParams{Name: web.CookieName(false), Value: l.Token, URL: base.String() + "/"})
			return err
		}),
		chromedp.Navigate(base.String()+"/settings/security"),
		chromedp.WaitEnabled(`form[data-passkey-add] button[type=submit]`),
		chromedp.SendKeys(`#pk-name`, "Chromium"),
		chromedp.SendKeys(`#pk-pw`, sitetest.Password),
		chromedp.Evaluate[chromedp.Void](`document.querySelector('form[data-passkey-add]').requestSubmit()`),
		chromedp.WaitVisible(`#passkeys`),
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) (err error) {
			if location, err = chromedp.Location()(ctx, t); err != nil {
				return err
			}
			rows, err = chromedp.OuterHTML(`#passkeys`)(ctx, t)
			return err
		}),
	)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.HasSuffix(location, "/settings/security?passkey=added") || !strings.Contains(rows, "Chromium") {
		t.Fatalf("after adding: %s\n%s", location, rows)
	}

	signOut := chromedp.Evaluate[chromedp.Void](`document.querySelector('[data-action="auth.sign_out"]').click()`)
	state := func() string {
		st, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`location.href + " | " + ((document.getElementById('pk-msg')||{}).textContent||"")`))
		return st
	}
	readLocation := chromedp.Func(func(ctx context.Context, t *chromedp.Target) (err error) {
		location, err = chromedp.Location()(ctx, t)
		return err
	})

	// The button: with autofill's quiet request out of the way, the button
	// opens the browser's prompt.
	var noAutofill page.ScriptIdentifier
	err = step(20*time.Second,
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			res, err := cdp.Call(ctx, t, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
				Source: `PublicKeyCredential.isConditionalMediationAvailable = () => Promise.resolve(false)`,
			})
			noAutofill = res.Identifier
			return err
		}),
		signOut,
		chromedp.WaitVisible(`[data-passkey-signin]`),
		chromedp.Navigate(base.String()+"/login?next=/settings/security"),
		chromedp.WaitVisible(`[data-passkey-signin]`),
		chromedp.Click(`[data-passkey-signin]`),
		chromedp.WaitVisible(`#app`),
		readLocation,
	)
	if err != nil || !strings.HasSuffix(location, "/settings/security") {
		t.Fatalf("sign in with the button: %v (%s)", err, state())
	}

	// Autofill: the virtual authenticator answers the quiet request at once,
	// as a real browser does when the admin picks the passkey.
	err = step(20*time.Second,
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			_, err := cdp.Call(ctx, t, page.RemoveScriptToEvaluateOnNewDocument, page.RemoveScriptToEvaluateOnNewDocumentParams{Identifier: noAutofill})
			return err
		}),
		signOut,
		chromedp.WaitVisible(`#app`),
		readLocation,
	)
	if err != nil || !strings.HasSuffix(location, "/") {
		t.Fatalf("sign in from autofill: %v (%s)", err, state())
	}
	var n int
	if err := s.App.DB.R.GetContext(t.Context(), &n, `SELECT count(*) FROM events WHERE type = 'auth.signed_in' AND json_extract(payload, '$.method') = 'passkey'`); err != nil || n != 2 {
		t.Errorf("%d passkey sign-ins: %v", n, err)
	}

	// A closed prompt, or an Android phone without a screen lock, is only a
	// NotAllowedError: it used to end in silence.
	var addMsg, signInMsg string
	err = step(20*time.Second,
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			_, err := cdp.Call(ctx, t, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
				Source: `PublicKeyCredential.isConditionalMediationAvailable = () => Promise.resolve(false);
					navigator.credentials.create = navigator.credentials.get = () => Promise.reject(new DOMException('closed', 'NotAllowedError'))`,
			})
			return err
		}),
		chromedp.Navigate(base.String()+"/settings/security"),
		chromedp.WaitEnabled(`form[data-passkey-add] button[type=submit]`),
		chromedp.SendKeys(`#pk-name`, "Phone"),
		chromedp.SendKeys(`#pk-pw`, sitetest.Password),
		chromedp.Evaluate[chromedp.Void](`document.querySelector('form[data-passkey-add]').requestSubmit()`),
		chromedp.WaitVisible(`form[data-passkey-add] .alert`),
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) (err error) {
			addMsg, err = chromedp.Text(`form[data-passkey-add] .alert`)(ctx, t)
			return err
		}),
		signOut,
		chromedp.WaitVisible(`[data-passkey-signin]`),
		chromedp.Click(`[data-passkey-signin]`),
		chromedp.WaitVisible(`#pk-msg`),
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) (err error) {
			signInMsg, err = chromedp.Text(`#pk-msg`)(ctx, t)
			return err
		}),
	)
	if err != nil || !strings.Contains(addMsg, "screen lock") || !strings.Contains(signInMsg, "the prompt was closed") {
		t.Errorf("a closed prompt: %v, add %q, sign in %q (%s)", err, addMsg, signInMsg, state())
	}
}
