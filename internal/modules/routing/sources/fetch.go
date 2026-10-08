package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/tikhonp/proxier/internal/platform/obs"
)

// Fetcher is the one HTTP client of the module.
type Fetcher struct {
	Client    *http.Client  // nil: a client following at most 5 redirects
	UserAgent string        // "" means "proxier/" + obs.AppVersion
	Timeout   time.Duration // per request: 30 s in jobs, 15 s inside a request
}

const maxRedirects = 5

var defaultClient = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("more than %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errors.New("a redirect away from http(s)")
	}
	return nil
}}

// Get fetches rawURL and returns its status and at most max bytes of its body
// (ErrTooBig when there is more).
func (f *Fetcher) Get(ctx context.Context, rawURL string, max int64) (int, []byte, error) {
	return f.GetWith(ctx, rawURL, max, nil)
}

// GetWith is Get with extra request headers (GitHub's Accept and
// Authorization).
func (f *Fetcher) GetWith(ctx context.Context, rawURL string, max int64, header http.Header) (int, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0, nil, fmt.Errorf("sources: not an http(s) address: %q", rawURL)
	}
	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	ua := f.UserAgent
	if ua == "" {
		ua = "proxier/" + obs.AppVersion
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", ua)
	client := f.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(body)) > max {
		return resp.StatusCode, nil, fmt.Errorf("%w: %s is over %d bytes", ErrTooBig, rawURL, max)
	}
	return resp.StatusCode, body, nil
}
