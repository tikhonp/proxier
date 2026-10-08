// Package fetch is the only thing link holders touch: GET and HEAD
// /s/{token}, which answers what the link serves now and records the fetch
// (docs/processes/subscriptions/subscription-fetch.md).
package fetch

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
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
	resp, format, err := s.d.Links.Serve(ctx, l, c.QueryParam("format"), false)
	if errors.Is(err, output.ErrBadFormat) {
		return echo.NewHTTPError(http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}
	if err != nil {
		return err
	}
	head := c.Request().Method == http.MethodHead
	if !head {
		// apps must not lose their list over a log: a failed write is logged
		if err := s.record(c, l, format, resp); err != nil {
			s.d.Log.Error("subscriptions: record fetch", "link", l.ID, "error", err)
		}
	}
	h := c.Response().Header()
	for _, hd := range resp.Headers {
		h[hd.Name] = []string{hd.Value} // lower case as written: Set would capitalise
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	if head {
		return c.NoContent(http.StatusOK)
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", resp.Body)
}
