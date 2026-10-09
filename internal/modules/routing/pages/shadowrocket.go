package pages

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/shadowrocket"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const srPath = "/routing/shadowrocket"

func srHref(id int64) string { return srPath + "/" + i64(id) }

// registerShadowrocket adds the config pages' routes.
func (h *handler) registerShadowrocket(r web.Routes) {
	r.Admin.GET(srPath, h.srList)
	r.Admin.GET(srPath+"/new", h.srNewPage)
	r.Admin.POST(srPath, h.srCreate)
	r.Admin.POST(srPath+"/import-base", h.srImportBase)
	r.Admin.GET(srPath+"/:id", h.srPage)
	r.Admin.GET(srPath+"/:id/url", h.srURL)
	r.Admin.GET(srPath+"/:id/qr", h.srQR)
	r.Admin.GET(srPath+"/:id/output", h.srOutput)
	r.Admin.GET(srPath+"/:id/base", h.srBasePage)
	r.Admin.POST(srPath+"/:id/base", h.srSaveBase)
	r.Admin.GET(srPath+"/:id/versions/:n", h.srVersion)
	r.Admin.GET(srPath+"/:id/versions/:n/diff", h.srDiff)
	r.Admin.POST(srPath+"/:id/versions/:n/restore", h.srRestore)
	r.Admin.GET(srPath+"/:id/edit", h.srEditPage)
	r.Admin.POST(srPath+"/:id/edit", h.srEdit)
	r.Admin.POST(srPath+"/:id/token", h.srAct(func(ctx context.Context, id int64) error {
		return h.Shadowrocket.RegenerateToken(ctx, id, events.ActorAdmin)
	}, "?regenerated=1"))
	r.Admin.POST(srPath+"/:id/disable", h.srAct(func(ctx context.Context, id int64) error {
		return h.Shadowrocket.SetEnabled(ctx, id, false, events.ActorAdmin)
	}, ""))
	r.Admin.POST(srPath+"/:id/enable", h.srAct(func(ctx context.Context, id int64) error {
		return h.Shadowrocket.SetEnabled(ctx, id, true, events.ActorAdmin)
	}, ""))
	r.Admin.GET(srPath+"/:id/delete", h.srDeletePage)
	r.Admin.POST(srPath+"/:id/delete", h.srDelete)
}

// loadSR reads the config of the :id parameter; 404 when there is none.
func (h *handler) loadSR(c *echo.Context) (shadowrocket.Config, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return shadowrocket.Config{}, echo.ErrNotFound
	}
	cfg, err := h.Shadowrocket.Get(c.Request().Context(), id)
	if errors.Is(err, shadowrocket.ErrNotFound) {
		return cfg, echo.ErrNotFound
	}
	return cfg, err
}

// srText translates a stored message: an i18n key, else the text as it is.
func srText(ctx context.Context, s string) string {
	if key, arg, ok := strings.Cut(s, ":"); ok && strings.HasPrefix(key, "mtvpn.") && i18n.From(ctx).Has(key) {
		return i18n.T(ctx, key, i18n.Args{"line": arg})
	}
	if i18n.From(ctx).Has(s) {
		return i18n.T(ctx, s)
	}
	return s
}

// lastFetch is "20 min ago · Shadowrocket/2.2.62" or "never fetched".
func lastFetch(ctx context.Context, c shadowrocket.Config) string {
	if c.LastFetchAt.IsZero() {
		return i18n.T(ctx, "shadowrocket.never_fetched")
	}
	s := i18n.From(ctx).Ago(c.LastFetchAt)
	if c.LastFetchUA != "" {
		s += " · " + c.LastFetchUA
	}
	return s
}

// ------------------------------------------------------------------ the configs page

type srRow struct {
	ID                                 int64
	Name, Sub, List, Policy, URL, Last string
	Disabled                           bool
}

func (h *handler) srList(c *echo.Context) error {
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	cs, err := h.Shadowrocket.List(ctx)
	if err != nil {
		return err
	}
	rows := make([]srRow, 0, len(cs))
	for _, cfg := range cs {
		rows = append(rows, srRow{
			ID: cfg.ID, Name: cfg.Name, List: cfg.List, Policy: cfg.Policy, Disabled: !cfg.Enabled,
			Sub:  loc.N("shadowrocket.row.sub", int64(cfg.Rules), i18n.Args{"version": cfg.Version}),
			URL:  "…/r/••••••/" + cfg.File(),
			Last: lastFetch(ctx, cfg),
		})
	}
	return web.Render(c, http.StatusOK, srListPage(h.shell(c, i18n.T(ctx, "shadowrocket.title"), srPath), rows))
}

// ------------------------------------------------------------------ new config, edit base

type srFormView struct {
	C          shadowrocket.Config // zero: a new config
	Name       string
	ListID     int64
	Policy     string
	Base, Note string
	ImportURL  string
	Lists      []lists.List
	Errs       map[string]string
}

func (h *handler) renderSRForm(c *echo.Context, status int, v srFormView) error {
	ctx := c.Request().Context()
	all, err := h.Lists.All(ctx)
	if err != nil {
		return err
	}
	v.Lists = all
	title := i18n.T(ctx, "shadowrocket.new.title")
	if v.C.ID != 0 {
		title = i18n.T(ctx, "shadowrocket.base.title", i18n.Args{"name": v.C.Name})
	}
	s := h.shell(c, title, srPath)
	s.Scripts = []string{"js/editor.bundle.js"}
	return web.Render(c, status, srFormPage(s, v, title))
}

func (h *handler) srNewPage(c *echo.Context) error {
	def, err := h.Lists.Default(c.Request().Context())
	if err != nil {
		return err
	}
	return h.renderSRForm(c, http.StatusOK, srFormView{ListID: def.ID, Policy: shadowrocket.DefaultPolicy})
}

// formBase is the posted base config: an uploaded file wins over the field.
func formBase(c *echo.Context) (string, error) {
	fh, err := c.FormFile("base_file")
	if err != nil || fh.Size == 0 {
		return c.FormValue("base"), nil
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, shadowrocket.MaxBase+1))
	return string(b), err
}

func formID(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func (h *handler) srCreate(c *echo.Context) error {
	ctx := c.Request().Context()
	base, err := formBase(c)
	if err != nil {
		return err
	}
	v := srFormView{
		Name: c.FormValue("name"), ListID: formID(c.FormValue("list")), Policy: c.FormValue("policy"),
		Base: base, Note: c.FormValue("note"), ImportURL: c.FormValue("import_url"),
	}
	id, err := h.Shadowrocket.Create(ctx, shadowrocket.New{Name: v.Name, ListID: v.ListID, Policy: v.Policy, Base: v.Base, Note: v.Note}, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderSRForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, srHref(id)+"?created=1")
}

// srImportBase reads the base config from the URL field into the form,
// which comes back with everything else as posted. Nothing is saved.
func (h *handler) srImportBase(c *echo.Context) error {
	ctx := c.Request().Context()
	v := srFormView{
		Name: c.FormValue("name"), ListID: formID(c.FormValue("list")), Policy: c.FormValue("policy"),
		Base: c.FormValue("base"), Note: c.FormValue("note"), ImportURL: strings.TrimSpace(c.FormValue("import_url")),
	}
	if id := formID(c.FormValue("config")); id != 0 {
		cfg, err := h.Shadowrocket.Get(ctx, id)
		if errors.Is(err, shadowrocket.ErrNotFound) {
			return echo.ErrNotFound
		}
		if err != nil {
			return err
		}
		v.C = cfg
	}
	text, err := h.Shadowrocket.ImportBase(ctx, v.ImportURL)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		v.Errs = map[string]string{"import_url": importBaseError(ctx, err)}
		return h.renderSRForm(c, http.StatusUnprocessableEntity, v)
	}
	v.Base = text
	return h.renderSRForm(c, http.StatusOK, v)
}

func importBaseError(ctx context.Context, err error) string {
	var he *shadowrocket.HTTPError
	switch {
	case errors.Is(err, shadowrocket.ErrBadURL):
		return i18n.T(ctx, "shadowrocket.err.import_url")
	case errors.Is(err, shadowrocket.ErrTooBig):
		return i18n.T(ctx, "shadowrocket.err.too_big")
	case errors.Is(err, shadowrocket.ErrNotUTF8):
		return i18n.T(ctx, "shadowrocket.err.utf8")
	case errors.As(err, &he):
		return i18n.T(ctx, "shadowrocket.err.import", i18n.Args{"error": "HTTP " + strconv.Itoa(he.Status)})
	}
	return i18n.T(ctx, "shadowrocket.err.import", i18n.Args{"error": sources.ErrorText(err)})
}

func (h *handler) srBasePage(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	cur, err := h.Shadowrocket.Version(c.Request().Context(), cfg.ID, 0)
	if err != nil {
		return err
	}
	return h.renderSRForm(c, http.StatusOK, srFormView{C: cfg, Base: cur.Content})
}

func (h *handler) srSaveBase(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	base, err := formBase(c)
	if err != nil {
		return err
	}
	v := srFormView{C: cfg, Base: base, Note: c.FormValue("note"), ImportURL: c.FormValue("import_url")}
	n, changed, err := h.Shadowrocket.SaveBase(ctx, cfg.ID, v.Base, v.Note, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderSRForm(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	q := "?unchanged=1"
	if changed {
		q = "?saved=" + strconv.Itoa(n)
	}
	return web.Redirect(c, srHref(cfg.ID)+q)
}

// ------------------------------------------------------------------ the config page

type srLine struct {
	N, Text string
	Ours    bool
}

type srVersionRow struct {
	Number        int
	Time, Note    string
	Current, Prev bool
}

type srFetchRow struct{ Time, IP, UA string }

type srView struct {
	C              shadowrocket.Config
	Line           string
	Band, BandKind string
	Masked, URL    string
	Preview        []srLine
	Versions       []srVersionRow
	Fetches        []srFetchRow
	OlderHref      string
	Lists          []lists.List
	Activity       []eventLine
}

// previewLines marks the lines Proxier inserted: everything between the
// base's common first and last lines.
func previewLines(base, out string) []srLine {
	b, o := splitKeep(base), splitKeep(out)
	p := 0
	for p < len(b) && p < len(o) && b[p] == o[p] {
		p++
	}
	q := 0
	for q < len(b)-p && q < len(o)-p && b[len(b)-1-q] == o[len(o)-1-q] {
		q++
	}
	lines := make([]srLine, 0, len(o))
	for i, l := range o {
		lines = append(lines, srLine{N: strconv.Itoa(i + 1), Text: strings.TrimRight(l, "\r\n"), Ours: i >= p && i < len(o)-q})
	}
	return lines
}

func splitKeep(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

const srFetchPage = 50

func (h *handler) srPage(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	v := srView{C: cfg, Masked: h.Shadowrocket.MaskedURL(cfg.Name)}
	if v.URL, err = h.Shadowrocket.URL(ctx, cfg.ID); err != nil {
		return err
	}
	fetched := i18n.T(ctx, "shadowrocket.never_fetched")
	if !cfg.LastFetchAt.IsZero() {
		fetched = i18n.T(ctx, "shadowrocket.fetched", i18n.Args{"ago": loc.Ago(cfg.LastFetchAt)})
	}
	if !cfg.Enabled {
		fetched = i18n.T(ctx, "shadowrocket.disabled")
	}
	v.Line = i18n.T(ctx, "shadowrocket.line", i18n.Args{"list": cfg.List, "policy": cfg.Policy, "version": cfg.Version, "fetched": fetched})
	switch {
	case c.QueryParam("created") == "1":
		v.Band, v.BandKind = i18n.T(ctx, "shadowrocket.band.created"), "pine"
	case c.QueryParam("regenerated") == "1":
		v.Band, v.BandKind = i18n.T(ctx, "shadowrocket.band.regenerated"), "pine"
	case c.QueryParam("saved") != "":
		v.Band, v.BandKind = i18n.T(ctx, "shadowrocket.band.saved", i18n.Args{"version": c.QueryParam("saved")}), "pine"
	case c.QueryParam("unchanged") == "1":
		v.Band, v.BandKind = i18n.T(ctx, "shadowrocket.band.unchanged"), "pine"
	case !cfg.Enabled:
		v.Band, v.BandKind = i18n.T(ctx, "shadowrocket.band.disabled"), "gold"
	}
	cur, err := h.Shadowrocket.Version(ctx, cfg.ID, 0)
	if err != nil {
		return err
	}
	out, err := h.Shadowrocket.Output(ctx, cfg.ID)
	if err != nil {
		return err
	}
	v.Preview = previewLines(cur.Content, string(out))
	vs, err := h.Shadowrocket.Versions(ctx, cfg.ID)
	if err != nil {
		return err
	}
	for _, ver := range vs {
		v.Versions = append(v.Versions, srVersionRow{
			Number: ver.Number, Time: loc.Time(ver.CreatedAt), Note: ver.Note, Current: ver.Number == cfg.Version, Prev: ver.Number > 1,
		})
	}
	before := formID(c.QueryParam("before"))
	fs, err := h.Shadowrocket.Fetches(ctx, cfg.ID, before, srFetchPage+1)
	if err != nil {
		return err
	}
	if len(fs) > srFetchPage {
		fs = fs[:srFetchPage]
		v.OlderHref = srHref(cfg.ID) + "?before=" + i64(fs[len(fs)-1].ID)
	}
	for _, f := range fs {
		v.Fetches = append(v.Fetches, srFetchRow{Time: loc.Time(f.At), IP: f.IP, UA: f.UserAgent})
	}
	if v.Activity, err = h.srActivity(ctx, cfg); err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, srPage(h.shell(c, cfg.Name, srPath), v))
}

func (h *handler) srActivity(ctx context.Context, cfg shadowrocket.Config) ([]eventLine, error) {
	loc := i18n.From(ctx)
	list, err := events.List(ctx, h.DB.R, events.Filter{Subject: shadowrocket.Subject(cfg.ID), Limit: 10})
	if err != nil {
		return nil, err
	}
	var out []eventLine
	for _, e := range list {
		args := i18n.Args{"subject": cfg.Name, "actor": e.Actor}
		for k, val := range e.Payload {
			args[k] = val
		}
		key := "event." + e.Type
		if e.Type == "routing.shadowrocket_updated" {
			ch, _ := e.Payload["changes"].(string)
			first, _, _ := strings.Cut(ch, ", ")
			key = "shadowrocket.event." + first
		}
		text := e.Type
		if loc.Has(key) {
			text = loc.T(key, args)
		}
		out = append(out, eventLine{Time: loc.Ago(e.Time.Time), Text: text})
	}
	return out, nil
}

// srURL is the Reveal fragment: the URL in clear, or masked with ?hide=1.
func (h *handler) srURL(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	u, err := h.Shadowrocket.URL(c.Request().Context(), cfg.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, srURLField(cfg.ID, u, h.Shadowrocket.MaskedURL(cfg.Name), c.QueryParam("hide") != "1"))
}

// srQR is the QR fragment, or the QR button again with ?hide=1.
func (h *handler) srQR(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	u, err := h.Shadowrocket.URL(c.Request().Context(), cfg.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return web.Render(c, http.StatusOK, srQRSlot(cfg.ID, cfg.Name, u, c.QueryParam("hide") != "1"))
}

// srOutput downloads what the phone gets now, as an attachment.
func (h *handler) srOutput(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	out, err := h.Shadowrocket.Output(c.Request().Context(), cfg.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+cfg.File()+`"`)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", out)
}

func (h *handler) loadVersion(c *echo.Context, cfg shadowrocket.Config) (shadowrocket.Version, error) {
	n, err := strconv.Atoi(c.Param("n"))
	if err != nil || n < 1 {
		return shadowrocket.Version{}, echo.ErrNotFound
	}
	v, err := h.Shadowrocket.Version(c.Request().Context(), cfg.ID, n)
	if errors.Is(err, shadowrocket.ErrNoVersion) {
		return v, echo.ErrNotFound
	}
	return v, err
}

func (h *handler) srVersion(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ver, err := h.loadVersion(c, cfg)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	title := i18n.T(ctx, "shadowrocket.version.title", i18n.Args{"name": cfg.Name, "version": ver.Number})
	lines := ui.Highlight("base.conf", []byte(ver.Content))
	return web.Render(c, http.StatusOK, srVersionPage(h.shell(c, title, srPath), cfg, ver, title, lines))
}

func (h *handler) srDiff(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ver, err := h.loadVersion(c, cfg)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var prev shadowrocket.Version
	if ver.Number > 1 {
		if prev, err = h.Shadowrocket.Version(ctx, cfg.ID, ver.Number-1); err != nil && !errors.Is(err, shadowrocket.ErrNoVersion) {
			return err
		}
	}
	name := cfg.File()
	file := ui.DiffFile{Path: name, Change: "changed"}
	if prev.Content == ver.Content {
		file.Note = i18n.T(ctx, "shadowrocket.diff.same")
	} else {
		file.Hunks = ui.ParseUnified(udiff.Unified("a/"+name, "b/"+name, prev.Content, ver.Content))
	}
	title := i18n.T(ctx, "shadowrocket.diff.title", i18n.Args{"name": cfg.Name, "from": ver.Number - 1, "to": ver.Number})
	return web.Render(c, http.StatusOK, srDiffPage(h.shell(c, title, srPath), cfg, title, ui.DiffView{Files: []ui.DiffFile{file}}))
}

func (h *handler) srRestore(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ver, err := h.loadVersion(c, cfg)
	if err != nil {
		return err
	}
	before := cfg.Version
	n, err := h.Shadowrocket.Restore(c.Request().Context(), cfg.ID, ver.Number, events.ActorAdmin)
	if err != nil {
		return err
	}
	if n == before {
		return web.Redirect(c, srHref(cfg.ID)+"?unchanged=1")
	}
	return web.Redirect(c, srHref(cfg.ID)+"?saved="+strconv.Itoa(n))
}

// ------------------------------------------------------------------ list and policy, actions

type srEditView struct {
	C      shadowrocket.Config
	ListID int64
	Policy string
	Lists  []lists.List
	Errs   map[string]string
}

func (h *handler) renderSREdit(c *echo.Context, status int, v srEditView) error {
	ctx := c.Request().Context()
	all, err := h.Lists.All(ctx)
	if err != nil {
		return err
	}
	v.Lists = all
	title := i18n.T(ctx, "shadowrocket.edit.title", i18n.Args{"name": v.C.Name})
	return web.Render(c, status, srEditPage(h.shell(c, title, srPath), v, title))
}

func (h *handler) srEditPage(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	return h.renderSREdit(c, http.StatusOK, srEditView{C: cfg, ListID: cfg.ListID, Policy: cfg.Policy})
}

func (h *handler) srEdit(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	v := srEditView{C: cfg, ListID: formID(c.FormValue("list")), Policy: c.FormValue("policy")}
	_, err = h.Shadowrocket.Edit(ctx, cfg.ID, v.ListID, v.Policy, events.ActorAdmin)
	var fe store.FieldErrors
	if errors.As(err, &fe) {
		v.Errs = errText(ctx, fe)
		return h.renderSREdit(c, http.StatusUnprocessableEntity, v)
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, srHref(cfg.ID))
}

// srAct runs a one-click action and goes back to the config page.
func (h *handler) srAct(fn func(ctx context.Context, id int64) error, query string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		cfg, err := h.loadSR(c)
		if err != nil {
			return err
		}
		if err := fn(c.Request().Context(), cfg.ID); err != nil {
			return err
		}
		return web.Redirect(c, srHref(cfg.ID)+query)
	}
}

func (h *handler) srDeletePage(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	title := i18n.T(ctx, "shadowrocket.delete.title", i18n.Args{"name": cfg.Name})
	return web.Render(c, http.StatusOK, srDeletePage(h.shell(c, title, srPath), cfg, title))
}

func (h *handler) srDelete(c *echo.Context) error {
	cfg, err := h.loadSR(c)
	if err != nil {
		return err
	}
	if err := h.Shadowrocket.Delete(c.Request().Context(), cfg.ID, events.ActorAdmin); err != nil {
		return err
	}
	return web.Redirect(c, srPath)
}
