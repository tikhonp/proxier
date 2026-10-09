package routers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/routing/selector"
	"github.com/tikhonp/proxier/internal/modules/routing/services"
	"github.com/tikhonp/proxier/internal/modules/routing/snapshot"
)

// Ways to adopt an unmanaged tag.
const (
	AdoptV2fly    = "v2fly"
	AdoptIplist   = "iplist"
	AdoptExisting = "existing"
	AdoptCustom   = "custom"
)

var (
	// ErrInvalidTag is a tag no service can have: it can only be removed or ignored.
	ErrInvalidTag = errors.New("routers: the tag can't be a service's tag")
	// ErrNoOffer is a way of adopting that the tag doesn't have.
	ErrNoOffer = errors.New("routers: that way of adopting isn't offered")
)

// Adoptable reports whether a tag can become a service's tag.
func Adoptable(tag string) bool { return selector.ValidTag(tag) == nil }

// Adopt makes an unmanaged tag Proxier's: the service it becomes (the
// catalog's v2fly or iplist selector, an existing service with the tag, or a
// custom service made from the router's names) is added to the router's
// list, past the guard, and a sync is queued at once, which records or
// updates the tag.
func (s *Service) Adopt(ctx context.Context, id int64, tag, how, actor string) (int64, error) {
	rt, err := s.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	u, err := s.UnmanagedOne(ctx, id, tag)
	if err != nil {
		return 0, err
	}
	if !Adoptable(tag) {
		return 0, ErrInvalidTag
	}
	lists := []int64{rt.ListID}
	var sid int64
	switch how {
	case AdoptV2fly, AdoptIplist:
		i := slices.IndexFunc(u.Catalog, func(sel string) bool { return strings.HasPrefix(sel, how+":") })
		if i < 0 {
			return 0, ErrNoOffer
		}
		if sid, err = s.d.Lists.AddNew(ctx, u.Catalog[i], lists, actor); err != nil {
			return 0, err
		}
	case AdoptExisting:
		if u.Existing == nil {
			return 0, ErrNoOffer
		}
		sid = u.Existing.ID
		if err := s.d.Lists.Add(ctx, rt.ListID, []int64{sid}, actor); err != nil {
			return 0, err
		}
	case AdoptCustom:
		if u.Existing != nil {
			return 0, &services.TagTakenError{Tag: tag, Existing: *u.Existing}
		}
		var suffix, exact []string
		for _, e := range u.Names {
			if e.Exact {
				exact = append(exact, e.Name)
			} else {
				suffix = append(suffix, e.Name)
			}
		}
		set := snapshot.New(suffix, exact, nil)
		if err := s.d.Lists.CheckSet(ctx, tag, set, lists); err != nil {
			s.d.Lists.Refused(ctx, err, actor)
			return 0, err
		}
		err = s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
			var err error
			if sid, err = s.d.Services.CreateCustomTx(ctx, tx, services.Custom{Name: tag, Tag: tag, Origin: "router"}, set, actor); err != nil {
				return err
			}
			return s.d.Lists.AddTx(ctx, tx, rt.ListID, []int64{sid}, actor)
		})
		if err != nil {
			s.d.Lists.Refused(ctx, err, actor)
			return 0, err
		}
	default:
		return 0, fmt.Errorf("routers: adopt how %q: %w", how, ErrNoOffer)
	}
	if _, err := s.syncAtOnce(ctx, id, syncPayload{Trigger: TriggerUnmanaged, Why: []string{"adopted " + tag}}, actor); err != nil &&
		!errors.Is(err, ErrNotActive) {
		return sid, err
	}
	return sid, nil
}
