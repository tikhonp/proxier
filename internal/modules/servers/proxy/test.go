package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"syscall"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
)

// smallObject is under what throttling that lets the first ~20 KB through
// would not show.
const smallObject = 64 << 10

// Result is the outcome of one proxy test.
type Result struct {
	OK    bool
	Class Class
	Error string
	URL   string
	// ConnectMS and TLSMS are the direct probes; FirstByteMS is from the
	// request to the first byte of the answer through the endpoint.
	ConnectMS, TLSMS, FirstByteMS int
	Bytes                         int64
	ThroughputKbps                int
	// SmallObject: the object was under 64 KiB, so throttling after ~20 KB
	// could not be seen.
	SmallObject bool
}

var (
	errStalled  = errors.New("stalled")
	errDeadline = errors.New("deadline")
)

// Test connects through e as a real client does and downloads o.URL.
//
//  1. a plain TCP connection to host:port            → tcp-timeout, tcp-refused
//  2. a TLS handshake with the endpoint's SNI, ALPN
//     and fingerprint                                → tls-failed
//  3. the object through an embedded xray client      → stalled, http-error, timeout
//
// xray's own errors are opaque, so the direct probes make the class of a
// network failure deterministic. A failing probe ends the test: step 3 would
// only repeat it less clearly.
func Test(ctx context.Context, e endpoint.Endpoint, o Options) Result {
	o = o.withDefaults()
	res := Result{URL: o.URL}
	fail := func(c Class, format string, args ...any) Result {
		res.Class, res.Error = c, fmt.Sprintf(format, args...)
		return res
	}
	if o.URL == "" {
		return fail(HTTPError, "no test URL")
	}

	host, port := o.target(e)
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	// 1: TCP
	probe, cancelProbe := context.WithTimeout(ctx, o.Probe)
	start := time.Now()
	dial := o.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(probe, "tcp", addr)
	cancelProbe()
	if err != nil {
		if ctx.Err() != nil {
			return fail(Timeout, "cancelled")
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return fail(TCPRefused, "connection to %s:%d refused", e.Host, e.Port)
		}
		return fail(TCPTimeout, "no TCP connection to %s:%d: %s", e.Host, e.Port, shortNetErr(err))
	}
	res.ConnectMS = ms(time.Since(start))

	// 2: TLS
	start = time.Now()
	err = handshake(ctx, conn, e, o)
	_ = conn.Close()
	if err != nil {
		if ctx.Err() != nil {
			return fail(Timeout, "cancelled")
		}
		return fail(TLSFailed, "TLS handshake with %s failed: %s", e.Host, shortNetErr(err))
	}
	res.TLSMS = ms(time.Since(start))

	// 3: the object through xray
	inst, err := instance(e, o)
	if err != nil {
		return fail(HTTPError, "%v", err)
	}
	defer func() { _ = inst.Close() }()

	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	deadline := time.AfterFunc(o.Timeout, func() { cancel(errDeadline) })
	defer deadline.Stop()
	stall := time.AfterFunc(o.Stall, func() { cancel(errStalled) })
	defer stall.Stop()

	dialX := dialThrough(inst)
	tr := &http.Transport{
		DialContext: func(c context.Context, network, a string) (net.Conn, error) {
			cn, err := dialX(c, network, a)
			if err != nil {
				return nil, err
			}
			return &stallConn{Conn: cn, timer: stall, every: o.Stall}, nil
		},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: o.Timeout,
	}
	defer tr.CloseIdleConnections()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, o.URL, nil)
	if err != nil {
		return fail(HTTPError, "bad test URL: %v", err)
	}
	var firstByte time.Time
	t0 := time.Now()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}))

	// finish classifies a failure of the request or of the body read.
	finish := func(err error, got int64) Result {
		res.Bytes = got
		switch cause := context.Cause(rctx); {
		case errors.Is(cause, errStalled):
			return fail(Stalled, "no data for %s after %s received", o.Stall, formatBytes(got))
		case errors.Is(cause, errDeadline):
			return fail(Timeout, "not finished in %s (%s received)", o.Timeout, formatBytes(got))
		case ctx.Err() != nil:
			return fail(Timeout, "cancelled")
		}
		return fail(HTTPError, "%s", shortNetErr(err))
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return finish(err, 0)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fail(HTTPError, "the test object answered HTTP %d", resp.StatusCode)
	}
	if firstByte.IsZero() {
		firstByte = time.Now()
	}
	res.FirstByteMS = ms(firstByte.Sub(t0))

	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return finish(err, n)
	}
	res.Bytes = n
	took := time.Since(firstByte)
	if took < time.Millisecond {
		took = time.Millisecond
	}
	res.ThroughputKbps = int(float64(n) * 8 / 1000 / took.Seconds())
	res.SmallObject = n < smallObject
	res.OK = true
	return res
}

// stallConn restarts the stall timer whenever data arrives.
type stallConn struct {
	net.Conn
	timer *time.Timer
	every time.Duration
}

func (c *stallConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.timer.Reset(c.every)
	}
	return n, err
}

// handshake performs the TLS handshake of the real client: the endpoint's SNI,
// ALPN and fingerprint, so a network that treats a Go handshake differently
// from a Chrome one does not mislead the probe.
func handshake(ctx context.Context, conn net.Conn, e endpoint.Endpoint, o Options) error {
	ctx, cancel := context.WithTimeout(ctx, o.Probe)
	defer cancel()
	sni := e.Params["sni"]
	if sni == "" {
		sni = e.Host
	}
	var alpn []string
	for _, a := range strings.Split(e.Params["alpn"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			alpn = append(alpn, a)
		}
	}
	id := utls.HelloChrome_Auto
	if fp := xtls.GetFingerprint(e.Params["fp"]); fp != nil {
		id = *fp
	}
	// Verification is ours: utls with InsecureSkipVerify hands the chain to
	// VerifyPeerCertificate.
	u := utls.UClient(conn, &utls.Config{
		ServerName: sni, NextProtos: alpn, InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error { return verifyPeer(raw, sni, o.PinCert) },
	}, id)
	return u.HandshakeContext(ctx)
}

func verifyPeer(raw [][]byte, sni, pin string) error {
	if len(raw) == 0 {
		return errors.New("the server sent no certificate")
	}
	if pin != "" {
		sum := sha256.Sum256(raw[0])
		if !strings.EqualFold(hex.EncodeToString(sum[:]), pin) {
			return errors.New("the certificate is not the pinned one")
		}
		return nil
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	inter := x509.NewCertPool()
	for _, r := range raw[1:] {
		if c, err := x509.ParseCertificate(r); err == nil {
			inter.AddCert(c)
		}
	}
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: sni, Intermediates: inter})
	return err
}

func ms(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	if m := int(d.Milliseconds()); m > 0 {
		return m
	}
	return 1
}

func formatBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	return strconv.FormatInt(n/1024, 10) + " KB"
}

// shortNetErr drops the "dial tcp 1.2.3.4:443:" prefixes of Go's net errors
// so a sentence of ours can carry the reason.
func shortNetErr(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}
