package services_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
)

func count(t *testing.T, h *routingtest.Harness, q string, args ...any) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, q, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddBareName(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\nfull:api.anthropic.com\nregexp:x\n")
	ctx := context.Background()
	id, err := h.Mod.Services.Add(ctx, "anthropic", "admin")
	if err != nil {
		t.Fatal(err)
	}
	it, _ := h.Mod.Services.Get(ctx, id)
	if it.Selector != "v2fly:anthropic" || it.Tag != "anthropic" || it.Source != "v2fly" {
		t.Errorf("%+v", it)
	}
	acc, err := h.Mod.Services.Accepted(ctx, id)
	if err != nil || acc.Status != "accepted" || !slices.Equal(acc.Set.Suffix, []string{"anthropic.com", "claude.ai"}) ||
		!slices.Equal(acc.Set.Exact, []string{"api.anthropic.com"}) || len(acc.Set.Skipped) != 1 || acc.AcceptedAt.IsZero() {
		t.Errorf("snapshot: %+v %v", acc, err)
	}
	ev := h.Events("routing.service_added")
	if len(ev) != 1 || ev[0].Payload["selector"] != "v2fly:anthropic" || ev[0].Payload["tag"] != "anthropic" ||
		ev[0].Payload["source"] != "v2fly" || ev[0].Subject.String() != "service:1" || ev[0].Actor != "admin" || !ev[0].Time.Equal(h.Now) {
		t.Errorf("event: %+v", ev)
	}
}

func TestAddSeparateTag(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\n")
	h.Up.Site("main", "ai", "claude.ai", "claude.ai")
	a := h.Upstream("v2fly:anthropic")
	b := h.Upstream("iplist:claude.ai")
	it, _ := h.Mod.Services.Get(context.Background(), b)
	if a == b || it.Tag != "claude.ai" || it.Selector != "iplist:claude.ai" {
		t.Errorf("%d %d %+v", a, b, it)
	}
}

func TestAddExistingSelector(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\n")
	id := h.Upstream("v2fly:anthropic")
	before := h.Up.Requests("/")
	_, err := h.Mod.Services.Add(context.Background(), "anthropic", "admin")
	var tt *services.TagTakenError
	if !errors.As(err, &tt) || tt.Tag != "anthropic" || tt.Existing.ID != id {
		t.Fatalf("%v", err)
	}
	if h.Up.Requests("/") != before || count(t, h, `SELECT count(*) FROM routing_services`) != 1 {
		t.Error("something was resolved or created")
	}
	rec := h.Login.Post("/routing/services", map[string][]string{"selector": {"anthropic"}})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "anthropic already exists") || !strings.Contains(body, `href="/routing/services/1"`) ||
		strings.Contains(body, "/switch") {
		t.Errorf("page %d: shows no existing service, or offers a switch", rec.Code)
	}
}

func TestIplistStoredAsItResolves(t *testing.T) {
	h := routingtest.New(t)
	h.Up.Site("main", "video", "youtube.com", "youtube.com", "ytimg.com")
	h.Up.Site("beta", "apple", "apple.com", "apple.com")
	ctx := context.Background()
	yt := h.Upstream("iplist:youtube.com")
	ap := h.Upstream("iplist:apple.com")
	a, _ := h.Mod.Services.Get(ctx, yt)
	b, _ := h.Mod.Services.Get(ctx, ap)
	if a.Selector != "iplist:youtube.com" || b.Selector != "iplist:beta:apple.com" || b.Tag != "apple.com" {
		t.Errorf("%s %s %s", a.Selector, b.Selector, b.Tag)
	}
	acc, _ := h.Mod.Services.Accepted(ctx, ap)
	if acc.Portal != "beta" || acc.Kind != "site" || acc.Selector != "iplist:beta:apple.com" {
		t.Errorf("snapshot: %+v", acc)
	}
}

func TestAddMissingList(t *testing.T) {
	h := routingtest.New(t)
	_, err := h.Mod.Services.Add(context.Background(), "v2fly:nosuchlist", "admin")
	if !errors.Is(err, sources.ErrNotFound) {
		t.Fatal(err)
	}
	rec := h.Login.Post("/routing/services", map[string][]string{"selector": {"v2fly:nosuchlist"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "v2fly has no list ‘nosuchlist’") && !strings.Contains(rec.Body.String(), "v2fly has no list &#39;nosuchlist&#39;") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	if count(t, h, `SELECT count(*) FROM routing_services`) != 0 || len(h.Events("routing.service_added")) != 0 {
		t.Error("something was created")
	}
}

func TestAddWithPortalDown(t *testing.T) {
	h := routingtest.New(t)
	h.Up.PortalDown("beta", true)
	_, err := h.Mod.Services.Add(context.Background(), "iplist:example.org", "admin")
	var ue *sources.UnreachableError
	if !errors.As(err, &ue) {
		t.Fatal(err)
	}
	rec := h.Login.Post("/routing/services", map[string][]string{"selector": {"iplist:example.org"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Not found on main, russia; beta unreachable.") {
		t.Errorf("%d", rec.Code)
	}
	if count(t, h, `SELECT count(*) FROM routing_services`) != 0 {
		t.Error("something was created")
	}
}

func TestAddURLServices(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	u := h.Up.File("share/x/tunneled-domains.txt", "example.com\n")
	l := h.Up.File("share/x/list.txt", "mine.example\n")
	id := h.Upstream(u)
	it, _ := h.Mod.Services.Get(ctx, id)
	if it.Tag != "tunneled-domains" || it.Selector != u || it.Source != "url" {
		t.Errorf("%+v", it)
	}
	mine := h.Upstream("mine=" + l)
	it, _ = h.Mod.Services.Get(ctx, mine)
	if it.Tag != "mine" || it.Selector != "mine="+l {
		t.Errorf("%+v", it)
	}
	other := h.Up.File("elsewhere/tunneled-domains.txt", "other.example\n")
	rec := h.Login.Post("/routing/services", map[string][]string{"selector": {other}})
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `value="tunneled-domains-2=`+other+`"`) || !strings.Contains(body, "/routing/services/1/switch") {
		t.Errorf("a taken URL tag: %d", rec.Code)
	}
	if _, err := h.Mod.Services.Add(ctx, "tunneled-domains-2="+other, "admin"); err != nil {
		t.Error(err)
	}
}

func TestAddRefusesEmpty(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("odd", "regexp:^a$\nkeyword:b\nnot_valid\n")
	_, err := h.Mod.Services.Add(context.Background(), "v2fly:odd", "admin")
	var ee *services.EmptyResolveError
	if !errors.Is(err, services.ErrEmptyResolve) || !errors.As(err, &ee) || ee.Skipped != 3 ||
		err.Error() != "v2fly:odd has no names RouterOS can use (3 skipped)" {
		t.Fatal(err)
	}
	if count(t, h, `SELECT count(*) FROM routing_services`) != 0 {
		t.Error("created")
	}
}

func TestSwitchSource(t *testing.T) {
	h := routingtest.New(t)
	ctx := context.Background()
	h.Up.Site("main", "apple", "apple", "apple.com")
	h.Up.Site("beta", "apple", "apple", "apple.com", "icloud.com")
	id := h.Upstream("iplist:apple")
	if err := h.Mod.Services.Switch(ctx, id, "iplist:beta:apple", "admin"); err != nil {
		t.Fatal(err)
	}
	it, _ := h.Mod.Services.Get(ctx, id)
	acc, _ := h.Mod.Services.Accepted(ctx, id)
	if it.Selector != "iplist:beta:apple" || acc.Portal != "beta" || acc.Count() != 2 || acc.Added != 1 {
		t.Errorf("%+v %+v", it, acc)
	}
	hist, _ := h.Mod.Services.Snapshots(ctx, id)
	if len(hist) != 2 || hist[1].Status != "superseded" {
		t.Errorf("history: %+v", hist)
	}
	marks := h.Marks.Changes()
	if len(marks) != 1 || !slices.Equal(marks[0].Services, []int64{id}) || marks[0].Actor != "admin" {
		t.Errorf("marks: %+v", marks)
	}
	ev := h.Events("routing.service_updated")
	if len(ev) != 1 || ev[0].Payload["changes"] != "source" || ev[0].Payload["from"] != "iplist:apple" || ev[0].Payload["to"] != "iplist:beta:apple" {
		t.Errorf("event: %+v", ev)
	}

	h.Up.V2fly("anthropic", "anthropic.com\n")
	an := h.Upstream("anthropic")
	err := h.Mod.Services.Switch(ctx, an, "iplist:claude.ai", "admin")
	var st *services.SwitchTagError
	if !errors.As(err, &st) || st.Want != "anthropic" || st.Got != "claude.ai" {
		t.Errorf("another tag: %v", err)
	}
	if err := h.Mod.Services.Switch(ctx, an, "v2fly:anthropic", "admin"); err != nil {
		t.Error(err)
	}
	if err := h.Mod.Services.Switch(ctx, id, "iplist:beta:apple", "admin"); err != nil {
		t.Error(err)
	}
	if len(h.Events("routing.service_updated")) != 1 || len(h.Marks.Changes()) != 1 {
		t.Error("switching to the same selector recorded something")
	}
	cu := h.Custom("mine", "example.com")
	if err := h.Mod.Services.Switch(ctx, cu, "mine=https://x/y", "admin"); !errors.Is(err, services.ErrCustom) {
		t.Errorf("custom: %v", err)
	}
}
