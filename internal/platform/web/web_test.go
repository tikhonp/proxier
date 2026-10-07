package web

import "testing"

func TestSafeNextAcceptsOnlyLocalPaths(t *testing.T) {
	cases := map[string]string{
		"/servers?x=1":           "/servers?x=1",
		"/settings/security":     "/settings/security",
		"":                       "/",
		"https://evil.example/":  "/",
		"//evil.example":         "/",
		"/\\evil.example":        "/",
		"javascript:alert(1)":    "/",
		"/login":                 "/",
		"/login?next=/x":         "/",
		"/logout":                "/",
		"relative/path":          "/",
		"/ok\r\nSet-Cookie: a=b": "/",
		"/a/../b":                "/a/../b",
	}
	for in, want := range cases {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
