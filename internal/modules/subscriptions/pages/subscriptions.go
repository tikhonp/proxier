package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// --------------------------------------------------------------- servers

// answerServers re-renders the Servers area for htmx (with the cursor on
// moved, 0 for none) and redirects to the page otherwise.
func (h *handler) answerServers(c *echo.Context, s subs.Subscription, moved int64, stale bool) error {
	if !htmx(c) {
		if stale {
			return echo.NewHTTPError(http.StatusConflict, "the servers changed meanwhile; reload the page")
		}
		return web.Redirect(c, subHref(s.ID))
	}
	v, err := h.serversView(c.Request().Context(), s, moved)
	if err != nil {
		return err
	}
	// htmx swaps only 2xx answers: a stale order answers 200 with the stored
	// order and says why.
	v.Stale = stale
	return web.Render(c, http.StatusOK, serversArea(v))
}

func serverParam(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("server"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

func (h *handler) remove(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	sid, err := serverParam(c)
	if err != nil {
		return err
	}
	err = h.Subs.RemoveServer(c.Request().Context(), s.ID, sid, events.ActorAdmin)
	if errors.Is(err, subs.ErrNotFound) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	return h.answerServers(c, s, 0, false)
}

func (h *handler) move(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	sid, err := serverParam(c)
	if err != nil {
		return err
	}
	err = h.Subs.Move(c.Request().Context(), s.ID, sid, c.FormValue("dir") == "down", events.ActorAdmin)
	if errors.Is(err, subs.ErrStaleOrder) {
		return h.answerServers(c, s, 0, true)
	}
	if err != nil {
		return err
	}
	return h.answerServers(c, s, sid, false)
}

func (h *handler) order(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	var ids []int64
	for _, p := range strings.Split(c.FormValue("order"), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return h.answerServers(c, s, 0, true)
		}
		ids = append(ids, n)
	}
	err = h.Subs.SetOrder(c.Request().Context(), s.ID, ids, events.ActorAdmin)
	if errors.Is(err, subs.ErrStaleOrder) {
		return h.answerServers(c, s, 0, true)
	}
	if err != nil {
		return err
	}
	return h.answerServers(c, s, 0, false)
}

type addRow struct {
	ID         int64
	Flag, Name string
	Kind, Word string
}

type addView struct {
	S       subs.Subscription
	Rows    []addRow
	Err     string
	Checked map[int64]bool
}

func (h *handler) addView(ctx context.Context, s subs.Subscription) (addView, error) {
	list, err := h.Subs.Addable(ctx, s.ID)
	if err != nil {
		return addView{}, err
	}
	v := addView{S: s, Checked: map[int64]bool{}}
	for _, se := range list {
		v.Rows = append(v.Rows, addRowOf(ctx, se))
	}
	return v, nil
}

func addRowOf(ctx context.Context, se servers.ServerEndpoints) addRow {
	return addRow{ID: se.ServerID, Flag: se.Flag, Name: se.Name, Kind: healthKind(se.Health), Word: i18n.T(ctx, "subs.health."+se.Health)}
}

func (h *handler) addPage(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	if !h.Subs.HasCatalog() {
		return echo.ErrNotFound
	}
	v, err := h.addView(c.Request().Context(), s)
	if err != nil {
		return err
	}
	return h.renderAdd(c, http.StatusOK, v)
}

func (h *handler) renderAdd(c *echo.Context, status int, v addView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, addPage(h.shell(c, i18n.T(ctx, "subs.add.title", i18n.Args{"name": v.S.Name}), "/subscriptions"), v))
}

func (h *handler) add(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	form, _ := c.FormValues()
	var ids []int64
	for _, p := range form["server"] {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad server id")
		}
		ids = append(ids, n)
	}
	err = h.Subs.AddServers(ctx, s.ID, ids, events.ActorAdmin)
	if errors.Is(err, subs.ErrNotServed) {
		v, verr := h.addView(ctx, s)
		if verr != nil {
			return verr
		}
		v.Err = i18n.T(ctx, "subs.add.not_served")
		for _, id := range ids {
			v.Checked[id] = true
		}
		return h.renderAdd(c, http.StatusConflict, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, subHref(s.ID))
}

// --------------------------------------------------------------- preview

type previewView struct {
	SubID     int64
	Formats   []string
	Format    string
	Revealed  bool
	Status    string
	Headers   []output.Header
	Text      string // the status line, the headers, a blank line, the lines or the body
	Base64    bool   // masked uri-base64: the lines, with a note
	Notes     []string
	AllHidden bool
	BadFormat bool
}

func (h *handler) previewView(ctx context.Context, s subs.Subscription, format string, reveal bool) (previewView, error) {
	loc := i18n.From(ctx)
	v := previewView{SubID: s.ID, Formats: s.Formats, Revealed: reveal}
	resp, members, err := h.Subs.Preview(ctx, s.ID, format, !reveal)
	if errors.Is(err, output.ErrBadFormat) {
		v.BadFormat = true
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Format = format
	if v.Format == "" {
		v.Format = s.DefaultFormat
	}
	v.Status = "200 OK"
	v.Headers = append([]output.Header{
		{Name: "content-type", Value: "text/plain; charset=utf-8"},
		{Name: "cache-control", Value: "no-store"},
	}, resp.Headers...)
	var b strings.Builder
	b.WriteString(v.Status + "\n")
	for _, hd := range v.Headers {
		b.WriteString(hd.Name + ": " + hd.Value + "\n")
	}
	b.WriteString("\n")
	if reveal {
		b.Write(resp.Body)
	} else {
		v.Base64 = v.Format == "uri-base64"
		for _, l := range resp.Lines {
			b.WriteString(l + "\n")
		}
	}
	v.Text = b.String()
	for _, hd := range resp.Hidden {
		v.Notes = append(v.Notes, i18n.T(ctx, "subs.preview.hidden", i18n.Args{
			"name": hd.Server.Name, "state": i18n.T(ctx, "subs.state."+hd.Server.Health),
			"for": loc.Duration(hd.For), "grace": loc.Duration(s.Hide.Grace),
		}))
	}
	for _, m := range members {
		if !m.InService {
			v.Notes = append(v.Notes, i18n.T(ctx, "subs.preview.out", i18n.Args{"name": m.Name}))
		}
	}
	v.AllHidden = resp.AllHidden
	return v, nil
}

func (h *handler) preview(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	reveal := c.QueryParam("reveal") == "1"
	v, err := h.previewView(c.Request().Context(), s, c.QueryParam("format"), reveal)
	if err != nil {
		return err
	}
	if v.BadFormat {
		return echo.NewHTTPError(http.StatusBadRequest, "format not allowed")
	}
	if reveal {
		c.Response().Header().Set("Cache-Control", "no-store")
	}
	return web.Render(c, http.StatusOK, previewBody(v))
}

// ---------------------------------------------------------------- delete

type deleteView struct {
	S       subs.Subscription
	Servers int
	Live    []string
	Tombs   []string // "Mom (until 7 Nov 2026)"
	Err     string
}

func (h *handler) deleteView(ctx context.Context, s subs.Subscription) (deleteView, error) {
	loc := i18n.From(ctx)
	v := deleteView{S: s}
	members, err := h.Subs.Members(ctx, s.ID)
	if err != nil {
		return v, err
	}
	v.Servers = len(members)
	holders, err := h.Subs.Holders(ctx, s.ID)
	if err != nil {
		return v, err
	}
	for _, x := range holders {
		if x.State == "deleted" {
			v.Tombs = append(v.Tombs, i18n.T(ctx, "subs.delete.tomb", i18n.Args{"name": x.Name, "date": loc.Date(x.Ends)}))
		} else {
			v.Live = append(v.Live, x.Name)
		}
	}
	return v, nil
}

func (h *handler) renderDelete(c *echo.Context, status int, v deleteView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, deletePage(h.shell(c, i18n.T(ctx, "subs.delete.title", i18n.Args{"name": v.S.Name}), "/subscriptions"), v))
}

func (h *handler) deletePage(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	v, err := h.deleteView(c.Request().Context(), s)
	if err != nil {
		return err
	}
	return h.renderDelete(c, http.StatusOK, v)
}

func (h *handler) deletePost(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = h.Subs.Delete(ctx, s.ID, events.ActorAdmin)
	if errors.Is(err, subs.ErrHasLinks) {
		v, verr := h.deleteView(ctx, s)
		if verr != nil {
			return verr
		}
		v.Err = i18n.T(ctx, "subs.delete.refused")
		return h.renderDelete(c, http.StatusConflict, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, "/subscriptions")
}
