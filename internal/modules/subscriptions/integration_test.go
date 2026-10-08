package subscriptions_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/i18n"
)

func names(t *testing.T, s *subs.Service, id int64) string {
	t.Helper()
	ms, err := s.Members(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return strings.Join(out, ",")
}

func TestRealActivationJoinsSubscription(t *testing.T) {
	h, mod := substest.WithServers(t)
	ctx := context.Background()
	me, err := mod.Subs.Create(ctx, "Me", "", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	family, _ := mod.Subs.Create(ctx, "Family", "", "", "admin")
	s, _ := mod.Subs.Get(ctx, me)
	st := subs.Settings{Name: s.Name, Title: s.Title, Formats: s.Formats, DefaultFormat: s.DefaultFormat, UpdateHours: s.UpdateHours,
		HideStates: s.Hide.States, GraceMinutes: 30, AutoAdd: true}
	if _, err := mod.Subs.Update(ctx, me, st, "admin"); err != nil {
		t.Fatal(err)
	}
	id := h.Provisioned()
	if h.Server(id).State != "active" {
		t.Fatalf("not active: %s", h.Server(id).State)
	}
	h.WaitFor("nl-1 in Me", func() bool { return names(t, mod.Subs, me) == "nl-1" })
	if got := names(t, mod.Subs, family); got != "" {
		t.Errorf("Family got %s", got)
	}
	// the preview serves the real endpoint
	resp, _, err := mod.Subs.Preview(i18n.WithLocalizer(ctx, h.App.I18n.Localizer(i18n.EN, nil)), me, "", false)
	if err != nil || resp.Outcome != output.OK || len(resp.Lines) != 1 || !strings.Contains(resp.Lines[0], "@nl-1.hosts.tikhonnnnn.com:443") ||
		!strings.HasSuffix(resp.Lines[0], "#%F0%9F%87%B3%F0%9F%87%B1%20Netherlands%201") {
		t.Errorf("preview: %+v %v", resp.Lines, err)
	}
}

func TestRealRetirementLeavesSubscriptions(t *testing.T) {
	h, mod := substest.WithServers(t)
	ctx := context.Background()
	id := h.Provisioned()
	family, _ := mod.Subs.Create(ctx, "Family", "", "", "admin")
	only, _ := mod.Subs.Create(ctx, "Only", "", "", "admin")
	for _, sub := range []int64{family, only} {
		if err := mod.Subs.AddServers(ctx, sub, []int64{id}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	// the server's page names its subscriptions
	body := h.Login.Get("/servers/" + itoa(id)).Body.String()
	if !strings.Contains(body, "Family, Only · links: 0") {
		t.Error("the server page does not name its subscriptions")
	}
	if _, err := h.Mod.Retire.Retire(ctx, id, "nl-1", true, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if h.Server(id).State != "retired" {
		t.Fatalf("not retired: %s", h.Server(id).State)
	}
	h.WaitFor("nl-1 removed", func() bool { return names(t, mod.Subs, family) == "" && names(t, mod.Subs, only) == "" })
	resp, _, err := mod.Subs.Preview(i18n.WithLocalizer(ctx, h.App.I18n.Localizer(i18n.EN, nil)), only, "", false)
	if err != nil || resp.Outcome != output.StubEmpty || resp.Lines[0] != output.StubURI("⚠️ No servers yet") {
		t.Errorf("empty preview: %+v %v", resp, err)
	}
	var retired int
	for _, e := range h.Events("subscription.servers_changed") {
		if e.Payload["reason"] == "retired" {
			retired++
		}
	}
	if retired != 2 {
		t.Errorf("%d retirement events", retired)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
