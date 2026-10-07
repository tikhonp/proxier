package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/auth"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// scripted answers prompts from a list; asking past the end fails the test.
type scripted struct {
	t       *testing.T
	answers []string
	asked   []string
}

func (s *scripted) next(prompt string) (string, error) {
	s.asked = append(s.asked, prompt)
	if len(s.answers) == 0 {
		s.t.Fatalf("asked %q with no answers left", prompt)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}
func (s *scripted) Line(p string) (string, error)     { return s.next(p) }
func (s *scripted) Password(p string) (string, error) { return s.next(p) }

func newAuth(t *testing.T) *auth.Service {
	t.Helper()
	d := dbtest.Open(t)
	v, _ := vault.New(make([]byte, 32))
	ev := events.NewCatalog()
	if err := ev.Declare(append(auth.Events, settings.ChangedEvent)...); err != nil {
		t.Fatal(err)
	}
	st := settings.New(d, v, ev)
	if err := st.Register(auth.SecuritySection, settings.Section{Name: "general", Module: "platform", Fields: []settings.Field{
		{Key: "general.language", Kind: settings.Enum, Default: "en", Options: []string{"en", "ru"}},
	}}); err != nil {
		t.Fatal(err)
	}
	a := auth.New(d, v, ev, st)
	a.Params = auth.Params{Memory: 1024, Time: 1, Threads: 1}
	a.Sleep = func(context.Context, time.Duration) {}
	return a
}

func TestCreateAdminAsksAgainForShortPassword(t *testing.T) {
	a := newAuth(t)
	p := &scripted{t: t, answers: []string{"admin", "short-11-ch", "short-11-ch", "long enough password", "long enough password"}}
	var out bytes.Buffer
	if err := createAdmin(t.Context(), a, p, &out); err != nil {
		t.Fatalf("createAdmin: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "at least 12 characters") {
		t.Errorf("no explanation shown:\n%s", out.String())
	}
	if ok, _ := a.AdminExists(t.Context()); !ok {
		t.Error("admin not created")
	}
}

func TestCreateAdminGivesUpAfterThreeRounds(t *testing.T) {
	a := newAuth(t)
	p := &scripted{t: t, answers: []string{"admin", "a", "b", "a", "b", "a", "b"}}
	if err := createAdmin(t.Context(), a, p, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error after three bad rounds")
	}
	if ok, _ := a.AdminExists(t.Context()); ok {
		t.Error("admin created")
	}
}

func TestCreateAdminRefusesWhenAdminExists(t *testing.T) {
	a := newAuth(t)
	if err := a.CreateAdmin(t.Context(), "admin", "long enough password"); err != nil {
		t.Fatal(err)
	}
	p := &scripted{t: t} // must not be asked anything
	var out bytes.Buffer
	if err := createAdmin(t.Context(), a, p, &out); !errors.Is(err, errAdminExists) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "An admin already exists; use reset-password.") || len(p.asked) != 0 {
		t.Errorf("output %q, asked %v", out.String(), p.asked)
	}
}

func TestResetPasswordEndsSessions(t *testing.T) {
	a := newAuth(t)
	if err := a.CreateAdmin(t.Context(), "admin", "long enough password"); err != nil {
		t.Fatal(err)
	}
	res, err := a.SignIn(t.Context(), "admin", "long enough password", "1.1.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &scripted{t: t, answers: []string{"a brand new password", "a brand new password"}}
	var out bytes.Buffer
	if err := resetPassword(t.Context(), a, p, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(t.Context(), res.Session.Token, "1.1.1.1"); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session survived: %v", err)
	}
	if !strings.Contains(out.String(), "1 session(s) ended") {
		t.Errorf("output %q", out.String())
	}
}
