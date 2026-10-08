package refresh_test

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/refresh"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/sources"
)

func TestAcceptedChangeMarksAndDigests(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	id := h.Upstream("v2fly:anthropic")
	h.List("Main", id)
	marks := len(h.Marks.Changes())

	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\nclaude.com\nfull:api.anthropic.com\nanthropic.ai\n")
	d := round(t, h)

	acc, err := h.Mod.Services.Accepted(bg, id)
	if err != nil || acc.Added != 3 || acc.Removed != 0 || acc.Count() != 5 || !acc.InRound {
		t.Fatalf("accepted: %+v %v", acc, err)
	}
	if n := snapshots(t, h, id); n["accepted"] != 1 || n["superseded"] != 1 {
		t.Errorf("snapshots: %v", n)
	}
	ev := h.Events("routing.snapshot_accepted")
	if len(ev) != 1 || num(ev[0].Payload["added"]) != 3 || ev[0].Payload["forced"] != false || ev[0].Payload["in_round"] != true ||
		num(ev[0].Payload["suffix"]) != 4 || num(ev[0].Payload["exact"]) != 1 {
		t.Errorf("accepted event: %+v", ev)
	}
	ch := h.Marks.Changes()
	if len(ch) != marks+1 || !slices.Equal(ch[len(ch)-1].Services, []int64{id}) || ch[len(ch)-1].Why != "anthropic changed upstream" {
		t.Errorf("marks: %+v", ch[marks:])
	}
	if num(d.Payload["changed"]) != 1 || num(d.Payload["added"]) != 3 || num(d.Payload["removed"]) != 0 ||
		num(d.Payload["services"]) != 1 || d.Subject.String() != "routing:refresh" || !notifies(t, h, d) {
		t.Errorf("digest: %+v", d)
	}
}

func TestUnchangedIsBookkeeping(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\nclaude.ai\n")
	id := h.Upstream("v2fly:anthropic")
	h.List("Main", id)
	var n0 int
	_ = h.App.DB.R.Get(&n0, `SELECT count(*) FROM events`)

	out, err := h.Mod.Refresh.Refresh(bg, id, false, "admin")
	if err != nil || out.Result != refresh.Same {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	var n1 int
	_ = h.App.DB.R.Get(&n1, `SELECT count(*) FROM events`)
	if n1 != n0 {
		t.Errorf("an unchanged refresh recorded %d events", n1-n0)
	}
	if n := snapshots(t, h, id); n["accepted"] != 1 || len(n) != 1 {
		t.Errorf("snapshots: %v", n)
	}
	if f, _, checked := failures(t, h, id); f != 0 || !checked {
		t.Errorf("bookkeeping: failures %d checked %v", f, checked)
	}
	d := round(t, h)
	if num(d.Payload["changed"])+num(d.Payload["rejected"])+num(d.Payload["failing"])+num(d.Payload["still_failing"]) != 0 || notifies(t, h, d) {
		t.Errorf("digest: %+v", d.Payload)
	}
}

func TestEmptyIsRejected(t *testing.T) {
	h := routingtest.New(t)
	u := h.Up.File("lists/mine.txt", "example.com\nexample.org\n")
	id := h.Upstream(u)
	h.List("Main", id)
	marks := len(h.Marks.Changes())

	h.Up.File("lists/mine.txt", "")
	d := round(t, h)
	if n := snapshots(t, h, id); n["rejected"] != 1 || n["accepted"] != 1 {
		t.Fatalf("snapshots: %v", n)
	}
	ev := h.Events("routing.snapshot_rejected")
	if len(ev) != 1 || ev[0].Payload["reason"] != "empty" || num(ev[0].Payload["old_count"]) != 2 || num(ev[0].Payload["new_count"]) != 0 {
		t.Errorf("rejected: %+v", ev)
	}
	if len(h.Marks.Changes()) != marks {
		t.Error("a rejection marked targets")
	}
	if acc, _ := h.Mod.Services.Accepted(bg, id); acc.Count() != 2 {
		t.Error("the accepted snapshot changed")
	}
	if num(d.Payload["rejected"]) != 1 || num(d.Payload["changed"]) != 0 || !notifies(t, h, d) {
		t.Errorf("digest: %+v", d.Payload)
	}
	if st, _ := h.Mod.Refresh.State(bg, id); st != "waiting" {
		t.Errorf("state %q", st)
	}
}

func TestShrinkAndAcceptAnyway(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", names("nf", 0, 120))
	id := h.Upstream("v2fly:netflix")
	h.List("Main", id)
	marks := len(h.Marks.Changes())

	h.Up.V2fly("netflix", names("nf", 0, 50))
	out, err := h.Mod.Refresh.Refresh(bg, id, false, "admin")
	if err != nil || out.Result != refresh.Rejected || out.Removed != 70 {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	w, ok, err := h.Mod.Refresh.Waiting(bg, id)
	if err != nil || !ok || w.Reason != "shrink" || w.LostPct != 58 || w.Count() != 50 || w.InRound {
		t.Fatalf("waiting: %+v %v %v", w, ok, err)
	}
	if len(h.Marks.Changes()) != marks {
		t.Error("a rejection marked targets")
	}

	if err := h.Mod.Refresh.Accept(bg, id, w.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	acc, _ := h.Mod.Services.Accepted(bg, id)
	if acc.ID != w.ID || acc.Count() != 50 || acc.ForcedBy != "admin" || acc.Removed != 70 || acc.Reason != "shrink" {
		t.Errorf("accepted: %+v", acc)
	}
	ev := h.Events("routing.snapshot_accepted")
	if len(ev) != 1 || ev[0].Payload["forced"] != true || num(ev[0].Payload["removed"]) != 70 {
		t.Errorf("event: %+v", ev)
	}
	if ch := h.Marks.Changes(); len(ch) != marks+1 || !slices.Equal(ch[len(ch)-1].Services, []int64{id}) {
		t.Errorf("marks: %+v", ch)
	}
	if n := snapshots(t, h, id); n["accepted"] != 1 || n["superseded"] != 1 || n["rejected"] != 0 {
		t.Errorf("snapshots: %v", n)
	}
	if err := h.Mod.Refresh.Accept(bg, id, w.ID, "admin"); !errors.Is(err, refresh.ErrNotWaiting) {
		t.Errorf("accepting it again: %v", err)
	}
	if err := h.Mod.Refresh.Dismiss(bg, id, w.ID, "admin"); !errors.Is(err, refresh.ErrNotWaiting) {
		t.Errorf("dismissing an accepted one: %v", err)
	}
}

func TestShrinkFloor(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("small", names("s", 0, 12))
	id := h.Upstream("v2fly:small")
	h.Up.V2fly("small", names("s", 0, 3))
	out, err := h.Mod.Refresh.Refresh(bg, id, false, "admin")
	if err != nil || out.Result != refresh.Accepted || out.Removed != 9 {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	if acc, _ := h.Mod.Services.Accepted(bg, id); acc.Count() != 3 {
		t.Errorf("accepted %d names", acc.Count())
	}
}

func TestFailedIncludeKeepsSnapshot(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("cn", "include:geolocation-cn\nexample.cn\n")
	h.Up.V2fly("geolocation-cn", "baidu.com\nqq.com\n")
	id := h.Upstream("v2fly:cn")
	h.Up.Fail("/v2fly/data/geolocation-cn", http.StatusNotFound)

	out, err := h.Mod.Refresh.Refresh(bg, id, false, "admin")
	var ie *sources.IncludeError
	if err != nil || out.Result != refresh.Failed || !errors.As(out.Err, &ie) {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	if n := snapshots(t, h, id); n["accepted"] != 1 || len(n) != 1 {
		t.Errorf("snapshots: %v", n)
	}
	if acc, _ := h.Mod.Services.Accepted(bg, id); acc.Count() != 3 {
		t.Errorf("accepted %d names", acc.Count())
	}
	if f, e, checked := failures(t, h, id); f != 1 || e != "include:geolocation-cn: HTTP 404" || checked {
		t.Errorf("failures %d %q checked %v", f, e, checked)
	}
	if st, _ := h.Mod.Refresh.State(bg, id); st != "failing" {
		t.Errorf("state %q", st)
	}
}

func TestThirdFailureNotifiesOnce(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	h.Up.V2fly("b", "b.com\n")
	a, b := h.Upstream("v2fly:a"), h.Upstream("v2fly:b")
	h.List("Main", a, b)
	h.Up.Fail("/v2fly/data/a", http.StatusBadGateway)
	h.Up.Fail("/v2fly/data/b", http.StatusBadGateway)

	for day := 1; day <= 4; day++ {
		d := round(t, h)
		want := 0
		if day >= 3 {
			want = 2
		}
		if n := len(h.Events("routing.refresh_failing")); n != want {
			t.Errorf("day %d: %d refresh_failing", day, n)
		}
		started, still := 0, 2
		if day == 1 {
			started, still = 2, 0
		}
		if num(d.Payload["failing"]) != started || num(d.Payload["still_failing"]) != still {
			t.Errorf("day %d digest: %+v", day, d.Payload)
		}
		h.Advance(24 * time.Hour)
	}
	ev := h.Events("routing.refresh_failing")
	if num(ev[0].Payload["failures"]) != 3 || ev[0].Payload["error"] != "HTTP 502" || !notifies(t, h, ev[0]) {
		t.Errorf("refresh_failing: %+v", ev[0])
	}
	for _, id := range []int64{a, b} {
		if n := snapshots(t, h, id); n["accepted"] != 1 || len(n) != 1 {
			t.Errorf("snapshots of %d: %v", id, n)
		}
	}

	// a success resets; the next run of failures notifies again at its third
	h.Up.Fail("/v2fly/data/a", 0)
	if out, err := h.Mod.Refresh.Refresh(bg, a, false, "admin"); err != nil || out.Result != refresh.Same {
		t.Fatalf("recovery: %+v %v", out, err)
	}
	if f, e, _ := failures(t, h, a); f != 0 || e != "" {
		t.Errorf("after a success: %d %q", f, e)
	}
	h.Up.Fail("/v2fly/data/a", http.StatusBadGateway)
	for i := 0; i < 3; i++ {
		if _, err := h.Mod.Refresh.Refresh(bg, a, false, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.Events("routing.refresh_failing")); n != 3 {
		t.Errorf("a new run of failures: %d events", n)
	}
}

func TestPinnedPortalDown(t *testing.T) {
	h := routingtest.New(t)
	h.Up.Site("beta", "apple", "apple.com", "apple.com", "icloud.com")
	id := h.Upstream("iplist:beta:apple")
	h.Up.PortalDown("beta", true)
	out, err := h.Mod.Refresh.Refresh(bg, id, false, "admin")
	var ue *sources.UnreachableError
	if err != nil || out.Result != refresh.Failed || !errors.As(out.Err, &ue) {
		t.Fatalf("refresh: %+v %v", out, err)
	}
	if acc, _ := h.Mod.Services.Accepted(bg, id); acc.Count() != 2 {
		t.Errorf("the snapshot changed: %d", acc.Count())
	}
	if f, e, _ := failures(t, h, id); f != 1 || e != "beta unreachable" {
		t.Errorf("failures %d %q", f, e)
	}

	// an unpinned selector found on main stays on main: a miss there fails
	h.Up.Site("main", "video", "youtube.com", "youtube.com")
	yt := h.Upstream("iplist:youtube.com")
	h.Up.Site("beta", "video", "youtube.com", "youtube.com", "ytimg.com")
	h.Up.PortalDown("main", true)
	if out, _ := h.Mod.Refresh.Refresh(bg, yt, false, "admin"); out.Result != refresh.Failed {
		t.Errorf("a main miss switched portals: %+v", out)
	}
}

func TestRejectionOutsideRoundNotifies(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", names("nf", 0, 100))
	id := h.Upstream("v2fly:netflix")
	h.List("Main", id)
	h.Up.V2fly("netflix", names("nf", 0, 10))

	h.StartJobs()
	job, err := h.Mod.Refresh.RefreshNow(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("job: %s %s", st, e)
	}
	ev := h.Events("routing.snapshot_rejected")
	if len(ev) != 1 || ev[0].Payload["in_round"] != false || ev[0].Actor != "job:"+itoa(job) || !notifies(t, h, ev[0]) {
		t.Fatalf("Refresh now: %+v", ev)
	}
	if len(h.Events("routing.refresh_digest")) != 0 {
		t.Error("Refresh now recorded a digest")
	}

	// in the round the same kind of rejection waits for the digest
	h.Up.V2fly("netflix", names("nf", 0, 5))
	round(t, h)
	ev = h.Events("routing.snapshot_rejected")
	if len(ev) != 2 || ev[1].Payload["in_round"] != true || notifies(t, h, ev[1]) {
		t.Errorf("in the round: %+v", ev)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestWaitingRejection(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("netflix", names("nf", 0, 100))
	id := h.Upstream("v2fly:netflix")
	h.Up.V2fly("netflix", names("nf", 0, 10))

	for i := 0; i < 2; i++ {
		if out, err := h.Mod.Refresh.Refresh(bg, id, false, "admin"); err != nil || out.Result != refresh.Rejected {
			t.Fatalf("refresh %d: %+v %v", i, out, err)
		}
	}
	if n := len(h.Events("routing.snapshot_rejected")); n != 1 {
		t.Errorf("the same rejection twice: %d events", n)
	}
	if n := snapshots(t, h, id); n["rejected"] != 1 {
		t.Errorf("snapshots: %v", n)
	}

	// upstream comes back as it was: the waiting rejection ends by itself
	h.Up.V2fly("netflix", names("nf", 0, 100))
	if out, _ := h.Mod.Refresh.Refresh(bg, id, false, "admin"); out.Result != refresh.Same {
		t.Fatalf("back: %+v", out)
	}
	ev := h.Events("routing.snapshot_dismissed")
	if len(ev) != 1 || ev[0].Payload["automatic"] != true || num(ev[0].Payload["new_count"]) != 10 {
		t.Errorf("dismissed: %+v", ev)
	}
	if _, ok, _ := h.Mod.Refresh.Waiting(bg, id); ok {
		t.Error("still waiting")
	}
	if st, _ := h.Mod.Refresh.State(bg, id); st != "ok" {
		t.Errorf("state %q", st)
	}

	// Dismiss ends one by hand; the next refresh checks again
	h.Up.V2fly("netflix", names("nf", 0, 20))
	if out, _ := h.Mod.Refresh.Refresh(bg, id, false, "admin"); out.Result != refresh.Rejected {
		t.Fatal("no second rejection")
	}
	w, ok, _ := h.Mod.Refresh.Waiting(bg, id)
	if !ok {
		t.Fatal("nothing waits")
	}
	if err := h.Mod.Refresh.Dismiss(bg, id, w.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	ev = h.Events("routing.snapshot_dismissed")
	if len(ev) != 2 || ev[1].Payload["automatic"] != false || ev[1].Actor != "admin" {
		t.Errorf("dismissed by hand: %+v", ev)
	}
	if st, _ := h.Mod.Refresh.State(bg, id); st != "ok" {
		t.Errorf("state after Dismiss %q", st)
	}
	if err := h.Mod.Refresh.Dismiss(bg, id, w.ID, "admin"); !errors.Is(err, refresh.ErrNotWaiting) {
		t.Errorf("dismissing twice: %v", err)
	}
	if out, _ := h.Mod.Refresh.Refresh(bg, id, false, "admin"); out.Result != refresh.Rejected || len(h.Events("routing.snapshot_rejected")) != 3 {
		t.Error("after Dismiss the next refresh checks again")
	}
}
