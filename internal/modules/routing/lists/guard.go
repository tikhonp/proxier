package lists

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/domain"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
	"github.com/tikhonp/proxier/internal/modules/servers"
)

// guard is the first name of the set (suffix names first, each sorted) that
// covers a server's hostname, as a GuardError the caller completes.
func guard(suffix, exact []string, srv []own.Server) *GuardError {
	if len(srv) == 0 {
		return nil
	}
	for _, n := range suffix {
		if server, host, ok := own.Covering(n, false, srv); ok {
			return &GuardError{Domain: n, Server: server, Hostname: host}
		}
	}
	for _, n := range exact {
		if server, host, ok := own.Covering(n, true, srv); ok {
			return &GuardError{Domain: n, Server: server, Hostname: host}
		}
	}
	return nil
}

// CheckService is services.Deps.Check: the guard over every list holding the
// service, inside the custom save's transaction. A service in no list is
// never refused.
func (s *Service) CheckService(ctx context.Context, tx *sqlx.Tx, serviceID int64, set snapshot.Set) error {
	ms, err := store.MembershipsOf(ctx, tx, serviceID)
	if err != nil || len(ms) == 0 {
		return err
	}
	srv, err := s.servers(ctx)
	if err != nil {
		return err
	}
	ge := guard(set.Suffix, set.Exact, srv)
	if ge == nil {
		return nil
	}
	svc, err := store.GetService(ctx, tx, serviceID)
	if err != nil {
		return err
	}
	ge.List, ge.Service = ms[0].Name, svc.Tag
	for _, m := range ms {
		ge.ListIDs = append(ge.ListIDs, m.ListID)
	}
	return ge
}

// CheckSet is the guard for a set about to join these lists (a new service).
func (s *Service) CheckSet(ctx context.Context, tag string, set snapshot.Set, listIDs []int64) error {
	if len(listIDs) == 0 {
		return nil
	}
	srv, err := s.servers(ctx)
	if err != nil {
		return err
	}
	ge := guard(set.Suffix, set.Exact, srv)
	if ge == nil {
		return nil
	}
	l, err := s.Get(ctx, listIDs[0])
	if err != nil {
		return err
	}
	ge.List, ge.Service, ge.ListIDs = l.Name, tag, listIDs
	return ge
}

// AddNew prepares a new upstream service, checks the guard and, in one Write,
// creates it and adds it to the lists. A refusal is recorded.
func (s *Service) AddNew(ctx context.Context, sel string, listIDs []int64, actor string) (int64, error) {
	p, err := s.d.Services.Prepare(ctx, sel)
	if err != nil {
		return 0, err
	}
	if err := s.CheckSet(ctx, p.Tag, p.Resolved.Set, listIDs); err != nil {
		s.Refused(ctx, err, actor)
		return 0, err
	}
	var id int64
	err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		var err error
		if id, err = s.d.Services.CreateTx(ctx, tx, p, actor); err != nil {
			return err
		}
		for _, l := range listIDs {
			if err := s.AddTx(ctx, tx, l, []int64{id}, actor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.Refused(ctx, err, actor)
		return 0, err
	}
	return id, nil
}

// RecordRefusal records list_refused_server_hostname for each list, in its
// own Write: the refused change's transaction has rolled back.
func (s *Service) RecordRefusal(ctx context.Context, ge *GuardError, lists []int64, actor string) error {
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		for _, id := range lists {
			if err := s.record(ctx, tx, "routing.list_refused_server_hostname", Subject(id), actor, map[string]any{
				"domain": ge.Domain, "server": ge.Server, "hostname": ge.Hostname, "service": ge.Service,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Refused records err's refusal when it is a *GuardError (services.Deps.Refused).
func (s *Service) Refused(ctx context.Context, err error, actor string) {
	var ge *GuardError
	if !errors.As(err, &ge) {
		return
	}
	// The request may be gone; the refusal is still worth keeping.
	if rerr := s.RecordRefusal(context.WithoutCancel(ctx), ge, ge.ListIDs, actor); rerr != nil {
		s.d.Log.Error("routing: recording a guard refusal", "err", rerr)
	}
}

// Covering is servers.RoutingGuard.Covering over every listed name of every
// list, the names a list doesn't install included: any listed name counts.
// One hit per hostname: the first list (the default first, then by name), its
// first service in order.
func (s *Service) Covering(ctx context.Context, hostnames []string) ([]servers.RoutedName, error) {
	if len(hostnames) == 0 {
		return nil, nil
	}
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []servers.RoutedName
	found := map[string]bool{}
	hit := func(name string, exact bool, tag, list string) {
		for _, h := range hostnames {
			if !found[h] && (h == name || !exact && domain.Covers(name, h)) {
				found[h] = true
				out = append(out, servers.RoutedName{Hostname: h, Domain: name, Service: tag, List: list})
			}
		}
	}
	for _, l := range all {
		ms, err := members(ctx, s.d.DB.R, l.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			for _, n := range m.Set.Suffix {
				hit(n, false, m.Tag, l.Name)
			}
			for _, n := range m.Set.Exact {
				hit(n, true, m.Tag, l.Name)
			}
		}
	}
	return out, nil
}
