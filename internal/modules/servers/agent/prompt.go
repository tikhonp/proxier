package agent

import (
	"fmt"
	"strings"
	"time"
)

// PromptData is what the prompt is made from.
type PromptData struct {
	Agent    string
	Template string
	Problem  string
	BaseURL  string // Proxier's address, without /agent/v1
	Token    string
	Expires  time.Time
	Context  Context
	BasedOn  int
}

// opening is the only line that differs between agents.
var opening = map[string]string{
	"claude-code": "You are Claude Code. The owner of a Proxier instance has handed you the draft of one of its server templates to fix.",
	"codex":       "You are Codex. The owner of a Proxier instance has handed you the draft of one of its server templates to fix.",
	"other":       "You are a coding agent. The owner of a Proxier instance has handed you the draft of one of its server templates to fix.",
}

// Prompt is the Markdown the admin pastes into the agent. It holds the real
// token: show it masked with Mask, copy it whole.
func Prompt(d PromptData) string {
	api := strings.TrimRight(d.BaseURL, "/") + "/agent/v1"
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("%s\n\n", opening[d.Agent])
	w("# Task: fix the draft of the template %q\n\n", d.Template)
	w("## The problem\n\n%s\n\n", d.Problem)

	w("## Access\n\n")
	w("- API: `%s`\n", api)
	w("- Every request carries `Authorization: Bearer %s`\n", d.Token)
	w("- The token reaches this one draft and nothing else, and stops working at %s UTC (or earlier if the owner revokes it).\n", d.Expires.UTC().Format("2006-01-02 15:04"))
	w("- The token is a secret. Do not print it, log it, or write it into any file.\n\n")

	w("## API\n\n")
	w("| Request | Does |\n|---|---|\n")
	w("| `GET /context` | The problem, the context the owner chose, the template's name and base version, links. Start here. |\n")
	w("| `GET /draft` | The manifest and the list of files with their ETags. |\n")
	w("| `GET /draft/files/{path}` | One file, with its `ETag`. |\n")
	w("| `PUT /draft/files/{path}` | Write a file (the request body is the file). Send `If-Match: <the file's ETag>` to change a file, `If-None-Match: *` to create one. A stale ETag gets `412`: read again and redo the edit. |\n")
	w("| `DELETE /draft/files/{path}` | Delete a file (`If-Match` as above). |\n")
	w("| `POST /draft/validate` | Run every check on the saved draft and return the report. |\n")
	w("| `POST /draft/preview?server={name}` | Render the saved draft for one of the servers listed in `/context`, secrets masked. |\n")
	w("| `GET /docs/manifest` | The manifest reference and the rendering rules. Read it before you edit `manifest.yaml`. |\n\n")
	w("Every save changes the draft's revision and with it every file's ETag; read a file again after any write. Files are at most 1 MiB, the draft at most 10 MiB, at most 60 requests a minute and 600 in all. A `401` with \"session ended\" means the access is over.\n\n")

	w("## Rules\n\n")
	w("- Work only through this API. You cannot publish, discard, import, deploy or touch anything else, and you must not try.\n")
	w("- Secret values are never visible: generated values and secret parameters render as `•••`, and job logs are redacted. Do not try to recover them.\n")
	w("- Edit the smallest thing that fixes the problem. Keep what the owner did not ask you to change, endpoint keys especially.\n")
	w("- Validate after your edits (`POST /draft/validate`) until the report has no errors, and preview if servers were offered.\n")
	w("- Never publish. The owner reviews the draft and publishes it.\n")
	w("- Finish by saying what you changed and why, file by file, and anything that still needs the owner's attention.\n")

	if d.BasedOn > 0 {
		w("\nThe draft is based on version %d of the template.\n", d.BasedOn)
	}
	var offered []string
	if d.Context.Report != nil {
		offered = append(offered, "the validation report as it was when you were given the draft")
	}
	if d.Context.Job != nil {
		offered = append(offered, fmt.Sprintf("the log of the failed job #%d (redacted)", d.Context.Job.ID))
	}
	if len(d.Context.Servers) > 0 {
		names := make([]string, len(d.Context.Servers))
		for i, sv := range d.Context.Servers {
			names[i] = sv.Name
		}
		offered = append(offered, "a preview for "+strings.Join(names, ", "))
	}
	if d.Context.Diff {
		offered = append(offered, "the diff against the base version")
	}
	if len(offered) > 0 {
		w("\n`GET /context` also holds: %s.\n", strings.Join(offered, "; "))
	}
	return b.String()
}

// Mask hides the token in a prompt for display.
func Mask(prompt, token string) string {
	return strings.ReplaceAll(prompt, token, TokenPrefix+"••••••••••••")
}
