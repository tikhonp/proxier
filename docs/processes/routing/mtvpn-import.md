# mtvpn import

**Actors**: Admin; routing module.

A one-time move of the current mtvpn setup into Proxier: the services and hosted service lists from `mtvpn.yaml` become services in a routing list, your own domain-list files become custom services if you want, and the Shadowrocket base becomes a hosted Shadowrocket config. Routers aren't touched by the import. They are added afterwards ([router sync](./router-sync.md)), and their first sync changes nothing that already matches.

## Steps

1. Routing → **Import from mtvpn**: paste the contents of `mtvpn.yaml`, or upload the file.
2. Proxier parses it with mtvpn's rules: flat `key: value` scalars and one-level `- item` lists. It reads only `services`, `service_lists` and `shadowrocket_base`. Every other key, notably `shadowrocket_upload`, `shadowrocket_user` and `shadowrocket_password`, is ignored and not stored. The preview says so.
3. Each `service_lists` URL is fetched and read as a hosted selector list (one selector per line, `- ` stripped, `#` comments at line start or after whitespace).
4. The **preview** lists every selector found, in order (config `services` first, then the lists' entries, as mtvpn did):
   - the selector as Proxier will store it (bare names become `v2fly:`) and its tag;
   - whether a service with that tag already exists (then it is reused);
   - for URL sources: **Keep as URL source** (default), or **Convert to custom service**, which copies the file's domains into Proxier so the file is no longer needed;
   - the target routing list (default "Main");
   - for `shadowrocket_base`: **Create a Shadowrocket config** from it (name, routing list).
5. **Import** resolves every new service (as in [service management](./service-management.md)) and adds the services to the list in preview order. Any Shadowrocket config is created. A selector that fails to resolve is reported and skipped, and the rest is imported. → `routing.service_added` per new service, `routing.list_updated`, `routing.shadowrocket_created`
6. The result page lists what was created, reused and skipped, with next steps: add your routers, and point Shadowrocket at the new URL.

## Rules

- The import never contacts a router.
- Selectors are deduplicated by tag, and the first spelling wins (mtvpn's rule).
- Credentials in the file are never stored, not even temporarily in a job payload.
- Running the import again is harmless: existing services are reused, and a service already in the list isn't added twice.

## Edge cases (each is a test)

- Today's `mtvpn.yaml` (one service list, one raw-URL service, a Shadowrocket base) → the list's selectors plus `tunneled-domains` imported into "Main", and a Shadowrocket config created. `shadowrocket_password` is not stored anywhere.
- `services: [anthropic]` and a list containing `v2fly:anthropic` → one service `v2fly:anthropic`, tag `anthropic`.
- `mine=https://…/list.txt` → a URL service with tag `mine`.
- A `service_lists` URL returning `404` → reported. The other selectors are still importable.
- A selector `iplist:nosuchsite` → skipped with "not found", the rest imported.
- **Convert to custom service** for `tunneled-domains` → a custom service with the file's domains, normalised. The URL isn't used again.
- Importing twice → the second import reuses everything and adds nothing.
- A file with an unknown key `foo: bar` → ignored, mentioned in the preview.
