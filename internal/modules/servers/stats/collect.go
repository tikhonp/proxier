// Package stats turns what a self-check reads from a server into samples,
// rolls old samples up and answers the Stats tab (docs/processes/servers/
// server-stats.md). Stats are facts: nothing here changes a server's health.
package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Sample is a stored raw sample.
type Sample = store.Sample

// Next builds the sample for cur taken at at, with the deltas against prev.
// CPU % and traffic are computed only when prev exists, the machine's uptime
// grew and no counter went down; otherwise they are NULL and the counters
// still start the next delta (a reboot resets them).
func Next(prev *Sample, cur remote.StatsReading, at time.Time) Sample {
	s := Sample{
		At: db.At(at), Load1: cur.Load1, MemUsed: cur.MemUsed, MemTotal: cur.MemTotal, DiskUsed: cur.DiskUsed, DiskTotal: cur.DiskTotal,
		CPUBusy: int64(cur.CPUBusy), CPUTotal: int64(cur.CPUTotal), RXCounter: int64(cur.RXBytes), TXCounter: int64(cur.TXBytes),
		Uptime: cur.Uptime, Containers: "[]",
	}
	if b, err := json.Marshal(cur.Containers); err == nil && len(cur.Containers) > 0 {
		s.Containers = string(b)
	}
	if prev == nil || cur.Uptime <= prev.Uptime ||
		s.CPUTotal < prev.CPUTotal || s.CPUBusy < prev.CPUBusy || s.RXCounter < prev.RXCounter || s.TXCounter < prev.TXCounter {
		return s
	}
	if dt := s.CPUTotal - prev.CPUTotal; dt > 0 {
		s.CPUPct = sql.NullFloat64{Float64: float64(s.CPUBusy-prev.CPUBusy) / float64(dt) * 100, Valid: true}
	}
	s.RXBytes = sql.NullInt64{Int64: s.RXCounter - prev.RXCounter, Valid: true}
	s.TXBytes = sql.NullInt64{Int64: s.TXCounter - prev.TXCounter, Valid: true}
	return s
}

// Service stores samples and answers queries.
type Service struct {
	DB       *db.DB
	Settings *settings.Store
	Now      func() time.Time
}

// New returns the service.
func New(d *db.DB, st *settings.Store) *Service {
	return &Service{DB: d, Settings: st, Now: time.Now}
}

// Store writes the sample for a reading inside tx, against the server's
// previous sample.
func (s *Service) Store(ctx context.Context, tx *sqlx.Tx, serverID int64, at time.Time, cur remote.StatsReading) error {
	var prev *Sample
	if p, ok, err := store.LastSample(ctx, tx, serverID); err != nil {
		return err
	} else if ok {
		prev = &p
	}
	sample := Next(prev, cur, at)
	sample.ServerID = serverID
	return store.InsertSample(ctx, tx, sample)
}
