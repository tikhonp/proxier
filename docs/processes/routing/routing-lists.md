# Routing lists

**Actors**: Admin; routing module; servers (through `ServerHostnames`); the sync engine.

A routing list is the set of services a target follows: "Main" for the home router and the phone, maybe "Parents" with fewer services for another router. Lists replace mtvpn's `services:` and `service_lists:`. This flow covers editing lists and the two rules that make a list's domains unambiguous and safe: ownership and the server-hostname guard.

## Steps — managing lists

1. Routing → **Lists**. "Main" exists from the first start and is the **default**, used for new targets.
2. **New list**: name, description. → `routing.list_created`
3. The list page:
   - its services in order, each with domain counts (owned / total) and a link;
   - **Add services** from existing services, from search or from discovery;
   - **Remove** per row, and drag to reorder;
   - its targets with their sync state;
   - warnings.

   Every change records → `routing.list_updated{added, removed, reordered}` and marks the list's targets for sync.
4. **Make default** moves the default mark to this list.
5. **Delete** is allowed only for a non-default list with no targets. Otherwise the dialog lists the targets and offers to move them to another list. → `routing.list_deleted`
6. A target's page has **Routing list**. Switching it makes the next sync converge to the new list: tags not in the new list are removed, and new ones are installed.

## Rules

- **Ownership** ([ADR 0013](../../adr/0013-one-owner-service-per-domain.md)): within a list, each name is installed under exactly one tag, and nothing redundant is installed:
  1. An **exact** domain is dropped from a service when another service has the same name, or a parent of it, as a suffix domain.
  2. A **suffix** domain is dropped from a service when another service has a parent of it as a suffix domain.
  3. A name that several services have in the same form (both suffix, or both exact) stays with the **first** of them in list order, its **owner**.

  These are the rules mtvpn's Shadowrocket export already used, now applied to routers too. Removing, editing or reordering a service recomputes ownership, and every tag whose domains changed is re-synced. So a name shared by two services is never lost when one of them leaves.
- **Server hostname guard**: a list must not contain a domain equal to, or a suffix of, any non-retired server's management or proxy hostname. Saving such a list (or a service in it) is refused, naming the server and the domain. → `routing.list_refused_server_hostname{domain, server}`. A new server whose hostname would be covered by an existing list is refused at provisioning, for the same reason.
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
