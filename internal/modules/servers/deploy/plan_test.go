package deploy_test

import (
	"errors"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func TestNothingToChange(t *testing.T) {
	h, id := stubbed(t)
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty || len(plan.Files) != 0 || len(plan.Endpoints) != 0 || plan.FromVersion != 1 || plan.ToVersion != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	// Nothing is queued without Force.
	if _, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{}, plan.Hash, "admin"); !errors.Is(err, deploy.ErrNothingToChange) {
		t.Fatalf("apply without a difference: %v", err)
	}
	if n := jobCount(t, h, deploy.JobDeploy); n != 0 {
		t.Fatalf("%d deploy jobs", n)
	}
	// Force uploads everything and runs the redeploy steps anyway.
	forced, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if forced.Empty || forced.UploadCount != 6 || len(forced.Files) != 0 {
		t.Fatalf("forced plan: %+v", forced)
	}
	job := apply(t, h, id, deploy.Target{Force: true})
	mustState(t, h, job, jobs.Succeeded)
	d := latest(t, h, id)
	if d.Kind != "redeploy" || d.State != "succeeded" || !d.Uploaded || d.FilesChanged != 6 {
		t.Fatalf("deployment: %+v", d)
	}
	if evs := h.Events("server.redeployed"); len(evs) != 1 || evs[0].Payload["kind"] != "redeploy" || evs[0].Payload["files_changed"] != float64(6) {
		t.Fatalf("events: %+v", evs)
	}
}

func TestMissingParameterAskedFirst(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(addParam("log_level", true, ""), true)
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Version: v2})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.MissingParams) != 1 || plan.MissingParams[0] != "log_level" || len(plan.ParamsAdded) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	// Apply refuses until it is given; nothing is queued.
	_, err = h.Mod.Deploy.Apply(bg, id, deploy.Target{Version: v2}, plan.Hash, "admin")
	var fe deploy.FieldErrors
	if !errors.As(err, &fe) || fe["param.log_level"].Key != "servers.err.param_required" {
		t.Fatalf("apply: %v", err)
	}
	if n := jobCount(t, h, deploy.JobDeploy); n != 0 {
		t.Fatalf("%d deploy jobs", n)
	}
	given := deploy.Target{Version: v2, Params: map[string]string{"log_level": "warn"}}
	plan, err = h.Mod.Deploy.Plan(bg, id, given)
	if err != nil || len(plan.MissingParams) != 0 {
		t.Fatalf("plan with the value: %+v %v", plan, err)
	}
	mustState(t, h, apply(t, h, id, given), jobs.Succeeded)
	if s := h.Server(id); s.TemplateVersion != v2 || s.Params != `{"log_level":"warn"}` {
		t.Fatalf("server: v%d %s", s.TemplateVersion, s.Params)
	}
}

func TestPlanIgnoresDeploymentsWithoutFiles(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(change("site/index.html", "</body>", "<!-- two --></body>"), true)
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v2}), jobs.Succeeded)
	if _, err := h.Mod.Deploy.Restart(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if d := latest(t, h, id); d.Kind != "restart" || d.Uploaded || d.State != "succeeded" {
		t.Fatalf("latest deployment: %+v", d)
	}
	// The restart is the latest deployment, but the current files are the upgrade's.
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty || plan.FromVersion != v2 {
		t.Fatalf("redeploy after a restart: %+v", plan)
	}
	// A failed upgrade after that: Roll back targets the upgrade's version.
	v3 := h.PublishVersion(change("site/index.html", "<!-- two -->", "<!-- three -->"), true)
	h.VPS.ComposeUpFails = "bad image"
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v3}), jobs.Failed)
	target, err := h.Mod.Deploy.RollBack(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if target.Version != v2 || !target.Force {
		t.Fatalf("roll back target: %+v", target)
	}
}
