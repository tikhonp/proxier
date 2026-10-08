package output_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestStubTexts(t *testing.T) {
	// a date-only expiry of 1 Dec 2026 in Moscow: expired from the next midnight
	expires := time.Date(2026, 12, 1, 21, 0, 0, 0, time.UTC)
	en, ru := loc(i18n.EN), loc(i18n.RU)
	for _, c := range []struct {
		l       *i18n.Localizer
		outcome string
		contact string
		want    string
	}{
		{en, output.StubDisabled, "@tikhonp", "⛔ Link disabled · contact @tikhonp"},
		{en, output.StubDisabled, "", "⛔ Link disabled"},
		{ru, output.StubDisabled, "@tikhonp", "⛔ Ссылка отключена · пишите @tikhonp"},
		{ru, output.StubDisabled, "", "⛔ Ссылка отключена"},
		{en, output.StubExpired, "@tikhonp", "⏳ Expired on 1 Dec 2026 · contact @tikhonp"},
		{en, output.StubExpired, "", "⏳ Expired on 1 Dec 2026"},
		{ru, output.StubExpired, "@tikhonp", "⏳ Срок истёк 1 дек 2026 · пишите @tikhonp"},
		{ru, output.StubExpired, "", "⏳ Срок истёк 1 дек 2026"},
		{en, output.StubDeleted, "@tikhonp", "⛔ Link removed"},
		{ru, output.StubDeleted, "", "⛔ Ссылка удалена"},
		{en, output.StubEmpty, "@tikhonp", "⚠️ No servers yet"},
		{ru, output.StubEmpty, "", "⚠️ Серверов пока нет"},
	} {
		if got := output.StubText(c.l, c.outcome, expires, c.contact); got != c.want {
			t.Errorf("%s %s %q: %q, want %q", c.l.Lang, c.outcome, c.contact, got, c.want)
		}
	}
}

func TestStubURIMatchesTheDoc(t *testing.T) {
	const want = "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?encryption=none&type=tcp&security=none#%E2%9B%94%20Link%20disabled%20%C2%B7%20contact%20%40tikhonp"
	got := output.StubURI(output.StubText(loc(i18n.EN), output.StubDisabled, time.Time{}, "@tikhonp"))
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	if output.Escape("aZ09-._~ /") != "aZ09-._~%20%2F" {
		t.Errorf("escape: %s", output.Escape("aZ09-._~ /"))
	}
}
