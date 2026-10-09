package cdp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
)

// The waits of a page (docs/processes/routing/domain-discovery.md, step 3).
const (
	idleFor     = 2 * time.Second
	idleAtMost  = 30 * time.Second
	afterScroll = 3 * time.Second
)

// Visit opens a fresh browser context (through v.Proxy when set), loads the
// page and up to v.Links same-site links in one tab, and records every
// request with its host, type, status and failure.
func (b *Browser) Visit(ctx context.Context, v discovery.Visit) (discovery.Result, error) {
	_, ws, err := b.endpoint(ctx)
	if err != nil {
		return discovery.Result{}, err
	}
	alloc, cancelAlloc := chromedp.NewRemoteAllocator(ctx, ws, chromedp.NoModifyURL)
	defer cancelAlloc()
	var opts []chromedp.CreateBrowserContextOption
	if v.Proxy != "" {
		opts = append(opts, func(p *target.CreateBrowserContextParams) *target.CreateBrowserContextParams {
			return p.WithProxyServer(v.Proxy)
		})
	}
	tab, cancel := chromedp.NewContext(alloc, chromedp.WithNewBrowserContext(opts...))
	defer cancel()

	rec := &recorder{max: v.MaxHosts, hosts: map[string]bool{}, byID: map[network.RequestID]int{}, last: time.Now()}
	chromedp.ListenTarget(tab, rec.event)
	if err := chromedp.Run(tab, network.Enable(), emulation.SetDeviceMetricsOverride(1280, 800, 1, false)); err != nil {
		return discovery.Result{}, fmt.Errorf("cdp: open a tab: %w", err)
	}
	timeout := v.PageTimeout
	if timeout <= 0 {
		timeout = discovery.PageTimeout
	}
	var res discovery.Result
	queue := []string{v.URL}
	visited := map[string]bool{}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		visited[strings.SplitN(u, "#", 2)[0]] = true
		rec.setPage(len(res.Pages))
		pg, title, links, err := b.page(tab, u, timeout, rec)
		if err != nil {
			return res, err
		}
		res.Pages = append(res.Pages, pg)
		if len(res.Pages) == 1 {
			res.Title = title
			if !pg.Loaded {
				break
			}
			for _, l := range links {
				if len(queue) >= v.Links {
					break
				}
				lu, err := url.Parse(l)
				if err != nil || lu.Scheme != "http" && lu.Scheme != "https" || !sameSite(strings.ToLower(lu.Hostname()), v.Site) {
					continue
				}
				lu.Fragment = ""
				if s := lu.String(); !visited[s] && !slices.Contains(queue, s) {
					queue = append(queue, s)
				}
			}
		}
	}
	res.Requests, res.Capped = rec.result()
	return res, nil
}

// page loads one URL in the tab. A page that doesn't load is not an error;
// a tab or protocol failure is.
func (b *Browser) page(tab context.Context, u string, timeout time.Duration, rec *recorder) (discovery.Page, string, []string, error) {
	pg := discovery.Page{URL: u}
	nav, cancel := context.WithTimeout(tab, timeout)
	defer cancel()
	var loader string
	err := chromedp.Run(nav, chromedp.ActionFunc(func(ctx context.Context) error {
		_, l, text, _, err := page.Navigate(u).Do(ctx)
		loader = string(l)
		if err == nil && text != "" {
			pg.Error = text
		}
		return err
	}))
	switch {
	case errors.Is(err, context.DeadlineExceeded) || nav.Err() != nil && tab.Err() == nil:
		pg.Error = "timeout"
		return pg, "", nil, nil
	case err != nil:
		return pg, "", nil, fmt.Errorf("cdp: navigate: %w", err)
	case pg.Error != "":
		return pg, "", nil, nil
	}
	pg.Loaded = true
	rec.waitIdle(nav)
	var title string
	var links []string
	var shot []byte
	// the scroll loads what loads lazily; the screenshot is of the top,
	// where a block page or a captcha shows
	err = chromedp.Run(nav,
		chromedp.Evaluate(`window.scrollTo({top: document.body ? document.body.scrollHeight : 0, behavior: 'instant'})`, nil),
		chromedp.Sleep(afterScroll),
		chromedp.Evaluate(`window.scrollTo({top: 0, behavior: 'instant'})`, nil),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			// a clip of the document's top: whatever the page's scroll position
			shot, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(60).
				WithCaptureBeyondViewport(true).WithClip(&page.Viewport{Width: 1280, Height: 800, Scale: 1}).Do(ctx)
			return err
		}),
		chromedp.Title(&title),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('a[href]')).map(a => a.href)`, &links),
	)
	if err != nil && nav.Err() == nil {
		return pg, title, nil, fmt.Errorf("cdp: read the page: %w", err)
	}
	pg.Screenshot = shot
	pg.Status = rec.status(loader)
	return pg, title, links, nil
}

// recorder keeps a tab's requests from its network events.
type recorder struct {
	mu       sync.Mutex
	max      int
	hosts    map[string]bool
	capped   bool
	reqs     []discovery.Request
	byID     map[network.RequestID]int
	inflight map[network.RequestID]bool
	last     time.Time
	pageNo   int
}

func (r *recorder) setPage(n int) {
	r.mu.Lock()
	r.pageNo = n
	r.mu.Unlock()
}

// add starts a request; a new host over the run's budget is left out and
// marks the visit capped.
func (r *recorder) add(id network.RequestID, rawURL, typ string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return
	}
	host := strings.ToLower(u.Hostname())
	if !r.hosts[host] {
		if len(r.hosts) >= r.max {
			r.capped = true
			return
		}
		r.hosts[host] = true
	}
	r.byID[id] = len(r.reqs)
	r.reqs = append(r.reqs, discovery.Request{URL: rawURL, Host: host, Type: typ, Page: r.pageNo})
	if r.inflight == nil {
		r.inflight = map[network.RequestID]bool{}
	}
	r.inflight[id] = true
}

func (r *recorder) done(id network.RequestID) {
	delete(r.inflight, id)
	r.last = time.Now()
}

func (r *recorder) event(ev any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		if e.RedirectResponse != nil {
			// the hop that redirected ends here; the next one is a new request
			if i, ok := r.byID[e.RequestID]; ok {
				r.reqs[i].Status = int(e.RedirectResponse.Status)
			}
			r.done(e.RequestID)
		}
		if e.Request != nil {
			r.add(e.RequestID, e.Request.URL, string(e.Type))
		}
		r.last = time.Now()
	case *network.EventResponseReceived:
		if i, ok := r.byID[e.RequestID]; ok && e.Response != nil {
			r.reqs[i].Status = int(e.Response.Status)
		}
	case *network.EventLoadingFinished:
		r.done(e.RequestID)
	case *network.EventLoadingFailed:
		if i, ok := r.byID[e.RequestID]; ok && !e.Canceled {
			r.reqs[i].Failed = e.ErrorText
		}
		r.done(e.RequestID)
	case *network.EventWebSocketCreated:
		r.add(e.RequestID, e.URL, "WebSocket")
		r.done(e.RequestID)
	case *network.EventWebSocketFrameError:
		if i, ok := r.byID[e.RequestID]; ok {
			r.reqs[i].Failed = e.ErrorMessage
		}
	}
}

// waitIdle waits until no request has been in flight for idleFor, at most idleAtMost.
func (r *recorder) waitIdle(ctx context.Context) {
	deadline := time.Now().Add(idleAtMost)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		r.mu.Lock()
		idle := len(r.inflight) == 0 && time.Since(r.last) >= idleFor
		r.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// status is the main document's status: its request id is the loader id.
func (r *recorder) status(loader string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i, ok := r.byID[network.RequestID(loader)]; ok {
		return r.reqs[i].Status
	}
	return 0
}

func (r *recorder) result() ([]discovery.Request, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]discovery.Request(nil), r.reqs...), r.capped
}
