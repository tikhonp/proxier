package render_test

import (
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

// The sample context has every address the manifest's endpoints bind to, so a
// broken endpoint on address 3 fails validation instead of the first redeploy
// of a server that has three addresses.
func TestSampleContextCoversEveryEndpointAddress(t *testing.T) {
	m, files := seedManifest(t)
	if got := len(render.SampleContext(m, "t", nil).Addresses); got != 2 {
		t.Fatalf("a manifest without addresses keeps %d sample addresses, want 2", got)
	}
	m.Endpoints = append(m.Endpoints, manifest.Endpoint{
		Key: "third", Type: "vless-xhttp-tls", Port: "443", Credential: "{{ .Gen.client_uuid }}",
		Params:  map[string]string{"path": "{{ .Gen.xhttp_path }}", "sni": "{{ .Address.Hostname }}", "bad": "{{ .Gen.nope }}"},
		Address: 3, Line: 7,
	})
	c := render.SampleContext(m, "t", nil)
	if len(c.Addresses) != 3 {
		t.Fatalf("addresses: %+v", c.Addresses)
	}
	seen := map[string]bool{}
	for i, a := range c.Addresses {
		if a.Number != i+1 || a.Primary != (i == 0) || !strings.HasPrefix(a.IP, "192.0.2.") || seen[a.IP] || seen[a.Hostname] {
			t.Fatalf("address %d: %+v", i, a)
		}
		seen[a.IP], seen[a.Hostname] = true, true
	}
	_, fs := render.Render(m, files, c)
	var found bool
	for _, f := range fs {
		if strings.Contains(f.Message, "nope") && f.Line == 7 {
			found = true
		}
	}
	if !found {
		t.Fatalf("a template error on an endpoint at address 3 must be reported: %v", fs)
	}
}

// The stack must accept every distinct endpoint credential, or an endpoint
// with its own would reject the connection URIs built from it.
func TestClientsListEveryDistinctCredential(t *testing.T) {
	m, files := seedManifest(t)
	m.Generated = append(m.Generated, manifest.Generated{Key: "uuid_alt", Kind: "uuid", Rotate: true})
	m.Endpoints = append(m.Endpoints,
		manifest.Endpoint{Key: "alt", Type: "vless-xhttp-tls", Port: "443", Credential: "{{ .Gen.uuid_alt }}",
			Params: map[string]string{"path": "{{ .Gen.xhttp_path }}", "sni": "{{ .Address.Hostname }}"}, Address: 2, Line: 1},
		// Shares the first credential: not a client of its own.
		manifest.Endpoint{Key: "dup", Type: "vless-xhttp-tls", Port: "8443", Credential: "{{ .Gen.client_uuid }}",
			Params: map[string]string{"path": "{{ .Gen.xhttp_path }}", "sni": "{{ .Server.ProxyHostname }}"}, Line: 1})
	files["site/index.html"] = []byte(`{{ range .Clients }}{{ .Name }}={{ .ID }};{{ end }}`)
	c := render.SampleContext(m, "t", nil)
	out, fs := render.Render(m, files, c)
	if len(fs) != 0 {
		t.Fatalf("findings: %v", fs)
	}
	want := "shared=" + c.Gen["client_uuid"] + ";shared-alt=" + c.Gen["uuid_alt"] + ";"
	var got string
	for _, f := range out.Files {
		if f.Path == "site/index.html" {
			got = string(f.Content)
		}
	}
	if got != want {
		t.Fatalf("clients %q, want %q", got, want)
	}
}

// A template with one credential sees exactly the one shared client.
func TestSingleCredentialKeepsTheSharedClient(t *testing.T) {
	m, files := seedManifest(t)
	files["site/index.html"] = []byte(`{{ range .Clients }}{{ .Name }}={{ .ID }};{{ end }}`)
	c := render.SampleContext(m, "t", nil)
	out, fs := render.Render(m, files, c)
	if len(fs) != 0 {
		t.Fatalf("findings: %v", fs)
	}
	for _, f := range out.Files {
		if f.Path == "site/index.html" && string(f.Content) != "shared="+c.Gen["client_uuid"]+";" {
			t.Fatalf("clients %q", f.Content)
		}
	}
}
