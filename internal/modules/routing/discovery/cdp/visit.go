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

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
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
	alloc, cancelAlloc := remote.NewAllocator(ctx, ws, remote.NoModifyURL)
	defer cancelAlloc()
	tab, cancel := chromedp.NewContext(alloc, chromedp.WithNewBrowserContext(func(p *target.CreateBrowserContextParams) {
		p.ProxyServer = v.Proxy
	}))
	defer cancel()

	rec := newRecorder(v.MaxHosts)
	rec.listen(tab)
	if _, err := chromedp.Call(tab, network.Enable, network.EnableParams{}); err != nil {
		return discovery.Result{}, fmt.Errorf("cdp: open a tab: %w", err)
	}
	if _, err := chromedp.Call(tab, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 1280, Height: 800, DeviceScaleFactor: 1}); err != nil {
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
	nr, err := chromedp.Call(nav, page.Navigate, page.NavigateParams{URL: u})
	if err == nil && nr.ErrorText != "" {
		pg.Error = nr.ErrorText
	}
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
	err = chromedp.Do(nav,
		chromedp.Evaluate[chromedp.Void](`window.scrollTo({top: document.body ? document.body.scrollHeight : 0, behavior: 'instant'})`),
		chromedp.Sleep(afterScroll),
		chromedp.Evaluate[chromedp.Void](`window.scrollTo({top: 0, behavior: 'instant'})`),
		chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			// a clip of the document's top: whatever the page's scroll position
			res, err := cdp.Call(ctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
				Format: page.CaptureScreenshotFormatJpeg, Quality: new(int64(60)), CaptureBeyondViewport: new(true),
				Clip: &page.Viewport{Width: 1280, Height: 800, Scale: 1},
			})
			if err != nil {
				return err
			}
			shot = res.Data
			if title, err = chromedp.Title()(ctx, t); err != nil {
				return err
			}
			links, err = chromedp.Evaluate[[]string](`Array.from(document.querySelectorAll('a[href]')).map(a => a.href)`)(ctx, t)
			return err
		}),
	)
	if err != nil && nav.Err() == nil {
		return pg, title, nil, fmt.Errorf("cdp: read the page: %w", err)
	}
	pg.Screenshot = shot
	pg.Status = rec.status(network.RequestID(nr.LoaderID))
	return pg, title, links, nil
}

// recorder keeps a tab's requests from its network events. Each kind of
// event comes on a stream of its own, so a request's end or response can
// arrive before its start or its redirects. They are kept by request ID and
// belong to the request's last hop; a redirect's status comes with the next
// hop, on the stream of starts, which is in order.
type recorder struct {
	mu     sync.Mutex
	max    int
	hosts  map[string]bool
	capped bool
	reqs   []discovery.Request
	byID   map[network.RequestID]int // the index of the request's last hop
	open   map[network.RequestID]bool
	ended  map[network.RequestID]bool
	final  map[network.RequestID]int
	failed map[network.RequestID]string
	last   time.Time
	pageNo int
}

func newRecorder(maxHosts int) *recorder {
	return &recorder{
		max: maxHosts, hosts: map[string]bool{}, byID: map[network.RequestID]int{}, open: map[network.RequestID]bool{},
		ended: map[network.RequestID]bool{}, final: map[network.RequestID]int{}, failed: map[network.RequestID]string{}, last: time.Now(),
	}
}

// listen subscribes to the tab's network events; they are read until the tab
// closes.
func (r *recorder) listen(tab context.Context) {
	on(tab, r, network.RequestWillBeSent, r.requestWillBeSent)
	on(tab, r, network.ResponseReceived, func(e network.EventResponseReceived) {
		if e.Response != nil {
			r.final[e.RequestID] = int(e.Response.Status)
		}
	})
	on(tab, r, network.LoadingFinished, func(e network.EventLoadingFinished) { r.end(e.RequestID) })
	on(tab, r, network.LoadingFailed, func(e network.EventLoadingFailed) {
		if !e.Canceled {
			r.failed[e.RequestID] = e.ErrorText
		}
		r.end(e.RequestID)
	})
	on(tab, r, network.WebSocketCreated, func(e network.EventWebSocketCreated) {
		r.add(e.RequestID, e.URL, "WebSocket")
		r.end(e.RequestID)
	})
	on(tab, r, network.WebSocketFrameError, func(e network.EventWebSocketFrameError) { r.failed[e.RequestID] = e.ErrorMessage })
}

// on subscribes at once and hands each event to f under the recorder's lock.
func on[E any](tab context.Context, r *recorder, ev cdp.Event[E], f func(E)) {
	events := chromedp.Events(tab, ev)
	go func() {
		for e, err := range events {
			if err != nil {
				return
			}
			r.mu.Lock()
			f(e)
			r.mu.Unlock()
		}
	}()
}

func (r *recorder) setPage(n int) {
	r.mu.Lock()
	r.pageNo = n
	r.mu.Unlock()
}

func (r *recorder) requestWillBeSent(e network.EventRequestWillBeSent) {
	if e.RedirectResponse != nil {
		// the hop that redirected ends here; the next one is a new entry
		if i, ok := r.byID[e.RequestID]; ok {
			r.reqs[i].Status = int(e.RedirectResponse.Status)
		}
	}
	if e.Request != nil {
		r.add(e.RequestID, e.Request.URL, string(e.Type))
	}
	r.last = time.Now()
}

// add starts a request or its next hop; a new host over the run's budget is
// left out and marks the visit capped.
func (r *recorder) add(id network.RequestID, rawURL, typ string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return
	}
	host := strings.ToLower(u.Hostname())
	if !r.hosts[host] {
		if len(r.hosts) >= r.max {
			r.capped = true
			// a redirect to the left-out host: the earlier hop keeps its status
			delete(r.byID, id)
			delete(r.open, id)
			return
		}
		r.hosts[host] = true
	}
	r.byID[id] = len(r.reqs)
	r.reqs = append(r.reqs, discovery.Request{URL: rawURL, Host: host, Type: typ, Page: r.pageNo})
	if !r.ended[id] {
		r.open[id] = true
	}
}

func (r *recorder) end(id network.RequestID) {
	r.ended[id] = true
	delete(r.open, id)
	r.last = time.Now()
}

// waitIdle waits until no request has been in flight for idleFor, at most idleAtMost.
func (r *recorder) waitIdle(ctx context.Context) {
	deadline := time.Now().Add(idleAtMost)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		r.mu.Lock()
		idle := len(r.open) == 0 && time.Since(r.last) >= idleFor
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
func (r *recorder) status(loader network.RequestID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byID[loader]
	if !ok {
		return 0
	}
	if st, ok := r.final[loader]; ok {
		return st
	}
	return r.reqs[i].Status
}

func (r *recorder) result() ([]discovery.Request, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reqs := append([]discovery.Request(nil), r.reqs...)
	for id, i := range r.byID {
		if st, ok := r.final[id]; ok {
			reqs[i].Status = st
		}
		if f, ok := r.failed[id]; ok {
			reqs[i].Failed = f
		}
	}
	return reqs, r.capped
}
