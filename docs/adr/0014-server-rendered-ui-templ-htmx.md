# The UI is server-rendered with templ and htmx

The admin UI is rendered by the Go binary with **templ** components. Interaction uses **htmx**: partial page updates, form posts, and server-sent events for live job logs and step progress. A small amount of plain JavaScript, with no framework, covers what htmx can't:

- the keymap and the bottom key line;
- the search and actions pop-up (`/`, `:`) and the keys list (`?`);
- drag-to-reorder;
- the code editor, with optional vim keys;
- charts, drawn as inline SVG rendered on the server.

The design in [ui/design](../ui/design/README.md) is built for this. Its pages are mostly lists, forms and panels. Live data arrives as events. `tokens.css` is the base stylesheet as it stands.

We rejected a Go JSON API with a single-page app. It would add a second language, a build pipeline, client-side state that duplicates server state, and an API to version. All of that for one admin user on a home server. The pieces that need rich client behaviour are few and self-contained, and none of them needs a framework.

## Consequences

- One binary and one language. Pages work without JavaScript except the interactive pieces listed above, so a slow phone still gets a usable page.
- Every action is a normal form post or an htmx request with the CSRF token ([architecture](../architecture.md#http-surfaces)). The search pop-up's action list comes from the same action registry that renders the buttons, so a button and its action always share the same words.
- Live views (job logs, provisioning steps, syncs) use SSE endpoints per job. A reconnect resumes from the last line ID, so nothing is lost after a phone sleeps.
- The keyboard layer is a progressive enhancement. Every key maps to a link or button that is already on the page.
- Translations live in the Go i18n catalogs used by the templates. Nothing is translated on the client.
- If a screen ever needs heavy client state, it can become an island (a single JS module on that page) without changing the rest.
