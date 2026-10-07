package cloudflaretest

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/dns"
	"github.com/tikhonp/proxier/internal/modules/servers/dns/cloudflare"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// Client is a client for the fake whose retries do not wait; Slept says how
// long it would have.
func (f *Fake) Client() *cloudflare.Client { return f.ClientFor(f.token) }

// ClientFor is a client holding token, which the fake accepts only when it is
// its own.
func (f *Fake) ClientFor(token string) *cloudflare.Client {
	c := cloudflare.New(token)
	c.BaseURL = f.srv.URL
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		f.mu.Lock()
		f.slept = append(f.slept, d)
		f.mu.Unlock()
		return ctx.Err()
	}
	return c
}

// Slept lists the waits the client asked for between retries.
func (f *Fake) Slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.slept...)
}

// Driver is a settings-backed driver on a real settings store whose token is
// the fake's and whose allowed zones are allowed.
func (f *Fake) Driver(t *testing.T, allowed ...string) (dns.Driver, *settings.Store) {
	t.Helper()
	d := dbtest.Open(t)
	v, err := vault.New(bytes.Repeat([]byte{3}, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cat := events.NewCatalog()
	if err := cat.Declare(settings.ChangedEvent); err != nil {
		t.Fatal(err)
	}
	st := settings.New(d, v, cat)
	if err := st.Register(cloudflare.Section); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(context.Background(), "admin", "cloudflare", map[string]string{
		cloudflare.TokenKey: f.token, cloudflare.ZonesKey: strings.Join(allowed, ","),
	}); err != nil {
		t.Fatal(err)
	}
	return cloudflare.NewDriver(st, func(string) *cloudflare.Client { return f.Client() }), st
}
