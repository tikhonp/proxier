package agent

import (
	"fmt"
	"strings"
	"time"
)

// PromptData is what the prompt is made from.
type PromptData struct {
	Agent   string
	Script  string
	Problem string
	BaseURL string // Proxier's address, without the API path
	Token   string
	Expires time.Time
	Context Context
	BasedOn int
}

// opening is the only line that differs between agents.
var opening = map[string]string{
	"claude-code": "You are Claude Code. The owner of a Proxier instance has handed you the draft of one of its MikroTik router scripts to change.",
	"codex":       "You are Codex. The owner of a Proxier instance has handed you the draft of one of its MikroTik router scripts to change.",
	"other":       "You are a coding agent. The owner of a Proxier instance has handed you the draft of one of its MikroTik router scripts to change.",
}

// Prompt is the Markdown the admin pastes into the agent. It holds the real
// token: show it masked with agentapi.Mask, copy it whole.
func Prompt(d PromptData) string {
	api := strings.TrimRight(d.BaseURL, "/") + APIPrefix
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("%s\n\n", opening[d.Agent])
	w("# Task: change the draft of the router script %q\n\n", d.Script)
	w("## The problem\n\n%s\n\n", d.Problem)

	w("## Access\n\n")
	w("- API: `%s`\n", api)
	w("- Every request carries `Authorization: Bearer %s`\n", d.Token)
	w("- The token reaches this one draft and nothing else, and stops working at %s UTC (or earlier if the owner revokes it).\n", d.Expires.UTC().Format("2006-01-02 15:04"))
	w("- The token is a secret. Do not print it, log it, or write it into any file.\n\n")

	w("## The script\n\n")
	w("The draft is one RouterOS script (`.rsc`) that sets up a fresh MikroTik when it is imported. Its `PARAMETERS` block (`:local` lines between `# PARAMETERS` and `# END PARAMETERS`) holds the values Proxier fills in for each router; the comments above a `:local` line are its description and `# @…` annotations. Read `GET /docs/parameters` before you touch that block.\n\n")

	w("## API\n\n")
	w("| Request | Does |\n|---|---|\n")
	w("| `GET /context` | The problem, the script's name and base version, the check of the draft and the context the owner chose. Start here. |\n")
	w("| `GET /draft` | The whole script as text, with its `ETag`. |\n")
	w("| `PUT /draft` | Replace the script (the request body is the whole script). Send `If-Match: <the ETag>`. A stale ETag gets `412`: read again and redo the edit. The answer has the new ETag and the check. |\n")
	w("| `POST /draft/check` | Check the saved draft (empty body), or the script in the body without saving it: what publishing would refuse or warn about, and the parameters it reads. |\n")
	w("| `GET /docs/parameters` | The reference of the PARAMETERS block: literals, descriptions, groups, annotations, errors and warnings. |\n\n")
	w("Every save changes the ETag. The script is at most 512 KiB, at most 60 requests a minute and 600 in all. A `401` with \"session ended\" means the access is over.\n\n")

	w("## Rules\n\n")
	w("- Work only through this API. You cannot publish, discard, generate for a router, or touch anything else, and you must not try.\n")
	w("- Secret values are never visible: the defaults of `@secret` parameters and of the `@fill subscription-link` one read as `%s`. Leave that value as it is, on the same parameter, and Proxier keeps the real one; do not try to recover it.\n", Masked)
	w("- Edit the smallest thing that fixes the problem. Keep what the owner did not ask you to change: parameter names especially, since routers and other parts of Proxier rely on them.\n")
	w("- The script must stay valid RouterOS that imports unchanged on the router. You cannot run it; reason carefully about RouterOS syntax and version.\n")
	w("- Check after your edits until the check has no errors. Mention every warning that remains.\n")
	w("- Never publish. The owner reviews the draft and publishes it.\n")
	w("- Finish by saying what you changed and why, and anything that still needs the owner's attention.\n")

	if d.BasedOn > 0 {
		w("\nThe draft is based on version %d of the script.\n", d.BasedOn)
	}
	if d.Context.Diff {
		w("\n`GET /context` also holds the diff against the base version.\n")
	}
	return b.String()
}
