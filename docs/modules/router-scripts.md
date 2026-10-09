# Router scripts

The router scripts module hosts `fresh-router.rsc` (and any other RouterOS setup script) in versions. It fills in a version's parameters for one new router and hands the result to the router: as a download, or through a short-lived fetch URL the router pulls with `/tool fetch`. It is separate from routing because it is about setting up a router once, not keeping it in sync. It can still create the router's subscription link and register the router for sync in the same step.

## Scripts and versions

- A **router script** has a name, a slug and a description, and holds **versions**. Versions are immutable and numbered. Editing happens in a **draft** based on the current version, and **Publish** creates the next one. The latest published version is the **current** one, and **Make current** can point back at an older one.
- The editor has RouterOS syntax highlighting. It shows the detected parameters next to the text as it is edited, and a diff against any other version.
- Any version can be downloaded unfilled, exactly as written.
- The first script is created by uploading today's `fresh-router.rsc`.
- A version stores its body byte for byte (and its SHA-256); nothing derived from it is stored except the warnings confirmed when it was published. Parameters are parsed from the body whenever they are needed.
- The slug names the downloaded files, so it is fixed once the first version exists. A script can be deleted only while no generation was made from it; otherwise it is archived (hidden from the list and search).
- **Line endings.** A pasted body gets LF (browsers send a text area with CRLF). An uploaded file is kept byte for byte. Saving from the editor gives the posted text the line endings of the body it replaces (CRLF when its first line ends with CRLF), so a save never changes every line of a script.

Flows: [script versions](../processes/router-scripts/script-versions.md).

## Parameters

Parameters are **detected from the script itself**, so the file stays a valid, importable RouterOS script with no template syntax ([ADR 0011](../adr/0011-router-script-parameters-from-local-block.md)):

- The **PARAMETERS block** starts at the first line that is `#`, optional spaces, `PARAMETERS` (case-sensitive, nothing else on the line: the header's "Edit the PARAMETERS section" isn't one) and ends at the next `# END PARAMETERS` line. If the end marker is missing, the block ends just before the first line that is neither a comment, a blank line nor a `:local` line, and publishing warns, naming the line where it stopped and the last three names read, because such a block can run into the next section. Today's script needs the one-line marker added after `dohForwarder`; without it the block would run on into the PRECHECK section and pick up `minVer` (and `ver`, `v` as computed values). Lines are read with `\r\n` or `\n` as their terminator, which is kept when the file is filled.
- Inside it, each line `:local <name> <literal>` is a **script parameter**. The literal is a quoted string (`"10.230.1"`, with RouterOS's escapes `\" \\ \$ \? \n \r \t \_ \a \b \f \v` and `\XX` in capital hex) or a bare word (`[A-Za-z0-9._:/+-]+`: `7024005`, `true`, `ether1`). The literal's value is its default.
- A `:local` whose value isn't one literal (an expression such as `($containerNet . ".2")`, a `[command]`, `$name`, a literal followed by more, or nothing) is a **computed value**, not a parameter. It is shown read-only with its expression, so the admin can see what it derives from. A bare `$name` or a parenthesised concatenation (`.`) of strings and names set earlier in the block also shows its value (`vpnGateway` → `192.168.89.2`); anything else shows none.
- The comment lines directly above a parameter (no blank line in between), without `#` and one space, the annotation lines and the `# PARAMETERS` line left out, are its **description**.
- Blank lines split the block into **groups**. When the only description in a group belongs to its first item, the panel and the form show it as the group's heading ("interface names" over `lanIface`, `containerIface`, `vethName`); it still belongs to that parameter.
- Optional annotations in the description refine the form field:

| Annotation | Effect |
|---|---|
| `# @secret` | Masked in the form. Stored encrypted in the generation. |
| `# @choices a\|b\|c` | A select instead of a text field. |
| `# @pattern <regex>` | Validated before generating. |
| `# @fill subscription-link` | The field offers "create a Proxier link for this router" or "pick an existing link". |
| `# @fill routing-address-list` | Filled from the router's routing target (`to_vpn_list` by default) and locked to it. |
| `# @fill routing-doh-forwarder` | Filled from the router's routing target (`vpn-doh` by default) and locked to it. |
| `# @fill proxier-ssh-key` | Filled with Proxier's public SSH key and locked to it, so the script can install the key itself. |
| `# @required` | The field can't be left empty. |

What a publish shows:

- **Errors** (publishing is refused): a duplicate name in the block; a `:local` line that can't be read (no name, or a quoted literal with an escape RouterOS doesn't know); a second `# PARAMETERS` line before the end; an unknown annotation (`@fil`); a malformed one (text after `@secret` or `@required`, `@choices` with no, empty or repeated values, a `@pattern` that doesn't compile, `@fill` without a known kind); the same annotation twice on one parameter; one `@fill` kind on two parameters; `@fill` on a bare literal or together with `@choices`; a non-empty default that isn't one of its `@choices`.
- **Warnings** (publishing needs a tick, and they are kept with the version): no PARAMETERS block; no end marker; a line inside an ended block that is neither a comment nor a `:local`; an end marker with no block before it; annotations above a computed value or followed by a blank line or the end marker; a default that doesn't match its `@pattern`; a parameter of the version the draft is based on that is gone.

Annotation lines outside the block are ordinary comments.

`@fill proxier-ssh-key` makes zero-touch registration possible: the parameter gets Proxier's public key line, and the script's own lines can create Proxier's restricted user and add the key (the commands are routing's `routeros.KeyCommands`), so the awaiting-setup probe connects with no manual step.

Annotations are comments, so RouterOS ignores them. Today's `fresh-router.rsc` already gives these parameters: `lanNet`, `wanIface`, `subUrl`, `image`, `timeZone`, `lanIface`, `containerIface`, `vethName`, `lanList`, `wanList`, `vpnList`, `vpnTable`, `vpnMark`, `containerDisk`, `containerNet`, `dohHost`, `dohIP`, `dohForwarder`. The computed one is `vpnGateway`. Adding `@fill subscription-link` above `subUrl`, `@fill routing-address-list` above `vpnList` and `@fill routing-doh-forwarder` above `dohForwarder` connects it to the other modules.

## Generations

A **generation** is a version filled in with one router's values (`generations` package). Generating (**Generate for a new router** on the script page, from the current version unless another is chosen):

1. **Router name** (1–60): it names the file, the default link and the registered router.
2. **Register for routing** (only with the routing module): register a new router (routing list, host, SSH user and port, a jump host picked from the ones routers already use, or another one, and the tailnet first hop), use a router already in Routing, or don't register. A new router's host defaults to `<lanNet>.1` when the version has a `lanNet` parameter of three dotted numbers. It is created in **awaiting setup**, so it raises no alarms before it exists. A jump host Proxier hasn't pinned gets a warning: the router waits until a Test connection confirms it.
3. **Subscription link** (only with the subscriptions module and a `@fill subscription-link` parameter): create a link (named "Router — <router name>" unless typed, in a chosen subscription), use an existing one (a disabled or expired one gets a warning), or type a URL. The link URL becomes the value. The router's mihomo container (`SUB1`) then uses Proxier's servers.
4. **Parameters**, grouped as in the script with their headings and descriptions, prefilled with their defaults; `@choices` is a select, `@secret` (and the subscription link) a password field that is never prefilled (empty keeps the default, or, on **Generate again**, the earlier generation's value). Locked fields show their value and source: the `@fill routing-*` ones while a router is registered or chosen, the link while one is created or chosen, and `@fill proxier-ssh-key` always (Proxier's public key line, not secret). Computed values show their value and expression.
5. The live summary says what generating will do: create the link, register the router, write `<slug>-<router>-v<n>.rsc` with how many lines differ from the version, then every problem by field and the warnings.
6. Generating checks every value again, then creates the link (`LinkIssuer.Issue`), registers the router (`RouterRegistrar.Register`) and saves the generation **in one transaction**: both ports are asked even when one refuses, their refusals show on the form together, and nothing is created unless everything is. The router's names, the link's URL and Proxier's key then fill their parameters, and `routerscript.generated` is recorded.

A generation stores its values, never its file: the plain values as JSON, the `@secret` ones and the link URL sealed (`generation:<id>:secrets`), the changed parameters, the link and the router by id and name, and the key's fingerprint. The file is `params.Fill(the version's body, the values)` whenever it is downloaded (or fetched, 4c): byte-identical to the version except the literals of the changed parameters.

The **generation page** shows the script and version (and when a newer one is current), the router and the link as they are now (linked; "removed", "deleted" or "not registered"), the file with **View changes** (the version against the file, every secret literal shown as `•••`), the values (changed first, all in a fold, secrets `•••`), and its activity. A band says when the link's URL (token regenerated, link deleted) or Proxier's SSH key changed after the generation, since the file still holds the old one. **Generate again** opens the form with the same version, router name and values, the existing link and router chosen (so nothing is created twice), and the secrets kept unless retyped.

Then:

- **Download** gives `fresh-router-<router>-v<version>.rsc`. It needs a signed-in admin.
- **Create fetch URL** gives a URL that works **once** and for **1 hour**, plus the commands to paste into the new router:

  ```
  /tool fetch url="https://proxier.tikhonnnnn.com/f/<token>" dst-path=fresh-router.rsc
  /import fresh-router.rsc
  ```

  The first successful download uses it up. Later requests get `404`. Each download is recorded (IP, user agent) and sends a notification.

A generation holds the router's subscription link, so the file is a secret. That is why fetch URLs are single-use and short-lived, and why downloads need a session.

Flows: [script generation](../processes/router-scripts/script-generation.md).

## Contract with routing

The router script installs the routing infrastructure: the address list, mangle marks, the routing table, the DoH forwarder, and the `mtvpn:` infra pins. Router sync fills the address list and the DNS entries. The names must agree on both sides ([ADR 0010](../adr/0010-router-contract-stays-mtvpn-compatible.md)):

| Script parameter | Router field | Default |
|---|---|---|
| `vpnList` | address list | `to_vpn_list` |
| `dohForwarder` | DoH forwarder | `vpn-doh` |

With the `@fill routing-*` annotations, a generation that registers a router always uses that router's names, so the two can't drift apart.

## Pages

- **Router scripts**: each script with its current version, last published, generations count.
- **Script page**: versions (number, published, notes; **Make current**, **Download**, **Diff**), the draft editor with detected parameters, **Generate for a new router**, and its generations (router name, version, date, the router's state now).
- **Generate** (`/router-scripts/:id/generate`): the form above with its live summary.
- **Generation page** (`/router-scripts/generations/:id`): version, the router and link as now, values (secrets masked), **View changes**, **Download**, **Generate again**; from 4c **Create fetch URL**, After the import and fetch history.

## Events

`routerscript.*`: see [events](../events.md#router-scripts).
