package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/sealed"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/sshx"
)

func (s *Service) target(srv store.Server) sshx.Target {
	return sshx.Target{
		Hop: sshx.Hop{
			Address: net.JoinHostPort(srv.IP, strconv.Itoa(srv.SSHPort)), User: remote.DeployUser,
			Subject: serverKey(srv.ID),
		},
		FirstContact: sshx.PinOnFirstContact,
	}
}

func (s *Service) env(log *jobs.Logger) remote.Env {
	if f := s.seams().RemoteEnv; f != nil {
		return f(log)
	}
	return remote.Env{Log: log}
}

// homeOnline says whether results stored now are conclusive.
func (s *Service) homeOnline(ctx context.Context) (bool, error) {
	state, _, err := store.GetHome(ctx, s.DB.R)
	return state == store.HomeOnline, err
}

type selfCheckItem struct {
	Kind    string `json:"kind"`
	Pass    bool   `json:"pass"`
	Message string `json:"message"`
}

// selfcheckStep runs the template's checks and reads the stats over one SSH
// connection, then stores both in one transaction.
func (s *Service) selfcheckStep(ctx context.Context, r *jobs.Run) error {
	p, err := s.payloadOf(r)
	if err != nil {
		return err
	}
	setup, err := s.Setup(ctx, p.ServerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	srv := setup.Server
	if srv.State != "active" {
		r.Log().Info("%s is %s: nothing to check", srv.Name, srv.State)
		return nil
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	env := s.env(r.Log())

	res := store.CheckResult{ServerID: srv.ID, Kind: "self", Vantage: "home"}
	detail := map[string]any{}
	var results []remote.CheckResult
	var reading *remote.StatsReading

	conn, cerr := s.SSH.Connect(ctx, s.target(srv), r.Log())
	var changed *sshx.HostKeyChangedError
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(cerr, &changed):
		res.Class, detail["error"] = SelfHostKey, cerr.Error()
	case cerr != nil:
		res.Class, detail["error"] = SelfUnreachable, cerr.Error()
	default:
		func() {
			defer func() { _ = conn.Close() }()
			var rerr error
			results, rerr = remote.SelfCheck(ctx, env, conn, setup.Dir, setup.Checks, cfg.Remote)
			if rerr != nil {
				res.Class, detail["error"] = SelfUnreachable, rerr.Error()
				results = nil
				return
			}
			res.Class, res.OK = SelfOK, true
			for _, c := range results {
				if !c.Pass {
					res.Class, res.OK = SelfStackFailed, false
				}
			}
			if st, serr := remote.Stats(ctx, env, conn, setup.Dir); serr != nil {
				r.Log().Warn("Could not read the stats: %v", serr)
			} else {
				reading = &st
			}
		}()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	items := make([]selfCheckItem, 0, len(results))
	for _, c := range results {
		items = append(items, selfCheckItem{Kind: c.Kind, Pass: c.Pass, Message: c.Message})
		if c.Pass {
			r.Log().Info("Check %s passed: %s", c.Kind, c.Message)
		} else {
			r.Log().Warn("Check %s failed: %s", c.Kind, c.Message)
		}
	}
	detail["checks"] = items
	if e, ok := detail["error"]; ok {
		r.Log().Warn("Self-check %s: %v", res.Class, e)
	}
	b, _ := json.Marshal(detail)
	res.Detail = string(b)

	online, err := s.homeOnline(ctx)
	if err != nil {
		return err
	}
	res.Inconclusive = !online
	at := s.now()
	res.At = db.At(at)
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if err := store.InsertCheckResult(ctx, tx, res); err != nil {
			return err
		}
		if reading != nil {
			if err := s.Stats.Store(ctx, tx, srv.ID, at, *reading); err != nil {
				return err
			}
		}
		return s.crossings(ctx, tx, srv.ID, results, cfg, r.Info().Actor())
	})
}

// crossings raises cert_expiring and disk_low once per crossing of their
// thresholds and clears the flags when the value is back above them.
func (s *Service) crossings(ctx context.Context, tx *sqlx.Tx, id int64, results []remote.CheckResult, cfg Config, actor string) error {
	h, err := store.GetHealth(ctx, tx, id)
	if err != nil {
		return err
	}
	var cert, disk *bool
	set := func(v bool) *bool { return &v }
	for _, c := range results {
		switch {
		case c.Kind == "cert-expiry" && c.HasDaysLeft:
			if c.DaysLeft < cfg.Remote.CertWarnDays && !h.CertWarned {
				cert = set(true)
				if _, err := s.Events.Record(ctx, tx, events.Event{Type: "server.cert_expiring", Subject: store.ServerSubject(id), Actor: actor,
					Payload: map[string]any{"days_left": c.DaysLeft}}); err != nil {
					return err
				}
			} else if c.DaysLeft >= cfg.Remote.CertWarnDays && h.CertWarned {
				cert = set(false)
			}
		case c.Kind == "disk-free" && c.FreePct > 0:
			if c.FreePct < cfg.Remote.DiskWarnPct && !h.DiskWarned {
				disk = set(true)
				if _, err := s.Events.Record(ctx, tx, events.Event{Type: "server.disk_low", Subject: store.ServerSubject(id), Actor: actor,
					Payload: map[string]any{"free_pct": int(c.FreePct + 0.5)}}); err != nil {
					return err
				}
			} else if c.FreePct >= cfg.Remote.DiskWarnPct && h.DiskWarned {
				disk = set(false)
			}
		}
	}
	return store.SetWarned(ctx, tx, id, cert, disk)
}

type proxyRow struct {
	URL         string `json:"url"`
	ConnectMS   int    `json:"connect_ms"`
	TLSMS       int    `json:"tls_ms"`
	FirstByteMS int    `json:"first_byte_ms"`
	Kbps        int    `json:"kbps"`
	Bytes       int64  `json:"bytes"`
	Error       string `json:"error,omitempty"`
	Small       bool   `json:"small_object,omitempty"`
}

// proxytestStep tests every endpoint once, as a client does. It asks Busy first:
// a redeploy in progress must not count against health, so nothing is stored.
func (s *Service) proxytestStep(ctx context.Context, r *jobs.Run) error {
	p, err := s.payloadOf(r)
	if err != nil {
		return err
	}
	busy, err := s.Jobs.BusyExcept(ctx, serverKey(p.ServerID), JobSelfcheck)
	if err != nil {
		return err
	}
	if busy {
		p.Skipped = "a job is running on the server"
		r.Log().Info("Skipped: %s", p.Skipped)
		return r.SavePayload(ctx, p)
	}
	srv, err := store.GetServer(ctx, s.DB.R, p.ServerID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if srv.State != "active" {
		p.Skipped = "the server is " + srv.State
		return r.SavePayload(ctx, p)
	}
	setup, err := s.Setup(ctx, p.ServerID)
	if err != nil {
		return err
	}
	rows, err := store.Endpoints(ctx, s.DB.R, p.ServerID)
	if err != nil {
		return err
	}
	eps, err := sealed.OpenEndpoints(s.Vault, rows)
	if err != nil {
		return err
	}
	url, timeout, stall, err := conf.ProxyTest(ctx, s.Settings, setup.ProxyTestURL)
	if err != nil {
		return err
	}
	base := proxy.Options{URL: url, Timeout: timeout, Stall: stall}
	sm := s.seams()

	type outcome struct {
		e   endpoint.Endpoint
		res proxy.Result
	}
	var outs []outcome
	for _, e := range eps {
		opts := base
		if sm.ProxyOptions != nil {
			opts = sm.ProxyOptions(ctx, e, opts)
		}
		res := sm.ProxyTest(ctx, e, opts)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if res.OK {
			r.Log().Info("%s passed: first byte after %d ms, %d kbit/s", e.Key, res.FirstByteMS, res.ThroughputKbps)
		} else {
			r.Log().Warn("%s failed (%s): %s", e.Key, res.Class, res.Error)
		}
		outs = append(outs, outcome{e, res})
	}

	online, err := s.homeOnline(ctx)
	if err != nil {
		return err
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	anyFailed := false
	for _, o := range outs {
		anyFailed = anyFailed || !o.res.OK
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, o := range outs {
			b, _ := json.Marshal(proxyRow{URL: o.res.URL, ConnectMS: o.res.ConnectMS, TLSMS: o.res.TLSMS, FirstByteMS: o.res.FirstByteMS,
				Kbps: o.res.ThroughputKbps, Bytes: o.res.Bytes, Error: o.res.Error, Small: o.res.SmallObject})
			if err := store.InsertCheckResult(ctx, tx, store.CheckResult{
				ServerID: p.ServerID, Kind: "proxy", EndpointKey: o.e.Key, Vantage: "home", At: db.At(now),
				OK: o.res.OK, Class: string(o.res.Class), Inconclusive: !online, Detail: string(b),
			}); err != nil {
				return err
			}
		}
		if !anyFailed || !online {
			return nil
		}
		// A failure: ask the outside for its opinion unless it gave one lately.
		last, ok, err := store.LastExternal(ctx, tx, p.ServerID)
		if err != nil {
			return err
		}
		if ok && now.Sub(last.Time) < cfg.OnDemandAfter {
			return nil
		}
		if _, err := s.enqueueExternal(ctx, tx, p.ServerID, "on_demand", 0, r.Info().Actor()); err != nil {
			return err
		}
		return s.setAwaiting(ctx, tx, p.ServerID, now)
	})
}

// enqueueExternal queues an external check of a server in tx.
func (s *Service) enqueueExternal(ctx context.Context, tx *sqlx.Tx, id int64, kind string, delay time.Duration, actor string) (jobs.Enqueued, error) {
	return s.Jobs.Enqueue(ctx, tx, jobs.Request{
		Type: JobExternal, Subject: store.ServerSubject(id), CoalescingKey: "external:" + strconv.FormatInt(id, 10),
		Delay: delay, Payload: payload{ServerID: id, Kind: kind}, CreatedBy: actor,
	})
}

// MarkAwaiting is setAwaiting for callers outside the package's jobs (tests).
func (s *Service) MarkAwaiting(ctx context.Context, tx *sqlx.Tx, id int64, at time.Time) error {
	return s.setAwaiting(ctx, tx, id, at)
}

// setAwaiting marks (or, with a zero time, clears) the on-demand external
// check the next evaluation waits for. It lives in the detail of the latest
// evaluation, which every evaluation keeps it in.
func (s *Service) setAwaiting(ctx context.Context, tx *sqlx.Tx, id int64, at time.Time) error {
	h, err := store.GetHealth(ctx, tx, id)
	if err != nil {
		return err
	}
	d := ParseDetail(h.Detail)
	d.Awaiting = ""
	if !at.IsZero() {
		d.Awaiting = at.UTC().Format(time.RFC3339Nano)
	}
	return store.SetDetail(ctx, tx, id, d.String())
}
