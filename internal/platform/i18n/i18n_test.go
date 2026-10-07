package i18n

import (
	"testing"
	"time"
)

func TestCatalogRefusesIncompleteMessages(t *testing.T) {
	cases := map[string]Messages{
		"missing RU":          {"a": {EN: "x"}},
		"placeholder differs": {"a": {EN: "Hi {name}", RU: "Привет"}},
	}
	for name, m := range cases {
		if err := NewCatalog().Add("m", m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := NewCatalog()
	if err := c.Add("m", Messages{"a": {EN: "Hi {name} {{x}}", RU: "Привет {name}"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Add("other", Messages{"a": {EN: "x", RU: "y"}}); err == nil {
		t.Error("a key defined twice was accepted")
	}
	if !c.Has("a") || c.Has("b") {
		t.Error("Has is wrong")
	}
}

func TestRussianPlurals(t *testing.T) {
	c := NewCatalog()
	_ = c.Add("m", Messages{"s": {EN: "{n} session|{n} sessions", RU: "{n} сессия|{n} сессии|{n} сессий"}})
	l := c.Localizer(RU, nil)
	want := map[int64]string{1: "1 сессия", 2: "2 сессии", 5: "5 сессий", 11: "11 сессий", 21: "21 сессия", 22: "22 сессии", 25: "25 сессий"}
	for n, w := range want {
		if got := l.N("s", n); got != w {
			t.Errorf("N(%d) = %q, want %q", n, got, w)
		}
	}
	en := c.Localizer(EN, nil)
	if en.N("s", 1) != "1 session" || en.N("s", 2) != "2 sessions" {
		t.Error("English plurals")
	}
}

func TestNumberFormat(t *testing.T) {
	c := NewCatalog()
	if got := c.Localizer(EN, nil).Number(1284); got != "1,284" {
		t.Errorf("EN %q", got)
	}
	if got := c.Localizer(RU, nil).Number(1284); got != "1 284" {
		t.Errorf("RU %q", got)
	}
	if got := c.Localizer(EN, nil).Number(-1234567); got != "-1,234,567" {
		t.Errorf("negative %q", got)
	}
}

func TestMatchAcceptLanguage(t *testing.T) {
	cases := []struct {
		in   string
		want Lang
		ok   bool
	}{
		{"ru-RU,ru;q=0.9", RU, true},
		{"en-US,en;q=0.8", EN, true},
		{"de", "", false},
		{"", "", false},
		{"de,ru;q=0.5", RU, true},
	}
	for _, c := range cases {
		got, ok := Match(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Match(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPlaceholdersAndMissingKeys(t *testing.T) {
	c := NewCatalog()
	_ = c.Add("m", Messages{"a": {EN: "Hi {name}, {{literal}}", RU: "Привет {name}, {{literal}}"}})
	l := c.Localizer(EN, time.UTC)
	if got := l.T("a", Args{"name": "Ann"}); got != "Hi Ann, {literal}" {
		t.Errorf("got %q", got)
	}
	if l.T("nope") != "nope" {
		t.Error("a missing key should render as itself")
	}
	if From(t.Context()).T("x") != "x" {
		t.Error("From without a localizer must not be nil")
	}
}

func TestClockDayAndDuration(t *testing.T) {
	tz, _ := time.LoadLocation("Europe/Moscow")
	at := time.Date(2026, 10, 7, 12, 14, 40, 0, time.UTC)
	en := NewCatalog().Localizer(EN, tz)
	if en.Clock(at) != "15:14:40" || en.Day(at) != "2026-10-07" {
		t.Fatalf("%s %s", en.Clock(at), en.Day(at))
	}
	ru := NewCatalog().Localizer(RU, tz)
	for _, c := range []struct {
		d        time.Duration
		en, want string
	}{
		{300 * time.Millisecond, "< 1 s", "< 1 с"},
		{21 * time.Second, "21 s", "21 с"},
		{112 * time.Second, "1 min 52 s", "1 мин 52 с"},
		{4 * time.Minute, "4 min", "4 мин"},
		{125 * time.Minute, "2 h 5 min", "2 ч 5 мин"},
	} {
		if got := en.Duration(c.d); got != c.en {
			t.Errorf("%v: %q, want %q", c.d, got, c.en)
		}
		if got := ru.Duration(c.d); got != c.want {
			t.Errorf("%v: %q, want %q", c.d, got, c.want)
		}
	}
}
