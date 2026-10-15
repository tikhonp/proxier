package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func memberHref(id int64) string { return "/settings/members/" + strconv.FormatInt(id, 10) }

// setupURL is the setup link of token.
func (h *handler) setupURL(token string) string {
	return h.Cfg.BaseURL.JoinPath("setup", token).String()
}

// maskSetupURL hides a setup link's token, as link URLs are shown.
func maskSetupURL(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[:i+1] + "•••"
	}
	return "•••"
}

// accessForm is the grid of levels on Add member and on a member's page.
type accessForm struct {
	Grant access.Grant // what the selects show
	// From is what the member has now (none for a new one) and Mine the
	// editor's own grant: a level above Mine can't be given, so its option
	// is off unless the member has it already.
	From, Mine access.Grant
	Disabled   bool
	Errs       map[access.Area]bool // the areas refused as above the editor's level
}

// parseGrant reads the access grid of a form, one select per area named
// level.<area>; an area left out is none.
func parseGrant(c *echo.Context) (access.Grant, error) {
	var g access.Grant
	for _, a := range access.Areas() {
		raw := c.FormValue("level." + string(a))
		if raw == "" {
			continue
		}
		l, err := access.ParseLevel(a, raw)
		if err != nil {
			return g, echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		g = g.With(a, l)
	}
	return g, nil
}

func overreachErrs(err error) map[access.Area]bool {
	var over *auth.OverreachError
	if !errors.As(err, &over) {
		return nil
	}
	out := map[access.Area]bool{}
	for _, a := range over.Areas {
		out[a] = true
	}
	return out
}

type memberRow struct {
	auth.RosterEntry
	Me bool
}

func (h *handler) membersPage(c *echo.Context) error {
	ctx := c.Request().Context()
	roster, err := h.Auth.Roster(ctx)
	if err != nil {
		return err
	}
	me := web.FromContext(ctx).Member.ID
	var in, removed []memberRow
	for _, r := range roster {
		row := memberRow{RosterEntry: r, Me: r.ID == me}
		if r.Removed() {
			removed = append(removed, row)
		} else {
			in = append(in, row)
		}
	}
	s := h.shell(c, i18n.T(ctx, "settings.members"), "/settings")
	return web.Render(c, http.StatusOK, membersPage(s, h.sortedPages(), in, removed, c.QueryParam("removed")))
}

// memberNewView is Add member: what was typed, and why it was refused.
type memberNewView struct {
	Username string
	Access   accessForm
	Err      string // i18n key
}

func (h *handler) memberNewPage(c *echo.Context) error {
	q := web.FromContext(c.Request().Context())
	return h.renderMemberNew(c, http.StatusOK, memberNewView{Access: accessForm{Grant: access.PresetViewer.Grant().Within(q.Grant), Mine: q.Grant}})
}

func (h *handler) renderMemberNew(c *echo.Context, status int, v memberNewView) error {
	ctx := c.Request().Context()
	s := h.shell(c, i18n.T(ctx, "members.new"), "/settings")
	return web.Render(c, status, memberNewPage(s, h.sortedPages(), v))
}

// memberAdd adds the member and opens their page, with the setup link to send.
func (h *handler) memberAdd(c *echo.Context) error {
	ctx := c.Request().Context()
	q := web.FromContext(ctx)
	g, err := parseGrant(c)
	if err != nil {
		return err
	}
	v := memberNewView{Username: c.FormValue("username"), Access: accessForm{Grant: g, Mine: q.Grant}}
	id, _, err := h.Auth.AddMember(ctx, *q.Session, c.FormValue("password"), v.Username, g)
	switch {
	case err == nil:
		return web.Redirect(c, memberHref(id)+"?added=1")
	case errors.Is(err, auth.ErrWrongCredentials):
		v.Err = "members.err.password"
	case errors.Is(err, auth.ErrUsernameTaken):
		v.Err = "members.err.taken"
	case errors.Is(err, auth.ErrUsernameLength):
		v.Err = "members.err.username"
	case overreachErrs(err) != nil:
		v.Err, v.Access.Errs = "members.err.above", overreachErrs(err)
	case errors.Is(err, auth.ErrNotAllowed):
		return access.Forbidden(access.Settings)
	default:
		return err
	}
	return h.renderMemberNew(c, http.StatusUnprocessableEntity, v)
}

// memberView is a member's page.
type memberView struct {
	M         auth.RosterEntry
	Me        bool
	Access    accessForm
	SetupURL  string  // the working setup link, "" without one
	Expires   db.Time // of the open link, working or not
	Added     bool    // just added: the link is shown to copy now
	Done      string  // "access" | "link"
	Err       string  // i18n key
	LinkErr   string  // i18n key, of New setup link
	CreatedBy string
}

func (h *handler) memberPage(c *echo.Context) error {
	v := memberView{Added: c.QueryParam("added") == "1"}
	if d := c.QueryParam("done"); d == "access" || d == "link" {
		v.Done = d
	}
	return h.renderMember(c, http.StatusOK, v, nil)
}

// renderMember shows a member; grant, when set, is what a refused save typed.
func (h *handler) renderMember(c *echo.Context, status int, v memberView, grant *access.Grant) error {
	ctx := c.Request().Context()
	q := web.FromContext(ctx)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	roster, err := h.Auth.Roster(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, r := range roster {
		if r.ID == id {
			v.M, found = r, true
		}
	}
	if !found {
		return echo.ErrNotFound
	}
	v.Me = id == q.Member.ID
	v.CreatedBy = web.ActorName(ctx, v.M.CreatedBy)
	g := v.M.Grant
	if grant != nil {
		g = *grant
	}
	errs := v.Access.Errs
	v.Access = accessForm{Grant: g, From: v.M.Grant, Mine: q.Grant, Disabled: v.Me || v.M.Removed(), Errs: errs}
	if !v.M.Removed() && !v.M.SetUp() {
		token, expires, ok, err := h.Auth.SetupLinkToken(ctx, id)
		if err != nil {
			return err
		}
		v.Expires = expires
		if ok {
			v.SetupURL = h.setupURL(token)
		}
		// the link is a secret: the page isn't kept
		c.Response().Header().Set("Cache-Control", "no-store")
	}
	s := h.shell(c, v.M.Username, "/settings")
	return web.Render(c, status, memberPage(s, h.sortedPages(), v))
}

func (h *handler) memberID(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

func (h *handler) memberAccess(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := h.memberID(c)
	if err != nil {
		return err
	}
	g, err := parseGrant(c)
	if err != nil {
		return err
	}
	err = h.Auth.SetAccess(ctx, web.FromContext(ctx).Member.ID, id, g)
	var v memberView
	switch {
	case err == nil:
		return web.Redirect(c, memberHref(id)+"?done=access")
	case errors.Is(err, auth.ErrNoMember):
		return echo.ErrNotFound
	case errors.Is(err, auth.ErrOwnAccess):
		v.Err = "members.err.own"
	case errors.Is(err, auth.ErrLastSettingsManager):
		v.Err = "members.err.last"
	case overreachErrs(err) != nil:
		v.Err, v.Access.Errs = "members.err.above", overreachErrs(err)
	case errors.Is(err, auth.ErrNotAllowed):
		return access.Forbidden(access.Settings)
	default:
		return err
	}
	return h.renderMember(c, http.StatusUnprocessableEntity, v, &g)
}

func (h *handler) memberSetupLink(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := h.memberID(c)
	if err != nil {
		return err
	}
	_, err = h.Auth.NewSetupLink(ctx, *web.FromContext(ctx).Session, c.FormValue("password"), id)
	var v memberView
	switch {
	case err == nil:
		return web.Redirect(c, memberHref(id)+"?done=link")
	case errors.Is(err, auth.ErrNoMember):
		return echo.ErrNotFound
	case errors.Is(err, auth.ErrWrongCredentials):
		v.LinkErr = "members.err.password"
	case errors.Is(err, auth.ErrAlreadySetUp):
		return web.Redirect(c, memberHref(id))
	case errors.Is(err, auth.ErrNotAllowed):
		return access.Forbidden(access.Settings)
	default:
		return err
	}
	return h.renderMember(c, http.StatusUnprocessableEntity, v, nil)
}

func (h *handler) memberRemove(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := h.memberID(c)
	if err != nil {
		return err
	}
	r, err := h.Auth.RemoveMember(ctx, web.FromContext(ctx).Member.ID, id)
	switch {
	case err == nil:
		// removing yourself ends your own session: the next page is sign-in
		return web.Redirect(c, "/settings/members?"+url.Values{"removed": {r.Username}}.Encode())
	case errors.Is(err, auth.ErrNoMember):
		return echo.ErrNotFound
	case errors.Is(err, auth.ErrLastSettingsManager):
		return h.renderMember(c, http.StatusConflict, memberView{Err: "members.err.last_remove"}, nil)
	case errors.Is(err, auth.ErrNotAllowed):
		return access.Forbidden(access.Settings)
	}
	return err
}

// setupPage is where a new member chooses their password. It is on the
// open chain: the new member has no session. A browser signed in as
// someone else is refused, so nobody sets up a member by accident in
// their own session.
func (h *handler) setupPage(c *echo.Context) error {
	return h.renderSetup(c, http.StatusOK, "")
}

func (h *handler) renderSetup(c *echo.Context, status int, errKey string) error {
	ctx := c.Request().Context()
	c.Response().Header().Set("Cache-Control", "no-store")
	token := c.Param("token")
	inv, err := h.Auth.SetupLink(ctx, token)
	if errors.Is(err, auth.ErrSetupLink) {
		return web.Render(c, http.StatusNotFound, ui.SetupGone())
	}
	if err != nil {
		return err
	}
	if q := web.FromContext(ctx); q.Member != nil {
		return web.Render(c, http.StatusConflict, ui.Setup(ui.SetupView{Username: inv.Username, SignedIn: q.Member.Username}))
	}
	return web.Render(c, status, ui.Setup(ui.SetupView{Token: token, Username: inv.Username, Err: errKey}))
}

func (h *handler) setupPost(c *echo.Context) error {
	ctx := c.Request().Context()
	if web.FromContext(ctx).Member != nil {
		return h.renderSetup(c, http.StatusConflict, "")
	}
	pw := c.FormValue("password")
	if pw != c.FormValue("again") {
		return h.renderSetup(c, http.StatusUnprocessableEntity, "settings.password.mismatch")
	}
	res, err := h.Auth.CompleteSetup(ctx, c.Param("token"), pw, i18n.From(ctx).Lang, c.RealIP(), c.Request().UserAgent())
	switch {
	case err == nil:
		web.SetSessionCookie(c, h.cookieSecure(), res.Session)
		return web.Redirect(c, "/")
	case errors.Is(err, auth.ErrSetupLink):
		return web.Render(c, http.StatusNotFound, ui.SetupGone())
	case errors.Is(err, auth.ErrPasswordTooShort):
		return h.renderSetup(c, http.StatusUnprocessableEntity, "settings.password.short")
	case errors.Is(err, auth.ErrPasswordTooLong):
		return h.renderSetup(c, http.StatusUnprocessableEntity, "settings.password.long")
	}
	return err
}
