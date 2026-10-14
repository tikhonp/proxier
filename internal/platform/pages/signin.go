package pages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// maxPasskeyBody bounds a passkey answer read into memory; a real one is a
// few kilobytes.
const maxPasskeyBody = 64 << 10

// passkeysOffered: the sign-in page loads the passkey script only once a
// passkey exists, so nothing changes for an admin who never added one.
func (h *handler) passkeysOffered(ctx context.Context) (bool, error) {
	if !h.Auth.PasskeysOn() {
		return false, nil
	}
	return h.Auth.HasPasskeys(ctx)
}

func (h *handler) renderSignIn(c *echo.Context, status int, v ui.SignInView) error {
	on, err := h.passkeysOffered(c.Request().Context())
	if err != nil {
		return err
	}
	v.Passkeys = on
	return web.Render(c, status, ui.SignIn(v))
}

func (h *handler) loginPage(c *echo.Context) error {
	next := web.SafeNext(c.QueryParam("next"))
	if web.FromContext(c.Request().Context()).Session != nil {
		return web.Redirect(c, next)
	}
	v := ui.SignInView{Next: next}
	if e := c.QueryParam("ended"); e == "idle" || e == "absolute" {
		v.Ended = e
	}
	return h.renderSignIn(c, http.StatusOK, v)
}

func lockedMinutes(l *auth.LockedError) int { return max(int(math.Ceil(l.For.Minutes())), 1) }

func (h *handler) login(c *echo.Context) error {
	username, password := c.FormValue("username"), c.FormValue("password")
	next := web.SafeNext(c.FormValue("next"))
	res, err := h.Auth.SignIn(c.Request().Context(), username, password, c.RealIP(), c.Request().UserAgent())

	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		return h.renderSignIn(c, http.StatusTooManyRequests, ui.SignInView{Next: next, Username: username, Error: "locked", LockedMin: lockedMinutes(locked)})
	case errors.Is(err, auth.ErrWrongCredentials), errors.Is(err, auth.ErrNoAdmin):
		return h.renderSignIn(c, http.StatusUnauthorized, ui.SignInView{Next: next, Username: username, Error: "wrong"})
	case err != nil:
		return err
	case res.Pending != "":
		return web.Render(c, http.StatusOK, ui.SignInCode(ui.SignInView{Next: next, Pending: res.Pending}))
	}
	web.SetSessionCookie(c, h.cookieSecure(), res.Session)
	return web.Redirect(c, next)
}

func (h *handler) loginCode(c *echo.Context) error {
	pending, next := c.FormValue("pending"), web.SafeNext(c.FormValue("next"))
	res, err := h.Auth.SignInCode(c.Request().Context(), pending, c.FormValue("code"), c.RealIP(), c.Request().UserAgent())

	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		return h.renderSignIn(c, http.StatusTooManyRequests, ui.SignInView{Next: next, Error: "locked", LockedMin: lockedMinutes(locked)})
	case errors.Is(err, auth.ErrSignInExpired):
		return h.renderSignIn(c, http.StatusUnauthorized, ui.SignInView{Next: next, Error: "code_expired"})
	case errors.Is(err, auth.ErrWrongCode):
		return web.Render(c, http.StatusUnauthorized, ui.SignInCode(ui.SignInView{Next: next, Pending: pending, Error: "code"}))
	case err != nil:
		return err
	}
	web.SetSessionCookie(c, h.cookieSecure(), res.Session)
	return web.Redirect(c, next)
}

// passkeyError is the JSON answer of a refused passkey request; the script
// shows Error.
func passkeyError(c *echo.Context, status int, key string, args ...i18n.Args) error {
	return c.JSON(status, map[string]string{"error": i18n.T(c.Request().Context(), key, args...)})
}

func (h *handler) loginPasskeyOptions(c *echo.Context) error {
	opts, token, err := h.Auth.BeginPasskeySignIn()
	if errors.Is(err, auth.ErrPasskeysOff) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"token": token, "options": json.RawMessage(opts)})
}

// passkeyAnswer is what the script posts back after the browser's prompt.
type passkeyAnswer struct {
	Token      string          `json:"token"`
	Next       string          `json:"next"`
	Credential json.RawMessage `json:"credential"`
}

func readPasskeyAnswer(c *echo.Context) (passkeyAnswer, error) {
	var a passkeyAnswer
	err := json.NewDecoder(io.LimitReader(c.Request().Body, maxPasskeyBody)).Decode(&a)
	return a, err
}

func (h *handler) loginPasskey(c *echo.Context) error {
	a, err := readPasskeyAnswer(c)
	if err != nil {
		return passkeyError(c, http.StatusBadRequest, "auth.passkey.rejected")
	}
	res, err := h.Auth.FinishPasskeySignIn(c.Request().Context(), a.Token, a.Credential, c.RealIP(), c.Request().UserAgent())
	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": i18n.N(c.Request().Context(), "auth.locked", int64(lockedMinutes(locked)))})
	case errors.Is(err, auth.ErrCeremonyExpired):
		return passkeyError(c, http.StatusConflict, "auth.passkey.expired")
	case errors.Is(err, auth.ErrPasskeyRejected):
		return passkeyError(c, http.StatusUnauthorized, "auth.passkey.rejected")
	case errors.Is(err, auth.ErrPasskeysOff):
		return echo.ErrNotFound
	case err != nil:
		return err
	}
	web.SetSessionCookie(c, h.cookieSecure(), res.Session)
	return c.JSON(http.StatusOK, map[string]string{"redirect": web.SafeNext(a.Next)})
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
