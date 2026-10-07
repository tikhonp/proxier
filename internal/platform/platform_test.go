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
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
	"github.com/tikhonp/proxier/internal/platform/ui"
	"github.com/tikhonp/proxier/internal/platform/web"
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
	// The first Migrate generated Proxier's SSH key before the settings changed.
	got, _ := events.After(ctx, a.DB.R, 0, 10)
	if len(got) != 2 || got[0].Type != "ssh.key_generated" || got[1].Type != "settings.changed" || got[1].Module != "platform" {
		t.Errorf("events = %+v", got)
	}
}

func TestFirstStartGeneratesTheSSHKeyOnce(t *testing.T) {
	cfg := testConfig(t)
	start := func() (line string, a *platform.App) {
		t.Helper()
		a, err := platform.Open(cfg, discard, fake{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.Close() })
		if err := a.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		line, _, err = a.SSH.PublicKey(ctx)
		if err != nil {
			t.Fatalf("no SSH key after Migrate: %v", err)
		}
		return line, a
	}
	first, a := start()
	if !strings.HasSuffix(first, " proxier@Proxier") {
		t.Errorf("key comment: %q", first)
	}
	_ = a.Close()
	second, a2 := start() // a restart
	if first != second {
		t.Fatal("the SSH key changed on restart")
	}
	got, _ := events.After(ctx, a2.DB.R, 0, 10)
	if len(got) != 1 || got[0].Type != "ssh.key_generated" {
		t.Errorf("events after a restart: %+v", got)
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

// everything implements every optional interface the platform looks for.
type full struct{ fake }

var inited, routed, searched bool

func (full) Init(d module.Deps) error {
	inited = d.Auth != nil && d.I18n != nil && d.Settings != nil
	return nil
}
func (full) Messages() i18n.Messages {
	return i18n.Messages{"fake.hello": {EN: "Hello", RU: "Привет"}}
}
func (full) Routes(r web.Routes) {
	r.Admin.GET("/fake", func(c *echo.Context) error { routed = true; return c.String(200, "fake page") })
}
func (full) Nav() []ui.NavItem {
	return []ui.NavItem{{Group: "servers", Label: "fake.hello", Href: "/fake", GoKey: "f", Order: 1}}
}
func (full) SettingsPages() []ui.SettingsPage {
	return []ui.SettingsPage{{Slug: "fake", Title: "fake.hello", Order: 60}}
}
func (full) Search(_ context.Context, q string, _ int) ([]ui.SearchHit, error) {
	searched = true
	return []ui.SearchHit{{Label: "fake thing " + q, Href: "/fake"}}, nil
}

func TestOptionalModuleInterfacesArePickedUp(t *testing.T) {
	inited, routed, searched = false, false, false
	s := sitetest.New(t, sitetest.Options{Modules: []module.Module{full{}}})
	if !inited {
		t.Error("Init was not called with the platform services")
	}
	if !s.App.I18n.Has("fake.hello") {
		t.Error("messages were not added")
	}
	l := s.SignIn("")
	if rec := l.Get("/fake"); rec.Code != 200 || !routed {
		t.Errorf("route: %d", rec.Code)
	}
	if body := l.Get("/").Body.String(); !strings.Contains(body, `href="/fake"`) || !strings.Contains(body, `data-go-key="f"`) {
		t.Error("the module's nav entry is missing from the shell")
	}
	if body := l.Get("/search?q=zzz").Body.String(); !searched || !strings.Contains(body, "fake thing zzz") {
		t.Errorf("searcher: %s", body)
	}
	if body := l.Get("/settings/general").Body.String(); !strings.Contains(body, `href="/settings/fake"`) {
		t.Error("the module's settings page is not listed")
	}
}
