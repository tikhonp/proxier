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

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"
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

	actx, cancel := chromedp.NewRemoteAllocator(context.Background(), raw)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	step := func(d time.Duration, actions ...chromedp.Action) error {
		sctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return chromedp.Run(sctx, actions...)
	}

	var location, rows string
	err = chromedp.Run(ctx,
		webauthn.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := webauthn.AddVirtualAuthenticator(&webauthn.VirtualAuthenticatorOptions{
				Protocol: webauthn.AuthenticatorProtocolCtap2, Ctap2version: webauthn.Ctap2versionCtap21,
				Transport: webauthn.AuthenticatorTransportInternal, HasResidentKey: true, HasUserVerification: true,
				IsUserVerified: true, AutomaticPresenceSimulation: true,
			}).Do(ctx)
			return err
		}),
		network.SetCookie(web.CookieName(false), l.Token).WithURL(base.String()+"/"),
		chromedp.Navigate(base.String()+"/settings/security"),
		chromedp.WaitEnabled(`form[data-passkey-add] button[type=submit]`),
		chromedp.SendKeys(`#pk-name`, "Chromium"),
		chromedp.SendKeys(`#pk-pw`, sitetest.Password),
		chromedp.Evaluate(`document.querySelector('form[data-passkey-add]').requestSubmit()`, nil),
		chromedp.WaitVisible(`#passkeys`),
		chromedp.Location(&location),
		chromedp.OuterHTML(`#passkeys`, &rows),
	)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.HasSuffix(location, "/settings/security?passkey=added") || !strings.Contains(rows, "Chromium") {
		t.Fatalf("after adding: %s\n%s", location, rows)
	}

	signOut := chromedp.Evaluate(`document.querySelector('[data-action="auth.sign_out"]').click()`, nil)
	state := func() string {
		var st string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`location.href + " | " + ((document.getElementById('pk-msg')||{}).textContent||"")`, &st))
		return st
	}

	// The button: with autofill's quiet request out of the way, the button
	// opens the browser's prompt.
	var noAutofill page.ScriptIdentifier
	err = step(20*time.Second,
		chromedp.ActionFunc(func(ctx context.Context) (err error) {
			noAutofill, err = page.AddScriptToEvaluateOnNewDocument(`PublicKeyCredential.isConditionalMediationAvailable = () => Promise.resolve(false)`).Do(ctx)
			return err
		}),
		signOut,
		chromedp.WaitVisible(`[data-passkey-signin]`),
		chromedp.Navigate(base.String()+"/login?next=/settings/security"),
		chromedp.WaitVisible(`[data-passkey-signin]`),
		chromedp.Click(`[data-passkey-signin]`),
		chromedp.WaitVisible(`#app`),
		chromedp.Location(&location),
	)
	if err != nil || !strings.HasSuffix(location, "/settings/security") {
		t.Fatalf("sign in with the button: %v (%s)", err, state())
	}

	// Autofill: the virtual authenticator answers the quiet request at once,
	// as a real browser does when the admin picks the passkey.
	err = step(20*time.Second,
		page.RemoveScriptToEvaluateOnNewDocument(noAutofill),
		signOut,
		chromedp.WaitVisible(`#app`),
		chromedp.Location(&location),
	)
	if err != nil || !strings.HasSuffix(location, "/") {
		t.Fatalf("sign in from autofill: %v (%s)", err, state())
	}
	var n int
	if err := s.App.DB.R.GetContext(t.Context(), &n, `SELECT count(*) FROM events WHERE type = 'auth.signed_in' AND json_extract(payload, '$.method') = 'passkey'`); err != nil || n != 2 {
		t.Errorf("%d passkey sign-ins: %v", n, err)
	}
}
