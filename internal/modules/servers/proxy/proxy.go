// Package proxy embeds xray-core: the proxy test connects through an endpoint
// exactly as a real client does, Dialer keeps such a connection open for
// callers that browse through a server, and ValidateConfig asks xray whether
// it accepts a template's config (ADR 0009). This is the only package that
// imports xray's full distribution, so no other package's tests link it.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Class says why a proxy test failed ("" = it passed).
type Class string

const (
	OK         Class = ""
	TCPTimeout Class = "tcp-timeout"
	TCPRefused Class = "tcp-refused"
	TLSFailed  Class = "tls-failed"
	Stalled    Class = "stalled"
	HTTPError  Class = "http-error"
	Timeout    Class = "timeout"
)

// Options of a proxy test or a dialer.
type Options struct {
	// URL is the test object. The caller picks the template's or the setting's.
	URL string
	// Timeout is the whole third step (default 15 s); Stall is how long no
	// data may arrive (default 5 s); Probe is each of the two direct probes
	// (default 5 s).
	Timeout, Stall, Probe time.Duration

	// The rest exists for tests and is never set from settings.

	// Resolve maps "host:port" of the endpoint to where to really connect.
	Resolve map[string]string
	// PinCert is the SHA-256 (hex) of the one certificate to trust: xray has
	// no "allow insecure" any more, and the test server's is self-signed.
	PinCert string
	// Dial replaces the direct TCP probe's dialer (to simulate a blackhole).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Second
	}
	if o.Stall <= 0 {
		o.Stall = 5 * time.Second
	}
	if o.Probe <= 0 {
		o.Probe = 5 * time.Second
	}
	return o
}

// target is where to connect for e: the endpoint's own address unless a test
// maps it elsewhere.
func (o Options) target(e endpoint.Endpoint) (host string, port int) {
	host, port = e.Host, e.Port
	to, ok := o.Resolve[net.JoinHostPort(e.Host, strconv.Itoa(e.Port))]
	if !ok {
		return host, port
	}
	h, p, err := net.SplitHostPort(to)
	if err != nil {
		return host, port
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return host, port
	}
	return h, n
}

// instance builds and starts an xray client for e: one outbound, nothing
// listening.
func instance(e endpoint.Endpoint, o Options) (*core.Instance, error) {
	t, ok := endpoint.Lookup(e.Type)
	if !ok {
		return nil, fmt.Errorf("proxy: unknown endpoint type %q", e.Type)
	}
	host, port := o.target(e)
	cfgJSON, err := t.ProxyConfig(e, endpoint.ProxyOptions{Address: host, Port: port, PinCert: o.PinCert})
	if err != nil {
		return nil, err
	}
	cfg, err := serial.LoadJSONConfig(bytes.NewReader(cfgJSON))
	if err != nil {
		return nil, fmt.Errorf("proxy: xray refused the client config: %w", err)
	}
	inst, err := core.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("proxy: xray could not build the client: %w", err)
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		return nil, fmt.Errorf("proxy: xray could not start the client: %w", err)
	}
	return inst, nil
}

// ValidateConfig asks xray whether it accepts config: it is loaded as JSON and
// an instance is built (never started, so nothing listens) and closed. The
// template's own log section is replaced: building an instance installs a
// process-wide log handler that would write to stdout in xray's own format.
func ValidateConfig(config []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(config, &top); err != nil {
		// xray's own reader says where the syntax breaks (line and character).
		if _, xerr := serial.LoadJSONConfig(bytes.NewReader(config)); xerr != nil && !json.Valid(config) {
			return trimXray(xerr)
		}
		return fmt.Errorf("not a JSON object: %w", err)
	}
	if top == nil {
		return errors.New("not a JSON object")
	}
	top["log"] = json.RawMessage(`{"loglevel":"none","access":"none"}`)
	quiet, err := json.Marshal(top)
	if err != nil {
		return err
	}
	cfg, err := serial.LoadJSONConfig(bytes.NewReader(quiet))
	if err != nil {
		return trimXray(err)
	}
	inst, err := core.New(cfg)
	if err != nil {
		return trimXray(err)
	}
	return inst.Close()
}

// xrayNoise is the package prefixes of xray's errors.
var xrayNoise = strings.NewReplacer("infra/conf/serial: ", "", "infra/conf: ", "", "app/log: ", "")

// trimXray drops the package prefixes of xray's error chain
// ("a > b > the reason") and keeps the chain, whose last link is the reason.
func trimXray(err error) error { return errors.New(xrayNoise.Replace(err.Error())) }

var _ io.Closer = (*core.Instance)(nil)
