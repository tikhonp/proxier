# Members and access

**Actors**: a member with `settings: manage`; the new member; any member; Proxier.

Several people sign in to one Proxier. Each is a **member** with their own password, sessions, authenticator app and passkeys ([sign-in](./sign-in.md)), and a **level** in each **area**. A member at `none` in an area doesn't see it at all: its pages answer 403, and navigation, the `:` search, the dashboard and Settings leave it out.

## Areas and levels

Areas follow the module boundaries. Each area has its own ladder, lowest first; a higher level includes the lower ones.

| Area | Covers | Levels |
|---|---|---|
| `servers` | servers, templates, rollouts, health | none · view · manage |
| `subscriptions` | subscriptions, links, alerts, cut-offs | none · own · view_all · manage_all |
| `routing` | routing lists, services, routers, Shadowrocket configs, discovery, imports | none · own · view_all · manage_all |
| `routerscripts` | scripts, versions, generations | none · own · view_all · manage_all |
| `activity` | the Activity and Jobs pages | none · own · all |
| `settings` | Settings and Members | none · manage |

- `view` (servers) shows the servers in service: name, country and health state, on the list only. Everything else of servers, templates included, needs `manage`.
- `own` creates things and reads and changes only the member's own; `view_all` reads everyone's too and still changes only the member's own; `manage_all` reads and changes everything in the area ([ownership](#ownership)).
- In `routerscripts`, writing, publishing, archiving and deleting scripts, and their agent sessions, need `manage_all`; `own` reads scripts and makes generations.
- In `routing`, adding a v2fly, iplist or URL service and refreshing the catalog need `manage_all`; a custom service needs `own`.
- **Cut off** a link and rotating a server need `servers: manage`: a cut-off rotates servers every member's links share ([ADR 0004](../../adr/0004-one-shared-credential-per-endpoint.md)). A member without it can disable their own link but not cut it off.
- In `activity`, `own` shows the events and jobs the member caused, or whose subject is theirs (their own member, or a thing they own); `all` shows everything.
- **Cancel** and **Retry** of a job need the manage level of the job's own area (a `servers.*` job: `servers: manage`; a platform job: `settings: manage`), whatever `activity` shows.
- Every member, whatever their grant, has the dashboard, the search pop-up, Settings → General (their language), Settings → Security (their password, passkeys, authenticator app and sessions) and Settings → Notifications (their Telegram chat and their rules). The instance's settings on General and the lockout on Security need `settings: manage`.
- **Presets** fill the grid: **Owner** is the top level everywhere; **Operator** is Owner without Settings; **Viewer** is `view`, `view_all` or `all` everywhere and no Settings.

## Ownership

Things in the owned areas have an **owner**, a member: subscriptions and links (`subscriptions`), routing lists, routers, Shadowrocket configs, custom services, mtvpn imports and discovery runs (`routing`), and generations with their fetch URLs (`routerscripts`). Servers, templates, scripts and their versions, and the shared services (v2fly, iplist, url) and catalog have none. A new thing belongs to the member who made it; what existed before members belongs to member 1.

- Every query of owned things takes an `access.Scope`: what the member reads (their own, or everyone's from `view_all`) and what they change (their own, or everyone's at `manage_all`). Handlers take it from the request (`web.Scope`), jobs and reactions use `access.All`, so a query without one doesn't compile.
- Another member's thing outside what the member reads is not found (404), never 403, so IDs don't tell what exists. One they read but may not change shows without its controls, and a forged change is refused with 403.
- Choices offer only what the member may change: a new link goes into one of their subscriptions, a router or Shadowrocket config follows one of their lists, and router scripts offer their own subscriptions, links, lists and routers, and make the new link and router theirs.
- A thing of another member names its owner on its page ("owner anna").
- Custom services are private: only their owner, or a member with `routing: manage_all`, sees them and puts them in lists. A tag stays unique across Proxier, since it is a service's identity on routers: a tag another member's private service has is refused as taken, without naming whose.
- Names stay unique across Proxier as before (subscriptions, links, lists, routers, Shadowrocket configs): a name another member uses is refused as taken.
- Notifications follow ownership: a thing's events go to its owner and to `manage_all` in its area, never to another member at `own` or `view_all` ([notifications](./notifications.md#recipients)).
- When a member is removed, each module hands everything they owned to the heir in its reaction to `member.removed`, recording one `subscription.handed_over`, `routing.handed_over` or `routerscript.handed_over` (subject the removed member) when it moved anything. Nothing is deleted, so links, configs and fetch URLs keep working.

## Steps: adding a member

1. Settings → Members → **Add member** (`settings: manage`). A username (1 to 64 characters, unique among the members in, ignoring case), a level per area (a preset fills them), and the adder's own password: a setup link is a new way in.
2. Proxier adds the member with no password and makes a **setup link**: a secret URL `/setup/<token>` that works once, for 24 hours. → `member.added{username, access}` (notifies), `member.setup_link_created{username, expires}`
3. The member's page shows the link with **Copy link**; the adder sends it to the new member. Until it is used the member is "Waiting for setup" and nothing signs them in.
4. The new member opens the link (no session needed), chooses a password (12 characters at least, twice) and is signed in, in the language their browser asked for. The link ends. → `member.setup_completed{username}`, `auth.signed_in`
5. A link that ran out or was lost: **New setup link…** on the member's page asks for the adder's password, ends the old link and makes a new one. → `member.setup_link_created`

## Steps: changing levels

1. A member's page on Settings → Members shows their levels; **Save** changes them. → `member.access_changed{username, changes, access}` (notifies)

## Steps: removing a member

1. **Remove member** on their page, confirmed in a dialog.
2. Their sessions end (`member_removed`), their passkeys, authenticator app, levels and setup link go, and the row stays, removed, so Activity still names them. → `member.removed{username, heir, heir_username, sessions}` (notifies). `heir` is the member who removed them, or for a member who removed themselves, the first member in with `settings: manage`: the member who takes over what the removed one owned.
3. Each module hands the removed member's things to the heir. → `subscription.handed_over{from, to, subscriptions, links}`, `routing.handed_over{from, to, lists, routers, shadowrocket, services, imports, discovery}`, `routerscript.handed_over{from, to, generations}`, each only when it moved something.
4. To bring someone back, add them again; the username is free once its member is removed.

## Notifications

Each member has their own Telegram chat and rules, and gets an event's notification only when it concerns them ([notifications](./notifications.md#recipients)): their own things and sign-ins, the area they manage at its top level, and, with `settings: manage`, the changes to members (`member.added`, `member.access_changed`, `member.removed`, and the setup link's two events when turned on). A removed member gets nothing.

## Rules

- At least one member who is in, has finished setup and has `settings: manage` always remains. Removing the last one is refused. (Demoting them can't happen: whoever changes levels manages Settings, and nobody changes their own.)
- Nobody gives a level above their own in any area. A level a member already has may stay above the editor's: the editor can still change their other areas. The form's options above the editor's own level are off, and a forged one is refused.
- Nobody changes their own levels.
- Only a member with `settings: manage` adds, changes or removes members; the service checks it again inside the change.
- A setup link is found by an HMAC of its token; the token is sealed in the vault (AAD `setup_link:<id>:token`) only so the member's page can copy it again, and erased when the link is used, replaced or its member removed. Pages that show one are `no-store`.
- A setup link works only while it is the member's open one, within 24 hours, and while its member is in and has no password. Every other link, unknown ones included, gets the same "This link doesn't work" page.
- A browser signed in as anyone is refused at a setup link: sign out first.
- A page of an area the grant doesn't reach answers 403 naming the area. An htmx request gets a small fragment instead, which the key line shows, since htmx swaps no 4xx.
- Every signed-in route checks an area, except the short list every member has; `TestEverySignedInRouteChecksAnArea` walks every route of the app to hold it.
- The first member, made by `proxier manage create-admin`, is Owner; `member.added` with actor `cli`.

## Edge cases (each is a test)

- The first member → Owner; `member.added` by `cli`.
- `create-admin` when a member exists → refused, nothing changes.
- Adding with a wrong password → refused, no member.
- A username another member in has, in any case → asked again with what was typed; a removed member's username → taken back, with a new ID.
- An empty username → refused.
- A level that doesn't exist in the form → 400.
- Before setup → nothing signs the member in.
- Setup link used twice → the second is "This link doesn't work".
- Setup link after 24 hours → refused, and not offered to copy; **New setup link…** with a wrong password → refused; with the password → the old link (even in time) refused, the new one works.
- A new link for a member already set up → refused.
- Setup link opened, or posted, in a browser signed in as someone → 409, and the link still works afterwards.
- Setup passwords that don't match, or shorter than 12 characters → asked again.
- The setup token is never stored in plain text; a used link keeps no token.
- Giving a level above your own → refused, and its option is off; keeping a level above your own while changing another area → saved.
- Changing your own levels → refused, the form is off.
- Saving the same levels → no event.
- A member without `settings: manage` adding, changing or removing → refused (403 on the routes, refused by the service too).
- The only settings manager removing themselves → refused; a manager still waiting for setup doesn't count; with another one set up → removed, and the heir is that one.
- Removing a member → their session ends at once, passkeys, app, levels and link go, sign-in refused, the list keeps them as removed, Activity names them, `member.removed` names the heir. Removing twice → refused, one event.
- A removed member's setup link → refused, its token erased.
- A member at `none` in an area opens its page → 403 page naming it; through htmx → the fragment. Nav, the jobs cell, search, the dashboard's Telegram band and Settings' pages of that area are not shown.
- General without `settings: manage` → only the member's language; Security → no lockout.
- Every signed-in route of every module → 403 for a member with no access, except every member's list.
- `servers: view` → the list of servers in service (name, country, health), no IP, no other states, no link; a server's pages, its health, stats and templates → 403; search finds servers in service by name or country, leading to the list, and no templates.
- `routerscripts: own` → no route that writes, publishes, archives or deletes scripts or opens their agent sessions.
- A member who sees a job but can't change its area → no **Cancel** or **Retry**, and 403 on the POST; the job is untouched.
- Each new POST of Members without the CSRF token → 403.
- Events from before members: `admin.created` and `admin.language_changed` become `member.added` and `member.language_changed`, and back on rollback.
- Two members at `own`: each sees only their own subscriptions, links, lists, routers, Shadowrocket configs and generations, in lists, search and the dashboard; the other's link, subscription, router or generation by URL → 404, not 403, and a POST to it → 404 with nothing changed.
- `view_all` (the Viewer preset) on another member's link, subscription, list, router or generation → shown with its owner and without controls; a forged change or a change page → 403, nothing changed; their own → changed.
- New link with another member's subscription forged in the form → refused, no link.
- Cut-off by a member without `servers: manage`, even on their own link → not offered, its page and POST 403, the link untouched; disabling it → done.
- Removed member's links still serve; their subscriptions, links and generations belong to the heir (`*.handed_over` once; a second delivery moves and records nothing); Activity still names them.
- Router scripts' "create a link" offers only the member's own subscriptions, and the new link is theirs; a forged subscription, link or router of another member → refused as a field error.
- At `activity: own`: Activity, Jobs and the jobs cell show only the member's events and jobs; another member's job by URL → 404.
- Custom service tag already taken by another member's private service → "tag is taken", without naming the service or its owner.
- A notification about a member's link → that member's chat and the `subscriptions: manage_all` members with the type on, and nobody else (`TestLinkNotificationGoesToItsOwnerAndManageAll`, `TestNotificationReachesTheOwnerAndManageAll`).
- A member added, their levels changed or removed → notified to the `settings: manage` members only (`TestMemberEventsGoToSettingsManagers`).
- A sign-in from a new IP → notified to that member only (`TestNewIPNoticeGoesOnlyToThatMember`).
- A removed member → no notification, even one queued before the removal (`TestRemovedMemberGetsNothing`); the owner of their things is the heir from the handover on.
- A member without `settings: manage` → their own Settings → Notifications: their chat, **Detect chat** without other members' chats, **Send test** to theirs, and only the rules they can be told about (`TestEachMemberHasTheirOwnChat`, `TestDetectChatLeavesOutOtherMembersChats`, `TestNotificationRulesArePerMember`).
