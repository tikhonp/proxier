package endpoint

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

// VlessXHTTPTLS is the manifest name of the first endpoint type.
const VlessXHTTPTLS = "vless-xhttp-tls"

var (
	// Modes xray's XHTTP knows.
	xhttpModes = []string{"auto", "packet-up", "stream-up", "stream-one"}
	// The fingerprints GUI clients offer; xray's "unsafe" is left out on purpose.
	fingerprints = []string{"chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized", "randomizednoalpn"}
	// A path that survives a URL untouched except for its slashes.
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~\-/]*$`)
)

func init() {
	register(Type{Name: VlessXHTTPTLS, Check: checkVlessXHTTP, URI: uriVlessXHTTP, ProxyConfig: configVlessXHTTP})
}

func checkVlessXHTTP(e render.RenderedEndpoint) []string {
	var out []string
	if id, err := uuid.Parse(e.Credential); err != nil || id.String() != strings.ToLower(e.Credential) {
		out = append(out, "the credential must be a UUID like 6f1c2b7e-0d3a-4c8e-9a51-2f7d8e4b1a90")
	}
	if e.Host == "" {
		out = append(out, "the host is empty")
	}
	path := e.Params["path"]
	switch {
	case path == "":
		out = append(out, "params.path is missing")
	case !strings.HasPrefix(path, "/"):
		out = append(out, fmt.Sprintf("params.path %q must start with /", path))
	case !pathPattern.MatchString(path):
		out = append(out, fmt.Sprintf("params.path %q may only hold letters, digits and . _ ~ - /", path))
	}
	if e.Params["sni"] == "" {
		out = append(out, "params.sni is missing")
	}
	if m := e.Params["mode"]; !slices.Contains(xhttpModes, m) {
		out = append(out, fmt.Sprintf("params.mode %q must be one of %s", m, strings.Join(xhttpModes, ", ")))
	}
	if fp := e.Params["fp"]; !slices.Contains(fingerprints, fp) {
		out = append(out, fmt.Sprintf("params.fp %q must be one of %s", fp, strings.Join(fingerprints, ", ")))
	}
	if strings.TrimSpace(e.Params["alpn"]) == "" {
		out = append(out, "params.alpn is missing")
	}
	return out
}

// xhttpHost is the HTTP Host of the XHTTP requests: the endpoint's own host
// unless the template gives another.
func xhttpHost(e Endpoint) string {
	if h := e.Params["host"]; h != "" {
		return h
	}
	return e.Host
}

// uriVlessXHTTP is the URI exactly as setup.sh printed it, so apps that
// compare links see no difference: parameter order encryption, security, sni,
// fp, host, alpn, type, path, mode; the path with its slashes as %2F; the
// fragment percent-encoded UTF-8 with %20 for spaces.
func uriVlessXHTTP(e Endpoint) string {
	q := func(s string) string { return url.QueryEscape(s) }
	var b strings.Builder
	b.WriteString("vless://")
	b.WriteString(e.Credential)
	b.WriteByte('@')
	b.WriteString(e.Host)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(e.Port))
	b.WriteString("?encryption=none&security=tls")
	b.WriteString("&sni=" + q(e.Params["sni"]))
	b.WriteString("&fp=" + q(e.Params["fp"]))
	b.WriteString("&host=" + q(xhttpHost(e)))
	b.WriteString("&alpn=" + q(e.Params["alpn"]))
	b.WriteString("&type=xhttp")
	b.WriteString("&path=" + q(e.Params["path"]))
	b.WriteString("&mode=" + q(e.Params["mode"]))
	if e.DisplayName != "" {
		b.WriteByte('#')
		b.WriteString(url.PathEscape(e.DisplayName))
	}
	return b.String()
}

// configVlessXHTTP is the config a client app would build from the URI: one
// vless outbound over XHTTP and TLS, nothing listening. Logging is off because
// a started xray instance installs a process-wide log handler that writes to
// stdout in its own format.
func configVlessXHTTP(e Endpoint, o ProxyOptions) ([]byte, error) {
	if errs := checkVlessXHTTP(render.RenderedEndpoint{Key: e.Key, Type: e.Type, Host: e.Host, Credential: e.Credential, Port: e.Port, Params: e.Params}); len(errs) > 0 {
		return nil, fmt.Errorf("endpoint %s: %s", e.Key, errs[0])
	}
	addr, port := e.Host, e.Port
	if o.Address != "" {
		addr = o.Address
	}
	if o.Port != 0 {
		port = o.Port
	}
	var alpn []string
	for _, a := range strings.Split(e.Params["alpn"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			alpn = append(alpn, a)
		}
	}
	tls := map[string]any{"serverName": e.Params["sni"], "fingerprint": e.Params["fp"], "alpn": alpn}
	if o.PinCert != "" {
		tls["pinnedPeerCertSha256"] = o.PinCert
	}
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": addr, "port": port,
				"users": []any{map[string]any{"id": e.Credential, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "tls", "tlsSettings": tls,
				"xhttpSettings": map[string]any{"path": e.Params["path"], "mode": e.Params["mode"], "host": xhttpHost(e)},
			},
		}},
	}
	return json.Marshal(cfg)
}
