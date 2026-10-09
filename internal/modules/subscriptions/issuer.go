package subscriptions

import (
	"context"
	"errors"
	"sort"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
)

// The port router scripts use (docs/modules/subscriptions.md#ports). A module
// never reads this module's tables; it asks through it.

// FieldErrors are the port's field errors (field → i18n key), so a consumer
// reads them with errors.As without importing a store.
type FieldErrors = store.FieldErrors

// ErrLinkNotFound: no link has that id (a deleted one still has its row).
var ErrLinkNotFound = errors.New("subscriptions: no such link")

// IssuerSubscription is a subscription a new link can serve.
type IssuerSubscription struct {
	ID      int64
	Name    string
	Servers int
}

// IssuedLink is a link as it is now. URL carries the token: callers must not
// log it.
type IssuedLink struct {
	ID           int64
	Name         string
	Subscription string // its name
	State        string // active, disabled, expired, deleted
	URL          string // "" in Links and for a deleted link
}

// LinkIssuer is how router scripts give a new router its subscription link.
type LinkIssuer interface {
	// Subscriptions lists every subscription by name.
	Subscriptions(ctx context.Context) ([]IssuerSubscription, error)
	// Links lists every link that isn't deleted, by name, without URLs.
	Links(ctx context.Context) ([]IssuedLink, error)
	// Link reads one link with its URL; ErrLinkNotFound for no such row.
	Link(ctx context.Context, id int64) (IssuedLink, error)
	// Issue creates an active link without expiry inside tx; FieldErrors
	// (name, subscription) before anything is written.
	Issue(ctx context.Context, tx *sqlx.Tx, name string, subscriptionID int64, actor string) (IssuedLink, error)
}

// LinkIssuer returns the module's link port. It reads the services when
// called, so main.go hands it over before Init.
func (m *Module) LinkIssuer() LinkIssuer { return issuer{m} }

type issuer struct{ m *Module }

func (i issuer) Subscriptions(ctx context.Context) ([]IssuerSubscription, error) {
	rows, err := i.m.Subs.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IssuerSubscription, 0, len(rows))
	for _, r := range rows {
		out = append(out, IssuerSubscription{ID: r.ID, Name: r.Name, Servers: len(r.Members)})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

func (i issuer) Links(ctx context.Context) ([]IssuedLink, error) {
	rows, err := i.m.Links.List(ctx, links.Filter{})
	if err != nil {
		return nil, err
	}
	now := i.m.Now()
	out := make([]IssuedLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, IssuedLink{ID: r.ID, Name: r.Name, Subscription: r.Subscription, State: r.Status(now)})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

func (i issuer) Link(ctx context.Context, id int64) (IssuedLink, error) {
	l, err := i.m.Links.Get(ctx, id)
	if errors.Is(err, links.ErrNotFound) {
		return IssuedLink{}, ErrLinkNotFound
	}
	if err != nil {
		return IssuedLink{}, err
	}
	out := IssuedLink{ID: l.ID, Name: l.Name, State: l.Status(i.m.Now())}
	if l.SubscriptionID != 0 {
		if sub, err := store.GetSubscription(ctx, i.m.deps.DB.R, l.SubscriptionID); err == nil {
			out.Subscription = sub.Name
		} else if !errors.Is(err, store.ErrNotFound) {
			return IssuedLink{}, err
		}
	}
	if l.State == "deleted" {
		return out, nil
	}
	token, err := i.m.Links.Token(ctx, id)
	if err != nil {
		return IssuedLink{}, err
	}
	out.URL = i.m.Links.URL(token)
	return out, nil
}

func (i issuer) Issue(ctx context.Context, tx *sqlx.Tx, name string, subscriptionID int64, actor string) (IssuedLink, error) {
	id, token, err := i.m.Links.CreateTx(ctx, tx, links.New{Name: name, SubscriptionID: subscriptionID, Lang: i.m.Links.DefaultLang(ctx)}, actor)
	if err != nil {
		return IssuedLink{}, err
	}
	sub, err := store.GetSubscription(ctx, tx, subscriptionID)
	if err != nil {
		return IssuedLink{}, err
	}
	l, err := store.GetLink(ctx, tx, id)
	if err != nil {
		return IssuedLink{}, err
	}
	return IssuedLink{ID: id, Name: l.Name, Subscription: sub.Name, State: "active", URL: i.m.Links.URL(token)}, nil
}
