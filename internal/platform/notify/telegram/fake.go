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

// Sent is a sendMessage call the Fake accepted.
type Sent struct {
	ChatID                string
	Text                  string
	ParseMode             string
	PreviewsOff           bool
	ButtonText, ButtonURL string
	At                    time.Time // the Fake's Now when it arrived
}

// Failure scripts an answer instead of success.
type Failure struct {
	Code        int
	Description string
	RetryAfter  int // seconds, with 429
}

// Fake is a Bot API on httptest for tests: scripted getMe/getUpdates answers,
// recorded sendMessage calls, injectable 429s and outages.
type Fake struct {
	// Now stamps recorded calls; tests set their fake clock.
	Now func() time.Time
	// Bot is what getMe answers.
	Bot User

	token string
	srv   *httptest.Server

	mu       sync.Mutex
	updates  []string
	sent     []Sent
	failures []Failure
	blocked  map[string]bool
	down     bool
	calls    []string
}

// NewFake starts a fake bot with the given token; the server closes with the
// test.
func NewFake(t testing.TB, token string) *Fake {
	t.Helper()
	f := &Fake{Now: time.Now, Bot: User{ID: 4242, Username: "proxier_test_bot", FirstName: "Proxier"}, token: token, blocked: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base address to give a Client or Channel.
func (f *Fake) URL() string { return f.srv.URL }

// AddUpdate queues a raw update, e.g.
// {"update_id":1,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon"}}}.
func (f *Fake) AddUpdate(raw string) {
	f.mu.Lock()
	f.updates = append(f.updates, raw)
	f.mu.Unlock()
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
		raw := "[" + strings.Join(f.updates, ",") + "]"
		f.answer(w, 200, map[string]any{"ok": true, "result": json.RawMessage(raw)})
	case "sendMessage":
		var p struct {
			ChatID      string `json:"chat_id"`
			Text        string `json:"text"`
			ParseMode   string `json:"parse_mode"`
			LinkPreview struct {
				Off bool `json:"is_disabled"`
			} `json:"link_preview_options"`
			Markup struct {
				Keyboard [][]struct {
					Text string `json:"text"`
					URL  string `json:"url"`
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
		if len(p.Markup.Keyboard) > 0 && len(p.Markup.Keyboard[0]) > 0 {
			s.ButtonText, s.ButtonURL = p.Markup.Keyboard[0][0].Text, p.Markup.Keyboard[0][0].URL
		}
		f.sent = append(f.sent, s)
		f.answer(w, 200, map[string]any{"ok": true, "result": map[string]any{"message_id": len(f.sent)}})
	default:
		f.refuse(w, http.StatusNotFound, fmt.Sprintf("Not Found: method %s", method), 0)
	}
}
