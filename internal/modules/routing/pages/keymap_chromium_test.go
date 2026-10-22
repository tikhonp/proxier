package pages_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// TestKeymapInRealChromium presses proxier.js's vim keys in a real Chromium
// on routing's pages, which have every kind: lists of things, a thing's rows
// with their own remove, edit and confirm pages with a Cancel. It needs a
// running Chromium: PROXIER_TEST_CHROMIUM_URL ("ws://127.0.0.1:9222"), and
// PROXIER_TEST_PAGE_HOST=host.docker.internal when Chromium is a container.
func TestKeymapInRealChromium(t *testing.T) {
	raw := os.Getenv("PROXIER_TEST_CHROMIUM_URL")
	if raw == "" {
		t.Skip("PROXIER_TEST_CHROMIUM_URL is not set")
	}
	pageHost := os.Getenv("PROXIER_TEST_PAGE_HOST")
	if pageHost == "" {
		pageHost = "127.0.0.1"
	}
	h := routingtest.New(t)
	mine := h.Custom("mine", "example.com")
	other := h.Custom("other", "example.org")
	h.List("Main")
	parents := h.List("Parents", mine, other)
	_, router := h.Router("home", parents)

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	srv := &http.Server{Handler: h.Site.Handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	base := "http://" + net.JoinHostPort(pageHost, port)

	actx, cancel := remote.NewAllocator(context.Background(), raw)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	b := browser{t: t, ctx: ctx, base: base}
	b.do(chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
		_, err := cdp.Call(ctx, tg, network.SetCookie, network.SetCookieParams{Name: web.CookieName(false), Value: h.Login.Token, URL: base + "/"})
		return err
	}))
	lists, list := "/routing/lists", fmt.Sprintf("/routing/lists/%d", parents)

	// e and x on a row go to the row's thing: its edit page, its delete page
	b.open(lists)
	b.press("j", "j", "e")
	b.at(list + "/edit")
	b.open(lists)
	b.press("j", "j", "x")
	b.at(list + "/delete")
	// esc on a confirm page is its Cancel
	b.press("Escape")
	b.at(list)

	// on the thing's page, x on a row opens that row's remove; esc closes it,
	// esc drops the row, and then x is the page's delete
	b.press("j", "x")
	if !b.eval(fmt.Sprintf(`document.getElementById('dlg-lists.remove.%d').open`, mine)) {
		t.Error("x on a service row didn't open its remove")
	}
	b.press("Escape")
	if b.eval(`!!document.querySelector('dialog[open]')`) {
		t.Error("esc left the dialog open")
	}
	b.press("Escape")
	if b.eval(`!!document.querySelector('.pc-cur')`) {
		t.Error("esc kept the row under the cursor")
	}
	b.press("x")
	b.at(list + "/delete")

	// an edit page: esc leaves the field, then the page through Cancel; after
	// typing, only a second esc leaves
	b.open(list + "/edit")
	b.press("Escape", "Escape")
	b.at(list)
	b.open(list + "/edit")
	b.press("z", "Escape", "Escape")
	b.sleep()
	if !strings.HasSuffix(b.location(), list+"/edit") || !b.eval(`document.getElementById('keymsg').textContent.indexOf('Unsaved') === 0`) {
		t.Errorf("the first esc after typing left, or said nothing: %s", b.location())
	}
	b.press("Escape")
	b.at(list)

	// i: into the first field of a page whose field has no autofocus
	edit := fmt.Sprintf("/routing/routers/%d/edit", router)
	b.open(edit)
	b.press("i")
	if !b.eval(`document.activeElement.tagName === 'INPUT' && !!document.activeElement.closest('#main')`) {
		t.Error("i didn't go into the first field")
	}
	b.press("Escape")

	// a page without a list: G, g g, ⌃D and ⌃U scroll it, and g offers Top
	if b.eval(`document.querySelectorAll('[data-row]').length > 0`) {
		t.Fatal("the router's edit page has rows: pick a page without")
	}
	// a style attribute would meet the CSP: the height goes through the DOM
	b.eval(`(function () { var d = document.createElement('div'); d.style.height = '4000px'; document.getElementById('main').appendChild(d); return true; })()`)
	b.press("G")
	if !b.eval(`window.scrollY > 3000`) {
		t.Error("G didn't scroll to the bottom")
	}
	b.press("g")
	if !b.eval(`document.getElementById('wk-top').offsetParent !== null`) {
		t.Error("g doesn't offer Top on a page without a list")
	}
	b.press("g")
	if !b.eval(`window.scrollY === 0`) {
		t.Error("g g didn't scroll to the top")
	}
	b.press("⌃d")
	if !b.eval(`Math.abs(window.scrollY - window.innerHeight / 2) < 2`) {
		t.Error("⌃D didn't scroll half a window")
	}
	b.press("⌃u")
	if !b.eval(`window.scrollY === 0`) {
		t.Error("⌃U didn't scroll back")
	}

	// in a list, the same keys move the cursor
	b.open(lists)
	last := `document.querySelectorAll('[data-row]')[document.querySelectorAll('[data-row]').length - 1].classList.contains('pc-cur')`
	first := `document.querySelector('[data-row]').classList.contains('pc-cur')`
	b.press("⌃d")
	if !b.eval(last) {
		t.Error("⌃D in a short list didn't reach its last row")
	}
	b.press("g", "g")
	if !b.eval(first) {
		t.Error("g g didn't go to the first row")
	}
	b.press("G")
	if !b.eval(last) {
		t.Error("G didn't go to the last row")
	}

	// e on a router's row is its edit page
	b.open("/routing/routers")
	b.press("j", "e")
	b.at(edit)
}

type browser struct {
	t    *testing.T
	ctx  context.Context
	base string
}

func (b browser) do(actions ...chromedp.Action[chromedp.Void]) {
	b.t.Helper()
	if err := chromedp.Do(b.ctx, actions...); err != nil {
		b.t.Fatal(err)
	}
}

func (b browser) open(path string) {
	b.t.Helper()
	b.do(chromedp.Navigate(b.base+path), chromedp.WaitVisible(`#app`))
}

func (b browser) sleep() { b.do(chromedp.Sleep(300 * time.Millisecond)) }

func (b browser) eval(js string) bool {
	b.t.Helper()
	var ok bool
	b.do(chromedp.Func(func(ctx context.Context, tg *chromedp.Target) (err error) {
		ok, err = chromedp.Evaluate[bool](js)(ctx, tg)
		return err
	}))
	return ok
}

func (b browser) location() string {
	b.t.Helper()
	var loc string
	b.do(chromedp.Func(func(ctx context.Context, tg *chromedp.Target) (err error) {
		loc, err = chromedp.Location()(ctx, tg)
		return err
	}))
	return loc
}

// at waits for a navigation to path and for its page.
func (b browser) at(path string) {
	b.t.Helper()
	for range 30 {
		if b.location() == b.base+path {
			b.do(chromedp.WaitVisible(`#app`))
			return
		}
		b.do(chromedp.Sleep(100 * time.Millisecond))
	}
	b.t.Fatalf("at %s, want %s", b.location(), b.base+path)
}

// press types keys the way a keyboard does: one keyDown carrying the
// character, so a handler's preventDefault drops it (chromedp.KeyEvent sends
// the character separately, and it lands on the next page). "⌃d" holds ctrl.
func (b browser) press(keys ...string) {
	b.t.Helper()
	for _, k := range keys {
		down := input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: k}
		switch {
		case k == "Escape":
			down.Code, down.WindowsVirtualKeyCode = "Escape", 27
		case strings.HasPrefix(k, "⌃"):
			c := strings.TrimPrefix(k, "⌃")
			down.Type, down.Key, down.Code, down.Modifiers = input.DispatchKeyEventTypeRawKeyDown, c, "Key"+strings.ToUpper(c), 2
			down.WindowsVirtualKeyCode = int64(strings.ToUpper(c)[0])
		default:
			down.Text = k
			if k != strings.ToLower(k) {
				down.Modifiers = 8
			}
		}
		up := down
		up.Type, up.Text = input.DispatchKeyEventTypeKeyUp, ""
		b.do(chromedp.Func(func(ctx context.Context, tg *chromedp.Target) error {
			for _, p := range []input.DispatchKeyEventParams{down, up} {
				if _, err := cdp.Call(ctx, tg, input.DispatchKeyEvent, p); err != nil {
					return err
				}
			}
			return nil
		}), chromedp.Sleep(60*time.Millisecond))
	}
}
