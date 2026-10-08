// Package pages holds the subscriptions module's handlers and templ files.
package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/alerts"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// Deps are what the pages use.
type Deps struct {
	Subs     *subs.Service
	Links    *links.Service
	Alerts   *alerts.Service
	DB       *db.DB
	Settings *settings.Store
	Now      func() time.Time
}

type handler struct {
	Deps
	shell         func(c *echo.Context, title, path string) ui.Shell
	settingsPages func() []ui.SettingsPage
}

// Register adds the module's routes.
func Register(r web.Routes, d Deps) {
	h := &handler{Deps: d, shell: r.Shell, settingsPages: r.SettingsPages}
	r.Admin.GET("/subscriptions", h.list)
	r.Admin.GET("/subscriptions/new", h.newPage)
	r.Admin.POST("/subscriptions", h.create)
	r.Admin.GET("/subscriptions/:id", h.page)
	r.Admin.POST("/subscriptions/:id", h.save)
	r.Admin.GET("/subscriptions/:id/servers/add", h.addPage)
	r.Admin.POST("/subscriptions/:id/servers", h.add)
	r.Admin.POST("/subscriptions/:id/servers/order", h.order)
	r.Admin.POST("/subscriptions/:id/servers/:server/remove", h.remove)
	r.Admin.POST("/subscriptions/:id/servers/:server/move", h.move)
	r.Admin.GET("/subscriptions/:id/preview", h.preview)
	r.Admin.GET("/subscriptions/:id/delete", h.deletePage)
	r.Admin.POST("/subscriptions/:id/delete", h.deletePost)
	r.Admin.POST("/subscriptions/:id/move-links", h.moveLinks)

	r.Admin.GET("/links", h.linkList)
	r.Admin.GET("/links/new", h.newLinkPage)
	r.Admin.POST("/links", h.createLink)
	r.Admin.GET("/links/:id", h.linkPage)
	r.Admin.GET("/links/:id/url", h.linkURL)
	r.Admin.GET("/links/:id/qr", h.linkQR)
	r.Admin.POST("/links/:id/disable", h.act(h.Links.Disable, ""))
	r.Admin.POST("/links/:id/enable", h.act(h.Links.Enable, ""))
	r.Admin.POST("/links/:id/regenerate", h.act(h.Links.RegenerateToken, "?regenerated=1"))
	r.Admin.GET("/links/:id/subscription", h.formPage("subscription"))
	r.Admin.POST("/links/:id/subscription", h.changeSubscription)
	r.Admin.GET("/links/:id/expiry", h.formPage("expiry"))
	r.Admin.POST("/links/:id/expiry", h.setExpiry)
	r.Admin.GET("/links/:id/edit", h.formPage("edit"))
	r.Admin.POST("/links/:id/edit", h.editLink)
	r.Admin.POST("/links/:id/delete", h.deleteLink)
	r.Admin.GET("/links/:id/cutoff", h.cutoffPlan)
	r.Admin.POST("/links/:id/cutoff", h.cutoffStart)
	r.Admin.GET("/links/:id/cutoff/status", h.cutoffStatus)
	r.Admin.POST("/links/:id/cutoff/:position/retry", h.cutoffRetry)
	r.Admin.GET("/links/:id/alerts", h.alertsPage)
	r.Admin.POST("/links/:id/alerts", h.saveAlerts)

	r.Admin.GET("/settings/subscriptions", h.settingsPage)
	r.Admin.POST("/settings/subscriptions", h.saveSettings)
}

func subHref(id int64) string { return "/subscriptions/" + strconv.FormatInt(id, 10) }

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func htmx(c *echo.Context) bool { return c.Request().Header.Get("HX-Request") == "true" }

// load reads the subscription of the :id parameter; 404 when there is none.
func (h *handler) load(c *echo.Context) (subs.Subscription, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return subs.Subscription{}, echo.ErrNotFound
	}
	s, err := h.Subs.Get(c.Request().Context(), id)
	if errors.Is(err, subs.ErrNotFound) {
		return s, echo.ErrNotFound
	}
	return s, err
}

// healthKind is the marker of a health state.
func healthKind(state string) string {
	return map[string]string{"healthy": "ok", "degraded": "look", "blocked": "blocked", "down": "broken", "unknown": "unknown", "paused": "paused"}[state]
}

// errText translates field errors (i18n keys) for the form.
func errText(ctx context.Context, fe store.FieldErrors) map[string]string {
	out := map[string]string{}
	for k, v := range fe {
		out[k] = i18n.T(ctx, v)
	}
	return out
}

// ---------------------------------------------------------------- the list

type chip struct {
	Kind, Word, Name string
}

type rowView struct {
	ID          int64
	Name, Title string
	Servers     []chip
	HiddenNow   []string
	Links       int
	Formats     string
	Hide        string
}

func (h *handler) list(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	rows, err := h.Subs.List(ctx)
	if err != nil {
		return err
	}
	var v []rowView
	for _, r := range rows {
		rv := rowView{ID: r.ID, Name: r.Name, Title: r.Title, Links: r.Links, Formats: formatsText(ctx, r.Subscription)}
		rv.Hide = i18n.T(ctx, "subs.hide.off")
		if r.Hide.On {
			rv.Hide = i18n.T(ctx, "subs.hide.on", i18n.Args{"grace": loc.Duration(r.Hide.Grace)})
			if r.Hide.Grace == 0 {
				rv.Hide = i18n.T(ctx, "subs.hide.on_now")
			}
		}
		var in []output.Server
		for _, m := range r.Members {
			if !m.InService {
				rv.Servers = append(rv.Servers, chip{Kind: "gone", Word: i18n.T(ctx, "subs.member.out"), Name: m.Name})
				continue
			}
			in = append(in, m.Server)
			rv.Servers = append(rv.Servers, chip{Kind: healthKind(m.Server.Health), Word: i18n.T(ctx, "subs.health."+m.Server.Health), Name: m.Name})
		}
		_, hidden, _ := output.Select(in, r.Hide, h.Now())
		for _, hd := range hidden {
			rv.HiddenNow = append(rv.HiddenNow, hd.Server.Name)
		}
		v = append(v, rv)
	}
	return web.Render(c, http.StatusOK, listPage(h.shell(c, i18n.T(ctx, "subs.title"), "/subscriptions"), v))
}

func formatsText(ctx context.Context, s subs.Subscription) string {
	out := ""
	for i, f := range s.Formats {
		if i > 0 {
			out += ", "
		}
		out += f
		if f == s.DefaultFormat {
			out += " " + i18n.T(ctx, "subs.formats.default_mark")
		}
	}
	return out
}

// ------------------------------------------------------------------ create

type newView struct {
	Name, Title, Description string
	Errs                     map[string]string
}

func (h *handler) newPage(c *echo.Context) error {
	return h.renderNew(c, http.StatusOK, newView{})
}

func (h *handler) renderNew(c *echo.Context, status int, v newView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, newPage(h.shell(c, i18n.T(ctx, "subs.new"), "/subscriptions"), v))
}

func (h *handler) create(c *echo.Context) error {
	ctx := c.Request().Context()
	v := newView{Name: c.FormValue("name"), Title: c.FormValue("title"), Description: c.FormValue("description")}
	id, err := h.Subs.Create(ctx, v.Name, v.Title, v.Description, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderNew(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, subHref(id))
}

// ---------------------------------------------------------------- the page

type memberRow struct {
	ID         int64
	Pos        int
	Name, Flag string
	Kind, Word string
	Out        string
	OutClass   string
	First      bool
	Last       bool
	Cursor     bool
}

type serversView struct {
	SubID    int64
	Name     string
	Rows     []memberRow
	CanAdd   bool
	AddWhy   string // why Add servers is disabled
	ShowAdd  bool   // false without a catalog
	Stale    bool
	OrderCSV string
}

type formView struct {
	Name, Title, Description string
	Formats                  map[string]bool
	Default                  string
	UpdateHours              string
	HideOn                   bool
	States                   map[string]bool
	Grace                    string
	AutoAdd                  bool
}

type eventLine struct{ Time, Text string }

type pageView struct {
	S        subs.Subscription
	Status   string
	Saved    string // "", "1" saved, "0" no changes
	Servers  serversView
	Form     formView
	Errs     map[string]string
	Preview  previewView
	Links    int
	LinkRows []subLinkRow
	Activity []eventLine
}

func formOf(s subs.Subscription) formView {
	f := formView{
		Name: s.Name, Title: s.Title, Description: s.Description, Default: s.DefaultFormat,
		UpdateHours: strconv.Itoa(s.UpdateHours), HideOn: s.Hide.On, Grace: strconv.Itoa(int(s.Hide.Grace / time.Minute)),
		AutoAdd: s.AutoAdd, Formats: map[string]bool{}, States: map[string]bool{},
	}
	for _, x := range s.Formats {
		f.Formats[x] = true
	}
	for _, x := range s.Hide.States {
		f.States[x] = true
	}
	return f
}

func (h *handler) serversView(ctx context.Context, s subs.Subscription, cursor int64) (serversView, error) {
	loc := i18n.From(ctx)
	members, err := h.Subs.Members(ctx, s.ID)
	if err != nil {
		return serversView{}, err
	}
	v := serversView{SubID: s.ID, Name: s.Name, ShowAdd: h.Subs.HasCatalog()}
	if v.ShowAdd {
		addable, err := h.Subs.Addable(ctx, s.ID)
		if err != nil {
			return v, err
		}
		v.CanAdd = len(addable) > 0
		if !v.CanAdd {
			v.AddWhy = i18n.T(ctx, "subs.add.nothing")
		}
	}
	var in []output.Server
	for _, m := range members {
		if m.InService {
			in = append(in, m.Server)
		}
	}
	_, hidden, _ := output.Select(in, s.Hide, h.Now())
	since := map[int64]time.Time{}
	for _, hd := range hidden {
		since[hd.Server.ID] = hd.Server.HealthSince
	}
	for i, m := range members {
		r := memberRow{ID: m.ServerID, Pos: m.Position, Name: m.Name, Flag: m.Flag, First: i == 0, Last: i == len(members)-1, Cursor: m.ServerID == cursor}
		switch t, isHidden := since[m.ServerID]; {
		case !m.InService:
			r.Kind, r.Word, r.Out, r.OutClass = "gone", "—", i18n.T(ctx, "subs.member.out"), "muted"
		case isHidden:
			r.Kind, r.Word = healthKind(m.Server.Health), i18n.T(ctx, "subs.health."+m.Server.Health)
			r.Out, r.OutClass = i18n.T(ctx, "subs.member.hidden_since", i18n.Args{"time": loc.Time(t)}), healthClass(m.Server.Health)
		default:
			r.Kind, r.Word = healthKind(m.Server.Health), i18n.T(ctx, "subs.health."+m.Server.Health)
			r.Out, r.OutClass = i18n.T(ctx, "subs.member.served"), "subtle"
		}
		if i > 0 {
			v.OrderCSV += ","
		}
		v.OrderCSV += i64(m.ServerID)
		v.Rows = append(v.Rows, r)
	}
	return v, nil
}

// healthClass colours the "hidden since" text in the state's colour.
func healthClass(state string) string {
	return map[string]string{"blocked": "iris", "down": "love", "degraded": "gold"}[state]
}

func (h *handler) pageView(c *echo.Context, s subs.Subscription) (pageView, error) {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	v := pageView{S: s, Form: formOf(s), Saved: c.QueryParam("saved")}
	var err error
	if v.Servers, err = h.serversView(ctx, s, 0); err != nil {
		return v, err
	}
	if v.Preview, err = h.previewView(ctx, s, "", false); err != nil {
		return v, err
	}
	if v.LinkRows, err = h.subLinks(ctx, s.ID); err != nil {
		return v, err
	}
	v.Links = len(v.LinkRows)
	v.Status = i18n.T(ctx, "subs.status", i18n.Args{"title": s.Title}) + " · " +
		loc.N("subs.n_servers", int64(len(v.Servers.Rows))) + " · " + loc.N("subs.n_links", int64(v.Links))
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: subs.Subject(s.ID), Limit: 10})
	if err != nil {
		return v, err
	}
	for _, e := range list {
		args := i18n.Args{"subject": s.Name, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		text := e.Type
		if key := "event." + e.Type; loc.Has(key) {
			text = loc.T(key, args)
		}
		if e.Type == "subscription.servers_changed" {
			text = serversChanged(ctx, e.Payload)
		}
		v.Activity = append(v.Activity, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return v, nil
}

func (h *handler) renderPage(c *echo.Context, status int, v pageView) error {
	return web.Render(c, status, subscriptionPage(h.shell(c, v.S.Name, "/subscriptions"), v))
}

func (h *handler) page(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	v, err := h.pageView(c, s)
	if err != nil {
		return err
	}
	return h.renderPage(c, http.StatusOK, v)
}

func (h *handler) save(c *echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	form, _ := c.FormValues()
	st := subs.Settings{
		Name: c.FormValue("name"), Title: c.FormValue("title"), Description: c.FormValue("description"),
		Formats: form["format"], DefaultFormat: c.FormValue("default_format"),
		HideOn: c.FormValue("hide") == "1", HideStates: form["hide_state"], AutoAdd: c.FormValue("auto_add") == "1",
	}
	// a number that doesn't parse is out of bounds: the service words the error
	if st.UpdateHours, err = strconv.Atoi(strings.TrimSpace(c.FormValue("update_hours"))); err != nil {
		st.UpdateHours = 0
	}
	if st.GraceMinutes, err = strconv.Atoi(strings.TrimSpace(c.FormValue("grace"))); err != nil {
		st.GraceMinutes = -1
	}
	changed, err := h.Subs.Update(ctx, s.ID, st, events.ActorAdmin)
	var errs store.FieldErrors
	if errors.As(err, &errs) {
		v, verr := h.pageView(c, s)
		if verr != nil {
			return verr
		}
		v.Saved = ""
		v.Errs = errText(ctx, errs)
		// keep what was typed
		v.Form = formView{
			Name: st.Name, Title: st.Title, Description: st.Description, Default: st.DefaultFormat,
			UpdateHours: c.FormValue("update_hours"), HideOn: st.HideOn, Grace: c.FormValue("grace"), AutoAdd: st.AutoAdd,
			Formats: map[string]bool{}, States: map[string]bool{},
		}
		for _, x := range st.Formats {
			v.Form.Formats[x] = true
		}
		for _, x := range st.HideStates {
			v.Form.States[x] = true
		}
		return h.renderPage(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	saved := "0"
	if changed {
		saved = "1"
	}
	return web.Redirect(c, subHref(s.ID)+"?saved="+saved)
}

// serversChanged words a servers_changed event without its empty fields.
func serversChanged(ctx context.Context, p map[string]any) string {
	str := func(k string) string { s, _ := p[k].(string); return s }
	var parts []string
	if a := str("added"); a != "" {
		key := "subs.event.added"
		if auto, _ := p["auto"].(bool); auto {
			key = "subs.event.added_auto"
		}
		parts = append(parts, i18n.T(ctx, key, i18n.Args{"names": a}))
	}
	if r := str("removed"); r != "" {
		key := "subs.event.removed"
		if str("reason") == "retired" {
			key = "subs.event.retired"
		}
		parts = append(parts, i18n.T(ctx, key, i18n.Args{"names": r}))
	}
	if re, _ := p["reordered"].(bool); re {
		parts = append(parts, i18n.T(ctx, "subs.event.reordered"))
	}
	return strings.Join(parts, "; ")
}
