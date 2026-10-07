package deploy_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// credentials is the first endpoint the catalog serves: what clients get.
func credentials(t *testing.T, h *serverstest.Harness, id int64) endpoint.Endpoint {
	t.Helper()
	se, ok, err := h.Mod.EndpointCatalog().Server(bg, id)
	if err != nil || !ok || len(se.Endpoints) == 0 {
		t.Fatalf("catalog: %v %v", ok, err)
	}
	return se.Endpoints[0]
}

func generated(t *testing.T, h *serverstest.Harness, id int64) (values, pending map[string]string) {
	t.Helper()
	rows, err := store.GeneratedValues(bg, h.App.DB.R, id)
	if err != nil {
		t.Fatal(err)
	}
	values, err = sealed.OpenGenerated(h.App.Vault, id, rows)
	if err != nil {
		t.Fatal(err)
	}
	pending, err = sealed.OpenPending(h.App.Vault, id, rows)
	if err != nil {
		t.Fatal(err)
	}
	return values, pending
}

func rotate(t *testing.T, h *serverstest.Harness, id int64) int64 {
	t.Helper()
	job, err := h.Mod.Deploy.Rotate(bg, id, "admin")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	return job
}

func TestRotateChangesCredentials(t *testing.T) {
	// Real XHTTP traffic: the in-process server accepts one UUID and path at a time.
	h := serverstest.NewHarness(t)
	id := h.Provisioned()
	old := credentials(t, h, id)

	job := rotate(t, h, id)
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	fresh := credentials(t, h, id)
	if fresh.Credential == old.Credential || fresh.Params["path"] == old.Params["path"] {
		t.Fatalf("the credentials did not change: %+v → %+v", old, fresh)
	}
	// The proxy server now knows the new ones only: the old URI fails, the new passes.
	opts := proxy.Options{
		URL: h.Proxy.ObjectURL(), Stall: 400 * time.Millisecond, Timeout: 4 * time.Second, PinCert: h.Proxy.CertSHA256,
		Resolve: map[string]string{net.JoinHostPort(old.Host, strconv.Itoa(old.Port)): net.JoinHostPort("127.0.0.1", strconv.Itoa(h.Proxy.Port))},
	}
	if res := proxy.Test(bg, old, opts); res.OK {
		t.Error("the old URI still passes the proxy test")
	}
	if res := proxy.Test(bg, fresh, opts); !res.OK {
		t.Errorf("the new URI fails the proxy test: %s %s", res.Class, res.Error)
	}
	ev := h.Events("server.credentials_rotated")
	if len(ev) != 1 {
		t.Fatalf("events: %+v", ev)
	}
	if b, _ := h.VPS.File(stack + "xray-config.json"); !strings.Contains(string(b), fresh.Credential) || strings.Contains(string(b), old.Credential) {
		t.Error("the server's xray config does not hold exactly the new UUID")
	}
	if d := latest(t, h, id); d.Kind != "rotate" || d.State != "succeeded" || !d.Uploaded {
		t.Errorf("deployment: %+v", d)
	}
	if log := h.Log(job); strings.Contains(log, fresh.Credential) || strings.Contains(log, old.Credential) {
		t.Error("a credential is in the job log")
	}
}

func TestRotationVisibleOnlyAfterCommit(t *testing.T) {
	h, id := stubbed(t)
	old := credentials(t, h, id)
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	job := rotate(t, h, id)
	<-reached
	// The new values exist, but nothing serves them yet.
	_, pending := generated(t, h, id)
	if len(pending) != 2 {
		t.Fatalf("pending values: %v", pending)
	}
	during := credentials(t, h, id)
	if during.Credential != old.Credential || during.Params["path"] != old.Params["path"] {
		t.Fatalf("the catalog serves %+v during the job, want the old %+v", during, old)
	}
	release()
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	after := credentials(t, h, id)
	if after.Credential != pending["client_uuid"] || after.Params["path"] != pending["xhttp_path"] {
		t.Fatalf("after the commit the catalog serves %+v, want the pending %v", after, pending)
	}
	if _, left := generated(t, h, id); len(left) != 0 {
		t.Errorf("pending values left: %v", left)
	}
}

func TestRotationUploadFailureRestores(t *testing.T) {
	h, id := stubbed(t)
	old := credentials(t, h, id)
	before, _ := h.VPS.File(stack + "xray-config.json")
	h.VPS.Fail(remote.OpPrepareDirs, "mkdir: No space left on device", 1)
	job := rotate(t, h, id)
	h.Drain()
	j := mustState(t, h, job, jobs.Failed)
	if j.ErrorStep != "upload-files" {
		t.Fatalf("failed at %q", j.ErrorStep)
	}
	if now := credentials(t, h, id); now.Credential != old.Credential || now.Params["path"] != old.Params["path"] {
		t.Errorf("the old values are not in force: %+v", now)
	}
	if after, _ := h.VPS.File(stack + "xray-config.json"); string(after) != string(before) {
		t.Error("the old files are not back")
	}
	if _, pending := generated(t, h, id); len(pending) != 0 {
		t.Errorf("pending values left: %v", pending)
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["kind"] != "rotate" || ev[0].Payload["step"] != "upload-files" || ev[0].Payload["restored"] != true {
		t.Fatalf("events: %+v", ev)
	}
	if n := len(h.Events("server.credentials_rotated")); n != 0 {
		t.Errorf("%d rotation events", n)
	}
	// The restore is a deployment of its own, and the current files again.
	ds := deployments(t, h, id)
	if ds[0].Kind != "restore" || ds[0].State != "succeeded" || !ds[0].Uploaded || ds[1].Kind != "rotate" || ds[1].State != "failed" {
		t.Errorf("deployments: %+v", ds)
	}
	if log := h.Log(job); !strings.Contains(log, "The old files are back") {
		t.Errorf("log:\n%s", log)
	}
}

func TestRotationProxyFailureRestores(t *testing.T) {
	h, id := stubbed(t)
	old := credentials(t, h, id)
	before, _ := h.VPS.File(stack + "xray-config.json")
	// The server rejects anything but the old UUID, as one that did not take the new config.
	h.ProxyAccepts(func(e endpoint.Endpoint) bool { return e.Credential == old.Credential })
	job := rotate(t, h, id)
	h.Drain()
	j := mustState(t, h, job, jobs.Failed)
	if j.ErrorStep != "proxy-test" {
		t.Fatalf("failed at %q", j.ErrorStep)
	}
	if after, _ := h.VPS.File(stack + "xray-config.json"); string(after) != string(before) {
		t.Error("the old files are not back")
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "proxy-test" || ev[0].Payload["restored"] != true {
		t.Fatalf("events: %+v", ev)
	}
	// Redeployed (compose up ran for the restore too) and the old values pass.
	ups := 0
	for _, c := range h.VPS.Commands() {
		if call, ok := remote.Parse(c[strings.Index(c, ": ")+2:]); ok && call.Op == remote.OpComposeUp {
			ups++
		}
	}
	if ups < 3 { // provisioning, the rotation, the restore
		t.Errorf("compose up ran %d times", ups)
	}
	if res := h.Mod.Provision.ProxyTest(bg, credentials(t, h, id), proxy.Options{}); !res.OK {
		t.Errorf("the old values do not pass: %+v", res)
	}
	if v, pending := generated(t, h, id); len(pending) != 0 || v["client_uuid"] != old.Credential {
		t.Errorf("values %v pending %v", v, pending)
	}
	if log := h.Log(job); !strings.Contains(log, "The old files are back") {
		t.Errorf("log:\n%s", log)
	}
}

func TestOnlyRotatableValuesChange(t *testing.T) {
	h, id := stubbed(t)
	before, _ := generated(t, h, id)
	envBefore, _ := h.VPS.File(stack + ".env")
	rotate(t, h, id)
	h.Drain()
	after, _ := generated(t, h, id)
	if after["client_uuid"] == before["client_uuid"] || after["xhttp_path"] == before["xhttp_path"] {
		t.Fatalf("rotatable values did not change: %v → %v", before, after)
	}
	if after["container_postfix"] != before["container_postfix"] {
		t.Errorf("container_postfix changed: %s → %s", before["container_postfix"], after["container_postfix"])
	}
	rows, _ := store.GeneratedValues(bg, h.App.DB.R, id)
	for _, r := range rows {
		if rotated := !r.RotatedAt.IsZero(); rotated != (r.Key != "container_postfix") {
			t.Errorf("%s: rotated_at set = %v", r.Key, rotated)
		}
	}
	envAfter, _ := h.VPS.File(stack + ".env")
	if !strings.Contains(string(envAfter), "CONTAINER_POSTFIX="+before["container_postfix"]) || string(envAfter) == string(envBefore) {
		t.Errorf(".env: %q", envAfter)
	}
	ev := h.Events("server.credentials_rotated")
	keys, _ := ev[0].Payload["keys"].([]any)
	if len(keys) != 2 || keys[0] != "client_uuid" || keys[1] != "xhttp_path" {
		t.Errorf("rotated keys: %+v", ev[0].Payload)
	}
	// A template with nothing to rotate refuses.
	h2 := serverstest.NewHarness(t, serverstest.StubProxy())
	h2.PublishVersion(func(files map[string][]byte) {
		files["manifest.yaml"] = []byte(strings.NewReplacer("rotate: true", "rotate: false").Replace(string(files["manifest.yaml"])))
	}, true)
	id2 := h2.Provisioned()
	if _, err := h2.Mod.Deploy.Rotate(bg, id2, "admin"); err != deploy.ErrNothingToRotate {
		t.Errorf("rotating a template without rotatable values: %v", err)
	}
}

func TestRotationWaitsForRedeploy(t *testing.T) {
	h, id := stubbed(t)
	stop := watchOverlap(t, h, "resource_key = ?", "server:"+strconv.FormatInt(id, 10))
	defer stop()
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	first, err := h.Mod.Deploy.Apply(bg, id, deploy.Target{Force: true}, "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-reached
	second := rotate(t, h, id)
	time.Sleep(50 * time.Millisecond)
	if j, _ := h.App.Jobs.Job(bg, second); j.State != jobs.Queued {
		t.Fatalf("the rotation is %s while the redeploy runs", j.State)
	}
	release()
	h.Drain()
	mustState(t, h, first, jobs.Succeeded)
	mustState(t, h, second, jobs.Succeeded)
	if ds := deployments(t, h, id); ds[0].Kind != "rotate" || ds[1].Kind != "redeploy" {
		t.Errorf("deployments: %+v", ds)
	}
}

func TestRotationResumesWithSamePending(t *testing.T) {
	h, id := stubbed(t)
	orig := h.Mod.Provision.ProxyTest
	var calls atomic.Int32
	reached := make(chan struct{})
	h.Mod.Provision.ProxyTest = func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Result {
		if calls.Add(1) == 1 {
			close(reached)
			<-ctx.Done() // the process stops in the middle of the proxy test
			return proxy.Result{Class: proxy.Timeout, Error: "cancelled"}
		}
		return orig(ctx, e, o)
	}
	job := rotate(t, h, id)
	<-reached
	h.StopJobs()
	if j, _ := h.App.Jobs.Job(bg, job); j.State != jobs.Interrupted {
		t.Fatalf("job %s", j.State)
	}
	_, pending := generated(t, h, id)
	if len(pending) != 2 {
		t.Fatalf("pending: %v", pending)
	}
	h.StartJobs()
	h.Drain()
	mustState(t, h, job, jobs.Succeeded)
	values, left := generated(t, h, id)
	if len(left) != 0 || values["client_uuid"] != pending["client_uuid"] || values["xhttp_path"] != pending["xhttp_path"] {
		t.Fatalf("after the resume: %v (pending %v), want %v", values, left, pending)
	}
	if n := len(h.Events("server.credentials_rotated")); n != 1 {
		t.Errorf("%d rotation events", n)
	}
}

func TestCancelledRotationIsRestored(t *testing.T) {
	h, id := stubbed(t)
	old := credentials(t, h, id)
	before, _ := h.VPS.File(stack + "xray-config.json")
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	job := rotate(t, h, id)
	<-reached // the new files are on the server
	if b, _ := h.VPS.File(stack + "xray-config.json"); string(b) == string(before) {
		t.Fatal("the rotation has not uploaded yet")
	}
	if err := h.App.Jobs.Cancel(bg, job, "admin"); err != nil {
		t.Fatal(err)
	}
	release()
	h.Drain()
	mustState(t, h, job, jobs.Cancelled)

	// A restore job was queued in the cancellation, and it ran.
	restore := h.LastJob(deploy.JobRestore)
	if restore == 0 {
		t.Fatal("no restore job")
	}
	mustState(t, h, restore, jobs.Succeeded)
	if log := h.Log(job); !strings.Contains(log, "restore queued as job #"+strconv.FormatInt(restore, 10)) {
		t.Errorf("the rotation's log does not name the restore:\n%s", log)
	}
	if after, _ := h.VPS.File(stack + "xray-config.json"); string(after) != string(before) {
		t.Error("the old files are not back")
	}
	if _, pending := generated(t, h, id); len(pending) != 0 {
		t.Errorf("pending values left: %v", pending)
	}
	if now := credentials(t, h, id); now.Credential != old.Credential {
		t.Errorf("the old values are not in force: %+v", now)
	}
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["cancelled"] != true || ev[0].Payload["kind"] != "rotate" {
		t.Fatalf("events: %+v", ev)
	}
	if rd := h.Events("server.redeployed"); len(rd) != 1 || rd[0].Payload["kind"] != "restore" {
		t.Errorf("restore events: %+v", rd)
	}
	ds := deployments(t, h, id)
	if ds[0].Kind != "restore" || ds[0].State != "succeeded" || ds[1].Kind != "rotate" || ds[1].State != "failed" || ds[1].Error != "cancelled" {
		t.Errorf("deployments: %+v", ds)
	}
}

func TestRotationThatCannotBeRestoredSaysSo(t *testing.T) {
	h, id := stubbed(t)
	old := credentials(t, h, id)
	h.VPS.Fail(remote.OpPrepareDirs, "mkdir: Read-only file system", -1)
	job := rotate(t, h, id)
	h.Drain()
	mustState(t, h, job, jobs.Failed)
	ev := h.Events("server.redeploy_failed")
	if len(ev) != 1 || ev[0].Payload["restored"] != false || ev[0].Payload["step"] != "upload-files" {
		t.Fatalf("events: %+v", ev)
	}
	if !strings.Contains(h.Log(job), "The restore failed too") {
		t.Errorf("log:\n%s", h.Log(job))
	}
	// The values Proxier serves are still the old ones, and nothing new is pending.
	if now := credentials(t, h, id); now.Credential != old.Credential {
		t.Errorf("served %+v", now)
	}
	if _, pending := generated(t, h, id); len(pending) != 0 {
		t.Errorf("pending: %v", pending)
	}
	if h.Server(id).State != "active" {
		t.Error("the server left the active state")
	}
}
