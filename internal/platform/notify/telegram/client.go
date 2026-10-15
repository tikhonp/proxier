// Package telegram is the Telegram notification channel
// (docs/integrations/telegram.md): a small Bot API client for the methods
// Proxier needs, and the channel that sends notifications with it.
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

// Button is an inline keyboard button: it opens URL, or sends Data back
// as a callback query.
type Button struct{ Text, URL, Data string }

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
	return c.callWithin(ctx, requestTimeout, method, params, result)
}

func (c *Client) callWithin(ctx context.Context, timeout time.Duration, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

// Incoming is one update the bot acts on: a message, or a tap on one of its
// buttons (Callback set).
type Incoming struct {
	ID   int64
	Chat Chat
	// From is who wrote or tapped; Lang is their Telegram language ("ru").
	From int64
	Lang string
	// Text is a message's text.
	Text string
	// Callback is a tap's query ID, Data the button's data and MessageID the
	// message the button is on.
	Callback, Data string
	MessageID      int64
}

// Poll returns the updates after offset, waiting up to wait for the first
// one. Asking with an offset confirms every update before it: Telegram
// drops them. It works only while the bot has no webhook, and Proxier never
// sets one.
func (c *Client) Poll(ctx context.Context, offset int64, wait time.Duration) ([]Incoming, error) {
	type from struct {
		ID   int64  `json:"id"`
		Lang string `json:"language_code"`
	}
	type message struct {
		ID   int64   `json:"message_id"`
		Chat rawChat `json:"chat"`
		From *from   `json:"from"`
		Text string  `json:"text"`
	}
	var raw []struct {
		ID          int64    `json:"update_id"`
		Message     *message `json:"message"`
		ChannelPost *message `json:"channel_post"`
		MyChat      *struct {
			Chat rawChat `json:"chat"`
			From from    `json:"from"`
		} `json:"my_chat_member"`
		Callback *struct {
			ID      string   `json:"id"`
			From    from     `json:"from"`
			Data    string   `json:"data"`
			Message *message `json:"message"`
		} `json:"callback_query"`
	}
	err := c.callWithin(ctx, wait+requestTimeout, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(wait / time.Second),
		"allowed_updates": []string{"message", "channel_post", "my_chat_member", "callback_query"},
	}, &raw)
	if err != nil {
		return nil, err
	}
	out := make([]Incoming, 0, len(raw))
	for _, u := range raw {
		in := Incoming{ID: u.ID}
		switch {
		case u.Message != nil, u.ChannelPost != nil:
			m := u.Message
			if m == nil {
				m = u.ChannelPost
			}
			in.Chat, in.Text = m.Chat.chat(), m.Text
			if m.From != nil {
				in.From, in.Lang = m.From.ID, m.From.Lang
			}
		case u.MyChat != nil:
			in.Chat, in.From, in.Lang = u.MyChat.Chat.chat(), u.MyChat.From.ID, u.MyChat.From.Lang
		case u.Callback != nil && u.Callback.Message != nil:
			in.Chat, in.From, in.Lang = u.Callback.Message.Chat.chat(), u.Callback.From.ID, u.Callback.From.Lang
			in.Callback, in.Data, in.MessageID = u.Callback.ID, u.Callback.Data, u.Callback.Message.ID
		}
		// An update of a kind not asked for still moves the offset.
		out = append(out, in)
	}
	return out, nil
}

func keyboard(rows [][]Button) map[string]any {
	kb := make([][]map[string]string, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]string, 0, len(row))
		for _, b := range row {
			if b.URL != "" {
				r = append(r, map[string]string{"text": b.Text, "url": b.URL})
			} else {
				r = append(r, map[string]string{"text": b.Text, "callback_data": b.Data})
			}
		}
		kb = append(kb, r)
	}
	return map[string]any{"inline_keyboard": kb}
}

// SendMessage sends HTML text to chatID (an integer, or @channel) with
// previews off and an optional inline keyboard.
func (c *Client) SendMessage(ctx context.Context, chatID, html string, buttons [][]Button) error {
	p := map[string]any{
		"chat_id": chatID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if len(buttons) > 0 {
		p["reply_markup"] = keyboard(buttons)
	}
	return c.call(ctx, "sendMessage", p, nil)
}

// EditMessage replaces the text and the keyboard of one of the bot's
// messages; no buttons takes the keyboard away.
func (c *Client) EditMessage(ctx context.Context, chatID string, messageID int64, html string, buttons [][]Button) error {
	return c.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true}, "reply_markup": keyboard(buttons),
	}, nil)
}

// AnswerCallback ends a button's spinner, showing text as a toast when set.
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// BotCommand is an entry of the bot's command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands sets the command menu shown to users whose Telegram is in
// lang ("" for every other language).
func (c *Client) SetCommands(ctx context.Context, lang string, cmds []BotCommand) error {
	p := map[string]any{"commands": cmds}
	if lang != "" {
		p["language_code"] = lang
	}
	return c.call(ctx, "setMyCommands", p, nil)
}
