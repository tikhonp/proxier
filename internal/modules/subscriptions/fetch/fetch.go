// Package fetch is the only thing link holders touch: GET and HEAD
// /s/{token}, which answers what the link serves now and records the fetch
// (docs/processes/subscriptions/subscription-fetch.md).
package fetch

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/linkpage"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

// tokenShape is what a token can look like; anything else is unknown
// without a lookup.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`)

// Deps are what the fetch uses.
type Deps struct {
	Links  *links.Service
	DB     *db.DB
	Events *events.Catalog
	// Alerts queues the country lookup of a new network; nil: none.
	Alerts *alerts.Service
	Log    *slog.Logger
	Now    func() time.Time
}

// Service answers fetches.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Register adds GET and HEAD /s/:token to the public group, whose chain
// already strips cookies, sets no-store and limits the rate.
func (s *Service) Register(g *echo.Group) {
	g.GET("/s/:token", s.serve)
	g.HEAD("/s/:token", s.serve)
}

func (s *Service) serve(c *echo.Context) error {
	ctx := c.Request().Context()
	token := c.Param("token")
	if !tokenShape.MatchString(token) {
		return echo.ErrNotFound
	}
	l, ok, err := s.d.Links.ByToken(ctx, token)
	if err != nil {
		return err
	}
	if !ok {
		return echo.ErrNotFound
	}
	if ended, err := s.d.Links.Ended(ctx, l); err != nil {
		return err
	} else if ended {
		return echo.ErrNotFound
	}
	query := c.QueryParam("format")
	sv, err := s.d.Links.Serve(ctx, l, query, false)
	if errors.Is(err, output.ErrBadFormat) {
		return echo.NewHTTPError(http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}
	if err != nil {
		return err
	}
	h := c.Response().Header()
	if query == "" {
		h.Set("Vary", "Accept, User-Agent")
	}
	if query == "" && wantsPage(c.Request()) {
		return s.page(c, l, token, sv)
	}
	head := c.Request().Method == http.MethodHead
	if !head {
		s.record(c, l, sv.Format, sv.Response)
	}
	for _, hd := range sv.Headers {
		h[hd.Name] = []string{hd.Value} // lower case as written: Set would capitalise
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	if head {
		return c.NoContent(http.StatusOK)
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", sv.Body)
}

// pageFormat is the format the fetch log stores for a page view.
const pageFormat = "page"

// wantsPage: a browser asking for a web page, not an app that happens to
// send Accept: text/html, nor a Mozilla user agent that takes anything. A
// link preview gets the page whatever it accepts: Chrome's read-aloud asked
// for */* and got the URIs.
func wantsPage(r *http.Request) bool {
	app := Detect(r.UserAgent())
	accept := strings.ToLower(strings.Join(r.Header.Values("Accept"), ","))
	return app == store.PreviewApp || strings.Contains(accept, "text/html") && app == "Browser"
}

// page answers a browser with the link's page instead of its URIs: none
// of the apps' headers, and for a link that no longer works only its
// message.
func (s *Service) page(c *echo.Context, l links.Link, token string, sv links.Served) error {
	if c.Request().Method == http.MethodHead {
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return c.NoContent(http.StatusOK)
	}
	v := linkpage.View{Outcome: sv.Outcome, Contact: sv.Contact}
	if v.Works() {
		v.Title, v.Expires, v.URL, v.Open = sv.Title, l.Expires, s.d.Links.URL(token), linkpage.PlatformOf(c.Request().UserAgent())
	} else {
		v.Message = output.StubText(sv.Loc, sv.Outcome, l.Expires, "")
	}
	var buf bytes.Buffer
	if err := linkpage.Page(v).Render(i18n.WithLocalizer(c.Request().Context(), sv.Loc), &buf); err != nil {
		return err
	}
	s.record(c, l, pageFormat, sv.Response)
	return c.Blob(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}
