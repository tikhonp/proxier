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
     - a PARAMETERS block without `# END PARAMETERS`, with the last detected parameters listed so you can see where it stopped;
     - a parameter that was in the version the draft is based on has disappeared ("vethName was in v3 and is gone here").

   **Publish vN…** saves the editor first, then opens the publish page: the errors with their lines, then the warnings, the notes, a tick "Publish with these warnings" when there are any, and what happens. On success the draft becomes version N+1 and the **current** version, the confirmed warnings are kept with it, and the first publish fixes the slug. → `routerscript.version_published{version, parameters, warnings}`
4. **Edit** creates a draft from the current version (or opens the one there is). **Save draft** keeps a revision: a save from a tab whose draft another tab saved meanwhile is refused with the band "The draft changed since you opened it (another tab saved it)." and **Reload**, and the typed text stays in the editor. **Discard draft** drops it; it needs a version (a script with none is deleted instead). → `routerscript.draft_saved{based_on}`, `routerscript.draft_discarded{based_on}`

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
- The same file with `# END PARAMETERS` added after `dohForwarder` → no warning, 18 parameters and `vpnGateway` computed, with descriptions from the comments above each `:local`.
- `:local containerIface "container"` under the sub-heading comment `# interface names` → only the comment line directly above `lanIface` is a description. `containerIface` has none.
- Two `:local lanNet …` lines in the block → publishing refused, "duplicate parameter lanNet".
- `# @fil subscription-link` (typo) → publishing refused, "unknown annotation @fil".
- A script without `# PARAMETERS` → a warning, then a version with no parameters.
- Version 4 removes `vethName`, which version 3 had → a warning on publish.
- **Make current** on version 3 while 4 exists → new generations use version 3. Version 4 stays in the list.
- A bare literal `:local retries 3` inside the block → a parameter with default `3`.
