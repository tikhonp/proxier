package web

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

// limitedPrefixes are the public spaces apps and routers fetch from. /agent/
// is public too, but limits itself per session (1h).
var limitedPrefixes = []string{"/s/", "/r/", "/f/"}

func isLimitedPath(p string) bool {
	for _, pre := range limitedPrefixes {
		if strings.HasPrefix(p, pre) || p == strings.TrimSuffix(pre, "/") {
			return true
		}
	}
	return false
}

// maxTracked is how many client IPs the limiter keeps before it drops the
// windows that have ended.
const maxTracked = 10000

// Limiter is a fixed window of Max requests per client IP for the public
// fetch spaces (/s/, /r/, /f/). Max 0 turns it off.
type Limiter struct {
	Max    int
	Window time.Duration
	Now    func() time.Time

	mu      sync.Mutex
	windows map[string]window
}

type window struct {
	start time.Time
	n     int
}

// NewLimiter returns a limiter on the real clock.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{Max: max, Window: window, Now: time.Now}
}

// allow counts one request of ip; when it is over the limit it says how long
// until the window ends.
func (l *Limiter) allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Max <= 0 {
		return true, 0
	}
	now := l.Now()
	if l.windows == nil {
		l.windows = map[string]window{}
	}
	w, ok := l.windows[ip]
	if !ok || !now.Before(w.start.Add(l.Window)) {
		if !ok && len(l.windows) >= maxTracked {
			for k, old := range l.windows {
				if !now.Before(old.start.Add(l.Window)) {
					delete(l.windows, k)
				}
			}
		}
		w = window{start: now}
	}
	w.n++
	l.windows[ip] = w
	if w.n > l.Max {
		return false, w.start.Add(l.Window).Sub(now)
	}
	return true, 0
}

// Middleware answers 429 with Retry-After (whole seconds, at least 1) once an
// IP is over the limit, for /s/, /r/ and /f/ only.
func (l *Limiter) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !isLimitedPath(c.Request().URL.Path) {
				return next(c)
			}
			if ok, wait := l.allow(c.RealIP()); !ok {
				secs := int((wait + time.Second - 1) / time.Second)
				if secs < 1 {
					secs = 1
				}
				c.Response().Header().Set("Retry-After", strconv.Itoa(secs))
				return echo.NewHTTPError(http.StatusTooManyRequests, http.StatusText(http.StatusTooManyRequests))
			}
			return next(c)
		}
	}
}

// MaxBody is the largest request body of the admin space: the template
// editor posts a whole draft (12 MiB of files, docs/build/1b.md) and a zip
// upload comes with form fields around it.
const MaxBody = 12<<20 + 256<<10

// LimitBody refuses a body over n bytes before anything reads it, so the
// CSRF middleware's form parsing is bounded too. A declared length over the
// limit is a 413 at once; an undeclared one is cut off while it is read.
func LimitBody(n int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			r := c.Request()
			if r.ContentLength > n {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
			}
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(c.Response(), r.Body, n)
			}
			return next(c)
		}
	}
}
