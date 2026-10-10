package routerscripts

import "github.com/tikhonp/proxier/internal/platform/events"

// Events is the module's event catalog (docs/events.md#router-scripts).
var Events = []events.Type{
	{Name: "routerscript.created", Description: "A router script was created."},
	{Name: "routerscript.changed", Description: "A router script's name, slug or description changed."},
	{Name: "routerscript.draft_saved", Description: "A router script's draft was saved."},
	{Name: "routerscript.draft_discarded", Description: "A router script's draft was discarded."},
	{Name: "routerscript.version_published", Description: "A router script's draft was published as its next version."},
	{Name: "routerscript.current_changed", Description: "Another version of a router script was made current."},
	{Name: "routerscript.archived", Description: "A router script was archived or unarchived."},
	{Name: "routerscript.deleted", Description: "A router script with no generations was deleted."},
	{Name: "routerscript.generated", Description: "A version was filled in for a new router."},
	{Name: "routerscript.fetch_url_created", Description: "A generation got a single-use fetch URL."},
	{Name: "routerscript.fetched", Notify: true, Emoji: "📥", Description: "A router fetched its generated script."},
	{Name: "routerscript.fetch_url_expired", Description: "A fetch URL ended unused."},
}

func init() {
	for i := range Events {
		Events[i].Module = modName
	}
}
