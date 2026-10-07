package pages

import (
	"errors"
	"math"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) loginPage(c *echo.Context) error {
	next := web.SafeNext(c.QueryParam("next"))
	if web.FromContext(c.Request().Context()).Session != nil {
		return web.Redirect(c, next)
	}
	v := ui.SignInView{Next: next}
	if e := c.QueryParam("ended"); e == "idle" || e == "absolute" {
		v.Ended = e
	}
	return web.Render(c, http.StatusOK, ui.SignIn(v))
}

func (h *handler) login(c *echo.Context) error {
	username, password := c.FormValue("username"), c.FormValue("password")
	next := web.SafeNext(c.FormValue("next"))
	res, err := h.Auth.SignIn(c.Request().Context(), username, password, c.RealIP(), c.Request().UserAgent())

	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		mins := int(math.Ceil(locked.For.Minutes()))
		return web.Render(c, http.StatusTooManyRequests, ui.SignIn(ui.SignInView{Next: next, Username: username, Error: "locked", LockedMin: max(mins, 1)}))
	case errors.Is(err, auth.ErrWrongCredentials), errors.Is(err, auth.ErrNoAdmin):
		return web.Render(c, http.StatusUnauthorized, ui.SignIn(ui.SignInView{Next: next, Username: username, Error: "wrong"}))
	case err != nil:
		return err
	}
	web.SetSessionCookie(c, h.cookieSecure(), res.Session)
	return web.Redirect(c, next)
}

func (h *handler) logout(c *echo.Context) error {
	if s := web.FromContext(c.Request().Context()).Session; s != nil {
		if err := h.Auth.SignOut(c.Request().Context(), s.ID, "self"); err != nil {
			return err
		}
	}
	web.ClearSessionCookie(c, h.cookieSecure())
	return web.Redirect(c, "/login")
}
