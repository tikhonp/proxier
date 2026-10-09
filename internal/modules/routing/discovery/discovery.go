// Package discovery finds the domains a website uses so they can be routed
// (docs/processes/routing/domain-discovery.md): a run looks the site up in
// the catalog, visits it in a fresh headless Chromium context (directly or
// through a server), classifies every hostname it loaded, and offers the
// picks as a custom service. Nothing changes until the admin saves.
package discovery

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/lists"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// Visit is one visit path's work for the browser.
type Visit struct {
	URL         string
	Proxy       string // "" direct; "socks5://10.89.251.2:41234"
	Path        string // "direct" or the server's name, for logs
	Links       int    // 0 or 5
	Site        string // the registrable domain links must share
	MaxHosts    int    // what is left of the run's 300
	PageTimeout time.Duration
}

// Page is one page a visit loaded, or tried to.
type Page struct {
	URL        string
	Loaded     bool
	Status     int
	Error      string
	Screenshot []byte // JPEG
}

// Request is one request a page made.
type Request struct {
	URL, Host, Type string
	Status          int
	Failed          string // "" or the failure text
	Page            int    // index in Result.Pages
}

// Result is what a visit saw.
type Result struct {
	Title    string
	Pages    []Page
	Requests []Request
	Capped   bool
}

// Browser visits sites. Every Visit is one fresh browser context.
type Browser interface {
	Visit(ctx context.Context, v Visit) (Result, error)
	Version(ctx context.Context) (string, error)
}

// Limits of a run.
const (
	MaxHosts    = 300
	PageTimeout = 90 * time.Second
	Links       = 5
	// Retention is how long runs and their screenshots are kept.
	Retention = store.DiscoveryRetention
)

// Ways to visit.
const (
	ViaDirect = "direct"
	ViaServer = "server"
	ViaAuto   = "auto"
)

// Run states.
const (
	Queued  = "queued"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

// Start is the Discover form.
type Start struct {
	Website  string
	Via      string // direct, server, auto
	ServerID int64
	Depth    int // 0 or 5
}

// VisitRecord is what a run keeps of one visit.
type VisitRecord struct {
	Path        string   `json:"path"` // "direct" or the server's name
	Loaded      bool     `json:"loaded"`
	Pages       int      `json:"pages"`
	Requests    int      `json:"requests"`
	Hosts       int      `json:"hosts"`
	Failed      int      `json:"failed"` // hosts with a failure
	Error       string   `json:"error,omitempty"`
	Screenshots []string `json:"screenshots"`
}

// Host is a hostname (or an IP literal) a run saw.
type Host struct {
	Host, Registrable string
	Class             Class
	Requests          int
	Failed            string // the first failure of the direct visit
	FailedRequests    int    // how many of its direct requests failed
	Seen              []string
	CoveredBy         []string // "anthropic in Main", computed when read
}

// Group is a registrable domain's hosts, as the pick table shows them.
type Group struct {
	Registrable string
	Class       Class // its first host's
	Hosts       []Host
	Requests    int
	Failed      int // hosts that failed directly
	CoveredBy   []string
	Ticked      bool // by default: first-party
}

// Run is a discovery run.
type Run struct {
	ID          int64
	Input, URL  string
	Host        string
	Registrable string
	Via         string
	ServerID    int64
	ServerName  string
	Depth       int
	State       string
	JobID       int64
	Title       string
	Suggestions []catalog.Suggestion
	Visits      []VisitRecord
	Hosts       []Host
	Groups      []Group // the hosts that are names, by registrable domain: first-party, failed, then by requests
	IPs         []Host  // IP literals
	Capped      bool
	Error       string
	CreatedAt   time.Time
	FinishedAt  time.Time
}

// Active reports whether the run is still queued or running.
func (r Run) Active() bool { return r.State == Queued || r.State == Running }

// Pick is what the admin ticked.
type Pick struct {
	Suffix []string // registrable domains of ticked groups
	Exact  []string // hosts ticked alone
}

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Jobs     *jobs.System
	Catalog  *catalog.Service
	Services *services.Service
	Lists    *lists.Service
	Browser  Browser                 // nil: lookup only
	Servers  servers.EndpointCatalog // nil: Direct only
	Dialer   servers.ProxyDialer     // nil: Direct only
	// Peer is the Chromium host's address ("chromium:9222"): the SOCKS
	// listener of a visit through a server routes to it and accepts only it.
	Peer    string
	DataDir string
	Now     func() time.Time
	Log     *slog.Logger
}

// Service runs discovery.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d}
}

// Errors.
var (
	ErrNotFound  = errors.New("discovery: no such run")
	ErrNoPicks   = errors.New("discovery: nothing ticked")
	ErrNotDone   = errors.New("discovery: the run hasn't finished")
	ErrNotCustom = errors.New("discovery: not a custom service")
)

// Subject is a run's event subject.
func Subject(id int64) events.Subject {
	return events.Subject{Type: "discovery", ID: itoa(id)}
}

// CanVisit reports whether runs visit the site (Chromium is configured).
func (s *Service) CanVisit() bool { return s.d.Browser != nil }

// ThroughServers reports whether a run can go through a server.
func (s *Service) ThroughServers() bool { return s.d.Servers != nil && s.d.Dialer != nil }

// Servers lists the active servers by name, for the form (the healthy ones
// can be chosen). Nil without the ports.
func (s *Service) Servers(ctx context.Context) ([]servers.ServerEndpoints, error) {
	if !s.ThroughServers() {
		return nil, nil
	}
	list, err := s.d.Servers.Active(ctx)
	if err != nil {
		return nil, err
	}
	sortByName(list)
	return list, nil
}

// Version asks the browser its version (3 s at most); "" without one.
func (s *Service) Version(ctx context.Context) (string, error) {
	if s.d.Browser == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.d.Browser.Version(ctx)
}

// Chromium's states, as the Integrations row and the Discover page say them.
const (
	ChromiumConnected     = "connected"
	ChromiumNotConfigured = "not_configured"
	ChromiumUnreachable   = "unreachable"
)

// Chromium is the browser's state and its detail: the version when
// connected, the error when unreachable.
func (s *Service) Chromium(ctx context.Context) (state, detail string) {
	if s.d.Browser == nil {
		return ChromiumNotConfigured, ""
	}
	v, err := s.Version(ctx)
	if err != nil {
		return ChromiumUnreachable, err.Error()
	}
	return ChromiumConnected, v
}
