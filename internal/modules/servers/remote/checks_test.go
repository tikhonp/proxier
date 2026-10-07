package remote_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
)

const stackDir = "/opt/proxier/vless-xhttp"

func selfCheck(t *testing.T, v *serverstest.VPS, check manifest.Check, th remote.Thresholds) remote.CheckResult {
	t.Helper()
	env := serverstest.NewSSHEnv(t)
	c := env.Access(v)
	res, err := remote.SelfCheck(context.Background(), remote.Env{}, c, stackDir, []manifest.Check{check}, th)
	if err != nil || len(res) != 1 {
		t.Fatalf("SelfCheck: %+v %v", res, err)
	}
	return res[0]
}

func TestSelfChecks(t *testing.T) {
	th := remote.Thresholds{CertWarnDays: 14, DiskWarnPct: 10, DiskFailPct: 2}
	cert := stackDir + "/certbot/conf/live/h.example/fullchain.pem"

	t.Run("compose-running", func(t *testing.T) {
		for _, array := range []bool{false, true} { // both shapes of `docker compose ps --format json`
			v := serverstest.NewVPS(t)
			v.PSArray = array
			v.SetService("xray", "running")
			v.SetService("nginx", "running")
			if r := selfCheck(t, v, manifest.Check{Kind: "compose-running"}, th); !r.Pass || !strings.Contains(r.Message, "2 containers") {
				t.Errorf("array=%v all running: %+v", array, r)
			}
			v.SetService("xray", "exited")
			if r := selfCheck(t, v, manifest.Check{Kind: "compose-running"}, th); r.Pass || !strings.Contains(r.Message, "xray: exited") {
				t.Errorf("array=%v one exited: %+v", array, r)
			}
		}
		v := serverstest.NewVPS(t)
		if r := selfCheck(t, v, manifest.Check{Kind: "compose-running"}, th); r.Pass || !strings.Contains(r.Message, "no containers") {
			t.Errorf("an empty stack passed: %+v", r)
		}
	})

	t.Run("http-local", func(t *testing.T) {
		url := "https://h.example/"
		check := manifest.Check{Kind: "http-local", Args: map[string]any{"url": url, "status": 200}}
		v := serverstest.NewVPS(t)
		v.SetService("nginx", "running")
		if r := selfCheck(t, v, check, th); !r.Pass {
			t.Errorf("200: %+v", r)
		}
		v.HTTPStatus[url] = "502"
		if r := selfCheck(t, v, check, th); r.Pass || !strings.Contains(r.Message, "answered 502, expected 200") {
			t.Errorf("502: %+v", r)
		}
		v.HTTPStatus[url] = "000"
		if r := selfCheck(t, v, check, th); r.Pass || !strings.Contains(r.Message, "no answer") {
			t.Errorf("no answer: %+v", r)
		}
	})

	t.Run("cert-expiry", func(t *testing.T) {
		check := manifest.Check{Kind: "cert-expiry", Args: map[string]any{"file": "certbot/conf/live/h.example/fullchain.pem"}}
		v := serverstest.NewVPS(t)
		if r := selfCheck(t, v, check, th); r.Pass || !strings.Contains(r.Message, "missing") {
			t.Errorf("missing: %+v", r)
		}
		v.PutFile(cert, []byte("pem"))
		for _, c := range []struct {
			days     int
			pass     bool
			expiring int
			text     string
		}{
			{-3, false, 0, "expired"},
			{10, true, 10, "expires in 10 days"},
			{14, true, 0, "valid for 14"},
			{80, true, 0, "valid for 80"},
		} {
			v.CertDaysLeft = c.days
			r := selfCheck(t, v, check, th)
			if r.Pass != c.pass || r.ExpiringDays != c.expiring || !strings.Contains(r.Message, c.text) {
				t.Errorf("%d days left: %+v", c.days, r)
			}
		}
	})

	t.Run("disk-free", func(t *testing.T) {
		for _, c := range []struct {
			free      float64
			pass, low bool
		}{{1, false, false}, {5, true, true}, {50, true, false}} {
			v := serverstest.NewVPS(t)
			v.DiskFreePct = c.free
			if r := selfCheck(t, v, manifest.Check{Kind: "disk-free"}, th); r.Pass != c.pass || r.Low != c.low {
				t.Errorf("%.0f%% free: %+v", c.free, r)
			}
		}
		// the thresholds are the caller's: 5 % free fails when the limit is 8
		v := serverstest.NewVPS(t)
		v.DiskFreePct = 5
		if r := selfCheck(t, v, manifest.Check{Kind: "disk-free"}, remote.Thresholds{DiskWarnPct: 20, DiskFailPct: 8}); r.Pass {
			t.Errorf("the caller's limit was ignored: %+v", r)
		}
	})

	t.Run("run", func(t *testing.T) {
		v := serverstest.NewVPS(t)
		v.RunHooks["./probe.sh"] = func(_ *serverstest.VPS, _ string, out, _ io.Writer) int { _, _ = io.WriteString(out, "ok\n"); return 0 }
		v.RunHooks["./broken.sh"] = func(_ *serverstest.VPS, _ string, _, errOut io.Writer) int {
			_, _ = io.WriteString(errOut, "queue is stuck\n")
			return 2
		}
		if r := selfCheck(t, v, manifest.Check{Kind: "run", Args: map[string]any{"run": "./probe.sh"}}, th); !r.Pass {
			t.Errorf("exit 0: %+v", r)
		}
		if r := selfCheck(t, v, manifest.Check{Kind: "run", Args: map[string]any{"run": "./broken.sh"}}, th); r.Pass || !strings.Contains(r.Message, "queue is stuck") {
			t.Errorf("exit 2: %+v", r)
		}
	})
}
