package jobs

import (
	"context"
	"errors"
	"time"
)

// DemoPayload is the payload of platform.demo.
type DemoPayload struct {
	Seconds    int  `json:"seconds"`     // default 60
	IntervalMS int  `json:"interval_ms"` // default 1000; tests make it small
	Fail       bool `json:"fail"`
}

// demoType is the Phase 0 exit demo: it writes a live log, can be cancelled
// mid-count, and fails on request.
func demoType() Type {
	return Type{
		Name: "platform.demo", Queue: Maintenance, MaxAttempts: 1,
		Steps: []Step{
			{Name: "prepare", Run: func(_ context.Context, r *Run) error {
				r.Log().Info("Preparing the demo")
				r.Log().Info("Checking the weather on the moon")
				r.Log().Info("Ready")
				return nil
			}},
			{Name: "count", Run: func(ctx context.Context, r *Run) error {
				var p DemoPayload
				if err := r.Payload(&p); err != nil {
					return Permanent(err)
				}
				if p.Seconds <= 0 {
					p.Seconds = 60
				}
				every := time.Second
				if p.IntervalMS > 0 {
					every = time.Duration(p.IntervalMS) * time.Millisecond
				}
				t := time.NewTicker(every)
				defer t.Stop()
				for i := 1; i <= p.Seconds; i++ {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-r.Cancelling():
						r.Log().Warn("Stopped at %d of %d", i-1, p.Seconds)
						return ErrCancelled
					case <-t.C:
						r.Log().Info("Counting: %d of %d", i, p.Seconds)
					}
				}
				return nil
			}},
			{Name: "finish", Run: func(_ context.Context, r *Run) error {
				var p DemoPayload
				if err := r.Payload(&p); err != nil {
					return Permanent(err)
				}
				if p.Fail {
					return errors.New("demo failure, as asked")
				}
				r.Log().Info("Done")
				return nil
			}},
		},
	}
}
