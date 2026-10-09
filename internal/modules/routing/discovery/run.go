package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/catalog"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/modules/servers"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/jobs"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func sortByName(list []servers.ServerEndpoints) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}

// Start checks the form, stores the run and queues its job (queue
// discovery, one run at a time). Errors are store.FieldErrors: website,
// via, server, depth.
func (s *Service) Start(ctx context.Context, st Start, actor string) (int64, error) {
	fe := store.FieldErrors{}
	t, err := Normalise(st.Website)
	if err != nil {
		fe["website"] = "discovery.err.website"
	}
	row := store.DiscoveryRun{Input: strings.TrimSpace(st.Website), URL: t.URL, Host: t.Host, Registrable: t.Registrable, Via: st.Via,
		Depth: st.Depth, CreatedAt: db.At(s.d.Now())}
	switch {
	case st.Via == ViaDirect:
	case (st.Via == ViaAuto || st.Via == ViaServer) && s.ThroughServers():
	default:
		fe["via"] = "discovery.err.via"
	}
	if st.Via == ViaServer && fe["via"] == "" {
		se, ok, err := s.d.Servers.Server(ctx, st.ServerID)
		switch {
		case err != nil:
			return 0, err
		case !ok:
			fe["server"] = "discovery.err.server"
		case se.Health != "healthy":
			fe["server"] = "discovery.err.server_health"
		default:
			row.ServerID, row.ServerName = sql.NullInt64{Int64: se.ServerID, Valid: true}, se.Name
		}
	}
	if st.Depth != 0 && st.Depth != Links {
		fe["depth"] = "discovery.err.depth"
	}
	if len(fe) > 0 {
		return 0, fe
	}
	var id int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		if id, err = store.InsertRun(ctx, tx, row); err != nil {
			return err
		}
		e, err := s.d.Jobs.Enqueue(ctx, tx, jobs.Request{Type: JobDiscover, Payload: payload{RunID: id}, Subject: Subject(id), CreatedBy: actor})
		if err != nil {
			return err
		}
		return store.SetRunJob(ctx, tx, id, e.ID)
	})
	return id, err
}

// Again starts a new run of the same website and depth through another path.
func (s *Service) Again(ctx context.Context, id int64, via string, serverID int64, actor string) (int64, error) {
	r, err := store.GetRun(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return s.Start(ctx, Start{Website: r.Input, Via: via, ServerID: serverID, Depth: r.Depth}, actor)
}

func fromRow(r store.DiscoveryRun) (Run, error) {
	run := Run{
		ID: r.ID, Input: r.Input, URL: r.URL, Host: r.Host, Registrable: r.Registrable, Via: r.Via, ServerID: r.ServerID.Int64,
		ServerName: r.ServerName, Depth: r.Depth, State: r.State, JobID: r.JobID.Int64, Title: r.Title, Capped: r.Capped,
		Error: r.Error, CreatedAt: r.CreatedAt.Time, FinishedAt: r.FinishedAt.Time,
	}
	if err := json.Unmarshal([]byte(r.Suggestions), &run.Suggestions); err != nil {
		return run, err
	}
	return run, json.Unmarshal([]byte(r.Visits), &run.Visits)
}

// Recent reads the newest runs, without their hosts.
func (s *Service) Recent(ctx context.Context, limit int) ([]Run, error) {
	rows, err := store.RecentRuns(ctx, s.d.DB.R, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		run, err := fromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// Get reads a run with its hosts, grouped by registrable domain, and what
// already covers them in every routing list.
func (s *Service) Get(ctx context.Context, id int64) (Run, error) {
	r, err := store.GetRun(ctx, s.d.DB.R, id)
	if errors.Is(err, store.ErrNotFound) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	run, err := fromRow(r)
	if err != nil {
		return run, err
	}
	rows, err := store.RunHosts(ctx, s.d.DB.R, id)
	if err != nil {
		return run, err
	}
	for _, h := range rows {
		run.Hosts = append(run.Hosts, hostOf(h))
	}
	group(&run)
	return run, s.covered(ctx, &run)
}

func hostOf(h store.DiscoveredHost) Host {
	out := Host{Host: h.Host, Registrable: h.Registrable, Class: Class(h.Class), Requests: h.Requests}
	if n, reason, ok := strings.Cut(h.Failed, " "); ok {
		out.FailedRequests, _ = strconv.Atoi(n)
		out.Failed = reason
	}
	if h.Seen != "" {
		out.Seen = strings.Split(h.Seen, ", ")
	}
	return out
}

func storedHost(runID int64, h Host) store.DiscoveredHost {
	out := store.DiscoveredHost{RunID: runID, Host: h.Host, Registrable: h.Registrable, Class: string(h.Class), Requests: h.Requests,
		Seen: strings.Join(h.Seen, ", ")}
	if h.Failed != "" {
		out.Failed = strconv.Itoa(h.FailedRequests) + " " + h.Failed
	}
	return out
}

// covered fills the hosts' and groups' "covered by" over every routing list:
// for a host, an exact entry of it or a suffix of it or above; for a group,
// a suffix of its registrable domain or above.
func (s *Service) covered(ctx context.Context, run *Run) error {
	if len(run.Hosts) == 0 {
		return nil
	}
	all, err := s.d.Lists.All(ctx)
	if err != nil {
		return err
	}
	add := func(to *[]string, name string, exact bool, members []own.Member, list string) {
		for _, c := range own.CoveredBy(name, exact, members, 0) {
			if w := c.Tag + " in " + list; !slices.Contains(*to, w) {
				*to = append(*to, w)
			}
		}
	}
	for _, l := range all {
		view, err := s.d.Lists.View(ctx, l.ID, nil)
		if err != nil {
			return err
		}
		members := make([]own.Member, 0, len(view.Members))
		for _, m := range view.Members {
			members = append(members, own.Member{ServiceID: m.Service.ID, Tag: m.Service.Tag, Set: m.Snapshot.Set})
		}
		for i := range run.Hosts {
			if h := &run.Hosts[i]; h.Class != IP {
				add(&h.CoveredBy, h.Host, true, members, l.Name)
			}
		}
		for i := range run.Groups {
			g := &run.Groups[i]
			add(&g.CoveredBy, g.Registrable, false, members, l.Name)
			for j := range g.Hosts {
				add(&g.Hosts[j].CoveredBy, g.Hosts[j].Host, true, members, l.Name)
			}
		}
	}
	return nil
}

// Tickable reports whether a host can be picked: a name a list can hold.
func (h Host) Tickable() bool { return h.Class != IP && domain.Valid(h.Host) }

// Ticked is a host's default when its group isn't ticked: it failed directly.
func (h Host) Ticked() bool { return h.Failed != "" && h.Tickable() }

// group sorts the hosts into groups (first-party, then those with direct
// failures, then by requests) and IP literals.
func group(run *Run) {
	byReg := map[string]*Group{}
	var order []string
	for _, h := range run.Hosts {
		if h.Class == IP || !domain.Valid(h.Registrable) {
			if h.Class == IP {
				run.IPs = append(run.IPs, h)
			}
			continue
		}
		g := byReg[h.Registrable]
		if g == nil {
			g = &Group{Registrable: h.Registrable, Class: h.Class}
			byReg[h.Registrable] = g
			order = append(order, h.Registrable)
		}
		g.Hosts = append(g.Hosts, h)
		g.Requests += h.Requests
		if h.Failed != "" {
			g.Failed++
		}
	}
	for _, reg := range order {
		g := byReg[reg]
		g.Ticked = g.Class == FirstParty
		run.Groups = append(run.Groups, *g)
	}
	sort.SliceStable(run.Groups, func(i, j int) bool {
		a, b := run.Groups[i], run.Groups[j]
		if (a.Class == FirstParty) != (b.Class == FirstParty) {
			return a.Class == FirstParty
		}
		if (a.Failed > 0) != (b.Failed > 0) {
			return a.Failed > 0
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.Registrable < b.Registrable
	})
}

// suggestionsJSON encodes the catalog's suggestions for the run row.
func suggestionsJSON(list []catalog.Suggestion) (string, error) {
	if list == nil {
		list = []catalog.Suggestion{}
	}
	b, err := json.Marshal(list)
	return string(b), err
}

func dbAt(t time.Time) db.Time { return db.At(t) }

func parseID(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
