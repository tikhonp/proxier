package provision_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy/proxytest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"golang.org/x/crypto/ssh"
)

// failed checks that the server's provisioning stopped at step with an error
// containing want, and returns the server.
func failed(t *testing.T, h *serverstest.Harness, id int64, step, want string) store.Server {
	t.Helper()
	srv := h.Server(id)
	if srv.State != "failed" || srv.FailedStep != step || !strings.Contains(srv.FailedError, want) {
		t.Fatalf("state %s, failed at %q with %q; want %q containing %q\n%s", srv.State, srv.FailedStep, srv.FailedError, step, want, h.Log(srv.ProvisionJobID.Int64))
	}
	return srv
}

// active checks that the server ended up active.
func active(t *testing.T, h *serverstest.Harness, id int64) store.Server {
	t.Helper()
	srv := h.Server(id)
	if srv.State != "active" {
		t.Fatalf("state %s, failed at %q: %s\n%s", srv.State, srv.FailedStep, srv.FailedError, h.Log(srv.ProvisionJobID.Int64))
	}
	return srv
}

func retry(t *testing.T, h *serverstest.Harness, id int64, password string, overwrite bool) {
	t.Helper()
	if _, err := h.Mod.Provision.Retry(bg, id, password, overwrite, "admin"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	h.Drain()
}

func TestProvisionSucceeds(t *testing.T) {
	h := serverstest.NewHarness(t)
	id := h.Create(h.Form())
	h.Drain()

	srv := active(t, h, id)
	if srv.Health != "unknown" || srv.HealthSince.IsZero() || srv.ActivatedAt.IsZero() || srv.TemplateVersion != 1 {
		t.Fatalf("server: %+v", srv)
	}
	if j := h.Job(id); j.State != jobs.Succeeded {
		t.Fatalf("job %s", j.State)
	}
	// DNS: one record for the hostname, DNS only, with Proxier's mark; stored with the server.
	recs := h.CF.Records("tikhonnnnn.com")
	if len(recs) != 1 || recs[0].Name != srv.ManagementHostname || recs[0].Content != "127.0.0.1" || recs[0].Proxied || recs[0].Comment != "proxier:nl-1" || recs[0].TTL != 60 {
		t.Fatalf("Cloudflare records: %+v", recs)
	}
	stored, err := store.DNSRecords(bg, h.App.DB.R, id)
	if err != nil || len(stored) != 1 || stored[0].RecordID != recs[0].ID || stored[0].Name != srv.ManagementHostname {
		t.Fatalf("stored DNS records: %+v %v", stored, err)
	}
	// The deployment: succeeded, uploaded, with every rendered file stored.
	cur, ok, err := store.CurrentDeployment(bg, h.App.DB.R, id)
	if err != nil || !ok || cur.Kind != "provision" || cur.State != "succeeded" || cur.TemplateVersion != 1 || cur.FilesChanged != 6 {
		t.Fatalf("deployment: %+v %v %v", cur, ok, err)
	}
	files, err := store.DeployedFiles(bg, h.App.DB.R, cur.ID)
	if err != nil || len(files) != 6 {
		t.Fatalf("deployed files: %d %v", len(files), err)
	}
	if b, ok := h.VPS.File("/opt/proxier/vless-xhttp/compose.yaml"); !ok || !strings.Contains(string(b), "services:") {
		t.Fatalf("the stack is not on the server")
	}
	// The endpoints, with display names and a connection URI that works.
	eps, err := store.Endpoints(bg, h.App.DB.R, id)
	if err != nil || len(eps) != 1 || eps[0].Key != "main" || eps[0].DisplayName != "🇳🇱 Netherlands 1" || eps[0].Port != 443 || eps[0].Host != srv.ProxyHostname {
		t.Fatalf("endpoints: %+v %v", eps, err)
	}
	// server.activated carries the proxy test's timings, not forced.
	ev := h.Events("server.activated")
	if len(ev) != 1 || ev[0].Payload["forced"] != false || ev[0].Actor != "job:"+strconv.FormatInt(h.Job(id).ID, 10) {
		t.Fatalf("activated events: %+v", ev)
	}
	if pt, _ := ev[0].Payload["proxy_test"].(map[string]any); pt["main"] == nil {
		t.Fatalf("no proxy test timings: %+v", ev[0].Payload)
	}
	// Proxier's key is the way in now, and the SSH host key was pinned once.
	if h.VPS.Hardened() != true {
		t.Error("sshd was not hardened")
	}
	if allowed, on := h.VPS.Firewall(); !on || strings.Join(allowed, " ") != fmt.Sprintf("%d/tcp 80/tcp 443/tcp", h.Form().SSHPort) {
		t.Errorf("firewall %v on=%v", allowed, on)
	}
	if n := len(h.Events("ssh.host_key_pinned")); n != 1 {
		t.Errorf("%d host keys pinned", n)
	}
}

func TestWrongRootPassword(t *testing.T) {
	h := serverstest.NewHarness(t)
	f := h.Form()
	f.RootPassword = "not-the-password"
	id := h.Create(f)
	h.Drain()
	failed(t, h, id, "preflight", "authentication failed")
	if got := h.JobSecrets(h.Job(id).ID)["root_password"]; got != "not-the-password" {
		t.Fatalf("the password is not kept for the retry: %q", got)
	}
	// Retry asks for it again; without one nothing happens.
	_, err := h.Mod.Provision.Retry(bg, id, "", false, "admin")
	if fe := refused(t, err); fe["root_password"].Key != "servers.err.password_required" {
		t.Fatalf("errors: %+v", fe)
	}
	if h.Server(id).State != "failed" {
		t.Fatal("a refused retry changed the server")
	}
	retry(t, h, id, serverstest.DefaultRootPassword, false)
	active(t, h, id)
}

func TestUnreachableAtPreflight(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.Close()
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "preflight", "")
	if len(h.CF.Calls()) != 0 {
		t.Errorf("DNS was touched: %v", h.CF.Calls())
	}
	if recs, _ := store.DNSRecords(bg, h.App.DB.R, id); len(recs) != 0 {
		t.Errorf("DNS records stored: %+v", recs)
	}
}

func TestPortInUse(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.PortsInUse[443] = "nginx (pid 812)"
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "preflight", "port 443 is in use by nginx (pid 812)")
	if exists, _ := h.VPS.HasUser("proxier"); exists {
		t.Error("preflight changed the server")
	}
}

func TestUnsupportedOS(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.OS = "ubuntu-20.04"
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "preflight", "ubuntu-20.04")
	h.VPS.OS, h.VPS.Arch = "debian-12", "riscv64"
	retry(t, h, id, serverstest.DefaultRootPassword, false)
	failed(t, h, id, "preflight", "riscv64")
}

func TestKeyLoginFailsAfterInstall(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.BreakAuthorizedKeys = true
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install-access", "key login as proxier failed")
	if got := h.JobSecrets(h.Job(id).ID)["root_password"]; got != serverstest.DefaultRootPassword {
		t.Fatalf("the password was not kept: %q", got)
	}
	// Fixed on the server, Retry (with the password) goes through.
	h.VPS.BreakAuthorizedKeys = false
	h.VPS.AddUser("proxier") // starts the file over, as an admin fixing the permissions would
	retry(t, h, id, serverstest.DefaultRootPassword, false)
	active(t, h, id)
}

func TestSSHDChangeIsRolledBack(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.BreakKeyLoginAfterSSHD = true
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "base-bootstrap", "reverted")
	if h.VPS.SSHDDropin() != "" || h.VPS.Hardened() {
		t.Fatal("the drop-in is still there")
	}
	// Proxier can still log in.
	c, err := h.App.SSH.Connect(bg, sshx.Target{Hop: sshx.Hop{Address: h.VPS.Addr, User: "proxier", Subject: "server:" + strconv.FormatInt(id, 10)}}, nil)
	if err != nil {
		t.Fatalf("Proxier is locked out: %v", err)
	}
	_ = c.Close()
	// The root password works again too, as before the change.
	if _, err := h.App.SSH.Connect(bg, sshx.Target{Hop: sshx.Hop{Address: h.VPS.Addr, User: "root", Password: serverstest.DefaultRootPassword, Subject: "server:" + strconv.FormatInt(id, 10)}}, nil); err != nil {
		t.Fatalf("the original configuration was not restored: %v", err)
	}
	// After the fault is gone the same job resumes and finishes.
	h.VPS.BreakKeyLoginAfterSSHD = false
	retry(t, h, id, "", false)
	active(t, h, id)
	if !h.VPS.Hardened() {
		t.Error("sshd is not hardened after the retry")
	}
}

func TestDockerAlreadyInstalled(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.Docker = true
	id := h.Create(h.Form())
	h.Drain()
	active(t, h, id)
	if log := h.Log(h.Job(id).ID); !strings.Contains(log, "Docker already installed, skipped") {
		t.Errorf("the skip is not logged:\n%s", log)
	}
	for _, c := range h.VPS.Commands() {
		if call, ok := remote.Parse(strings.SplitN(c, ": ", 2)[1]); ok && call.Op == remote.OpDockerInstall {
			t.Error("Docker was installed again")
		}
	}
}

func TestExistingDeployUserIsReused(t *testing.T) {
	h := serverstest.NewHarness(t)
	const old = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLD old-key"
	h.VPS.AddUser("proxier", old)
	h.VPS.SetSudoers("# written by an earlier attempt\nproxier ALL=(ALL) ALL\n")
	id := h.Create(h.Form())
	h.Drain()
	active(t, h, id)
	if exists, locked := h.VPS.HasUser("proxier"); !exists || !locked {
		t.Errorf("user exists=%v locked=%v", exists, locked)
	}
	if h.VPS.Sudoers() != remote.SudoersLine {
		t.Errorf("sudoers %q was not rewritten", h.VPS.Sudoers())
	}
	keys := h.VPS.AuthorizedKeys("proxier")
	if len(keys) != 2 || keys[0] != old {
		t.Fatalf("keys: %v", keys)
	}
	line, _, _ := h.App.SSH.PublicKey(bg)
	if keys[1] != line {
		t.Errorf("Proxier's key: %q", keys[1])
	}
}

func TestBadSudoersIsNotInstalled(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.VisudoFails = true
	h.VPS.SetSudoers("# the original\n")
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install-access", "visudo")
	if h.VPS.Sudoers() != "# the original\n" {
		t.Errorf("the original sudoers was touched: %q", h.VPS.Sudoers())
	}
	if got := h.JobSecrets(h.Job(id).ID)["root_password"]; got != serverstest.DefaultRootPassword {
		t.Errorf("the password was not kept: %q", got)
	}
}

func TestSudoRequiresTTY(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.RequireTTY = true
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install-access", "a terminal is required")
	if got := h.JobSecrets(h.Job(id).ID)["root_password"]; got == "" {
		t.Error("the password was erased though sudo does not work")
	}
}

func TestRootPasswordIsErased(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.DockerInstallFails = true // stops the job after install-access
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "base-bootstrap", "")
	jobID := h.Job(id).ID
	if got := h.JobSecrets(jobID); len(got) != 0 {
		t.Fatalf("the password is still stored: %v", got)
	}
	pw := serverstest.DefaultRootPassword
	var dump strings.Builder
	dump.WriteString(h.Log(jobID))
	for _, q := range []string{`SELECT payload FROM jobs`, `SELECT payload FROM events`, `SELECT message FROM job_log`, `SELECT failed_error FROM servers_servers`, `SELECT params FROM servers_servers`} {
		var col []string
		if err := h.App.DB.R.Select(&col, q); err != nil && !strings.Contains(err.Error(), "no such") {
			t.Fatalf("%s: %v", q, err)
		}
		dump.WriteString(strings.Join(col, "\n"))
	}
	if strings.Contains(dump.String(), pw) {
		t.Fatal("the root password appears in the job log, a payload, an event or the server row")
	}
	for _, c := range h.VPS.Commands() {
		if strings.Contains(c, pw) {
			t.Fatalf("the password was sent as part of a command: %s", c)
		}
	}
	// A retry after install-access does not ask for it.
	h.VPS.DockerInstallFails = false
	retry(t, h, id, "", false)
	active(t, h, id)
}

func TestPersonalKeysInstalled(t *testing.T) {
	h := serverstest.NewHarness(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	personal := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " me@laptop"
	if err := h.App.Settings.Set(bg, "admin", "ssh", map[string]string{"ssh.personal_keys": personal + "\n"}); err != nil {
		t.Fatal(err)
	}
	id := h.Create(h.Form())
	h.Drain()
	active(t, h, id)
	keys := h.VPS.AuthorizedKeys("proxier")
	line, _, _ := h.App.SSH.PublicKey(bg)
	if len(keys) != 2 || keys[0] != line || keys[1] != personal {
		t.Fatalf("keys on the server: %q", keys)
	}
}

func TestDNSConflictThenOverwrite(t *testing.T) {
	h := serverstest.NewHarness(t)
	host := "nl-1.hosts.tikhonnnnn.com"
	h.CF.AddRecord("tikhonnnnn.com", host, "203.0.113.9", "")
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "dns", "203.0.113.9")
	if recs := h.CF.Records("tikhonnnnn.com"); len(recs) != 1 || recs[0].Content != "203.0.113.9" {
		t.Fatalf("the foreign record was touched: %+v", recs)
	}
	// Retrying without Overwrite stops at the same place.
	retry(t, h, id, "", false)
	failed(t, h, id, "dns", "203.0.113.9")
	// Overwrite is the admin's explicit choice.
	retry(t, h, id, "", true)
	active(t, h, id)
	recs := h.CF.Records("tikhonnnnn.com")
	if len(recs) != 1 || recs[0].Content != "127.0.0.1" || recs[0].Comment != "proxier:nl-1" {
		t.Fatalf("records after the overwrite: %+v", recs)
	}
}

func TestDNSWaitTimeoutThenRetry(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.HideDNS(true)
	h.Mod.Waiter.Timeout = 50e6
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "dns", "not visible")
	if n := len(h.CF.Records("tikhonnnnn.com")); n != 1 {
		t.Fatalf("%d records", n)
	}
	h.HideDNS(false)
	retry(t, h, id, "", false)
	active(t, h, id)
	if n := len(h.CF.Records("tikhonnnnn.com")); n != 1 {
		t.Fatalf("a retry made %d records", n)
	}
	if recs, _ := store.DNSRecords(bg, h.App.DB.R, id); len(recs) != 1 {
		t.Fatalf("%d stored records", len(recs))
	}
	// The retry resumed at DNS: the steps before it were not run again.
	steps, _ := h.App.Jobs.Steps(bg, h.Job(id).ID)
	if steps[0].CarriedFrom == 0 || steps[1].CarriedFrom == 0 || steps[2].CarriedFrom == 0 || steps[3].CarriedFrom != 0 {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestCertbotRateLimited(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.CertbotRateLimited = true
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install", "too many certificates")
	// Activate anyway is for the smoke test only.
	if err := h.Mod.Provision.ActivateAnyway(bg, id, "admin"); err == nil {
		t.Fatal("Activate anyway worked after an install failure")
	}
}

func TestResumeAtBaseBootstrap(t *testing.T) {
	h := serverstest.NewHarness(t)
	reached, release := h.VPS.Hold(remote.OpDockerInstall)
	id := h.Create(h.Form())
	<-reached
	// The process stops in the middle of "Install Docker".
	h.StopJobs()
	if st := h.Job(id).State; st != jobs.Interrupted {
		t.Fatalf("job %s, want interrupted", st)
	}
	release()
	h.StartJobs()
	h.Drain()
	active(t, h, id)
	// Running the step again was harmless: one drop-in, no duplicate keys, ufw on.
	if h.VPS.AptRuns() != 2 {
		t.Errorf("apt ran %d times, want 2 (once before the stop, once on resume)", h.VPS.AptRuns())
	}
	if keys := h.VPS.AuthorizedKeys("proxier"); len(keys) != 1 {
		t.Errorf("keys: %v", keys)
	}
	if h.VPS.SSHDDropin() != remote.SSHDDropin {
		t.Errorf("drop-in %q", h.VPS.SSHDDropin())
	}
	steps, _ := h.App.Jobs.Steps(bg, h.Job(id).ID)
	if steps[0].State != "succeeded" || steps[1].State != "succeeded" {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestSmokeTestStalls(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.Proxy.SetMode(proxytest.StallAfter(16 << 10))
	id := h.Create(h.Form())
	h.Drain()
	srv := failed(t, h, id, "smoke-test", "stalled")
	if !strings.Contains(srv.FailedError, "the proxy test through main failed") {
		t.Errorf("error %q", srv.FailedError)
	}
	log := h.Log(h.Job(id).ID)
	if strings.Count(log, "Proxy test through main, attempt") != 3 {
		t.Errorf("want 3 attempts:\n%s", log)
	}
	// Everything before the smoke test is in place, and the deployment is closed as failed.
	if eps, _ := store.Endpoints(bg, h.App.DB.R, id); len(eps) != 1 {
		t.Errorf("endpoints: %v", eps)
	}
	if dep, ok, _ := store.FailedDeployment(bg, h.App.DB.R, id, "provision"); !ok || dep.State != "failed" {
		t.Errorf("deployment: %+v", dep)
	}
	// The event notifies (not cancelled).
	ev := h.Events("server.provisioning_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != "smoke-test" || ev[0].Payload["cancelled"] != nil {
		t.Fatalf("events: %+v", ev)
	}
}

func TestActivateAnyway(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.Proxy.SetMode(proxytest.StallAfter(16 << 10))
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "smoke-test", "stalled")
	if err := h.Mod.Provision.ActivateAnyway(bg, id, "admin"); err != nil {
		t.Fatal(err)
	}
	srv := active(t, h, id)
	if srv.Health != "unknown" || srv.FailedStep != "" || srv.FailedError != "" {
		t.Fatalf("server: %+v", srv)
	}
	ev := h.Events("server.activated")
	if len(ev) != 1 || ev[0].Payload["forced"] != true || ev[0].Actor != "admin" {
		t.Fatalf("events: %+v", ev)
	}
	// The files the failed run deployed are the server's current files.
	if cur, ok, _ := store.CurrentDeployment(bg, h.App.DB.R, id); !ok || cur.State != "succeeded" {
		t.Fatalf("current deployment: %+v %v", cur, ok)
	}
	// Only once.
	if err := h.Mod.Provision.ActivateAnyway(bg, id, "admin"); err == nil {
		t.Error("an active server was activated again")
	}
}

func TestActivateAnywayOnlyAfterSmokeTest(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.ComposeUpFails = "port is already allocated"
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install", "port is already allocated")
	if err := h.Mod.Provision.ActivateAnyway(bg, id, "admin"); err != provision.ErrNotAllowed {
		t.Fatalf("err = %v", err)
	}
	if h.Server(id).State != "failed" {
		t.Error("the server changed")
	}
}

func TestCancelDuringDNS(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.HideDNS(true)
	h.Mod.Waiter.Timeout = 60e9
	id := h.Create(h.Form())
	jobID := h.Job(id).ID
	h.WaitFor("the DNS wait", func() bool { return strings.Contains(h.Log(jobID), "Waiting for resolvers") })
	if err := h.App.Jobs.Cancel(bg, jobID, "admin"); err != nil {
		t.Fatal(err)
	}
	h.Drain()
	if st := h.Job(id).State; st != jobs.Cancelled {
		t.Fatalf("job %s", st)
	}
	failed(t, h, id, "dns", "cancelled")
	if n := len(h.CF.Records("tikhonnnnn.com")); n != 1 {
		t.Errorf("the record must stay until retirement or retry: %d", n)
	}
	ev := h.Events("server.provisioning_failed")
	if len(ev) != 1 || ev[0].Payload["cancelled"] != true {
		t.Fatalf("events: %+v", ev)
	}
	// A cancelled failure never notifies.
	var n int
	if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM notifications`); err != nil || n != 0 {
		t.Errorf("%d notifications (%v)", n, err)
	}
	// Retry picks it up again.
	h.HideDNS(false)
	retry(t, h, id, "", false)
	active(t, h, id)
}

func TestRetryKeepsVersion(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.ComposeUpFails = "port is already allocated"
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install", "")
	v2 := h.PublishVersion(func(files map[string][]byte) {
		files["site/index.html"] = append(files["site/index.html"], []byte("<!-- version two -->")...)
	}, true)
	if v2 != 2 {
		t.Fatalf("published v%d", v2)
	}
	h.VPS.ComposeUpFails = ""
	retry(t, h, id, "", false)
	srv := active(t, h, id)
	if srv.TemplateVersion != 1 {
		t.Fatalf("the retry moved the server to v%d", srv.TemplateVersion)
	}
	// And a new server gets the new default.
	if b, _ := h.VPS.File("/opt/proxier/vless-xhttp/site/index.html"); strings.Contains(string(b), "version two") {
		t.Error("the retry deployed the new version")
	}
}

func TestProvisionLogIsRedacted(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.PublishVersion(func(files map[string][]byte) {
		m := string(files["manifest.yaml"])
		m = strings.Replace(m, "generated:", "  - key: api_secret\n    label: API secret\n    type: string\n    secret: true\n    required: false\n    sample: s3cret-sample\n\ngenerated:", 1)
		m = strings.Replace(m, "    - compose-up: { pull: true }", "    - run: echo {{ .Params.api_secret }}\n    - compose-up: { pull: true }", 1)
		files["manifest.yaml"] = []byte(m)
	}, true)
	h.VPS.RunHooks["echo hunter2-api-secret"] = func(v *serverstest.VPS, dir string, out, _ io.Writer) int {
		cfg, _ := v.File(dir + "/xray-config.json") // prints the generated uuid and path too
		_, _ = out.Write([]byte("api secret is hunter2-api-secret\n" + string(cfg)))
		return 0
	}
	f := h.Form()
	f.Params["api_secret"] = "hunter2-api-secret"
	id := h.Create(f)
	h.Drain()
	active(t, h, id)
	log := h.Log(h.Job(id).ID)
	if !strings.Contains(log, "api secret is") {
		t.Fatalf("the hook did not run:\n%s", log)
	}
	if strings.Contains(log, "hunter2-api-secret") {
		t.Error("the secret parameter is in the log")
	}
	rows, _ := store.GeneratedValues(bg, h.App.DB.R, id)
	if len(rows) != 3 {
		t.Fatalf("%d generated values", len(rows))
	}
	eps, _ := store.Endpoints(bg, h.App.DB.R, id)
	ep, err := openEndpoint(h, eps[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{ep.Credential, ep.Params["path"]} {
		if secret == "" || strings.Contains(log, secret) {
			t.Errorf("generated value %q in the log", secret)
		}
	}
	if !strings.Contains(log, jobs.Redacted) {
		t.Error("nothing was replaced by the redaction marker")
	}
}

func TestRetryAfterAccessNeedsNoPassword(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.DockerInstallFails = true
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "base-bootstrap", "")
	h.VPS.DockerInstallFails = false
	// no password asked, none needed
	if _, err := h.Mod.Provision.Retry(bg, id, "", false, "admin"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	h.Drain()
	active(t, h, id)
}

func TestRetryReopensDeployment(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.ComposeUpFails = "port is already allocated"
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "install", "")
	first, ok, _ := store.FailedDeployment(bg, h.App.DB.R, id, "provision")
	if !ok || !first.Uploaded {
		t.Fatalf("the failed run left %+v", first)
	}
	h.VPS.ComposeUpFails = ""
	newJob, err := h.Mod.Provision.Retry(bg, id, "", false, "admin")
	if err != nil {
		t.Fatal(err)
	}
	// The deployment is running again for the new job before the job finishes.
	h.Drain()
	active(t, h, id)
	var all []store.Deployment
	if err := h.App.DB.R.Select(&all, `SELECT id, server_id, kind, state, uploaded, job_id FROM servers_deployments`); err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != first.ID || all[0].State != "succeeded" || !all[0].Uploaded || all[0].JobID.Int64 != newJob {
		t.Fatalf("deployments: %+v (new job %d)", all, newJob)
	}
	cur, ok, _ := store.CurrentDeployment(bg, h.App.DB.R, id)
	if !ok || cur.ID != first.ID {
		t.Fatalf("current deployment: %+v", cur)
	}
}

func TestInstallAccessResumesAfterPasswordErased(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.DockerInstallFails = true
	id := h.Create(h.Form())
	h.Drain()
	failed(t, h, id, "base-bootstrap", "")
	jobID := h.Job(id).ID
	if len(h.JobSecrets(jobID)) != 0 {
		t.Fatal("the password is still stored")
	}
	// As after a restart between the erasure and the end of install-access:
	// the step is not done, the password is gone.
	if _, err := h.App.DB.W.Exec(`UPDATE job_steps SET state = 'failed' WHERE job_id = ? AND name = 'install-access'`, jobID); err != nil {
		t.Fatal(err)
	}
	h.VPS.DockerInstallFails = false
	retry(t, h, id, "", false)
	active(t, h, id)
	if !strings.Contains(h.Log(h.Job(id).ID), "already in place") {
		t.Errorf("install-access did not resume through the key login:\n%s", h.Log(h.Job(id).ID))
	}
}

func TestInstallStepsMapToJobSteps(t *testing.T) {
	h := serverstest.NewHarness(t)
	id := h.Create(h.Form())
	h.Drain()
	active(t, h, id)
	steps, err := h.App.Jobs.Steps(bg, h.Job(id).ID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	want := "preflight install-access base-bootstrap dns generated-values upload-files install endpoints self-check smoke-test activate"
	if strings.Join(names, " ") != want {
		t.Fatalf("job steps: %v", names)
	}
	// On the server: bootstrap and upload ran once each; then the install list in order.
	count := map[remote.Op]int{}
	var order []remote.Op
	for _, c := range h.VPS.Commands() {
		call, ok := remote.Parse(strings.SplitN(c, ": ", 2)[1])
		if !ok {
			continue
		}
		count[call.Op]++
		switch call.Op {
		case remote.OpRun, remote.OpComposePull, remote.OpComposeUp:
			order = append(order, call.Op)
		case remote.OpHTTP:
			if len(order) == 3 { // the install's wait-http; later ones are the self-check's
				order = append(order, call.Op)
			}
		}
	}
	if count[remote.OpApt] != 1 || count[remote.OpPrepareDirs] != 1 {
		t.Errorf("bootstrap ran %d times and the upload %d", count[remote.OpApt], count[remote.OpPrepareDirs])
	}
	if got := fmt.Sprint(order); got != fmt.Sprint([]remote.Op{remote.OpRun, remote.OpComposePull, remote.OpComposeUp, remote.OpHTTP}) {
		t.Errorf("install order: %v", order)
	}
}

func openEndpoint(h *serverstest.Harness, row store.EndpointRow) (endpoint.Endpoint, error) {
	return sealed.OpenEndpoint(h.App.Vault, row)
}
