package pages

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/modules/servers/templates"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

const (
	maxDraftFiles = templates.MaxImportFiles
	fileField     = "file["
	binField      = "bin["
)

// edFile is a file of the draft as the editor form carries it: text in a
// text area, anything else (not UTF-8, or with a NUL) base64 in a hidden
// field, so one post holds the whole draft either way.
type edFile struct {
	Path   string
	Text   string
	Binary bool
	B64    string
	Size   int
}

// edRow is a row of the file tree: a folder or a file.
type edRow struct {
	Path, Name string
	Depth      int
	Dir        bool
}

type editorView struct {
	T         templates.Info
	Files     []edFile
	Rows      []edRow
	Active    string
	Revision  int
	BasedOn   int
	UpdatedAt db.Time
	UpdatedBy string
	Source    templates.Source
	Next      int // the number a publish would make
	Saved     bool
	Stale     bool              // the draft changed under this editor: Save is off, Copy my version is the way out
	Error     string            // translated
	Message   string            // translated, shown in #ed-msg
	Servers   []store.ServerRef // active servers of the template, for the preview picker
	Agent     *agentBandView    // the open agent session on the draft
}

func newEditorFiles(files map[string][]byte) ([]edFile, []edRow) {
	paths := sortedPaths(files)
	out := make([]edFile, 0, len(paths))
	for _, p := range paths {
		b := files[p]
		f := edFile{Path: p, Size: len(b)}
		if isBinary(b) {
			f.Binary, f.B64 = true, base64.StdEncoding.EncodeToString(b)
		} else {
			f.Text = string(b)
		}
		out = append(out, f)
	}
	return out, treeRows(paths)
}

// treeRows lists folders and files in path order, a folder before its first
// child.
func treeRows(paths []string) []edRow {
	var rows []edRow
	seen := map[string]bool{}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		for i := 0; i < len(parts)-1; i++ {
			dir := strings.Join(parts[:i+1], "/")
			if !seen[dir] {
				seen[dir] = true
				rows = append(rows, edRow{Path: dir, Name: parts[i] + "/", Depth: i, Dir: true})
			}
		}
		rows = append(rows, edRow{Path: p, Name: parts[len(parts)-1], Depth: len(parts) - 1})
	}
	return rows
}

// validDraftPath is a path the editor may hold.
func validDraftPath(p string) bool { return templates.ValidPath(p) }

// posted is what the editor form carries.
type posted struct {
	Files    map[string][]byte
	Revision int
	Active   string
}

// readPosted reads the whole draft from the form. The text areas' newlines
// are normalised: browsers send CRLF.
func readPosted(c *echo.Context) (posted, string) {
	req := c.Request()
	if err := req.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return posted{}, "templates.err.too_large"
	}
	p := posted{Files: map[string][]byte{}, Active: req.PostFormValue("active")}
	p.Revision, _ = strconv.Atoi(req.PostFormValue("revision"))
	for k, vs := range req.PostForm {
		if len(vs) == 0 {
			continue
		}
		var name string
		var content []byte
		switch {
		case strings.HasPrefix(k, fileField) && strings.HasSuffix(k, "]"):
			name = k[len(fileField) : len(k)-1]
			content = []byte(strings.ReplaceAll(vs[0], "\r\n", "\n"))
		case strings.HasPrefix(k, binField) && strings.HasSuffix(k, "]"):
			name = k[len(binField) : len(k)-1]
			b, err := base64.StdEncoding.DecodeString(vs[0])
			if err != nil {
				return posted{}, "templates.err.bad_file"
			}
			content = b
		default:
			continue
		}
		if !validDraftPath(name) {
			return posted{}, "templates.err.bad_path"
		}
		if _, dup := p.Files[name]; dup {
			return posted{}, "templates.err.bad_path"
		}
		p.Files[name] = content
	}
	if len(p.Files) > maxDraftFiles {
		return posted{}, "templates.err.too_many"
	}
	return p, ""
}

func (h *handler) editorShell(c *echo.Context, info templates.Info) ui.Shell {
	s := h.shell(c, info.Name, "/templates")
	s.Scripts = []string{"js/editor.bundle.js"}
	s.PageKeys = "templates.hints.editor"
	s.Keys = append(s.Keys, ui.KeyGroup{Where: "templates.keys.editor", Keys: []ui.Key{
		{Keys: "⌘S", What: "templates.keys.save"}, {Keys: "tab", What: "templates.keys.indent"}, {Keys: "⌘F", What: "templates.keys.find"},
		{Keys: "⌘Z", What: "templates.keys.undo"}, {Keys: "esc", What: "templates.keys.leave"},
	}})
	return s
}

func (h *handler) renderEditor(c *echo.Context, status int, id int64, files map[string][]byte, v editorView) error {
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	v.T = info
	v.Next = info.Latest + 1
	if v.Servers, err = store.ActiveServersOf(ctx, h.Store.DB.R, id); err != nil {
		return err
	}
	if v.Agent, err = h.agentBand(c, id); err != nil {
		return err
	}
	v.Files, v.Rows = newEditorFiles(files)
	if _, ok := files[v.Active]; !ok {
		v.Active = manifest.Name
		if _, ok := files[v.Active]; !ok && len(v.Rows) > 0 {
			for _, r := range v.Rows {
				if !r.Dir {
					v.Active = r.Path
					break
				}
			}
		}
	}
	return web.Render(c, status, editorPage(h.editorShell(c, info), v))
}

func (h *handler) editor(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	d, err := h.Templates.EditDraft(c.Request().Context(), id, "admin")
	if err != nil {
		return notFound(err)
	}
	return h.renderEditor(c, http.StatusOK, id, d.Files, editorView{
		Active: c.QueryParam("file"), Revision: d.Revision, BasedOn: d.BasedOn, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy,
		Source: d.Source, Saved: c.QueryParam("saved") == "1",
	})
}

// staleEditor answers a save or validation whose revision is no longer the
// draft's: the posted files stay in the editor so nothing is lost, and the
// band says what to do.
func (h *handler) staleEditor(c *echo.Context, id int64, p posted) error {
	d, err := h.Templates.Draft(c.Request().Context(), id)
	if err != nil {
		return redirectGone(c, id, err)
	}
	return h.renderEditor(c, http.StatusConflict, id, p.Files, editorView{
		Active: p.Active, Revision: p.Revision, BasedOn: d.BasedOn, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy, Source: d.Source, Stale: true,
	})
}

// redirectGone sends the admin to the template page when its draft is gone.
func redirectGone(c *echo.Context, id int64, err error) error {
	if errors.Is(err, templates.ErrNoDraft) {
		return web.Redirect(c, actionHref(id, ""))
	}
	return notFound(err)
}

func (h *handler) draftSave(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, bad := readPosted(c)
	if bad != "" {
		return h.badForm(c, id, bad)
	}
	_, err = h.Templates.SaveDraft(ctx, id, p.Revision, p.Files, "admin", "admin")
	switch {
	case errors.Is(err, templates.ErrDraftChanged):
		return h.staleEditor(c, id, p)
	case err != nil:
		return redirectGone(c, id, err)
	}
	if c.FormValue("next") == "publish" {
		return web.Redirect(c, actionHref(id, "/publish"))
	}
	return web.Redirect(c, actionHref(id, "/edit?saved=1&file="+urlQuery(p.Active)))
}

// badForm answers a form the editor could not read with the editor page as
// far as the draft goes: nothing was posted that can be shown.
func (h *handler) badForm(c *echo.Context, id int64, key string) error {
	ctx := c.Request().Context()
	d, err := h.Templates.Draft(ctx, id)
	if err != nil {
		return redirectGone(c, id, err)
	}
	return h.renderEditor(c, http.StatusUnprocessableEntity, id, d.Files, editorView{
		Revision: d.Revision, BasedOn: d.BasedOn, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy, Source: d.Source,
		Error: i18n.T(ctx, key),
	})
}

func urlQuery(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "%20", "?", "%3F").Replace(s)
}

// ---- fragments (htmx) -------------------------------------------------------

type reportView struct {
	Findings []finding.Finding
	Errors   int
	Warnings int
	Revision int
	Stale    bool
	Saved    bool
}

// draftValidate saves what the editor holds (when it changed) and validates
// the saved draft, so the report is always about a draft that exists.
func (h *handler) draftValidate(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, bad := readPosted(c)
	if bad != "" {
		return web.Render(c, http.StatusOK, messageFragment(i18n.T(ctx, bad)))
	}
	rev, err := h.Templates.SaveDraft(ctx, id, p.Revision, p.Files, "admin", "admin")
	switch {
	case errors.Is(err, templates.ErrDraftChanged):
		return web.Render(c, http.StatusOK, staleFragment())
	case err != nil:
		return notFound(err)
	}
	report, err := h.Templates.ValidateDraft(ctx, id, "admin", "admin")
	if err != nil {
		return notFound(err)
	}
	fs := append([]finding.Finding(nil), report.Findings...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Severity == finding.Error && fs[j].Severity != finding.Error })
	return web.Render(c, http.StatusOK, reportFragment(reportView{
		Findings: fs, Errors: len(report.Errors()), Warnings: len(report.Warnings()), Revision: rev,
	}))
}

type previewView struct {
	Server     string
	IP         string
	ForReal    bool // rendered for a real server
	Reveal     bool // with its secrets
	ServerID   int64
	TemplateID int64
	Files      []previewFile
	Findings   []finding.Finding
}

type previewFile struct {
	Path  string
	Mode  string
	Lines []ui.CodeLine
}

func (h *handler) draftPreview(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, bad := readPosted(c)
	if bad != "" {
		return web.Render(c, http.StatusOK, messageFragment(i18n.T(ctx, bad)))
	}
	var pv templates.Preview
	v := previewView{TemplateID: id}
	if raw := c.FormValue("server"); raw != "" {
		serverID, perr := strconv.ParseInt(raw, 10, 64)
		srv, serr := store.GetServer(ctx, h.Store.DB.R, serverID)
		if perr != nil || serr != nil || srv.State != "active" || srv.TemplateID != id {
			return web.Render(c, http.StatusOK, messageFragment(i18n.T(ctx, "templates.preview.no_server")))
		}
		v.ForReal, v.Reveal, v.ServerID = true, c.FormValue("reveal") == "1", srv.ID
		build, berr := h.Provision.PreviewContext(ctx, srv.ID, v.Reveal)
		if berr != nil {
			return berr
		}
		if pv, err = h.Templates.PreviewFor(ctx, id, p.Files, build); err != nil {
			return notFound(err)
		}
		// Revealed or not, a preview of a real server is never kept by a cache.
		c.Response().Header().Set("Cache-Control", "no-store")
	} else if pv, err = h.Templates.PreviewFiles(ctx, id, p.Files); err != nil {
		return notFound(err)
	}
	v.Server, v.IP, v.Findings = pv.Server.Name, pv.Server.IP, pv.Findings
	for i, f := range pv.Files {
		v.Files = append(v.Files, previewFile{Path: f.Path, Mode: strconv.FormatUint(uint64(f.Mode.Perm()), 8), Lines: previewLines(f.Path, f.Content, i)})
	}
	return web.Render(c, http.StatusOK, previewFragment(v))
}

func previewLines(name string, content []byte, _ int) []ui.CodeLine {
	if isBinary(content) {
		return nil
	}
	return ui.Highlight(name, content)
}

// draftUpload adds a file, or unpacks a zip, into the posted tree and sends
// the tree back with the unsaved edits kept. Nothing is saved.
func (h *handler) draftUpload(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, bad := readPosted(c)
	msg := ""
	if bad != "" {
		msg = i18n.T(ctx, bad)
	}
	if bad == "" {
		f, hdr, ferr := c.Request().FormFile("upload")
		if ferr != nil {
			msg = i18n.T(ctx, "templates.err.no_file")
		} else {
			defer func() { _ = f.Close() }()
			p.Active, msg = addUpload(ctx, p, f, hdr.Filename, hdr.Size, c.Request().FormValue("dir"))
		}
	}
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	v := editorView{T: info, Active: p.Active, Revision: p.Revision, Message: msg}
	v.Files, v.Rows = newEditorFiles(p.Files)
	if _, ok := p.Files[v.Active]; !ok {
		v.Active = manifest.Name
	}
	return web.Render(c, http.StatusOK, editorFilesResponse(v))
}

// addUpload merges an upload into p.Files and returns the file to select and
// a translated message when it was refused.
func addUpload(ctx context.Context, p posted, f multipart.File, name string, size int64, dir string) (string, string) {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		files, err := templates.ReadZip(f, size)
		if err != nil {
			return p.Active, i18n.T(ctx, importErrKey(err))
		}
		if len(p.Files)+len(files) > maxDraftFiles {
			return p.Active, i18n.T(ctx, "templates.err.too_many")
		}
		for k, v := range files {
			p.Files[k] = v
		}
		return manifest.Name, ""
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return p.Active, i18n.T(ctx, "templates.err.bad_file")
	}
	dir = strings.Trim(dir, "/")
	full := name
	if dir != "" {
		full = dir + "/" + name
	}
	if !validDraftPath(full) {
		return p.Active, i18n.T(ctx, "templates.err.bad_path")
	}
	if _, exists := p.Files[full]; !exists && len(p.Files) >= maxDraftFiles {
		return p.Active, i18n.T(ctx, "templates.err.too_many")
	}
	p.Files[full] = body
	return full, ""
}

// ---- discard ----------------------------------------------------------------

func (h *handler) draftDiscard(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	if err := h.Templates.DiscardDraft(c.Request().Context(), id, "admin"); err != nil && !errors.Is(err, templates.ErrNoDraft) {
		return notFound(err)
	}
	return web.Redirect(c, actionHref(id, "?saved=discarded"))
}

// ---- publish ----------------------------------------------------------------

type publishView struct {
	T        templates.Info
	Report   finding.Report
	Revision int
	Number   int
	Notes    string
	Stale    bool
	Confirm  bool // warnings were not confirmed
	Existing int  // servers built from this template
}

func (h *handler) renderPublish(c *echo.Context, status int, id int64, v publishView) error {
	ctx := c.Request().Context()
	info, err := h.Templates.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	v.T, v.Number, v.Existing = info, info.Latest+1, info.Servers
	s := h.shell(c, i18n.T(ctx, "templates.publish.title", i18n.Args{"n": v.Number}), "/templates")
	return web.Render(c, status, publishPage(s, v))
}

func (h *handler) publishPage(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	d, err := h.Templates.Draft(ctx, id)
	if err != nil {
		return redirectGone(c, id, err)
	}
	report, err := h.Templates.Report(ctx, id)
	if err != nil {
		return notFound(err)
	}
	return h.renderPublish(c, http.StatusOK, id, publishView{Report: report, Revision: d.Revision})
}

func (h *handler) publishDo(c *echo.Context) error {
	id, err := tplID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	rev, _ := strconv.Atoi(c.FormValue("revision"))
	notes := strings.TrimSpace(c.FormValue("notes"))
	confirm := c.FormValue("confirm_warnings") == "1"
	n, err := h.Templates.Publish(ctx, id, rev, notes, confirm, "admin")
	var ve *templates.ValidationError
	var we *templates.WarningsError
	switch {
	case err == nil:
		return web.Redirect(c, actionHref(id, "?saved=published&v="+strconv.Itoa(n)))
	case errors.As(err, &ve):
		return h.renderPublish(c, http.StatusUnprocessableEntity, id, publishView{Report: ve.Report, Revision: rev, Notes: notes})
	case errors.As(err, &we):
		return h.renderPublish(c, http.StatusUnprocessableEntity, id, publishView{Report: we.Report, Revision: rev, Notes: notes, Confirm: true})
	case errors.Is(err, templates.ErrDraftChanged):
		return h.renderPublish(c, http.StatusConflict, id, publishView{Revision: rev, Notes: notes, Stale: true})
	}
	return redirectGone(c, id, err)
}
