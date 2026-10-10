package pages

import (
	"context"
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

// setLanguage changes the admin's language. Settings → General is the only
// place that changes it: the admin menu and the : pop-up have no switch.
func (h *handler) setLanguage(c *echo.Context) error {
	l := i18n.Lang(c.FormValue("lang"))
	if !l.Valid() {
		return echo.NewHTTPError(400, "unknown language")
	}
	if err := h.Auth.SetLanguage(c.Request().Context(), l); err != nil {
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
	if err := h.Settings.Set(ctx, "admin", "general", values); err != nil {
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
}

func (h *handler) securityPage(c *echo.Context) error {
	return h.renderSecurity(c, http.StatusOK, securityView{
		PasswordDone: c.QueryParam("password") == "changed",
		LockoutSaved: c.QueryParam("saved") == "1",
	})
}

func (h *handler) renderSecurity(c *echo.Context, status int, v securityView) error {
	ctx := c.Request().Context()
	q := web.FromContext(ctx)
	sessions, err := h.Auth.Sessions(ctx)
	if err != nil {
		return err
	}
	ended, err := h.Auth.RecentEnded(ctx, 10)
	if err != nil {
		return err
	}
	vals, sec, err := h.sectionValues(ctx, "security", v.LockoutVals)
	if err != nil {
		return err
	}
	s := h.shell(c, i18n.T(ctx, "settings.security"), "/settings")
	view := securityPageView{
		Current: q.Session.ID, Sessions: sessions, Ended: ended,
		PasswordErr: v.PasswordErr, PasswordDone: v.PasswordDone,
		Section: sec, Values: vals, LockoutErrs: v.LockoutErrs, LockoutSaved: v.LockoutSaved,
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
	err := h.Auth.ChangePassword(ctx, web.FromContext(ctx).Session.ID, old, pw)
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
	if err := h.Settings.Set(ctx, "admin", "security", values); err != nil {
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
	if cur := web.FromContext(ctx).Session; cur != nil && cur.ID == id {
		if err := h.Auth.SignOut(ctx, id, "self"); err != nil {
			return err
		}
		web.ClearSessionCookie(c, h.cookieSecure())
		return web.Redirect(c, "/login")
	}
	if err := h.Auth.SignOut(ctx, id, "settings"); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/security")
}

func (h *handler) signOutEverywhere(c *echo.Context) error {
	if _, err := h.Auth.SignOutEverywhere(c.Request().Context()); err != nil {
		return err
	}
	web.ClearSessionCookie(c, h.cookieSecure())
	return web.Redirect(c, "/login")
}
