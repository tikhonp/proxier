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

Annotations are comments, so RouterOS ignores them. Today's `fresh-router.rsc` already gives these parameters: `lanNet`, `wanIface`, `subUrl`, `image`, `timeZone`, `lanIface`, `containerIface`, `vethName`, `lanList`, `wanList`, `vpnList`, `vpnTable`, `vpnMark`, `containerDisk`, `containerNet`, `dohHost`, `dohIP`, `dohForwarder`. The computed one is `vpnGateway`. Adding `@fill subscription-link` above `subUrl`, `@fill routing-address-list` above `vpnList` and `@fill routing-doh-forwarder` above `dohForwarder` connects it to the other modules.

## Generations

A **generation** is a version filled in with one router's values. Generating:

1. The form lists the parameters with their defaults, descriptions and annotations.
2. **Subscription link** (`@fill subscription-link`): create a new link "Router — <name>" in a chosen subscription, or pick an existing one. The link URL becomes the value. The router's mihomo container (`SUB1`) then uses Proxier's servers.
3. **Register for routing** (optional): name, routing list, and how Proxier will reach it (its LAN address, e.g. `<lanNet>.1`, plus a jump host). The router is created in **awaiting setup**, so it raises no alarms before it exists.
4. Proxier rewrites only the literal of each parameter line in the PARAMETERS block, quoting and escaping for RouterOS strings. Every other byte is copied from the version.
5. The generation is saved, immutable. Values marked `@secret` and the link URL are encrypted.

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
- **Script page**: versions (number, published, notes; **Make current**, **Download**, **Diff**), the draft editor with detected parameters, **Generate for a new router**, and its generations.
- **Generation page**: version, values (secrets masked), linked link and router, **Download**, **Create fetch URL**, fetch history.

## Events

`routerscript.*`: see [events](../events.md#router-scripts).
