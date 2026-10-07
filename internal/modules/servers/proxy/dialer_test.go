package proxy_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy/proxytest"
)

func TestDialerThroughEndpoint(t *testing.T) {
	s := proxytest.New(t)
	dial, closer, err := proxy.Dialer(t.Context(), s.Endpoint(), opts(s))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}
	// two connections through the same dialer
	for range 2 {
		resp, err := client.Get(s.ObjectURL())
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || n != 256<<10 {
			t.Fatalf("through the dialer: %d, %d bytes, %v", resp.StatusCode, n, err)
		}
	}
	if err := closer.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	// only TCP goes through an endpoint
	if _, err := dial(t.Context(), "udp", "127.0.0.1:53"); err == nil {
		t.Error("udp was accepted")
	}
	if _, err := dial(t.Context(), "tcp", "no-port"); err == nil {
		t.Error("an address without a port was accepted")
	}
}

func TestDialerRefusesABrokenEndpoint(t *testing.T) {
	s := proxytest.New(t)
	e := s.Endpoint()
	e.Params["mode"] = "turbo"
	if _, _, err := proxy.Dialer(t.Context(), e, opts(s)); err == nil {
		t.Error("a broken endpoint gave a dialer")
	}
}
