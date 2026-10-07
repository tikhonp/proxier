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

	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Section is Settings → Integrations → Telegram. The page does not use the
// generic form.
var Section = settings.Section{
	Name: "telegram", Module: "platform",
	Fields: []settings.Field{
		{Key: "telegram.bot_token", Kind: settings.Secret, MaxLen: 200},
		{Key: "telegram.bot_username", Kind: settings.String, MaxLen: 64},
		// An integer, or @channel.
		{Key: "telegram.chat_id", Kind: settings.String, MaxLen: 64, Validate: validChatID},
		{Key: "telegram.chat_title", Kind: settings.String, MaxLen: 128},
	},
}

func validChatID(s string) error {
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

// Channel sends notifications through the bot in settings. It reads the token
// and the chat on every send, so a change applies at once.
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

// Configured is true with a token and a chat.
func (c *Channel) Configured(ctx context.Context) (bool, error) {
	token, chat, err := c.credentials(ctx)
	return token != "" && chat != "", err
}

func (c *Channel) credentials(ctx context.Context) (token, chat string, err error) {
	if token, err = c.st.Get(ctx, "telegram.bot_token"); err != nil {
		return "", "", err
	}
	chat, err = c.st.Get(ctx, "telegram.chat_id")
	return token, chat, err
}

// Send delivers m, at most once a second.
func (c *Channel) Send(ctx context.Context, m notify.Message) error {
	token, chat, err := c.credentials(ctx)
	if err != nil {
		return err
	}
	if token == "" || chat == "" {
		return errors.New("telegram is not configured")
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
