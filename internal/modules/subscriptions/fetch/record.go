package fetch

import (
	"net/netip"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/output"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/subs"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
)

// allUnhealthyEvery is how often a subscription may raise
// subscription.all_unhealthy.
const allUnhealthyEvery = time.Hour

// record stores the fetch, the link's last fetch, the country lookup of a
// network not seen yet and, when hiding would have emptied the output,
// subscription.all_unhealthy (at most hourly), in one transaction.
func (s *Service) record(c *echo.Context, l links.Link, format string, resp output.Response) error {
	ctx := c.Request().Context()
	ip := c.RealIP()
	addr, _ := netip.ParseAddr(ip)
	network := Network(addr)
	app := Detect(c.Request().UserAgent())
	ua := TrimUA(c.Request().UserAgent())
	now := db.At(s.d.Now())
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		if _, err := store.InsertFetch(ctx, tx, store.Fetch{
			LinkID: l.ID, At: now, IP: ip, Network: network, UserAgent: ua, App: app, Format: format, Outcome: resp.Outcome,
		}); err != nil {
			return err
		}
		if err := store.SetLastFetch(ctx, tx, l.ID, now, app, network); err != nil {
			return err
		}
		if s.d.Alerts != nil {
			if err := s.d.Alerts.QueueCountry(ctx, tx, network); err != nil {
				return err
			}
		}
		if !resp.AllHidden {
			return nil
		}
		sub, err := store.GetSubscription(ctx, tx, l.SubscriptionID)
		if err != nil {
			return err
		}
		if !sub.AllUnhealthyAt.IsZero() && now.Sub(sub.AllUnhealthyAt.Time) < allUnhealthyEvery {
			return nil
		}
		if err := store.SetAllUnhealthy(ctx, tx, sub.ID, now); err != nil {
			return err
		}
		names := make([]string, len(resp.Served))
		for i, sv := range resp.Served {
			names[i] = sv.Name
		}
		_, err = s.d.Events.Record(ctx, tx, events.Event{
			Time: now, Type: "subscription.all_unhealthy", Subject: subs.Subject(sub.ID), Actor: events.ActorSystem,
			Payload: map[string]any{"servers": strings.Join(names, ", "), "count": len(names)},
		})
		return err
	})
}
