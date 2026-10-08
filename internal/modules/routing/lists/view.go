package lists

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/own"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
	"github.com/tikhonp/proxier/internal/modules/routing/store"
)

// Member is a service of a list with what it installs there.
type Member struct {
	Position int
	Service  services.Item
	Snapshot services.Snapshot // accepted
	Owned    own.Owned
}

// View is what targets following a list get: its services in order and the
// ownership computed over them. Every page, Shadowrocket fetch (3d) and sync
// (3e) uses it; nothing is cached.
type View struct {
	List    List
	Members []Member
	Result  own.Result
}

// View loads a list's services in order with their accepted snapshots, asks
// the hostnames port once, and computes ownership. exclude maps names to
// leave out (a router's infra pins) to their reason.
func (s *Service) View(ctx context.Context, id int64, exclude map[string]string) (View, error) {
	l, err := s.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	rows, err := store.Members(ctx, s.d.DB.R, id)
	if err != nil {
		return View{}, err
	}
	v := View{List: l}
	members := make([]own.Member, 0, len(rows))
	for _, r := range rows {
		it, err := s.d.Services.Get(ctx, r.ServiceID)
		if err != nil {
			return View{}, err
		}
		acc, err := s.d.Services.Accepted(ctx, r.ServiceID)
		if err != nil {
			return View{}, err
		}
		v.Members = append(v.Members, Member{Position: r.Position, Service: it, Snapshot: acc})
		members = append(members, own.Member{ServiceID: it.ID, Tag: it.Tag, Set: acc.Set})
	}
	srv, err := s.servers(ctx)
	if err != nil {
		return View{}, err
	}
	v.Result = own.Compute(members, srv, exclude)
	for i := range v.Members {
		v.Members[i].Owned = v.Result.Services[i]
	}
	return v, nil
}

// servers are the guard's servers; none without the port.
func (s *Service) servers(ctx context.Context) ([]own.Server, error) {
	if s.d.Hostnames == nil {
		return nil, nil
	}
	list, err := s.d.Hostnames.Servers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]own.Server, 0, len(list))
	for _, sh := range list {
		out = append(out, own.Server{Name: sh.Name, Hostnames: sh.Hostnames})
	}
	return out, nil
}

// members reads a list's services in order with their accepted names, on q
// (a write transaction, so a reorder computes before and after on what it
// changes).
func members(ctx context.Context, q sqlx.QueryerContext, listID int64) ([]own.Member, error) {
	rows, err := store.Members(ctx, q, listID)
	if err != nil {
		return nil, err
	}
	out := make([]own.Member, 0, len(rows))
	for _, r := range rows {
		svc, err := store.GetService(ctx, q, r.ServiceID)
		if err != nil {
			return nil, err
		}
		set, err := accepted(ctx, q, r.ServiceID)
		if err != nil {
			return nil, err
		}
		out = append(out, own.Member{ServiceID: svc.ID, Tag: svc.Tag, Set: set})
	}
	return out, nil
}

func accepted(ctx context.Context, q sqlx.QueryerContext, serviceID int64) (snapshot.Set, error) {
	a, err := store.AcceptedSnapshot(ctx, q, serviceID)
	if err != nil {
		return snapshot.Set{}, err
	}
	return snapshot.Set{Suffix: snapshot.Decode(a.Suffix), Exact: snapshot.Decode(a.Exact)}, nil
}
