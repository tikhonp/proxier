# Subscription management

**Actors**: a member with `subscriptions` access; subscriptions module; servers (through `EndpointCatalog` and its events).

A subscription defines the servers returned by a group of links. Examples include "Family", "Friends" and "Me, everything". Its owner chooses the server order, output formats and whether to hide unhealthy servers. Each link points to one subscription. See [link lifecycle](./link-lifecycle.md).

## Steps: creating and editing

1. Subscriptions → **New subscription**: name, title (prefilled with the name), description. It belongs to the member who made it. → `subscription.created`
2. **Servers**: **Add servers** lists the active servers that aren't in it yet (flag, name, health), with multi-select; the ticked ones are appended in name order. Rows are reordered by drag, by `J`/`K` on the selected row, or by their ↑/↓ buttons. Each row shows health and whether it is hidden right now, with **Remove** (a simple confirmation). → `subscription.servers_changed{added, removed, reordered}`
3. **Settings**:
   - **formats** (allowed and default);
   - **update interval** in hours;
   - **score in endpoint names**: Off, IP reputation, Claude or ChatGPT;
   - **hide unhealthy servers**: off, or on with the states (default `blocked` and `down`) and grace period (default 30 min);
   - **add new servers automatically**.

   → `subscription.updated{changes}`
4. **Preview** shows exactly what a link of this subscription would receive now: the headers and the connection URIs (credentials masked until revealed), with hidden servers listed and the reason, and members not in service. A preview records nothing.
5. **Delete** works only when no non-deleted link points to the subscription. Otherwise the dialog lists the non-deleted links and offers **Move links to…**, which moves the links the member may change to another subscription they may change. → `subscription.deleted`

## Steps: reactions to servers

1. `server.activated` → every subscription with **add new servers automatically** appends the server at the end. → `subscription.servers_changed{added, auto: true}`
2. `server.retired` → the server is removed from every subscription. → `subscription.servers_changed{removed, reason: retired}`
3. `server.redeployed` / `server.credentials_rotated` → nothing to do: outputs are computed on every fetch from the current endpoints.
4. `server.health_changed` → nothing is stored. Hiding is decided at fetch time from the state and "since".
5. A fetch where hiding would leave no servers raises `subscription.all_unhealthy` (at most once per subscription per hour) and serves every server ([fetch](./subscription-fetch.md)).

## Rules

- A subscription belongs to a member ([ownership](../platform/members.md#ownership)). At `own` a member sees and changes only their own; `view_all` reads everyone's (servers, preview, links in it they read) without the controls, and a forged change is refused with 403; `manage_all` changes every one. Another member's subscription outside what the member reads is a 404.
- The reactions to servers change every subscription, whoever owns it.
- Names are unique, whoever owns the subscription. Titles don't have to be.
- Only active servers can be added. A server keeps its position until removed. Removed servers don't leave gaps.
- A server can be in many subscriptions. Its connection parameters are the same in all of them, while display-name scores may differ (credentials are per endpoint, [ADR 0004](../../adr/0004-one-shared-credential-per-endpoint.md)).
- The default format must be one of the allowed formats, and at least one format is allowed.
- Changes take effect on each link's next fetch. There is nothing to push to apps.
- "Add new servers automatically" only affects servers activated after it was turned on.

## Edge cases (each is a test)

- Score settings persist on save; saving the same setting records no event. An unknown score mode is refused.
- Off preserves the output. IP reputation, Claude and ChatGPT add only their matching cached scores, including zero. Missing scores leave names unchanged.
- Plain output, decoded base64 output and masked previews use the same score suffix. The suffix preserves endpoint-key names, credentials, URI parameters, endpoint order and stub text.

- Create "Family" with nl-1 and de-1 → the preview shows two connection URIs in that order, with the title "Family".
- Drag de-1 above nl-1 → the next fetch serves de-1 first.
- Add a failed server → not offered in the list.
- `nl-2` activated while "Me" has "add new servers automatically" on → appended to "Me" only.
- nl-1 retired while in "Family" and "Friends" → removed from both, with one `subscription.servers_changed` each.
- Delete "Friends" with 3 links → refused, offering to move the links. After moving them → deleted.
- Hide unhealthy on (grace 30 min) and de-1 `blocked` for 10 min → still served. At 31 min → left out, and the preview says why.
- Hide unhealthy on and both servers `down` past the grace period → both served, and one `subscription.all_unhealthy` notification.
- Turn off the `uri-plain` format while it is the default → refused until another default is chosen.
- Two members at `own` → each lists and searches only their own subscriptions; the other's by URL, its delete page or a change → 404.
- `view_all` on another member's subscription → shown without Add servers, ordering, removing, settings or delete; its delete forged → 403, nothing changed.

- Delete a subscription whose only link was deleted 10 days ago → deleted; the link's URL serves “⛔ Link removed” until its tombstone ends, then `404` (`TestDeletedLinkTombstone`).
