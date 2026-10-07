package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	for args, want := range map[string]int{"": 2, "nope": 2, "help": 0, "version": 0} {
		var out, errOut bytes.Buffer
		var argv []string
		if args != "" {
			argv = strings.Fields(args)
		}
		if got := run(argv, &out, &errOut); got != want {
			t.Errorf("run(%q) = %d, want %d", args, got, want)
		}
	}
}

func TestHealthcheckCommand(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	listen := srv.Listener.Addr().String() // 127.0.0.1:port
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "PROXIER_LISTEN" {
				return v
			}
			return ""
		}
	}
	run := func(v string) int {
		var errOut bytes.Buffer
		return healthcheck(env(v), &errOut)
	}

	if got := run(listen); got != 0 {
		t.Fatalf("healthy server: exit %d", got)
	}
	// ":port" form, as PROXIER_LISTEN is written in compose.
	if got := run(":" + listen[strings.LastIndex(listen, ":")+1:]); got != 0 {
		t.Fatalf(":port form: exit %d", got)
	}
	status = http.StatusServiceUnavailable
	if got := run(listen); got != 1 {
		t.Fatalf("unhealthy server: exit %d", got)
	}
	srv.Close()
	if got := run(listen); got != 1 {
		t.Fatalf("nothing listening: exit %d", got)
	}
	if got := run("not an address"); got != 1 {
		t.Fatalf("bad PROXIER_LISTEN: exit %d", got)
	}
}
