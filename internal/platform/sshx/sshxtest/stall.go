package sshxtest

import (
	"context"
	"net"
	"sync"
)

// Links dials for the client (SSH.Dial) and can make the connections it
// opened die without a word, as a tailnet connection does whose peer
// vanished: nothing more arrives, every write blocks, and only closing it
// locally ends it. Freeze is the other side of that: a server that goes quiet
// while its link still takes what the client writes.
type Links struct {
	mu    sync.Mutex
	conns map[string][]*stallConn // by the address dialed
}

// NewLinks returns a dialer whose connections are direct TCP until Stall.
func NewLinks() *Links { return &Links{conns: map[string][]*stallConn{}} }

// Dial connects to addr and keeps the connection for Stall.
func (l *Links) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	sc := &stallConn{Conn: c, stalled: make(chan struct{}), closed: make(chan struct{})}
	l.mu.Lock()
	l.conns[addr] = append(l.conns[addr], sc)
	l.mu.Unlock()
	return sc, nil
}

// Stall kills every connection open to addr now; later ones work.
func (l *Links) Stall(addr string) {
	l.mu.Lock()
	cs := l.conns[addr]
	delete(l.conns, addr)
	l.mu.Unlock()
	for _, c := range cs {
		c.stallOnce.Do(func() { close(c.stalled) })
	}
}

type stallConn struct {
	net.Conn
	stalled, closed      chan struct{}
	stallOnce, closeOnce sync.Once
}

func (c *stallConn) isStalled() bool {
	select {
	case <-c.stalled:
		return true
	default:
		return false
	}
}

// Read drops whatever arrives once stalled, and waits for the local close.
func (c *stallConn) Read(p []byte) (int, error) {
	if !c.isStalled() {
		n, err := c.Conn.Read(p)
		if !c.isStalled() {
			return n, err
		}
	}
	<-c.closed
	return 0, net.ErrClosed
}

// Write blocks once stalled, until the local close.
func (c *stallConn) Write(p []byte) (int, error) {
	select {
	case <-c.stalled:
		<-c.closed
		return 0, net.ErrClosed
	default:
	}
	return c.Conn.Write(p)
}

func (c *stallConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}
