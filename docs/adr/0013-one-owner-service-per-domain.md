# Within a routing list, every domain belongs to exactly one service

When two services in a routing list share a name, Proxier assigns it to one owner. A name several services share in the same form goes to the first of them in list order. A name under another service's broader suffix was dropped too, until [ADR 0016](./0016-every-listed-name-reaches-the-router.md) superseded that part: it is installed, and only an exact name that another service has as a suffix is dropped for that suffix. Each tag is then installed with exactly its owned names. mtvpn installed services one after another, and each service's block took over the shared names, so the last service installed won. Removing that service then left the name missing on the router until the other service happened to be updated again. With explicit ownership, removing, reordering or editing a service recomputes it and re-syncs every tag whose names changed, so a shared name is never lost. The coverage rules mtvpn applied to the Shadowrocket export were first used for routers too; ADR 0016 keeps them for the export only.

## Consequences

- One change to a list can touch several tags on a router (the old and the new owner). The sync pushes tags that gain names first, so a name moving between tags is never missing.
- The service page has to show "owned elsewhere" for names a service lists but doesn't install, or the counts look wrong.
