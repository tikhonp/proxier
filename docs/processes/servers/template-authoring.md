# Template authoring

**Actors**: Admin; Proxier (validators).

A template defines a server's stack ([servers module](../../modules/servers.md#templates)). This flow covers writing it, checking it before it can reach a server, and keeping its versions. A version that fails validation can't be published. A published version can't change.

## Steps — creating and editing

1. Servers → Templates → **New template**: name, slug (from the name, editable until the first publish), description. An empty draft opens with a manifest skeleton. → `template.created`
2. The editor shows a file tree (`manifest.yaml` plus files) and a code editor with syntax highlighting by file type. **Add file**, **Rename**, **Delete**, **Upload** (a single file or a zip into the tree).
3. **Save draft** keeps the draft. There is at most one draft per template, and it records which version it was based on.
4. **Validate** runs every check below and shows a report: per file ok / warning / error, with messages and line numbers. It changes nothing.
5. **Preview** renders the draft for the sample context, or for a chosen active server, and shows the rendered files with secrets masked.
6. **Publish** asks for version notes, then validates again. With errors, it shows the report and publishes nothing. With only warnings, it shows them and asks to confirm. On success, the draft becomes version N+1 and is gone. → `template.version_published{version, warnings}`
7. **Edit** on a template with no draft creates one from the latest version.
8. **Discard draft** deletes the draft after a confirmation.

## Steps — versions

1. The template page lists versions: number, published, notes, and the number of servers (by lifecycle state) built from each.
2. **View** a version (read-only), **Diff** two versions (file by file), **Export** a version as a zip.
3. **Make default** sets the version used for new servers. Servers built from older versions show "update available". → `template.default_version_changed{from, to}`
4. **New draft from this version** replaces the draft with an older version's content, for reverting. Publishing it creates a new number, so history only grows.

## Steps — import

1. **Import zip**: the zip must contain `manifest.yaml` at its root or in exactly one top-level folder. Its files become a draft (of a new template, or of an existing one after a confirmation that replaces the draft).
2. **Import from git**: an HTTPS repository URL, a ref (branch, tag or commit) and a path. Proxier downloads the ref's archive, takes the path's contents, and makes them a draft as above. The draft records where it came from (URL, ref, commit).
3. Either way the draft is validated like any edit before it can be published.

## Steps — handing the draft to an agent

A coding agent on the admin's machine (Claude Code, Codex or similar) can fix a draft remotely. Proxier never runs the agent. It gives the agent a prompt and a short-lived key that only reaches this draft.

1. In the editor or on the template page, the admin chooses **Hand off to an agent…** and fills in:
   - what the problem is and what the agent should do, as free text;
   - which context to include:
     - the latest validation report;
     - a failed job's log (offered when a job built from this template failed);
     - a preview for a chosen server;
     - the diff against the base version;
   - the agent (Claude Code, Codex, other), which changes only the wording;
   - how long access lasts: 30 min, 2 h (the default) or 8 h.
2. Proxier creates an **agent session** and returns:
   - a token (`pxa_…`), stored only as a hash;
   - a Markdown prompt to **Copy** and paste into the agent; there is no download. It holds the problem text, the base URL, the token, the API below and the rules. → `template.agent_session_opened{template, expires_at, context}`
3. The agent works through the agent API. Each request is checked against the session's scope. The changes it makes are recorded with "by the agent" as the actor:
   - a save → `template.draft_saved{by: agent}`;
   - a validation → `template.draft_validated{by: agent}`.
4. While a session is open, the template page and the editor show a band: who opened it and for what, how many saves and validations, when it expires, **What it changed** (the activity filtered to this session) and **Revoke access**.
5. The session ends on expiry, on **Revoke access**, when the draft is published or discarded, or after the 8-hour maximum. A request after that gets `401` with "session ended". → `template.agent_session_closed{reason, saves}`
6. The admin reviews the draft (diff against the base version, validation, preview) and publishes as usual. Publishing is never done by the agent.

The **agent API** lives under `/agent/v1`. It accepts only an agent token, never a session cookie.

| Method and path | Does |
|---|---|
| `GET /context` | The problem text, the chosen context (report, redacted job log), the template's name, base version and links. |
| `GET /draft` | The manifest and the file list with ETags. |
| `GET /draft/files/{path}` | One file, with its ETag. |
| `PUT /draft/files/{path}` | Write a file. Requires `If-Match`; a mismatch gets `412` "the draft changed". |
| `DELETE /draft/files/{path}` | Delete a file (`If-Match` as above). |
| `POST /draft/validate` | Run validation and return the report. |
| `POST /draft/preview?server=<name>` | Render for one of the servers allowed in this session, secrets masked. |
| `GET /docs/manifest` | The manifest reference and the validator rules. |

Rules:

- **One open session per draft.** Opening a new one revokes the old one.
- **Narrow scope.** A token reaches one draft and nothing else. It can't:
  - publish, discard or import;
  - deploy, or preview for a server it wasn't given;
  - read a secret value: generated values and secret parameters render as `•••`, and job logs are the redacted ones.
- **Rate limit.** 600 requests per session, and 60 per minute. Files keep the editor's size limit.
- **Editing alongside the agent.** The admin's open editor gets "the draft changed" after an agent save and offers to reload, the same as two browser tabs.
- **The prompt is a secret.** The dialog says so, and the token is shown masked in its preview. Copying it again later isn't possible: the token is shown once. A new hand-off makes a new session.

## Validation

Validation renders the template for a **sample context** and checks every file. It runs inside Proxier's own container with bundled tools and Go libraries. It never starts containers and never uses a Docker socket ([ADR 0009](../../adr/0009-embedded-xray-core-no-docker-socket.md)).

Sample context: server `xx-1` in location `xx` ("Sample"), IP `192.0.2.10`, both hostnames `xx-1.hosts.example.invalid`, parameters from each parameter's `sample` (or `default`), generated values freshly generated, `.Clients` with one sample client.

| Check | Error when | Warning when |
|---|---|---|
| Manifest schema | A required field is missing; an unknown step, check, endpoint type, generated kind or validator; duplicate keys; a step refers to a missing file; `dir` is not an absolute path, or is `/`; a bad port spec. | `requires` is missing. |
| Parameters | A required parameter has neither `sample` nor `default`. | — |
| Rendering | Any template error, including a missing key, in a file, step, endpoint or check. | — |
| Endpoints | Rendered fields are invalid for the endpoint type (e.g. a `vless-xhttp-tls` path without a leading `/`, or a port outside 1–65535); endpoint keys are not unique. | An endpoint key used by the previous version has disappeared: servers upgrading will lose that endpoint from their subscriptions. |
| `xray` | The rendered JSON isn't valid, or xray-core refuses to build an instance from it. | — |
| `compose` | compose-go can't parse it with the rendered `.env`, or it has no services. | An image has no tag, or the tag `latest`. |
| `nginx` | Bundled `nginx -t` fails on a sandboxed copy (see below). | — |
| `json`, `yaml` | Syntax error. | — |
| `shell` | `bash -n` fails. | — |
| Size | A file is over 1 MB, or the version over 10 MB. | — |

The nginx sandbox copies the rendered nginx files into a temporary directory and adjusts only what can't exist outside a real server. `*.template` files are first expanded with the environment the compose file gives the nginx service, as the nginx image does at start. `ssl_certificate` and `ssl_certificate_key` point to a throwaway self-signed pair. Hostnames of other compose services in `*_pass` directives resolve to `127.0.0.1`. Everything else is tested as written.

## Rules

- Versions are immutable and numbered without gaps per template.
- A version used by any server, retired ones included, can't be deleted. A template that has never built a server can be deleted, with all its versions.
- **Archive** hides a template from the new-server form. It changes nothing for existing servers, and can be undone. → `template.archived`
- Endpoint keys must stay stable between versions. Subscriptions refer to servers, and the output includes each server's endpoints by key ([servers module](../../modules/servers.md#manifest)).
- The seed template "VLESS XHTTP behind nginx" is created on first start as version 1 and is an ordinary template from then on.

## Edge cases (each is a test)

- A draft where `steps.install` runs `./issue-cert.sh` but no such file exists → error on that step.
- A required parameter `letsencrypt_email` without `sample` or `default` → error "give a sample value".
- `xray-config.json` uses `{{ .Gen.client_uid }}` (typo) → render error naming the missing key and the line.
- `xray-config.json` renders to JSON that xray rejects (e.g. unknown `network`) → error from the xray validator.
- `compose.yaml` with `image: ghcr.io/xtls/xray-core:latest` → warning; publishing still allowed after confirmation.
- The seed nginx template with `${SERVER_DOMAIN}` and `grpc_pass grpc://xray:10001` → nginx validation passes in the sandbox.
- An nginx file with a missing `;` → error with nginx's message and line.
- Publishing with errors → no version is created, and the draft is unchanged.
- Publishing → version N+1 exists and the draft is gone. Editing again → a new draft based on N+1.
- Version 2 drops endpoint key `main` that version 1 had → warning on publish.
- Delete a template whose version 1 built a retired server → refused. Archive offered instead.
- Import a zip with `manifest.yaml` nested two folders deep → refused with "manifest.yaml not found at the root".
- Import from git at a commit SHA → draft records that SHA. Re-importing the same ref later → a new draft with the new commit recorded.
- A file of 1.2 MB → error. Ten files of 0.9 MB → error on the version total.
- Two browser tabs editing the same draft → the second save is refused with "the draft changed since you opened it".
