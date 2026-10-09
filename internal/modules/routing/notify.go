package routing

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

// RenderNotification words the module's refresh, catalog and router sync
// notifications in the admin's language with plural forms. 3f's router ones
// use the default texts. The button opens the subject's page (the platform's).
func (m *Module) RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	p := e.Payload
	switch e.Type {
	case "routing.snapshot_rejected":
		reason := loc.T("refresh.notify.reason.empty")
		if s, _ := p["reason"].(string); s == "shrink" {
			reason = loc.T("refresh.notify.reason.shrink", i18n.Args{"pct": int64(num(p["lost_pct"]))})
		}
		args := i18n.Args{"subject": m.serviceTag(ctx, e.Subject), "old": loc.Number(int64(num(p["old_count"]))), "reason": reason}
		return notify.Message{
			Title: loc.T("notify.routing.snapshot_rejected", args),
			Body:  loc.N("notify.routing.snapshot_rejected.body", int64(num(p["new_count"])), args),
		}, true, nil
	case "routing.refresh_failing":
		args := i18n.Args{"subject": m.serviceTag(ctx, e.Subject), "error": p["error"]}
		return notify.Message{
			Title: loc.T("notify.routing.refresh_failing", args),
			Body:  loc.N("notify.routing.refresh_failing.body", int64(num(p["failures"])), args),
		}, true, nil
	case "routing.refresh_digest":
		msg := notify.Message{Title: loc.T("notify.routing.refresh_digest.none")}
		if changed := int64(num(p["changed"])); changed > 0 {
			msg.Title = loc.N("notify.routing.refresh_digest", changed, i18n.Args{
				"added": loc.Number(int64(num(p["added"]))), "removed": loc.Number(int64(num(p["removed"]))),
			})
		}
		var parts []string
		for _, k := range []string{"rejected", "failing", "still_failing"} {
			if n := int64(num(p[k])); n > 0 {
				parts = append(parts, loc.N("notify.routing.refresh_digest."+k, n))
			}
		}
		msg.Body = strings.Join(parts, " · ")
		return msg, true, nil
	case "routing.catalog_refresh_failed":
		source, _ := p["source"].(string)
		args := i18n.Args{"source": source, "error": p["error"], "since": dateOf(loc, p["since"]), "date": "—"}
		if st, err := m.Catalog.Status(ctx); err == nil {
			for _, s := range st {
				if s.Source == source && !s.RefreshedAt.IsZero() {
					args["date"] = loc.ShortDate(s.RefreshedAt)
				}
			}
		}
		if args["date"] == "—" {
			return notify.Message{Title: loc.T("notify.routing.catalog_refresh_failed", args),
				Body: loc.T("notify.routing.catalog_refresh_failed.never", args)}, true, nil
		}
		return notify.Message{Title: loc.T("notify.routing.catalog_refresh_failed", args),
			Body: loc.T("notify.routing.catalog_refresh_failed.body", args)}, true, nil
	case "routing.router_sync_failed":
		args := i18n.Args{"subject": m.routerName(ctx, e.Subject), "step": p["step"], "error": p["error"]}
		body := loc.T("notify.routing.router_sync_failed.body", args)
		if a := int64(num(p["attempt"])); a > 1 {
			body += " · " + loc.N("notify.routing.router_sync_failed.attempts", a)
		}
		return notify.Message{Title: loc.T("notify.routing.router_sync_failed", args), Body: body}, true, nil
	case "routing.router_recovered":
		args := i18n.Args{"subject": m.routerName(ctx, e.Subject)}
		return notify.Message{
			Title: loc.T("notify.routing.router_recovered", args),
			Body:  loc.N("notify.routing.router_recovered.body", int64(num(p["failures"])), args),
		}, true, nil
	}
	return notify.Message{}, false, nil
}

// routerName names a router subject; a removed one keeps its id.
func (m *Module) routerName(ctx context.Context, s events.Subject) string {
	if id, err := strconv.ParseInt(s.ID, 10, 64); err == nil {
		if r, err := m.Routers.Get(ctx, id); err == nil {
			return r.Name
		}
	}
	return s.String()
}

// serviceTag names a service subject; a removed one keeps its id.
func (m *Module) serviceTag(ctx context.Context, s events.Subject) string {
	if id, err := strconv.ParseInt(s.ID, 10, 64); err == nil {
		if it, err := m.Services.Get(ctx, id); err == nil {
			return it.Tag
		}
	}
	return s.String()
}

func dateOf(loc *i18n.Localizer, v any) string {
	s, _ := v.(string)
	t, err := time.Parse(db.TimeLayout, s)
	if err != nil {
		return "—"
	}
	return loc.ShortDate(t)
}
