package deploy_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

const stack = "/opt/proxier/vless-xhttp/"

func TestUpgradeOneFile(t *testing.T) {
	h, id := stubbed(t)
	before, _, _ := h.Mod.EndpointCatalog().Server(bg, id)
	// A file the upgrade does not change is not uploaded again: tamper with one.
	h.VPS.PutFile(stack+".env", []byte("TAMPERED=1\n"))
	v2 := h.PublishVersion(change("site/index.html", "</body>", "<!-- v2 --></body>"), true)

	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Version: v2})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Path != "site/index.html" || plan.Files[0].Change != "changed" || plan.UploadCount != 1 || len(plan.Endpoints) != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.Kind != "upgrade" || plan.FromVersion != 1 || plan.ToVersion != v2 {
		t.Fatalf("plan kind: %+v", plan)
	}
	commands := len(h.VPS.Commands())
	job := apply(t, h, id, deploy.Target{Version: v2})
	mustState(t, h, job, jobs.Succeeded)

	if b, _ := h.VPS.File(stack + "site/index.html"); !strings.Contains(string(b), "<!-- v2 -->") {
		t.Error("the changed file is not on the server")
	}
	if b, _ := h.VPS.File(stack + ".env"); string(b) != "TAMPERED=1\n" {
		t.Errorf("an unchanged file was uploaded again: %q", b)
	}
	up := false
	for _, c := range h.VPS.Commands()[commands:] {
		remoteCall, ok := remote.Parse(strings.TrimPrefix(c, "proxier: "))
		up = up || ok && remoteCall.Op == remote.OpComposeUp
	}
	if !up {
		t.Error("compose up did not run")
	}
	srv := h.Server(id)
	if srv.TemplateVersion != v2 {
		t.Errorf("the server is at v%d", srv.TemplateVersion)
	}
	after, _, _ := h.Mod.EndpointCatalog().Server(bg, id)
	if len(after.Endpoints) != 1 || after.Endpoints[0].Credential != before.Endpoints[0].Credential || after.Endpoints[0].Params["path"] != before.Endpoints[0].Params["path"] {
		t.Errorf("the connection URI changed: %+v → %+v", before.Endpoints, after.Endpoints)
	}
	d := latest(t, h, id)
	if d.Kind != "upgrade" || d.TemplateVersion != v2 || d.FilesChanged != 1 || !d.Uploaded || d.State != "succeeded" {
		t.Errorf("deployment: %+v", d)
	}
	ev := h.Events("server.redeployed")
	if len(ev) != 1 || ev[0].Payload["from_version"] != float64(1) || ev[0].Payload["to_version"] != float64(v2) || ev[0].Payload["kind"] != "upgrade" {
		t.Errorf("events: %+v", ev)
	}
}

func TestUpgradeCreatesGeneratedValue(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(all(
		change("manifest.yaml", "  - { key: container_postfix, kind: hex, length: 4 }\n", "  - { key: container_postfix, kind: hex, length: 4 }\n  - { key: stats_token, kind: hex, length: 16 }\n"),
		change(".env", "SERVER_DOMAIN=", "STATS_TOKEN={{ .Gen.stats_token }}\nSERVER_DOMAIN="),
	), true)
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Version: v2})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.NewGenerated) != 1 || plan.NewGenerated[0] != "stats_token" {
		t.Fatalf("new generated values: %v", plan.NewGenerated)
	}
	// Planning creates nothing.
	rows, _ := store.GeneratedValues(bg, h.App.DB.R, id)
	if len(rows) != 3 {
		t.Fatalf("%d generated values after a plan", len(rows))
	}
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v2}), jobs.Succeeded)
	rows, _ = store.GeneratedValues(bg, h.App.DB.R, id)
	values, err := sealed.OpenGenerated(h.App.Vault, id, rows)
	if err != nil || len(values) != 4 || len(values["stats_token"]) != 16 {
		t.Fatalf("generated values: %v %v", values, err)
	}
	if b, _ := h.VPS.File(stack + ".env"); !strings.Contains(string(b), "STATS_TOKEN="+values["stats_token"]) {
		t.Errorf(".env on the server: %q", b)
	}
	// The value never reaches the log.
	if log := h.Log(h.LastJob(deploy.JobDeploy)); strings.Contains(log, values["stats_token"]) {
		t.Error("the new value is in the job log")
	}
}

func TestUpgradeRemovesOldFile(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(all(
		change("manifest.yaml", "  - { path: site/index.html }\n", "  - { path: site/index.html }\n  - { path: site/extra.css }\n"),
		func(files map[string][]byte) { files["site/extra.css"] = []byte("body{}\n") },
	), true)
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v2}), jobs.Succeeded)
	if _, ok := h.VPS.File(stack + "site/extra.css"); !ok {
		t.Fatal("v2 did not deploy extra.css")
	}
	// Runtime data of the stack, which no deployment created.
	h.VPS.PutFile(stack+"certbot/conf/live/x/fullchain.pem", []byte("cert"))

	v3 := h.PublishVersion(all(
		change("manifest.yaml", "  - { path: site/extra.css }\n", ""),
		func(files map[string][]byte) { delete(files, "site/extra.css") },
	), true)
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Version: v3})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Path != "site/extra.css" || plan.Files[0].Change != "removed" || plan.UploadCount != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v3}), jobs.Succeeded)
	if _, ok := h.VPS.File(stack + "site/extra.css"); ok {
		t.Error("the removed file is still on the server")
	}
	if b, ok := h.VPS.File(stack + "certbot/conf/live/x/fullchain.pem"); !ok || string(b) != "cert" {
		t.Error("runtime data was touched")
	}
}

func TestDroppedEndpointDisappears(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(change("manifest.yaml", "key: main ", "key: alt  "), true)
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Version: v2})
	if err != nil {
		t.Fatal(err)
	}
	var removed, added bool
	for _, e := range plan.Endpoints {
		removed = removed || e.Key == "main" && e.Change == "removed"
		added = added || e.Key == "alt" && e.Change == "added"
	}
	if !removed || !added || len(plan.Warnings) != 1 || plan.Warnings[0].Key != "servers.plan.warn.endpoint_removed" || plan.Warnings[0].Args["key"] != "main" {
		t.Fatalf("plan: %+v", plan)
	}
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v2}), jobs.Succeeded)
	se, ok, err := h.Mod.EndpointCatalog().Server(bg, id)
	if err != nil || !ok || len(se.Endpoints) != 1 || se.Endpoints[0].Key != "alt" {
		t.Fatalf("catalog: %+v %v %v", se, ok, err)
	}
}

func TestUnreachableFailsAtConnect(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(change("site/index.html", "</body>", "<!-- v2 --></body>"), true)
	before, _ := h.VPS.File(stack + "site/index.html")
	h.VPS.Close()
	job := apply(t, h, id, deploy.Target{Version: v2})
	j := mustState(t, h, job, jobs.Failed)
	if j.ErrorStep != "connect" {
		t.Errorf("failed at %q", j.ErrorStep)
	}
	if after, _ := h.VPS.File(stack + "site/index.html"); string(after) != string(before) {
		t.Error("the files changed")
	}
	if srv := h.Server(id); srv.State != "active" || srv.TemplateVersion != 1 {
		t.Errorf("server: %s v%d", srv.State, srv.TemplateVersion)
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "connect" || ev[0].Payload["kind"] != "upgrade" {
		t.Errorf("events: %+v", ev)
	}
	// No file reached the server, so there is nothing to roll back.
	if _, err := h.Mod.Deploy.RollBack(bg, id); err == nil {
		t.Error("roll back offered")
	}
}

func TestComposeFailureOffersRollBack(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(change("site/index.html", "</body>", "<!-- v2 --></body>"), true)
	h.VPS.ComposeUpFails = "pull access denied for no-such-image"
	job := apply(t, h, id, deploy.Target{Version: v2})
	j := mustState(t, h, job, jobs.Failed)
	if j.ErrorStep != "redeploy" || !strings.Contains(j.Error, "pull access denied") {
		t.Fatalf("job: %s at %s", j.Error, j.ErrorStep)
	}
	srv := h.Server(id)
	if srv.State != "active" || srv.TemplateVersion != 1 {
		t.Fatalf("server: %s v%d", srv.State, srv.TemplateVersion)
	}
	d := latest(t, h, id)
	if d.State != "failed" || !d.Uploaded || d.TemplateVersion != v2 {
		t.Fatalf("deployment: %+v", d)
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "redeploy" || ev[0].Payload["cancelled"] != nil {
		t.Fatalf("events: %+v", ev)
	}

	target, err := h.Mod.Deploy.RollBack(bg, id)
	if err != nil {
		t.Fatalf("roll back: %v", err)
	}
	if target.Version != 1 || !target.Force || target.Kind != "redeploy" {
		t.Fatalf("roll back target: %+v", target)
	}
	plan, err := h.Mod.Deploy.Plan(bg, id, target)
	if err != nil || plan.ToVersion != 1 || plan.Empty || plan.UploadCount != 6 {
		t.Fatalf("roll back plan: %+v %v", plan, err)
	}
	// Applying it puts the first version's files back.
	h.VPS.ComposeUpFails = ""
	mustState(t, h, apply(t, h, id, target), jobs.Succeeded)
	if b, _ := h.VPS.File(stack + "site/index.html"); strings.Contains(string(b), "<!-- v2 -->") {
		t.Error("the failed version is still on the server")
	}
	if _, err := h.Mod.Deploy.RollBack(bg, id); err == nil {
		t.Error("roll back offered after a good deployment")
	}
}

func TestDeploysAreSerializedPerServer(t *testing.T) {
	h, id := stubbed(t)
	stop := watchOverlap(t, h, "resource_key = ?", "server:"+strconv.FormatInt(id, 10))
	defer stop()
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	first, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{Force: true}, "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	second, err := h.Mod.Deploy.Restart(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // long enough for a free worker to take it
	if j, _ := h.App.Jobs.Job(bg, second); j.State != jobs.Queued {
		t.Fatalf("the second job is %s while the first runs", j.State)
	}
	release()
	h.Drain()
	mustState(t, h, first, jobs.Succeeded)
	mustState(t, h, second, jobs.Succeeded)
	// The second job ran after the first, and never beside it.
	if ds := deployments(t, h, id); ds[0].Kind != "restart" || ds[1].Kind != "redeploy" {
		t.Errorf("deployments: %+v", ds)
	}
}

func TestPlanIsRecomputed(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(change("site/index.html", "</body>", "<!-- v2 --></body>"), true)
	plan, err := h.Mod.Deploy.Plan(bg, id, deploy.Target{Version: v2})
	if err != nil {
		t.Fatal(err)
	}
	// Another change lands between the preview and the apply.
	if _, err := h.App.DB.W.Exec(`UPDATE servers_servers SET params = '{"letsencrypt_email":"ops@example.com"}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	job, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{Version: v2}, plan.Hash, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	if log := h.Log(job); !strings.Contains(log, "The plan was recomputed") {
		t.Errorf("the log does not say so:\n%s", log)
	}
	// The new difference was applied: the certificate script carries the email.
	if b, _ := h.VPS.File(stack + "issue-cert.sh"); !strings.Contains(string(b), "ops@example.com") {
		t.Error("the new difference was not applied")
	}
	// A job whose plan is the one that was shown does not say so.
	plan, _ = h.Mod.Deploy.Plan(bg, id, deploy.Target{Force: true})
	job, _ = h.Mod.Deploy.Apply(bg, id, deploy.Target{Force: true}, plan.Hash, "admin")
	h.Drain()
	if log := h.Log(job); strings.Contains(log, "recomputed") {
		t.Errorf("a false alarm:\n%s", log)
	}
}

func TestHostKeyChangedStopsDeploy(t *testing.T) {
	h, id := stubbed(t)
	h.VPS.RotateHostKey()
	h.VPS.PutFile(stack+"site/index.html", []byte("untouched"))
	job := apply(t, h, id, deploy.Target{Force: true})
	j := mustState(t, h, job, jobs.Failed)
	if j.ErrorStep != "connect" || !strings.Contains(j.Error, "host key changed") {
		t.Fatalf("job: %q at %q", j.Error, j.ErrorStep)
	}
	if b, _ := h.VPS.File(stack + "site/index.html"); string(b) != "untouched" {
		t.Error("something was uploaded")
	}
	for _, d := range deployments(t, h, id) {
		if d.Kind != "provision" {
			t.Errorf("a deployment was started: %+v", d)
		}
	}
}

func TestRedeployUsesOnlyProxiersKey(t *testing.T) {
	h, id := stubbed(t)
	h.VPS.SetRootPassword("a-different-password")
	seen := len(h.VPS.Commands())
	job := apply(t, h, id, deploy.Target{Force: true})
	mustState(t, h, job, jobs.Succeeded)
	for _, c := range h.VPS.Commands()[seen:] {
		if strings.HasPrefix(c, "root:") {
			t.Errorf("a command ran as root: %s", c)
		}
	}
	if secrets := h.JobSecrets(job); len(secrets) != 0 {
		t.Errorf("the job holds secrets: %v", secrets)
	}
}

func TestParamsChangeOnlyOnSuccess(t *testing.T) {
	h, id := stubbed(t)
	target := deploy.Target{Kind: deploy.KindParams, Params: map[string]string{"letsencrypt_email": "ops@example.com"}}
	plan, err := h.Mod.Deploy.Plan(bg, id, target)
	if err != nil || len(plan.ParamsAdded) != 0 || len(plan.ParamsChanged) != 1 || plan.ParamsChanged[0] != "letsencrypt_email" || plan.Empty {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	h.VPS.ComposeUpFails = "port is already allocated"
	mustState(t, h, apply(t, h, id, target), jobs.Failed)
	if p := h.Server(id).Params; p != "{}" {
		t.Fatalf("a failed change stored %s", p)
	}
	h.VPS.ComposeUpFails = ""
	mustState(t, h, apply(t, h, id, target), jobs.Succeeded)
	srv := h.Server(id)
	if srv.Params != `{"letsencrypt_email":"ops@example.com"}` {
		t.Fatalf("params after success: %s", srv.Params)
	}
	d := latest(t, h, id)
	if d.Kind != "params" || d.Params != srv.Params {
		t.Errorf("deployment: %+v", d)
	}
}

func TestCancelledDeployFailsDeployment(t *testing.T) {
	h, id := stubbed(t)
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	job, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{Force: true}, "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	if err := h.App.Jobs.Cancel(bg, job, "admin"); err != nil {
		t.Fatal(err)
	}
	release()
	h.Drain()
	mustState(t, h, job, jobs.Cancelled)
	d := latest(t, h, id)
	if d.State != "failed" || d.Error != "cancelled" {
		t.Fatalf("deployment: %+v", d)
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["cancelled"] != true || ev[0].Payload["error"] != "cancelled" {
		t.Fatalf("events: %+v", ev)
	}
	for _, typ := range servers.Events {
		if typ.Name == "server.redeploy_failed" && typ.NotifyIf(ev[0].Payload) {
			t.Error("a cancelled deploy would notify")
		}
	}
	if srv := h.Server(id); srv.State != "active" {
		t.Errorf("server %s", srv.State)
	}
}
