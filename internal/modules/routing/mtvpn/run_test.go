package mtvpn_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/mtvpn"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// everywhere searches every table's every column for s.
func everywhere(t *testing.T, h *routingtest.Harness, s string) []string {
	t.Helper()
	var tables []string
	if err := h.App.DB.R.Select(&tables, `SELECT name FROM sqlite_master WHERE type = 'table'`); err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, tb := range tables {
		rows, err := h.App.DB.R.Query(`SELECT * FROM "` + tb + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if strings.Contains(fmt.Sprint(v), s) || (func() bool { b, ok := v.([]byte); return ok && bytes.Contains(b, []byte(s)) })() {
					hits = append(hits, tb+"."+cols[i])
				}
			}
		}
		_ = rows.Close()
	}
	return hits
}

func TestImportTodaysFile(t *testing.T) {
	var log bytes.Buffer
	h := routingtest.New(t, routingtest.LogTo(&log))
	text := today(h)
	im := preview(t, h, text)
	gotTags := []string{}
	for _, r := range im.Rows {
		gotTags = append(gotTags, r.Tag)
	}
	if !reflect.DeepEqual(gotTags, []string{"anthropic", "tunneled-domains", "youtube", "google", "instagram.com"}) {
		t.Fatalf("rows: %v", gotTags)
	}
	if im.Rows[0].From != "services" || im.Rows[2].From != "mtvpn-main.txt" || !im.Rows[1].URL {
		t.Errorf("rows: %+v", im.Rows)
	}
	if !reflect.DeepEqual(im.Ignored, []string{"router", "shadowrocket_upload", "shadowrocket_user", "shadowrocket_password"}) {
		t.Errorf("ignored: %v", im.Ignored)
	}
	sr := im.Shadowrocket
	if !sr.Offered || !sr.Create || sr.Name != "iphone" || sr.Lines != 6 || sr.ListID != 1 {
		t.Errorf("base: %+v", sr)
	}
	im = run(t, h, im, all(im))
	if im.State != "done" {
		t.Fatalf("state %s", im.State)
	}
	for _, r := range im.Rows {
		if r.Outcome != mtvpn.Created || r.ServiceID == 0 {
			t.Errorf("row %s: %+v", r.Tag, r)
		}
	}
	if got := tags(t, h, 1); !reflect.DeepEqual(got, []string{"anthropic", "tunneled-domains", "youtube", "google", "instagram.com"}) {
		t.Errorf("Main: %v", got)
	}
	it, _ := h.Mod.Services.ByTag(bg(), "tunneled-domains")
	if it.Source != selector.URL {
		t.Errorf("tunneled-domains: %+v", it)
	}
	if im.Shadowrocket.Outcome != mtvpn.Created || im.Shadowrocket.ID == 0 {
		t.Fatalf("config: %+v", im.Shadowrocket)
	}
	c, err := h.Mod.Shadowrocket.Get(bg(), im.Shadowrocket.ID)
	if err != nil || c.Name != "iphone" || c.List != "Main" || c.Rules == 0 {
		t.Errorf("config: %+v %v", c, err)
	}
	if n := len(h.Events("routing.service_added")); n != 5 {
		t.Errorf("%d service_added", n)
	}
	if ev := h.Events("routing.list_updated"); len(ev) != 1 || ev[0].Payload["added"] != "anthropic, tunneled-domains, youtube, google, instagram.com" {
		t.Errorf("list_updated: %+v", ev)
	}
	if n := len(h.Events("routing.shadowrocket_created")); n != 1 {
		t.Errorf("%d shadowrocket_created", n)
	}
	if hits := everywhere(t, h, password); len(hits) != 0 {
		t.Errorf("the password is stored in %v", hits)
	}
	if hits := everywhere(t, h, "copyparty.example"); len(hits) != 0 {
		t.Errorf("an ignored value is stored in %v", hits)
	}
	if strings.Contains(log.String(), password) {
		t.Error("the password is in the log")
	}
}

func TestUnresolvableSelectorSkipped(t *testing.T) {
	h := routingtest.New(t)
	h.Up.V2fly("anthropic", "anthropic.com\n")
	im := preview(t, h, "services:\n  - iplist:nosuchsite\n  - anthropic\n")
	im = run(t, h, im, all(im))
	if r := im.Rows[0]; r.Outcome != mtvpn.Failed || r.Error != "not found on main, beta, russia" || r.ServiceID != 0 {
		t.Errorf("nosuchsite: %+v", r)
	}
	if r := im.Rows[1]; r.Outcome != mtvpn.Created {
		t.Errorf("anthropic: %+v", r)
	}
	if got := tags(t, h, 1); !reflect.DeepEqual(got, []string{"anthropic"}) {
		t.Errorf("Main: %v", got)
	}
	if im.State != "done" {
		t.Errorf("state %s", im.State)
	}
}

func TestConvertToCustom(t *testing.T) {
	h := routingtest.New(t)
	text := today(h)
	im := preview(t, h, text)
	c := all(im)
	c.Convert[1] = true
	im = run(t, h, im, c)
	r := im.Rows[1]
	if r.Outcome != mtvpn.Converted {
		t.Fatalf("row: %+v", r)
	}
	it, err := h.Mod.Services.Get(bg(), r.ServiceID)
	if err != nil || it.Source != selector.Custom || it.Origin != "import" || it.Tag != "tunneled-domains" || it.Name != "tunneled-domains" {
		t.Fatalf("service: %+v %v", it, err)
	}
	snap, _ := h.Mod.Services.Accepted(bg(), it.ID)
	// www.example.com is absorbed under example.com, as a custom save does
	if !reflect.DeepEqual(snap.Set.Suffix, []string{"example.com"}) || !reflect.DeepEqual(snap.Set.Exact, []string{"api.example.org"}) {
		t.Errorf("names: %+v", snap.Set)
	}
	rows, err := h.Mod.Services.CustomRows(bg(), it.ID)
	if err != nil || len(rows) != 2 {
		t.Errorf("custom rows: %+v %v", rows, err)
	}
	// the round refreshes upstream services only: the file isn't fetched again
	before := h.Up.Requests("/files/tunneled-domains.txt")
	h.RunSchedule("routing.refresh_round")
	h.Drain()
	if after := h.Up.Requests("/files/tunneled-domains.txt"); after != before {
		t.Errorf("the URL was fetched again: %d → %d", before, after)
	}
}

func TestImportTwiceAddsNothing(t *testing.T) {
	h := routingtest.New(t)
	text := today(h)
	first := preview(t, h, text)
	run(t, h, first, all(first))
	updated := len(h.Events("routing.list_updated"))
	im := preview(t, h, text)
	for _, r := range im.Rows {
		if r.Status != mtvpn.StatusExists {
			t.Errorf("row %s: %s", r.Tag, r.Status)
		}
	}
	if sr := im.Shadowrocket; !sr.Offered || sr.Create || sr.Name != "iphone-2" {
		t.Errorf("config: %+v", sr)
	}
	im = run(t, h, im, all(im))
	for _, r := range im.Rows {
		if r.Outcome != mtvpn.Reused {
			t.Errorf("row %s: %s", r.Tag, r.Outcome)
		}
	}
	if n := len(h.Events("routing.list_updated")); n != updated {
		t.Errorf("list_updated %d → %d", updated, n)
	}
	if n := len(h.Events("routing.service_added")); n != 5 {
		t.Errorf("%d service_added", n)
	}
	if cs, _ := h.Mod.Shadowrocket.List(bg()); len(cs) != 1 {
		t.Errorf("configs: %+v", cs)
	}
	if im.State != "done" {
		t.Errorf("state %s", im.State)
	}
	// running a finished import again is refused
	if _, err := h.Mod.Import.Run(bg(), im.ID, all(im), "admin"); err != mtvpn.ErrNotPreview {
		t.Errorf("run again: %v", err)
	}
}

func TestGuardSkipsImportedRow(t *testing.T) {
	h := routingtest.New(t)
	h.Servers.Put(1, "nl-1", "nl-1.hosts.example.net")
	h.Up.V2fly("anthropic", "anthropic.com\n")
	h.Up.V2fly("hosts", "example.net\n")
	im := preview(t, h, "services:\n  - anthropic\n  - hosts\n")
	im = run(t, h, im, all(im))
	if r := im.Rows[1]; r.Outcome != mtvpn.Skipped || r.Guard == nil || r.Guard.Domain != "example.net" || r.Guard.Server != "nl-1" || r.Guard.Hostname != "nl-1.hosts.example.net" {
		t.Errorf("hosts: %+v", r)
	}
	if got := tags(t, h, 1); !reflect.DeepEqual(got, []string{"anthropic"}) {
		t.Errorf("Main: %v", got)
	}
	if ev := h.Events("routing.list_refused_server_hostname"); len(ev) != 1 || ev[0].Payload["service"] != "hosts" {
		t.Errorf("refusals: %+v", ev)
	}
}

func TestImportResumes(t *testing.T) {
	h := routingtest.New(t)
	for _, n := range []string{"a", "b", "c", "d"} {
		h.Up.V2fly(n, n+".example.com\n")
	}
	im := preview(t, h, "services:\n  - a\n  - b\n  - c\n  - d\n")
	h.Up.Hang("/v2fly/data/c", true)
	if _, err := h.Mod.Import.Run(bg(), im.ID, all(im), "admin"); err != nil {
		t.Fatal(err)
	}
	h.StartJobs()
	waitFor(t, "two rows", func() bool {
		cur, _ := h.Mod.Import.Get(bg(), im.ID)
		return cur.Rows[1].Outcome == mtvpn.Created
	})
	h.StopJobs() // a shutdown: the job is interrupted mid-row
	cur, _ := h.Mod.Import.Get(bg(), im.ID)
	if cur.State != "running" || cur.Rows[2].Outcome != "" {
		t.Fatalf("after the stop: %s %+v", cur.State, cur.Rows[2])
	}
	h.Up.Hang("/v2fly/data/c", false)
	h.StartJobs()
	h.Drain()
	cur, _ = h.Mod.Import.Get(bg(), im.ID)
	if cur.State != "done" {
		t.Fatalf("state %s", cur.State)
	}
	if n := len(h.Events("routing.service_added")); n != 4 {
		t.Errorf("%d service_added", n)
	}
	var n int
	_ = h.App.DB.R.Get(&n, `SELECT count(*) FROM routing_services`)
	if n != 4 {
		t.Errorf("%d services", n)
	}
	if got := tags(t, h, 1); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("Main: %v", got)
	}
	var job sql.NullInt64
	_ = h.App.DB.R.Get(&job, `SELECT job_id FROM routing_imports WHERE id = ?`, im.ID)
	if st, _ := h.Job(job.Int64); st != "succeeded" {
		t.Errorf("job %s", st)
	}
}
