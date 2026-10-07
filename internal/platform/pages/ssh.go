package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type sshView struct {
	Line, Fingerprint string
	Hosts             []sshx.KnownHost
	Keys              string // personal keys, as typed or saved
	KeysChanged       bool
	KeysErr           string // translated
	Saved             string // regenerated | keys | accepted | forgotten
}

func (h *handler) sshPage(c *echo.Context) error {
	return h.renderSSH(c, http.StatusOK, sshView{Saved: c.QueryParam("saved")})
}

func (h *handler) renderSSH(c *echo.Context, status int, v sshView) error {
	ctx := c.Request().Context()
	var err error
	if v.Line, v.Fingerprint, err = h.SSH.PublicKey(ctx); err != nil {
		return err
	}
	if v.Hosts, err = h.SSH.KnownHosts(ctx); err != nil {
		return err
	}
	if v.KeysErr == "" {
		val, err := h.Settings.Lookup(ctx, sshx.PersonalKeysField)
		if err != nil {
			return err
		}
		v.Keys, v.KeysChanged = val.Value, !val.IsDefault
	}
	s := h.shell(c, i18n.T(ctx, "settings.ssh"), "/settings")
	return web.Render(c, status, sshPage(s, h.sortedPages(), v))
}

func (h *handler) sshRegenerate(c *echo.Context) error {
	if err := h.SSH.Regenerate(c.Request().Context(), "admin"); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/ssh?saved=regenerated")
}

func (h *handler) sshPersonalKeys(c *echo.Context) error {
	ctx := c.Request().Context()
	text := strings.ReplaceAll(c.FormValue("keys"), "\r\n", "\n")
	err := h.Settings.Set(ctx, "admin", "ssh", map[string]string{sshx.PersonalKeysField: text})
	var fe settings.FieldErrors
	if errors.As(err, &fe) {
		return h.renderSSH(c, http.StatusUnprocessableEntity, sshView{Keys: text, KeysErr: fe[sshx.PersonalKeysField]})
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, "/settings/ssh?saved=keys")
}

func (h *handler) hostID(c *echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return 0, echo.ErrNotFound
	}
	return id, nil
}

func (h *handler) sshAccept(c *echo.Context) error {
	id, err := h.hostID(c)
	if err != nil {
		return err
	}
	// Nothing waiting (someone else got there first) is as good as accepted.
	switch err := h.SSH.Accept(c.Request().Context(), id, "admin"); {
	case errors.Is(err, sshx.ErrNotFound):
		return echo.ErrNotFound
	case err != nil && !errors.Is(err, sshx.ErrNoPending):
		return err
	}
	return web.Redirect(c, "/settings/ssh?saved=accepted")
}

func (h *handler) sshForget(c *echo.Context) error {
	id, err := h.hostID(c)
	if err != nil {
		return err
	}
	if err := h.SSH.Forget(c.Request().Context(), id, "admin"); errors.Is(err, sshx.ErrNotFound) {
		return echo.ErrNotFound
	} else if err != nil {
		return err
	}
	return web.Redirect(c, "/settings/ssh?saved=forgotten")
}

// hostCount is "N known hosts", as the regenerate dialog and the list title say it.
func hostCount(ctx context.Context, n int) string {
	return i18n.N(ctx, "ssh.hosts_count", int64(n))
}
