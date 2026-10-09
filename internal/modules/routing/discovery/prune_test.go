package discovery_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/discovery"
	"github.com/tikhonp/proxier/internal/platform/db"
)

func TestOldRunsPruned(t *testing.T) {
	h, b := newH(t)
	for _, u := range []string{"https://old.example/", "https://new.example/"} {
		b.Site(u, "", loaded(u, "", req(u, u[8:len(u)-1])))
	}
	old := wait(t, h, start(t, h, discovery.Start{Website: "old.example", Via: discovery.ViaDirect}), discovery.Done)
	recent := wait(t, h, start(t, h, discovery.Start{Website: "new.example", Via: discovery.ViaDirect}), discovery.Done)
	dir := filepath.Join(h.App.Cfg.DataDir, "discovery")
	for _, r := range []discovery.Run{old, recent} {
		if len(r.Visits) != 1 || len(r.Visits[0].Screenshots) != 1 {
			t.Fatalf("run %d: %+v", r.ID, r.Visits)
		}
		if p, err := h.Mod.Discovery.Screenshot(r.ID, r.Visits[0].Screenshots[0]); err != nil || filepath.Dir(p) != filepath.Join(dir, itoa(r.ID)) {
			t.Fatalf("screenshot of %d: %s %v", r.ID, p, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, itoa(old.ID))); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("run directory: %v %v", fi, err)
	}
	h.Exec(`UPDATE routing_discovery_runs SET created_at = ? WHERE id = ?`, db.At(h.Now.Add(-31*24*time.Hour)), old.ID)
	stray := filepath.Join(dir, "999")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := h.Mod.Prune(bg, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Mod.Discovery.Get(bg, old.ID); err != discovery.ErrNotFound {
		t.Errorf("the old run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, itoa(old.ID))); !os.IsNotExist(err) {
		t.Errorf("the old screenshots: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("a directory without a run stayed: %v", err)
	}
	if r, err := h.Mod.Discovery.Get(bg, recent.ID); err != nil || len(r.Hosts) != 1 {
		t.Errorf("the newer run: %+v %v", r, err)
	}
	if _, err := h.Mod.Discovery.Screenshot(recent.ID, recent.Visits[0].Screenshots[0]); err != nil {
		t.Errorf("the newer screenshot: %v", err)
	}
	for _, bad := range []string{"../../proxier.db", "1-1.png", ""} {
		if _, err := h.Mod.Discovery.Screenshot(recent.ID, bad); err == nil {
			t.Errorf("screenshot %q served", bad)
		}
	}
}
