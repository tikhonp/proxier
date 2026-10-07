package settings_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

var ctx = context.Background()

type env struct {
	db    *db.DB
	store *settings.Store
}

func setup(t *testing.T) env {
	t.Helper()
	d := dbtest.Open(t)
	v, err := vault.New(bytes.Repeat([]byte{7}, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	c := events.NewCatalog()
	if err := c.Declare(settings.ChangedEvent); err != nil {
		t.Fatal(err)
	}
	s := settings.New(d, v, c)
	if err := s.Register(settings.Section{Name: "demo", Module: "platform", Fields: []settings.Field{
		{Key: "demo.name", Kind: settings.String, Default: "Proxier", MaxLen: 10},
		{Key: "demo.count", Kind: settings.Int, Default: "5", Min: 1, Max: 10},
		{Key: "demo.on", Kind: settings.Bool, Default: "false"},
		{Key: "demo.every", Kind: settings.Duration, Default: "5m0s", Min: int64(time.Minute), Max: int64(time.Hour)},
		{Key: "demo.lang", Kind: settings.Enum, Default: "en", Options: []string{"en", "ru"}},
		{Key: "demo.token", Kind: settings.Secret},
	}}); err != nil {
		t.Fatal(err)
	}
	return env{d, s}
}

func (e env) changes(t *testing.T) []events.Event {
	t.Helper()
	got, err := events.After(ctx, e.db.R, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDefaults(t *testing.T) {
	e := setup(t)
	v, err := e.store.Lookup(ctx, "demo.name")
	if err != nil || v.Value != "Proxier" || !v.IsDefault {
		t.Fatalf("Lookup = %+v, %v", v, err)
	}
	n, _ := e.store.GetInt(ctx, "demo.count")
	on, _ := e.store.GetBool(ctx, "demo.on")
	every, _ := e.store.GetDuration(ctx, "demo.every")
	if n != 5 || on || every != 5*time.Minute {
		t.Errorf("typed defaults: %d %v %v", n, on, every)
	}
	if _, err := e.store.Get(ctx, "demo.nope"); !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestSetTypedValues(t *testing.T) {
	e := setup(t)
	err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{
		"demo.name": "  Home  ", "demo.count": "07", "demo.on": "on", "demo.every": "90m", "demo.lang": "ru",
	})
	if err == nil || !errors.As(err, new(settings.FieldErrors)) {
		t.Fatalf("90m is above the max: err = %v", err)
	}
	err = e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{
		"demo.name": "  Home  ", "demo.count": "07", "demo.on": "on", "demo.every": "90s", "demo.lang": "ru",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"demo.name": "Home", "demo.count": "7", "demo.on": "true", "demo.every": "1m30s", "demo.lang": "ru"} {
		v, err := e.store.Lookup(ctx, key)
		if err != nil || v.Value != want || v.IsDefault {
			t.Errorf("%s = %+v, %v; want %q", key, v, err, want)
		}
	}
}

func TestRefusalWritesNothing(t *testing.T) {
	e := setup(t)
	err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{
		"demo.name": "ok", "demo.count": "11", "demo.lang": "de", "demo.on": "maybe",
	})
	var fe settings.FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v", err)
	}
	if len(fe) != 3 || fe["demo.count"] == "" || fe["demo.lang"] == "" || fe["demo.on"] == "" {
		t.Errorf("field errors = %v", fe)
	}
	if v, _ := e.store.Get(ctx, "demo.name"); v != "Proxier" {
		t.Errorf("valid field written despite the refusal: %q", v)
	}
	if len(e.changes(t)) != 0 {
		t.Error("an event was recorded for a refused save")
	}
}

func TestSecretIsSealed(t *testing.T) {
	e := setup(t)
	const token = "123456:AAH-super-secret-bot-token"
	if err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"demo.token": token}); err != nil {
		t.Fatal(err)
	}
	if v, err := e.store.Get(ctx, "demo.token"); err != nil || v != token {
		t.Fatalf("Get = %q, %v", v, err)
	}
	var raw []byte
	var value *string
	if err := e.db.R.QueryRow(`SELECT secret_value, value FROM settings WHERE key = 'demo.token'`).Scan(&raw, &value); err != nil {
		t.Fatal(err)
	}
	if value != nil || len(raw) == 0 || bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte("secret")) {
		t.Errorf("stored in the clear: value=%v raw=%q", value, raw)
	}
	ev := e.changes(t)
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	if keys := ev[0].Payload["keys"].([]any); len(keys) != 1 || keys[0] != "demo.token" {
		t.Errorf("payload = %v", ev[0].Payload)
	}
	var payload string
	_ = e.db.R.Get(&payload, `SELECT payload FROM events`)
	if bytes.Contains([]byte(payload), []byte(token)) {
		t.Error("the event holds the secret")
	}
}

func TestChangedEventListsOnlyChangedKeys(t *testing.T) {
	e := setup(t)
	if err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"demo.name": "Proxier", "demo.count": "5"}); err != nil {
		t.Fatal(err)
	}
	if len(e.changes(t)) != 0 {
		t.Fatal("saving the defaults recorded an event")
	}
	if err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"demo.name": "Home", "demo.count": "5", "demo.lang": "ru"}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"demo.name": "Home", "demo.lang": "ru"}); err != nil {
		t.Fatal(err)
	}
	ev := e.changes(t)
	if len(ev) != 1 {
		t.Fatalf("events = %+v", ev)
	}
	got := ev[0]
	if got.Type != "settings.changed" || got.Module != "platform" || got.Subject.String() != "settings:demo" || got.Actor != "admin" {
		t.Errorf("event = %+v", got)
	}
	keys := got.Payload["keys"].([]any)
	if len(keys) != 2 || keys[0] != "demo.lang" || keys[1] != "demo.name" {
		t.Errorf("keys = %v", keys)
	}
}

func TestBackToDefaultRemovesRow(t *testing.T) {
	e := setup(t)
	_ = e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"demo.lang": "ru"})
	if err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"demo.lang": "en"}); err != nil {
		t.Fatal(err)
	}
	v, _ := e.store.Lookup(ctx, "demo.lang")
	if !v.IsDefault {
		t.Error("value equal to the default is still stored")
	}
	var n int
	_ = e.db.R.Get(&n, `SELECT count(*) FROM settings`)
	if n != 0 {
		t.Errorf("%d rows left", n)
	}
	if len(e.changes(t)) != 2 {
		t.Error("going back to the default is a change too")
	}
}

func TestKeyOutsideSection(t *testing.T) {
	e := setup(t)
	if err := e.store.Set(ctx, events.ActorAdmin, "demo", map[string]string{"other.x": "1"}); !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("err = %v", err)
	}
	if err := e.store.Set(ctx, events.ActorAdmin, "nope", nil); err == nil {
		t.Error("unknown section accepted")
	}
}

func TestRegisterChecks(t *testing.T) {
	e := setup(t)
	for name, sec := range map[string]settings.Section{
		"duplicate section": {Name: "demo"},
		"key outside":       {Name: "x", Fields: []settings.Field{{Key: "y.a", Kind: settings.String}}},
		"bad default":       {Name: "x", Fields: []settings.Field{{Key: "x.a", Kind: settings.Int, Default: "many"}}},
		"non-canonical":     {Name: "x", Fields: []settings.Field{{Key: "x.a", Kind: settings.Duration, Default: "5m"}}},
		"duplicate key":     {Name: "x", Fields: []settings.Field{{Key: "x.a", Kind: settings.Bool, Default: "false"}, {Key: "x.a", Kind: settings.Bool, Default: "false"}}},
	} {
		if err := e.store.Register(sec); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	if len(e.store.Sections()) != 1 {
		t.Errorf("a refused section was kept: %v", e.store.Sections())
	}
}
