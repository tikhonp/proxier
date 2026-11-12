package deploy_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/deploy"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Retirement renders the uninstall steps with the same .Addresses as the
// stack, and .Clients from every endpoint, address-bound ones included.
func TestRetirementSetupSeesAddressesAndClients(t *testing.T) {
	h, id := stubbed(t)
	v2 := h.PublishVersion(all(
		change("manifest.yaml", "    host: \"{{ .Server.ProxyHostname }}\"", "    address: 1\n    host: \"{{ .Address.Hostname }}\""),
		change("manifest.yaml", "    - compose-down: { volumes: true }",
			"    - run: \"echo addrs={{ range .Addresses }}{{ .IP }},{{ end }} clients={{ range .Clients }}{{ .Name }},{{ end }}\"\n    - compose-down: { volumes: true }"),
	), true)
	mustState(t, h, apply(t, h, id, deploy.Target{Version: v2}), jobs.Succeeded)
	if err := h.Mod.Deploy.AddAddress(bg, id, "203.0.113.77", "alt.example.com", "admin"); err != nil {
		t.Fatal(err)
	}

	setup, err := h.Mod.Deploy.RetirementSetup(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	var cmd string
	for _, s := range setup.Uninstall {
		if s.Kind == "run" {
			cmd, _ = s.Args["run"].(string)
		}
	}
	srv := h.Server(id)
	want := "addrs=" + srv.IP + ",203.0.113.77, clients=shared,"
	if !strings.Contains(cmd, want) {
		t.Errorf("uninstall step %q, want it to contain %q", cmd, want)
	}
}
