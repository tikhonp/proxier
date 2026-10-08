// Package endpoint is the registry of endpoint types: what a client connects
// to, how it is described to apps (the connection URI) and how Proxier itself
// connects to it for the proxy test (docs/integrations/vless-xhttp.md). Only
// vless-xhttp-tls exists; a new type is one more entry in the registry.
package endpoint

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

// Endpoint is a rendered endpoint of a server, ready to use.
type Endpoint struct {
	Key, Type, Host, Credential string
	Port                        int
	Params                      map[string]string
	DisplayName                 string
}

// ProxyOptions change how a client config is built. The zero value is the
// endpoint exactly as a real client has it; the fields exist for tests.
type ProxyOptions struct {
	// Address and Port replace where to connect, not the names in the TLS
	// handshake and the HTTP host.
	Address string
	Port    int
	// PinCert is the SHA-256 (hex) of the one certificate to accept; xray no
	// longer has an "allow insecure" switch, so a self-signed test server is
	// trusted this way.
	PinCert string
}

// Type is one kind of endpoint.
type Type struct {
	Name string
	// Check returns what is wrong with the rendered fields, one sentence each.
	Check func(e render.RenderedEndpoint) []string
	// URI is the connection URI that apps import.
	URI func(e Endpoint) string
	// ProxyConfig is the xray client JSON: one outbound, no inbounds.
	ProxyConfig func(e Endpoint, o ProxyOptions) ([]byte, error)
	// Mask returns the endpoint with every secret part replaced by Masked:
	// the type knows which parts those are.
	Mask func(e Endpoint) Endpoint
}

// Masked stands in for a secret on screen.
const Masked = "••••••••"

// URI is the connection URI of e through its type.
func URI(e Endpoint) (string, error) {
	t, ok := Lookup(e.Type)
	if !ok {
		return "", fmt.Errorf("endpoint %s: unknown type %q", e.Key, e.Type)
	}
	return t.URI(e), nil
}

// MaskedURI is the URI of the masked endpoint, with the percent-encoded
// bullets turned back into Masked so it reads as a hidden value.
func MaskedURI(e Endpoint) (string, error) {
	t, ok := Lookup(e.Type)
	if !ok {
		return "", fmt.Errorf("endpoint %s: unknown type %q", e.Key, e.Type)
	}
	m := e
	if t.Mask != nil {
		m = t.Mask(e)
	}
	u := t.URI(m)
	for _, enc := range []string{url.QueryEscape(Masked), url.PathEscape(Masked)} {
		u = strings.ReplaceAll(u, enc, Masked)
	}
	return u, nil
}

var registry = map[string]Type{}

func register(t Type) { registry[t.Name] = t }

// Lookup finds a type by its manifest name.
func Lookup(name string) (Type, bool) {
	t, ok := registry[name]
	return t, ok
}

// Names lists the registered types.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	return out
}

// Check is validate.EndpointFields: the fields of any registered type.
func Check(e render.RenderedEndpoint) []string {
	t, ok := Lookup(e.Type)
	if !ok {
		return nil // the manifest check reports an unknown type
	}
	return t.Check(e)
}

// DisplayName is "{flag} {location name} {number}", plus " · {key}" when the
// server has more than one endpoint. The location name is as the admin
// entered it, whatever the language of the link that serves it.
func DisplayName(flag, locationName string, number int, key string, many bool) string {
	var b strings.Builder
	if flag != "" {
		b.WriteString(flag)
		b.WriteByte(' ')
	}
	b.WriteString(locationName)
	b.WriteByte(' ')
	b.WriteString(strconv.Itoa(number))
	if many {
		b.WriteString(" · ")
		b.WriteString(key)
	}
	return b.String()
}
