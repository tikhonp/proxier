package routing

import "github.com/tikhonp/proxier/internal/platform/events"

// Events is the module's event catalog (docs/events.md#routing).
var Events = []events.Type{
	{Name: "routing.service_added", Description: "A service was added."},
	{Name: "routing.service_updated", Description: "A service's source, name, tag, description or domains changed."},
	{Name: "routing.service_removed", Description: "A service in no routing list was removed."},
	{Name: "routing.snapshot_accepted", Description: "A service's new snapshot was accepted (the daily digest carries it)."},
	{Name: "routing.snapshot_rejected", Notify: true, Emoji: "🟡", Description: "A refresh's snapshot was held back by a safety check.",
		NotifyIf: func(p map[string]any) bool { return !truthy(p["in_round"]) }},
	{Name: "routing.snapshot_dismissed", Description: "A held-back snapshot was dismissed."},
	{Name: "routing.refresh_failing", Notify: true, Emoji: "🔴", Description: "A service's refresh failed three times in a row."},
	{Name: "routing.refresh_digest", Notify: true, Emoji: "📋", Description: "The daily refresh round's summary, when it has news.",
		NotifyIf: func(p map[string]any) bool { return num(p["changed"])+num(p["rejected"])+num(p["failing"]) > 0 }},
	{Name: "routing.catalog_refreshed", Description: "The catalog was refreshed."},
	{Name: "routing.catalog_refresh_failed", Notify: true, Emoji: "🔴", Description: "A catalog source has failed to refresh for three days."},
	{Name: "routing.list_created", Description: "A routing list was created."},
	{Name: "routing.list_updated", Description: "A routing list's services, order, name, description or default changed."},
	{Name: "routing.list_deleted", Description: "A routing list was deleted."},
	{Name: "routing.list_refused_server_hostname", Description: "A change was refused: it would route a server's own hostname."},
	{Name: "routing.shadowrocket_created", Description: "A Shadowrocket config was created."},
	{Name: "routing.shadowrocket_updated", Description: "A Shadowrocket config changed."},
	{Name: "routing.shadowrocket_deleted", Description: "A Shadowrocket config was deleted."},
	{Name: "routing.router_added", Description: "A router was added."},
	{Name: "routing.router_updated", Description: "A router's name, connection, names or list changed."},
	{Name: "routing.router_connected", Description: "A router connected for the first time."},
	{Name: "routing.router_synced", Description: "A router was synced."},
	{Name: "routing.router_sync_failed", Notify: true, Emoji: "🔴", Description: "A router sync gave up.",
		NotifyIf: func(p map[string]any) bool { return truthy(p["final"]) }},
	{Name: "routing.router_recovered", Notify: true, Emoji: "🟢", Description: "A router synced again after a notified failure.",
		NotifyIf: func(p map[string]any) bool { return truthy(p["notified"]) }},
	{Name: "routing.router_paused", Description: "A router was paused: no syncs."},
	{Name: "routing.router_resumed", Description: "A paused router was resumed."},
	{Name: "routing.router_removed", Description: "A router was removed."},
	{Name: "routing.drift_detected", Notify: true, Emoji: "🟡", Description: "A router's entries differ from what Proxier installed.",
		NotifyIf: func(p map[string]any) bool { return !truthy(p["repair"]) }},
	{Name: "routing.unmanaged_tags_found", Notify: true, Emoji: "🟡", Description: "A router has tags Proxier never installed."},
	{Name: "routing.unmanaged_tag_ignored", Description: "An unmanaged tag was ignored, or no longer ignored."},
	{Name: "routing.discovery_completed", Description: "A discovery run finished."},
	{Name: "routing.discovery_failed", Description: "A discovery run failed."},
}

func init() {
	for i := range Events {
		Events[i].Module = modName
	}
}

// truthy reads a boolean payload field.
func truthy(v any) bool { b, _ := v.(bool); return b }

// num reads a number payload field: an int when recorded, a float64 once
// read back from JSON.
func num(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}
