package pages

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const importPath = "/routing/import"

func importHref(id int64) string { return importPath + "/" + i64(id) }

// registerImport adds the mtvpn import's routes.
func (h *handler) registerImport(r web.Routes) {
	r.Admin.GET(importPath, h.importPage)
	r.Admin.POST(importPath, h.importPreview)
	r.Admin.GET(importPath+"/:id", h.importStep)
	r.Admin.POST(importPath+"/:id", h.importRun)
	r.Admin.GET(importPath+"/:id/status", h.importStatus)
}

func (h *handler) loadImport(c *echo.Context) (mtvpn.Import, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return mtvpn.Import{}, echo.ErrNotFound
	}
	im, err := h.Import.Get(c.Request().Context(), id)
	if errors.Is(err, mtvpn.ErrNotFound) {
		return im, echo.ErrNotFound
	}
	return im, err
}

// ------------------------------------------------------------------ step 1: paste

type importPasteView struct {
	Text, Err string
}

func (h *handler) importShell(c *echo.Context) ui.Shell {
	return h.shell(c, i18n.T(c.Request().Context(), "mtvpn.title"), listsPath)
}

func (h *handler) importPage(c *echo.Context) error {
	return web.Render(c, http.StatusOK, importPastePage(h.importShell(c), importPasteView{}))
}

// importText is the pasted text, or the uploaded file's.
func importText(c *echo.Context) (string, error) {
	fh, err := c.FormFile("file")
	if err != nil || fh.Size == 0 {
		return c.FormValue("text"), nil
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, mtvpn.MaxFile+1))
	return string(b), err
}

func (h *handler) importPreview(c *echo.Context) error {
	ctx := c.Request().Context()
	text, err := importText(c)
	if err != nil {
		return err
	}
	id, err := h.Import.Preview(ctx, text, events.ActorAdmin)
	var fe store.FieldErrors
	var pe *mtvpn.ParseError
	switch {
	case errors.As(err, &fe):
		return web.Render(c, http.StatusUnprocessableEntity, importPastePage(h.importShell(c), importPasteView{Text: text, Err: i18n.T(ctx, fe["file"])}))
	case errors.As(err, &pe):
		key := "mtvpn.err.not_kv"
		if pe.NoKey {
			key = "mtvpn.err.no_key"
		}
		return web.Render(c, http.StatusUnprocessableEntity, importPastePage(h.importShell(c), importPasteView{Text: text, Err: i18n.T(ctx, key, i18n.Args{"line": pe.Line})}))
	case err != nil:
		return err
	}
	return web.Redirect(c, importHref(id))
}

// ------------------------------------------------------------------ step 2: preview

type importRowView struct {
	I                                int
	Row                              mtvpn.Row
	Status, StatusKind, Problem      string
	Outcome, OutcomeKind, OutcomeErr string
}

type importView struct {
	Im       mtvpn.Import
	Rows     []importRowView
	Counts   string
	Ignored  string
	Failures []importFailure
	Lists    []lists.List
	ListName string
	Button   string
	Errs     map[string]string
	Base     string // the base box's line
	// the result
	Steps  []ui.JobStep
	Totals string
	Config string
}

type importFailure struct{ URL, Error string }

func (h *handler) importView(ctx context.Context, im mtvpn.Import) (importView, error) {
	loc := i18n.From(ctx)
	v := importView{Im: im}
	all, err := h.Lists.All(ctx)
	if err != nil {
		return v, err
	}
	v.Lists = all
	for _, l := range all {
		if l.ID == im.ListID {
			v.ListName = l.Name
		}
	}
	var fresh, exists, invalid, ticked int
	for i, r := range im.Rows {
		rv := importRowView{I: i, Row: r}
		switch r.Status {
		case mtvpn.StatusNew:
			fresh++
			rv.Status, rv.StatusKind = i18n.T(ctx, "mtvpn.status.new"), "ok"
		case mtvpn.StatusExists:
			exists++
			rv.Status, rv.StatusKind = i18n.T(ctx, "mtvpn.status.exists"), "off"
		default:
			invalid++
			rv.Status, rv.StatusKind = i18n.T(ctx, "mtvpn.status.invalid"), "broken"
			rv.Problem = srText(ctx, r.Problem)
		}
		if r.Include {
			ticked++
		}
		switch r.Outcome {
		case mtvpn.Created, mtvpn.Reused, mtvpn.Converted:
			rv.Outcome, rv.OutcomeKind = i18n.T(ctx, "mtvpn.outcome."+r.Outcome), "ok"
		case mtvpn.Skipped:
			rv.Outcome, rv.OutcomeKind = i18n.T(ctx, "mtvpn.outcome.skipped"), "look"
			if r.Guard != nil {
				rv.OutcomeErr = i18n.T(ctx, "lists.err.guard", i18n.Args{"domain": r.Guard.Domain, "hostname": r.Guard.Hostname, "server": r.Guard.Server})
			}
		case mtvpn.Failed:
			rv.Outcome, rv.OutcomeKind, rv.OutcomeErr = i18n.T(ctx, "mtvpn.outcome.failed"), "broken", srText(ctx, r.Error)
		}
		v.Rows = append(v.Rows, rv)
	}
	v.Counts = loc.N("mtvpn.counts", int64(len(im.Rows)), i18n.Args{"new": fresh, "exists": exists, "invalid": invalid})
	if len(im.Ignored) > 0 {
		v.Ignored = i18n.T(ctx, "mtvpn.ignored", i18n.Args{"keys": strings.Join(im.Ignored, ", ")})
	}
	for _, f := range im.ListFailures {
		v.Failures = append(v.Failures, importFailure{URL: f.URL, Error: srText(ctx, f.Error)})
	}
	sr := im.Shadowrocket
	switch {
	case sr.Offered:
		v.Base = loc.N("mtvpn.base.found", int64(sr.Lines))
	case sr.Problem != "":
		v.Base = i18n.T(ctx, "mtvpn.base.problem", i18n.Args{"reason": srText(ctx, sr.Problem)})
	}
	configs := 0
	if sr.Offered && sr.Create {
		configs = 1
	}
	v.Button = loc.N("mtvpn.import_button", int64(ticked), i18n.Args{"configs": loc.N("mtvpn.configs", int64(configs))})
	if im.JobID != 0 {
		steps, err := h.Jobs.Steps(ctx, im.JobID)
		if err != nil {
			return v, err
		}
		for _, st := range steps {
			v.Steps = append(v.Steps, ui.JobStep{Name: i18n.T(ctx, "job."+mtvpn.JobImport+".step."+st.Name), State: string(st.State), Kind: importStepKind(string(st.State))})
		}
	}
	counts := map[string]int{}
	for _, r := range im.Rows {
		if r.Outcome != "" {
			counts[r.Outcome]++
		}
	}
	v.Totals = i18n.T(ctx, "mtvpn.totals", i18n.Args{
		"created": counts[mtvpn.Created], "reused": counts[mtvpn.Reused], "converted": counts[mtvpn.Converted],
		"skipped": counts[mtvpn.Skipped] + counts[mtvpn.Failed],
	})
	switch sr.Outcome {
	case mtvpn.Created:
		v.Config = i18n.T(ctx, "mtvpn.config.created", i18n.Args{"name": sr.Name})
	case mtvpn.Failed:
		v.Config = i18n.T(ctx, "mtvpn.config.failed", i18n.Args{"name": sr.Name, "error": srText(ctx, sr.Error)})
	}
	return v, nil
}

func importStepKind(state string) string {
	switch state {
	case "succeeded":
		return "ok"
	case "running":
		return "running"
	case "failed":
		return "broken"
	case "cancelled":
		return "off"
	}
	return "unknown"
}

func (h *handler) importStep(c *echo.Context) error {
	im, err := h.loadImport(c)
	if err != nil {
		return err
	}
	return h.renderImport(c, http.StatusOK, im, nil)
}

func (h *handler) renderImport(c *echo.Context, status int, im mtvpn.Import, errs map[string]string) error {
	v, err := h.importView(c.Request().Context(), im)
	if err != nil {
		return err
	}
	v.Errs = errs
	return web.Render(c, status, importStepPage(h.importShell(c), v))
}

func (h *handler) importRun(c *echo.Context) error {
	im, err := h.loadImport(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	form, err := c.FormValues()
	if err != nil {
		return err
	}
	ch := mtvpn.Choices{
		Include: map[int]bool{}, Convert: map[int]bool{}, ListID: formID(c.FormValue("list")),
		CreateConfig: c.FormValue("config_create") == "1", ConfigName: c.FormValue("config_name"), ConfigList: formID(c.FormValue("config_list")),
	}
	for _, s := range form["include"] {
		if i, err := strconv.Atoi(s); err == nil {
			ch.Include[i] = true
		}
	}
	for i := range im.Rows {
		if c.FormValue("convert."+strconv.Itoa(i)) == "1" {
			ch.Convert[i] = true
		}
	}
	_, err = h.Import.Run(ctx, im.ID, ch, events.ActorAdmin)
	var fe store.FieldErrors
	switch {
	case errors.As(err, &fe):
		for i := range im.Rows {
			im.Rows[i].Include, im.Rows[i].Convert = ch.Include[i], ch.Convert[i]
		}
		im.Shadowrocket.Create, im.Shadowrocket.Name, im.Shadowrocket.ListID = ch.CreateConfig, ch.ConfigName, ch.ConfigList
		return h.renderImport(c, http.StatusUnprocessableEntity, im, errText(ctx, fe))
	case errors.Is(err, mtvpn.ErrNotPreview):
		return web.Redirect(c, importHref(im.ID))
	case err != nil:
		return err
	}
	return web.Redirect(c, importHref(im.ID))
}

// importStatus is the progress area, polled while the import runs; once it
// ended the page reloads into the result.
func (h *handler) importStatus(c *echo.Context) error {
	im, err := h.loadImport(c)
	if err != nil {
		return err
	}
	if im.State != "running" {
		c.Response().Header().Set("HX-Refresh", "true")
	}
	v, err := h.importView(c.Request().Context(), im)
	if err != nil {
		return err
	}
	return web.Render(c, http.StatusOK, importProgress(v))
}
