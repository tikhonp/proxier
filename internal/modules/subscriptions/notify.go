package subscriptions

import (
	"context"
	"strconv"
	"time"

	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

// RenderNotification names the day in link.expiring_soon and link.expired,
// in the admin's language and zone: the last day the link works (or worked),
// as its stub entry does; and gives link.shared_suspected its counts in the
// language's plural forms (never an IP). The other types use the default
// texts.
func (m *Module) RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	switch e.Type {
	case "link.expiring_soon", "link.expired", "link.shared_suspected":
	default:
		return notify.Message{}, false, nil
	}
	id, err := strconv.ParseInt(e.Subject.ID, 10, 64)
	if err != nil {
		return notify.Message{}, false, nil
	}
	l, err := m.Links.Get(ctx, id)
	if err != nil {
		return notify.Message{}, false, nil
	}
	if e.Type == "link.shared_suspected" {
		networks, _ := e.Payload["networks"].(float64)
		apps, _ := e.Payload["apps"].(float64)
		args := i18n.Args{"subject": l.Name, "networks": loc.N("alerts.n_networks", int64(networks)), "apps": loc.N("alerts.n_apps", int64(apps))}
		return notify.Message{Title: loc.T("notify.link.shared_suspected", args), Body: loc.T("notify.link.shared_suspected.body", args)}, true, nil
	}
	text, _ := e.Payload["expiry"].(string)
	expires, err := time.Parse(db.TimeLayout, text)
	if err != nil {
		return notify.Message{}, false, nil
	}
	key := "notify." + e.Type
	args := i18n.Args{"subject": l.Name, "date": loc.ShortDate(expires.Add(-time.Second))}
	return notify.Message{Title: loc.T(key, args), Body: loc.T(key+".body", args)}, true, nil
}
