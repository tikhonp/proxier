package params_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/params/paramstest"
)

func TestGroupsAndHeadings(t *testing.T) {
	s := params.Parse(paramstest.WithEnd(paramstest.Today()))
	if len(s.Groups) != 7 {
		t.Fatalf("%d groups", len(s.Groups))
	}
	var got []string
	for _, g := range s.Groups {
		var items []string
		for _, it := range g.Items {
			items = append(items, it.Name())
		}
		got = append(got, g.Heading+": "+strings.Join(items, ","))
	}
	want := []string{
		": lanNet,wanIface,subUrl,image,timeZone",
		"interface names: lanIface,containerIface,vethName",
		"interface *lists* names: lanList,wanList",
		"selective-VPN routing names. $vpnList must match LIST in mtvpn.py.: vpnList,vpnTable,vpnMark",
		"disk holding the container's layers and root-dir (a /disk slot name): containerDisk",
		"container internal /24: router side = .1, mihomo veth = .2 = the VPN gateway: containerNet,vpnGateway",
		"DoH resolver for tunneled services only. $dohForwarder must match DOH_FORWARDER in mtvpn.py.: dohHost,dohIP,dohForwarder",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group %d: %q, want %q", i+1, got[i], want[i])
		}
	}
	// the first group keeps its descriptions with their items
	first := s.Groups[0]
	if first.Items[0].Param.Description == "" || first.Items[1].Param.Description == "" || first.Items[2].Param.Description == "" {
		t.Error("the first group lost its descriptions")
	}
	// the heading's description still belongs to the parameter
	if s.Groups[1].Items[0].Param.Description != "interface names" {
		t.Error("lanIface lost its description")
	}
	if c := s.Groups[5].Items[1].Computed; c == nil || c.Name != "vpnGateway" {
		t.Errorf("vpnGateway: %+v", s.Groups[5].Items[1])
	}
}
