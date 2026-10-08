package pages

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/country"
	"github.com/tikhonp/proxier/internal/modules/servers/retire"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

func (h *handler) registerRetire(r web.Routes) {
	r.Admin.GET("/servers/:id/retire", h.retirePage)
	r.Admin.POST("/servers/:id/retire", h.retirePost)
	r.Admin.POST("/servers/:id/retire/retry", h.retireRetry)
}

type retireView struct {
	S             store.Server
	Flag          string
	Usage         string
	DNS           []string
	StackPossible bool
	RemoveStack   bool
	Typed         string
	Err           string
}

func (h *handler) retireView(c *echo.Context, s store.Server) (retireView, error) {
	ctx := c.Request().Context()
	cons, err := h.Retire.Consequences(ctx, s.ID)
	if err != nil {
		return retireView{}, err
	}
	v := retireView{S: s, Flag: country.Flag(s.Country), StackPossible: cons.StackPossible, RemoveStack: cons.StackDefault}
	v.Usage = i18n.T(ctx, "servers.retire.usage_unknown")
	if cons.UsageKnown && (cons.Links > 0 || len(cons.Subscriptions) > 0) {
		v.Usage = i18n.T(ctx, "servers.retire.usage", i18n.Args{"links": cons.Links, "subs": len(cons.Subscriptions)})
	}
	for _, r := range cons.DNS {
		v.DNS = append(v.DNS, r.Name+" → "+r.Content)
	}
	return v, nil
}

func (h *handler) renderRetire(c *echo.Context, status int, v retireView) error {
	ctx := c.Request().Context()
	return web.Render(c, status, retirePageView(h.shell(c, i18n.T(ctx, "servers.retire.title", i18n.Args{"name": v.S.Name}), "/servers"), v))
}

// retirePage is the dialog: what retirement will do and the name to type.
func (h *handler) retirePage(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	if s.State == "retired" || s.Retiring() {
		return web.Redirect(c, serverHref(s.ID))
	}
	v, err := h.retireView(c, s)
	if err != nil {
		return err
	}
	return h.renderRetire(c, http.StatusOK, v)
}

func (h *handler) retirePost(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	remove := c.FormValue("remove_stack") == "1"
	_, err = h.Retire.Retire(ctx, s.ID, c.FormValue("name"), remove, "admin")
	switch {
	case errors.Is(err, retire.ErrNameMismatch):
		v, verr := h.retireView(c, s)
		if verr != nil {
			return verr
		}
		v.Typed, v.RemoveStack = c.FormValue("name"), remove && v.StackPossible
		v.Err = i18n.T(ctx, "servers.retire.mismatch")
		return h.renderRetire(c, http.StatusUnprocessableEntity, v)
	case errors.Is(err, retire.ErrNotAllowed):
		return echo.NewHTTPError(http.StatusConflict, "the server is retired or already being retired")
	case err != nil:
		return err
	}
	return web.Redirect(c, serverHref(s.ID))
}

func (h *handler) retireRetry(c *echo.Context) error {
	s, err := h.loadServer(c)
	if err != nil {
		return err
	}
	_, err = h.Retire.RetryFailed(c.Request().Context(), s.ID, "admin")
	if errors.Is(err, retire.ErrNotRetiring) {
		return echo.NewHTTPError(http.StatusConflict, "the server's retirement has not failed")
	}
	if err != nil {
		return err
	}
	return web.Redirect(c, serverHref(s.ID))
}
