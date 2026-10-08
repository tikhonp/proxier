package stats

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
)

// Retention of each kind of row (docs/data-model.md).
const (
	RawKeep    = 7 * 24 * time.Hour
	HourlyKeep = 90 * 24 * time.Hour
	ResultKeep = 30 * 24 * time.Hour
	batchSize  = 5000
)

// Rollup moves raw samples older than 7 days into hourly rows (averages;
// traffic sums), in batches of 5 000 rows per transaction, then drops hourly
// rows older than 90 days and check and reference results older than 30 days.
func (s *Service) Rollup(ctx context.Context) error {
	now := s.Now().UTC()
	cutoff := db.At(now.Add(-RawKeep).Truncate(time.Hour))
	for {
		n := 0
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			n, err = rollBatch(ctx, tx, cutoff)
			return err
		})
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := store.PruneHourly(ctx, tx, db.At(now.Add(-HourlyKeep))); err != nil {
			return err
		}
		return store.PruneResults(ctx, tx, db.At(now.Add(-ResultKeep)))
	})
}

type hourKey struct {
	server int64
	hour   time.Time
}

// rollBatch rolls one batch and returns how many raw rows it consumed. A full
// batch leaves out its last hour, which the next batch rolls whole; an hour
// split anyway (a batch that is one hour) merges into the stored row.
func rollBatch(ctx context.Context, tx *sqlx.Tx, cutoff db.Time) (int, error) {
	rows, err := store.SamplesBefore(ctx, tx, cutoff, batchSize)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	if len(rows) == batchSize {
		last := rows[len(rows)-1].At.Truncate(time.Hour)
		cut := len(rows)
		for cut > 0 && !rows[cut-1].At.Truncate(time.Hour).Before(last) {
			cut--
		}
		if cut > 0 {
			rows = rows[:cut]
		}
	}
	type acc struct {
		n                                  int
		load, cpu                          float64
		cpuN                               int
		mem, memTot, disk, diskTot, rx, tx int64
	}
	groups := map[hourKey]*acc{}
	for _, r := range rows {
		k := hourKey{r.ServerID, r.At.Truncate(time.Hour)}
		a := groups[k]
		if a == nil {
			a = &acc{}
			groups[k] = a
		}
		a.n++
		a.load += r.Load1
		if r.CPUPct.Valid {
			a.cpu += r.CPUPct.Float64
			a.cpuN++
		}
		a.mem += r.MemUsed
		a.memTot += r.MemTotal
		a.disk += r.DiskUsed
		a.diskTot += r.DiskTotal
		a.rx += r.RXBytes.Int64
		a.tx += r.TXBytes.Int64
	}
	for k, a := range groups {
		h := store.Hourly{
			ServerID: k.server, Hour: db.At(k.hour), Samples: a.n, Load1: a.load / float64(a.n),
			MemUsed: a.mem / int64(a.n), MemTotal: a.memTot / int64(a.n), DiskUsed: a.disk / int64(a.n), DiskTotal: a.diskTot / int64(a.n),
			RXBytes: a.rx, TXBytes: a.tx,
		}
		if a.cpuN > 0 {
			h.CPUPct.Float64, h.CPUPct.Valid = a.cpu/float64(a.cpuN), true
		}
		if old, ok, err := store.GetHourly(ctx, tx, k.server, h.Hour); err != nil {
			return 0, err
		} else if ok {
			h = merge(old, h)
		}
		if err := store.PutHourly(ctx, tx, h); err != nil {
			return 0, err
		}
	}
	return len(rows), store.DeleteSamples(ctx, tx, rows)
}

// merge combines two parts of one hour, weighting averages by sample count.
func merge(a, b store.Hourly) store.Hourly {
	n := a.Samples + b.Samples
	w := func(x, y float64) float64 { return (x*float64(a.Samples) + y*float64(b.Samples)) / float64(n) }
	wi := func(x, y int64) int64 { return int64(w(float64(x), float64(y))) }
	out := store.Hourly{
		ServerID: a.ServerID, Hour: a.Hour, Samples: n, Load1: w(a.Load1, b.Load1),
		MemUsed: wi(a.MemUsed, b.MemUsed), MemTotal: wi(a.MemTotal, b.MemTotal), DiskUsed: wi(a.DiskUsed, b.DiskUsed), DiskTotal: wi(a.DiskTotal, b.DiskTotal),
		RXBytes: a.RXBytes + b.RXBytes, TXBytes: a.TXBytes + b.TXBytes,
	}
	switch {
	case a.CPUPct.Valid && b.CPUPct.Valid:
		out.CPUPct.Float64, out.CPUPct.Valid = w(a.CPUPct.Float64, b.CPUPct.Float64), true
	case a.CPUPct.Valid:
		out.CPUPct = a.CPUPct
	default:
		out.CPUPct = b.CPUPct
	}
	return out
}
