package socks_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery/socks"
	"golang.org/x/net/proxy"
)

// recorder dials directly and remembers the addresses it was asked for.
type recorder struct {
	mu    sync.Mutex
	addrs []string
}

func (r *recorder) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.addrs = append(r.addrs, addr)
	r.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func client(t *testing.T, s *socks.Server) *http.Client {
	t.Helper()
	d, err := proxy.SOCKS5("tcp", strings.TrimPrefix(s.URL(), "socks5://"), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: d.(proxy.ContextDialer).DialContext,
	}}
}

func TestSocksRelays(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hello "+r.Host) }))
	defer site.Close()
	rec := &recorder{}
	s, err := socks.Listen(context.Background(), "127.0.0.1:9222", rec.dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !strings.HasPrefix(s.URL(), "socks5://127.0.0.1:") {
		t.Fatalf("url %s", s.URL())
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(site.URL, "http://"))
	c := client(t, s)
	for _, u := range []string{site.URL, "http://localhost:" + port} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.HasPrefix(string(b), "hello ") {
			t.Errorf("%s: %q", u, b)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	// the name reached dial as a name: it resolves at the server's end
	if len(rec.addrs) != 2 || rec.addrs[0] != "127.0.0.1:"+port || rec.addrs[1] != "localhost:"+port {
		t.Errorf("dialed %v", rec.addrs)
	}
}

func TestSocksRefusesOtherPeers(t *testing.T) {
	rec := &recorder{}
	// only 127.0.0.2 may connect; this test connects from 127.0.0.1
	s, err := socks.Listen(context.Background(), "127.0.0.2:9222", rec.dial)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(s.URL(), "socks5://")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write([]byte{5, 1, 0})
	if n, err := c.Read(make([]byte, 2)); err == nil || n != 0 {
		t.Errorf("another address was answered: %d %v", n, err)
	}
	_ = c.Close()
	_ = s.Close()

	// Close ends relayed connections
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c) }()
		}
	}()
	s, err = socks.Listen(context.Background(), "127.0.0.1:9222", rec.dial)
	if err != nil {
		t.Fatal(err)
	}
	d, err := proxy.SOCKS5("tcp", strings.TrimPrefix(s.URL(), "socks5://"), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.Dial("tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if _, err := conn.Read(buf); err == nil {
		t.Error("a relayed connection outlived Close")
	}
}
