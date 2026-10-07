package platform_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

var (
	ctx     = context.Background()
	discard = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	tz, _ := time.LoadLocation("Europe/Moscow")
	return &config.Config{
		DataDir:   t.TempDir() + "/data",
		MasterKey: bytes.Repeat([]byte{3}, config.MasterKeySize),
		BaseURL:   &url.URL{Scheme: "https", Host: "proxier.example.com"},
		TZ:        tz,
	}
}

type fake struct{}

func (fake) Name() string { return "fake" }
func (fake) Migrations() fs.FS {
	return fstest.MapFS{"00001_init.sql": {Data: []byte("-- +goose Up\nCREATE TABLE fake_things (id INTEGER PRIMARY KEY);\n-- +goose Down\nDROP TABLE fake_things;\n")}}
}
func (fake) EventTypes() []events.Type {
	return []events.Type{{Name: "fake.happened", Module: "fake", Notify: true}}
}

func open(t *testing.T) *platform.App {
	t.Helper()
	a, err := platform.Open(testConfig(t), discard, fake{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return a
}

func TestOpenMigratesEveryModule(t *testing.T) {
	a := open(t)
	st, err := a.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modules := map[string]bool{}
	for _, s := range st {
		if !s.Applied {
			t.Errorf("%s %s not applied", s.Module, s.Path)
		}
		modules[s.Module] = true
	}
	if !modules["platform"] || !modules["fake"] {
		t.Errorf("modules migrated: %v", modules)
	}
	if a.Modules[0].Name() != "platform" {
		t.Error("the platform does not come first")
	}
	if err := a.Migrate(ctx); err != nil {
		t.Errorf("second Migrate: %v", err)
	}
}

func TestEventsAndSettingsAreDeclared(t *testing.T) {
	a := open(t)
	for _, name := range []string{"settings.changed", "auth.locked", "fake.happened"} {
		if _, ok := a.Events.Lookup(name); !ok {
			t.Errorf("%s not declared", name)
		}
	}
	tz, err := a.Settings.Get(ctx, "general.time_zone")
	if err != nil || tz != "Europe/Moscow" {
		t.Errorf("general.time_zone = %q, %v", tz, err)
	}
	err = a.Settings.Set(ctx, events.ActorAdmin, "general", map[string]string{"general.time_zone": "Mars/Base"})
	if _, ok := err.(settings.FieldErrors); !ok {
		t.Errorf("bad time zone: err = %v", err)
	}
	if err := a.Settings.Set(ctx, events.ActorAdmin, "general", map[string]string{"general.time_zone": "Asia/Yekaterinburg", "general.language": "ru"}); err != nil {
		t.Fatal(err)
	}
	got, _ := events.After(ctx, a.DB.R, 0, 10)
	if len(got) != 1 || got[0].Type != "settings.changed" || got[0].Module != "platform" {
		t.Errorf("events = %+v", got)
	}
}

func TestDuplicateModuleRefused(t *testing.T) {
	if _, err := platform.Open(testConfig(t), discard, fake{}, fake{}); err == nil {
		t.Fatal("a module registered twice was accepted")
	}
}

func TestHealth(t *testing.T) {
	a := open(t)
	e := a.HTTP()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	_ = a.Close()
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz after close = %d", rec.Code)
	}
}
