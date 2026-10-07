# Shadowrocket (hosted config target)

The hosted Shadowrocket config is mtvpn's `shadowrocket` command, served at a URL instead of uploaded to copyparty ([Shadowrocket config](../processes/routing/shadowrocket-config.md)).

## Output

1. The **base config** verbatim: your hand-written file, with `[General]`, DNS, upstream `RULE-SET`s, `[Proxy Group]` and so on.
2. Its `[Rule]` section (case-insensitive header) gets a block inserted:
   - before the first `FINAL,` line in the section, if there is one, because Shadowrocket stops at the first matching rule and anything after `FINAL` is dead;
   - otherwise at the end of the section, followed by `FINAL,DIRECT`, and a blank line if a section header comes right after;
   - in both cases above any trailing blank lines of the section.
3. The block:

   ```

   # Services from routing list "Main", by Proxier (2026-10-05 04:12 UTC)

   # anthropic
   DOMAIN-SUFFIX,anthropic.com,PROXY
   DOMAIN-SUFFIX,claude.ai,PROXY

   # youtube
   DOMAIN-SUFFIX,youtube.com,PROXY
   DOMAIN,youtubei.googleapis.com,PROXY
   ```

   - One `# <tag>` comment per service, in list order, with no block for a service left with no rules.
   - `DOMAIN-SUFFIX,<domain>,<policy>` for suffix domains and `DOMAIN,<domain>,<policy>` for exact ones, sorted within the service.
   - Ownership is applied first ([routing lists](../processes/routing/routing-lists.md#rules)): a name covered by another service's broader suffix is dropped, and a name several services share stays with the first. These are the rules mtvpn's `shadowrocket_rules` used.
   - The policy defaults to `PROXY` and is set per config (e.g. a proxy group's name).
4. Served as `text/plain; charset=utf-8` with `Cache-Control: no-store`.

## On the phone

Shadowrocket → Config → add the URL, then select it. Shadowrocket refreshes the config from the URL. The proxies themselves come from the phone's subscription link ([subscriptions](../modules/subscriptions.md)). The config only decides which domains use `PROXY`.

## Validation of the base config

- It must contain a `[Rule]` section. Otherwise saving is refused.
- Nothing else is interpreted. Every other line is copied byte for byte, so a broken base is the admin's to fix, and **Preview** shows exactly what the phone gets.
