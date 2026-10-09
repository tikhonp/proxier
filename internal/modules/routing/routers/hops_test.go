package routers_test

import (
	"fmt"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/platform/tailnet"
)

func TestHopsTable(t *testing.T) {
	behind := routers.Router{Conn: routers.Connection{Host: "10.230.2.1", Tailnet: true, JumpHost: "parents-pi"}}
	direct := routers.Router{Conn: routers.Connection{Host: "10.230.1.1"}}
	up := tailnet.Status{State: tailnet.Running, Name: "proxier"}
	off := tailnet.Status{State: tailnet.Off}
	show := func(hs []routers.Hop) string {
		var s string
		for _, h := range hs {
			s += fmt.Sprintf("[%s %s %s %q %s]", h.Kind, h.Label, h.Result, h.Detail, h.Via)
		}
		return s
	}
	for _, c := range []struct {
		name string
		r    routers.Router
		s    routers.Sync
		node tailnet.Status
		want string
	}{
		{"tailnet off", behind, routers.Sync{Hop: "tailnet", Error: "The tailnet is off (no PROXIER_TS_AUTHKEY)."}, off,
			`[tailnet proxier off "The tailnet is off (no PROXIER_TS_AUTHKEY)." ][jump parents-pi not-tried "" ][router 10.230.2.1 not-tried "" ]`},
		{"jump offline", behind, routers.Sync{Hop: "jump", Error: "Proxier can't reach the jump host parents-pi:22: no route to host."}, up,
			`[tailnet proxier up "" ][jump parents-pi offline "Proxier can't reach the jump host parents-pi:22: no route to host." ][router 10.230.2.1 not-tried "" parents-pi]`},
		{"router refused", behind, routers.Sync{Hop: "router", Error: "The router 10.230.2.1 refused Proxier's key: install it on the router."}, up,
			`[tailnet proxier up "" ][jump parents-pi up "" ][router 10.230.2.1 refused "The router 10.230.2.1 refused Proxier's key: install it on the router." ]`},
		{"key changed", behind, routers.Sync{Hop: "router", Error: "The router's host key changed: accept the new key in Settings → SSH."}, up,
			`[tailnet proxier up "" ][jump parents-pi up "" ][router 10.230.2.1 key-changed "The router's host key changed: accept the new key in Settings → SSH." ]`},
		{"direct", direct, routers.Sync{Hop: "router", Error: "Proxier can't reach the router 10.230.1.1:22: connection refused."}, off,
			`[router 10.230.1.1 offline "Proxier can't reach the router 10.230.1.1:22: connection refused." ]`},
	} {
		if got := show(routers.Hops(c.r, c.s, c.node)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
