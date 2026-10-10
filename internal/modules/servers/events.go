package servers

import (
	"github.com/tikhonp/proxier/internal/platform/events"
)

const modName = "servers"

func notCancelled(p map[string]any) bool {
	c, _ := p["cancelled"].(bool)
	return !c
}

// healthNotifies is the recovery rule (build README, Phase 1 decisions): a
// change to blocked or down notifies, and so does a change back to healthy,
// but only from blocked or down. degraded and unknown stay quiet.
func healthNotifies(p map[string]any) bool {
	to, _ := p["to"].(string)
	from, _ := p["from"].(string)
	switch to {
	case "blocked", "down":
		return true
	case "healthy":
		return from == "blocked" || from == "down"
	}
	return false
}

// Events is the module's event catalog (docs/events.md#servers).
var Events = []events.Type{
	{Name: "template.created", Description: "A template was created."},
	{Name: "template.version_published", Description: "A template version was published (version, warnings)."},
	{Name: "template.default_version_changed", Description: "The default version of a template changed."},
	{Name: "template.archived", Description: "A template was archived."},
	{Name: "template.unarchived", Description: "A template was brought back from the archive."},
	{Name: "template.deleted", Description: "A template that never built a server was deleted."},
	{Name: "template.agent_session_opened", Notify: true, Emoji: "🤖", Description: "A draft was handed to an agent: anyone holding the prompt can edit it until the session ends."},
	{Name: "template.draft_saved", Description: "A template draft was saved."},
	{Name: "template.draft_discarded", Description: "A template draft was discarded."},
	{Name: "template.changed", Description: "A template's name, slug or description changed."},
	{Name: "template.draft_validated", Description: "A template draft was validated."},
	{Name: "template.agent_session_closed", Description: "An agent session ended (expired, revoked, published, discarded, replaced)."},
	{Name: "location.created", Description: "A location was added."},
	{Name: "location.changed", Description: "A location was renamed or moved to another country."},
	{Name: "location.deleted", Description: "A location without servers was deleted."},
	{Name: "server.created", Description: "A server was added and provisioning queued."},
	{Name: "server.provisioning_failed", Notify: true, NotifyIf: notCancelled, Emoji: "🔴", Description: "Provisioning stopped at a step."},
	{Name: "server.activated", Notify: true, Emoji: "🟢", Description: "A server passed its smoke test and is active."},
	{Name: "server.redeployed", Description: "A server was redeployed, upgraded, restored or restarted."},
	{Name: "server.redeploy_failed", Notify: true, NotifyIf: notCancelled, Emoji: "🔴", Description: "A redeploy, upgrade or rotation failed."},
	{Name: "server.credentials_rotated", Notify: true, Emoji: "🔑", Description: "A server's credentials were rotated."},
	{Name: "server.health_changed", Notify: true, NotifyIf: healthNotifies, Emoji: "🔴", Description: "A server's health changed: blocked, down, or back to healthy."},
	{Name: "server.still_unhealthy", Notify: true, Emoji: "🔴", Description: "Reminder every 24 h while a server is blocked or down."},
	{Name: "server.cert_expiring", Notify: true, Emoji: "🟡", Description: "A server's certificate has fewer than 14 days left (once per crossing, not daily)."},
	{Name: "server.disk_low", Notify: true, Emoji: "🟡", Description: "A server has less than 10 % disk free (once per crossing)."},
	{Name: "server.checks_paused", Description: "Checks of a server were paused."},
	{Name: "server.checks_resumed", Description: "Checks of a server were resumed."},
	{Name: "server.retired", Description: "A server was retired."},
	{Name: "server.retire_failed", Notify: true, NotifyIf: notCancelled, Emoji: "🔴", Description: "Retirement stopped at a step; the server stays out of service until retried."},
	{Name: "server.notes_changed", Description: "The notes of a server changed."},
	{Name: "server.rollout_started", Description: "A rolling upgrade started."},
	{Name: "server.rollout_finished", Description: "A rolling upgrade finished, stopped or was cancelled."},
	{Name: "health.home_offline", Description: "Home has no internet: server verdicts are on hold."},
	{Name: "health.foreign_unreachable", Notify: true, Emoji: "🟡", Description: "Foreign internet is unreachable from home: server verdicts are on hold."},
	{Name: "health.home_recovered", Notify: true, Emoji: "🟢", Description: "Home's connectivity is back."},
}

func init() {
	for i := range Events {
		Events[i].Module = modName
	}
}
