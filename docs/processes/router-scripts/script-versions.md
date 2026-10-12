# Script versions

**Actors**: Admin; router scripts module.

`fresh-router.rsc` changes over time (RouterOS quirks, new watchdogs, guest VLAN rules). Proxier keeps every version, shows what each changed, and detects each version's parameters from the script itself. The script stays a plain RouterOS file you could still import by hand ([ADR 0011](../../adr/0011-router-script-parameters-from-local-block.md)).

## Steps — creating and publishing

1. Router scripts → **New script**: name ("fresh-router"), slug, description, and the body (paste or upload `fresh-router.rsc`). The body becomes a draft. → `routerscript.created`
2. The editor shows the body with RouterOS highlighting and, next to it, the **detected parameters** (name, default, description, annotations) and **computed values**, updated as the body changes.
3. **Publish** asks for version notes, then checks the body:
   - **errors** block publishing: duplicate parameter names in the PARAMETERS block, or an annotation that is malformed or unknown;
   - **warnings** need a confirmation:
     - no PARAMETERS block at all (the script is still versioned and downloadable, with nothing to fill in);
     - a PARAMETERS block without `# END PARAMETERS`, with the last detected parameters listed so you can see where it stopped (or, when it has no `:local` line, saying so);
     - a parameter that was in the version the draft is based on has disappeared ("vethName was in v3 and is gone here").

   **Publish vN…** saves the editor first, then opens the publish page: the errors with their lines, then the warnings, the notes, a tick "Publish with these warnings" when there are any, and what happens. On success the draft becomes version N+1 and the **current** version, the confirmed warnings are kept with it, and the first publish fixes the slug. → `routerscript.version_published{version, parameters, warnings}`
4. **Edit** creates a draft from the current version (or opens the one there is). **Save draft** keeps a revision: a save from a tab whose draft another tab saved meanwhile is refused with the band "The draft changed since you opened it (another tab saved it)." and **Reload**, and the typed text stays in the editor. **Discard draft** drops it; it needs a version (a script with none is deleted instead). → `routerscript.draft_saved{based_on}`, `routerscript.draft_discarded{based_on}`

## Steps — handing the draft to an agent

A coding agent on the admin's machine (Claude Code, Codex or similar) can change a draft remotely, as it can a [template draft](../servers/template-authoring.md#steps--handing-the-draft-to-an-agent). Proxier never runs the agent. It gives the agent a prompt and a short-lived key that only reaches this draft.

1. Over the draft, the admin chooses **Hand off to an agent…** and fills in:
   - what the problem is and what the agent should do, as free text (for example the error RouterOS printed on import);
   - whether to include the diff against the base version (offered when the draft has one, ticked by default);
   - the agent (Claude Code, Codex, other), which changes only the wording;
   - how long access lasts: 30 min, 2 h (the default) or 8 h.

   The form names the parameters whose defaults the agent will never see (below).
2. Proxier creates an **agent session** and returns a token (`pxa_…`, stored only as a hash) inside a Markdown prompt to **Copy**; the page shows the prompt with the token masked, once. → `routerscript.agent_session_opened{expires_at, agent, context, session}`
3. The agent works through the agent API. Its saves are recorded with `agent:<session>` as the actor → `routerscript.draft_saved{based_on}`.
4. While a session is open, the script page shows a band over the draft: the agent, the first line of the problem, how many saves, when it expires, **What it changed** (the activity of this session) and **Revoke access**. The page asks every 10 seconds whether the draft is still as the editor holds it; when an agent (or another tab) saved, the band "The draft changed since you opened it (an agent or another tab saved it)" turns **Save draft** and **Publish** off and offers **Reload**. The typed text stays in the editor.
5. The session ends on expiry, on **Revoke access**, when the draft is published or discarded, when a new hand-off replaces it (`replaced`), or after the 8-hour maximum. Deleting the script deletes its sessions. A request after that gets `401` "session ended", the same answer as an unknown token. → `routerscript.agent_session_closed{reason, saves, session}`
6. The admin reviews the draft (the parameters panel, **Diff with its version**) and publishes as usual. Publishing is never done by the agent.

The **agent API** lives under `/agent/v1/router-script` and accepts only an agent token, never a session cookie.

| Method and path | Does |
|---|---|
| `GET /context` | The problem, the script's name, slug, base version and revision, the check of the draft, the diff against the base version when included (`null` otherwise), links. |
| `GET /draft` | The whole script as `text/plain`, with `ETag: "<revision>"`. |
| `PUT /draft` | Replace the script. Requires `If-Match` with the ETag: a missing header gets `428`, a stale one `412` "the draft changed". The body gets the stored draft's line endings, as an editor save does. The answer has the new revision, ETag, whether anything changed and the check. |
| `POST /draft/check` | What publishing would refuse or warn about, in English, with lines, and the parameters and computed values read from the script: of the saved draft (empty body) or of the body, which is not saved. |
| `GET /docs/parameters` | The Parameters section of the [router scripts module](../../modules/router-scripts.md#parameters), Markdown. |

Rules:

- **One open session per draft.** Opening a new one ends the old one.
- **Narrow scope.** A token reaches one draft and nothing else. It can't publish, discard, generate, create fetch URLs, or read another script, a version or a generation.
- **Secret defaults never leave Proxier.** The default of every `@secret` parameter and of the `@fill subscription-link` one (a link URL holds the link's token) reads as `MASKED-BY-PROXIER`, a valid literal quoted or bare, in the draft, the check, computed values derived from it and the diff. A save puts the stored default back wherever a parameter of the same name still holds the mask; a real value written there replaces it. A mask on a parameter whose stored default isn't hidden (renamed, new, or never secret), or on one that can no longer hold the stored value (its quotes removed), refuses the save with `422`, so the mask never becomes a value.
- **ETags** are `"<draft revision>"`; every save bumps the revision.
- **Limits.** The script's own limits (512 KiB, UTF-8 without NUL bytes, not empty: `422` with the editor's message; a body over 1 MiB is `413`). 600 requests per session and 60 per minute, as for templates.
- **The prompt is a secret.** The token is shown once; reloading the answer to the form shows "Already copied" and opens nothing. A new hand-off makes a new session.

## Steps — versions

1. The script page lists versions: number, published, notes, generations made from it, and which one is current.
2. **View**, **Diff** (any two), **Download** (unfilled, exactly as written).
3. **Make current** points generation at an older version, e.g. after a bad release. → `routerscript.current_changed{from, to}`

## Parameter detection

Given this excerpt of today's script:

```
# PARAMETERS
# LAN /24 prefix (no trailing dot). Router gets .1, DHCP hands out .20-.254
:local lanNet "10.230.1"
# mihomo subscription URL (SUB1 env of the container)
# @fill subscription-link
:local subUrl "https://files....t"
...
# container internal /24: router side = .1, mihomo veth = .2 = the VPN gateway
:local containerNet "192.168.89"
:local vpnGateway ($containerNet . ".2")
...
:local dohForwarder "vpn-doh"
# END PARAMETERS

# PRECHECK RouterOS version. The template targets 7.24.5+ only ...
:local minVer 7024005
```

- The block runs from `# PARAMETERS` to `# END PARAMETERS`. Comments starting with capitals inside it (`# LAN …`, `# WAN …`, `# DOH_FORWARDER …`) are just descriptions.
- `lanNet` is a parameter: default `10.230.1`, description "LAN /24 prefix (no trailing dot). Router gets .1, DHCP hands out .20-.254".
- `subUrl` is a parameter filled from a subscription link.
- `containerNet` is a parameter.
- `vpnGateway` is **computed** (its value is an expression) and shown read-only.
- `minVer` is after the end marker, so it is ordinary code and not a parameter.

## Rules

- Versions are immutable and numbered without gaps. Generations always point to the exact version they were made from.
- Only literal values in the PARAMETERS block are parameters. `:local` lines elsewhere in the script are ordinary code.
- Annotations are comments, so a version with annotations still imports on a router unchanged.
- A version that generations were made from can't be deleted. Archiving the script hides it from the list. Its versions and generations stay.

## Edge cases (each is a test)

- Upload today's `fresh-router.rsc` (no end marker) → a warning on publish. The block runs into the PRECHECK section: `minVer` is detected as a parameter, and `ver` and `v` as computed values.
- A `# PARAMETERS` line followed only by a comment, with no end marker → a warning that the block has no `:local` line, not an empty list of last values ("…are .").
- The same file with `# END PARAMETERS` added after `dohForwarder` → no warning, 18 parameters and `vpnGateway` computed, with descriptions from the comments above each `:local`.
- `:local containerIface "container"` under the sub-heading comment `# interface names` → only the comment line directly above `lanIface` is a description. `containerIface` has none.
- Two `:local lanNet …` lines in the block → publishing refused, "duplicate parameter lanNet".
- `# @fil subscription-link` (typo) → publishing refused, "unknown annotation @fil".
- A script without `# PARAMETERS` → a warning, then a version with no parameters.
- Version 4 removes `vethName`, which version 3 had → a warning on publish.
- **Make current** on version 3 while 4 exists → new generations use version 3. Version 4 stays in the list.
- A bare literal `:local retries 3` inside the block → a parameter with default `3`.
- Hand-off with an empty problem, one over 4,000 characters or an unknown agent → refused, nothing recorded. Without a draft → back to the script page.
- Access asked for longer than 8 hours → 8 hours.
- A second hand-off on the same draft → the first ends (`replaced`) and its token gets `401`. If the first had expired unnoticed, it ends as `expired`.
- An ended, an expired, an unknown token, no token and a session cookie → the same `401` "session ended".
- `GET /draft` of a script with a `@secret` parameter and a `@fill subscription-link` default → both read `MASKED-BY-PROXIER`; the check and the context hold neither value.
- `PUT /draft` without `If-Match` → `428`; with a stale ETag → `412`; neither changes the draft.
- The agent changes `lanNet` and leaves the masks → the saved draft has the new `lanNet` and both real defaults; `draft_saved` has actor `agent:<id>`; the save is counted. The same text again → no event, not counted.
- The agent renames the masked parameter, puts the mask on a plain one, or removes its quotes → `422`, the draft unchanged.
- An empty, binary or larger than 512 KiB body → `422` with the editor's message; over 1 MiB → `413`.
- An LF body saved over a CRLF draft → the draft keeps CRLF.
- The admin saves while the agent edits → the agent's write with the old ETag gets `412`.
- `POST /draft/check` with a duplicate parameter → "Duplicate parameter lanNet.", nothing saved. A computed value built from a masked default shows the mask, not the value. A parameter of the base version gone → the warning.
- The diff of a draft that changed a secret default and `lanNet` → shows `lanNet` only.
- Publish, Discard, Revoke access → the session ends with that reason and the token gets `401`. Revoking a session through another script's page → `404`.
- Deleting the script → its token gets `401`.
- 60 requests in a minute → the 61st gets `429` with `Retry-After`; the next minute works again; the 601st request of a session → `429`.
- The posted hand-off form reloaded → "Already copied", no second session.
- The band shows the first line of the problem only; it and the poll go when the session ends; the poll answers the "draft changed" band after a save by the agent.
