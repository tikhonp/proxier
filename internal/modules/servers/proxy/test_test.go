package proxy_test

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy/proxytest"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

// opts are the options for s: tests connect to 127.0.0.1 and trust its certificate.
func opts(s *proxytest.Server) proxy.Options {
	return proxy.Options{
		URL: s.ObjectURL(), Resolve: s.Resolve(), PinCert: s.CertSHA256,
		Timeout: 10 * time.Second, Stall: 2 * time.Second, Probe: 2 * time.Second,
	}
}

func TestProxyTestPasses(t *testing.T) {
	s := proxytest.New(t)
	r := proxy.Test(t.Context(), s.Endpoint(), opts(s))
	if !r.OK || r.Class != proxy.OK || r.Error != "" {
		t.Fatalf("a healthy endpoint failed: %+v", r)
	}
	if r.Bytes != 256<<10 {
		t.Errorf("bytes %d", r.Bytes)
	}
	if r.ConnectMS < 1 || r.TLSMS < 1 || r.FirstByteMS < 1 || r.ThroughputKbps < 1 {
		t.Errorf("timings missing: %+v", r)
	}
	if r.SmallObject {
		t.Error("256 KiB is not a small object")
	}
	if r.URL != s.ObjectURL() {
		t.Errorf("url %q", r.URL)
	}
}

func TestProxyTestTCPClasses(t *testing.T) {
	s := proxytest.New(t)
	o := opts(s)

	// nobody listens: refused
	s.CloseTCP()
	r := proxy.Test(t.Context(), s.Endpoint(), o)
	if r.OK || r.Class != proxy.TCPRefused || r.Error == "" {
		t.Errorf("closed port: %+v", r)
	}

	// a connect that never completes: timeout, after the probe's time and not later
	o.Probe = 150 * time.Millisecond
	o.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	r = proxy.Test(t.Context(), s.Endpoint(), o)
	if r.OK || r.Class != proxy.TCPTimeout {
		t.Errorf("blackhole: %+v", r)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("the probe took %s", d)
	}
}

func TestProxyTestTLSFailed(t *testing.T) {
	s := proxytest.New(t)
	s.RejectTLS()
	r := proxy.Test(t.Context(), s.Endpoint(), opts(s))
	if r.OK || r.Class != proxy.TLSFailed || r.ConnectMS < 1 || r.TLSMS != 0 {
		t.Errorf("TLS rejected: %+v", r)
	}
	// a certificate that is not the trusted one fails the same way
	s2 := proxytest.New(t)
	o := opts(s2)
	o.PinCert = s.CertSHA256
	if r := proxy.Test(t.Context(), s2.Endpoint(), o); r.OK || r.Class != proxy.TLSFailed {
		t.Errorf("wrong certificate: %+v", r)
	}
}

func TestProxyTestStalled(t *testing.T) {
	s := proxytest.New(t)
	s.SetMode(proxytest.StallAfter(16 << 10))
	o := opts(s)
	o.Stall = time.Second
	start := time.Now()
	r := proxy.Test(t.Context(), s.Endpoint(), o)
	if r.OK || r.Class != proxy.Stalled || r.Bytes != 16<<10 {
		t.Fatalf("stalled object: %+v", r)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("a one second stall took %s to notice", d)
	}
	if want := "16 KB"; !strings.Contains(r.Error, want) {
		t.Errorf("the error should say how much arrived (%s): %q", want, r.Error)
	}
}

func TestProxyTestHTTPError(t *testing.T) {
	s := proxytest.New(t)
	s.SetMode(proxytest.Status(503))
	r := proxy.Test(t.Context(), s.Endpoint(), opts(s))
	if r.OK || r.Class != proxy.HTTPError || !strings.Contains(r.Error, "503") {
		t.Errorf("503: %+v", r)
	}
}

func TestProxyTestTimeout(t *testing.T) {
	s := proxytest.New(t)
	// Slow, but not so slow that nothing arrives for the stall time: the
	// overall deadline is what ends it.
	s.SetMode(proxytest.Slow(900*time.Millisecond, 1<<10))
	o := opts(s)
	o.Timeout, o.Stall = 400*time.Millisecond, 5*time.Second
	r := proxy.Test(t.Context(), s.Endpoint(), o)
	if r.OK || r.Class != proxy.Timeout {
		t.Errorf("deadline: %+v", r)
	}
}

func TestProxyTestWrongCredentialFails(t *testing.T) {
	s := proxytest.New(t)
	o := opts(s)
	o.Stall = time.Second

	old := s.Endpoint() // what a client holds before the rotation
	if r := proxy.Test(t.Context(), old, opts(s)); !r.OK {
		t.Fatalf("before the rotation: %+v", r)
	}
	s.Rotate(uuid.NewString(), "")
	if r := proxy.Test(t.Context(), old, o); r.OK || r.Class == proxy.OK || r.Error == "" {
		t.Errorf("the old credential passed: %+v", r)
	}
	if r := proxy.Test(t.Context(), s.Endpoint(), opts(s)); !r.OK {
		t.Errorf("the new credential failed: %+v", r)
	}
	// and the path is part of it
	old = s.Endpoint()
	s.Rotate("", "/rotated-path")
	if r := proxy.Test(t.Context(), old, o); r.OK {
		t.Errorf("the old path passed: %+v", r)
	}
	if r := proxy.Test(t.Context(), s.Endpoint(), opts(s)); !r.OK {
		t.Errorf("the new path failed: %+v", r)
	}
}

func TestProxyTestSmallObjectWarns(t *testing.T) {
	s := proxytest.New(t)
	s.SetMode(proxytest.OK(32 << 10))
	r := proxy.Test(t.Context(), s.Endpoint(), opts(s))
	if !r.OK || !r.SmallObject || r.Bytes != 32<<10 {
		t.Errorf("32 KB object: %+v", r)
	}
}

func TestProxyTestEdges(t *testing.T) {
	s := proxytest.New(t)
	o := opts(s)
	o.URL = ""
	if r := proxy.Test(t.Context(), s.Endpoint(), o); r.OK || r.Class != proxy.HTTPError {
		t.Errorf("no URL: %+v", r)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if r := proxy.Test(ctx, s.Endpoint(), opts(s)); r.OK || r.Error != "cancelled" {
		t.Errorf("cancelled: %+v", r)
	}
	e := s.Endpoint()
	e.Type = "wireguard"
	if r := proxy.Test(t.Context(), e, opts(s)); r.OK {
		t.Errorf("unknown type: %+v", r)
	}
}

// capture returns what fn writes to stdout. xray logs through a goroutine, so
// it waits a moment for the lines to arrive.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	var out []byte
	done := make(chan struct{})
	go func() { out, _ = io.ReadAll(r); close(done) }()
	defer func() { os.Stdout = orig }() // also when fn fails the test
	fn()
	time.Sleep(300 * time.Millisecond)
	os.Stdout = orig
	_ = w.Close()
	<-done
	return string(out)
}

// heard reports whether an instance with logging on writes to stdout. The
// instance stays up until its first line arrives: closing it stops xray's log
// goroutine, which drops whatever it has not written yet.
func heard(t *testing.T) bool {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() {
		os.Stdout = orig
		_ = w.Close()
		_ = r.Close()
	}()
	cfg, err := serial.LoadJSONConfig(strings.NewReader(`{"log":{"loglevel":"debug"},"outbounds":[{"protocol":"freedom"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := core.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inst.Close() }()
	if err := r.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _ := r.Read(make([]byte, 512))
	return n > 0
}

// TestConcurrentProxyTestsStayQuiet runs the eight tests the checks queue
// allows at once, and watches stdout: a started xray instance installs a
// process-wide log handler that would write there in its own format.
func TestConcurrentProxyTestsStayQuiet(t *testing.T) {
	// the control: an instance with logging on is heard, so silence below means something
	if !heard(t) {
		t.Fatal("the control instance wrote nothing to stdout: this test can no longer see xray's logs")
	}

	results := make([]proxy.Result, 8)
	var s *proxytest.Server
	out := capture(t, func() {
		s = proxytest.New(t)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = proxy.Test(t.Context(), s.Endpoint(), opts(s))
			}()
		}
		wg.Wait()
		// a template's own log section must not turn xray's logging on either
		if err := proxy.ValidateConfig([]byte(`{"log":{"loglevel":"debug"},"outbounds":[{"protocol":"freedom"}]}`)); err != nil {
			t.Errorf("validate: %v", err)
		}
	})
	for i, res := range results {
		if !res.OK {
			t.Errorf("test %d failed: %+v", i, res)
		}
	}
	if out != "" {
		t.Errorf("xray wrote to stdout:\n%s", out)
	}
}
