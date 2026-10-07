package pages

import (
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func countryFlag(cc string) string { return country.Flag(cc) }

func (h *handler) registerStack(r web.Routes) {
	r.Admin.GET("/servers/:id/stack", h.stackTab)
	r.Admin.GET("/servers/:id/stack/files/:deployment/*", h.stackFile)
	r.Admin.GET("/servers/:id/stack/diff/:deployment", h.stackDiff)
}

type stackFileRow struct {
	Path, Name, Href string
	Depth            int
	Dir, Current     bool
	Secrets          bool
}

type stackDeployment struct {
	Time, Kind, Result, Version string
	State                       string
	Current                     bool
	JobHref, DiffHref           string
	Error                       string
}

type stackView struct {
	serverView
	Dep        *store.Deployment
	DepNote    string
	Files      []stackFileRow
	Selected   string
	Code       []ui.CodeLine
	Masked     bool
	RevealHref string
	History    []stackDeployment
	CanRoll    bool
}

// stackCodeView is the code block of one file, revealed or masked.
type stackCodeView struct {
	Path       string
	Code       []ui.CodeLine
	Masked     bool
	RevealHref string
}

func (v stackView) codeView() stackCodeView {
	return stackCodeView{Path: v.Selected, Code: v.Code, Masked: v.Masked, RevealHref: v.RevealHref}
}

func (h *handler) stackTab(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if s.State == "provisioning" {
		return web.Redirect(c, serverHref(s.ID))
	}
	ctx := c.Request().Context()
	loc := i18n.From(ctx)
	h.noStore(c)
	v := stackView{serverView: h.headView(ctx, s)}

	cur, ok, err := store.CurrentDeployment(ctx, h.Store.DB.R, s.ID)
	if err != nil {
		return err
	}
	if ok {
		v.Dep = &cur
		v.DepNote = i18n.T(ctx, "servers.stack.deployed", i18n.Args{
			"time": loc.Time(cur.StartedAt.Time), "version": vlabel(cur.TemplateVersion), "kind": i18n.T(ctx, "servers.dep.kind."+cur.Kind),
		})
		secrets, err := h.Deploy.StoredSecrets(ctx, s, &cur)
		if err != nil {
			return err
		}
		mask := deploy.Mask(secrets)
		files, err := store.DeployedFiles(ctx, h.Store.DB.R, cur.ID)
		if err != nil {
			return err
		}
		contents := map[string][]byte{}
		hasSecrets := map[string]bool{}
		paths := make([]string, 0, len(files))
		for _, f := range files {
			b, err := sealed.OpenFile(h.Vault, cur.ID, f.Path, f.Content)
			if err != nil {
				return err
			}
			contents[f.Path] = b
			hasSecrets[f.Path] = string(mask.Apply(b)) != string(b)
			paths = append(paths, f.Path)
		}
		sort.Strings(paths)
		v.Selected = c.QueryParam("file")
		if _, ok := contents[v.Selected]; !ok {
			v.Selected = ""
			if len(paths) > 0 {
				v.Selected = paths[0]
			}
		}
		v.Files = fileTree(s.ID, paths, v.Selected, hasSecrets)
		if v.Selected != "" {
			v.Code = ui.Highlight(v.Selected, mask.Apply(contents[v.Selected]))
			v.Masked = hasSecrets[v.Selected]
			if v.Masked {
				v.RevealHref = serverHref(s.ID) + "/stack/files/" + i64(cur.ID) + "/" + v.Selected
			}
		}
	}

	deps, err := store.Deployments(ctx, h.Store.DB.R, s.ID)
	if err != nil {
		return err
	}
	if h.Deploy != nil && s.State == "active" {
		v.CanRoll = h.Deploy.CanRollBack(ctx, s.ID)
	}
	for _, d := range deps {
		row := stackDeployment{
			Time: loc.Time(d.StartedAt.Time), Kind: i18n.T(ctx, "servers.dep.kind."+d.Kind), Version: vlabel(d.TemplateVersion),
			State: d.State, Current: ok && d.ID == cur.ID, Error: d.Error,
		}
		switch d.State {
		case "succeeded":
			row.Result = i18n.T(ctx, "servers.dep.state.succeeded")
		case "failed":
			row.Result = i18n.T(ctx, "servers.dep.state.failed")
		default:
			row.Result = i18n.T(ctx, "servers.dep.state.running")
		}
		if d.Uploaded {
			row.Result += " · " + i18n.From(ctx).N("servers.dep.files", int64(d.FilesChanged))
			if _, has, err := store.PreviousUploading(ctx, h.Store.DB.R, s.ID, d.ID); err != nil {
				return err
			} else if has && d.State == "succeeded" {
				row.DiffHref = serverHref(s.ID) + "/stack/diff/" + i64(d.ID)
			}
		}
		if d.JobID.Valid {
			row.JobHref = "/jobs/" + i64(d.JobID.Int64)
		}
		v.History = append(v.History, row)
	}
	return web.Render(c, http.StatusOK, stackPage(h.shell(c, i18n.T(ctx, "servers.stack.title", i18n.Args{"name": s.Name}), "/servers"), v))
}

// fileTree lists the paths as a tree: a row per directory, then its files.
func fileTree(serverID int64, paths []string, selected string, secrets map[string]bool) []stackFileRow {
	var rows []stackFileRow
	seen := map[string]bool{}
	for _, p := range paths {
		var parts []string
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			parts = append([]string{d}, parts...)
		}
		for i, d := range parts {
			if !seen[d] {
				seen[d] = true
				rows = append(rows, stackFileRow{Path: d, Name: path.Base(d) + "/", Depth: i, Dir: true})
			}
		}
		rows = append(rows, stackFileRow{
			Path: p, Name: path.Base(p), Depth: len(parts), Current: p == selected, Secrets: secrets[p],
			Href: serverHref(serverID) + "/stack?file=" + urlQuery(p),
		})
	}
	return rows
}

// stackFile answers Reveal: one file's real content, never cached.
func (h *handler) stackFile(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	depID, err := strconv.ParseInt(c.Param("deployment"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	ctx := c.Request().Context()
	dep, err := store.GetDeployment(ctx, h.Store.DB.R, depID)
	if err != nil || dep.ServerID != s.ID {
		return echo.ErrNotFound
	}
	p := strings.TrimPrefix(c.Param("*"), "/")
	files, err := store.DeployedFiles(ctx, h.Store.DB.R, dep.ID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Path != p {
			continue
		}
		b, err := sealed.OpenFile(h.Vault, dep.ID, f.Path, f.Content)
		if err != nil {
			return err
		}
		h.noStore(c)
		return web.Render(c, http.StatusOK, stackCode(stackCodeView{Path: p, Code: ui.Highlight(p, b)}))
	}
	return echo.ErrNotFound
}

type stackDiffView struct {
	serverView
	Dep, Prev store.Deployment
	From, To  string
	Diff      ui.DiffView
	Same      bool
}

// stackDiff compares a deployment with the previous one that put files on the
// server, with every secret masked.
func (h *handler) stackDiff(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	depID, err := strconv.ParseInt(c.Param("deployment"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	ctx := c.Request().Context()
	dep, err := store.GetDeployment(ctx, h.Store.DB.R, depID)
	if err != nil || dep.ServerID != s.ID || !dep.Uploaded {
		return echo.ErrNotFound
	}
	prev, ok, err := store.PreviousUploading(ctx, h.Store.DB.R, s.ID, dep.ID)
	if err != nil {
		return err
	}
	if !ok {
		return echo.ErrNotFound
	}
	h.noStore(c)
	oldMask, newMask, err := h.Deploy.DiffMaskers(ctx, s, prev, dep)
	if err != nil {
		return err
	}
	load := func(d store.Deployment, m deploy.Masker) (map[string][]byte, error) {
		rows, err := store.DeployedFiles(ctx, h.Store.DB.R, d.ID)
		if err != nil {
			return nil, err
		}
		out := map[string][]byte{}
		for _, f := range rows {
			b, err := sealed.OpenFile(h.Vault, d.ID, f.Path, f.Content)
			if err != nil {
				return nil, err
			}
			out[f.Path] = m.Apply(b)
		}
		return out, nil
	}
	a, err := load(prev, oldMask)
	if err != nil {
		return err
	}
	b, err := load(dep, newMask)
	if err != nil {
		return err
	}
	v := stackDiffView{serverView: h.headView(ctx, s), Dep: dep, Prev: prev, Diff: ui.DiffView{Split: c.QueryParam("view") == "split"}}
	v.From = i18n.T(ctx, "servers.dep.kind."+prev.Kind) + " " + vlabel(prev.TemplateVersion)
	v.To = i18n.T(ctx, "servers.dep.kind."+dep.Kind) + " " + vlabel(dep.TemplateVersion)
	for _, d := range templates.Diff(a, b) {
		if d.Change == templates.Unchanged {
			continue
		}
		f := ui.DiffFile{Path: d.Path, Change: d.Change, Hunks: ui.ParseUnified(d.Unified)}
		if d.Binary {
			f.Note = i18n.T(ctx, "templates.diff.binary", i18n.Args{"old": d.OldSize, "new": d.NewSize})
		}
		v.Diff.Files = append(v.Diff.Files, f)
	}
	v.Same = len(v.Diff.Files) == 0
	return web.Render(c, http.StatusOK, stackDiffPage(h.shell(c, i18n.T(ctx, "servers.stack.diff.title", i18n.Args{"name": s.Name}), "/servers"), v))
}
