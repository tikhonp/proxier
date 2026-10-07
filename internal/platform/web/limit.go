package web

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

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
