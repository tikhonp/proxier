package subscriptions

import "github.com/tikhonp/proxier/internal/platform/events"

const modName = "subscriptions"

// Events is the module's event catalog (docs/events.md#subscriptions). Every
// type of Phase 2 is declared now, so the later sub-phases only record them.
var Events = []events.Type{
	{Name: "subscription.created", Description: "A subscription was created."},
	{Name: "subscription.updated", Description: "A subscription's settings changed (changes)."},
	{Name: "subscription.deleted", Description: "A subscription no link pointed to was deleted."},
	{Name: "subscription.servers_changed", Description: "Servers were added to a subscription, removed or reordered."},
	{Name: "subscription.all_unhealthy", Notify: true, Emoji: "🟡", Description: "Hiding unhealthy servers would have left a subscription empty, so every server is served."},
	{Name: "link.created", Description: "A link was created."},
	{Name: "link.disabled", Description: "A link was disabled: it serves its stub entry."},
	{Name: "link.enabled", Description: "A disabled link was enabled again."},
	{Name: "link.token_regenerated", Description: "A link got a new URL; the old one answers 404."},
	{Name: "link.deleted", Description: "A link was deleted; it serves “Link removed” until its tombstone ends."},
	{Name: "link.subscription_changed", Description: "A link was moved to another subscription."},
	{Name: "link.expiry_changed", Description: "A link's expiry was set, changed or cleared."},
	{Name: "link.changed", Description: "A link's name, note, language, format or alert limits changed."},
	{Name: "link.expiring_soon", Notify: true, Emoji: "⏳", Description: "A link expires soon."},
	{Name: "link.expired", Notify: true, Emoji: "⏳", Description: "A link expired and serves its stub entry."},
	{Name: "link.shared_suspected", Notify: true, Emoji: "🟡", Description: "A link was fetched from more networks or apps than one person's devices explain."},
	{Name: "link.cut_off", Description: "A link was cut off: disabled, and its servers rotated."},
}

func init() {
	for i := range Events {
		Events[i].Module = modName
	}
}
