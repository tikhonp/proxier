# Every name a service lists reaches the router, even under another's suffix

Supersedes the coverage part of [ADR 0013](./0013-one-owner-service-per-domain.md): Proxier no longer drops a name because another service in the list has a suffix above it, and a custom service no longer absorbs its own names under its suffixes. A router installs every name its services list, for every service, upstream or custom. Only the same name is still given to one owner: in the same form, the first service in list order; as both a suffix and an exact name, the suffix.

A suffix entry doesn't make a MikroTik resolve the names under it. The router resolves only the names it is given: each address-list hostname entry is resolved by the router itself, and each FWD entry puts what it resolves into the address list. A subdomain the admin adds on purpose, as in the domain-list files mtvpn installed, is there so the router fetches its addresses too, for example for a client that doesn't ask the router's DNS. mtvpn never dropped such names on routers; it only dropped them from the Shadowrocket export. Proxier had applied that export's rule to routers as well, so a deliberately added `console.anthropic.com` never reached the router while `anthropic.com` was in another service.

The same name stays with one owner because a router holds one entry per name: the service block removes every other entry with its names, so two tags claiming one name would take it from each other on every sync ([RouterOS integration](../integrations/routeros.md#service-block)).

## Consequences

- A list installs more entries: each name under another service's suffix is its own pair of entries. They are harmless (the same forwarder, the same address list) and they make the router resolve those names.
- Reordering services, or removing the service with the broader suffix, no longer moves those names: only names shared in the same form (and an exact name a suffix of the same name replaces) change owner.
- The Shadowrocket config is unchanged: it still drops every rule another rule of the config covers, since Shadowrocket's `DOMAIN-SUFFIX` already matches the names under it.
- The service page lists a name as not installed only when another service installs the same name, the guard leaves it out, or it is a router's infra pin. The custom editor says which rows another service installs instead, and which are installed though another service's suffix covers them too.

## Rejected

- **Only custom services' names**: the admin's own lists kept as URL sources by the mtvpn import, and upstream lists, have such names too, and mtvpn installed them all.
- **A per-name "force" flag**: a schema change and a choice to make on every name, for what is the right default.
