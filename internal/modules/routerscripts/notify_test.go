package routerscripts_test

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func TestFetchedNotificationText(t *testing.T) {
	h := rscriptstest.New(t)
	ctx := context.Background()
	id := h.Script("fresh-router", paramstest.Annotated())
	v, err := h.Mod.Generations.Form(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := v.Form
	f.RouterName, f.Router.Mode, f.Link.Mode = "Dacha", generations.RouterNone, generations.LinkType
	gid, err := h.Mod.Generations.Generate(ctx, id, f, "admin")
	if err != nil {
		t.Fatal(err)
	}
	fetched := func(ua string) events.Event {
		return events.Event{Type: "routerscript.fetched", Subject: generations.Subject(gid), Actor: "system",
			Payload: map[string]any{"ip": "198.51.100.4", "user_agent": ua}}
	}
	for _, c := range []struct {
		lang        i18n.Lang
		ua          string
		title, body string
	}{
		{i18n.EN, "Mikrotik/7.24.5 Fetch", "Dacha · fresh-router v1 was fetched from 198.51.100.4", "Mikrotik/7.24.5 Fetch. Its fetch URL is used up."},
		{i18n.EN, "", "Dacha · fresh-router v1 was fetched from 198.51.100.4", "Its fetch URL is used up."},
		{i18n.RU, "Mikrotik/7.24.5 Fetch", "Dacha · fresh-router v1: файл скачан с 198.51.100.4", "Mikrotik/7.24.5 Fetch. Ссылка для скачивания больше не работает."},
		{i18n.RU, "", "Dacha · fresh-router v1: файл скачан с 198.51.100.4", "Ссылка для скачивания больше не работает."},
	} {
		msg, ok, err := h.Mod.RenderNotification(ctx, fetched(c.ua), h.App.I18n.Localizer(c.lang, nil))
		if err != nil || !ok || msg.Title != c.title || msg.Body != c.body {
			t.Errorf("%s %q: %+v %v %v", c.lang, c.ua, msg, ok, err)
		}
	}
	// it notifies by default, 📥; other types keep the default texts
	for _, typ := range h.App.Events.Types() {
		if typ.Name == "routerscript.fetched" && (!typ.Notify || typ.Emoji != "📥") {
			t.Errorf("type: %+v", typ)
		}
	}
	if _, ok, _ := h.Mod.RenderNotification(ctx, events.Event{Type: "routerscript.generated", Subject: generations.Subject(gid)}, h.App.I18n.Localizer(i18n.EN, nil)); ok {
		t.Error("generated rendered by the module")
	}
}
