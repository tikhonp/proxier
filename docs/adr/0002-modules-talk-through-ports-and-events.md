# Modules are compiled-in packages that talk through ports and events, with no cross-module foreign keys

Proxier must let modules (servers, subscriptions, routing, router scripts) be swapped, extended or left out without rewriting the others. Each module is a Go package registered in `main`. It owns its tables, migrations, routes, jobs and translations. It calls other modules only through small exported interfaces (ports such as `EndpointCatalog` and `LinkIssuer`) and reacts to their events. It never reads another module's tables, and the database has no foreign keys between modules: a row may hold another module's ID, and cleanup happens by event (e.g. `server.retired` makes subscriptions drop the server).

## Considered options

- **Go runtime plugins or separate services**: Go plugins are fragile across builds, and services would bring network APIs, deployment and versioning for a single-user tool.
- **One shared schema with foreign keys everywhere**: simpler queries, but every module then depends on every other module's tables, and "swappable" stops being true.

## Consequences

- Cross-module cleanups are eventually consistent (after the event is dispatched), and event handlers must be idempotent.
- A module consuming a port must work, with features hidden, when the providing module is absent.
