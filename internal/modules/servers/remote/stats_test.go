package remote

import (
	"strings"
	"testing"
)

const statsOut = `##loadavg
0.42 0.30 0.25 1/200 1234
##stat
cpu  1000 10 500 8000 100 0 20 0 0 0
##meminfo
MemTotal:        2000000 kB
MemAvailable:    1500000 kB
##df
Filesystem        1-blocks       Used  Available Capacity Mounted on
/dev/vda1      20000000000 5000000000 15000000000      25% /
##route
default via 10.0.0.1 dev eth0 proto dhcp src 10.0.0.5 metric 100
##netdev
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  111111     100    0    0    0     0          0         0   111111     100    0    0    0     0       0          0
  eth0: 5000000    4000    0    0    0     0          0         0  7000000    3000    0    0    0     0       0          0
docker0:  900000     500    0    0    0     0          0         0   800000     400    0    0    0     0       0          0
##uptime
12345.67 23456.78
##docker
/stack-nginx-1|running|0|nginx:stable-alpine
/stack-xray-1|restarting|3|ghcr.io/xtls/xray-core:latest
`

func TestDefaultRouteInterfaceOnly(t *testing.T) {
	r, err := ParseStats(statsOut)
	if err != nil {
		t.Fatal(err)
	}
	if r.Iface != "eth0" || r.RXBytes != 5000000 || r.TXBytes != 7000000 {
		t.Errorf("counted %q rx %d tx %d: only the default-route device counts, not lo or docker0", r.Iface, r.RXBytes, r.TXBytes)
	}
	if r.Load1 != 0.42 || r.CPUTotal != 9630 || r.CPUBusy != 1530 {
		t.Errorf("load %v cpu busy %d of %d", r.Load1, r.CPUBusy, r.CPUTotal)
	}
	if r.MemTotal != 2000000*1024 || r.MemUsed != 500000*1024 {
		t.Errorf("mem %d of %d", r.MemUsed, r.MemTotal)
	}
	if r.DiskTotal != 20000000000 || r.DiskUsed != 5000000000 || r.Uptime != 12345.67 {
		t.Errorf("disk %d of %d, uptime %v", r.DiskUsed, r.DiskTotal, r.Uptime)
	}
	if len(r.Containers) != 2 || r.Containers[1].Name != "stack-xray-1" || r.Containers[1].Restarts != 3 || r.Containers[1].State != "restarting" {
		t.Errorf("containers %+v", r.Containers)
	}
}

func TestStatsWithoutDockerHasNoContainers(t *testing.T) {
	out := statsOut[:strings.Index(statsOut, "##docker")] + "##docker\n"
	r, err := ParseStats(out)
	if err != nil || len(r.Containers) != 0 {
		t.Fatalf("%v %+v", err, r.Containers)
	}
	if _, err := ParseStats("##loadavg\n0.1 0 0 1/1 1\n"); err == nil {
		t.Error("a missing section was accepted")
	}
}

func TestStatsCommandRoundTrips(t *testing.T) {
	c, ok := Parse(CmdStats("/opt/stack"))
	if !ok || c.Op != OpStats || len(c.Args) != 1 || c.Args[0] != "/opt/stack" || !c.Sudo {
		t.Fatalf("%+v %v", c, ok)
	}
}
