package links_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/substest"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// threeServers: nl-1, de-1, fi-1 in that order in "Family", and a link "Alex".
func threeServers(t *testing.T) (*substest.Harness, int64) {
	t.Helper()
	h := substest.New(t)
	h.Catalog.Put(substest.Server(3, "fi-1", "🇫🇮", "Finland", 1))
	sub := h.Subscription("Family", 1, 2, 3)
	id, _ := h.Link(sub, "Alex")
	return h, id
}

// states is the items' states in order, "skipped" as "skipped:<reason>" and
// failures as "failed:<error>".
func states(t *testing.T, h *substest.Harness, linkID int64) ([]string, links.CutOff) {
	t.Helper()
	c, ok, err := h.Mod.Links.LatestCutOff(bg, linkID)
	if err != nil || !ok {
		t.Fatalf("latest cut-off: %v %v", ok, err)
	}
	var out []string
	for _, it := range c.Items {
		s := it.State
		if it.Error != "" {
			s += ":" + it.Error
		}
		out = append(out, s)
	}
	return out, c
}

func wantStates(t *testing.T, h *substest.Harness, linkID int64, want ...string) links.CutOff {
	t.Helper()
	got, c := states(t, h, linkID)
	if len(got) != len(want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items %v, want %v", got, want)
		}
	}
	return c
}

// queued counts the fake rotations waiting in the job queue.
func queued(t *testing.T, h *substest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE type = ? AND state = 'queued'`, substest.FakeRotate); err != nil {
		t.Fatal(err)
	}
	return n
}

// finish ends the running rotation as the real job would: the job row ends
// and the event is fed to the subscriber.
func finish(t *testing.T, h *substest.Harness, it links.CutOffItem, failure string) {
	t.Helper()
	h.Exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, it.JobID)
	if failure == "" {
		h.Rotated(it.JobID, it.ServerID)
	} else {
		h.RotationFailed(it.JobID, it.ServerID, failure)
	}
}

func running(t *testing.T, c links.CutOff) links.CutOffItem {
	t.Helper()
	for _, it := range c.Items {
		if it.State == "running" {
			return it
		}
	}
	t.Fatalf("nothing runs: %+v", c.Items)
	return links.CutOffItem{}
}

func TestCutOffRunsOneAtATime(t *testing.T) {
	h, id := threeServers(t)
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if l, _ := h.Mod.Links.Get(bg, id); l.State != "disabled" {
		t.Errorf("the link is %s", l.State)
	}
	if len(h.Events("link.disabled")) != 1 || len(h.Events("link.cut_off")) != 1 {
		t.Errorf("events: disabled %d, cut_off %d", len(h.Events("link.disabled")), len(h.Events("link.cut_off")))
	}
	if p := h.Events("link.cut_off")[0].Payload; p["servers"] != "nl-1, de-1, fi-1" || p["skipped"] != "" {
		t.Errorf("link.cut_off %+v", p)
	}
	c := wantStates(t, h, id, "running", "waiting", "waiting")
	if queued(t, h) != 1 {
		t.Fatalf("queued rotations: %d", queued(t, h))
	}
	// The job is the rotator's request for the first server.
	j, err := h.App.Jobs.Job(bg, c.Items[0].JobID)
	if err != nil || j.ResourceKey != "server:1" || j.CreatedBy != "admin" {
		t.Errorf("job %+v %v", j, err)
	}

	finish(t, h, running(t, c), "")
	c = wantStates(t, h, id, "done", "running", "waiting")
	if j, _ := h.App.Jobs.Job(bg, c.Items[1].JobID); j.ResourceKey != "server:2" || j.CreatedBy[:6] != "event:" {
		t.Errorf("second job %+v", j)
	}
	finish(t, h, running(t, c), "")
	c = wantStates(t, h, id, "done", "done", "running")
	finish(t, h, running(t, c), "")
	wantStates(t, h, id, "done", "done", "done")
	if queued(t, h) != 0 {
		t.Errorf("queued after the last: %d", queued(t, h))
	}
}

func TestCutOffContinuesAfterFailure(t *testing.T) {
	h, id := threeServers(t)
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	_, c := states(t, h, id)
	finish(t, h, running(t, c), "")
	_, c = states(t, h, id)
	finish(t, h, running(t, c), "proxy test failed: timeout")
	c = wantStates(t, h, id, "done", "failed:proxy test failed: timeout", "running")
	if c.Items[2].ServerID != 3 {
		t.Errorf("third: %+v", c.Items[2])
	}
	// A cancelled rotation is a failure named "cancelled".
	h.Exec(`UPDATE jobs SET state = 'cancelled' WHERE id = ?`, c.Items[2].JobID)
	h.Feed(events.Event{Type: "server.redeploy_failed", Actor: "job:" + itoa(c.Items[2].JobID),
		Payload: map[string]any{"kind": "rotate", "error": "context canceled", "cancelled": true}})
	wantStates(t, h, id, "done", "failed:proxy test failed: timeout", "failed:cancelled")
}

func TestCutOffRetry(t *testing.T) {
	h, id := threeServers(t)
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	_, c := states(t, h, id)
	finish(t, h, running(t, c), "")
	_, c = states(t, h, id)
	finish(t, h, running(t, c), "ssh: connection refused")
	c = wantStates(t, h, id, "done", "failed:ssh: connection refused", "running")
	third := running(t, c)

	// Only a failed one can be retried.
	if err := h.Mod.Links.RetryCutOff(bg, id, 1, "admin"); !errors.Is(err, links.ErrNotRetryable) {
		t.Errorf("retrying a done one: %v", err)
	}
	if err := h.Mod.Links.RetryCutOff(bg, id, 2, "admin"); err != nil {
		t.Fatal(err)
	}
	wantStates(t, h, id, "done", "waiting", "running")
	if queued(t, h) != 1 {
		t.Errorf("queued while the third runs: %d", queued(t, h))
	}
	finish(t, h, third, "")
	c = wantStates(t, h, id, "done", "running", "done")
	finish(t, h, running(t, c), "")
	wantStates(t, h, id, "done", "done", "done")
}

func TestCutOffSkipsUnrotatable(t *testing.T) {
	h, id := threeServers(t)
	h.Rotator.Refuse(1, servers.ErrNotActive)
	h.Rotator.Refuse(3, servers.ErrNothingToRotate)
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	c := wantStates(t, h, id, "skipped:"+links.ReasonNotInService, "running", "skipped:"+links.ReasonNothing)
	if p := h.Events("link.cut_off")[0].Payload; p["servers"] != "de-1" || p["skipped"] != "nl-1, fi-1" {
		t.Errorf("link.cut_off %+v", p)
	}
	finish(t, h, running(t, c), "")
	wantStates(t, h, id, "skipped:"+links.ReasonNotInService, "done", "skipped:"+links.ReasonNothing)

	// A server that stops being rotatable while it waits is skipped when its
	// turn comes, and the chain goes on.
	h2, id2 := threeServers(t)
	if _, err := h2.Mod.Links.CutOff(bg, id2, "admin"); err != nil {
		t.Fatal(err)
	}
	h2.Rotator.Refuse(2, servers.ErrNotActive)
	_, c = states(t, h2, id2)
	finish(t, h2, running(t, c), "")
	wantStates(t, h2, id2, "done", "skipped:"+links.ReasonNotInService, "running")

	// Nothing to rotate at all: the link is only disabled.
	h3, id3 := threeServers(t)
	for _, s := range []int64{1, 2, 3} {
		h3.Rotator.Refuse(s, servers.ErrNotActive)
	}
	if _, err := h3.Mod.Links.CutOff(bg, id3, "admin"); err != nil {
		t.Fatal(err)
	}
	wantStates(t, h3, id3, "skipped:"+links.ReasonNotInService, "skipped:"+links.ReasonNotInService, "skipped:"+links.ReasonNotInService)
	if l, _ := h3.Mod.Links.Get(bg, id3); l.State != "disabled" || queued(t, h3) != 0 {
		t.Errorf("link %s, queued %d", l.State, queued(t, h3))
	}
}

func TestCutOffEventsAreIdempotent(t *testing.T) {
	h, id := threeServers(t)
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	_, c := states(t, h, id)
	first := running(t, c)
	finish(t, h, first, "")
	before, c := states(t, h, id)
	second := running(t, c)

	// The same event again, an event of a job no item holds, a failure of
	// another kind of change, an actor that isn't a job: nothing changes.
	h.Rotated(first.JobID, first.ServerID)
	h.RotationFailed(first.JobID, first.ServerID, "late")
	h.Rotated(9999, 2)
	h.Feed(events.Event{Type: "server.redeploy_failed", Actor: "job:" + itoa(second.JobID), Payload: map[string]any{"kind": "redeploy", "error": "x"}})
	h.Feed(events.Event{Type: "server.credentials_rotated", Actor: "admin"})
	after, _ := states(t, h, id)
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("items changed: %v → %v", before, after)
		}
	}
	if queued(t, h) != 1 {
		t.Errorf("queued: %d", queued(t, h))
	}
}

func TestCutOffDisabledLink(t *testing.T) {
	h, id := threeServers(t)
	if err := h.Mod.Links.Disable(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.Events("link.disabled")); n != 1 {
		t.Errorf("link.disabled recorded %d times", n)
	}
	wantStates(t, h, id, "running", "waiting", "waiting")

	// A deleted link can't be cut off.
	if err := h.Mod.Links.Delete(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Links.CutOff(bg, id, "admin"); !errors.Is(err, links.ErrDeleted) {
		t.Errorf("cutting off a deleted link: %v", err)
	}
	if _, err := h.Mod.Links.PlanCutOff(bg, id); !errors.Is(err, links.ErrDeleted) {
		t.Errorf("planning a deleted link: %v", err)
	}
}

func TestCutOffPlan(t *testing.T) {
	h, id := threeServers(t)
	h.Catalog.SetHealth(2, "blocked", substest.HealthySince)
	h.Rotator.Refuse(3, servers.ErrNothingToRotate)
	// Other links: two in Family, one in Mine (holds de-1), one in Other (holds
	// only fi-1, which won't rotate), and ones that don't count.
	h.Link(h.Subscription("Mine", 2), "Me")
	fam := h.Subscription("Family 2", 1)
	h.Link(fam, "Dad")
	disabled, _ := h.Link(fam, "Old")
	if err := h.Mod.Links.Disable(bg, disabled, "admin"); err != nil {
		t.Fatal(err)
	}
	expired, _ := h.Link(fam, "Expired")
	if err := h.Mod.Links.SetExpiry(bg, expired, h.Now.Add(1), "admin"); err != nil {
		t.Fatal(err)
	}
	h.Link(h.Subscription("Other", 3), "Nobody")
	l, _ := h.Mod.Links.Get(bg, id)
	h.Link(l.SubscriptionID, "Mom")
	h.Advance(2)

	p, err := h.Mod.Links.PlanCutOff(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rotate) != 2 || p.Rotate[0].Name != "nl-1" || p.Rotate[0].Health != "healthy" || p.Rotate[1].Name != "de-1" || p.Rotate[1].Health != "blocked" {
		t.Errorf("rotate %+v", p.Rotate)
	}
	if len(p.Skip) != 1 || p.Skip[0].Name != "fi-1" || p.Skip[0].Reason != links.ReasonNothing {
		t.Errorf("skip %+v", p.Skip)
	}
	// Mom (Family), Me (Mine), Dad (Family 2).
	if p.OtherLinks != 3 || p.OtherSubs != 3 {
		t.Errorf("other links %d in %d subscriptions", p.OtherLinks, p.OtherSubs)
	}
	if queued(t, h) != 0 || len(h.Events("link.cut_off")) != 0 {
		t.Error("the plan started something")
	}

	// A server out of service: not in service, without health.
	h.Catalog.Drop(1)
	h.Rotator.Refuse(1, servers.ErrNotActive)
	p, _ = h.Mod.Links.PlanCutOff(bg, id)
	if len(p.Skip) != 2 || p.Skip[0].Name != "nl-1" || p.Skip[0].Reason != links.ReasonNotInService || p.Skip[0].Health != "" {
		t.Errorf("skip %+v", p.Skip)
	}

	// Without a rotator there is no cut-off.
	h4 := substest.New(t, substest.NoRotator())
	id4, _ := h4.Link(h4.Subscription("Family", 1), "Alex")
	if _, err := h4.Mod.Links.PlanCutOff(bg, id4); !errors.Is(err, links.ErrNoRotator) {
		t.Errorf("plan without a rotator: %v", err)
	}
	if h4.Mod.Links.CanCutOff() {
		t.Error("CanCutOff without a rotator")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
