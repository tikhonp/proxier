package stats_test

import (
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/stats"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

func TestRollup(t *testing.T) {
	e := newEnv(t)
	// 12 samples in one hour, 8 days ago, plus one from an hour ago
	hour := e.Now.Add(-8 * 24 * time.Hour).Truncate(time.Hour)
	for i := 0; i < 12; i++ {
		r := reading(i)
		r.Load1 = float64(i)
		e.store(hour.Add(time.Duration(i)*5*time.Minute), r)
	}
	e.store(e.Now.Add(-time.Hour), reading(20))

	// an old check result and a recent one
	for _, at := range []time.Time{e.Now.Add(-31 * 24 * time.Hour), e.Now.Add(-time.Hour)} {
		if err := e.DB.Write(bg, func(tx *sqlx.Tx) error {
			return store.InsertCheckResult(bg, tx, store.CheckResult{ServerID: e.Server, Kind: "self", Vantage: "home", At: db.At(at), OK: true})
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := e.Svc.Rollup(bg); err != nil {
		t.Fatal(err)
	}
	rows, err := store.HourlyRows(bg, e.DB.R, e.Server, db.At(hour.Add(-time.Hour)), db.At(hour.Add(time.Hour)))
	if err != nil || len(rows) != 1 {
		t.Fatalf("hourly rows %v %v", rows, err)
	}
	h := rows[0]
	if h.Samples != 12 || h.Load1 != 5.5 {
		t.Errorf("hour %+v: want 12 samples with load averaging 5.5", h)
	}
	// the first sample has no delta; the other eleven add 500 000 rx and 100 000 tx each
	if h.RXBytes != 11*500_000 || h.TXBytes != 11*100_000 {
		t.Errorf("traffic is summed: rx %d tx %d", h.RXBytes, h.TXBytes)
	}
	if !h.CPUPct.Valid || h.CPUPct.Float64 != 10 {
		t.Errorf("cpu average over the 11 samples that have one: %+v", h.CPUPct)
	}
	left, err := store.Samples(bg, e.DB.R, e.Server, db.At(hour.Add(-time.Hour)), db.At(e.Now))
	if err != nil || len(left) != 1 {
		t.Fatalf("raw samples left: %d %v: the old ones are deleted, the recent one stays", len(left), err)
	}
	var n int
	if err := e.DB.R.Get(&n, `SELECT count(*) FROM servers_check_results`); err != nil || n != 1 {
		t.Errorf("check results %d: the 31 day old one goes", n)
	}

	// the 30 d series reads the rolled hour and the recent raw sample
	s, err := e.Svc.Series(bg, e.Server, stats.Range30d)
	if err != nil || len(s.Points) != 2 || !s.Hourly {
		t.Fatalf("30 d series %+v %v", s.Points, err)
	}
	// rolling up again changes nothing
	if err := e.Svc.Rollup(bg); err != nil {
		t.Fatal(err)
	}
	if rows2, _ := store.HourlyRows(bg, e.DB.R, e.Server, db.At(hour.Add(-time.Hour)), db.At(hour.Add(time.Hour))); len(rows2) != 1 || rows2[0].Samples != 12 {
		t.Errorf("second rollup: %+v", rows2)
	}
}
