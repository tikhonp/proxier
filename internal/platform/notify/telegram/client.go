// Package telegram is the Telegram notification channel
// (docs/integrations/telegram.md): a small Bot API client for the three
// methods Proxier needs, and the channel that sends notifications with it.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Bot API.
const DefaultBaseURL = "https://api.telegram.org"

// requestTimeout bounds one call.
const requestTimeout = 10 * time.Second

// Client calls the Bot API. The token is in the URL path, and *url.Error prints
// the URL, so no error leaves this package with the request's URL or the token
// in it.
type Client struct {
	BaseURL string // DefaultBaseURL
	Token   string
	HTTP    *http.Client
}

type User struct {
	ID                  int64
	Username, FirstName string
}

type Chat struct {
	ID    int64
	Type  string // private, group, supergroup, channel
	Title string // falls back to the person's name, then @username
	// Username is the @name of a public chat, without the @.
	Username string
}

// Update is the newest update of one chat.
type Update struct {
	ID   int64
	Chat Chat
}

// APIError is a Bot API refusal. Description is Telegram's, never the token.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration // set on 429
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram: %d %s (retry after %s)", e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

type URLButton struct{ Text, URL string }

type response struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/bot"+c.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.scrub(errors.New("telegram: bad request"))
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL, and the token with it, stays out
		}
		return c.scrub(fmt.Errorf("telegram: %s: %w", method, err))
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return c.scrub(fmt.Errorf("telegram: %s: %w", method, err))
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		// Not the Bot API's answer: a proxy or an outage page.
		return &APIError{Code: res.StatusCode, Description: http.StatusText(res.StatusCode)}
	}
	if !r.OK {
		code := cmpOr(r.ErrorCode, res.StatusCode)
		e := &APIError{Code: code, Description: c.scrubText(r.Description)}
		if r.Parameters.RetryAfter > 0 {
			e.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
		}
		return e
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("telegram: %s: bad answer: %w", method, err)
	}
	return nil
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func (c *Client) scrubText(s string) string {
	if c.Token != "" {
		s = strings.ReplaceAll(s, c.Token, "•••")
	}
	return s
}

// scrub is the last line of defence: no wrapped error text holds the token.
func (c *Client) scrub(err error) error {
	if c.Token == "" || !strings.Contains(err.Error(), c.Token) {
		return err
	}
	return errors.New(c.scrubText(err.Error()))
}

// GetMe asks who the token belongs to.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var r struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	}
	if err := c.call(ctx, "getMe", struct{}{}, &r); err != nil {
		return User{}, err
	}
	return User{ID: r.ID, Username: r.Username, FirstName: r.FirstName}, nil
}

type rawChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (r rawChat) chat() Chat {
	title := r.Title
	if title == "" {
		title = strings.TrimSpace(r.FirstName + " " + r.LastName)
	}
	if title == "" && r.Username != "" {
		title = "@" + r.Username
	}
	return Chat{ID: r.ID, Type: r.Type, Title: title, Username: r.Username}
}

// GetUpdates lists the chats the bot has heard from, newest first, one entry
// per chat. Updates are not confirmed, so asking twice gives the same answer.
// It works only while the bot has no webhook, and Proxier never sets one.
func (c *Client) GetUpdates(ctx context.Context, limit int) ([]Update, error) {
	var raw []struct {
		ID      int64 `json:"update_id"`
		Message *struct {
			Chat rawChat `json:"chat"`
		} `json:"message"`
		ChannelPost *struct {
			Chat rawChat `json:"chat"`
		} `json:"channel_post"`
		MyChat *struct {
			Chat rawChat `json:"chat"`
		} `json:"my_chat_member"`
	}
	err := c.call(ctx, "getUpdates", map[string]any{
		"limit": limit, "timeout": 0,
		"allowed_updates": []string{"message", "channel_post", "my_chat_member"},
	}, &raw)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []Update
	for i := len(raw) - 1; i >= 0; i-- {
		u := raw[i]
		var chat *rawChat
		switch {
		case u.Message != nil:
			chat = &u.Message.Chat
		case u.ChannelPost != nil:
			chat = &u.ChannelPost.Chat
		case u.MyChat != nil:
			chat = &u.MyChat.Chat
		default:
			continue
		}
		if seen[chat.ID] {
			continue
		}
		seen[chat.ID] = true
		out = append(out, Update{ID: u.ID, Chat: chat.chat()})
	}
	return out, nil
}

// SendMessage sends HTML text to chatID (an integer, or @channel) with
// previews off and an optional URL button.
func (c *Client) SendMessage(ctx context.Context, chatID, html string, button *URLButton) error {
	p := map[string]any{
		"chat_id": chatID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if button != nil {
		p["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{{{"text": button.Text, "url": button.URL}}}}
	}
	return c.call(ctx, "sendMessage", p, nil)
}
