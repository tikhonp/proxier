# Routing lists

**Actors**: Admin; routing module; servers (through `ServerHostnames`); the sync engine.

A routing list is the set of services a target follows: "Main" for the home router and the phone, maybe "Parents" with fewer services for another router. Lists replace mtvpn's `services:` and `service_lists:`. This flow covers editing lists and the two rules that make a list's domains unambiguous and safe: ownership and the server-hostname guard.

## Steps — managing lists

1. Routing → **Lists**. "Main" exists from the first start and is the **default**, used for new targets.
2. **New list**: name (1–60 characters, unique, letter case counts), description. → `routing.list_created`
3. The list page:
   - its services in order, each with domain counts (owned / total) and a link;
   - **Add services** from existing services (ticked in a picker with a filter; they are appended in tag order), from a new selector (**Add service** with the list ticked), from search or from discovery;
   - **Remove** per row; reorder by drag, or `J`/`K` and the row's ↑/↓. After a reorder a band says what moved, which names change owner and which targets re-sync, with **Undo** (`u`);
   - its targets with their sync state;
   - warnings: guard refusals of the last 7 days, and names left out now because they cover a server's hostname.

   Every change records → `routing.list_updated{added, removed, reordered}` and marks the list's targets for sync. **Rename…** changes the name and description (nothing to sync).
4. **Make default** moves the default mark to this list.
5. **Delete** is allowed only for a non-default list. When targets follow it, the dialog lists them and moves them to another list in the same step (each router records `router_updated{changes: list}` and is marked for sync). Its services stay. → `routing.list_deleted{name, moved}`
6. A target's page has **Routing list**. Switching it makes the next sync converge to the new list: tags not in the new list are removed, and new ones are installed.

## Rules

- **Ownership** ([ADR 0013](../../adr/0013-one-owner-service-per-domain.md)): within a list, each name is installed under exactly one tag, and nothing redundant is installed:
  0. Names that trip the server-hostname guard, and names a target pins for itself (a router's infra pins), are taken out of every service first: nobody installs them and they cover nothing.
  1. An **exact** domain is dropped from a service when another service has the same name, or a parent of it, as a suffix domain.
  2. A **suffix** domain is dropped from a service when another service has a parent of it as a suffix domain.
  3. A name that several services have in the same form (both suffix, or both exact) stays with the **first** of them in list order, its **owner**.

  Names under a service's **own** suffixes stay (`mail.google.com` under `google.com` in one upstream service): mtvpn installed them too, so a router it managed changes as little as possible.

  These are the rules mtvpn's Shadowrocket export already used, now applied to routers too. Removing, editing or reordering a service recomputes ownership, and every tag whose domains changed is re-synced. So a name shared by two services is never lost when one of them leaves.
- **Server hostname guard**: a list must not contain a domain equal to, or a suffix of, any non-retired server's management or proxy hostname (an exact name only when equal). Adding a service to a list, and saving a custom service that is in a list, is refused, naming the server and the domain; nothing is written. → `routing.list_refused_server_hostname{domain, server, hostname, service}`, one per list it would have broken. A refresh or a switched source that brings such a name is never refused for it: the snapshot is accepted, the name is left out of what targets get, and the list shows a warning while it lasts. A new server whose hostname any listed name would cover is refused at provisioning, for the same reason.
- A service can be in any number of lists. Ownership is computed per list.
- Exactly one list is the default. The default can't be deleted.
- An empty list is allowed. Targets following it end up with no Proxier-installed tags.

## Edge cases (each is a test)

- "Main" = [`v2fly:anthropic`, `custom:mine`], both containing suffix `claude.ai` → `anthropic` owns it. `mine` is installed without it.
- Remove `anthropic` from "Main" → `mine` now owns `claude.ai`. Both tags are re-synced, and `claude.ai` stays on the router.
- Reorder to [`mine`, `anthropic`] → `mine` owns `claude.ai`, `anthropic` drops it, and both re-sync.
- `anthropic` has suffix `anthropic.com`, and `mine` has exact `console.anthropic.com` → dropped from `mine` (covered).
- `mine` (first) has suffix `api.example.com`, and `other` (later) has suffix `example.com` → `api.example.com` is dropped from `mine`: the broader suffix covers it, whatever the order. Remove `other` → `mine` installs `api.example.com` again.
- `anthropic` (first) has exact `claude.ai`, and `mine` (later) has suffix `claude.ai` → the exact entry is dropped from `anthropic`, and `mine` installs the suffix.
- Add a custom service with `hosts.tikhonnnnn.com` to "Main" while `nl-1.hosts.tikhonnnnn.com` exists → refused, naming nl-1.
- New server `nl-3.hosts.tikhonnnnn.com` while "Main" contains suffix `tikhonnnnn.com` → provisioning refused on the form, naming the list and service.
- Delete "Parents" with one router → refused, offering to move the router.
- Switch the home router from "Main" to "Parents" → the next sync removes tags missing from "Parents" and keeps or updates the rest.
- Delete the default list → refused.
