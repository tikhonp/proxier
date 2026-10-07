package serverstest

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare/cloudflaretest"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/provision"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy/proxytest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/sitetest"
)

// CFToken is the token of the fake Cloudflare account.
const CFToken = "cf-test-token"

// Harness wires the servers module with a full app (sitetest), a fake
// Cloudflare, a fake resolver, an in-process proxy server the smoke test can
// reach and a VPS to provision. Nothing touches the network.
type Harness struct {
	T     *testing.T
	Site  *sitetest.Site
	App   *platform.App
	Mod   *servers.Module
	Login *sitetest.Login
	VPS   *VPS
	CF    *cloudflaretest.Fake
	Proxy *proxytest.Server

	// LocationID is "nl" (Netherlands); TemplateID is the seed template.
	LocationID, TemplateID int64

	mu         sync.Mutex
	dnsVisible bool
	stopJobs   func()
	stopDisp   func()
	stubbed    bool
	stallSmoke bool
	accepts    func(endpoint.Endpoint) bool
}

// Option changes what NewHarness builds.
type Option func(*Harness)

// StubProxy makes the smoke test a stub that passes (or, after StallSmoke,
// stalls) instead of pushing real XHTTP traffic through an in-process xray.
// Tests outside package provision use it: xray's XHTTP client has a data
// race, and only the packages the Makefile runs without -race may drive it.
func StubProxy() Option { return func(h *Harness) { h.stubbed = true } }

// NewHarness builds everything and starts the job workers.
func NewHarness(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	mod := servers.New()
	site := sitetest.New(t, sitetest.Options{Modules: []module.Module{mod}})
	h := &Harness{T: t, Site: site, App: site.App, Mod: mod, dnsVisible: true}
	for _, o := range opts {
		o(h)
	}
	ctx := context.Background()

	// Cloudflare: a fake account owning tikhonnnnn.com, its token saved.
	h.CF = cloudflaretest.New(t, CFToken, "tikhonnnnn.com")
	mod.CloudflareClient = h.CF.ClientFor
	if err := h.App.Settings.Set(ctx, "admin", "cloudflare", map[string]string{"cloudflare.api_token": CFToken, "cloudflare.zones": "tikhonnnnn.com"}); err != nil {
		t.Fatal(err)
	}
	// The IP → country lookup is off unless a test turns it on.
	if err := h.App.Settings.Set(ctx, "admin", "servers", map[string]string{"servers.ip_country_url": ""}); err != nil {
		t.Fatal(err)
	}

	// Resolvers see what the fake Cloudflare holds, unless a test hides it.
	mod.Waiter = dns.Waiter{
		Resolvers: dns.DefaultResolvers, Every: time.Millisecond, Timeout: 3 * time.Second,
		Lookup: h.lookup,
	}

	h.VPS = NewVPS(t)

	p := mod.Provision
	p.SmokeGap = 5 * time.Millisecond
	p.RemoteEnv = func(log *jobs.Logger) remote.Env { return remote.Env{Log: log, Poll: time.Millisecond} }
	if h.stubbed {
		p.ProxyTest = func(_ context.Context, e endpoint.Endpoint, _ proxy.Options) proxy.Result {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.stallSmoke {
				return proxy.Result{Class: proxy.Stalled, Error: "no data for 5s after 16 KB received"}
			}
			if h.accepts != nil && !h.accepts(e) {
				return proxy.Result{Class: proxy.HTTPError, Error: "EOF"}
			}
			return proxy.Result{OK: true, FirstByteMS: 1, ThroughputKbps: 1000}
		}
	} else {
		// The smoke test goes to the in-process proxy with the values the
		// template generated, and downloads from its test object.
		h.Proxy = proxytest.New(t)
		p.ProxyOptions = func(_ context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Options {
			h.Proxy.Rotate(e.Credential, e.Params["path"])
			o.URL = h.Proxy.ObjectURL()
			o.Resolve = map[string]string{
				net.JoinHostPort(e.Host, strconv.Itoa(e.Port)): net.JoinHostPort("127.0.0.1", strconv.Itoa(h.Proxy.Port)),
			}
			o.PinCert = h.Proxy.CertSHA256
			o.Stall, o.Timeout = 400*time.Millisecond, 4*time.Second
			return o
		}
		// A server that does not accept an endpoint's credentials fails the
		// test at once, as the real one would after its handshake.
		p.ProxyTest = func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Result {
			h.mu.Lock()
			ok := h.accepts == nil || h.accepts(e)
			h.mu.Unlock()
			if !ok {
				return proxy.Result{Class: proxy.HTTPError, Error: "EOF"}
			}
			return proxy.Test(ctx, e, o)
		}
	}
	mod.Deploy.RebootPoll, mod.Deploy.RebootTimeout, mod.Deploy.CheckGap = time.Millisecond, 3*time.Second, time.Millisecond

	// Jobs: workers poll fast and stop quickly.
	// Every address reaches the one fake VPS, so a test can have many servers.
	h.App.SSH.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}
	h.App.Jobs.Poll, h.App.Jobs.SchedulerPoll, h.App.Jobs.Grace = 5*time.Millisecond, 50*time.Millisecond, 300*time.Millisecond
	h.StartJobs()
	t.Cleanup(h.StopJobs)
	h.App.Dispatcher.Poll = func() time.Duration { return 5 * time.Millisecond }
	h.startDispatcher()
	t.Cleanup(h.stopDispatcher)

	h.Login = site.SignIn("")

	loc, err := mod.Store.CreateLocation(ctx, "nl", "Netherlands", "NL", "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.LocationID = loc
	tpls, err := mod.Templates.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range tpls {
		if tp.Slug == seed.Slug {
			h.TemplateID = tp.ID
		}
	}
	if h.TemplateID == 0 {
		t.Fatal("the seed template is missing")
	}
	return h
}

// StartJobs runs the job workers (the harness starts them already; call it
// after StopJobs to simulate the process starting again).
func (h *Harness) StartJobs() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopJobs != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.App.Jobs.Start(ctx) }()
	h.stopJobs = func() { cancel(); <-done }
}

// startDispatcher delivers events to the subscribers (rollouts move on them).
func (h *Harness) startDispatcher() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.App.Dispatcher.Start(ctx) }()
	h.mu.Lock()
	h.stopDisp = func() { cancel(); <-done }
	h.mu.Unlock()
}

// StopDispatcher stops delivering events, so a test can hand them to a
// subscriber itself; it stays stopped.
func (h *Harness) StopDispatcher() { h.stopDispatcher() }

func (h *Harness) stopDispatcher() {
	h.mu.Lock()
	stop := h.stopDisp
	h.stopDisp = nil
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// ProxyAccepts decides which endpoints the proxy tests accept: the others fail
// at once, as a server that does not know their credentials would. nil
// accepts everything again.
func (h *Harness) ProxyAccepts(f func(endpoint.Endpoint) bool) {
	h.mu.Lock()
	h.accepts = f
	h.mu.Unlock()
}

// FollowVPS makes the proxy tests accept exactly the endpoints whose
// credential and path are in some file on the fake server: the stack there
// decides who gets in.
func (h *Harness) FollowVPS() {
	h.ProxyAccepts(func(e endpoint.Endpoint) bool {
		if !h.VPS.AnyFileContains(e.Credential) {
			return false
		}
		path := e.Params["path"]
		return path == "" || h.VPS.AnyFileContains(path)
	})
}

// StopJobs stops the workers as a shutdown does: what runs is interrupted and
// resumes at StartJobs.
func (h *Harness) StopJobs() {
	h.mu.Lock()
	stop := h.stopJobs
	h.stopJobs = nil
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// JobSecrets opens the secrets still stored with a job: empty once erased.
func (h *Harness) JobSecrets(jobID int64) map[string]string {
	h.T.Helper()
	var blob []byte
	if err := h.App.DB.R.Get(&blob, `SELECT secrets FROM jobs WHERE id = ?`, jobID); err != nil {
		h.T.Fatal(err)
	}
	out := map[string]string{}
	if len(blob) == 0 {
		return out
	}
	b, err := h.App.Vault.Open(blob, "job:"+strconv.FormatInt(jobID, 10)+":payload")
	if err != nil {
		h.T.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		h.T.Fatal(err)
	}
	return out
}

// PublishVersion publishes a new version of the seed template made from the
// latest one by change, makes it the default when asked, and returns its number.
func (h *Harness) PublishVersion(change func(files map[string][]byte), makeDefault bool) int {
	h.T.Helper()
	ctx := context.Background()
	d, err := h.Mod.Templates.EditDraft(ctx, h.TemplateID, "admin")
	if err != nil {
		h.T.Fatal(err)
	}
	files := map[string][]byte{}
	for k, v := range d.Files {
		files[k] = v
	}
	change(files)
	rev, err := h.Mod.Templates.SaveDraft(ctx, h.TemplateID, d.Revision, files, "admin", "admin")
	if err != nil {
		h.T.Fatal(err)
	}
	v, err := h.Mod.Templates.Publish(ctx, h.TemplateID, rev, "test", true, "admin")
	if err != nil {
		h.T.Fatal(err)
	}
	if makeDefault {
		if err := h.Mod.Templates.MakeDefault(ctx, h.TemplateID, v, "admin"); err != nil {
			h.T.Fatal(err)
		}
	}
	return v
}

// lookup answers resolvers from the fake Cloudflare's records.
func (h *Harness) lookup(_ context.Context, _ dns.Resolver, name string) ([]netip.Addr, string, error) {
	h.mu.Lock()
	visible := h.dnsVisible
	h.mu.Unlock()
	if !visible {
		return nil, "doh", nil
	}
	for _, r := range h.CF.Records("tikhonnnnn.com") {
		if r.Name == name {
			if a, err := netip.ParseAddr(r.Content); err == nil {
				return []netip.Addr{a}, "doh", nil
			}
		}
	}
	return nil, "doh", nil
}

// StallSmoke makes the stubbed smoke test stall, as a blocked network does
// (StubProxy only).
func (h *Harness) StallSmoke(stalls bool) {
	h.mu.Lock()
	h.stallSmoke = stalls
	h.mu.Unlock()
}

// HideDNS makes the resolvers see nothing, as before a record propagates.
func (h *Harness) HideDNS(hidden bool) {
	h.mu.Lock()
	h.dnsVisible = !hidden
	h.mu.Unlock()
}

// Form is a valid new-server form for the VPS.
func (h *Harness) Form() provision.Form {
	_, port, _ := net.SplitHostPort(h.VPS.Addr)
	n, _ := strconv.Atoi(port)
	return provision.Form{
		IP: "127.0.0.1", SSHPort: n, RootPassword: DefaultRootPassword,
		LocationID: h.LocationID, TemplateID: h.TemplateID, Params: map[string]string{},
	}
}

// Create provisions a server from f and returns its id.
func (h *Harness) Create(f provision.Form) int64 {
	h.T.Helper()
	id, err := h.Mod.Provision.Create(context.Background(), f, "admin")
	if err != nil {
		h.T.Fatalf("create: %v", err)
	}
	return id
}

// Drain waits until no job is running or due.
func (h *Harness) Drain() {
	h.T.Helper()
	deadline := time.Now().Add(60 * time.Second)
	calm := 0
	for time.Now().Before(deadline) {
		var n int
		if err := h.App.DB.R.Get(&n, `SELECT count(*) FROM jobs WHERE state IN ('running', 'interrupted', 'queued')`); err != nil {
			h.T.Fatal(err)
		}
		if n == 0 {
			if calm++; calm >= 3 {
				return
			}
		} else {
			calm = 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.T.Fatal("jobs did not become idle")
}

// WaitFor polls cond for up to 30 seconds.
func (h *Harness) WaitFor(what string, cond func() bool) {
	h.T.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.T.Fatalf("timed out waiting for %s", what)
}

// Server returns a server row.
func (h *Harness) Server(id int64) store.Server {
	h.T.Helper()
	s, err := store.GetServer(context.Background(), h.App.DB.R, id)
	if err != nil {
		h.T.Fatal(err)
	}
	return s
}

// Job returns the server's current provisioning job.
func (h *Harness) Job(serverID int64) jobs.Job {
	h.T.Helper()
	s := h.Server(serverID)
	j, err := h.App.Jobs.Job(context.Background(), s.ProvisionJobID.Int64)
	if err != nil {
		h.T.Fatal(err)
	}
	return j
}

// Log is the whole log of a job as text.
func (h *Harness) Log(jobID int64) string {
	h.T.Helper()
	lines, err := h.App.Jobs.LogTail(context.Background(), jobID, 10000)
	if err != nil {
		h.T.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Events lists the recorded events of a type, newest first.
func (h *Harness) Events(typ string) []events.Event {
	h.T.Helper()
	l, err := events.List(context.Background(), h.App.DB.R, events.Filter{Type: typ, Limit: 100})
	if err != nil {
		h.T.Fatal(err)
	}
	return l
}

// Provisioned creates a server and waits for the job to end.
func (h *Harness) Provisioned() int64 {
	h.T.Helper()
	id := h.Create(h.Form())
	h.Drain()
	return id
}

// LastJob is the id of the newest job of a type.
func (h *Harness) LastJob(typ string) int64 {
	h.T.Helper()
	var id int64
	if err := h.App.DB.R.Get(&id, `SELECT COALESCE(MAX(id), 0) FROM jobs WHERE type = ?`, typ); err != nil {
		h.T.Fatal(err)
	}
	return id
}

// AddServer provisions another server on the same fake VPS under another
// address (10.77.0.x reaches it too) and returns its id. The machine is
// "reinstalled" first so root's password works again.
func (h *Harness) AddServer(ip string) int64 {
	h.T.Helper()
	h.VPS.Unharden()
	f := h.Form()
	f.IP = ip
	id := h.Create(f)
	h.Drain()
	if s := h.Server(id); s.State != "active" {
		h.T.Fatalf("server %s on %s: %s %s: %s", s.Name, ip, s.State, s.FailedStep, s.FailedError)
	}
	return id
}
