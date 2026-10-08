package stats_test

import (
	"testing"
	"time"

	"github.com/tikhonp/proxier/internal/modules/servers/stats"
)

func TestGapsAreNotZeros(t *testing.T) {
	e := newEnv(t)
	start := e.Now.Add(-2 * time.Hour)
	// every 5 minutes, but unreachable for 30 minutes: six samples are missing
	n := 0
	for i := 0; i <= 24; i++ {
		at := start.Add(time.Duration(i) * 5 * time.Minute)
		if i > 6 && i < 13 {
			continue
		}
		e.store(at, reading(n))
		n++
	}
	s, err := e.Svc.Series(bg, e.Server, stats.Range24h)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Points) != 25-6 {
		t.Fatalf("%d points: the missing samples must not be filled", len(s.Points))
	}
	broken := 0
	for i := 1; i < len(s.Points); i++ {
		d := s.Points[i].At.Sub(s.Points[i-1].At)
		if d > s.Gap {
			broken++
			if d != 35*time.Minute {
				t.Errorf("the gap is %s", d)
			}
		}
	}
	if broken != 1 {
		t.Errorf("%d breaks (gap limit %s), want 1", broken, s.Gap)
	}
	for _, p := range s.Points {
		if p.MemPct == 0 && p.Load1 == 0 {
			t.Errorf("a zero-filled point at %s", p.At)
		}
	}
	// the first sample has no CPU or traffic: nil, not zero
	if s.Points[0].CPU != nil || s.Points[0].RX != nil {
		t.Errorf("first point %+v", s.Points[0])
	}
}

func TestTrafficTodayInDisplayZone(t *testing.T) {
	e := newEnv(t)
	if err := e.Settings.Set(bg, "admin", "general", map[string]string{"general.time_zone": "Europe/Moscow"}); err != nil {
		t.Fatal(err)
	}
	// 00:30 on 8 Oct in Moscow is 21:30 on 7 Oct UTC
	e.Now = time.Date(2026, 10, 7, 21, 30, 0, 0, time.UTC)
	e.store(time.Date(2026, 10, 7, 20, 40, 0, 0, time.UTC), reading(0)) // 23:40 Moscow, yesterday
	e.store(time.Date(2026, 10, 7, 20, 50, 0, 0, time.UTC), reading(1)) // 23:50 Moscow, yesterday: +500000 rx
	e.store(time.Date(2026, 10, 7, 21, 10, 0, 0, time.UTC), reading(2)) // 00:10 Moscow, today: +500000
	e.store(time.Date(2026, 10, 7, 21, 20, 0, 0, time.UTC), reading(3)) // 00:20 Moscow, today: +500000
	rx, tx, err := e.Svc.TrafficToday(bg, e.Server)
	if err != nil {
		t.Fatal(err)
	}
	if rx != 1_000_000 || tx != 200_000 {
		t.Errorf("today rx %d tx %d: only samples since 00:00 Moscow count", rx, tx)
	}
	mrx, _, err := e.Svc.TrafficMonth(bg, e.Server)
	if err != nil {
		t.Fatal(err)
	}
	if mrx != 1_500_000 {
		t.Errorf("this month rx %d", mrx)
	}
}
