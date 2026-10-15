package telegram

import (
	"context"
	"errors"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Section is Settings → Integrations → Telegram: the one bot. Each member's
// chat is their own (auth.Member.TelegramChatID). The page does not use the
// generic form.
var Section = settings.Section{
	Name: "telegram", Module: "platform",
	Fields: []settings.Field{
		{Key: "telegram.bot_token", Kind: settings.Secret, MaxLen: 200},
		{Key: "telegram.bot_username", Kind: settings.String, MaxLen: 64},
	},
}

// ValidChatID checks a chat ID: an integer, or @channel. "" is no chat.
func ValidChatID(s string) error {
	if len(s) > 64 {
		return errors.New("at most 64 characters")
	}
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "@") && len(s) > 1 && !strings.ContainsAny(s, " \t") {
		return nil
	}
	if _, err := strconv.ParseInt(s, 10, 64); err != nil {
		return errors.New("a number, or @channel")
	}
	return nil
}

// minGap is the pause between two messages: Telegram's per-chat limit.
const minGap = time.Second

// Channel sends notifications through the bot in settings to each member's
// own chat. It reads the token on every send, so a change applies at once.
type Channel struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// BaseURL and HTTP are where and how it calls; tests point them at a Fake.
	BaseURL string
	HTTP    *http.Client

	st   *settings.Store
	mu   sync.Mutex // one send at a time, in order
	last time.Time
}

// NewChannel returns the channel; baseURL "" is the real Bot API.
func NewChannel(st *settings.Store, httpc *http.Client, baseURL string) *Channel {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Channel{Now: time.Now, Sleep: sleep, BaseURL: baseURL, HTTP: httpc, st: st}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Channel) Name() string { return "telegram" }

// Client returns a client for token on this channel's address.
func (c *Channel) Client(token string) *Client {
	return &Client{BaseURL: c.BaseURL, Token: token, HTTP: c.HTTP}
}

// Configured is true with a bot token.
func (c *Channel) Configured(ctx context.Context) (bool, error) {
	token, err := c.st.Get(ctx, "telegram.bot_token")
	return token != "", err
}

// Address is the member's own chat.
func (c *Channel) Address(m auth.Member) string { return m.TelegramChatID }

// Send delivers m to chat, at most one message a second whatever the chat.
func (c *Channel) Send(ctx context.Context, chat string, m notify.Message) error {
	token, err := c.st.Get(ctx, "telegram.bot_token")
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("telegram is not configured")
	}
	if chat == "" {
		return errors.New("no chat")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait := c.last.Add(minGap).Sub(c.Now()); wait > 0 {
		if err := c.Sleep(ctx, wait); err != nil {
			return err
		}
	}
	var button *URLButton
	if m.URL != "" {
		button = &URLButton{Text: m.Button, URL: m.URL}
	}
	err = c.Client(token).SendMessage(ctx, chat, Format(m), button)
	c.last = c.Now()
	var api *APIError
	if errors.As(err, &api) && api.Code == http.StatusTooManyRequests {
		return &notify.RateLimitedError{RetryAfter: max(api.RetryAfter, time.Second)}
	}
	return err
}

// Format renders the message as Telegram HTML: "<emoji> <b>title</b>\nbody".
// Every value is escaped; nothing else is allowed into the markup.
func Format(m notify.Message) string {
	var b strings.Builder
	if m.Emoji != "" {
		b.WriteString(html.EscapeString(m.Emoji))
		b.WriteByte(' ')
	}
	b.WriteString("<b>" + html.EscapeString(m.Title) + "</b>")
	if m.Body != "" {
		b.WriteString("\n" + html.EscapeString(m.Body))
	}
	return b.String()
}
