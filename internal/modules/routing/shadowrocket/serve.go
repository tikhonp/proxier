package shadowrocket

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// tokenShape is what a token can look like; anything else is unknown
// without a lookup.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`)

// MaxUA is how much of a user agent the fetch log keeps.
const MaxUA = 256

// Register adds GET and HEAD /r/:token/:file to the public group, whose
// chain already strips cookies, sets no-store and limits the rate.
func (s *Service) Register(g *echo.Group) {
	g.GET("/r/:token/:file", s.serve)
	g.HEAD("/r/:token/:file", s.serve)
}

func (s *Service) serve(c *echo.Context) error {
	ctx := c.Request().Context()
	token := c.Param("token")
	if !tokenShape.MatchString(token) {
		return echo.ErrNotFound
	}
	r, err := store.ShadowrocketByLookup(ctx, s.d.DB.R, s.d.Vault.Lookup(token))
	if errors.Is(err, store.ErrNotFound) || err == nil && (!r.Enabled || c.Param("file") != r.Name+".conf") {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	body, err := s.render(ctx, r)
	if err != nil {
		return err
	}
	head := c.Request().Method == http.MethodHead
	if !head {
		ua := []rune(c.Request().UserAgent())
		if len(ua) > MaxUA {
			ua = ua[:MaxUA]
		}
		f := store.ShadowrocketFetch{ConfigID: r.ID, At: db.At(s.d.Now()), IP: c.RealIP(), UserAgent: string(ua)}
		// the phone must not lose its config over a log: a failed write is logged
		if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error { return store.RecordShadowrocketFetch(ctx, tx, f) }); err != nil {
			s.d.Log.Error("routing: record a Shadowrocket fetch", "config", r.ID, "error", err)
		}
	}
	if head {
		c.Response().Header().Set("Content-Type", "text/plain; charset=utf-8")
		return c.NoContent(http.StatusOK)
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", body)
}
