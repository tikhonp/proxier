package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/discovery/socks"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

// path is one way a run visits the site.
type path struct {
	name     string // "direct" or the server's name
	serverID int64  // 0 direct
}

var direct = path{name: ViaDirect}

// stepVisit visits the site on the run's paths, saving what each visit saw
// as it ends. Without a browser it only says so. A visit that can't run
// fails the job; a page that never loads doesn't.
func (s *Service) stepVisit(ctx context.Context, r *jobs.Run) error {
	run, err := s.load(ctx, r)
	if err != nil {
		return err
	}
	log := r.Log()
	if s.d.Browser == nil {
		log.Info("Chromium is not configured (PROXIER_CHROMIUM_URL): the catalog lookup only")
		return nil
	}
	// a resumed step starts over
	if err := os.RemoveAll(s.runDir(run.ID)); err != nil {
		return err
	}
	m := &merge{run: run, hosts: map[string]*Host{}}
	switch run.Via {
	case ViaServer:
		_, err = s.visit(ctx, r, m, path{name: run.ServerName, serverID: run.ServerID})
	case ViaAuto:
		var res Result
		if res, err = s.visit(ctx, r, m, direct); err == nil && failedSomewhere(res) {
			p, ok, perr := s.firstHealthy(ctx)
			switch {
			case perr != nil:
				err = perr
			case !ok:
				log.Warn("the direct visit failed, and no server is healthy to repeat it through")
			default:
				log.Info("the direct visit failed: repeating it through %s", p.name)
				_, err = s.visit(ctx, r, m, p)
			}
		}
	default:
		_, err = s.visit(ctx, r, m, direct)
	}
	return err
}

// failedSomewhere: the first page didn't load, or any request failed.
func failedSomewhere(res Result) bool {
	if len(res.Pages) == 0 || !res.Pages[0].Loaded {
		return true
	}
	return slices.ContainsFunc(res.Requests, func(q Request) bool { return q.Failed != "" })
}

// firstHealthy is the first healthy active server by name.
func (s *Service) firstHealthy(ctx context.Context) (path, bool, error) {
	list, err := s.Servers(ctx)
	if err != nil {
		return path{}, false, err
	}
	for _, se := range list {
		if se.Health == "healthy" {
			return path{name: se.Name, serverID: se.ServerID}, true, nil
		}
	}
	return path{}, false, nil
}

// visit runs one visit, through a fresh SOCKS listener on the server's
// dialer when it has one, writes its screenshots and saves the run.
func (s *Service) visit(ctx context.Context, r *jobs.Run, m *merge, p path) (Result, error) {
	v := Visit{URL: m.run.URL, Path: p.name, Links: m.run.Depth, Site: m.run.Registrable, MaxHosts: max(MaxHosts-len(m.order), 0),
		PageTimeout: PageTimeout}
	if p.serverID != 0 {
		if !s.ThroughServers() {
			return Result{}, jobs.Permanent(fmt.Errorf("visit through %s: servers aren't available", p.name))
		}
		dial, closer, err := s.d.Dialer.Dial(ctx, p.serverID)
		if err != nil {
			return Result{}, fmt.Errorf("visit through %s: %w", p.name, err)
		}
		defer func() { _ = closer.Close() }()
		ln, err := socks.Listen(ctx, s.d.Peer, dial)
		if err != nil {
			return Result{}, fmt.Errorf("visit through %s: %w", p.name, err)
		}
		defer func() { _ = ln.Close() }()
		v.Proxy = ln.URL()
	}
	r.Log().Info("visiting %s (%s)", v.URL, p.name)
	res, err := s.d.Browser.Visit(ctx, v)
	if err != nil {
		return res, fmt.Errorf("the browser: %w", err)
	}
	rec := VisitRecord{Path: p.name, Pages: len(res.Pages), Requests: len(res.Requests), Screenshots: []string{}}
	if len(res.Pages) > 0 && res.Pages[0].Loaded {
		rec.Loaded = true
	} else if len(res.Pages) > 0 && res.Pages[0].Error != "" {
		rec.Error = res.Pages[0].Error
	} else if !rec.Loaded {
		rec.Error = "no page"
	}
	n := len(m.visits) + 1
	for i, pg := range res.Pages {
		if len(pg.Screenshot) == 0 {
			continue
		}
		name := fmt.Sprintf("%d-%d.jpg", n, i+1)
		if err := s.writeShot(m.run.ID, name, pg.Screenshot); err != nil {
			return res, err
		}
		rec.Screenshots = append(rec.Screenshots, name)
	}
	rec.Hosts, rec.Failed = m.add(p, res)
	if m.title == "" || rec.Loaded && !m.titleLoaded {
		if res.Title != "" {
			m.title, m.titleLoaded = res.Title, rec.Loaded
		}
	}
	m.visits = append(m.visits, rec)
	m.capped = m.capped || res.Capped
	r.Log().Info("%s: %d pages, %d requests, %d hosts, %d failed", p.name, rec.Pages, rec.Requests, rec.Hosts, rec.Failed)
	return res, s.save(ctx, m)
}

func (s *Service) runDir(id int64) string {
	return filepath.Join(s.d.DataDir, "discovery", itoa(id))
}

func (s *Service) writeShot(id int64, name string, jpeg []byte) error {
	dir := s.runDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), jpeg, 0o600)
}

func (s *Service) save(ctx context.Context, m *merge) error {
	visits, err := json.Marshal(m.visits)
	if err != nil {
		return err
	}
	rows := make([]store.DiscoveredHost, 0, len(m.order))
	for _, h := range m.order {
		rows = append(rows, storedHost(m.run.ID, *m.hosts[h]))
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		return store.SaveRunVisits(ctx, tx, m.run.ID, m.title, string(visits), m.capped, rows)
	})
}

// merge gathers a run's hosts across its visits.
type merge struct {
	run         Run
	hosts       map[string]*Host
	order       []string
	visits      []VisitRecord
	title       string
	titleLoaded bool
	capped      bool
}

// add counts a visit's requests per hostname, at most MaxHosts in the run,
// and returns how many hosts the visit saw and how many of them failed.
// Only a direct visit's failures are kept on the hosts.
func (m *merge) add(p path, res Result) (hosts, failed int) {
	seen := map[string]bool{}
	bad := map[string]bool{}
	for _, q := range res.Requests {
		host := hostname(q)
		if host == "" {
			continue
		}
		h := m.hosts[host]
		if h == nil {
			if len(m.order) >= MaxHosts {
				m.capped = true
				continue
			}
			h = &Host{Host: host, Class: Classify(host, m.run.Registrable)}
			if h.Class != IP {
				if reg, err := domain.Registrable(host); err == nil {
					h.Registrable = reg
				} else {
					h.Registrable = host
				}
			}
			m.hosts[host] = h
			m.order = append(m.order, host)
		}
		h.Requests++
		if !slices.Contains(h.Seen, p.name) {
			h.Seen = append(h.Seen, p.name)
		}
		seen[host] = true
		if q.Failed == "" {
			continue
		}
		bad[host] = true
		if p.serverID == 0 {
			if h.Failed == "" {
				h.Failed = q.Failed
			}
			h.FailedRequests++
		}
	}
	return len(seen), len(bad)
}

// hostname is a request's host, lowercase, an IPv6 literal without brackets.
func hostname(q Request) string {
	h := q.Host
	if h == "" {
		u, err := url.Parse(q.URL)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss" {
			return ""
		}
		h = u.Hostname()
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().String()
	}
	return h
}
