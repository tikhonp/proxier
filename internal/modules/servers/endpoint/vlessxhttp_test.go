package endpoint_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

const uuid1 = "6f1c2b7e-0d3a-4c8e-9a51-2f7d8e4b1a90"

func seedEndpoint() endpoint.Endpoint {
	return endpoint.Endpoint{
		Key: "main", Type: endpoint.VlessXHTTPTLS, Host: "nl-1.hosts.tikhonnnnn.com", Port: 443, Credential: uuid1,
		Params: map[string]string{
			"path": "/0123456789abcdef", "sni": "nl-1.hosts.tikhonnnnn.com", "mode": "stream-up", "fp": "chrome", "alpn": "h2",
		},
		DisplayName: "🇳🇱 Netherlands 1",
	}
}

func TestVlessXHTTPURIGolden(t *testing.T) {
	typ, ok := endpoint.Lookup(endpoint.VlessXHTTPTLS)
	if !ok {
		t.Fatal("the type is not registered")
	}
	want := "vless://" + uuid1 + "@nl-1.hosts.tikhonnnnn.com:443?encryption=none&security=tls" +
		"&sni=nl-1.hosts.tikhonnnnn.com&fp=chrome&host=nl-1.hosts.tikhonnnnn.com&alpn=h2&type=xhttp" +
		"&path=%2F0123456789abcdef&mode=stream-up#%F0%9F%87%B3%F0%9F%87%B1%20Netherlands%201"
	if got := typ.URI(seedEndpoint()); got != want {
		t.Errorf("URI\n got %s\nwant %s", got, want)
	}
	// a template's own host= wins in the host parameter, nothing else moves
	e := seedEndpoint()
	e.Params["host"] = "front.example.com"
	if got := typ.URI(e); !strings.Contains(got, "&host=front.example.com&alpn=h2&") {
		t.Errorf("host param not used: %s", got)
	}
	// no name, no fragment
	e = seedEndpoint()
	e.DisplayName = ""
	if got := typ.URI(e); strings.Contains(got, "#") {
		t.Errorf("fragment without a name: %s", got)
	}
}

func rendered(mut func(*render.RenderedEndpoint)) render.RenderedEndpoint {
	s := seedEndpoint()
	e := render.RenderedEndpoint{Key: s.Key, Type: s.Type, Host: s.Host, Credential: s.Credential, Port: s.Port, Params: map[string]string{}}
	for k, v := range s.Params {
		e.Params[k] = v
	}
	if mut != nil {
		mut(&e)
	}
	return e
}

func TestVlessXHTTPFieldChecks(t *testing.T) {
	if got := endpoint.Check(rendered(nil)); len(got) != 0 {
		t.Fatalf("the seed endpoint is refused: %v", got)
	}
	for name, c := range map[string]struct {
		mut  func(*render.RenderedEndpoint)
		want string
	}{
		"path without slash": {func(e *render.RenderedEndpoint) { e.Params["path"] = "abc" }, "must start with /"},
		"path with a query":  {func(e *render.RenderedEndpoint) { e.Params["path"] = "/a?b" }, "may only hold"},
		"empty path":         {func(e *render.RenderedEndpoint) { delete(e.Params, "path") }, "params.path is missing"},
		"bad uuid":           {func(e *render.RenderedEndpoint) { e.Credential = "not-a-uuid" }, "must be a UUID"},
		"braced uuid":        {func(e *render.RenderedEndpoint) { e.Credential = "{" + uuid1 + "}" }, "must be a UUID"},
		"unknown mode":       {func(e *render.RenderedEndpoint) { e.Params["mode"] = "turbo" }, `params.mode "turbo"`},
		"unknown fp":         {func(e *render.RenderedEndpoint) { e.Params["fp"] = "unsafe" }, `params.fp "unsafe"`},
		"no sni":             {func(e *render.RenderedEndpoint) { e.Params["sni"] = "" }, "params.sni is missing"},
		"no alpn":            {func(e *render.RenderedEndpoint) { e.Params["alpn"] = " " }, "params.alpn is missing"},
		"no host":            {func(e *render.RenderedEndpoint) { e.Host = "" }, "the host is empty"},
	} {
		got := endpoint.Check(rendered(c.mut))
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: got %q, want one message with %q", name, got, c.want)
		}
	}
	// an unknown type is the manifest check's business
	if got := endpoint.Check(render.RenderedEndpoint{Type: "wireguard"}); got != nil {
		t.Errorf("unknown type: %v", got)
	}
}

func TestDisplayNames(t *testing.T) {
	if got := endpoint.DisplayName("🇳🇱", "Netherlands", 1, "main", false); got != "🇳🇱 Netherlands 1" {
		t.Errorf("one endpoint: %q", got)
	}
	if got := endpoint.DisplayName("🇳🇱", "Netherlands", 2, "backup", true); got != "🇳🇱 Netherlands 2 · backup" {
		t.Errorf("two endpoints: %q", got)
	}
	if got := endpoint.DisplayName("", "Home", 3, "main", false); got != "Home 3" {
		t.Errorf("no flag: %q", got)
	}
}

func TestProxyConfigShape(t *testing.T) {
	typ, _ := endpoint.Lookup(endpoint.VlessXHTTPTLS)
	b, err := typ.ProxyConfig(seedEndpoint(), endpoint.ProxyOptions{Address: "127.0.0.1", Port: 8443, PinCert: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Log       map[string]string
		Inbounds  []any
		Outbounds []struct {
			Protocol string
			Settings struct {
				Vnext []struct {
					Address string
					Port    int
					Users   []map[string]string
				}
			}
			StreamSettings struct {
				Network, Security string
				TLSSettings       map[string]any
				XHTTPSettings     map[string]string
			}
		}
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	o := cfg.Outbounds[0]
	v := o.Settings.Vnext[0]
	if cfg.Log["loglevel"] != "none" || cfg.Log["access"] != "none" || len(cfg.Inbounds) != 0 {
		t.Errorf("log or inbounds: %+v", cfg)
	}
	if o.Protocol != "vless" || v.Address != "127.0.0.1" || v.Port != 8443 || v.Users[0]["id"] != uuid1 || v.Users[0]["encryption"] != "none" {
		t.Errorf("outbound: %+v", o)
	}
	if o.StreamSettings.Network != "xhttp" || o.StreamSettings.Security != "tls" ||
		o.StreamSettings.TLSSettings["serverName"] != "nl-1.hosts.tikhonnnnn.com" || o.StreamSettings.TLSSettings["fingerprint"] != "chrome" ||
		o.StreamSettings.XHTTPSettings["path"] != "/0123456789abcdef" || o.StreamSettings.XHTTPSettings["host"] != "nl-1.hosts.tikhonnnnn.com" {
		t.Errorf("stream settings: %+v", o.StreamSettings)
	}
	// without options it connects to the endpoint itself
	b, _ = typ.ProxyConfig(seedEndpoint(), endpoint.ProxyOptions{})
	if !strings.Contains(string(b), `"address":"nl-1.hosts.tikhonnnnn.com","port":443`) || strings.Contains(string(b), "pinnedPeerCertSha256") {
		t.Errorf("defaults: %s", b)
	}
	// a broken endpoint gives no config
	e := seedEndpoint()
	e.Params["mode"] = "x"
	if _, err := typ.ProxyConfig(e, endpoint.ProxyOptions{}); err == nil {
		t.Error("a broken endpoint built a config")
	}
}
