package proxy_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
	"github.com/tikhonp/proxier/internal/modules/servers/seed"
	"github.com/tikhonp/proxier/internal/modules/servers/validate"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

// wired puts the validators' hooks where the servers module does.
func wired(t *testing.T) {
	t.Helper()
	validate.XrayConfig, validate.EndpointFields = proxy.ValidateConfig, endpoint.Check
	t.Cleanup(func() { validate.XrayConfig, validate.EndpointFields = nil, nil })
}

func TestSeedXrayConfigAccepted(t *testing.T) {
	files := seed.Files()
	m, fs := manifest.Parse(files[manifest.Name])
	if m == nil {
		t.Fatalf("seed manifest: %v", fs)
	}
	rendered, rf := render.Render(m, files, render.SampleContext(m, seed.Slug, nil))
	if len(rf) != 0 {
		t.Fatalf("render findings: %v", rf)
	}
	var xray []byte
	for _, f := range rendered.Files {
		if f.Path == "xray-config.json" {
			xray = f.Content
		}
	}
	if len(xray) == 0 {
		t.Fatal("the seed has no xray-config.json")
	}
	if err := proxy.ValidateConfig(xray); err != nil {
		t.Errorf("xray refuses the seed's config: %v", err)
	}

	// and through the validators, nothing from the xray or endpoints checks
	wired(t)
	rep := validate.Validate(t.Context(), validate.Input{Slug: seed.Slug, Files: files})
	for _, f := range rep.Findings {
		if f.Check == "xray" || f.Check == "endpoints" {
			t.Errorf("unexpected finding: %s", f)
		}
	}
}

// A client may name a site by an address the server can't reach: Hiddify on
// Android sent IPv6 ones to servers without IPv6, which closed them. The
// seed's xray reads the name from the request and dials that instead.
func TestSeedDialsTheNameNotTheAddress(t *testing.T) {
	files := seed.Files()
	m, _ := manifest.Parse(files[manifest.Name])
	rendered, rf := render.Render(m, files, render.SampleContext(m, seed.Slug, nil))
	if len(rf) != 0 {
		t.Fatalf("render findings: %v", rf)
	}
	var cfg []byte
	for _, f := range rendered.Files {
		if f.Path == "xray-config.json" {
			cfg = f.Content
		}
	}
	e := rendered.Endpoints[0]
	inbound := freePort(t)
	cfg = bytes.Replace(cfg, []byte(`"port": 10001`), []byte(`"listen": "127.0.0.1", "port": `+strconv.Itoa(inbound)), 1)
	cfg = bytes.Replace(cfg, []byte(`"loglevel": "warning"`), []byte(`"loglevel": "none"`), 1)
	// freedom refuses loopback, where the test's site is
	cfg = bytes.Replace(cfg, []byte(`"protocol": "freedom",`), []byte(`"protocol": "freedom", "settings": {"finalRules": [{"action": "allow", "ip": ["127.0.0.1"]}]},`), 1)
	startXray(t, cfg)
	client := startXray(t, []byte(`{"log": {"loglevel": "none"}, "outbounds": [{"protocol": "vless",
		"settings": {"vnext": [{"address": "127.0.0.1", "port": `+strconv.Itoa(inbound)+`, "users": [{"id": "`+e.Credential+`", "encryption": "none"}]}]},
		"streamSettings": {"network": "xhttp", "security": "none", "xhttpSettings": {"path": "`+e.Params["path"]+`", "mode": "stream-up"}}}]}`))

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "the site") }))
	defer site.Close()
	port := site.Listener.Addr().(*net.TCPAddr).Port
	// 100::/64 routes nowhere (RFC 6666)
	conn, err := core.Dial(t.Context(), client, xnet.TCPDestination(xnet.ParseAddress("100::1"), xnet.Port(port)))
	if err != nil {
		t.Fatal(err)
	}
	// a link's reads don't take deadlines: closing it ends a read that waits
	stop := time.AfterFunc(10*time.Second, func() { _ = conn.Close() })
	defer stop.Stop()
	_, _ = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: localhost:%d\r\nConnection: close\r\n\r\n", port)
	got, err := io.ReadAll(conn)
	if !bytes.HasSuffix(got, []byte("the site")) {
		t.Errorf("through the seed's xray: %q, %v", got, err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func startXray(t *testing.T, cfg []byte) *core.Instance {
	t.Helper()
	c, err := serial.LoadJSONConfig(bytes.NewReader(cfg))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := core.New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func TestXrayRejectsUnknownNetwork(t *testing.T) {
	wired(t)
	files := seed.Files()
	files["xray-config.json"] = bytes.Replace(files["xray-config.json"], []byte(`"network": "xhttp"`), []byte(`"network": "carrier-pigeon"`), 1)
	rep := validate.Validate(t.Context(), validate.Input{Slug: seed.Slug, Files: files})
	var got *finding.Finding
	for i, f := range rep.Findings {
		if f.Check == "xray" {
			got = &rep.Findings[i]
		}
	}
	if got == nil || got.Severity != finding.Error || got.Path != "xray-config.json" || !strings.Contains(strings.ToLower(got.Message), "carrier-pigeon") {
		t.Fatalf("the xray check should fail on the network with xray's own words, got %v\nall: %v", got, rep.Findings)
	}
	if rep.OK() {
		t.Error("a config xray refuses must not publish")
	}
}

func TestValidateConfigRefusals(t *testing.T) {
	for name, cfg := range map[string]string{
		"not json":   `{"inbounds": [`,
		"not object": `[]`,
		"null":       `null`,
		"bad port":   `{"inbounds":[{"port":"x","protocol":"vless","settings":{"clients":[],"decryption":"none"}}]}`,
	} {
		if err := proxy.ValidateConfig([]byte(cfg)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := proxy.ValidateConfig([]byte(`{"outbounds":[{"protocol":"freedom"}]}`)); err != nil {
		t.Errorf("a minimal config: %v", err)
	}
}
