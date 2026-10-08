package retire_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare/cloudflaretest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/retire"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

var bg = context.Background()

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// stubbed is an active server on a harness whose proxy tests are stubs.
func stubbed(t *testing.T) (*serverstest.Harness, int64) {
	t.Helper()
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	h.Mod.Retire.ConnectTimeout = time.Second
	h.Mod.Retire.WaitPoll = 5 * time.Millisecond
	id := h.Provisioned()
	if s := h.Server(id); s.State != "active" {
		t.Fatalf("server %s: %s %s", s.State, s.FailedStep, s.FailedError)
	}
	return h, id
}

// ran lists the arguments of every command of an op the fake VPS saw.
func ran(h *serverstest.Harness, op remote.Op) [][]string {
	var out [][]string
	for _, line := range h.VPS.Commands() {
		_, cmd, _ := strings.Cut(line, ": ")
		if c, ok := remote.Parse(cmd); ok && c.Op == op {
			out = append(out, c.Args)
		}
	}
	return out
}

func stackDir(t *testing.T, h *serverstest.Harness, id int64) string {
	t.Helper()
	cs, err := h.Mod.Deploy.CheckSetup(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return cs.Dir
}

func retireJob(t *testing.T, h *serverstest.Harness, id int64) jobs.Job {
	t.Helper()
	s := h.Server(id)
	if !s.RetireJobID.Valid {
		t.Fatal("the server has no retire job")
	}
	j, err := h.App.Jobs.Job(bg, s.RetireJobID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func mustRetire(t *testing.T, h *serverstest.Harness, id int64, removeStack bool) int64 {
	t.Helper()
	name := h.Server(id).Name
	job, err := h.Mod.Retire.Retire(bg, id, name, removeStack, "admin")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	return job
}

func strs(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestRetireReachableServer(t *testing.T) {
	h, id := stubbed(t)
	dir := stackDir(t, h, id)
	if len(h.VPS.Files(dir)) == 0 {
		t.Fatal("the stack has no files on the server")
	}
	h.StopJobs()
	mustRetire(t, h, id, true)
	// The server is out of service at once, before the job has run.
	if s := h.Server(id); !s.Retiring() || s.State != "active" {
		t.Fatalf("right after Retire: %s, retiring %v", s.State, s.Retiring())
	}
	h.StartJobs()
	h.Drain()

	s := h.Server(id)
	if s.State != "retired" || s.RetiredAt.IsZero() || s.Health != "" || s.Retiring() {
		t.Fatalf("after: state %s, retired at %v, health %q, retiring %v", s.State, s.RetiredAt, s.Health, s.Retiring())
	}
	if j := retireJob(t, h, id); j.State != jobs.Succeeded {
		t.Fatalf("job %s: %s\n%s", j.State, j.Error, h.Log(j.ID))
	}
	if down := ran(h, remote.OpComposeDown); len(down) != 1 || down[0][1] != "true" {
		t.Errorf("compose down: %v", down)
	}
	if rm := ran(h, remote.OpRemoveDir); len(rm) != 1 || rm[0][0] != dir {
		t.Errorf("rm -rf: %v, want %s", rm, dir)
	}
	if files := h.VPS.Files(dir); len(files) != 0 {
		t.Errorf("files left on the server: %v", files)
	}
	if recs := h.CF.Records("tikhonnnnn.com"); len(recs) != 0 {
		t.Errorf("DNS records left: %+v", recs)
	}
	list, err := h.Mod.EndpointCatalog().Active(bg)
	if err != nil || len(list) != 0 {
		t.Errorf("the catalog still lists it: %+v %v", list, err)
	}
	ev := h.Events("server.retired")
	if len(ev) != 1 {
		t.Fatalf("server.retired events: %d", len(ev))
	}
	pl := ev[0].Payload
	if got := strs(pl["dns_removed"]); len(got) != 1 || got[0] != "nl-1.hosts.tikhonnnnn.com" || pl["stack_removed"] != true {
		t.Errorf("payload: %v", pl)
	}
	if kept, _ := pl["dns_kept"].([]any); len(kept) != 0 {
		t.Errorf("dns_kept: %v", kept)
	}
	if ev[0].Actor != retireJob(t, h, id).Actor() {
		t.Errorf("actor %q", ev[0].Actor)
	}
}

func TestRetireUnreachableServer(t *testing.T) {
	h, id := stubbed(t)
	h.VPS.Close()
	job := mustRetire(t, h, id, true)
	h.Drain()

	if s := h.Server(id); s.State != "retired" {
		t.Fatalf("state %s", s.State)
	}
	log := h.Log(job)
	if !strings.Contains(log, "Skipped: unreachable") {
		t.Errorf("the log does not say the stack step was skipped:\n%s", log)
	}
	if recs := h.CF.Records("tikhonnnnn.com"); len(recs) != 0 {
		t.Errorf("DNS records left: %+v", recs)
	}
	if pl := h.Events("server.retired")[0].Payload; pl["stack_removed"] != false || len(strs(pl["dns_removed"])) != 1 {
		t.Errorf("payload: %v", pl)
	}
}

func TestRetireKeepsForeignRecord(t *testing.T) {
	h, id := stubbed(t)
	recs := h.CF.Records("tikhonnnnn.com")
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	h.CF.Change(recs[0].ID, func(r *cloudflaretest.Record) { r.Content = "198.51.100.9" })
	job := mustRetire(t, h, id, true)
	h.Drain()

	if s := h.Server(id); s.State != "retired" {
		t.Fatalf("state %s", s.State)
	}
	if left := h.CF.Records("tikhonnnnn.com"); len(left) != 1 || left[0].Content != "198.51.100.9" {
		t.Errorf("the record someone else points elsewhere must stay: %+v", left)
	}
	pl := h.Events("server.retired")[0].Payload
	kept, _ := pl["dns_kept"].([]any)
	if len(kept) != 1 || len(strs(pl["dns_removed"])) != 0 {
		t.Fatalf("payload: %v", pl)
	}
	k := kept[0].(map[string]any)
	if k["name"] != "nl-1.hosts.tikhonnnnn.com" || k["reason"] != "points to 198.51.100.9" {
		t.Errorf("kept: %v", k)
	}
	var reason string
	if err := h.App.DB.R.Get(&reason, `SELECT kept FROM servers_dns_records WHERE server_id = ?`, id); err != nil || reason != "points to 198.51.100.9" {
		t.Errorf("stored reason %q %v", reason, err)
	}
	if !strings.Contains(h.Log(job), "Kept the DNS record") {
		t.Errorf("log:\n%s", h.Log(job))
	}
}

func TestRetireCancelsProvisioning(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	h.Mod.Retire.WaitPoll = 5 * time.Millisecond
	reached, release := h.VPS.Hold(remote.OpComposeUp)
	defer release()
	id := h.Create(h.Form())
	<-reached
	prov := h.Job(id)
	if prov.State != jobs.Running {
		t.Fatalf("provisioning is %s", prov.State)
	}

	job := mustRetire(t, h, id, true)
	h.WaitFor("the provisioning job to be cancelled", func() bool {
		j, err := h.App.Jobs.Job(bg, prov.ID)
		return err == nil && j.State == jobs.Cancelled
	})
	release()
	h.Drain()

	j, _ := h.App.Jobs.Job(bg, prov.ID)
	if j.CancelRequestedBy != "job:"+itoa(job) {
		t.Errorf("provisioning was cancelled by %q", j.CancelRequestedBy)
	}
	if s := h.Server(id); s.State != "retired" {
		t.Fatalf("state %s: the retire log:\n%s", s.State, h.Log(job))
	}
	if rj := retireJob(t, h, id); rj.State != jobs.Succeeded {
		t.Errorf("retire job %s: %s", rj.State, rj.Error)
	}
	if ev := h.Events("server.provisioning_failed"); len(ev) != 1 || ev[0].Payload["cancelled"] != true {
		t.Errorf("provisioning events: %+v", ev)
	}
	if !strings.Contains(h.Log(job), "Cancelling job #"+itoa(prov.ID)) {
		t.Errorf("log:\n%s", h.Log(job))
	}
}

func TestRetireFailedBeforeDNS(t *testing.T) {
	h := serverstest.NewHarness(t)
	h.VPS.Close() // provisioning fails at preflight, before DNS
	id := h.Create(h.Form())
	h.Drain()
	if s := h.Server(id); s.State != "failed" || s.FailedStep != "preflight" {
		t.Fatalf("setup: %s at %s", s.State, s.FailedStep)
	}
	if c, err := h.Mod.Retire.Consequences(bg, id); err != nil || len(c.DNS) != 0 || c.StackPossible {
		t.Fatalf("consequences: %+v %v", c, err)
	}
	// Nothing was uploaded: asking for the stack's removal does nothing.
	job := mustRetire(t, h, id, true)
	h.Drain()

	if s := h.Server(id); s.State != "retired" {
		t.Fatalf("state %s\n%s", s.State, h.Log(job))
	}
	log := h.Log(job)
	if !strings.Contains(log, "No DNS records to delete") || strings.Contains(log, "Skipped: unreachable") {
		t.Errorf("log:\n%s", log)
	}
	if pl := h.Events("server.retired")[0].Payload; len(strs(pl["dns_removed"])) != 0 || pl["stack_removed"] != false {
		t.Errorf("payload: %v", pl)
	}
}

func TestNamesAreNeverReusedAfterRetire(t *testing.T) {
	h, _ := stubbed(t)
	second := h.AddServer("10.77.0.2")
	if got := h.Server(second).Name; got != "nl-2" {
		t.Fatalf("second server: %s", got)
	}
	mustRetire(t, h, second, false)
	h.Drain()
	if s := h.Server(second); s.State != "retired" {
		t.Fatalf("state %s", s.State)
	}
	h.VPS.Unharden()
	f := h.Form()
	f.IP = "10.77.0.3"
	third := h.Create(f)
	if got := h.Server(third).Name; got != "nl-3" {
		t.Fatalf("the next server in nl is %s, not nl-3", got)
	}
}

func TestRetireErasesSecretsAndForgetsHost(t *testing.T) {
	h, id := stubbed(t)
	subject := "server:" + itoa(id)
	pinned := func() int {
		hosts, err := h.App.SSH.KnownHosts(bg)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, k := range hosts {
			if k.Subject == subject {
				n++
			}
		}
		return n
	}
	if pinned() == 0 {
		t.Fatal("provisioning pinned no host key")
	}
	cur, ok, err := store.CurrentDeployment(bg, h.App.DB.R, id)
	if err != nil || !ok {
		t.Fatalf("no current deployment: %v", err)
	}
	filesBefore, _ := store.DeployedFiles(bg, h.App.DB.R, cur.ID)
	if vals, _ := store.GeneratedValues(bg, h.App.DB.R, id); len(vals) == 0 {
		t.Fatal("no generated values to erase")
	}

	mustRetire(t, h, id, true)
	h.Drain()

	if vals, _ := store.GeneratedValues(bg, h.App.DB.R, id); len(vals) != 0 {
		t.Errorf("generated values left: %d", len(vals))
	}
	if eps, _ := store.Endpoints(bg, h.App.DB.R, id); len(eps) != 0 {
		t.Errorf("endpoints left: %d", len(eps))
	}
	if files, _ := store.DeployedFiles(bg, h.App.DB.R, cur.ID); len(files) == 0 || len(files) != len(filesBefore) {
		t.Errorf("deployed files: %d, were %d (they are kept)", len(files), len(filesBefore))
	}
	if pinned() != 0 {
		t.Error("the host key is still pinned")
	}
	if len(h.Events("ssh.host_forgotten")) == 0 {
		t.Error("no ssh.host_forgotten event")
	}
}

func TestRetireNameMustMatch(t *testing.T) {
	h, id := stubbed(t)
	for _, typed := range []string{"", "nl-2", "NL-1", "nl-1 x"} {
		if _, err := h.Mod.Retire.Retire(bg, id, typed, true, "admin"); !errors.Is(err, retire.ErrNameMismatch) {
			t.Errorf("typed %q: %v", typed, err)
		}
	}
	if s := h.Server(id); s.Retiring() || s.State != "active" {
		t.Fatalf("a wrong name changed the server: %s", s.State)
	}

	// The handler refuses it too, and the page keeps the button disabled.
	rec := h.Login.Post("/servers/"+itoa(id)+"/retire", map[string][]string{"name": {"nl-9"}, "remove_stack": {"1"}})
	if rec.Code != 422 {
		t.Fatalf("POST with the wrong name: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "does not match") || !strings.Contains(body, "data-confirm-submit") || !strings.Contains(body, "disabled") {
		t.Errorf("the refusal page:\n%s", body)
	}
	if s := h.Server(id); s.Retiring() {
		t.Fatal("the handler started a retirement for a wrong name")
	}
	get := h.Login.Get("/servers/" + itoa(id) + "/retire")
	if get.Code != 200 || !strings.Contains(get.Body.String(), "disabled") || !strings.Contains(get.Body.String(), `data-confirm-name="nl-1"`) {
		t.Errorf("the dialog page: %d\n%s", get.Code, get.Body)
	}
	// And the right name works.
	rec = h.Login.Post("/servers/"+itoa(id)+"/retire", map[string][]string{"name": {"nl-1"}})
	if rec.Code != 200 && rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("POST with the right name: %d\n%s", rec.Code, rec.Body)
	}
	if !h.Server(id).Retiring() && h.Server(id).State != "retired" {
		t.Fatal("the right name did not start the retirement")
	}
	h.Drain()
}

func TestRetiredServerIsReadOnly(t *testing.T) {
	h, id := stubbed(t)
	// Every secret the server held: none may reach a page after retirement.
	rows, err := store.GeneratedValues(bg, h.App.DB.R, id)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := sealed.OpenGenerated(h.App.Vault, id, rows)
	if err != nil || len(gen) == 0 {
		t.Fatalf("generated values: %v %v", gen, err)
	}
	mustRetire(t, h, id, true)
	h.Drain()
	if h.Server(id).State != "retired" {
		t.Fatal("not retired")
	}
	dep, ok, err := store.CurrentDeployment(bg, h.App.DB.R, id)
	if err != nil || !ok {
		t.Fatalf("no deployment: %v", err)
	}
	sid := itoa(id)
	var bodies []string
	get := func(path string) string {
		t.Helper()
		rec := h.Login.Get(path)
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
		return rec.Body.String()
	}

	over := get("/servers/" + sid)
	for _, want := range []string{"Retired on", "nl-1"} {
		if !strings.Contains(over, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	for _, not := range []string{"/redeploy", "/rotate", "/retire", "/checks/run", "/retry", "/activate", `name="notes"`, "/upgrade", "/params"} {
		if strings.Contains(over, not) {
			t.Errorf("the retired server's page offers %q", not)
		}
	}
	stack := get("/servers/" + sid + "/stack")
	if !strings.Contains(stack, "Deployments") || !strings.Contains(stack, "succeeded") || !strings.Contains(stack, `href="/jobs/`) {
		t.Error("the Stack tab does not list the deployments")
	}
	for _, not := range []string{"/stack/files/", "/stack/diff/", "stack-code", "/rollback"} {
		if strings.Contains(stack, not) {
			t.Errorf("the Stack tab of a retired server has %q", not)
		}
	}
	get("/servers")
	get("/servers?state=all")

	for _, path := range []string{
		"/servers/" + sid + "/redeploy", "/servers/" + sid + "/upgrade", "/servers/" + sid + "/params", "/servers/" + sid + "/rollback",
		"/servers/" + sid + "/rotate", "/servers/" + sid + "/pause", "/servers/" + sid + "/retry",
		"/servers/" + sid + "/stack/files/" + itoa(dep.ID) + "/docker-compose.yaml", "/servers/" + sid + "/stack/diff/" + itoa(dep.ID),
	} {
		if rec := h.Login.Get(path); rec.Code != 409 {
			t.Errorf("GET %s: %d, want 409", path, rec.Code)
		}
	}
	for _, path := range []string{
		"/plan", "/apply", "/restart", "/images", "/reboot", "/logs", "/rotate", "/checks/run", "/pause", "/resume",
		"/retry", "/activate", "/cancel", "/notes", "/retire", "/retire/retry",
	} {
		if rec := h.Login.Post("/servers/"+sid+path, map[string][]string{"name": {"nl-1"}}); rec.Code != 409 {
			t.Errorf("POST %s: %d, want 409", path, rec.Code)
		}
	}
	if rec := h.Login.Get("/servers/" + sid + "/retire"); rec.Code != 303 && rec.Code != 302 {
		t.Errorf("GET retire dialog of a retired server: %d", rec.Code)
	}
	for k, v := range gen {
		for _, b := range bodies {
			if strings.Contains(b, v) {
				t.Errorf("a response contains the erased value %s", k)
			}
		}
	}
}

func TestRetireCancelsQueuedJobs(t *testing.T) {
	h, id := stubbed(t)
	reached, release := h.VPS.Hold(remote.OpStats)
	defer release()
	if res, err := h.Mod.Health.RunNow(bg, id, "admin"); err != nil || res.Busy {
		t.Fatalf("run now: %+v %v", res, err)
	}
	<-reached // a self-check is running and holds the server

	restart, err := h.Mod.Deploy.Restart(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := h.App.Jobs.Job(bg, restart); j.State != jobs.Queued {
		t.Fatalf("the restart is %s, want queued behind the self-check", j.State)
	}
	held, _ := h.App.Jobs.Holding(bg, "server:"+itoa(id))
	var check int64
	for _, j := range held {
		if j.State == jobs.Running {
			check = j.ID
		}
	}
	if check == 0 {
		t.Fatalf("no running job holds the server: %+v", held)
	}

	job := mustRetire(t, h, id, false)
	h.WaitFor("the queued restart to be cancelled", func() bool {
		j, _ := h.App.Jobs.Job(bg, restart)
		return j.State == jobs.Cancelled
	})
	// The running self-check is waited for, not cancelled.
	time.Sleep(50 * time.Millisecond)
	if s := h.Server(id); s.State == "retired" {
		t.Fatal("retired while a self-check was still running")
	}
	if j, _ := h.App.Jobs.Job(bg, check); j.State != jobs.Running {
		t.Fatalf("the self-check is %s", j.State)
	}
	if rj, _ := h.App.Jobs.Job(bg, job); rj.State != jobs.Running {
		t.Fatalf("the retire job is %s while it waits", rj.State)
	}
	release()
	h.Drain()

	if s := h.Server(id); s.State != "retired" {
		t.Fatalf("state %s\n%s", s.State, h.Log(job))
	}
	if j, _ := h.App.Jobs.Job(bg, check); j.State != jobs.Succeeded {
		t.Errorf("the self-check ended %s: %s", j.State, j.Error)
	}
	if !strings.Contains(h.Log(job), "Waiting for job #"+itoa(check)) {
		t.Errorf("log:\n%s", h.Log(job))
	}
}

func TestRetiringServerStopsChecks(t *testing.T) {
	h, id := stubbed(t)
	h.StopJobs()
	mustRetire(t, h, id, false)

	if ids, err := store.CheckableServerIDs(bg, h.App.DB.R); err != nil || len(ids) != 0 {
		t.Errorf("a round would check %v %v", ids, err)
	}
	if list, err := h.Mod.EndpointCatalog().Active(bg); err != nil || len(list) != 0 {
		t.Errorf("the catalog offers it: %+v %v", list, err)
	}
	if _, ok, _ := h.Mod.EndpointCatalog().Server(bg, id); ok {
		t.Error("the catalog serves it by id")
	}
	if _, err := h.Mod.Health.RunNow(bg, id, "admin"); err == nil {
		t.Error("Run checks now works on a retiring server")
	}
	if _, err := h.Mod.Deploy.Restart(bg, id, "admin"); err == nil {
		t.Error("Restart works on a retiring server")
	}
	// Its DNS still points at it until the end, so routing must still refuse the names.
	names, ips, err := h.Mod.ServerHostnames().Hostnames(bg)
	if err != nil || len(names) != 1 || len(ips) != 1 {
		t.Errorf("hostnames while retiring: %v %v %v", names, ips, err)
	}
	if _, err := h.Mod.Retire.Retire(bg, id, "nl-1", false, "admin"); !errors.Is(err, retire.ErrNotAllowed) {
		t.Errorf("retiring twice: %v", err)
	}

	h.StartJobs()
	h.Drain()
	names, ips, _ = h.Mod.ServerHostnames().Hostnames(bg)
	if len(names) != 0 || len(ips) != 0 {
		t.Errorf("hostnames after retirement: %v %v", names, ips)
	}
}

// clock is the fake time of the job system, for the backoff between attempts.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestRetireFailureAndRetry(t *testing.T) {
	h, id := stubbed(t)
	h.StopJobs()
	clk := &clock{t: time.Now().UTC()}
	h.App.Jobs.Now = clk.Now
	h.StartJobs()
	// Cloudflare answers 500 to every call of three attempts; the client itself
	// tries each call four times (1 + 3 retries).
	h.CF.Fail("GET /zones/{zone}/dns_records/{id}", 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500)

	first := mustRetire(t, h, id, true)
	var j jobs.Job
	for i := 0; i < 200; i++ {
		clk.Add(6 * time.Minute) // past the backoff of 1 and 5 minutes
		time.Sleep(10 * time.Millisecond)
		j, _ = h.App.Jobs.Job(bg, first)
		if j.State == jobs.Failed {
			break
		}
	}
	if j.State != jobs.Failed || j.Attempt != 3 || j.ErrorStep != retire.StepDNS {
		t.Fatalf("job %s attempt %d at %q: %s", j.State, j.Attempt, j.ErrorStep, j.Error)
	}
	h.Drain()

	ev := h.Events("server.retire_failed")
	if len(ev) != 1 || ev[0].Payload["step"] != retire.StepDNS || !strings.Contains(ev[0].Payload["error"].(string), "nl-1.hosts") {
		t.Fatalf("events: %+v", ev)
	}
	s := h.Server(id)
	if s.State != "active" || !s.Retiring() || s.RetireJobID.Int64 != first {
		t.Fatalf("a failed retirement must leave the server retiring: %s retiring %v job %d", s.State, s.Retiring(), s.RetireJobID.Int64)
	}
	if ids, _ := store.CheckableServerIDs(bg, h.App.DB.R); len(ids) != 0 {
		t.Errorf("checks still run: %v", ids)
	}
	if list, _ := h.Mod.EndpointCatalog().Active(bg); len(list) != 0 {
		t.Errorf("the catalog offers it: %+v", list)
	}

	second, err := h.Mod.Retire.RetryFailed(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && h.Server(id).State != "retired"; i++ {
		clk.Add(6 * time.Minute)
		time.Sleep(10 * time.Millisecond)
	}
	h.Drain()
	if s := h.Server(id); s.State != "retired" || s.RetireJobID.Int64 != second {
		t.Fatalf("after the retry: %s job %d", s.State, s.RetireJobID.Int64)
	}
	if down := ran(h, remote.OpComposeDown); len(down) != 1 {
		t.Errorf("the finished steps ran again: %d compose down", len(down))
	}
	if _, err := h.Mod.Retire.RetryFailed(bg, id, "admin"); !errors.Is(err, retire.ErrNotRetiring) {
		t.Errorf("retrying a retired server: %v", err)
	}
}

func TestFinishIsIdempotent(t *testing.T) {
	h, id := stubbed(t)
	// The first half of `finish` committed and the process died before the
	// host key was forgotten.
	if err := h.App.DB.Write(bg, func(tx *sqlx.Tx) error {
		_, err := store.MarkRetired(bg, tx, id, db.At(time.Now()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	subject := "server:" + itoa(id)
	hosts, _ := h.App.SSH.KnownHosts(bg)
	pinned := 0
	for _, k := range hosts {
		if k.Subject == subject {
			pinned++
		}
	}
	if pinned == 0 {
		t.Fatal("no pinned host to forget")
	}

	if err := h.Mod.Retire.Finish(bg, id, "job:1"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mod.Retire.Finish(bg, id, "job:1"); err != nil { // and again: nothing left to do
		t.Fatal(err)
	}
	hosts, _ = h.App.SSH.KnownHosts(bg)
	for _, k := range hosts {
		if k.Subject == subject {
			t.Errorf("host %s is still pinned", k.Address)
		}
	}
	if len(h.Events("ssh.host_forgotten")) != pinned {
		t.Errorf("ssh.host_forgotten events: %d, pinned %d", len(h.Events("ssh.host_forgotten")), pinned)
	}
	if n := len(h.Events("server.retired")); n != 0 {
		t.Errorf("the re-run recorded %d server.retired events", n)
	}
}
