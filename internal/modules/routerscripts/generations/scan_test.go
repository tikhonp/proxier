package generations_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/generations"
	"github.com/tikhonp/proxier/internal/modules/routerscripts/rscriptstest"
)

func TestFetchURLExpires(t *testing.T) {
	h := rscriptstest.New(t)
	gid := generated(t, h)
	_, token := createURL(t, h, gid)
	h.Advance(59 * time.Minute)
	if n, err := h.Mod.Generations.Expire(bg, "job:1"); err != nil || n != 0 {
		t.Fatalf("within its hour: %d %v", n, err)
	}
	h.Advance(time.Minute)
	// before any scan: the fetch refuses at once and uses nothing
	if res := fetch(h, http.MethodGet, token, ""); res.Code != http.StatusNotFound {
		t.Fatalf("after its hour: %d", res.Code)
	}
	if rows := urlRows(t, h); rows[0].State != "waiting" || len(h.Events("routerscript.fetched")) != 0 {
		t.Fatalf("the refused fetch changed something: %+v", rows)
	}
	n, err := h.Mod.Generations.Expire(bg, "job:2")
	if err != nil || n != 1 {
		t.Fatalf("scan: %d %v", n, err)
	}
	rows := urlRows(t, h)
	if rows[0].State != "expired" || rows[0].Token != nil || rows[0].Lookup != nil {
		t.Fatalf("expired: %+v", rows)
	}
	ev := h.Events("routerscript.fetch_url_expired")
	if len(ev) != 1 || ev[0].Actor != "job:2" || ev[0].Payload["expired"] != "2026-10-09T13:00:00.000Z" {
		t.Fatalf("event: %+v", ev)
	}
	if n, err := h.Mod.Generations.Expire(bg, "job:3"); err != nil || n != 0 || len(h.Events("routerscript.fetch_url_expired")) != 1 {
		t.Fatalf("the next scan: %d %v", n, err)
	}
	// the job and its schedule
	types, sch := h.Mod.JobTypes(), h.Mod.Schedules()
	if len(types) != 1 || types[0].Name != generations.JobFetchURLs || types[0].Queue != "maintenance" || types[0].MaxAttempts != 1 || !types[0].Quiet ||
		len(types[0].Steps) != 1 || types[0].Steps[0].Name != "expire" {
		t.Errorf("job type: %+v", types)
	}
	if len(sch) != 1 || sch[0].Name != generations.JobFetchURLs || sch[0].Every != 5*time.Minute {
		t.Errorf("schedule: %+v", sch)
	}
}
