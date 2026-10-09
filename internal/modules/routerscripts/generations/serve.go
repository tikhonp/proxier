package generations

import (
	"errors"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// tokenShape is vault.NewToken's shape; anything else is unknown without a
// lookup.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// maxUserAgent is what a fetch keeps of the user agent.
const maxUserAgent = 256

// errGone: another request used the URL between the lookup and the write.
var errGone = errors.New("generations: fetch URL already used")

// Register adds /f/:token to the public group, whose chain already strips
// cookies, sets no-store and noindex and limits the rate. Only GET serves the
// file: every other method answers the same 404 and uses nothing, so a
// client's HEAD can't spend the URL.
func (s *Service) Register(g *echo.Group) {
	g.Any("/f/:token", s.serve)
}

func (s *Service) serve(c *echo.Context) error {
	if c.Request().Method != http.MethodGet {
		return echo.ErrNotFound
	}
	ctx := c.Request().Context()
	token := c.Param("token")
	if !tokenShape.MatchString(token) {
		return echo.ErrNotFound
	}
	u, err := store.FetchURLByLookup(ctx, s.d.DB.R, s.d.Vault.Lookup(token), db.At(s.d.Now()))
	if errors.Is(err, store.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	g, err := s.row(ctx, u.GenerationID)
	if err != nil {
		return err
	}
	// the file is made before the URL is spent: a failure leaves it working
	body, err := s.Body(ctx, u.GenerationID)
	if err != nil {
		return err
	}
	ip, ua := c.RealIP(), truncate(c.Request().UserAgent(), maxUserAgent)
	now := db.At(s.d.Now())
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		ok, err := store.UseFetchURL(ctx, tx, u.ID, now, ip, ua)
		if err != nil {
			return err
		}
		if !ok {
			return errGone
		}
		_, err = s.d.Events.Record(ctx, tx, events.Event{Time: now, Type: "routerscript.fetched", Subject: Subject(u.GenerationID),
			Actor: events.ActorSystem, Payload: map[string]any{"ip": ip, "user_agent": ua}})
		return err
	})
	if errors.Is(err, errGone) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+g.FileName+`"`)
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", body)
}

// truncate keeps at most n characters of s, never splitting one.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
