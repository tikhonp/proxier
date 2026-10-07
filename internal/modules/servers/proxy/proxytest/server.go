// Package proxytest is an in-process VLESS XHTTP server over TLS (a real xray
// server instance on 127.0.0.1 with a self-signed certificate) and a test
// object server behind it, so the proxy test runs without a network.
package proxytest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Host is the name the endpoint answers to (certificate and SNI).
const Host = "proxy.test"

// Server is the proxy plus its test object.
type Server struct {
	t *testing.T

	// UUID and Path are what a client needs; Port is where the proxy listens.
	UUID, Path string
	Port       int
	// CertSHA256 is the hex SHA-256 of the certificate, for proxy.Options.PinCert.
	CertSHA256 string

	certPEM, keyPEM []string

	mu   sync.Mutex
	inst *core.Instance
	// plain stands in for the proxy after RejectTLS: it accepts and hangs up.
	plain net.Listener

	objects *httptest.Server
	mode    Mode
}

// New starts the proxy and the test object server (mode OK(256 KiB)). Both
// stop when the test ends.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t, UUID: uuid.NewString(), Path: "/" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]}
	s.makeCert()
	s.mode = OK(256 << 10)
	s.objects = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		m := s.mode
		s.mu.Unlock()
		m(w, r)
	}))
	t.Cleanup(func() {
		// A stalled or slow answer would otherwise hold Close until it gives up.
		s.objects.CloseClientConnections()
		s.objects.Close()
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Port = l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	s.startProxy()
	t.Cleanup(s.stop)
	return s
}

func (s *Server) makeCert() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: Host},
		DNSNames:  []string{Host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		s.t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		s.t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	s.CertSHA256 = hex.EncodeToString(sum[:])
	lines := func(typ string, b []byte) []string {
		return strings.Split(strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}))), "\n")
	}
	s.certPEM, s.keyPEM = lines("CERTIFICATE", der), lines("EC PRIVATE KEY", kb)
}

func (s *Server) serverConfig() []byte {
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none"},
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": s.Port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{map[string]any{"id": s.UUID}}, "decryption": "none"},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "tls",
				"tlsSettings":   map[string]any{"alpn": []string{"h2"}, "certificates": []any{map[string]any{"certificate": s.certPEM, "key": s.keyPEM}}},
				"xhttpSettings": map[string]any{"path": s.Path, "mode": "auto"},
			},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom"}},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	return b
}

func (s *Server) startProxy() {
	cfg, err := serial.LoadJSONConfig(bytes.NewReader(s.serverConfig()))
	if err != nil {
		s.t.Fatalf("proxytest: xray refused the server config: %v", err)
	}
	inst, err := core.New(cfg)
	if err != nil {
		s.t.Fatalf("proxytest: %v", err)
	}
	if err := inst.Start(); err != nil {
		s.t.Fatalf("proxytest: %v", err)
	}
	s.mu.Lock()
	s.inst = inst
	s.mu.Unlock()
}

func (s *Server) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inst != nil {
		_ = s.inst.Close()
		s.inst = nil
	}
	if s.plain != nil {
		_ = s.plain.Close()
		s.plain = nil
	}
}

// Rotate restarts the proxy with another UUID and path (the zero value keeps
// the current one): clients holding the old values are refused afterwards,
// like after a credential rotation, and a provisioning test can give the
// server the values the template generated.
func (s *Server) Rotate(uuid, path string) {
	s.stop()
	if uuid != "" {
		s.UUID = uuid
	}
	if path != "" {
		s.Path = path
	}
	s.startProxy()
}

// CloseTCP stops the proxy: connections to its port are refused.
func (s *Server) CloseTCP() { s.stop() }

// RejectTLS replaces the proxy with a listener that hangs up on every
// connection, so the TCP connection works and the TLS handshake does not.
func (s *Server) RejectTLS() {
	s.stop()
	var l net.Listener
	var err error
	for range 50 { // the port may take a moment to free up
		if l, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(s.Port)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		s.t.Fatalf("proxytest: %v", err)
	}
	s.mu.Lock()
	s.plain = l
	s.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
}

// Endpoint is the endpoint a client would be given for this server.
func (s *Server) Endpoint() endpoint.Endpoint {
	return endpoint.Endpoint{
		Key: "main", Type: endpoint.VlessXHTTPTLS, Host: Host, Port: s.Port, Credential: s.UUID,
		Params:      map[string]string{"path": s.Path, "sni": Host, "mode": "stream-up", "fp": "chrome", "alpn": "h2"},
		DisplayName: "Test endpoint",
	}
}

// Resolve maps the endpoint's host:port to 127.0.0.1, for proxy.Options.Resolve.
func (s *Server) Resolve() map[string]string {
	return map[string]string{
		net.JoinHostPort(Host, strconv.Itoa(s.Port)): net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port)),
	}
}

// ObjectURL is the test object, reachable through the proxy.
func (s *Server) ObjectURL() string { return s.objects.URL + "/object" }

// SetMode changes how the test object answers.
func (s *Server) SetMode(m Mode) {
	s.mu.Lock()
	s.mode = m
	s.mu.Unlock()
}

// Mode is how the test object answers.
type Mode func(w http.ResponseWriter, r *http.Request)

// OK sends n bytes.
func OK(n int) Mode {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(n))
		_, _ = w.Write(make([]byte, n))
	}
}

// StallAfter sends n bytes of a larger object, then nothing until the client
// goes away: the throttling that lets the first KB through.
func StallAfter(n int) Mode {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(n*16))
		_, _ = w.Write(make([]byte, n))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
}

// Status answers with code and a short body.
func Status(code int) Mode {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, fmt.Sprintf("status %d", code), code)
	}
}

// Slow waits before it starts to send n bytes.
func Slow(firstByte time.Duration, n int) Mode {
	ok := OK(n)
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(firstByte):
			ok(w, r)
		case <-r.Context().Done():
		}
	}
}
