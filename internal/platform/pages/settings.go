package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) sortedPages() []ui.SettingsPage {
	out := append([]ui.SettingsPage(nil), h.SettingsPages...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// sectionValues reads the current values of a section; submitted overrides
// them (a refused save shows what was typed).
func (h *handler) sectionValues(ctx context.Context, name string, submitted map[string]string) ([]settings.Value, settings.Section, error) {
	var sec settings.Section
	for _, s := range h.Settings.Sections() {
		if s.Name == name {
			sec = s
		}
	}
	vals := make([]settings.Value, 0, len(sec.Fields))
	for _, f := range sec.Fields {
		v, err := h.Settings.Lookup(ctx, f.Key)
		if err != nil {
			return nil, sec, err
		}
		if raw, ok := submitted[f.Key]; ok && f.Kind != settings.Secret {
			v.Value = raw
		}
		vals = append(vals, v)
	}
	return vals, sec, nil
}

func (h *handler) submitted(c *echo.Context, sec settings.Section) map[string]string {
	out := map[string]string{}
	form, _ := c.FormValues()
	for _, f := range sec.Fields {
		if vs, ok := form[f.Key]; ok && len(vs) > 0 {
			out[f.Key] = vs[0]
		}
	}
	return out
}

func (h *handler) generalPage(c *echo.Context) error {
	return h.renderGeneral(c, http.StatusOK, nil, nil, c.QueryParam("saved") == "1")
}

// setLanguage changes the member's own language. Settings → General is the
// only place that changes it: the header and the : pop-up have no switch.
func (h *handler) setLanguage(c *echo.Context) error {
	l := i18n.Lang(c.FormValue("lang"))
	if !l.Valid() {
		return echo.NewHTTPError(400, "unknown language")
	}
	if err := h.Auth.SetLanguage(c.Request().Context(), web.FromContext(c.Request().Context()).Member.ID, l); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/general?language=saved")
}

func (h *handler) renderGeneral(c *echo.Context, status int, submitted map[string]string, errs settings.FieldErrors, saved bool) error {
	ctx := c.Request().Context()
	vals, sec, err := h.sectionValues(ctx, "general", submitted)
	if err != nil {
		return err
	}
	s := h.shell(c, i18n.T(ctx, "settings.general"), "/settings")
	lang := generalLang{Current: s.Lang, Saved: c.QueryParam("language") == "saved"}
	return web.Render(c, status, generalPage(s, h.sortedPages(), sec, vals, errs, saved, h.Cfg.BaseURL.String(), lang))
}

func (h *handler) generalSave(c *echo.Context) error {
	ctx := c.Request().Context()
	var sec settings.Section
	for _, s := range h.Settings.Sections() {
		if s.Name == "general" {
			sec = s
		}
	}
	values := h.submitted(c, sec)
	if err := h.Settings.Set(ctx, web.Actor(ctx), "general", values); err != nil {
		if fe, ok := err.(settings.FieldErrors); ok {
			return h.renderGeneral(c, http.StatusUnprocessableEntity, values, fe, false)
		}
		return err
	}
	return web.Redirect(c, "/settings/general?saved=1")
}

// securityView is everything the Security page can show at once.
type securityView struct {
	PasswordErr  string // i18n key, "" none
	PasswordDone bool
	LockoutErrs  settings.FieldErrors
	LockoutVals  map[string]string
	LockoutSaved bool
	TOTPSetup    *auth.TOTPSetup
	TOTPErr      string // i18n key
	TOTPDone     string
	PasskeyErr   string // i18n key
	PasskeyDone  string
}

func (h *handler) securityPage(c *echo.Context) error {
	v := securityView{
		PasswordDone: c.QueryParam("password") == "changed",
		LockoutSaved: c.QueryParam("saved") == "1",
	}
	if t := c.QueryParam("totp"); t == "on" || t == "off" {
		v.TOTPDone = t
	}
	if p := c.QueryParam("passkey"); p == "added" || p == "removed" {
		v.PasskeyDone = p
	}
	return h.renderSecurity(c, http.StatusOK, v)
}

func (h *handler) renderSecurity(c *echo.Context, status int, v securityView) error {
	ctx := c.Request().Context()
	q := web.FromContext(ctx)
	sessions, err := h.Auth.Sessions(ctx, q.Member.ID)
	if err != nil {
		return err
	}
	ended, err := h.Auth.RecentEnded(ctx, q.Member.ID, 10)
	if err != nil {
		return err
	}
	vals, sec, err := h.sectionValues(ctx, "security", v.LockoutVals)
	if err != nil {
		return err
	}
	totpOn, err := h.Auth.TOTPEnabled(ctx, q.Member.ID)
	if err != nil {
		return err
	}
	passkeys, err := h.Auth.Passkeys(ctx, q.Member.ID)
	if err != nil {
		return err
	}
	s := h.shell(c, i18n.T(ctx, "settings.security"), "/settings")
	view := securityPageView{
		Current: q.Session.ID, Sessions: sessions, Ended: ended,
		PasswordErr: v.PasswordErr, PasswordDone: v.PasswordDone,
		Section: sec, Values: vals, LockoutErrs: v.LockoutErrs, LockoutSaved: v.LockoutSaved,
		TOTPOn: totpOn, TOTPSetup: v.TOTPSetup, TOTPErr: v.TOTPErr, TOTPDone: v.TOTPDone,
		PasskeysOn: h.Auth.PasskeysOn(), Passkeys: passkeys, PasskeyErr: v.PasskeyErr, PasskeyDone: v.PasskeyDone,
	}
	return web.Render(c, status, securityPage(s, h.sortedPages(), view))
}

func (h *handler) changePassword(c *echo.Context) error {
	ctx := c.Request().Context()
	old, pw, again := c.FormValue("old"), c.FormValue("new"), c.FormValue("again")
	fail := func(key string) error {
		return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{PasswordErr: key})
	}
	if pw != again {
		return fail("settings.password.mismatch")
	}
	err := h.Auth.ChangePassword(ctx, *web.FromContext(ctx).Session, old, pw)
	switch err {
	case nil:
		return web.Redirect(c, "/settings/security?password=changed")
	case auth.ErrWrongCredentials:
		return fail("settings.password.wrong")
	case auth.ErrPasswordTooShort:
		return fail("settings.password.short")
	case auth.ErrPasswordTooLong:
		return fail("settings.password.long")
	}
	return err
}

func (h *handler) saveLockout(c *echo.Context) error {
	ctx := c.Request().Context()
	var sec settings.Section
	for _, s := range h.Settings.Sections() {
		if s.Name == "security" {
			sec = s
		}
	}
	values := h.submitted(c, sec)
	if err := h.Settings.Set(ctx, web.Actor(ctx), "security", values); err != nil {
		if fe, ok := err.(settings.FieldErrors); ok {
			return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{LockoutErrs: fe, LockoutVals: values})
		}
		return err
	}
	return web.Redirect(c, "/settings/security?saved=1")
}

func (h *handler) signOutSession(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	q := web.FromContext(ctx)
	if q.Session.ID == id {
		if err := h.Auth.SignOut(ctx, q.Member.ID, id, "self"); err != nil {
			return err
		}
		web.ClearSessionCookie(c, h.cookieSecure())
		return web.Redirect(c, "/login")
	}
	if err := h.Auth.SignOut(ctx, q.Member.ID, id, "settings"); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/security")
}

func (h *handler) signOutEverywhere(c *echo.Context) error {
	if _, err := h.Auth.SignOutEverywhere(c.Request().Context(), web.FromContext(c.Request().Context()).Member.ID); err != nil {
		return err
	}
	web.ClearSessionCookie(c, h.cookieSecure())
	return web.Redirect(c, "/login")
}

func (h *handler) totpSetup(c *echo.Context) error {
	ctx := c.Request().Context()
	setup, err := h.Auth.BeginTOTP(ctx, *web.FromContext(ctx).Session, c.FormValue("password"))
	switch {
	case errors.Is(err, auth.ErrWrongCredentials):
		return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{TOTPErr: "settings.password.wrong"})
	case errors.Is(err, auth.ErrTOTPOn):
		return web.Redirect(c, "/settings/security")
	case err != nil:
		return err
	}
	return h.renderSecurity(c, http.StatusOK, securityView{TOTPSetup: &setup})
}

func (h *handler) totpEnable(c *echo.Context) error {
	ctx := c.Request().Context()
	session, token := *web.FromContext(ctx).Session, c.FormValue("token")
	err := h.Auth.EnableTOTP(ctx, session, token, c.FormValue("code"))
	switch {
	case err == nil:
		return web.Redirect(c, "/settings/security?totp=on")
	case errors.Is(err, auth.ErrWrongCode):
		setup, rerr := h.Auth.ResumeTOTP(ctx, session, token)
		if rerr != nil {
			return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{TOTPErr: "settings.totp.expired"})
		}
		return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{TOTPSetup: &setup, TOTPErr: "settings.totp.wrong"})
	case errors.Is(err, auth.ErrSetupExpired), errors.Is(err, auth.ErrSetupInvalid):
		return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{TOTPErr: "settings.totp.expired"})
	case errors.Is(err, auth.ErrTOTPOn):
		return web.Redirect(c, "/settings/security")
	}
	return err
}

func (h *handler) totpDisable(c *echo.Context) error {
	err := h.Auth.DisableTOTP(c.Request().Context(), web.FromContext(c.Request().Context()).Member.ID, c.FormValue("password"))
	switch {
	case err == nil:
		return web.Redirect(c, "/settings/security?totp=off")
	case errors.Is(err, auth.ErrWrongCredentials):
		return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{TOTPErr: "settings.password.wrong"})
	case errors.Is(err, auth.ErrTOTPOff):
		return web.Redirect(c, "/settings/security")
	}
	return err
}

// passkeyOptions starts adding a passkey: the script posts the form here and
// hands the options to navigator.credentials.create.
func (h *handler) passkeyOptions(c *echo.Context) error {
	ctx := c.Request().Context()
	opts, token, err := h.Auth.BeginPasskey(ctx, *web.FromContext(ctx).Session, c.FormValue("name"), c.FormValue("password"))
	switch {
	case errors.Is(err, auth.ErrWrongCredentials):
		return passkeyError(c, http.StatusUnprocessableEntity, "settings.password.wrong")
	case errors.Is(err, auth.ErrPasskeyName):
		return passkeyError(c, http.StatusUnprocessableEntity, "settings.passkeys.bad_name")
	case errors.Is(err, auth.ErrPasskeysOff):
		return echo.ErrNotFound
	case err != nil:
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"token": token, "options": json.RawMessage(opts)})
}

func (h *handler) passkeyAdd(c *echo.Context) error {
	ctx := c.Request().Context()
	a, err := readPasskeyAnswer(c)
	if err != nil {
		return passkeyError(c, http.StatusBadRequest, "auth.passkey.rejected")
	}
	err = h.Auth.FinishPasskey(ctx, *web.FromContext(ctx).Session, a.Token, a.Credential)
	switch {
	case errors.Is(err, auth.ErrCeremonyExpired):
		return passkeyError(c, http.StatusConflict, "auth.passkey.expired")
	case errors.Is(err, auth.ErrPasskeyRejected):
		return passkeyError(c, http.StatusUnprocessableEntity, "auth.passkey.rejected")
	case errors.Is(err, auth.ErrPasskeysOff):
		return echo.ErrNotFound
	case err != nil:
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"redirect": "/settings/security?passkey=added"})
}

func (h *handler) passkeyRemove(c *echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	err = h.Auth.RemovePasskey(c.Request().Context(), web.FromContext(c.Request().Context()).Member.ID, id, c.FormValue("password"))
	switch {
	case err == nil:
		return web.Redirect(c, "/settings/security?passkey=removed")
	case errors.Is(err, auth.ErrWrongCredentials):
		return h.renderSecurity(c, http.StatusUnprocessableEntity, securityView{PasskeyErr: "settings.password.wrong"})
	case errors.Is(err, auth.ErrNoPasskey):
		return h.renderSecurity(c, http.StatusNotFound, securityView{PasskeyErr: "settings.passkeys.gone"})
	}
	return err
}
