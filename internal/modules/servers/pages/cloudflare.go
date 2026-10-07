package pages

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// cfView is Settings → Integrations → Cloudflare.
type cfView struct {
	TokenSet bool
	Allowed  []string          // the ticked zones (the setting)
	Listed   []cloudflare.Zone // what the token sees; nil when it could not be listed
	ListErr  string            // translated
	TokenErr string            // translated
	Saved    string            // token | zones
}

// cfClient is a client for the saved token; ok=false without a token.
func (h *handler) cfClient(ctx context.Context) (c *cloudflare.Client, ok bool, err error) {
	tok, err := h.Settings.Get(ctx, cloudflare.TokenKey)
	if err != nil || tok == "" {
		return nil, false, err
	}
	return h.CloudflareClient(tok), true, nil
}

// cfErr is the sentence for a Cloudflare failure.
func cfErr(ctx context.Context, err error) string {
	if errors.Is(err, cloudflare.ErrTokenRejected) {
		return i18n.T(ctx, "servers.cloudflare.refused")
	}
	return i18n.T(ctx, "servers.cloudflare.unreachable")
}

func (h *handler) cloudflareState(ctx context.Context) (cfView, error) {
	var v cfView
	zones, err := h.Settings.Get(ctx, cloudflare.ZonesKey)
	if err != nil {
		return v, err
	}
	v.Allowed = dns.SplitZones(zones)
	c, ok, err := h.cfClient(ctx)
	if err != nil {
		return v, err
	}
	v.TokenSet = ok
	if ok {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		zs, err := c.Zones(ctx)
		if err != nil {
			h.Log.Warn("pages: cloudflare zones", "error", err)
			v.ListErr = cfErr(ctx, err)
		} else {
			v.Listed = zs
		}
	}
	return v, nil
}

func (h *handler) cloudflarePage(c *echo.Context) error {
	v, err := h.cloudflareState(c.Request().Context())
	if err != nil {
		return err
	}
	v.Saved = c.QueryParam("saved")
	return h.renderCloudflare(c, http.StatusOK, v)
}

func (h *handler) renderCloudflare(c *echo.Context, status int, v cfView) error {
	s := h.shell(c, "Cloudflare", "/settings")
	return web.Render(c, status, cloudflarePage(s, h.settingsPages(), v))
}

// cloudflareToken checks the token against Cloudflare and saves it only when
// Cloudflare accepts it and can list zones with it.
func (h *handler) cloudflareToken(c *echo.Context) error {
	ctx := c.Request().Context()
	fail := func(status int, msg string) error {
		v, err := h.cloudflareState(ctx)
		if err != nil {
			return err
		}
		v.TokenErr = msg
		return h.renderCloudflare(c, status, v)
	}
	token := strings.TrimSpace(c.FormValue("token"))
	if token == "" {
		return fail(http.StatusUnprocessableEntity, i18n.T(ctx, "servers.cloudflare.token_empty"))
	}
	client := h.CloudflareClient(token)
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := client.VerifyToken(vctx)
	if err == nil {
		_, err = client.Zones(vctx)
	}
	if err != nil {
		h.Log.Warn("pages: cloudflare token check", "error", err)
		status := http.StatusUnprocessableEntity
		if !errors.Is(err, cloudflare.ErrTokenRejected) {
			status = http.StatusBadGateway
		}
		return fail(status, cfErr(ctx, err))
	}
	if err := h.Settings.Set(ctx, "admin", "cloudflare", map[string]string{cloudflare.TokenKey: token}); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/integrations/cloudflare?saved=token")
}

// cloudflareZones saves the ticked zones: only ones the token sees.
func (h *handler) cloudflareZones(c *echo.Context) error {
	ctx := c.Request().Context()
	v, err := h.cloudflareState(ctx)
	if err != nil {
		return err
	}
	if !v.TokenSet {
		v.TokenErr = i18n.T(ctx, "servers.cloudflare.no_token")
		return h.renderCloudflare(c, http.StatusUnprocessableEntity, v)
	}
	if v.Listed == nil {
		return h.renderCloudflare(c, http.StatusBadGateway, v) // ListErr says why
	}
	form, _ := c.FormValues()
	var ticked []string
	for _, z := range v.Listed {
		if slices.Contains(form["zone"], z.Name) {
			ticked = append(ticked, z.Name)
		}
	}
	if err := h.Settings.Set(ctx, "admin", "cloudflare", map[string]string{cloudflare.ZonesKey: strings.Join(ticked, ",")}); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/integrations/cloudflare?saved=zones")
}

// cfTestView is the Test fragment.
type cfTestView struct {
	Zones []cloudflare.Zone
	Err   string
}

// cloudflareTest lists the zones again, to show the token still works.
func (h *handler) cloudflareTest(c *echo.Context) error {
	ctx := c.Request().Context()
	client, ok, err := h.cfClient(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return web.Render(c, http.StatusOK, cloudflareTestFragment(cfTestView{Err: i18n.T(ctx, "servers.cloudflare.no_token")}))
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	zs, err := client.Zones(tctx)
	if err != nil {
		h.Log.Warn("pages: cloudflare test", "error", err)
		return web.Render(c, http.StatusOK, cloudflareTestFragment(cfTestView{Err: cfErr(ctx, err)}))
	}
	return web.Render(c, http.StatusOK, cloudflareTestFragment(cfTestView{Zones: zs}))
}
