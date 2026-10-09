# Script generation

**Actors**: Admin; router scripts module; subscriptions (`LinkIssuer`); routing (`RouterRegistrar`); the new router (through `GET /f/{token}`); the notifier.

Setting up a new MikroTik means filling in the current router script for it, getting the file onto the router, and importing it. Proxier fills it in from a form. It can create the router's subscription link (so the router's mihomo uses Proxier's servers) and register the router for domain sync, all in one go. The router can then fetch its file itself.

## Steps — generating

1. Script page → **Generate for a new router** (it uses the current version; the form's **Version** select offers the others, and a version page has **Generate from this version**). An archived script, or one without a version, can't generate (409 with the reason).
2. **Router name** (1–60, required): it names the file, the default link name and the registered router.
3. **Register for routing** (when the routing module is present), one of:
   - **Register a new router** (the default): routing list (the default first), host (empty means `<lanNet>.1` when the version has a `lanNet` parameter of three dotted numbers, shown as the placeholder and following the typed lanNet; without one the host is required), SSH user (`proxier`), port (22), jump host (none, one of the jump hosts routers already use, or **Another jump host…** with host, port and user), first hop through the tailnet. A jump host Proxier hasn't pinned gets the warning that the router waits until a Test connection confirms it (the probe never pins a jump host).
   - **Use a router already in Routing**: its name becomes the router name.
   - **Don't register**.

   With a router registered or chosen, parameters annotated `@fill routing-address-list` and `@fill routing-doh-forwarder` are filled from the router's names and locked.
4. **Subscription link** (a parameter annotated `@fill subscription-link`, when the subscriptions module is present), one of:
   - **Create a link** (the default): name (empty means "Router — <router name>") and subscription. The URL becomes the value. The link is created only at step 7.
   - **Use an existing link**: pick one, and its URL becomes the value. A disabled or expired link gets a warning: the router's mihomo gets the stub entry until it is enabled.
   - **Type a URL**: the parameter's own field, still secret.
5. **Parameters**: the version's parameters with their defaults and descriptions, grouped as in the script. `@choices` is a select; `@secret` and the subscription link are password fields, never prefilled (empty means the default). A parameter annotated `@fill proxier-ssh-key` gets Proxier's public key, always locked and not secret. Computed values are shown read-only with their value and what they derive from.
6. **On generate**, the live summary, says what will happen (create the link, register the router, write `<file>` and how many lines differ from the version), then every problem by field, or "Every value is a valid RouterOS literal.", and the warnings. It writes nothing.
7. **Generate**: every value is checked again (required values present, `@pattern` and `@choices` respected, values that can't be expressed as a RouterOS literal refused, the names free), then in **one transaction**:
   1. the generation row is inserted;
   2. the link is created through `LinkIssuer.Issue` if asked → `link.created`;
   3. the router is registered through `RouterRegistrar.Register` if asked (state **awaiting setup**) → `routing.router_added{by: routerscripts}`;
   4. both ports are asked even when one refuses (each writes nothing then), and every refusal shows on its field together; any refusal rolls everything back;
   5. the link's URL, the router's names and Proxier's key fill their parameters;
   6. the generation is saved: version, router name, values (secret ones and the link URL sealed), changed parameters, link and router, the key's fingerprint.

   → `routerscript.generated{script, version, router, link, registered, link_created}`
8. The generation page opens with **Download** and **Generate again** (and, from 4c, **Create fetch URL**).

The file is never stored: it is the version's body with the values filled in, made whenever it is downloaded or fetched. Its name is `<slug>-<router>-v<version>.rsc`, the router name's characters outside `A-Za-z0-9._-` turned into `-` (runs collapsed, trimmed; `router` when nothing is left).

**Generate again** opens the form for the same version with the generation's router name and values, the existing link and router chosen while they exist, and its secrets kept unless retyped.

The generation page says when the link's URL changed after the generation (its token was regenerated or it was deleted), or Proxier's SSH key did: the file holds the old one, so generate again.

## Steps — getting it onto the router

1. **Download** (signed-in admin) → `fresh-router-<router>-v<version>.rsc`, `no-store`. It records nothing (the request log has it).
2. **Create fetch URL** → a token valid for **1 hour** and **one** successful download. The page shows the URL, its expiry, and the commands to paste into the new router's terminal:

   ```
   /tool fetch url="https://proxier.tikhonnnnn.com/f/<token>" dst-path=fresh-router.rsc
   /import fresh-router.rsc
   ```

   → `routerscript.fetch_url_created{expires}`
3. The router requests the URL, and Proxier answers with the generation's body (`text/plain`). The token is used up. → `routerscript.fetched{ip, user_agent}` (notifies)
4. Any request after that, after the expiry, or with an unknown token → `404`.
5. An unused fetch URL that expired → `routerscript.fetch_url_expired`
6. After the import, the router has Proxier's user and key when the script used `@fill proxier-ssh-key`; otherwise the admin installs them with the commands the generation page shows. Routing's awaiting-setup probe then connects within 10 minutes, and the first sync runs ([router sync](../routing/router-sync.md)).

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
