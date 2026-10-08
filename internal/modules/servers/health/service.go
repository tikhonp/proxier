package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/servers/checkhost"
	"github.com/tikhonp/proxier/internal/modules/servers/conf"
	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/manifest"
	"github.com/tikhonp/proxier/internal/modules/servers/proxy"
	"github.com/tikhonp/proxier/internal/modules/servers/remote"
	"github.com/tikhonp/proxier/internal/modules/servers/stats"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
	"github.com/tikhonp/proxier/internal/platform/sshx"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

// Job types.
const (
	JobReference     = "servers.reference"
	JobRound         = "servers.round"
	JobSelfcheck     = "servers.selfcheck"
	JobProxytest     = "servers.proxytest"
	JobExternal      = "servers.external"
	JobExternalRound = "servers.external_round"
	JobEvaluate      = "servers.evaluate"
	JobResume        = "servers.resume"
	JobReminders     = "servers.reminders"
	JobStatsRollup   = "servers.stats_rollup"
	JobNodes         = "servers.checkhost_nodes"
)

// Setup is what a server's checks need from its template version: the stack's
// directory, the checks to run on it and the proxy test object. The module
// builds it by rendering the version in force for the server.
type Setup struct {
	Server       store.Server
	Dir          string
	Checks       []manifest.Check
	ProxyTestURL string
}

// Seams are what tests replace; the module points them at provisioning's, so
// one replacement serves every proxy test.
type Seams struct {
	ProxyTest    func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Result
	ProxyOptions func(ctx context.Context, e endpoint.Endpoint, o proxy.Options) proxy.Options
	RemoteEnv    func(log *jobs.Logger) remote.Env
}

// Deps is what the service needs of the platform and the module.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Vault    *vault.Vault
	Jobs     *jobs.System
	SSH      *sshx.SSH
	Settings *settings.Store
	I18n     *i18n.Catalog
	Stats    *stats.Service
	Log      *slog.Logger
	// Setup renders the checks of a server; Seams are read at use.
	Setup func(ctx context.Context, serverID int64) (Setup, error)
	Seams func() Seams
}

// Service runs the checks, decides the verdict and applies it.
type Service struct {
	Deps
	Now func() time.Time
	// ProxyDelay offsets the proxy test of a round from its self-check (150 s).
	ProxyDelay time.Duration
	// EvalDelay is how long a waiting evaluation sleeps before it looks again (2 min).
	EvalDelay time.Duration
	// CheckHost returns the external checker's client; nil means the public one.
	CheckHost func() *checkhost.Client
	// HTTP is the client of the reference check.
	HTTP *http.Client
}

// New returns the service.
func New(d Deps) *Service {
	return &Service{Deps: d, Now: time.Now, ProxyDelay: 150 * time.Second, EvalDelay: WaitForExternal}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) seams() Seams {
	var sm Seams
	if s.Seams != nil {
		sm = s.Seams()
	}
	if sm.ProxyTest == nil {
		sm.ProxyTest = proxy.Test
	}
	return sm
}

func (s *Service) checkhostClient() *checkhost.Client {
	if s.CheckHost != nil {
		return s.CheckHost()
	}
	return &checkhost.Client{}
}

func serverKey(id int64) string { return "server:" + strconv.FormatInt(id, 10) }

// --- settings

// Config is the settings the checks read.
type Config struct {
	Thresholds      Thresholds
	Remote          remote.Thresholds
	SelfEvery       time.Duration
	ExternalEvery   time.Duration
	OnDemandAfter   time.Duration
	HourlyCap       int
	ReminderEvery   time.Duration
	NodesRU, Abroad []string
}

// Config reads the settings.
func (s *Service) Config(ctx context.Context) (Config, error) {
	var c Config
	var err error
	d := func(key string) time.Duration {
		if err != nil {
			return 0
		}
		var v time.Duration
		v, err = s.Settings.GetDuration(ctx, key)
		return v
	}
	n := func(key string) int {
		if err != nil {
			return 0
		}
		var v int64
		v, err = s.Settings.GetInt(ctx, key)
		return int(v)
	}
	c.Thresholds = Thresholds{SlowFirstByte: d(conf.SlowFirstByte), SlowKbps: n(conf.SlowKbps), Confirmations: n(conf.FlapConfirmations)}
	c.Remote = remote.Thresholds{CertWarnDays: n(conf.CertWarnDays), DiskWarnPct: float64(n(conf.DiskWarnPct)), DiskFailPct: float64(n(conf.DiskFailPct)), Now: s.Now}
	c.SelfEvery, c.ExternalEvery, c.OnDemandAfter = d(conf.SelfcheckEvery), d(conf.ExternalEvery), d(conf.ExternalOnDemandAfter)
	c.HourlyCap, c.ReminderEvery = n(conf.ExternalHourlyCap), d(conf.ReminderEvery)
	if err != nil {
		return c, err
	}
	c.NodesRU, err = s.nodeList(ctx, conf.ExternalNodesRU)
	if err != nil {
		return c, err
	}
	c.Abroad, err = s.nodeList(ctx, conf.ExternalNodesAbroad)
	return c, err
}

// --- the state the verdict looks at

type selfDetail struct {
	Checks []struct {
		Kind    string `json:"kind"`
		Pass    bool   `json:"pass"`
		Message string `json:"message"`
	} `json:"checks"`
	Error string `json:"error"`
}

type nodeDetail struct {
	Region  string `json:"region"`
	Country string `json:"country"`
	MS      int    `json:"ms"`
	Error   string `json:"error"`
}

type proxyDetail struct {
	FirstByteMS int    `json:"first_byte_ms"`
	Kbps        int    `json:"kbps"`
	Error       string `json:"error"`
}

// Load reads the check state of a server at now: per check the newest result
// no older than two of its intervals, and not inconclusive.
func (s *Service) Load(ctx context.Context, id int64, now time.Time) (CheckState, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return CheckState{}, err
	}
	h, err := store.GetHealth(ctx, s.DB.R, id)
	if err != nil {
		return CheckState{}, err
	}
	var in CheckState
	in.Home, _, err = store.GetHome(ctx, s.DB.R)
	if err != nil {
		return in, err
	}
	in.Paused = !h.PausedUntil.IsZero()
	if in.Paused && h.PausedUntil.Before(store.PausedForever.Time) {
		in.PausedUntil = h.PausedUntil.Time
	}
	in.AwaitingExternal = awaitingOf(h.Detail)

	rows, err := store.Endpoints(ctx, s.DB.R, id)
	if err != nil {
		return in, err
	}
	for _, r := range rows {
		in.Endpoints = append(in.Endpoints, r.Key)
	}

	fresh := func(at db.Time, interval time.Duration) bool { return !at.Before(now.Add(-2 * interval)) }

	self, err := store.LatestResults(ctx, s.DB.R, id, "self")
	if err != nil {
		return in, err
	}
	for _, r := range self {
		if r.Inconclusive || r.Class == "skipped" || !fresh(r.At, cfg.SelfEvery) {
			continue
		}
		sr := SelfResult{At: r.At.Time, Class: r.Class}
		var d selfDetail
		_ = json.Unmarshal([]byte(r.Detail), &d)
		sr.Error = d.Error
		for _, c := range d.Checks {
			if !c.Pass {
				sr.Failures = append(sr.Failures, Failure{Kind: c.Kind, Message: c.Message})
			}
		}
		in.Self = &sr
	}

	prox, err := store.LatestResults(ctx, s.DB.R, id, "proxy")
	if err != nil {
		return in, err
	}
	in.Proxy = map[string]ProxyResult{}
	for _, r := range prox {
		if r.Inconclusive || !fresh(r.At, cfg.SelfEvery) {
			continue
		}
		var d proxyDetail
		_ = json.Unmarshal([]byte(r.Detail), &d)
		in.Proxy[r.EndpointKey] = ProxyResult{At: r.At.Time, OK: r.OK, Class: r.Class, Error: d.Error, FirstByteMS: d.FirstByteMS, Kbps: d.Kbps}
	}

	ext, err := store.LatestResults(ctx, s.DB.R, id, "external")
	if err != nil {
		return in, err
	}
	var newest db.Time
	for _, r := range ext {
		if r.At.After(newest.Time) {
			newest = r.At
		}
	}
	if !newest.IsZero() && fresh(newest, cfg.ExternalEvery) {
		er := ExternalResult{At: newest.Time}
		usable := false
		for _, r := range ext {
			if !r.At.Equal(newest.Time) || r.Inconclusive || r.Class == "skipped" || r.Class == "unavailable" {
				continue
			}
			var d nodeDetail
			_ = json.Unmarshal([]byte(r.Detail), &d)
			er.Nodes = append(er.Nodes, NodeAnswer{Node: r.Vantage, Country: d.Country, Abroad: d.Region == "abroad", OK: r.OK, MS: d.MS, Error: d.Error})
			usable = true
		}
		if usable {
			in.External = &er
		}
	}
	return in, nil
}

// awaitingOf reads the time an on-demand external check was asked for.
func awaitingOf(detail string) time.Time {
	var m struct {
		Awaiting string `json:"awaiting_external"`
	}
	_ = json.Unmarshal([]byte(detail), &m)
	if m.Awaiting == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, m.Awaiting)
	return t
}

// Evaluate looks at a server's latest results and applies the verdict. A
// verdict that needs the external check on its way waits: it stores nothing and
// asks for another look after EvalDelay.
func (s *Service) Evaluate(ctx context.Context, id int64, actor string) error {
	h, err := store.GetHealth(ctx, s.DB.R, id)
	if err != nil {
		return err
	}
	if h.State != "active" || h.Retiring {
		return nil
	}
	cfg, err := s.Config(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	in, err := s.Load(ctx, id, now)
	if err != nil {
		return err
	}
	o := Evaluate(in, cfg.Thresholds, now)
	if o.Wait {
		// Ask again later, unless the answer arrived while this verdict was
		// made: the check that answers clears the mark in its own transaction,
		// so looking again inside ours cannot leave a stray job behind.
		waiting := false
		err := s.DB.Write(ctx, func(tx *sqlx.Tx) error {
			cur, err := store.GetHealth(ctx, tx, id)
			if err != nil || ParseDetail(cur.Detail).Awaiting == "" {
				return err
			}
			waiting = true
			_, err = s.Jobs.Enqueue(ctx, tx, jobs.Request{
				Type: JobEvaluate, Subject: store.ServerSubject(id), CoalescingKey: "evaluate:" + strconv.FormatInt(id, 10),
				Delay: s.EvalDelay, CreatedBy: actor, Payload: map[string]any{"server_id": id},
			})
			return err
		})
		if err != nil {
			return err
		}
		if waiting {
			s.Jobs.Kick()
			return nil
		}
		return s.Evaluate(ctx, id, actor)
	}
	return s.DB.Write(ctx, func(tx *sqlx.Tx) error {
		_, err := s.Apply(ctx, tx, id, o, actor)
		return err
	})
}
