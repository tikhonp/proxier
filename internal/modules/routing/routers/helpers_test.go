package routers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/routers"
	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var bg = context.Background()

// lines makes n domain names "<prefix>-<i>.com", one per line.
func lines(prefix string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s-%d.com\n", prefix, i)
	}
	return b.String()
}

func entries(names ...string) []routeros.Entry {
	out := make([]routeros.Entry, 0, len(names))
	for _, n := range names {
		out = append(out, routeros.Entry{Name: strings.TrimPrefix(n, "full:"), Exact: strings.HasPrefix(n, "full:")})
	}
	return out
}

// main is the default list's id.
func mainList(t *testing.T, h *routingtest.Harness) int64 {
	t.Helper()
	l, err := h.Mod.Lists.Default(bg)
	if err != nil {
		t.Fatal(err)
	}
	return l.ID
}

// syncNow runs Sync now and waits for the jobs to settle.
func syncNow(t *testing.T, h *routingtest.Harness, id int64) int64 {
	t.Helper()
	h.StartJobs()
	job, err := h.Mod.Routers.SyncNow(bg, id, "admin")
	if err != nil {
		t.Fatal(err)
	}
	h.Settle()
	return job
}

// lastSync is the router's newest sync row.
func lastSync(t *testing.T, h *routingtest.Harness, id int64) routers.Sync {
	t.Helper()
	s, err := h.Mod.Routers.Syncs(bg, id, 0, 1)
	if err != nil || len(s) == 0 {
		t.Fatalf("no sync: %v", err)
	}
	return s[0]
}

func applied(t *testing.T, h *routingtest.Harness, id int64) map[string]routers.Applied {
	t.Helper()
	a, err := h.Mod.Routers.Applied(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func router(t *testing.T, h *routingtest.Harness, id int64) routers.Router {
	t.Helper()
	r, err := h.Mod.Routers.Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// setCustom replaces a custom service's names.
func setCustom(t *testing.T, h *routingtest.Harness, id int64, tag string, names ...string) {
	t.Helper()
	rows := make([]services.DomainRow, 0, len(names))
	for _, n := range names {
		rows = append(rows, services.DomainRow{Domain: strings.TrimPrefix(n, "full:"), Exact: strings.HasPrefix(n, "full:")})
	}
	if _, err := h.Mod.Services.SaveCustom(bg, id, services.Edit{Name: tag, Tag: tag, Rows: rows}, "admin"); err != nil {
		t.Fatalf("save %s: %v", tag, err)
	}
}

func customNames(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d.com", prefix, i)
	}
	return out
}

// notifies says whether an event passes its type's NotifyIf.
func notifies(t *testing.T, e events.Event) bool {
	t.Helper()
	for _, typ := range routing.Events {
		if typ.Name == e.Type {
			return typ.Notify && (typ.NotifyIf == nil || typ.NotifyIf(e.Payload))
		}
	}
	t.Fatalf("no type %s", e.Type)
	return false
}

func truthy(v any) bool { b, _ := v.(bool); return b }

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// log is a job's log text.
func jobLog(t *testing.T, h *routingtest.Harness, id int64) string {
	t.Helper()
	lines, err := h.App.Jobs.LogTail(bg, id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

var _ = store.ErrNotFound

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// routerOf reads a sync job's router.
func routerOf(t *testing.T, payload []byte) int64 {
	t.Helper()
	var p struct {
		RouterID int64 `json:"router_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.RouterID
}
