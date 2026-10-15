package telegram

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Sent is a sendMessage or editMessageText call the Fake accepted.
type Sent struct {
	ChatID                string
	Text                  string
	ParseMode             string
	PreviewsOff           bool
	ButtonText, ButtonURL string // the first button's
	Keyboard              [][]Button
	// MessageID is the new message's, or the edited one's when Edit.
	MessageID int64
	Edit      bool
	At        time.Time // the Fake's Now when it arrived
}

// Failure scripts an answer instead of success.
type Failure struct {
	Code        int
	Description string
	RetryAfter  int // seconds, with 429
}

// Fake is a Bot API on httptest for tests: scripted getMe/getUpdates answers
// (an offset confirms the updates before it, as Telegram does), recorded
// sendMessage, editMessageText, answerCallbackQuery and setMyCommands
// calls, injectable 429s and outages. getUpdates never waits.
type Fake struct {
	// Now stamps recorded calls; tests set their fake clock.
	Now func() time.Time
	// Bot is what getMe answers.
	Bot User

	token string
	srv   *httptest.Server

	mu       sync.Mutex
	updates  []update
	lastID   int64
	messages int64
	sent     []Sent
	answers  []string
	commands map[string][]BotCommand
	failures []Failure
	blocked  map[string]bool
	down     bool
	calls    []string
}

// NewFake starts a fake bot with the given token; the server closes with the
// test.
func NewFake(t testing.TB, token string) *Fake {
	t.Helper()
	f := &Fake{Now: time.Now, Bot: User{ID: 4242, Username: "proxier_test_bot", FirstName: "Proxier"}, token: token,
		blocked: map[string]bool{}, commands: map[string][]BotCommand{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base address to give a Client or Channel.
func (f *Fake) URL() string { return f.srv.URL }

type update struct {
	id  int64
	raw string
}

// AddUpdate queues a raw update, e.g.
// {"update_id":1,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon"}}}.
func (f *Fake) AddUpdate(raw string) {
	var u struct {
		ID int64 `json:"update_id"`
	}
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		panic(err)
	}
	f.mu.Lock()
	f.updates = append(f.updates, update{u.ID, raw})
	f.lastID = max(f.lastID, u.ID)
	f.mu.Unlock()
}

func (f *Fake) nextUpdate(body map[string]any) {
	f.mu.Lock()
	f.lastID++
	body["update_id"] = f.lastID
	raw, _ := json.Marshal(body)
	f.updates = append(f.updates, update{f.lastID, string(raw)})
	f.mu.Unlock()
}

// Message queues text written in the private chat of the Telegram user
// chatID, whose Telegram is in lang.
func (f *Fake) Message(chatID int64, lang, text string) {
	f.nextUpdate(map[string]any{"message": map[string]any{
		"message_id": 1, "text": text,
		"chat": map[string]any{"id": chatID, "type": "private", "first_name": "User"},
		"from": map[string]any{"id": chatID, "is_bot": false, "first_name": "User", "language_code": lang},
	}})
}

// GroupMessage queues text written by the user from in the group chatID.
func (f *Fake) GroupMessage(chatID, from int64, text string) {
	f.nextUpdate(map[string]any{"message": map[string]any{
		"message_id": 1, "text": text,
		"chat": map[string]any{"id": chatID, "type": "group", "title": "Group"},
		"from": map[string]any{"id": from, "is_bot": false, "first_name": "User"},
	}})
}

// Tap queues a tap on the button with data on message messageID of the
// private chat chatID.
func (f *Fake) Tap(chatID, messageID int64, data string) {
	f.nextUpdate(map[string]any{"callback_query": map[string]any{
		"id": fmt.Sprintf("cb-%d-%s", messageID, data), "data": data,
		"from":    map[string]any{"id": chatID, "is_bot": false, "first_name": "User"},
		"message": map[string]any{"message_id": messageID, "chat": map[string]any{"id": chatID, "type": "private"}},
	}})
}

// Pending is how many updates are not confirmed yet.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

// Answers returns the texts of answered callback queries, in order.
func (f *Fake) Answers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.answers...)
}

// Commands returns the command menu set for lang ("" for the default).
func (f *Fake) Commands(lang string) []BotCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commands[lang]
}

// FailNext makes the next sendMessage calls fail, one failure each.
func (f *Fake) FailNext(fs ...Failure) {
	f.mu.Lock()
	f.failures = append(f.failures, fs...)
	f.mu.Unlock()
}

// Block makes sendMessage to chatID answer "bot was blocked by the user".
func (f *Fake) Block(chatID string) {
	f.mu.Lock()
	f.blocked[chatID] = true
	f.mu.Unlock()
}

// Unblock lets sendMessage to chatID through again.
func (f *Fake) Unblock(chatID string) {
	f.mu.Lock()
	delete(f.blocked, chatID)
	f.mu.Unlock()
}

// SetDown makes every call answer 502, an outage.
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// Sent returns the accepted messages in arrival order.
func (f *Fake) Sent() []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Sent(nil), f.sent...)
}

// Calls returns the method names called, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *Fake) answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *Fake) refuse(w http.ResponseWriter, code int, desc string, retryAfter int) {
	b := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retryAfter > 0 {
		b["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	f.answer(w, code, b)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	token, method, _ := strings.Cut(rest, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	switch {
	case !ok || token != f.token:
		f.refuse(w, http.StatusUnauthorized, "Unauthorized", 0)
		return
	case f.down:
		http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
		return
	}
	switch method {
	case "getMe":
		f.answer(w, 200, map[string]any{"ok": true, "result": map[string]any{
			"id": f.Bot.ID, "is_bot": true, "username": f.Bot.Username, "first_name": f.Bot.FirstName}})
	case "getUpdates":
		var p struct {
			Offset int64 `json:"offset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		var left []update
		var raws []string
		for _, u := range f.updates {
			if u.id >= p.Offset {
				left = append(left, u)
				raws = append(raws, u.raw)
			}
		}
		f.updates = left
		f.answer(w, 200, map[string]any{"ok": true, "result": json.RawMessage("[" + strings.Join(raws, ",") + "]")})
	case "answerCallbackQuery":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.answers = append(f.answers, p.Text)
		f.answer(w, 200, map[string]any{"ok": true, "result": true})
	case "setMyCommands":
		var p struct {
			Commands []BotCommand `json:"commands"`
			Lang     string       `json:"language_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.commands[p.Lang] = p.Commands
		f.answer(w, 200, map[string]any{"ok": true, "result": true})
	case "sendMessage", "editMessageText":
		var p struct {
			ChatID      string `json:"chat_id"`
			MessageID   int64  `json:"message_id"`
			Text        string `json:"text"`
			ParseMode   string `json:"parse_mode"`
			LinkPreview struct {
				Off bool `json:"is_disabled"`
			} `json:"link_preview_options"`
			Markup struct {
				Keyboard [][]struct {
					Text string `json:"text"`
					URL  string `json:"url"`
					Data string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&p); err != nil {
			f.refuse(w, http.StatusBadRequest, "Bad Request: can't parse JSON", 0)
			return
		}
		if len(f.failures) > 0 {
			fl := f.failures[0]
			f.failures = f.failures[1:]
			f.refuse(w, fl.Code, fl.Description, fl.RetryAfter)
			return
		}
		if f.blocked[p.ChatID] {
			f.refuse(w, http.StatusForbidden, "Forbidden: bot was blocked by the user", 0)
			return
		}
		s := Sent{ChatID: p.ChatID, Text: p.Text, ParseMode: p.ParseMode, PreviewsOff: p.LinkPreview.Off, At: f.Now()}
		for _, row := range p.Markup.Keyboard {
			var r []Button
			for _, b := range row {
				r = append(r, Button{Text: b.Text, URL: b.URL, Data: b.Data})
			}
			s.Keyboard = append(s.Keyboard, r)
		}
		if len(s.Keyboard) > 0 && len(s.Keyboard[0]) > 0 {
			s.ButtonText, s.ButtonURL = s.Keyboard[0][0].Text, s.Keyboard[0][0].URL
		}
		if method == "editMessageText" {
			s.Edit, s.MessageID = true, p.MessageID
		} else {
			f.messages++
			s.MessageID = f.messages
		}
		f.sent = append(f.sent, s)
		f.answer(w, 200, map[string]any{"ok": true, "result": map[string]any{"message_id": s.MessageID}})
	default:
		f.refuse(w, http.StatusNotFound, fmt.Sprintf("Not Found: method %s", method), 0)
	}
}
