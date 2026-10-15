package pages

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform/access"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
	"github.com/tikhonp/proxier/internal/platform/web"
)

// ruleGroup is the rules of one module.
type ruleGroup struct {
	Module  string
	Rules   []notify.Rule
	Changed int // rules not at their default
}

type notificationsView struct {
	Groups     []ruleGroup
	OnlyChange bool
	Total      int
	Chat       chatView
}

// chatView is the member's own Telegram chat on their Settings →
// Notifications.
type chatView struct {
	Bot         string // the bot's username, "" while there is no bot
	ID, Title   string
	MaySetUpBot bool // settings: manage, who sets the bot up
	// Set after an action.
	Saved bool
	Err   string // translated
}

func (v chatView) Label() string {
	if v.Title != "" {
		return v.Title
	}
	return v.ID
}

func (h *handler) chatView(ctx context.Context) chatView {
	q := web.FromContext(ctx)
	v := chatView{ID: q.Member.TelegramChatID, Title: q.Member.TelegramChatTitle,
		MaySetUpBot: q.Grant.Can(access.Settings, access.LevelManage)}
	if bot := h.telegramBot(ctx); bot.TokenSet {
		v.Bot = bot.Username
	}
	return v
}

func (h *handler) notificationsPage(c *echo.Context) error {
	ctx := c.Request().Context()
	v := h.chatView(ctx)
	v.Saved = c.QueryParam("saved") == "chat"
	return h.renderNotifications(c, http.StatusOK, v)
}

func (h *handler) renderNotifications(c *echo.Context, status int, chat chatView) error {
	ctx := c.Request().Context()
	q := web.FromContext(ctx)
	rules, err := h.Notify.Rules(ctx, q.Member.ID, q.Grant)
	if err != nil {
		return err
	}
	only := c.QueryParam("show") == "changed"
	v := notificationsView{OnlyChange: only, Total: len(rules), Chat: chat}
	by := map[string][]notify.Rule{}
	for _, r := range rules {
		if only && r.Enabled == r.Default {
			continue
		}
		by[r.Type.Module] = append(by[r.Type.Module], r)
	}
	mods := make([]string, 0, len(by))
	for m := range by {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	for _, m := range mods {
		g := ruleGroup{Module: m, Rules: by[m]}
		for _, r := range g.Rules {
			if r.Enabled != r.Default {
				g.Changed++
			}
		}
		v.Groups = append(v.Groups, g)
	}
	s := h.shell(c, i18n.T(ctx, "settings.notifications"), "/settings")
	return web.Render(c, status, notificationsPage(s, h.sortedPages(), v))
}

// notificationsSet turns one of the member's rules on or off.
func (h *handler) notificationsSet(c *echo.Context) error {
	ctx := c.Request().Context()
	var on bool
	switch c.FormValue("enabled") {
	case "1":
		on = true
	case "0":
	default:
		return echo.ErrBadRequest
	}
	q := web.FromContext(ctx)
	err := h.Notify.SetRule(ctx, q.Member.ID, q.Grant, c.Param("type"), on)
	if errors.Is(err, notify.ErrUnknownRule) {
		return echo.ErrNotFound
	}
	if err != nil {
		return err
	}
	to := "/settings/notifications"
	if c.FormValue("show") == "changed" {
		to += "?show=changed"
	}
	// back at the rule: on a phone its group is the one left open
	return web.Redirect(c, to+"#rule-"+c.Param("type"))
}

// detectView is the "Detect chat" fragment.
type detectView struct {
	Chats    []telegram.Chat
	Username string
	Err      string // translated
}

// notificationsDetect lists the chats that wrote to the bot lately. Another
// member's chat is left out: it is theirs, and naming it would tell who
// else gets what.
func (h *handler) notificationsDetect(c *echo.Context) error {
	ctx := c.Request().Context()
	bot := h.telegramBot(ctx)
	if !bot.TokenSet {
		return web.Render(c, http.StatusOK, detectFragment(detectView{Err: i18n.T(ctx, "telegram.detect.no_token")}))
	}
	token, err := h.Settings.Get(ctx, "telegram.bot_token")
	if err != nil {
		return err
	}
	roster, err := h.Auth.Roster(ctx)
	if err != nil {
		return err
	}
	me := web.FromContext(ctx).Member.ID
	taken := map[string]bool{}
	for _, m := range roster {
		if m.ID != me && !m.Removed() && m.TelegramChatID != "" {
			taken[m.TelegramChatID] = true
		}
	}
	updates, err := h.Telegram.Client(token).GetUpdates(ctx, 100)
	if err != nil {
		h.Log.Warn("pages: telegram getUpdates", "error", err)
		return web.Render(c, http.StatusOK, detectFragment(detectView{Err: i18n.T(ctx, "telegram.unreachable")}))
	}
	v := detectView{Username: bot.Username}
	for _, u := range updates {
		takenByName := u.Chat.Username != "" && taken["@"+u.Chat.Username]
		if !taken[chatIDString(u.Chat.ID)] && !takenByName {
			v.Chats = append(v.Chats, u.Chat)
		}
	}
	return web.Render(c, http.StatusOK, detectFragment(v))
}

// notificationsChat saves the member's chat, picked from Detect or typed;
// an empty one stops their notifications.
func (h *handler) notificationsChat(c *echo.Context) error {
	ctx := c.Request().Context()
	id, title := strings.TrimSpace(c.FormValue("chat_id")), strings.TrimSpace(c.FormValue("chat_title"))
	if telegram.ValidChatID(id) != nil || len(title) > 128 {
		v := h.chatView(ctx)
		v.Err = i18n.T(ctx, "telegram.chat.invalid")
		return h.renderNotifications(c, http.StatusUnprocessableEntity, v)
	}
	if err := h.Auth.SetTelegramChat(ctx, web.FromContext(ctx).Member.ID, id, title); err != nil {
		return err
	}
	return web.Redirect(c, "/settings/notifications?saved=chat")
}

// testView is the "Send test" fragment.
type testView struct {
	OK  bool
	Err string
}

// notificationsTest sends a test message to the member's chat from the
// handler itself: no transaction is open, and the answer is shown at once.
// The chat stays saved whatever happens.
func (h *handler) notificationsTest(c *echo.Context) error {
	ctx := c.Request().Context()
	v := h.chatView(ctx)
	if v.Bot == "" || v.ID == "" {
		return web.Render(c, http.StatusOK, testFragment(testView{Err: i18n.T(ctx, "telegram.test.not_configured")}))
	}
	err := h.Telegram.Send(ctx, v.ID, notify.Message{
		Emoji: "🟢", Title: i18n.T(ctx, "telegram.test.title"), Body: i18n.T(ctx, "telegram.test.body"),
		URL: h.baseLink("/settings/notifications"), Button: i18n.T(ctx, "notify.open"),
	})
	if err != nil {
		h.Log.Warn("pages: telegram test", "error", err)
		return web.Render(c, http.StatusOK, testFragment(testView{Err: err.Error()}))
	}
	return web.Render(c, http.StatusOK, testFragment(testView{OK: true}))
}

// notificationRetry sends one of the member's failed notifications again.
func (h *handler) notificationRetry(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return echo.ErrNotFound
	}
	if err := h.Notify.Retry(ctx, web.FromContext(ctx).Member.ID, id); err != nil && !errors.Is(err, notify.ErrNotFailed) {
		return err
	}
	return web.Redirect(c, "/")
}

// failedSince is the dashboard's window.
const failedWindow = 7 * 24 * time.Hour

func chatIDString(id int64) string { return strconv.FormatInt(id, 10) }

// moduleLabel names a group of rules; a module without a translation shows its name.
func moduleLabel(ctx context.Context, module string) string {
	return tr(ctx, "notifications.module."+module, module)
}
