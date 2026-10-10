# Deployment

Proxier is deployed like the other self-hosted services: an image built by GitHub Actions, a compose file in the infra repo of the node that runs it, and a server block in the sh-main gateway. The image and CI live in this repository (`Dockerfile`, `.github/workflows/ci.yaml`). The compose file (`sh-blackberry/proxier.yaml`) and the gateway block (`sh-main/nginx/conf.d/proxier.conf`) were drafted in 0e; this document describes what they contain.

## Topology

```mermaid
flowchart LR
    C[Clients, admin browser] -- HTTPS proxier.tikhonnnnn.com --> N[sh-main nginx]
    N -- SSH reverse tunnel --> P[proxier container on blackberry]
    P --- V[(volume /data)]
    P -- CDP and SOCKS, network 'discovery' --> B[chromium sidecar]
    P -- tsnet node 'proxier' --> H[headscale tailnet]
```

- **blackberry** (home, Russia) runs the `proxier` and `chromium` containers. Its compose file is `sh-blackberry/proxier.yaml`, with env from the `secrets/` submodule.
- **sh-main** terminates TLS for `proxier.tikhonnnnn.com` and forwards everything through the existing SSH reverse tunnel, like `files.tikhonnnnn.com`: the sidecar `ssht-proxier` forwards `ssh-server:10005` to `proxier:8080`. nginx sends `X-Real-IP` and `X-Forwarded-For`, its body size limit fits uploads up to 10 MB (template imports), and it does not buffer responses and waits up to an hour on one, because the live job log is a server-sent event stream. Its JSON access log writes the path (`http_path`), and public link paths (`/s/`, `/r/`, `/f/`) carry their secret token, so the block leaves those requests out of the access log (`map $uri $proxier_log`, `access_log … if=$proxier_log`, drafted in 2d); Proxier's own request log masks the token instead. nginx's error log still names the path of a request that fails at the gateway (an unreachable tunnel, for example). Proxier sends its own security headers (`Content-Security-Policy`, `Strict-Transport-Security`, `X-Frame-Options`, `Referrer-Policy` and the rest), so the block adds none: the gateway's shared headers include must stay out of it. With both, a browser gets two `Content-Security-Policy` headers and enforces both, so the gateway's would quietly tighten Proxier's (seen on 2026-10-10: a second CSP `img-src https: data:; upgrade-insecure-requests` and a second HSTS header).
- **The trusted proxy.** Proxier takes the client address from `X-Real-IP` only when the TCP peer is in `PROXIER_TRUSTED_PROXIES`. The compose file gives the `proxier` network a fixed subnet (`10.89.250.0/29`); its only other member is the tunnel sidecar, so that subnet is the value. Without it every client would look like the tunnel, and the sign-in lockout and the "new IP" check would be useless.
- **headscale** (on sh-main) gets a new node, `proxier`, joined with a pre-auth key ([ADR 0008](./adr/0008-tsnet-node-for-router-reachability.md)). headscale ACLs decide which jump hosts and routers that node may reach.

## Containers

| Container | Image | Notes |
|---|---|---|
| `proxier` | `ghcr.io/tikhonp/proxier` | `read_only`, `cap_drop: ALL`, `no-new-privileges`, non-root user (65532), `/data` bind mount owned by that user, `tmpfs /tmp`, no published ports (the tunnel reaches it on the compose network). No Docker socket ([ADR 0009](./adr/0009-embedded-xray-core-no-docker-socket.md)). Healthy when `proxier healthcheck` (the image has no curl) gets 200 from its own `/healthz`. |
| `ssht-proxier` | `jnovack/autossh` | The reverse tunnel to sh-main, like every other service's. |
| `chromium` | `chromedp/headless-shell` (pinned tag) | On its own network, `discovery`, shared only with `proxier`; no volume, no ports, memory limit 1 GB. It reaches sites directly (a direct visit goes out from home) or through Proxier's per-visit SOCKS listener (a visit through a server). Optional: without it, discovery runs the catalog lookup only. See [the Chromium sidecar](#the-chromium-sidecar). |

The image bundles what the file validators need besides the Go libraries: an `nginx` binary for `nginx -t` and `bash` for `bash -n`. Both run on files in a temporary directory ([template authoring](./processes/servers/template-authoring.md#validation)).

## The Chromium sidecar

Discovery's headless visits ([domain discovery](./processes/routing/domain-discovery.md)) run in a `chromedp/headless-shell` container that Proxier drives over the DevTools protocol (CDP). Drafted in 3g for `sh-blackberry/proxier.yaml` (the user commits it; that repository wasn't on the build machine, so the draft lives here):

```yaml
services:
  proxier:
    # … as before, plus:
    environment:
      PROXIER_CHROMIUM_URL: "http://chromium:9222"
    networks:
      - proxier
      - discovery

  chromium:
    image: chromedp/headless-shell:156.0.8078.12   # pinned: the stable tag on 2026-10-09
    restart: unless-stopped
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    mem_limit: 1g
    shm_size: 256m
    tmpfs: [/tmp]
    labels:
      dev.dozzle.group: proxier
    networks:
      - discovery

networks:
  discovery:
    driver: bridge
    ipam:
      config:
        - subnet: 10.89.251.0/29
```

- **Why its own network.** The `proxier` network's subnet is `PROXIER_TRUSTED_PROXIES`: any member of it may set `X-Real-IP`. Chromium visits arbitrary sites and runs their scripts, so it must not be on it; `discovery` holds only `proxier` and `chromium`. It is not `internal`: a direct visit goes out from home, as the admin's own browser would.
- **`PROXIER_CHROMIUM_URL`** is the DevTools address. Proxier resolves its host to an IP before asking `/json/version` (DevTools refuses a `Host` header that is neither an IP nor `localhost`) and connects to the WebSocket at that IP. Empty: discovery runs the catalog lookup only, and Settings → Integrations and the Discover page say so.
- **A visit through a server** opens a SOCKS5 listener in Proxier on its address in `discovery` (the local address that routes to the sidecar), on a random port, for that visit only. It accepts connections only from the sidecar's IP (Chrome can't authenticate to a SOCKS proxy) and relays them through the server's endpoint. Each visit is a fresh browser context (no cookies, no storage), disposed afterwards.
- **Checked** on 2026-10-09 with `docker compose config` and by running the image with these limits on a bridge network next to a Linux build of Proxier's discovery code: `/json/version` by IP, a direct visit of a real site (6 pages, trackers and CDNs classified) and a visit through the SOCKS listener (connections relayed).
- **Upgrades** are deliberate: change the pinned tag. Nothing else depends on the Chrome version.

## Images and CI

Same pattern as vk2tg and alcs:

- `ci.yml` on every push and PR: `go build`, `go vet`, `go test -race` in one job, golangci-lint and govulncheck in a parallel one. A failure of either withholds the image.
- `docker.yml` (or an `image` job after tests) on `main` and tags: buildx, `linux/amd64` (blackberry is x86-64), pushed to `ghcr.io/tikhonp/proxier` as `:<sha>`, `:<branch>`, `:latest` for `main` and `:<tag>` for releases. `APP_VERSION` is set to `<ref>-<sha>`.
- Dozzle's nightly update (04:00, label `dev.dozzle.update=auto`) updates the container, as it does for the other services.

## The `/data` volume

```
/data/proxier.db (+ -wal, -shm)   the database
/data/backups/                    nightly snapshots, proxier-YYYY-MM-DD.db, the newest 14
/data/tailnet/                    the tailnet node's state (only once the tailnet is on)
```

The directory is created by the image owned by uid 65532; a bind mount on the host must be made the same way, and so must `backups/` in it, because the backup container mounts that directory and Docker would otherwise create it owned by root, which Proxier can't write (`sudo install -d -o 65532 -g 65532 -m 700 ~/.local/share/proxier ~/.local/share/proxier/backups`). The tailnet state is not in the database or its snapshots.

## First start

1. Generate the master key (`openssl rand -base64 32`) and keep it in the secrets submodule **and** in Vaultwarden. Without it the database's secrets can't be recovered.
2. Start the container. Migrations run on startup. Proxier generates its SSH key pair (ed25519) and creates the default routing list "Main" and the default location set.
3. `docker exec -it proxier /bin/proxier manage create-admin` creates the admin interactively. It's the only way to create one.
4. Sign in, then in Settings: the Cloudflare API token and zones, the Telegram bot (token + detected chat), your personal SSH public keys (installed on every new server), the hostname pattern (default `{location}-{number}.hosts.tikhonnnnn.com`), check-host nodes, and the time zone and languages.
5. Copy Proxier's public SSH key (Settings → SSH) onto the jump hosts and routers you want to sync.

What the admin does by hand, once: create the DNS record for `proxier.tikhonnnnn.com` and issue its certificate (`make proxier.tikhonnnnn.com` in sh-main); create the data directory with the right owner; put `PROXIER_MASTER_KEY` (and a headscale pre-auth key for the node `proxier`, `PROXIER_TS_AUTHKEY`) into `secrets/blackberry/proxier.env` and the master key into Vaultwarden; allow the node `proxier` to reach the jump hosts and routers in headscale's ACL (stored in its database: edit it in Headplane); commit both infra repos; push the image.

## Backups

- Every night at 03:30 (Settings → Backups changes the time and the count) Proxier writes a consistent snapshot of the database (`VACUUM INTO`, on a connection of its own, so writes go on meanwhile) to `/data/backups/proxier-YYYY-MM-DD.db` and keeps 14. A file appears only when whole: it is written under a temporary name and renamed. blackberry's existing backup rsyncs `/data/backups` (not the live database, whose copy would not be consistent) to the backup disk at 03:45, and from there to sh-apple.
- Settings → Backups → **Back up now** queues a snapshot at once; **Download latest** gives the newest. Its secrets are still encrypted: restoring needs the same master key.
- Restoring means stopping the container, putting the snapshot in place as `proxier.db`, and starting it.

## Operating notes

- **Home internet down** means the admin UI and every link URL are unreachable. Client apps keep their last list, so existing connections are unaffected. Checks pause their verdicts (reference check), so coming back online doesn't raise false alarms.
- **Upgrades** are image updates. Migrations run on startup, and a migration failure keeps the old schema and exits. Running jobs are interrupted by the restart and resume ([jobs](./processes/platform/jobs.md)).
- **Losing the master key** loses every secret: generated values, tokens, the Cloudflare and Telegram tokens, Proxier's SSH key. Servers keep running, but Proxier can't manage them, and link URLs would need new tokens.
- **Moving the public host** (a new `PROXIER_BASE_URL`) changes every link URL. Keep the old name pointing at Proxier until the apps have refreshed from the new URLs. Give people the new URLs first.
