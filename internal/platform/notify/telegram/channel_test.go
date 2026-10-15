package telegram_test

import (
	"strings"
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
	for _, bad := range []string{"abc", "12 3", "@", "1.5", "@" + strings.Repeat("a", 64)} {
		if telegram.ValidChatID(bad) == nil {
			t.Errorf("%q accepted as a chat", bad)
		}
	}
	for _, ok := range []string{"", "5", "-1001234", "@alerts"} {
		if err := telegram.ValidChatID(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}
