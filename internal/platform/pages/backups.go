package pages

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/backup"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type backupsView struct {
	Section settings.Section
	Values  []settings.Value
	Errs    settings.FieldErrors
	List    []backup.Backup
	Saved   bool
}

func (h *handler) backupsPage(c *echo.Context) error {
	return h.renderBackups(c, http.StatusOK, nil, nil, c.QueryParam("saved") == "1")
}

func (h *handler) renderBackups(c *echo.Context, status int, submitted map[string]string, errs settings.FieldErrors, saved bool) error {
	ctx := c.Request().Context()
	vals, sec, err := h.sectionValues(ctx, "backups", submitted)
	if err != nil {
		return err
	}
	list, err := h.Backup.List()
	if err != nil {
		return err
	}
	s := h.shell(c, i18n.T(ctx, "settings.backups"), "/settings")
	return web.Render(c, status, backupsPage(s, h.sortedPages(), backupsView{Section: sec, Values: vals, Errs: errs, List: list, Saved: saved}))
}

func (h *handler) backupsSave(c *echo.Context) error {
	ctx := c.Request().Context()
	var sec settings.Section
	for _, s := range h.Settings.Sections() {
		if s.Name == "backups" {
			sec = s
		}
	}
	values := h.submitted(c, sec)
	if err := h.Settings.Set(ctx, "admin", "backups", values); err != nil {
		var fe settings.FieldErrors
		if errors.As(err, &fe) {
			return h.renderBackups(c, http.StatusUnprocessableEntity, values, fe, false)
		}
		return err
	}
	return web.Redirect(c, "/settings/backups?saved=1")
}

// backupsRun queues a backup now and shows its job.
func (h *handler) backupsRun(c *echo.Context) error {
	req := h.Backup.Request()
	req.CreatedBy = "admin"
	e, err := h.Jobs.EnqueueNow(c.Request().Context(), req)
	if err != nil {
		return err
	}
	return web.Redirect(c, "/jobs/"+strconv.FormatInt(e.ID, 10))
}

// backupsLatest streams the newest backup. Its secrets are still sealed:
// restoring needs the same master key.
func (h *handler) backupsLatest(c *echo.Context) error {
	b, rc, err := h.Backup.Latest()
	if errors.Is(err, backup.ErrNone) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	hd := c.Response().Header()
	hd.Set("Content-Disposition", `attachment; filename="`+b.Name+`"`)
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Length", strconv.FormatInt(b.Size, 10))
	return c.Stream(http.StatusOK, "application/vnd.sqlite3", rc)
}

func backupSize(n int64) string {
	const mb = 1 << 20
	if n < mb {
		return strconv.FormatInt((n+1023)/1024, 10) + " KB"
	}
	return strconv.FormatFloat(float64(n)/mb, 'f', 1, 64) + " MB"
}
