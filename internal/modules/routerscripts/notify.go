package routerscripts

import (
	"context"

	"github.com/tikhonp/proxier/internal/platform/events"
	"github.com/tikhonp/proxier/internal/platform/i18n"
	"github.com/tikhonp/proxier/internal/platform/notify"
)

// RenderNotification words routerscript.fetched so that a fetch without a
// user agent still reads well; the other types use the default texts.
func (m *Module) RenderNotification(ctx context.Context, e events.Event, loc *i18n.Localizer) (notify.Message, bool, error) {
	if e.Type != "routerscript.fetched" {
		return notify.Message{}, false, nil
	}
	subject := "generation " + e.Subject.ID
	if names, err := m.NameSubjects(ctx, e.Subject.Type, []string{e.Subject.ID}); err == nil {
		if ref, ok := names[e.Subject.ID]; ok {
			subject = ref.Label
		}
	}
	ip, _ := e.Payload["ip"].(string)
	ua, _ := e.Payload["user_agent"].(string)
	args := i18n.Args{"subject": subject, "ip": ip, "user_agent": ua}
	body := loc.T("notify.routerscript.fetched.body", args)
	if ua == "" {
		body = loc.T("notify.routerscript.fetched.body_no_agent")
	}
	return notify.Message{Title: loc.T("notify.routerscript.fetched", args), Body: body}, true, nil
}
