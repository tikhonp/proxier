package telegram_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
)

var bg = context.Background()

const token = "123456:SECRET-token_value"

func TestGetUpdatesFindsChats(t *testing.T) {
	f := telegram.NewFake(t, token)
	f.AddUpdate(`{"update_id":1,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon","last_name":"P"}}}`)
	f.AddUpdate(`{"update_id":2,"message":{"chat":{"id":-100,"type":"group","title":"Family"}}}`)
	f.AddUpdate(`{"update_id":3,"channel_post":{"chat":{"id":-1007,"type":"channel","title":"Alerts","username":"alerts"}}}`)
	f.AddUpdate(`{"update_id":4,"my_chat_member":{"chat":{"id":-200,"type":"supergroup","title":"Ops"}}}`)
	f.AddUpdate(`{"update_id":5,"message":{"chat":{"id":5,"type":"private","first_name":"Tikhon","last_name":"P"}}}`)
	f.AddUpdate(`{"update_id":6,"edited_message":{"chat":{"id":9,"type":"private"}}}`)
	c := &telegram.Client{BaseURL: f.URL(), Token: token}

	got, err := c.GetUpdates(bg, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Distinct, newest first: chat 5 is represented by its newest update.
	want := []struct {
		id    int64
		typ   string
		title string
		upd   int64
	}{
		{5, "private", "Tikhon P", 5},
		{-200, "supergroup", "Ops", 4},
		{-1007, "channel", "Alerts", 3},
		{-100, "group", "Family", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.Chat.ID != w.id || g.Chat.Type != w.typ || g.Chat.Title != w.title || g.ID != w.upd {
			t.Errorf("chat %d: %+v, want %+v", i, g, w)
		}
	}
	if got[2].Chat.Username != "alerts" {
		t.Errorf("username %q", got[2].Chat.Username)
	}
}

func TestGetMeAndSend(t *testing.T) {
	f := telegram.NewFake(t, token)
	c := &telegram.Client{BaseURL: f.URL(), Token: token}
	me, err := c.GetMe(bg)
	if err != nil || me.Username != "proxier_test_bot" {
		t.Fatalf("%+v %v", me, err)
	}
	if err := c.SendMessage(bg, "5", "<b>hi</b>", &telegram.URLButton{Text: "Open", URL: "https://x.test/a"}); err != nil {
		t.Fatal(err)
	}
	s := f.Sent()
	if len(s) != 1 || s[0].Text != "<b>hi</b>" || s[0].ButtonURL != "https://x.test/a" {
		t.Fatalf("%+v", s)
	}
}

func TestRateLimitIsAnAPIError(t *testing.T) {
	f := telegram.NewFake(t, token)
	f.FailNext(telegram.Failure{Code: 429, Description: "Too Many Requests: retry after 20", RetryAfter: 20})
	c := &telegram.Client{BaseURL: f.URL(), Token: token}
	err := c.SendMessage(bg, "5", "x", nil)
	var api *telegram.APIError
	if !errors.As(err, &api) || api.Code != 429 || api.RetryAfter != 20*time.Second {
		t.Fatalf("%v", err)
	}
}

func TestErrorsNeverContainToken(t *testing.T) {
	good := telegram.NewFake(t, "999:other")
	down := telegram.NewFake(t, token)
	down.SetDown(true)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	// A server that echoes the request path, token included, into its answer.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"error_code":500,"description":"failed at `+r.URL.Path+`"}`, 500)
	}))
	t.Cleanup(echo.Close)

	cases := map[string]string{
		"bad DNS":            "http://no-such-host.invalid",
		"refused connection": closedURL,
		"401":                good.URL(),
		"500 page":           down.URL(),
		"500 with the token": echo.URL,
	}
	for name, base := range cases {
		t.Run(name, func(t *testing.T) {
			c := &telegram.Client{BaseURL: base, Token: token, HTTP: &http.Client{Timeout: 3 * time.Second}}
			var errs []error
			_, e1 := c.GetMe(bg)
			_, e2 := c.GetUpdates(bg, 10)
			errs = append(errs, e1, e2, c.SendMessage(bg, "5", "x", nil))
			for _, err := range errs {
				if err == nil {
					t.Fatal("expected an error")
				}
				if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("the token is in %q", err)
				}
				var api *telegram.APIError
				if errors.As(err, &api) && strings.Contains(api.Description, token) {
					t.Fatalf("the token is in the description %q", api.Description)
				}
			}
		})
	}
}
