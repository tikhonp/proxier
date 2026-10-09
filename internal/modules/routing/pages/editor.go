package pages

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type edRow struct {
	N               int // 1-based, the form's index
	Domain, Unicode string
	Exact           bool
	Note            string
	New             bool
	Err             string
	Hint            string // "covered by openai in Main"
}

type reportView struct {
	Line    string
	Lines   []string
	Addable int // rows Add to the table would add
	Preview bool
}

type editorView struct {
	It                     services.Item
	Name, Tag, Description string
	Rows                   []edRow
	Paste                  string
	Report                 *reportView
	Errs                   map[string]string
	Status                 string
	Saved                  int    // names after save
	Footer                 string // "Saving changes what the targets of Main get."
	Guard                  string // a guard refusal
}

// editorRows turns service rows into the table, marking rows not saved yet.
func editorRows(rows []services.DomainRow, saved map[string]bool) []edRow {
	out := make([]edRow, 0, len(rows))
	for i, r := range rows {
		d := strings.TrimSpace(r.Domain)
		out = append(out, edRow{N: i + 1, Domain: d, Unicode: domain.Unicode(d), Exact: r.Exact, Note: r.Note, New: !saved[d]})
	}
	return out
}

func (h *handler) savedSet(ctx context.Context, id int64) (map[string]bool, []services.DomainRow, error) {
	rows, err := h.Services.CustomRows(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	m := map[string]bool{}
	for _, r := range rows {
		m[r.Domain] = true
	}
	return m, rows, nil
}

func (h *handler) loadCustom(c *echo.Context) (services.Item, error) {
	it, err := h.load(c)
	if err != nil {
		return it, err
	}
	if it.Source != selector.Custom {
		return it, echo.ErrNotFound
	}
	return it, nil
}

func (h *handler) editPage(c *echo.Context) error {
	it, err := h.loadCustom(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	saved, rows, err := h.savedSet(ctx, it.ID)
	if err != nil {
		return err
	}
	v := editorView{It: it, Name: it.Name, Tag: it.Tag, Description: it.Description, Rows: editorRows(rows, saved)}
	return h.renderEditor(c, http.StatusOK, v)
}

func (h *handler) renderEditor(c *echo.Context, status int, v editorView) error {
	ctx := c.Request().Context()
	unsaved := 0
	for _, r := range v.Rows {
		if r.New {
			unsaved++
		}
	}
	if err := h.coverHints(ctx, &v); err != nil {
		return err
	}
	v.Status = i18n.T(ctx, "services.custom") + " · " + i18n.N(ctx, "services.editor.rows", int64(len(v.Rows)))
	if unsaved > 0 {
		v.Status += " · " + i18n.N(ctx, "services.editor.unsaved", int64(unsaved))
	}
	if htmx(c) {
		// htmx swaps only 2xx answers: errors re-render with 200
		return web.Render(c, http.StatusOK, editorForm(v))
	}
	return web.Render(c, status, editorPage(h.shell(c, i18n.T(ctx, "services.editor.title", i18n.Args{"tag": v.It.Tag}), listPath), v))
}

// coverHints marks the rows that another service of a list holding this one
// has, or covers with a suffix above, and words the footer.
func (h *handler) coverHints(ctx context.Context, v *editorView) error {
	ms, err := h.Lists.Memberships(ctx, v.It.ID)
	if err != nil || len(ms) == 0 {
		return err
	}
	var names, targets []string
	for _, m := range ms {
		names = append(names, m.List.Name)
		ts, err := h.Lists.Targets(ctx, m.List.ID)
		if err != nil {
			return err
		}
		for _, t := range ts {
			if t.Kind == "router" && !slices.Contains(targets, t.Name) {
				targets = append(targets, t.Name)
			}
		}
		view, err := h.Lists.View(ctx, m.List.ID, nil)
		if err != nil {
			return err
		}
		members := make([]own.Member, 0, len(view.Members))
		for _, mem := range view.Members {
			members = append(members, own.Member{ServiceID: mem.Service.ID, Tag: mem.Service.Tag, Set: mem.Snapshot.Set})
		}
		for i := range v.Rows {
			r := &v.Rows[i]
			if r.Hint != "" || r.Domain == "" {
				continue
			}
			if cs := own.CoveredBy(strings.ToLower(r.Domain), r.Exact, members, v.It.ID); len(cs) > 0 {
				r.Hint = i18n.T(ctx, "lists.editor.covered", i18n.Args{"tag": cs[0].Tag, "list": m.List.Name})
			}
		}
	}
	v.Footer = i18n.T(ctx, "lists.editor.footer", i18n.Args{"lists": andList(ctx, names)})
	if len(targets) > 0 {
		v.Footer = i18n.T(ctx, "lists.editor.footer_routers", i18n.Args{"routers": andList(ctx, targets), "lists": andList(ctx, names)})
	}
	return nil
}

// formRows reads the table as the form sent it: domain.<n>, match.<n>, note.<n>.
func formRows(c *echo.Context) []services.DomainRow {
	n, _ := strconv.Atoi(c.FormValue("rows"))
	if n > services.MaxRows+1000 {
		n = services.MaxRows + 1000
	}
	var out []services.DomainRow
	for i := 1; i <= n; i++ {
		k := strconv.Itoa(i)
		out = append(out, services.DomainRow{
			Domain: c.FormValue("domain." + k), Exact: c.FormValue("match."+k) == "exact", Note: c.FormValue("note." + k),
		})
	}
	return out
}

func (h *handler) edit(c *echo.Context) error {
	it, err := h.loadCustom(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	saved, _, err := h.savedSet(ctx, it.ID)
	if err != nil {
		return err
	}
	rows := formRows(c)
	v := editorView{It: it, Name: c.FormValue("name"), Tag: c.FormValue("tag"), Description: c.FormValue("description"), Paste: c.FormValue("paste")}
	op := c.FormValue("op")
	switch {
	case op == "preview" || op == "add":
		next, rep := h.Services.Paste(v.Paste, rows)
		v.Report = report(ctx, rep)
		if op == "add" {
			rows, v.Paste = next, ""
		} else {
			v.Report.Preview = true
			v.Report.Addable = len(rep.Added)
		}
	case strings.HasPrefix(op, "remove-"):
		if n, err := strconv.Atoi(strings.TrimPrefix(op, "remove-")); err == nil && n >= 1 && n <= len(rows) {
			rows = append(rows[:n-1], rows[n:]...)
		}
	default: // save, also ⌘↵ (no button)
		res, err := h.Services.SaveCustom(ctx, it.ID, services.Edit{Name: v.Name, Tag: v.Tag, Description: v.Description, Rows: rows}, events.ActorAdmin)
		var ge *lists.GuardError
		if errors.As(err, &ge) {
			v.Rows = editorRows(rows, saved)
			v.Guard = guardText(ctx, ge)
			return h.renderEditor(c, http.StatusUnprocessableEntity, v)
		}
		var fe store.FieldErrors
		if errors.As(err, &fe) {
			v.Rows = editorRows(rows, saved)
			v.Errs = errText(ctx, fe)
			for i := range v.Rows {
				v.Rows[i].Err = v.Errs["row."+strconv.Itoa(v.Rows[i].N)]
			}
			return h.renderEditor(c, http.StatusUnprocessableEntity, v)
		}
		if err != nil {
			return err
		}
		q := ""
		if res.Changed {
			q = "?saved=1"
			if n := len(res.Report.Merged); n > 0 {
				// the band says how many rows the save merged or absorbed
				q += "&merged=" + strconv.Itoa(n)
			}
		}
		return web.Redirect(c, svcHref(it.ID)+q)
	}
	v.Rows = editorRows(rows, saved)
	return h.renderEditor(c, http.StatusOK, v)
}

// report words what Paste found: "+12 added · 2 merged · 1 refused", then
// each merged, punycode and refused line.
func report(ctx context.Context, r services.Report) *reportView {
	loc := i18n.From(ctx)
	v := &reportView{Line: "+" + loc.N("services.paste.added", int64(len(r.Added))) + " · " +
		loc.N("services.paste.merged", int64(len(r.Merged))) + " · " + loc.N("services.paste.refused", int64(len(r.Refused)))}
	for _, m := range r.Merged {
		if m.Under == m.Name {
			v.Lines = append(v.Lines, i18n.T(ctx, "services.paste.duplicate", i18n.Args{"name": m.Name}))
		} else {
			v.Lines = append(v.Lines, i18n.T(ctx, "services.paste.absorbed", i18n.Args{"name": m.Name, "under": m.Under}))
		}
	}
	for _, p := range r.Punycode {
		v.Lines = append(v.Lines, i18n.T(ctx, "services.paste.punycode", i18n.Args{"unicode": p.Unicode, "name": p.Domain}))
	}
	for _, f := range r.Refused {
		v.Lines = append(v.Lines, i18n.T(ctx, "services.paste.refused_line", i18n.Args{"line": f.Line, "reason": i18n.T(ctx, f.Reason)}))
	}
	return v
}
