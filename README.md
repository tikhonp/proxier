# Proxier

A self-hosted control panel for one person's proxy setup: VPS proxy servers built from templates and checked from Russia, subscription links for people and devices, domain routing kept in sync on MikroTik routers and in Shadowrocket, and the router setup script. One Go binary, one SQLite file, a templ + htmx UI in English and Russian.

![Proxier dashboard](docs/assets/screenshot-dashboard.png)

Docs start at [docs/README.md](docs/README.md).

```sh
make test     # tests
make check    # templ generate, gofmt, vet, generated-file diff, tests
make dev      # hot reload on :8080 (needs .env, see .env.example)
```

AGPL-3.0, see [LICENSE.md](LICENSE.md).
