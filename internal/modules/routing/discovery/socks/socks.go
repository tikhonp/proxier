// Package socks is discovery's per-run SOCKS5 listener: Chromium visits a
// site through it, and every connection it relays goes out through a
// server's endpoint (docs/processes/routing/domain-discovery.md, step 3.1).
// Chrome can't authenticate to a SOCKS proxy, so the only check is the
// peer's address: the listener accepts the Chromium host and nothing else.
package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// handshakeTimeout bounds the greeting and the request of a connection.
const handshakeTimeout = 10 * time.Second

// Server is a running listener.
type Server struct {
	ln     net.Listener
	allow  netip.Addr
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	conns map[net.Conn]bool
	wg    sync.WaitGroup
	once  sync.Once
}

// Listen serves SOCKS5 CONNECT on the local address that routes to peer
// ("chromium:9222" or "10.89.251.3:9222"), on a random port, accepting
// only peer's IP and relaying every connection through dial. Names are
// passed to dial as they came, so they resolve at the far end.
func Listen(ctx context.Context, peer string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*Server, error) {
	host, port, err := net.SplitHostPort(peer)
	if err != nil {
		return nil, fmt.Errorf("socks: peer %q: %w", peer, err)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("socks: resolve %s: %w", host, err)
	}
	allow := ips[0].Unmap()
	// A UDP "connection" sends nothing: it only asks the kernel which local
	// address routes to the peer.
	probe, err := net.Dial("udp", net.JoinHostPort(allow.String(), port))
	if err != nil {
		return nil, fmt.Errorf("socks: no route to %s: %w", peer, err)
	}
	local := probe.LocalAddr().(*net.UDPAddr).IP
	_ = probe.Close()
	ln, err := net.Listen("tcp", net.JoinHostPort(local.String(), "0"))
	if err != nil {
		return nil, fmt.Errorf("socks: listen: %w", err)
	}
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Server{ln: ln, allow: allow, dial: dial, ctx: sctx, cancel: cancel, conns: map[net.Conn]bool{}}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// URL is the proxy URL Chromium is given: socks5://<ip>:<port>.
func (s *Server) URL() string { return "socks5://" + s.ln.Addr().String() }

// Close stops the listener and ends every relayed connection.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		s.cancel()
		err = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return err
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		_ = c.Close()
		return false
	}
	s.conns[c] = true
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	_ = c.Close()
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
		if err != nil || ap.Addr().Unmap() != s.allow || !s.track(c) {
			_ = c.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(c)
			s.handle(c)
		}()
	}
}

// The replies of RFC 1928.
const (
	repOK          = 0
	repFailure     = 1
	repRefused     = 5
	repCommand     = 7
	repAddressType = 8
)

func (s *Server) handle(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(handshakeTimeout))
	addr, err := request(c)
	if err != nil {
		var re replyError
		if errors.As(err, &re) {
			_ = reply(c, byte(re))
		}
		return
	}
	up, err := s.dial(s.ctx, "tcp", addr)
	if err != nil {
		_ = reply(c, repRefused)
		return
	}
	if !s.track(up) {
		return
	}
	defer s.untrack(up)
	if reply(c, repOK) != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	<-done
}

type replyError byte

func (e replyError) Error() string { return "socks: reply " + strconv.Itoa(int(e)) }

// request reads the greeting (no authentication) and a CONNECT request, and
// returns its address as host:port.
func request(c net.Conn) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return "", err
	}
	if hdr[0] != 5 {
		return "", errors.New("socks: not SOCKS5")
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	ok := false
	for _, m := range methods {
		ok = ok || m == 0
	}
	if !ok {
		_, _ = c.Write([]byte{5, 0xff})
		return "", errors.New("socks: the client needs authentication")
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", err
	}
	if req[0] != 5 {
		return "", errors.New("socks: not SOCKS5")
	}
	var host string
	switch req[3] {
	case 1, 4:
		b := make([]byte, 4)
		if req[3] == 4 {
			b = make([]byte, 16)
		}
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		a, _ := netip.AddrFromSlice(b)
		host = a.String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return "", err
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", replyError(repAddressType)
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", err
	}
	if req[1] != 1 {
		return "", replyError(repCommand)
	}
	if host == "" {
		return "", replyError(repFailure)
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))), nil
}

// reply answers a request; the bound address is not meaningful here.
func reply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}
