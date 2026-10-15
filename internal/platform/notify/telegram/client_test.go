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

func TestPollReadsUpdatesAndConfirmsThem(t *testing.T) {
	f := telegram.NewFake(t, token)
	f.AddUpdate(`{"update_id":1,"message":{"message_id":3,"text":"/link","chat":{"id":5,"type":"private","first_name":"Tikhon","last_name":"P"},"from":{"id":5,"language_code":"ru"}}}`)
	f.AddUpdate(`{"update_id":2,"channel_post":{"chat":{"id":-1007,"type":"channel","title":"Alerts","username":"alerts"}}}`)
	f.AddUpdate(`{"update_id":3,"my_chat_member":{"chat":{"id":-200,"type":"supergroup","title":"Ops"},"from":{"id":5}}}`)
	f.AddUpdate(`{"update_id":4,"callback_query":{"id":"q1","data":"c1.0","from":{"id":5},"message":{"message_id":9,"chat":{"id":5,"type":"private"}}}}`)
	f.AddUpdate(`{"update_id":5,"edited_message":{"chat":{"id":9,"type":"private"}}}`)
	c := &telegram.Client{BaseURL: f.URL(), Token: token}

	got, err := c.Poll(bg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []telegram.Incoming{
		{ID: 1, Chat: telegram.Chat{ID: 5, Type: "private", Title: "Tikhon P"}, From: 5, Lang: "ru", Text: "/link"},
		{ID: 2, Chat: telegram.Chat{ID: -1007, Type: "channel", Title: "Alerts", Username: "alerts"}},
		{ID: 3, Chat: telegram.Chat{ID: -200, Type: "supergroup", Title: "Ops"}, From: 5},
		{ID: 4, Chat: telegram.Chat{ID: 5, Type: "private"}, From: 5, Callback: "q1", Data: "c1.0", MessageID: 9},
		{ID: 5}, // a kind not asked for still moves the offset
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("update %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	// Asking after the last one confirms them all.
	if got, err := c.Poll(bg, 6, 0); err != nil || len(got) != 0 || f.Pending() != 0 {
		t.Fatalf("%+v %v, %d pending", got, err, f.Pending())
	}
}

func TestGetMeAndSend(t *testing.T) {
	f := telegram.NewFake(t, token)
	c := &telegram.Client{BaseURL: f.URL(), Token: token}
	me, err := c.GetMe(bg)
	if err != nil || me.Username != "proxier_test_bot" {
		t.Fatalf("%+v %v", me, err)
	}
	if err := c.SendMessage(bg, "5", "<b>hi</b>", [][]telegram.Button{{{Text: "Open", URL: "https://x.test/a"}}}); err != nil {
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
			_, e2 := c.Poll(bg, 0, 0)
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
