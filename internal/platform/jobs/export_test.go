package jobs

import (
	"context"
	"time"
)

// SetTick pretends pool or scheduler name last looped at t, and that Start ran.
func (s *System) SetTick(name string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
	s.ticks[name] = t
}

// SetJitter replaces the jitter source.
func (s *System) SetJitter(f func(time.Duration) time.Duration) { s.jitter = f }

// NextRun exposes the schedule arithmetic.
func (s *System) NextRun(c Schedule, now time.Time) time.Time {
	return s.next(context.Background(), c, now)
}

// OpenSecretsForTest opens a job's sealed secrets.
func (s *System) OpenSecretsForTest(id int64, blob []byte) (map[string]string, error) {
	return s.openSecrets(id, blob)
}
