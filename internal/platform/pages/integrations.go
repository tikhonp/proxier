package pages

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type integrationRow = ui.IntegrationRow

func (h *handler) integrationsPage(c *echo.Context) error {
	ctx := c.Request().Context()
	bot := h.telegramBot(ctx)
	state := i18n.T(ctx, "integrations.not_configured")
	if bot.TokenSet {
		state = i18n.T(ctx, "integrations.telegram.state", i18n.Args{"bot": "@" + bot.Username})
	}
	rows := []integrationRow{{Name: "Telegram", Href: "/settings/integrations/telegram", State: state, On: bot.TokenSet, SetUp: true}}
	ts := h.Tailnet.Status(ctx)
	tsState := i18n.T(ctx, "tailnet.state."+string(ts.State))
	if ts.State == tailnet.Running && ts.IP.IsValid() {
		tsState += " · " + ts.IP.String()
	}
	rows = append(rows, integrationRow{Name: "Tailnet", Href: "/settings/integrations/tailnet", State: tsState, On: ts.State == tailnet.Running})
	for _, m := range h.Integrators {
		rows = append(rows, m.Integrations(ctx)...)
	}
	s := h.shell(c, i18n.T(ctx, "settings.integrations"), "/settings")
	return web.Render(c, http.StatusOK, integrationsPage(s, h.sortedPages(), rows))
}

// telegramBot is the one bot, on Settings → Integrations → Telegram. Each
// member's chat is on their Settings → Notifications.
type telegramBot struct {
	Username string
	TokenSet bool
	// Set after an action.
	Saved    bool
	TokenErr string // translated
}

func (h *handler) telegramBot(ctx context.Context) telegramBot {
	get := func(key string) string {
		v, e := h.Settings.Get(ctx, key)
		if e != nil {
			h.Log.Error("pages: telegram settings", "key", key, "error", e)
		}
		return v
	}
	return telegramBot{Username: get("telegram.bot_username"), TokenSet: get("telegram.bot_token") != ""}
}

func (h *handler) telegramPage(c *echo.Context) error {
	bot := h.telegramBot(c.Request().Context())
	bot.Saved = c.QueryParam("saved") == "token"
	return h.renderTelegram(c, http.StatusOK, bot)
}

func (h *handler) renderTelegram(c *echo.Context, status int, bot telegramBot) error {
	s := h.shell(c, "Telegram", "/settings")
	return web.Render(c, status, telegramPage(s, h.sortedPages(), bot))
}

// telegramToken checks the token with getMe and saves it with the bot's
// username; a refused token saves nothing.
func (h *handler) telegramToken(c *echo.Context) error {
	ctx := c.Request().Context()
	bot := h.telegramBot(ctx)
	fail := func(msg string) error {
		bot.TokenErr = i18n.T(ctx, msg)
		return h.renderTelegram(c, http.StatusUnprocessableEntity, bot)
	}
	token := strings.TrimSpace(c.FormValue("token"))
	if token == "" {
		return fail("telegram.token.empty")
	}
	me, err := h.Telegram.Client(token).GetMe(ctx)
	var api *telegram.APIError
	switch {
	case errors.As(err, &api) && api.Code >= 400 && api.Code < 500:
		return fail("telegram.token.refused")
	case err != nil:
		h.Log.Warn("pages: telegram getMe", "error", err)
		return fail("telegram.unreachable")
	}
	err = h.Settings.Set(ctx, web.Actor(ctx), "telegram", map[string]string{
		"telegram.bot_token": token, "telegram.bot_username": me.Username,
	})
	if err != nil {
		var fe settings.FieldErrors
		if errors.As(err, &fe) {
			bot.TokenErr = fe["telegram.bot_token"]
			return h.renderTelegram(c, http.StatusUnprocessableEntity, bot)
		}
		return err
	}
	return web.Redirect(c, "/settings/integrations/telegram?saved=token")
}

func (h *handler) baseLink(path string) string {
	if h.Cfg.BaseURL == nil {
		return ""
	}
	return strings.TrimRight(h.Cfg.BaseURL.String(), "/") + path
}
