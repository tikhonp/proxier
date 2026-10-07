# Subscription management

**Actors**: Admin; subscriptions module; servers (through `EndpointCatalog` and its events).

A subscription is the set of servers a group of links gets: "Family", "Friends", "Me — everything". The admin chooses and orders the servers, and sets how the output is written and whether broken servers disappear from it on their own. Links then just point at a subscription ([link lifecycle](./link-lifecycle.md)).

## Steps — creating and editing

1. Subscriptions → **New subscription**: name, title (prefilled with the name), description. → `subscription.created`
2. **Servers**: **Add servers** lists the active servers that aren't in it yet (flag, name, health), with multi-select. Rows can be dragged to reorder. Each row shows health and whether it is hidden right now, with **Remove**. → `subscription.servers_changed{added, removed, reordered}`
3. **Settings**:
   - **formats** (allowed and default);
   - **update interval** in hours;
   - **hide unhealthy servers**: off, or on with the states (default `blocked` and `down`) and grace period (default 30 min);
   - **add new servers automatically**.

   → `subscription.updated{changes}`
4. **Preview** shows exactly what a link of this subscription would receive now: the headers and the connection URIs (credentials masked until revealed), with hidden servers listed and the reason.
5. **Delete** is offered only when no link points to the subscription. Otherwise the dialog lists the links and offers **Move links to…**. → `subscription.deleted`

## Steps — reactions to servers

1. `server.activated` → every subscription with **add new servers automatically** appends the server at the end. → `subscription.servers_changed{added, auto: true}`
2. `server.retired` → the server is removed from every subscription. → `subscription.servers_changed{removed, reason: retired}`
3. `server.redeployed` / `server.credentials_rotated` → nothing to do: outputs are computed on every fetch from the current endpoints.
4. `server.health_changed` → nothing is stored. Hiding is decided at fetch time from the state and "since".
5. A fetch where hiding would leave no servers raises `subscription.all_unhealthy` (at most once per subscription per hour) and serves every server ([fetch](./subscription-fetch.md)).

## Rules

- Names are unique. Titles don't have to be.
- Only active servers can be added. A server keeps its position until removed. Removed servers don't leave gaps.
- A server can be in many subscriptions. Its connection URIs are the same in all of them (credentials are per endpoint, [ADR 0004](../../adr/0004-one-shared-credential-per-endpoint.md)).
- The default format must be one of the allowed formats, and at least one format is allowed.
- Changes take effect on each link's next fetch. There is nothing to push to apps.
- "Add new servers automatically" only affects servers activated after it was turned on.

## Edge cases (each is a test)

- Create "Family" with nl-1 and de-1 → the preview shows two connection URIs in that order, with the title "Family".
- Drag de-1 above nl-1 → the next fetch serves de-1 first.
- Add a failed server → not offered in the list.
- `nl-2` activated while "Me" has "add new servers automatically" on → appended to "Me" only.
- nl-1 retired while in "Family" and "Friends" → removed from both, with one `subscription.servers_changed` each.
- Delete "Friends" with 3 links → refused, offering to move the links. After moving them → deleted.
- Hide unhealthy on (grace 30 min) and de-1 `blocked` for 10 min → still served. At 31 min → left out, and the preview says why.
- Hide unhealthy on and both servers `down` past the grace period → both served, and one `subscription.all_unhealthy` notification.
- Turn off the `uri-plain` format while it is the default → refused until another default is chosen.
