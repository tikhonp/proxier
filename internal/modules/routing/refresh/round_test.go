package refresh_test

import (
	"slices"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
)

func TestRoundSkipsServicesInNoList(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("listed", "listed.com\n")
	h.Up.V2fly("loose", "loose.com\n")
	listed, loose := h.Upstream("v2fly:listed"), h.Upstream("v2fly:loose")
	h.Custom("mine", "mine.com")
	h.List("Main", listed)
	before := h.Up.Requests("/v2fly/data/loose")

	d := round(t, h)
	if _, _, checked := failures(t, h, listed); !checked {
		t.Error("the listed service wasn't refreshed")
	}
	if _, _, checked := failures(t, h, loose); checked || h.Up.Requests("/v2fly/data/loose") != before {
		t.Error("a service in no list was refreshed by the round")
	}
	if num(d.Payload["services"]) != 1 {
		t.Errorf("digest services: %v", d.Payload["services"])
	}

	job, err := h.Mod.Refresh.RefreshNow(bg, loose, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if st, _ := h.Job(job); st != "succeeded" {
		t.Errorf("Refresh now: %s", st)
	}
	if _, _, checked := failures(t, h, loose); !checked {
		t.Error("Refresh now didn't refresh a service in no list")
	}
}

func TestRoundResumes(t *testing.T) {
	h := routingtest.New(t)
	var ids []int64
	for _, n := range []string{"a", "b", "c"} {
		h.Up.V2fly(n, n+".com\n")
		ids = append(ids, h.Upstream("v2fly:"+n))
	}
	h.List("Main", ids...)
	for _, n := range []string{"a", "b", "c"} {
		h.Up.V2fly(n, n+".com\n"+n+".net\n")
	}
	h.Up.Hang("/v2fly/data/c", true)

	h.StartJobs()
	job := h.RunSchedule("routing.refresh_round")
	deadline := time.Now().Add(20 * time.Second)
	for len(h.Events("routing.snapshot_accepted")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the first two services weren't refreshed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.StopJobs() // a restart while c hangs
	if st, _ := h.Job(job); st != "interrupted" {
		t.Fatalf("job after the stop: %s", st)
	}
	if f, _, _ := failures(t, h, ids[2]); f != 0 {
		t.Error("a cut-short refresh counted as a failure")
	}

	h.Up.Hang("/v2fly/data/c", false)
	h.StartJobs()
	h.Drain()
	if st, e := h.Job(job); st != "succeeded" {
		t.Fatalf("resumed job: %s %s", st, e)
	}
	ev := h.Events("routing.snapshot_accepted")
	var subjects []string
	for _, e := range ev {
		subjects = append(subjects, e.Subject.String())
	}
	if !slices.Equal(subjects, []string{"service:1", "service:2", "service:3"}) {
		t.Errorf("accepted events: %v", subjects)
	}
	d := h.Events("routing.refresh_digest")
	if len(d) != 1 || num(d[0].Payload["changed"]) != 3 || num(d[0].Payload["added"]) != 3 {
		t.Errorf("digest: %+v", d)
	}
}

func TestRoundMarksEachChange(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("a", "a.com\n")
	h.Up.V2fly("b", "b.com\n")
	a, b := h.Upstream("v2fly:a"), h.Upstream("v2fly:b")
	h.List("Main", a, b)
	marks := len(h.Marks.Changes())
	h.Up.V2fly("a", "a.com\na.net\n")
	h.Up.V2fly("b", "b.com\nb.net\n")
	round(t, h)
	ch := h.Marks.Changes()[marks:]
	if len(ch) != 2 || !slices.Equal(ch[0].Services, []int64{a}) || !slices.Equal(ch[1].Services, []int64{b}) {
		t.Errorf("marks: %+v", ch)
	}
	for _, c := range ch {
		if c.Actor == "" || c.Actor[:4] != "job:" {
			t.Errorf("mark actor %q", c.Actor)
		}
	}
}
