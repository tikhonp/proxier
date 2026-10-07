# Script generation

**Actors**: Admin; router scripts module; subscriptions (`LinkIssuer`); routing (`RouterRegistrar`); the new router (through `GET /f/{token}`); the notifier.

Setting up a new MikroTik means filling in the current router script for it, getting the file onto the router, and importing it. Proxier fills it in from a form. It can create the router's subscription link (so the router's mihomo uses Proxier's servers) and register the router for domain sync, all in one go. The router can then fetch its file itself.

## Steps — generating

1. Script page → **Generate for a new router** (it uses the current version, and the form offers older ones).
2. The form lists the version's parameters with their defaults and descriptions, grouped as in the script. Computed values are shown read-only with what they derive from.
3. **Subscription link** (a parameter annotated `@fill subscription-link`), one of:
   - **Create a link**: name (default "Router — <router name>") and subscription. The URL becomes the value. The link is created only at step 6.
   - **Use an existing link**: pick one, and its URL becomes the value.
   - **Type a value**: any URL.
4. **Register for routing** (optional, when the routing module is present): router name, routing list, host (default `<lanNet>.1`), port, user, jump host. Parameters annotated `@fill routing-address-list` and `@fill routing-doh-forwarder` are then filled from the router's names and locked.
5. **Validation**: required values present, `@pattern` and `@choices` respected, values that can't be expressed as a RouterOS literal refused.
6. **Generate**:
   1. Create the link through `LinkIssuer` if asked → `link.created`
   2. Register the router through `RouterRegistrar` if asked (state **awaiting setup**) → `routing.router_added`
   3. Write the body: every parameter line in the PARAMETERS block gets its new literal, quoted and escaped for RouterOS (`\"`, `\\`, `\$`). Every other byte is copied from the version.
   4. Save the generation: version, router name, values (secret ones and the link URL encrypted), link, router.

   → `routerscript.generated{version, router, link}`
7. The generation page opens with **Download** and **Create fetch URL**.

## Steps — getting it onto the router

1. **Download** (signed-in admin) → `fresh-router-<router>-v<version>.rsc`.
2. **Create fetch URL** → a token valid for **1 hour** and **one** successful download. The page shows the URL, its expiry, and the commands to paste into the new router's terminal:

   ```
   /tool fetch url="https://proxier.tikhonnnnn.com/f/<token>" dst-path=fresh-router.rsc
   /import fresh-router.rsc
   ```

   → `routerscript.fetch_url_created{expires}`
3. The router requests the URL, and Proxier answers with the generation's body (`text/plain`). The token is used up. → `routerscript.fetched{ip, user_agent}` (notifies)
4. Any request after that, after the expiry, or with an unknown token → `404`.
5. An unused fetch URL that expired → `routerscript.fetch_url_expired`
6. After the import, the admin installs Proxier's key on the router (until [zero-touch registration](../../open-questions.md) exists). Routing's awaiting-setup probe then connects within 10 minutes, and the first sync runs ([router sync](../routing/router-sync.md)).

## Rules

- A generation is immutable. Changing a value means generating again, which makes a new generation. A link or router registered by the first one is reused if chosen, never duplicated without asking.
- A generation contains the router's subscription link, so the file is a secret. Downloads need a session. Fetch URLs live 1 hour and work once. Neither the token nor the file content ever appears in a log or event.
- Only parameter literals are changed. A generated file is byte-identical to its version everywhere else.
- Creating a fetch URL invalidates any earlier unused fetch URL of the same generation.
- If the router scripts module runs without subscriptions or routing, those form sections are absent and the values are typed by hand.

## Edge cases (each is a test)

- Generate with all defaults except `lanNet "10.40.1"` → only the `lanNet` line differs from the version.
- A value containing `"` and `$` (`a"b$c`) → written as `"a\"b\$c"`.
- **Create a link** in "Family" → link "Router — Parents" created, and its URL is `subUrl`'s value.
- **Register for routing** → router "Parents" in awaiting setup, with `vpnList` and `dohForwarder` locked to its names.
- A required parameter left empty → refused, nothing created (no link, no router).
- **Create fetch URL**, router fetches → `200` with the body, `routerscript.fetched` notification. A second fetch → `404`.
- A fetch URL never used → `404` after 1 hour, and `routerscript.fetch_url_expired`.
- A second **Create fetch URL** → the first URL stops working.
- **Download** without a session → redirected to sign-in.
- Generating twice for "Parents" and choosing the existing link and router → no duplicates created.
- Routing module disabled → no "register for routing" section, and `vpnList` is an ordinary field.
