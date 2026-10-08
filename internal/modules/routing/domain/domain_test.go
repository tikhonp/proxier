package domain

import (
	"errors"
	"slices"
	"testing"
)

func TestNormalise(t *testing.T) {
	for _, c := range []struct {
		in    string
		want  string
		exact bool
		err   error
	}{
		{"https://www.Example.com/path", "www.example.com", false, nil},
		{"*.cdn.example.net", "cdn.example.net", false, nil},
		{"full:api.example.org", "api.example.org", true, nil},
		{"domain:yastatic.net", "yastatic.net", false, nil},
		{"  Rutracker.ORG  # torrents", "rutracker.org", false, nil},
		{"user@mail.example.com:25", "mail.example.com", false, nil},
		{"example.com.:8443", "example.com", false, nil},
		{"example.com/a?b#c", "example.com", false, nil},
		{"1.2.3.4", "", false, ErrIP},
		{"[2001:db8::1]", "", false, ErrIP},
		{"2001:db8::1", "", false, ErrIP},
		{"https://93.158.134.11/x", "", false, ErrIP},
		{"regexp:x", "", false, ErrUnsupported},
		{"keyword:netflix", "", false, ErrUnsupported},
		{"include:google", "", false, ErrUnsupported},
		{"exa mple.com", "", false, ErrInvalid},
		{"localhost", "", false, ErrInvalid},
		{"-bad.com", "", false, ErrInvalid},
		{"", "", false, ErrEmpty},
		{"# only a comment", "", false, ErrEmpty},
	} {
		got, err := Normalise(c.in)
		if !errors.Is(err, c.err) || got.Domain != c.want || got.Exact != c.exact {
			t.Errorf("%q → %+v %v, want %q exact=%v %v", c.in, got, err, c.want, c.exact, c.err)
		}
	}
}

func TestIDNToPunycode(t *testing.T) {
	n, err := Normalise("пример.рф")
	if err != nil || n.Domain != "xn--e1afmkfd.xn--p1ai" || n.Unicode != "пример.рф" || n.Exact {
		t.Fatalf("%+v %v", n, err)
	}
	n, err = Normalise("https://Кинопоиск.рф/film")
	if err != nil || n.Domain != "xn--h1aaecngahu.xn--p1ai" || n.Unicode != "кинопоиск.рф" {
		t.Fatalf("%+v %v", n, err)
	}
	if u := Unicode("xn--e1afmkfd.xn--p1ai"); u != "пример.рф" {
		t.Errorf("Unicode: %q", u)
	}
	if u := Unicode("example.com"); u != "" {
		t.Errorf("Unicode of ASCII: %q", u)
	}
}

func TestCoversParentsRegistrable(t *testing.T) {
	if !Covers("example.com", "example.com") || !Covers("example.com", "a.example.com") || Covers("example.com", "badexample.com") ||
		Covers("a.example.com", "example.com") {
		t.Error("Covers")
	}
	if got := Parents("a.b.c.d"); !slices.Equal(got, []string{"b.c.d", "c.d"}) {
		t.Errorf("Parents: %v", got)
	}
	if got := Parents("example.com"); len(got) != 0 {
		t.Errorf("Parents of a registrable: %v", got)
	}
	for in, want := range map[string]string{"www.claude.ai": "claude.ai", "me.github.io": "me.github.io", "a.b.co.uk": "b.co.uk"} {
		if got, err := Registrable(in); err != nil || got != want {
			t.Errorf("Registrable(%s) = %q %v, want %q", in, got, err, want)
		}
	}
}
