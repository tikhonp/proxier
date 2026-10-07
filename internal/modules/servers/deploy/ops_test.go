package deploy_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// ran lists the ops of the commands the server got after the first n.
func ran(h *serverstest.Harness, n int) []remote.Op {
	var ops []remote.Op
	for _, c := range h.VPS.Commands()[n:] {
		if call, ok := remote.Parse(c[strings.Index(c, ": ")+2:]); ok {
			ops = append(ops, call.Op)
		}
	}
	return ops
}

func has(ops []remote.Op, want remote.Op) bool {
	for _, op := range ops {
		if op == want {
			return true
		}
	}
	return false
}

func TestRestartStack(t *testing.T) {
	h, id := stubbed(t)
	seen := len(h.VPS.Commands())
	job, err := h.Mod.Deploy.Restart(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	ops := ran(h, seen)
	restart, ps := -1, -1
	for i, op := range ops {
		if op == remote.OpComposeRestart && restart < 0 {
			restart = i
		}
		if op == remote.OpComposePS {
			ps = i
		}
	}
	if restart < 0 || ps < restart {
		t.Fatalf("expected a restart and then the self-check, ran %v", ops)
	}
	d := latest(t, h, id)
	if d.Kind != "restart" || d.State != "succeeded" || d.Uploaded || d.FilesChanged != 0 {
		t.Fatalf("deployment: %+v", d)
	}
	ev := h.Events("server.redeployed")
	if len(ev) != 1 || ev[0].Payload["kind"] != "restart" {
		t.Fatalf("events: %+v", ev)
	}
	// Only an active server can be restarted.
	if _, err := h.Mod.Deploy.Restart(bg, 9999, "admin"); err == nil {
		t.Error("restarted a server that does not exist")
	}
}

func TestUpdateImagesNoChange(t *testing.T) {
	h, id := stubbed(t)
	seen := len(h.VPS.Commands())
	job, err := h.Mod.Deploy.UpdateImages(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	ops := ran(h, seen)
	for _, want := range []remote.Op{remote.OpComposePull, remote.OpComposeUp, remote.OpComposeImages} {
		if !has(ops, want) {
			t.Errorf("%s did not run: %v", want, ops)
		}
	}
	ev := h.Events("server.redeployed")
	imgs, ok := ev[0].Payload["changed_images"].([]any)
	if len(ev) != 1 || ev[0].Payload["kind"] != "images" || !ok || len(imgs) != 0 {
		t.Fatalf("events: %+v", ev)
	}
	if d := latest(t, h, id); d.Kind != "images" || d.State != "succeeded" {
		t.Fatalf("deployment: %+v", d)
	}
}

func TestUpdateImagesReportsChangedImages(t *testing.T) {
	h, id := stubbed(t)
	h.VPS.NewImage("xray")
	if _, err := h.Mod.Deploy.UpdateImages(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	ev := h.Events("server.redeployed")
	imgs, _ := ev[0].Payload["changed_images"].([]any)
	if len(imgs) != 1 || imgs[0] != "xray:latest" {
		t.Fatalf("changed images: %+v", ev[0].Payload)
	}
}

func TestRebootTimeout(t *testing.T) {
	h, id := stubbed(t)
	h.VPS.RebootNeverReturns = true
	h.Mod.Deploy.RebootTimeout = 150 * time.Millisecond
	job, err := h.Mod.Deploy.Reboot(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	j := mustState(t, h, job, jobs.Failed)
	if j.ErrorStep != "wait-ssh" || !strings.Contains(j.Error, "did not come back") {
		t.Fatalf("job: %q at %q", j.Error, j.ErrorStep)
	}
	if h.VPS.Reboots() != 1 {
		t.Errorf("%d reboots", h.VPS.Reboots())
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["kind"] != "reboot" || ev[0].Payload["step"] != "wait-ssh" {
		t.Fatalf("events: %+v", ev)
	}
	if d := latest(t, h, id); d.Kind != "reboot" || d.State != "failed" {
		t.Fatalf("deployment: %+v", d)
	}
	if h.Server(id).State != "active" {
		t.Error("the server left the active state")
	}
}

func TestRebootWaitsForTheMachine(t *testing.T) {
	h, id := stubbed(t)
	h.VPS.RebootDowntime = 120 * time.Millisecond
	job, err := h.Mod.Deploy.Reboot(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	if ev := h.Events("server.redeployed"); len(ev) != 1 || ev[0].Payload["kind"] != "reboot" {
		t.Fatalf("events: %+v", ev)
	}
	if !strings.Contains(h.Log(job), "The server is back") {
		t.Errorf("log:\n%s", h.Log(job))
	}
}

func TestContainerLogs(t *testing.T) {
	h, id := stubbed(t)
	services := h.VPS.Services()
	if len(services) < 2 {
		t.Fatalf("services: %v", services)
	}
	var events int
	if err := h.App.DB.R.Get(&events, `SELECT count(*) FROM events`); err != nil {
		t.Fatal(err)
	}
	deps := len(deployments(t, h, id))
	job, err := h.Mod.Deploy.ContainerLogs(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	log := h.Log(job)
	for name := range services {
		if n := strings.Count(log, name+"-1  | log line "); n != 200 {
			t.Errorf("%d lines of %s, want 200", n, name)
		}
	}
	var after int
	if err := h.App.DB.R.Get(&after, `SELECT count(*) FROM events`); err != nil || after != events {
		t.Errorf("events: %d → %d (%v)", events, after, err)
	}
	if n := len(deployments(t, h, id)); n != deps {
		t.Errorf("a deployment was recorded: %d → %d", deps, n)
	}
}

func TestContainerLogsOnFailedServer(t *testing.T) {
	// A server that failed after its stack was uploaded has logs to read.
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	h.StallSmoke(true)
	id := h.Create(h.Form())
	h.Drain()
	if s := h.Server(id); s.State != "failed" {
		t.Fatalf("server %s", s.State)
	}
	job, err := h.Mod.Deploy.ContainerLogs(bg, id, "admin")
	if err != nil {
		t.Fatalf("logs of a failed server with a stack: %v", err)
	}
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	// The other operations are for active servers only.
	if _, err := h.Mod.Deploy.Restart(bg, id, "admin"); !errors.Is(err, deploy.ErrNotActive) {
		t.Errorf("restart of a failed server: %v", err)
	}

	// One that never got a stack has none.
	h2 := serverstest.NewHarness(t, serverstest.StubProxy())
	h2.VPS.PortsInUse[443] = "nginx (pid 812)"
	id2 := h2.Create(h2.Form())
	h2.Drain()
	if _, err := h2.Mod.Deploy.ContainerLogs(bg, id2, "admin"); !errors.Is(err, deploy.ErrNotAllowed) {
		t.Errorf("logs of a server without a stack: %v", err)
	}
}
