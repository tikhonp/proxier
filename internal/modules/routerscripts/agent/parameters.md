## Parameters

Parameters are **detected from the script itself**, so the file stays a valid, importable RouterOS script with no template syntax ([ADR 0011](../../../../docs/adr/0011-router-script-parameters-from-local-block.md)):

- The **PARAMETERS block** starts at the first line that is `#`, optional spaces, `PARAMETERS` (case-sensitive, nothing else on the line: the header's "Edit the PARAMETERS section" isn't one) and ends at the next `# END PARAMETERS` line. If the end marker is missing, the block ends just before the first line that is neither a comment, a blank line nor a `:local` line, and publishing warns, naming the line where it stopped and the last three names read, because such a block can run into the next section: in `fresh-router.rsc` without the marker, it runs on into the PRECHECK section and picks up `minVer` (and `ver`, `v` as computed values). Lines are read with `\r\n` or `\n` as their terminator, which is kept when the file is filled.
- Inside it, each line `:local <name> <literal>` is a **script parameter**. The literal is a quoted string (`"10.230.1"`, with RouterOS's escapes `\" \\ \$ \? \n \r \t \_ \a \b \f \v` and `\XX` in capital hex) or a bare word (`[A-Za-z0-9._:/+-]+`: `7024005`, `true`, `ether1`). The literal's value is its default.
- A `:local` whose value isn't one literal (an expression such as `($containerNet . ".2")`, a `[command]`, `$name`, a literal followed by more, or nothing) is a **computed value**, not a parameter. It is shown read-only with its expression, so the member can see what it derives from. A bare `$name` or a parenthesised concatenation (`.`) of strings and names set earlier in the block also shows its value (`vpnGateway` → `192.168.89.2`); anything else shows none.
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

Annotations are comments, so RouterOS ignores them. In `fresh-router.rsc`, `@fill subscription-link` sits above `subUrl`, `@fill routing-address-list` above `vpnList`, `@fill routing-doh-forwarder` above `dohForwarder` and `@fill proxier-ssh-key` above `proxierKey`; its one computed value is `vpnGateway`.
