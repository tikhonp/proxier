package telegram_test

import (
	"testing"

	"github.com/tikhonp/proxier/internal/platform/notify"
	"github.com/tikhonp/proxier/internal/platform/notify/telegram"
)

func TestFormatEscapesHTML(t *testing.T) {
	got := telegram.Format(notify.Message{
		Emoji: "🔴", Title: `<script>alert("x")</script> & co`, Body: `<b>bold</b> it's`,
	})
	want := "🔴 <b>&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; co</b>\n&lt;b&gt;bold&lt;/b&gt; it&#39;s"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if got := telegram.Format(notify.Message{Title: "t"}); got != "<b>t</b>" {
		t.Fatalf("%q", got)
	}
}

func TestChatIDValidation(t *testing.T) {
	for _, bad := range []string{"abc", "12 3", "@", "1.5"} {
		found := false
		for _, f := range telegram.Section.Fields {
			if f.Key == "telegram.chat_id" && f.Validate(bad) != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("%q accepted as a chat", bad)
		}
	}
	for _, ok := range []string{"", "5", "-1001234", "@alerts"} {
		for _, f := range telegram.Section.Fields {
			if f.Key == "telegram.chat_id" && f.Validate(ok) != nil {
				t.Errorf("%q refused", ok)
			}
		}
	}
}
