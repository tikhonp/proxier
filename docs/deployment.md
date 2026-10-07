# Deployment

Proxier is deployed like the other self-hosted services: an image built by GitHub Actions, a compose file in the infra repo of the node that runs it, and a server block in the sh-main gateway. None of these files exist yet. This document describes what they will contain.

## Topology

```mermaid
flowchart LR
    C[Clients, admin browser] -- HTTPS proxier.tikhonnnnn.com --> N[sh-main nginx]
    N -- SSH reverse tunnel --> P[proxier container on blackberry]
    P --- V[(volume /data)]
    P -- CDP, internal network only --> B[chromium sidecar]
    P -- tsnet node 'proxier' --> H[headscale tailnet]
```

- **blackberry** (home, Russia) runs the `proxier` and `chromium` containers. Its compose file is `sh-blackberry/proxier.yaml`, with env from the `secrets/` submodule.
- **sh-main** terminates TLS for `proxier.tikhonnnnn.com` and forwards everything through the existing SSH reverse tunnel, like `files.tikhonnnnn.com`. It sends `X-Real-IP` and `X-Forwarded-For`, and its body size limit fits uploads up to 10 MB (template imports).
- **headscale** (on sh-main) gets a new node, `proxier`, joined with a pre-auth key ([ADR 0008](./adr/0008-tsnet-node-for-router-reachability.md)). headscale ACLs decide which jump hosts and routers that node may reach.

## Containers

| Container | Image | Notes |
|---|---|---|
| `proxier` | `ghcr.io/tikhonp/proxier` | `read_only`, `cap_drop: ALL`, `no-new-privileges`, non-root user, `/data` volume, no published ports (the tunnel reaches it on the compose network). No Docker socket ([ADR 0009](./adr/0009-embedded-xray-core-no-docker-socket.md)). |
| `chromium` | `chromedp/headless-shell` (pinned tag) | Internal network only, no volume, memory limit (1 GB). It reaches the internet directly or through Proxier's discovery SOCKS listener. Optional: without it, discovery runs catalog lookup only. |

The image bundles what the file validators need besides the Go libraries: an `nginx` binary for `nginx -t` and `bash` for `bash -n`. Both run on files in a temporary directory ([template authoring](./processes/servers/template-authoring.md#validation)).

## Images and CI

Same pattern as vk2tg and alcs:

- `ci.yml` on every push and PR: `go build`, `go vet`, `go test -race`, golangci-lint, govulncheck. A failure withholds the image.
- `docker.yml` (or an `image` job after tests) on `main` and tags: buildx, `linux/amd64` (blackberry is x86-64), pushed to `ghcr.io/tikhonp/proxier` as `:<sha>`, `:<branch>`, `:latest` for `main` and `:<tag>` for releases. `APP_VERSION` is set to `<ref>-<sha>`.
- Watchtower on blackberry updates the container, as it does for the other services.

## First start

1. Generate the master key (`openssl rand -base64 32`) and keep it in the secrets submodule **and** in Vaultwarden. Without it the database's secrets can't be recovered.
2. Start the container. Migrations run on startup. Proxier generates its SSH key pair (ed25519) and creates the default routing list "Main" and the default location set.
3. `docker exec -it proxier /bin/proxier manage create-admin` creates the admin interactively. It's the only way to create one.
4. Sign in, then in Settings: the Cloudflare API token and zones, the Telegram bot (token + detected chat), your personal SSH public keys (installed on every new server), the hostname pattern (default `{location}-{number}.hosts.tikhonnnnn.com`), check-host nodes, and the time zone and languages.
5. Copy Proxier's public SSH key (Settings → SSH) onto the jump hosts and routers you want to sync.

## Backups

- Every night at 03:30 Proxier writes a consistent snapshot of the database (`VACUUM INTO`) to `/data/backups/proxier-YYYY-MM-DD.db` and keeps 14. blackberry's existing backup then copies `/data` to the backup disk and to sh-apple.
- Settings → Backups → **Download backup** gives the latest snapshot. Its secrets are still encrypted: restoring needs the same master key.
- Restoring means stopping the container, putting the snapshot in place as `proxier.db`, and starting it.

## Operating notes

- **Home internet down** means the admin UI and every link URL are unreachable. Client apps keep their last list, so existing connections are unaffected. Checks pause their verdicts (reference check), so coming back online doesn't raise false alarms.
- **Upgrades** are image updates. Migrations run on startup, and a migration failure keeps the old schema and exits. Running jobs are interrupted by the restart and resume ([jobs](./processes/platform/jobs.md)).
- **Losing the master key** loses every secret: generated values, tokens, the Cloudflare and Telegram tokens, Proxier's SSH key. Servers keep running, but Proxier can't manage them, and link URLs would need new tokens.
- **Moving the public host** (a new `PROXIER_BASE_URL`) changes every link URL. Keep the old name pointing at Proxier until the apps have refreshed from the new URLs. Give people the new URLs first.
