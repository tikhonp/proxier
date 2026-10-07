package serverstest

import (
	"context"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
	"github.com/tikhonp/proxier/internal/platform/jobs/jobstest"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"golang.org/x/crypto/ssh"
)

// SSHEnv is an SSH client with an identity on a migrated database, for tests
// of package remote that need a connection but not the whole module.
type SSHEnv struct {
	T       testing.TB
	SSH     *sshx.SSH
	Jobs    *jobstest.Harness
	Key     ssh.PublicKey
	KeyLine string // the authorized_keys line of Proxier's key
}

// NewSSHEnv builds the client.
func NewSSHEnv(t testing.TB) *SSHEnv {
	t.Helper()
	h := jobstest.New(t)
	if err := h.Events.Declare(sshx.Events...); err != nil {
		t.Fatal(err)
	}
	if err := h.Settings.Register(sshx.Section); err != nil {
		t.Fatal(err)
	}
	s := sshx.New(h.DB, h.Vault, h.Events, h.Settings, nil, dbtest.Discard)
	if err := s.EnsureIdentity(context.Background(), "Proxier"); err != nil {
		t.Fatal(err)
	}
	line, _, err := s.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return &SSHEnv{T: t, SSH: s, Jobs: h, Key: pub, KeyLine: line}
}

// Connect opens a connection to v as user; with a password it logs in with it
// (root's first login), otherwise with Proxier's key.
func (e *SSHEnv) Connect(v *VPS, user, password string) (*sshx.Client, error) {
	c, err := e.SSH.Connect(context.Background(), sshx.Target{Hop: sshx.Hop{
		Address: v.Addr, User: user, Password: password, Subject: "server:1",
	}}, nil)
	if err == nil {
		e.T.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

// MustConnect is Connect that fails the test on error.
func (e *SSHEnv) MustConnect(v *VPS, user, password string) *sshx.Client {
	e.T.Helper()
	c, err := e.Connect(v, user, password)
	if err != nil {
		e.T.Fatalf("connect as %s: %v", user, err)
	}
	return c
}

// Access does what install-access does, directly: the deploy user exists with
// Proxier's key and sudo.
func (e *SSHEnv) Access(v *VPS) *sshx.Client {
	e.T.Helper()
	v.AddUser("proxier", e.KeyLine)
	v.SetSudoers("proxier ALL=(ALL) NOPASSWD:ALL\n")
	v.AuthorizeKey("proxier", e.Key)
	return e.MustConnect(v, "proxier", "")
}
