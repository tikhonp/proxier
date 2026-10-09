package routers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// MaxScript is the most of a preview's scripts stored; the rest is cut.
const MaxScript = 4 << 20

// CutHere ends a script that was cut.
const CutHere = "\n… cut here\n"

// PreviewShown is how long the router page shows a preview.
const PreviewShown = time.Hour

type previewPayload struct {
	RouterID int64 `json:"router_id"`
	SyncID   int64 `json:"sync_id"`
}

// Preview starts a read-only run of a sync's first steps: it reads the
// router, plans and builds the scripts, and stores them in a sync row of
// kind preview. A preview already queued for the router is reused.
func (s *Service) Preview(ctx context.Context, id int64, actor string) (int64, error) {
	var syncID int64
	err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if rt, err := store.GetRouter(ctx, tx, id); errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		} else if err != nil {
			return err
		} else if rt.State != StateActive {
			return ErrNotActive
		}
		var err error
		if syncID, err = store.InsertSync(ctx, tx, store.Sync{RouterID: id, Kind: "preview", StartedAt: s.now()}); err != nil {
			return err
		}
		e, err := s.d.Jobs.Enqueue(ctx, tx, jobs.Request{
			Type: JobPreview, Payload: previewPayload{RouterID: id, SyncID: syncID}, ResourceKey: resourceKey(id),
			CoalescingKey: fmt.Sprintf("preview:%d", id), Subject: Subject(id), CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		if !e.Merged {
			return store.SetSyncJob(ctx, tx, syncID, e.ID)
		}
		if err := store.DeleteSync(ctx, tx, syncID); err != nil {
			return err
		}
		prev, err := store.RunningPreview(ctx, tx, id, syncID)
		if err != nil {
			return err
		}
		syncID = prev.ID
		return nil
	})
	if err == nil {
		s.d.Jobs.Kick()
	}
	return syncID, err
}

// stepPreview: connect, read, plan, build the files; nothing is applied or
// recorded besides the row.
func (s *Service) stepPreview(ctx context.Context, r *jobs.Run) error {
	var p previewPayload
	if err := r.Payload(&p); err != nil {
		return jobs.Permanent(err)
	}
	rt, err := store.GetRouter(ctx, s.d.DB.R, p.RouterID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if rt.State != StateActive {
		return jobs.Permanent(fmt.Errorf("%s is %s: no preview", rt.Name, rt.State))
	}
	failed := func(step string, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pr := ProblemOf(connOf(rt), err)
		f := &failure{step: step, hop: pr.Hop, text: pr.Text + Untouched, err: err}
		if step != StepConnect {
			f.hop, f.text = "", "Reading the router failed: "+err.Error()+"."+Untouched
		}
		werr := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			return store.SyncFailed(ctx, tx, p.SyncID, f.step, f.hop, f.text, s.now())
		})
		return errors.Join(werr, jobs.Permanent(f))
	}
	cl, err := s.connect(ctx, r, rt)
	if err != nil {
		return failed(StepConnect, err)
	}
	defer func() { _ = cl.Close() }()
	names := connOf(rt).Names
	st, err := s.read(ctx, cl, names)
	if err != nil {
		return failed(StepRead, err)
	}
	plan, view, err := s.plan(ctx, rt, st, Extra{})
	if err != nil {
		return err
	}
	logSkips(r.Log(), view)
	files := routeros.Files(names, "preview", fmt.Sprintf("proxier sync · router %s · preview", rt.Name), Blocks(plan))
	var b strings.Builder
	for i, f := range files {
		if i > 0 {
			b.WriteString("\n")
		}
		b.Write(f.Body)
	}
	script := b.String()
	if len(script) > MaxScript {
		script = script[:MaxScript] + CutHere
	}
	added, updated, removed, unchanged, recorded := Count(plan)
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := s.recordUnmanaged(ctx, tx, rt, st, unmanagedIn(plan), r.Info().Actor()); err != nil {
			return err
		}
		if err := store.SyncPlanned(ctx, tx, p.SyncID, jsonOf(plan), db.At(st.ReadAt), store.Sync{
			Added: added, Updated: updated, Removed: removed, Unchanged: unchanged, Recorded: recorded,
		}); err != nil {
			return err
		}
		return store.SyncDone(ctx, tx, p.SyncID, script, s.now())
	})
}
