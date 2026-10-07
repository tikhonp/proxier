package deploy_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// fleet provisions n servers (nl-1 … nl-n) on one fake VPS and publishes a
// second version as the default.
func fleet(t *testing.T, n int, v2 func(map[string][]byte)) (*serverstest.Harness, []int64, int) {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	ids := []int64{h.Provisioned()}
	for i := 1; i < n; i++ {
		ids = append(ids, h.AddServer(fmt.Sprintf("10.77.0.%d", i+1)))
	}
	if v2 == nil {
		v2 = change("site/index.html", "</body>", "<!-- v2 --></body>")
	}
	return h, ids, h.PublishVersion(v2, true)
}

func waitRollout(t *testing.T, h *serverstest.Harness, id int64) store.Rollout {
	t.Helper()
	var ro store.Rollout
	h.WaitFor("the rollout to finish", func() bool {
		var err error
		ro, err = store.GetRollout(bg, h.App.DB.R, id)
		return err == nil && !ro.FinishedAt.IsZero()
	})
	return ro
}

func items(t *testing.T, h *serverstest.Harness, id int64) []store.RolloutItem {
	t.Helper()
	its, err := store.RolloutItems(bg, h.App.DB.R, id)
	if err != nil {
		t.Fatal(err)
	}
	return its
}

func states(its []store.RolloutItem) string {
	var s []string
	for _, it := range its {
		s = append(s, it.State)
	}
	return strings.Join(s, " ")
}

func finishedEvents(h *serverstest.Harness) []events.Event {
	return h.Events("server.rollout_finished")
}

func TestRolloutStopsAtFirstFailure(t *testing.T) {
	h, ids, _ := fleet(t, 5, nil)
	// The third server's proxy test fails.
	h.ProxyAccepts(func(e endpoint.Endpoint) bool { return !strings.HasPrefix(e.Host, "nl-3.") })
	id, err := h.Mod.Deploy.StartRollout(bg, ids, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ro := waitRollout(t, h, id)
	if ro.State != "stopped" {
		t.Fatalf("rollout %s", ro.State)
	}
	its := items(t, h, id)
	if got := states(its); got != "done done failed waiting waiting" {
		t.Fatalf("items: %s", got)
	}
	if !strings.Contains(its[2].Error, "proxy test") {
		t.Errorf("the failure: %q", its[2].Error)
	}
	for i, want := range []int{2, 2, 1, 1, 1} {
		if v := h.Server(ids[i]).TemplateVersion; v != want {
			t.Errorf("nl-%d is at v%d, want v%d", i+1, v, want)
		}
	}
	fin := finishedEvents(h)
	if len(fin) != 1 || fin[0].Payload["state"] != "stopped" || fin[0].Payload["done"] != float64(2) || fin[0].Payload["failed"] != float64(1) || fin[0].Payload["not_started"] != float64(2) {
		t.Fatalf("finished events: %+v", fin)
	}
	if n := jobCount(t, h, deploy.JobDeploy); n != 3 {
		t.Errorf("%d deploy jobs ran, want 3", n)
	}
}

func TestRolloutRunsInNameOrder(t *testing.T) {
	h, ids, _ := fleet(t, 3, nil)
	// Handed over in reverse; the rollout still goes nl-1, nl-2, nl-3.
	id, err := h.Mod.Deploy.StartRollout(bg, []int64{ids[2], ids[0], ids[1]}, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	ro := waitRollout(t, h, id)
	if ro.State != "done" {
		t.Fatalf("rollout %s", ro.State)
	}
	its := items(t, h, id)
	// An item's job exists only after the previous one recorded its success.
	recorded := map[string]time.Time{}
	for _, e := range h.Events("server.redeployed") {
		recorded[e.Actor] = e.Time.Time
	}
	var prev int64
	for i, it := range its {
		if it.ServerID != ids[i] || it.State != "done" {
			t.Fatalf("item %d: server %d %s", i, it.ServerID, it.State)
		}
		if it.JobID.Int64 <= prev {
			t.Errorf("item %d has job %d, not after job %d", i, it.JobID.Int64, prev)
		}
		if i > 0 {
			j, err := h.App.Jobs.Job(bg, it.JobID.Int64)
			if err != nil {
				t.Fatal(err)
			}
			if at := recorded[fmt.Sprintf("job:%d", prev)]; at.IsZero() || j.CreatedAt.Before(at) {
				t.Errorf("item %d's job was created at %s, before item %d recorded its success at %s", i, j.CreatedAt, i-1, at)
			}
		}
		prev = it.JobID.Int64
	}
	started := h.Events("server.rollout_started")
	if len(started) != 1 || started[0].Payload["to_version"] != float64(2) {
		t.Errorf("started events: %+v", started)
	}
	if fin := finishedEvents(h); len(fin) != 1 || fin[0].Payload["state"] != "done" || fin[0].Payload["done"] != float64(3) {
		t.Errorf("finished events: %+v", fin)
	}
}

func TestRolloutNeedsParameters(t *testing.T) {
	secret := "tok-0123456789-secret"
	h, ids, v2 := fleet(t, 2, all(
		addParam("api_token", true, "    secret: true\n"),
		change(".env", "SERVER_DOMAIN=", "API_TOKEN={{ .Params.api_token }}\nSERVER_DOMAIN="),
	))
	plan, err := h.Mod.Deploy.PlanRollout(bg, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0].Name != "nl-1" || plan[0].From != 1 || plan[0].To != v2 || len(plan[0].MissingParams) != 1 || plan[0].MissingParams[0] != "api_token" {
		t.Fatalf("plan: %+v", plan)
	}
	// Start is refused until every server has its value.
	_, err = h.Mod.Deploy.StartRollout(bg, ids, map[int64]map[string]string{ids[0]: {"api_token": secret}}, "admin")
	var fe deploy.FieldErrors
	if !errors.As(err, &fe) || fe[fmt.Sprintf("server.%d.param.api_token", ids[1])].Key != "servers.err.param_required" {
		t.Fatalf("start without a value: %v", err)
	}
	if n, _ := store.ListRollouts(bg, h.App.DB.R, 10); len(n) != 0 {
		t.Fatalf("a refused start left %d rollouts", len(n))
	}
	id, err := h.Mod.Deploy.StartRollout(bg, ids, map[int64]map[string]string{
		ids[0]: {"api_token": secret}, ids[1]: {"api_token": secret + "-2"},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if ro := waitRollout(t, h, id); ro.State != "done" {
		t.Fatalf("rollout %s", ro.State)
	}
	its := items(t, h, id)
	for i, it := range its {
		// The secret value is sealed, never stored in plain text.
		if bytes.Contains(it.ParamsSecret, []byte(secret)) || strings.Contains(it.Params, secret) || len(it.ParamsSecret) == 0 {
			t.Errorf("item %d stores its parameters wrongly: %q", i, it.Params)
		}
		got, err := sealed.OpenRolloutParams(h.App.Vault, id, it.Position, it.ParamsSecret)
		if err != nil || got["api_token"] == "" {
			t.Errorf("item %d: %v %v", i, got, err)
		}
		if strings.Contains(h.Log(it.JobID.Int64), secret) {
			t.Errorf("item %d: the secret is in the job log", i)
		}
	}
	srv := h.Server(ids[0])
	if got, _ := sealed.OpenParams(h.App.Vault, srv.ID, srv.ParamsSecret); got["api_token"] != secret {
		t.Errorf("the server's secret parameters: %v", got)
	}
	if strings.Contains(srv.Params, secret) {
		t.Error("the secret is in the public parameters")
	}
}

func TestStopRollout(t *testing.T) {
	h, ids, _ := fleet(t, 3, nil)
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	id, err := h.Mod.Deploy.StartRollout(bg, ids, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	if err := h.Mod.Deploy.StopRollout(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	// The running item is left to finish: no end yet, nothing new starts.
	ro, _ := store.GetRollout(bg, h.App.DB.R, id)
	if ro.State != "cancelled" || !ro.FinishedAt.IsZero() || len(finishedEvents(h)) != 0 {
		t.Fatalf("right after Stop: %+v, %d finished events", ro, len(finishedEvents(h)))
	}
	if err := h.Mod.Deploy.StopRollout(bg, id, "admin"); !errors.Is(err, deploy.ErrNotAllowed) {
		t.Errorf("stopping twice: %v", err)
	}
	release()
	ro = waitRollout(t, h, id)
	if ro.State != "cancelled" {
		t.Fatalf("rollout %s", ro.State)
	}
	if got := states(items(t, h, id)); got != "done waiting waiting" {
		t.Fatalf("items: %s", got)
	}
	fin := finishedEvents(h)
	if len(fin) != 1 || fin[0].Payload["state"] != "cancelled" || fin[0].Payload["done"] != float64(1) || fin[0].Payload["not_started"] != float64(2) {
		t.Fatalf("finished events: %+v", fin)
	}
	if n := jobCount(t, h, deploy.JobDeploy); n != 1 {
		t.Errorf("%d deploy jobs", n)
	}

	// With no item running, Stop ends the rollout at once.
	var other int64
	err = h.App.DB.Write(bg, func(tx *sqlxTx) error {
		var err error
		if other, err = store.InsertRollout(bg, tx, h.TemplateID, 2, "admin", dbNow()); err != nil {
			return err
		}
		return store.InsertRolloutItem(bg, tx, store.RolloutItem{RolloutID: other, Position: 0, ServerID: ids[0], FromVersion: 1, Params: "{}", State: "waiting"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Deploy.StopRollout(bg, other, "admin"); err != nil {
		t.Fatal(err)
	}
	ro, _ = store.GetRollout(bg, h.App.DB.R, other)
	if ro.State != "cancelled" || ro.FinishedAt.IsZero() {
		t.Fatalf("rollout without a running item: %+v", ro)
	}
	if fin := finishedEvents(h); len(fin) != 2 {
		t.Errorf("%d finished events", len(fin))
	}
}

func TestCancelledItemStopsRollout(t *testing.T) {
	h, ids, _ := fleet(t, 3, nil)
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	id, err := h.Mod.Deploy.StartRollout(bg, ids, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	its := items(t, h, id)
	if err := h.App.Jobs.Cancel(bg, its[0].JobID.Int64, "admin"); err != nil {
		t.Fatal(err)
	}
	release()
	ro := waitRollout(t, h, id)
	if ro.State != "stopped" {
		t.Fatalf("rollout %s", ro.State)
	}
	if got := states(items(t, h, id)); got != "failed waiting waiting" {
		t.Fatalf("items: %s", got)
	}
	if it := items(t, h, id)[0]; it.Error != "cancelled" {
		t.Errorf("item error %q", it.Error)
	}
	if fin := finishedEvents(h); len(fin) != 1 || fin[0].Payload["state"] != "stopped" {
		t.Fatalf("finished events: %+v", fin)
	}
}

func TestRolloutSubscriberIsIdempotent(t *testing.T) {
	h, ids, _ := fleet(t, 3, nil)
	h.StopDispatcher() // the test hands the events over itself
	id, err := h.Mod.Deploy.StartRollout(bg, ids, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	first := items(t, h, id)[0].JobID.Int64
	h.WaitFor("the first item's job", func() bool {
		j, err := h.App.Jobs.Job(bg, first)
		return err == nil && j.State == jobs.Succeeded
	})
	var done events.Event
	for _, e := range h.Events("server.redeployed") {
		if e.Actor == fmt.Sprintf("job:%d", first) {
			done = e
		}
	}
	if done.ID == 0 {
		t.Fatal("no server.redeployed for the first item")
	}
	deliver := func() {
		t.Helper()
		if err := h.App.DB.Write(bg, func(tx *sqlxTx) error { return h.Mod.Deploy.Subscriber().Handle(bg, tx, done) }); err != nil {
			t.Fatal(err)
		}
	}
	deliver()
	h.Drain()
	second := items(t, h, id)[1].JobID.Int64
	if second == 0 || second == first {
		t.Fatalf("the next item was not queued: %d", second)
	}
	jobsBefore := jobCount(t, h, deploy.JobDeploy)
	deliver() // delivered again
	deliver()
	if got := items(t, h, id)[1].JobID.Int64; got != second {
		t.Errorf("the second item's job changed from %d to %d", second, got)
	}
	if n := jobCount(t, h, deploy.JobDeploy); n != jobsBefore {
		t.Errorf("%d deploy jobs after the duplicates, was %d", n, jobsBefore)
	}
	if its := items(t, h, id); its[2].State != "waiting" {
		t.Errorf("items: %s", states(its))
	}
}

func TestRolloutSkipsCurrentServers(t *testing.T) {
	h, ids, v2 := fleet(t, 3, nil)
	// nl-2 is at the default version already.
	mustState(t, h, apply(t, h, ids[1], deploy.Target{Version: v2}), jobs.Succeeded)
	before := jobCount(t, h, deploy.JobDeploy)

	plan, err := h.Mod.Deploy.PlanRollout(bg, ids)
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Skip != "" || plan[1].Skip != "servers.rollout.skip.current" || plan[2].Skip != "" || plan[0].FilesChanged != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	id, err := h.Mod.Deploy.StartRollout(bg, ids, nil, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if ro := waitRollout(t, h, id); ro.State != "done" {
		t.Fatalf("rollout %s", ro.State)
	}
	its := items(t, h, id)
	if got := states(its); got != "done skipped done" {
		t.Fatalf("items: %s", got)
	}
	if its[1].Error != "servers.rollout.skip.current" {
		t.Errorf("skip reason %q", its[1].Error)
	}
	if n := jobCount(t, h, deploy.JobDeploy); n != before+2 {
		t.Errorf("%d deploy jobs, want %d", n, before+2)
	}
	if fin := finishedEvents(h); len(fin) != 1 || fin[0].Payload["skipped"] != float64(1) || fin[0].Payload["done"] != float64(2) {
		t.Errorf("finished events: %+v", fin)
	}
}
