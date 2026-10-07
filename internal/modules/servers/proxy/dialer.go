package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
)

// DialFunc opens a TCP connection to addr through the endpoint.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Dialer starts an xray client for e and keeps it until the closer is closed.
// Connections made through it travel the endpoint exactly like a real
// client's. Discovery in Phase 3 browses through a server this way.
func Dialer(_ context.Context, e endpoint.Endpoint, o Options) (DialFunc, io.Closer, error) {
	inst, err := instance(e, o)
	if err != nil {
		return nil, nil, err
	}
	return dialThrough(inst), inst, nil
}

func dialThrough(inst *core.Instance) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		switch network {
		case "tcp", "tcp4", "tcp6":
		default:
			return nil, fmt.Errorf("proxy: %s connections are not supported through an endpoint", network)
		}
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("proxy: bad port in %q", addr)
		}
		dest := xnet.TCPDestination(xnet.ParseAddress(host), xnet.Port(port))
		// The link xray makes lives as long as the context it is dispatched
		// with; http.Transport cancels a dial's context when a request ends
		// but keeps the connection, so the link must not follow it.
		return core.Dial(context.WithoutCancel(ctx), inst, dest)
	}
}
