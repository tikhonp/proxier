package stats_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/store"
)

func lastSample(e *env) store.Sample {
	e.T.Helper()
	s, ok, err := store.LastSample(bg, e.DB.R, e.Server)
	if err != nil || !ok {
		e.T.Fatalf("no sample: %v", err)
	}
	return s
}

func TestFirstSampleHasNoDeltas(t *testing.T) {
	e := newEnv(t)
	e.store(e.Now, reading(0))
	s := lastSample(e)
	if s.CPUPct.Valid || s.RXBytes.Valid || s.TXBytes.Valid {
		t.Errorf("the first sample has deltas: %+v", s)
	}
	// load, memory, disk and uptime are stored, and so are the counters the next delta starts from
	if s.Load1 != 0.5 || s.MemTotal != 2<<30 || s.DiskUsed != 5<<30 || s.Uptime != 100000 || s.RXCounter != 1_000_000 || s.CPUTotal != 10000 {
		t.Errorf("sample %+v", s)
	}
}

func TestSecondSampleDeltas(t *testing.T) {
	e := newEnv(t)
	e.store(e.Now, reading(0))
	e.store(e.Now.Add(5*time.Minute), reading(1))
	s := lastSample(e)
	if !s.CPUPct.Valid || s.CPUPct.Float64 != 10 {
		t.Errorf("cpu %+v: want 100 busy of 1000 = 10 %%", s.CPUPct)
	}
	if s.RXBytes.Int64 != 500_000 || s.TXBytes.Int64 != 100_000 {
		t.Errorf("traffic rx %v tx %v", s.RXBytes, s.TXBytes)
	}
}

func TestRebootResetsDeltas(t *testing.T) {
	e := newEnv(t)
	e.store(e.Now, reading(5))
	// rebooted: uptime and every counter start over
	r := reading(0)
	r.Uptime = 20
	e.store(e.Now.Add(5*time.Minute), r)
	if s := lastSample(e); s.CPUPct.Valid || s.RXBytes.Valid || s.TXBytes.Valid {
		t.Errorf("a sample after a reboot has deltas: %+v", s)
	}
	// the next one starts from the new counters and is normal
	r2 := reading(1)
	r2.Uptime = 320
	e.store(e.Now.Add(10*time.Minute), r2)
	s := lastSample(e)
	if !s.RXBytes.Valid || s.RXBytes.Int64 != 500_000 || !s.CPUPct.Valid {
		t.Errorf("after the reboot: %+v", s)
	}
}
