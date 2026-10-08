package stats_test

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/stats"
	"github.com/tikhonp/proxier/internal/modules/servers/storetest"
)

var bg = context.Background()

type env struct {
	*storetest.Env
	Svc    *stats.Service
	Now    time.Time
	Server int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{Env: storetest.New(t), Now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	e.Svc = stats.New(e.DB, e.Settings)
	e.Svc.Now = func() time.Time { return e.Now }
	e.Server = e.AddServer("nl-1", "active")
	return e
}

// reading is a stats reading with round numbers; the counters grow with n.
func reading(n int) remote.StatsReading {
	return remote.StatsReading{
		Load1: 0.5, CPUBusy: uint64(1000 + 100*n), CPUTotal: uint64(10000 + 1000*n),
		MemUsed: 1 << 29, MemTotal: 2 << 30, DiskUsed: 5 << 30, DiskTotal: 20 << 30,
		Iface: "eth0", RXBytes: uint64(1_000_000 + 500_000*n), TXBytes: uint64(2_000_000 + 100_000*n),
		Uptime: float64(100000 + 300*n),
	}
}

func (e *env) store(at time.Time, r remote.StatsReading) {
	e.T.Helper()
	if err := e.DB.Write(bg, func(tx *sqlx.Tx) error { return e.Svc.Store(bg, tx, e.Server, at, r) }); err != nil {
		e.T.Fatal(err)
	}
}
