# Exit demos

Each [roadmap](./roadmap.md) phase ends with a demo that must work end to end on the real deployment (`proxier.tikhonnnnn.com`). Every phase is built and tested against fakes; none of these demos has been run yet, because each needs real hardware, accounts or phones. When one is run, write what passed and what differed into the relevant docs (and answer the open questions it names in [open-questions.md](./open-questions.md)), then mark it done here.

## Phase 0: platform

**Exit:** sign in on `proxier.tikhonnnnn.com`. A demo job writes a live log, survives a container restart, and its failure reaches Telegram. (Rehearsed locally in the production image; not done for real: the deployment, Telegram delivery, the tailnet join, the live log in a browser.)

1. `docker exec -it proxier /bin/proxier manage create-admin`; sign in on `https://proxier.tikhonnnnn.com` (from a new IP → Telegram 🔐).
2. Settings → Integrations → Telegram: token, Detect chat, Send test.
3. Jobs → **Run demo job** with "fail at the end"; watch the live log on a phone and a laptop.
4. Mid-count, `docker restart proxier`: the job shows `interrupted`, then resumes; the log streams again from where it was.
5. The job fails at `finish` → `job.failed` → Telegram 🔴 with **Open in Proxier** opening the job.
6. Settings → Backups → **Back up now** → **Download latest**.
7. Tailnet: the node joins headscale with a pre-auth key, survives a restart, and what happens at the key's expiry (open question "to verify" 7).

## Phase 1: Servers

**Exit:** a fresh VPS becomes `nl-2`, active and healthy, from the UI alone. Firewalling 443 on it from home produces a `blocked` verdict and a Telegram message, and removing the rule recovers it. Rotation changes the connection URI and the old one stops working.

1. Settings → Integrations → Cloudflare: token, zone `tikhonnnnn.com`.
2. Servers → **New server**: a fresh VPS (IP, root password), location `nl` suggested, seed template → watch provisioning to **active**; the name is `nl-2` (or the next free number); `server.activated` arrives in Telegram. The smoke test answers whether XHTTP `stream-up` works through nginx `grpc_pass` (open question "to verify" 6).
3. Import the URI into a phone app; it connects.
4. First check round → `healthy`.
5. On the home router, block 443 to the server's IP from home only. After two rounds → `blocked` with the reason naming nodes abroad; Telegram 🟣. Remove the rule → `healthy`; Telegram 🟢.
6. **Rotate credentials** → the URI changes; the old URI stops working in the app; the new one works.

## Phase 2: Subscriptions

**Exit:** your phone and one family member use links from Proxier. Disabling a link turns that app's list into the stub entry on refresh. A link opened from many networks raises an alert. Before it: commit and deploy the gateway's access-log change in `~/projects/sh-main/nginx/conf.d/proxier.conf`.

1. Subscriptions → **New subscription** "Me" with the server; another, "Family", with it too.
2. **New link** "Tikhon — iPhone" in Me → on the phone, add the subscription by scanning the QR (Happ, Streisand or v2RayTun). The app shows the title and the server, connects, and refreshes. Note which headers it honours (title, interval, expiry): open question "to verify" 2.
3. **New link** for a family member in Family; their phone imports it and connects.
4. **Disable…** the family link → refresh in their app → its list becomes the single entry "⛔ Ссылка отключена · пишите …" (or the English one); **Enable** → the server comes back on the next refresh. Note which apps replace their list and which keep the old one: open question "to verify" 1.
5. Fetch one link from more than 4 networks within a day (the phone on mobile data and on Wi-Fi, `curl` through a few VPN exits) → within 15 minutes a 🟡 "… looks shared" message in Telegram, and the band on the link page.
6. Optional: **Cut off** a test link of a subscription → the rotations run one by one; the other link's app works again after a refresh; the cut-off link's copied URI no longer connects.

## Phase 3: Routing

**Exit:** `mtvpn.yaml` is imported. The home router's first sync is a no-op for unchanged services. The phone subscribes to the hosted Shadowrocket config instead of copyparty. Typing a new site finds its domains, and the router gets them within a minute of saving.

1. Commit and deploy the sh-blackberry draft from [deployment](./deployment.md#the-chromium-sidecar) (the `chromium` service and the `discovery` network). Settings → Integrations shows Chromium connected.
2. Routing → Lists → **Import from mtvpn…**: paste today's `mtvpn.yaml`. In the preview check the rows (bare names written `v2fly:`, `tunneled-domains` as a URL source or copied into a custom service), the ignored keys, and the base box; import into "Main" with the config `iphone`. On the result page nothing mentions the password; Activity neither.
3. Shadowrocket → `iphone`: compare **What the phone gets** with today's copyparty file (the inserted block may differ where ownership now drops shared names). In Shadowrocket: Config → + → paste the URL → Use config; refresh it; delete the copyparty URL from the phone.
4. Routers → **Add router**: the home router. Install Proxier's key on the router with the dialog's `/user ssh-keys add …` (note whether it works on 7.24.5, or whether `/user ssh-keys import` of an uploaded file is needed: open question "to verify" 5), and on the jump host if one is used. **Test connection**, compare each fingerprint with `/ip ssh print` (router) and `ssh-keygen -lf` (jump host), **Save and sync**.
5. Open the initial sync's plan: unchanged tags are **recorded**, with no upload. Updates may appear only where ownership differs from what mtvpn installed (a name shared by two services now installed once); note them. The router keeps working.
6. Discover a site that isn't in any list (one blocked from home), **Auto**. Its domains are ticked; **Create** a custom service in "Main". Within a minute the router page shows a sync with one update; on the router `/ip dns static print where comment=<tag>` lists them.
7. Note what was recorded or updated at the first sync, the key command, SFTP, anything that differed.

## Phase 4: Router scripts

**Exit:** a factory-reset router fetches its generation with `/tool fetch`, imports it, appears in Routing, and is synced without any manual step (zero-touch key install is built). Needs a spare MikroTik on RouterOS 7.24.5+.

1. In `~/projects/mikrotik/fresh-router.rsc`: add `# END PARAMETERS` after `dohForwarder`; `# @fill subscription-link` directly above `:local subUrl`, `# @fill routing-address-list` above `:local vpnList`, `# @fill routing-doh-forwarder` above `:local dohForwarder`; a parameter for Proxier's key and the lines that use it, guarded so that a file without a key still imports, for example:

   ```
   # Proxier's public key: the proxier user logs in with it
   # @fill proxier-ssh-key
   :local proxierKey ""
   ```

   and, in the body (under `# system`):

   ```
   :if ([:len $proxierKey] > 0) do={
       /user group add name=proxier policy=read,write,ftp,ssh
       /user add name=proxier group=proxier password=[:rndstr length=32 from="abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"]
       /user ssh-keys add user=proxier key=$proxierKey
   }
   ```

   The user gets a password nobody knows (Winbox and the console would otherwise accept an empty one). To verify on the router (open questions "to verify" 5 and 9): `:rndstr` on 7.24.5, `/user ssh-keys add … key=` from an imported script, the group's policy for `/import` of the service blocks. If a DHCP client is added by hand for the fetch (step 4), the script's own `/ip dhcp-client add interface=$wanIface` may refuse a second client on the port: `:do { /ip dhcp-client remove [find interface=$wanIface] } on-error={}` before it avoids that. Update the header's USAGE and PREREQUISITES (no `mtvpn.py`, no manual key).
2. Router scripts → **New script**: upload the file. The panel shows 19 parameters, `vpnGateway` computed, no problems. **Publish v1**.
3. **Generate for a new router**: router name ("Dacha"), **Register a new router** on "Main" with its LAN address (`<lanNet>.1`) and the jump host that reaches it (already confirmed in Proxier: the probe never pins a jump host), **Create a link** in a subscription. The summary lists the link, the router and the file; generate. Routing → Routers shows Dacha awaiting setup.
4. On the router: `/system reset-configuration no-defaults=yes skip-backup=yes keep-users=yes`, then the script's prerequisites (device mode with `fetch=yes`, the container package, the USB disk), a temporary DHCP client on the WAN port if there is no internet. **Create fetch URL**, paste both lines.
5. Telegram: "Dacha · fresh-router v1 was fetched from <IP>". Fetching the same URL again answers `404`.
6. Within 10 minutes the probe pins the router's key; Dacha is active, `router_connected` and the first sync follow; the generation page's After the import shows all three steps done. On the router `/ip dns static print where comment=<a tag of Main>` lists the domains; mihomo runs with `SUB1` = the link (its fetch appears on the link page).
7. Note `/tool fetch`'s user agent, what the key and user lines needed on 7.24.5, the DHCP-client question, timings, anything that differed.

## Also not seen for real yet

- Most pages were checked only through HTTP tests or in the headless-shell container, never on a phone: look at the deployed instance on a phone, especially the forms (new server, generate), dialogs, live logs and charts.
- Telegram delivery of every notification text; ipinfo, Cloudflare, check-host.net (rate limits: open question "to verify" 3), GitHub/v2fly and the iplist portals from the deployment.
- An agent session (`/agent/v1`) driven by a real Claude Code or Codex run.
