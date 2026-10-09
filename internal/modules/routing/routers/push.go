package routers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/routeros"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

// push uploads each file over SFTP, imports it, scans the output and always
// deletes it. After a file succeeds its blocks are recorded as applied (a
// split update once its last part landed; a removal forgets its tag). After
// a file fails, the router is read again and each of its tags that landed
// anyway is recorded; the others keep their old applied state.
func (s *Service) push(ctx context.Context, r *jobs.Run, cl *sshx.Client, rt store.Router, plan []TagPlan, files []routeros.File) error {
	log := r.Log()
	want := map[string][]routeros.Entry{}
	for _, p := range plan {
		if p.Action == Update {
			want[p.Tag] = p.Entries
		}
	}
	for i, f := range files {
		descs := make([]string, 0, len(f.Parts))
		for _, p := range f.Parts {
			descs = append(descs, p.Describe())
		}
		log.Info("File %d/%d %s: %s.", i+1, len(files), f.Name, strings.Join(descs, ", "))
		err := cl.Put(ctx, f.Name, f.Body)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &failure{step: StepPush, text: fmt.Sprintf("Uploading %s failed (%v): does Proxier's group have the ftp policy?", f.Name, err), err: err}
		}
		res, err := cl.Run(ctx, routeros.CmdImport(f.Name), sshx.RunOptions{Timeout: routeros.ImportTimeout, Log: log})
		if err == nil {
			err = routeros.ScanImport(res.ExitCode, append(append([]byte{}, res.Stdout...), res.Stderr...))
		}
		if ctx.Err() != nil {
			return ctx.Err() // a shutdown or a cancel: the resume removes the file
		}
		if _, rerr := cl.Run(ctx, routeros.CmdRemoveFile(f.Name), sshx.RunOptions{Timeout: time.Minute}); rerr != nil {
			log.Warn("Removing %s: %v", f.Name, rerr)
		}
		if err != nil {
			s.landed(ctx, r, cl, rt, f, want)
			return &failure{step: StepPush, text: fmt.Sprintf("%s: %s", f.Name, strings.TrimPrefix(err.Error(), "import: ")), err: err}
		}
		if err := s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			now := s.now()
			for _, p := range f.Parts {
				switch {
				case p.Remove:
					if err := store.DeleteRouterTag(ctx, tx, rt.ID, p.Tag); err != nil {
						return err
					}
				case p.Last():
					if err := setApplied(ctx, tx, rt.ID, p.Tag, want[p.Tag], now); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// landed reads the router after a failed file and records each of its tags
// that ended as planned anyway: /import stops at the first failing line, so
// earlier blocks of the file may have run.
func (s *Service) landed(ctx context.Context, r *jobs.Run, cl *sshx.Client, rt store.Router, f routeros.File, want map[string][]routeros.Entry) {
	st, err := s.read(ctx, cl, connOf(rt).Names)
	if err != nil {
		r.Log().Warn("Reading the router after the failed file: %v", err)
		return
	}
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		now := s.now()
		for _, p := range f.Parts {
			t := st.Tags[p.Tag]
			switch {
			case p.Remove && t.empty():
				r.Log().Info("%s: removed before the failure.", p.Tag)
				if err := store.DeleteRouterTag(ctx, tx, rt.ID, p.Tag); err != nil {
					return err
				}
			case !p.Remove && p.Last() && Matches(t, want[p.Tag], st.Names):
				r.Log().Info("%s: installed before the failure.", p.Tag)
				if err := setApplied(ctx, tx, rt.ID, p.Tag, want[p.Tag], now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		r.Log().Warn("Recording what landed: %v", err)
	}
}

// verify reads again: every tag pushed must now match (updates) or hold
// nothing (removals).
func (s *Service) verify(ctx context.Context, cl *sshx.Client, n routeros.Names, plan []TagPlan) error {
	st, err := s.read(ctx, cl, n)
	if err != nil {
		return &failure{step: StepVerify, text: "Reading the router to verify failed: " + err.Error() + ".", err: err}
	}
	var bad []string
	for _, p := range plan {
		t := st.Tags[p.Tag]
		switch p.Action {
		case Update:
			if Matches(t, p.Entries, st.Names) {
				continue
			}
			have := t.Entries()
			if have == len(p.Entries) && t != nil {
				have = len(t.List)
			}
			if have != len(p.Entries) {
				bad = append(bad, fmt.Sprintf("%s: %d names on the router, %d expected", p.Tag, have, len(p.Entries)))
			} else {
				bad = append(bad, fmt.Sprintf("%s: the entries on the router differ from what was pushed", p.Tag))
			}
		case Remove:
			if !t.empty() {
				bad = append(bad, fmt.Sprintf("%s: %d names still on the router, 0 expected", p.Tag, t.Entries()))
			}
		}
	}
	if len(bad) > 0 {
		text := strings.Join(bad, "; ") + "."
		return &failure{step: StepVerify, text: text, err: fmt.Errorf("verify: %s", text)}
	}
	return nil
}
