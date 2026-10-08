package refresh_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tikhonp/proxier/internal/modules/routing/routingtest"
	"github.com/tikhonp/proxier/internal/platform/events"
)

var bg = context.Background()

// names makes n domain names "<prefix>-<i>.com".
func names(prefix string, from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%s-%d.com\n", prefix, i)
	}
	return b.String()
}

// snapshots counts a service's snapshots by status.
func snapshots(t *testing.T, h *routingtest.Harness, id int64) map[string]int {
	t.Helper()
	var rows []struct {
		Status string `db:"status"`
		N      int    `db:"n"`
	}
	if err := h.App.DB.R.Select(&rows, `SELECT status, count(*) AS n FROM routing_snapshots WHERE service_id = ? GROUP BY status`, id); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out
}

// round runs the daily round through the job workers and returns its digest.
func round(t *testing.T, h *routingtest.Harness) events.Event {
	t.Helper()
	h.StartJobs()
	id := h.RunSchedule("routing.refresh_round")
	h.Drain()
	if st, e := h.Job(id); st != "succeeded" {
		t.Fatalf("round job %d: %s %s", id, st, e)
	}
	d := h.Events("routing.refresh_digest")
	if len(d) == 0 || d[len(d)-1].Actor != fmt.Sprintf("job:%d", id) {
		t.Fatalf("no digest of job %d: %+v", id, d)
	}
	return d[len(d)-1]
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// notifies evaluates the event type's NotifyIf on a recorded event.
func notifies(t *testing.T, h *routingtest.Harness, e events.Event) bool {
	t.Helper()
	typ, ok := h.App.Events.Lookup(e.Type)
	if !ok {
		t.Fatalf("no type %s", e.Type)
	}
	if typ.NotifyIf != nil {
		return typ.Notify && typ.NotifyIf(e.Payload)
	}
	return typ.Notify
}

func failures(t *testing.T, h *routingtest.Harness, id int64) (n int, lastErr string, checked bool) {
	t.Helper()
	var row struct {
		Failures int     `db:"failures"`
		LastErr  string  `db:"last_error"`
		Checked  *string `db:"last_checked_at"`
	}
	if err := h.App.DB.R.Get(&row, `SELECT failures, last_error, last_checked_at FROM routing_services WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return row.Failures, row.LastErr, row.Checked != nil
}
