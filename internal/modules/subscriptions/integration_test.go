package subscriptions_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/subscriptions"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sitetest"

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

func TestFetchDuringAndAfterRotation(t *testing.T) {
	h, mod := substest.WithServers(t)
	ctx := context.Background()
	id := h.Provisioned()
	sub, _ := mod.Subs.Create(ctx, "Family", "", "", "admin")
	if err := mod.Subs.AddServers(ctx, sub, []int64{id}, "admin"); err != nil {
		t.Fatal(err)
	}
	link, err := mod.Links.Create(ctx, links.New{Name: "Mom", SubscriptionID: sub, Lang: "en"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := mod.Links.Token(ctx, link)
	fetch := func() string {
		t.Helper()
		rec := h.Site.Do(sitetest.Req{Path: "/s/" + token, Addr: "198.51.100.23:5000"})
		if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "vless://") {
			t.Fatalf("fetch: %d %q", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	before := fetch()

	reached, release := h.VPS.Hold(remote.OpComposeUp)
	job, err := h.Mod.Deploy.Rotate(ctx, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	if during := fetch(); during != before {
		t.Errorf("while the rotation runs the link serves new values:\n%s\n%s", before, during)
	}
	release()
	h.Drain()
	if j, err := h.App.Jobs.Job(ctx, job); err != nil || j.State != jobs.Succeeded {
		t.Fatalf("rotation: %+v %v", j, err)
	}
	after := fetch()
	if after == before || strings.Split(after, "@")[0] == strings.Split(before, "@")[0] {
		t.Errorf("after the rotation the credential is the same:\n%s\n%s", before, after)
	}
}

// cutOffSetup: three real servers in "Family", the link "Alex" to cut off and
// "Mom" whose app keeps working.
func cutOffSetup(t *testing.T) (h *serverstest.Harness, mod *subscriptions.Module, ids []int64, alex int64, momToken string) {
	t.Helper()
	h, mod = substest.WithServers(t)
	ctx := context.Background()
	ids = []int64{h.Provisioned(), h.AddServer("10.77.0.2"), h.AddServer("10.77.0.3")}
	sub, _ := mod.Subs.Create(ctx, "Family", "", "", "admin")
	for _, id := range ids {
		if err := mod.Subs.AddServers(ctx, sub, []int64{id}, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	alex, err := mod.Links.Create(ctx, links.New{Name: "Alex", SubscriptionID: sub, Lang: "en"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	mom, err := mod.Links.Create(ctx, links.New{Name: "Mom", SubscriptionID: sub, Lang: "en"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	momToken, _ = mod.Links.Token(ctx, mom)
	return h, mod, ids, alex, momToken
}

// credentialsOf is the user part of each vless:// line a link serves.
func credentialsOf(t *testing.T, h *serverstest.Harness, token string) []string {
	t.Helper()
	rec := h.Site.Do(sitetest.Req{Path: "/s/" + token, Addr: "198.51.100.23:5000"})
	if rec.Code != 200 {
		t.Fatalf("fetch: %d %q", rec.Code, rec.Body.String())
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		out = append(out, strings.SplitN(line, "@", 2)[0])
	}
	return out
}

// chained fails unless each rotation after the first was queued by the event
// that ended the previous one (they run one after another).
func chained(t *testing.T, h *serverstest.Harness) {
	t.Helper()
	var rows []struct {
		ID        int64  `db:"id"`
		CreatedBy string `db:"created_by"`
	}
	if err := h.App.DB.R.Select(&rows, `SELECT id, created_by FROM jobs WHERE type = 'servers.rotate' ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	ended := map[string]string{} // event:<id> → the job it ended
	for _, typ := range []string{"server.credentials_rotated", "server.redeploy_failed"} {
		for _, e := range h.Events(typ) {
			ended["event:"+itoa(e.ID)] = e.Actor
		}
	}
	for i := 1; i < len(rows); i++ {
		if ended[rows[i].CreatedBy] != "job:"+itoa(rows[i-1].ID) {
			t.Errorf("rotation #%d was created by %s, not by the end of #%d", rows[i].ID, rows[i].CreatedBy, rows[i-1].ID)
		}
	}
}

func cutOffStates(t *testing.T, mod *subscriptions.Module, link int64) string {
	t.Helper()
	c, ok, err := mod.Links.LatestCutOff(context.Background(), link)
	if err != nil || !ok {
		t.Fatalf("cut-off: %v %v", ok, err)
	}
	var out []string
	for _, it := range c.Items {
		out = append(out, it.State)
	}
	return strings.Join(out, ",")
}

func TestRealCutOffRotatesEveryServer(t *testing.T) {
	h, mod, _, alex, mom := cutOffSetup(t)
	ctx := context.Background()
	before := credentialsOf(t, h, mom)
	if len(before) != 3 {
		t.Fatalf("Mom gets %d servers", len(before))
	}
	if _, err := mod.Links.CutOff(ctx, alex, "admin"); err != nil {
		t.Fatal(err)
	}
	if l, _ := mod.Links.Get(ctx, alex); l.State != "disabled" {
		t.Errorf("Alex is %s right after Cut off", l.State)
	}
	h.WaitFor("three rotations", func() bool { return cutOffStates(t, mod, alex) == "done,done,done" })
	h.Drain()
	if n := len(h.Events("server.credentials_rotated")); n != 3 {
		t.Errorf("%d server.credentials_rotated", n)
	}
	chained(t, h)
	after := credentialsOf(t, h, mom)
	if len(after) != 3 {
		t.Fatalf("Mom gets %d servers after", len(after))
	}
	for i := range after {
		if after[i] == before[i] {
			t.Errorf("server %d still has its old credential for Mom: %s", i+1, after[i])
		}
	}
}

func TestRealCutOffWithUnreachableServer(t *testing.T) {
	h, mod, ids, alex, _ := cutOffSetup(t)
	ctx := context.Background()
	h.Unreachable(h.Server(ids[1]).IP, true)
	if _, err := mod.Links.CutOff(ctx, alex, "admin"); err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the chain to end", func() bool { return cutOffStates(t, mod, alex) == "done,failed,done" })
	h.Drain()
	if l, _ := mod.Links.Get(ctx, alex); l.State != "disabled" {
		t.Errorf("Alex is %s", l.State)
	}
	c, _, _ := mod.Links.LatestCutOff(ctx, alex)
	if c.Items[1].ServerID != ids[1] || c.Items[1].Error == "" {
		t.Errorf("the failed item: %+v", c.Items[1])
	}
	rotated := map[string]bool{}
	for _, e := range h.Events("server.credentials_rotated") {
		rotated[e.Subject.ID] = true
	}
	if !rotated[itoa(ids[0])] || rotated[itoa(ids[1])] || !rotated[itoa(ids[2])] {
		t.Errorf("rotated: %v", rotated)
	}
	// The link page offers Retry for it; once reachable, Retry rotates it.
	body := h.Login.Get("/links/" + itoa(alex)).Body.String()
	if !strings.Contains(body, "/links/"+itoa(alex)+"/cutoff/2/retry") {
		t.Error("the link page offers no Retry for the failed server")
	}
	h.Unreachable(h.Server(ids[1]).IP, false)
	if err := mod.Links.RetryCutOff(ctx, alex, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	h.WaitFor("the retry", func() bool { return cutOffStates(t, mod, alex) == "done,done,done" })
}
