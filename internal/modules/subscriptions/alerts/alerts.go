// Package alerts notices links that have probably been passed on: it counts
// who fetches each link (distinct networks and apps), raises at most one
// shared-link alert per link a day over the limits, keeps the per-link limits
// and mute, and looks up the country of each fetching network
// (docs/processes/subscriptions/shared-link-alerts.md).
package alerts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/conf"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/links"
	"github.com/tikhonp/proxier/internal/modules/subscriptions/store"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/jobs"
	"github.com/tikhonp/proxier/internal/platform/settings"
)

// Window is what the counts and an alert cover: the last 24 hours.
const Window = 24 * time.Hour

// MaxLimit bounds a per-link limit, like the settings.
const MaxLimit = 1000

// Deps are what the service uses.
type Deps struct {
	DB       *db.DB
	Events   *events.Catalog
	Settings *settings.Store
	Jobs     *jobs.System
	Now      func() time.Time
	Log      *slog.Logger
}

// Service counts fetches, raises alerts and looks up countries.
type Service struct{ d Deps }

// New returns the service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Limits are what a link is held to.
type Limits struct {
	Networks, Apps int // an alert when more than these fetched it in 24 h
	Muted          bool
	Default        bool // both come from the settings
}

func (s *Service) defaults(ctx context.Context) (networks, apps int, err error) {
	n, err := s.d.Settings.GetInt(ctx, conf.AlertNetworks)
	if err != nil {
		return 0, 0, err
	}
	a, err := s.d.Settings.GetInt(ctx, conf.AlertApps)
	return int(n), int(a), err
}

func limitsOf(networks, apps sql.NullInt64, muted bool, defNetworks, defApps int) Limits {
	l := Limits{Networks: defNetworks, Apps: defApps, Muted: muted, Default: !networks.Valid && !apps.Valid}
	if networks.Valid {
		l.Networks = int(networks.Int64)
	}
	if apps.Valid {
		l.Apps = int(apps.Int64)
	}
	return l
}

// Limits are the link's overrides, else the settings.
func (s *Service) Limits(ctx context.Context, l links.Link) (Limits, error) {
	n, a, err := s.defaults(ctx)
	if err != nil {
		return Limits{}, err
	}
	return limitsOf(nullInt(l.AlertNetworks), nullInt(l.AlertApps), l.AlertsMuted, n, a), nil
}

func nullInt(n int) sql.NullInt64 { return sql.NullInt64{Int64: int64(n), Valid: n != 0} }

// HasAlert: alerted within the last 24 hours and not muted. The Links list,
// the link page and the dashboard show it.
func (s *Service) HasAlert(l links.Link, now time.Time) bool {
	return !l.AlertsMuted && !l.AlertedAt.IsZero() && now.Sub(l.AlertedAt) < Window
}

// SetLimits saves a link's limits (0 = the setting, else 1–1000) and mute;
// link.changed names "alert limits" and/or "alerts muted". Raising the limits
// leaves a raised alert as it is. Errors are store.FieldErrors.
func (s *Service) SetLimits(ctx context.Context, linkID int64, networks, apps int, muted bool, actor string) error {
	fe := store.FieldErrors{}
	if networks < 0 || networks > MaxLimit {
		fe["networks"] = "alerts.err.limit"
	}
	if apps < 0 || apps > MaxLimit {
		fe["apps"] = "alerts.err.limit"
	}
	if len(fe) > 0 {
		return fe
	}
	return s.d.DB.Write(ctx, func(tx *sqlx.Tx) error {
		l, err := store.GetLink(ctx, tx, linkID)
		if errors.Is(err, store.ErrNotFound) {
			return links.ErrNotFound
		}
		if err != nil {
			return err
		}
		if l.State == "deleted" {
			return links.ErrDeleted
		}
		n, a := nullInt(networks), nullInt(apps)
		var fields []string
		if n != l.AlertNetworks || a != l.AlertApps {
			fields = append(fields, "alert limits")
		}
		if muted != l.AlertsMuted {
			fields = append(fields, "alerts muted")
		}
		if len(fields) == 0 {
			return nil
		}
		if err := store.SetAlertLimits(ctx, tx, linkID, n, a, muted); err != nil {
			return err
		}
		_, err = s.d.Events.Record(ctx, tx, events.Event{
			Time: db.At(s.d.Now()), Type: "link.changed", Subject: links.Subject(linkID), Actor: actor,
			Payload: map[string]any{"fields": strings.Join(fields, ", ")},
		})
		return err
	})
}
