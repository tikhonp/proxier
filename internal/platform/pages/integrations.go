package pages

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
)

type integrationRow = ui.IntegrationRow

func (h *handler) integrationsPage(c *echo.Context) error {
	ctx := c.Request().Context()
	tg, err := h.telegramState(ctx)
	if err != nil {
		return err
	}
	state := i18n.T(ctx, "integrations.not_configured")
	if tg.Configured {
		state = i18n.T(ctx, "integrations.telegram.state", i18n.Args{"bot": "@" + tg.Username, "chat": tg.chatLabel()})
	}
	rows := []integrationRow{{Name: "Telegram", Href: "/settings/integrations/telegram", State: state, On: tg.Configured}}
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

// telegramState is what the Telegram page and the integrations list show.
type telegramState struct {
	Username, ChatID, ChatTitle string
	TokenSet, Configured        bool
	// Set after an action.
	Saved    string // "token" | "chat"
	TokenErr string // translated
	ChatErr  string // translated
}

func (t telegramState) chatLabel() string {
	if t.ChatTitle != "" {
		return t.ChatTitle
	}
	return t.ChatID
}

func (h *handler) telegramState(ctx context.Context) (telegramState, error) {
	var t telegramState
	get := func(key string) string {
		v, e := h.Settings.Get(ctx, key)
		if e != nil {
			h.Log.Error("pages: telegram settings", "key", key, "error", e)
		}
		return v
	}
	t.Username, t.ChatID, t.ChatTitle = get("telegram.bot_username"), get("telegram.chat_id"), get("telegram.chat_title")
	t.TokenSet = get("telegram.bot_token") != ""
	t.Configured = t.TokenSet && t.ChatID != ""
	return t, nil
}

func (h *handler) telegramPage(c *echo.Context) error {
	t, err := h.telegramState(c.Request().Context())
	if err != nil {
		return err
	}
	t.Saved = c.QueryParam("saved")
	return h.renderTelegram(c, http.StatusOK, t)
}

func (h *handler) renderTelegram(c *echo.Context, status int, t telegramState) error {
	s := h.shell(c, "Telegram", "/settings")
	return web.Render(c, status, telegramPage(s, h.sortedPages(), t))
}

// telegramToken checks the token with getMe and saves it with the bot's
// username; a refused token saves nothing.
func (h *handler) telegramToken(c *echo.Context) error {
	ctx := c.Request().Context()
	t, err := h.telegramState(ctx)
	if err != nil {
		return err
	}
	fail := func(msg string) error {
		t.TokenErr = i18n.T(ctx, msg)
		return h.renderTelegram(c, http.StatusUnprocessableEntity, t)
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
	err = h.Settings.Set(ctx, "admin", "telegram", map[string]string{
		"telegram.bot_token": token, "telegram.bot_username": me.Username,
	})
	if err != nil {
		var fe settings.FieldErrors
		if errors.As(err, &fe) {
			t.TokenErr = fe["telegram.bot_token"]
			return h.renderTelegram(c, http.StatusUnprocessableEntity, t)
		}
		return err
	}
	return web.Redirect(c, "/settings/integrations/telegram?saved=token")
}

// detectView is the "Detect chat" fragment.
type detectView struct {
	Chats    []telegram.Chat
	Username string
	Err      string // translated
}

func (h *handler) telegramDetect(c *echo.Context) error {
	ctx := c.Request().Context()
	t, err := h.telegramState(ctx)
	if err != nil {
		return err
	}
	if !t.TokenSet {
		return web.Render(c, http.StatusOK, detectFragment(detectView{Err: i18n.T(ctx, "telegram.detect.no_token")}))
	}
	token, err := h.Settings.Get(ctx, "telegram.bot_token")
	if err != nil {
		return err
	}
	updates, err := h.Telegram.Client(token).GetUpdates(ctx, 100)
	if err != nil {
		h.Log.Warn("pages: telegram getUpdates", "error", err)
		return web.Render(c, http.StatusOK, detectFragment(detectView{Err: i18n.T(ctx, "telegram.unreachable")}))
	}
	v := detectView{Username: t.Username}
	for _, u := range updates {
		v.Chats = append(v.Chats, u.Chat)
	}
	return web.Render(c, http.StatusOK, detectFragment(v))
}

// telegramChat saves the chat picked from Detect, or typed.
func (h *handler) telegramChat(c *echo.Context) error {
	ctx := c.Request().Context()
	id, title := strings.TrimSpace(c.FormValue("chat_id")), strings.TrimSpace(c.FormValue("chat_title"))
	err := h.Settings.Set(ctx, "admin", "telegram", map[string]string{"telegram.chat_id": id, "telegram.chat_title": title})
	if err != nil {
		var fe settings.FieldErrors
		if errors.As(err, &fe) {
			t, serr := h.telegramState(ctx)
			if serr != nil {
				return serr
			}
			t.ChatErr = i18n.T(ctx, "telegram.chat.invalid")
			return h.renderTelegram(c, http.StatusUnprocessableEntity, t)
		}
		return err
	}
	return web.Redirect(c, "/settings/integrations/telegram?saved=chat")
}

// testView is the "Send test" fragment.
type testView struct {
	OK  bool
	Err string
}

// telegramTest sends a test message from the handler itself: no transaction is
// open, and the answer is shown at once. The chat stays saved whatever happens.
func (h *handler) telegramTest(c *echo.Context) error {
	ctx := c.Request().Context()
	t, err := h.telegramState(ctx)
	if err != nil {
		return err
	}
	if !t.Configured {
		return web.Render(c, http.StatusOK, testFragment(testView{Err: i18n.T(ctx, "telegram.test.not_configured")}))
	}
	err = h.Telegram.Send(ctx, notify.Message{
		Emoji: "🟢", Title: i18n.T(ctx, "telegram.test.title"), Body: i18n.T(ctx, "telegram.test.body"),
		URL: h.baseLink("/settings/integrations/telegram"), Button: i18n.T(ctx, "notify.open"),
	})
	if err != nil {
		h.Log.Warn("pages: telegram test", "error", err)
		return web.Render(c, http.StatusOK, testFragment(testView{Err: err.Error()}))
	}
	return web.Render(c, http.StatusOK, testFragment(testView{OK: true}))
}

func (h *handler) baseLink(path string) string {
	if h.Cfg.BaseURL == nil {
		return ""
	}
	return strings.TrimRight(h.Cfg.BaseURL.String(), "/") + path
}
