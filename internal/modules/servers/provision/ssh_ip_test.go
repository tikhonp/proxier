package provision_test

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/modules/servers/ipchange"
	"github.com/tikhonp/proxier/internal/modules/servers/serverstest"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func TestSeparateSSHIP(t *testing.T) {
	for _, management := range []string{"100.64.0.10", "fd7a:115c:a1e0::10", "192.168.1.10", "203.0.113.10"} {
		t.Run(management, func(t *testing.T) {
			h := serverstest.NewHarness(t, serverstest.StubProxy())
			h.PublishVersion(func(files map[string][]byte) {
				files["site/index.html"] = []byte(`service={{ .Server.IP }} ssh={{ .Server.SSHIP }} primary={{ (index .Addresses 0).IP }}`)
			}, true)
			f := h.Form()
			f.SSHIP = " " + management + " "
			// All SSH to the public IP must fail: every lifecycle connection
			// needs to use the separately configured management address.
			if management != f.IP {
				h.Unreachable(f.IP, true)
			}
			id := h.Create(f)
			h.Drain()
			srv := active(t, h, id)
			if srv.SSHIP != management || srv.IP != f.IP {
				t.Fatalf("stored addresses: %+v", srv)
			}
			for _, rec := range h.CF.Records("proxier.test") {
				if rec.Content != f.IP {
					t.Fatalf("management address leaked into DNS: %+v", rec)
				}
			}
			b, _ := h.VPS.File("/opt/proxier/vless-xhttp/site/index.html")
			want := "service=" + f.IP + " ssh=" + management + " primary=" + f.IP
			if string(b) != want {
				t.Fatalf("rendered addresses: %q, want %q", b, want)
			}
			target := deploy.Target{Force: true}
			plan, err := h.Mod.Deploy.Plan(bg, id, target)
			if err != nil {
				t.Fatal(err)
			}
			jobID, err := h.Mod.Deploy.Apply(bg, id, target, plan.Hash, "admin")
			if err != nil {
				t.Fatal(err)
			}
			h.Drain()
			job, err := h.App.Jobs.Job(bg, jobID)
			if err != nil || job.State != jobs.Succeeded {
				t.Fatalf("redeploy: %+v %v", job, err)
			}
			if _, err := h.Mod.Health.RunNow(bg, id, "admin"); err != nil {
				t.Fatal(err)
			}
			h.Drain()
			checks, err := store.LatestResults(bg, h.App.DB.R, id, "self")
			if err != nil || len(checks) == 0 || !checks[0].OK {
				t.Fatalf("self-check: %+v %v", checks, err)
			}
			const newPublic = "203.0.113.20"
			h.Unreachable(newPublic, true)
			jobID, err = h.Mod.IPChange.Change(bg, id, ipchange.Form{IP: newPublic, Force: true}, "admin")
			if err != nil {
				t.Fatal(err)
			}
			h.Drain()
			job, err = h.App.Jobs.Job(bg, jobID)
			if err != nil || job.State != jobs.Succeeded {
				t.Fatalf("service IP change: %+v %v\n%s", job, err, h.Log(jobID))
			}
			if got := h.Server(id); got.IP != newPublic || got.SSHIP != management {
				t.Fatalf("public IP change affected management: %+v", got)
			}
			if _, err := h.App.SSH.KnownHost(bg, net.JoinHostPort(management, strconv.Itoa(srv.SSHPort))); err != nil {
				t.Fatalf("management host key was forgotten: %v", err)
			}
			_, err = h.Mod.Retire.Retire(bg, id, srv.Name, true, "admin")
			if err != nil {
				t.Fatal(err)
			}
			h.Drain()
			if got := h.Server(id); got.State != "retired" {
				t.Fatalf("retirement did not use management IP: %+v", got)
			}
		})
	}
}

func TestSSHIPValidation(t *testing.T) {
	h := serverstest.NewHarness(t, serverstest.StubProxy())
	for _, ip := range []string{"garbage", "100.64.0.1:22", "fe80::1%eth0", "0.0.0.0", "::", "224.0.0.1", "ff02::1"} {
		f := h.Form()
		f.SSHIP = ip
		_, err := h.Mod.Provision.Create(bg, f, "admin")
		if got := refused(t, err)["ssh_ip"].Key; got != "servers.err.ssh_ip" {
			t.Errorf("%q: %s", ip, got)
		}
	}
	nothingCreated(t, h)
	for _, ip := range []string{"", "100.64.0.10", "fd7a:115c:a1e0::10", "::ffff:100.64.0.10"} {
		f := h.Form()
		f.SSHIP = ip
		sum := h.Mod.Provision.Summarize(bg, f, false)
		if len(sum.Errors) != 0 {
			t.Fatalf("%q: %+v", ip, sum.Errors)
		}
		if strings.Contains(sum.DNSRecord, "100.64.") || strings.Contains(sum.DNSRecord, "fd7a:") {
			t.Fatal("management address leaked into summary DNS")
		}
	}
}
