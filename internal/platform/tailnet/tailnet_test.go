package tailnet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db/dbtest"
)

func TestTailnetOffWithoutAuthKey(t *testing.T) {
	dir := t.TempDir()
	n := New(&config.Config{DataDir: dir, TSHostname: "proxier", TSControlURL: "https://hs.example"}, dbtest.Discard)
	ctx := context.Background()

	n.Start(ctx)
	st := n.Status(ctx)
	if st.State != Off || st.Name != "proxier" || st.IP.IsValid() {
		t.Fatalf("status: %+v", st)
	}
	if _, err := n.Dial(ctx, "tcp", "100.64.0.1:22"); !errors.Is(err, ErrOff) {
		t.Fatalf("Dial while off: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("an off node created %d entries in the data dir", len(entries))
	}
	if err := n.Reauthenticate(ctx, ""); err == nil {
		t.Fatal("re-authenticating with no key must fail")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeWithSavedStateStartsWithoutAKey(t *testing.T) {
	dir := t.TempDir()
	n := New(&config.Config{DataDir: dir}, dbtest.Discard)
	if n.hasState() {
		t.Fatal("a fresh data dir has no node")
	}
	if err := os.MkdirAll(filepath.Join(dir, "tailnet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tailnet", "tailscaled.state"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !n.hasState() {
		t.Fatal("a node that joined before must be started again after a restart")
	}
}
