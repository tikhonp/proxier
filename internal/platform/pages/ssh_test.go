package pages_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/sshx/sshxtest"
	"golang.org/x/crypto/ssh"
)

// pinnedThenChanged returns a site with one host whose key changed after it
// was pinned.
func pinnedThenChanged(t *testing.T) (s *sitetest.Site, l *sitetest.Login, srv *sshxtest.Server, old, now string, id int64) {
	t.Helper()
	s = sitetest.New(t, sitetest.Options{})
	l = s.SignIn("")
	line, _, err := s.App.SSH.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	srv = sshxtest.NewServer(t, pub)
	tg := sshx.Target{Hop: sshx.Hop{Address: srv.Addr, User: "root", Subject: "server:1"}}
	c, err := s.App.SSH.Connect(context.Background(), tg, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	old = ssh.FingerprintSHA256(srv.HostKey())
	srv.RotateHostKey()
	now = ssh.FingerprintSHA256(srv.HostKey())
	var changed *sshx.HostKeyChangedError
	if _, err := s.App.SSH.Connect(context.Background(), tg, nil); !errors.As(err, &changed) {
		t.Fatalf("want a changed key, got %v", err)
	}
	hosts, _ := s.App.SSH.KnownHosts(context.Background())
	return s, l, srv, old, now, hosts[0].ID
}

func recorded(t *testing.T, s *sitetest.Site, typ string) []events.Event {
	t.Helper()
	l, err := events.List(context.Background(), s.App.DB.R, events.Filter{Type: typ, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestKnownHostActions(t *testing.T) {
	s, l, srv, old, now, id := pinnedThenChanged(t)
	idStr := "/settings/ssh/hosts/" + strconv.FormatInt(id, 10)

	// The page shows both fingerprints, the way out, and Proxier's own key.
	line, fp, _ := s.App.SSH.PublicKey(context.Background())
	body := l.Get("/settings/ssh").Body.String()
	for _, want := range []string{srv.Addr, old, now, "Accept new key", "server:1", line, fp, "Regenerate"} {
		if !strings.Contains(body, want) {
			t.Errorf("the SSH page lacks %q", want)
		}
	}

	// Accept and Forget need the CSRF token.
	for _, act := range []string{"/accept", "/forget"} {
		rec := s.Do(sitetest.Req{Method: "POST", Path: idStr + act, Cookies: []*http.Cookie{l.Cookie}, Form: url.Values{}})
		if rec.Code != 403 {
			t.Fatalf("%s without CSRF: %d", act, rec.Code)
		}
	}
	if hosts, _ := s.App.SSH.KnownHosts(context.Background()); len(hosts) != 1 || hosts[0].PendingFingerprint != now {
		t.Fatal("a refused request changed the host")
	}

	if rec := l.Post(idStr+"/accept", nil); rec.Code != 303 {
		t.Fatalf("accept: %d\n%s", rec.Code, rec.Body)
	}
	ev := recorded(t, s, "ssh.host_key_accepted")
	if len(ev) != 1 || ev[0].Actor != "admin" || ev[0].Payload["old"] != old || ev[0].Payload["new"] != now {
		t.Fatalf("events: %+v", ev)
	}
	if page := l.Get("/settings/ssh").Body.String(); strings.Contains(page, "Accept new key") || !strings.Contains(page, now) {
		t.Errorf("after accepting, the page should show only the new key:\n%s", page)
	}
	// A second accept has nothing to do and changes nothing.
	if rec := l.Post(idStr+"/accept", nil); rec.Code != 303 || len(recorded(t, s, "ssh.host_key_accepted")) != 1 {
		t.Fatalf("second accept: %d", rec.Code)
	}

	if rec := l.Post(idStr+"/forget", nil); rec.Code != 303 {
		t.Fatalf("forget: %d", rec.Code)
	}
	if hosts, _ := s.App.SSH.KnownHosts(context.Background()); len(hosts) != 0 {
		t.Fatal("the host is still pinned")
	}
	if rec := l.Post(idStr+"/forget", nil); rec.Code != 404 {
		t.Fatalf("forgetting twice: %d", rec.Code)
	}
	if rec := l.Post("/settings/ssh/hosts/nope/accept", nil); rec.Code != 404 {
		t.Fatalf("a bad id: %d", rec.Code)
	}
}

func TestRegenerateAndPersonalKeysPages(t *testing.T) {
	s := sitetest.New(t, sitetest.Options{})
	l := s.SignIn("")
	_, before, _ := s.App.SSH.PublicKey(context.Background())

	if rec := l.Post("/settings/ssh/regenerate", nil); rec.Code != 303 || !strings.HasSuffix(rec.Header().Get("Location"), "saved=regenerated") {
		t.Fatalf("regenerate: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if _, after, _ := s.App.SSH.PublicKey(context.Background()); after == before {
		t.Fatal("the key was not replaced")
	}
	if got := recorded(t, s, "ssh.key_generated"); len(got) != 2 || got[0].Payload["regenerated"] != true {
		t.Fatalf("events: %+v", got)
	}

	rec := l.Post("/settings/ssh/personal-keys", url.Values{"keys": {"# mine\nnot a key\r\n"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "line 2 is not a public key") || !strings.Contains(rec.Body.String(), "not a key") {
		t.Fatalf("a bad line: %d\n%s", rec.Code, rec.Body)
	}
	good := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl me@laptop"
	if rec := l.Post("/settings/ssh/personal-keys", url.Values{"keys": {good + "\r\n\r\n"}}); rec.Code != 303 {
		t.Fatalf("good keys: %d\n%s", rec.Code, rec.Body)
	}
	if page := l.Get("/settings/ssh?saved=keys").Body.String(); !strings.Contains(page, good) || !strings.Contains(page, "Personal keys saved.") {
		t.Errorf("the saved key is not on the page:\n%s", page)
	}
}
