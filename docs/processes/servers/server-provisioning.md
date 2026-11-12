# Server provisioning

**Actors**: a member with `servers: manage`; the provisioning job (queue `provisioning`); the new VPS; Cloudflare, or the member's own DNS provider for a hostname outside the Cloudflare zones; Let's Encrypt (through the template's steps); the proxy test client.

Provisioning creates a server from a template version. Proxier connects with a root password or a pre-installed SSH key. The member chooses full or deploy-only setup and watches the job's progress. Proxier manages DNS in its configured Cloudflare zones. The member manages records for other hostnames. Activation requires a passing proxy test from home unless the member selects Activate anyway after a smoke-test failure.

## Steps: the form

1. Servers → **New server**. Fields:
   - **IP address** accepts IPv4. It is the server's IP, used for public DNS and client connections. **SSH port**, under Setup options, defaults to 22.
   - **Advanced IP configuration → SSH connection IP**, optional, IPv4 or IPv6. Use it to connect through a separate network, for example `100.64.0.10` on Tailscale, while the server's IP stays `203.0.113.10`. Every SSH connection uses it: provisioning, redeploys, self-checks, reboots and retirement. Blank, or the same as the server's IP, means SSH uses the server's IP. Proxier stores the same IP as blank, so SSH follows the server's IP through an IP change. A later public IP change leaves an explicit SSH IP unchanged. This does not install Tailscale, assign IPs to interfaces or change routing. Proxier must already reach the SSH connection IP through the host network or its configured tailnet. Prefer Deploy only for an existing Tailscale node: Full setup resets the firewall.
   - **SSH access** selects Root password or SSH key. With Root password, Proxier creates the `proxier` account, installs its key and passwordless sudo, then erases the password after verifying access. With SSH key, first copy Proxier's public key from Settings → SSH into the SSH account's `authorized_keys`. The account defaults to `root`. Other accounts need passwordless sudo. Proxier does not create an account or install keys in key mode.
   - **Server setup**, under Setup options, selects Full or Deploy only. Full disables password and root login over SSH, resets the firewall, enables TCP BBR when supported, and installs the prerequisites and Docker. Deploy only installs the prerequisites and Docker if missing. It leaves SSH configuration, firewall rules and TCP settings unchanged.
   - Settings → Servers controls the initial access and setup selections. The defaults are Root password and Full. Each server stores its own choices.
   - **Country**, a searchable picker: typing filters the list of every country by name (in the member's language) or code, and the field takes "🇳🇱 Netherlands · NL"; a code (`nl`) or a name in English or Russian typed in full counts too. It is preselected from the IP's country. The country's lower-case code is the first half of the server name ([ADR 0015](../../adr/0015-servers-have-a-country-not-a-location.md)).
   - **Hostname**, optional: the management and proxy hostname, e.g. `vpn.example.com`. Left empty, the server gets the name the hostname pattern gives (`nl-2.hosts.example.com`). Proxier trims it, writes it in lower case and drops a trailing dot. When none of the Cloudflare zones covers it, the member creates its A record by hand at their DNS provider (a **record made by hand**).
   - **Additional addresses**, optional rows of IP and hostname: more IPs on the same machine, each with its own client-facing name. They are stored with the server and templates see them as `.Addresses`; an endpoint binds to one with `address: <n>`. Proxier does not write or check their DNS records: the member points each hostname at its IP. The stack is one, so the template renders the extra inbounds/server blocks itself.
   - **Skip DNS check** appears beside a manually managed record in the live summary. It skips the form check and provisioning wait for a record the member manages. Use it for a proxied record or a hostname that will point to the server later. It has no effect on records in configured Cloudflare zones. Certificate issuance, self-checks and the smoke test still run. The seed template still needs a hostname that passes the ACME challenge.
   - **Template** and **version** (default version preselected), then the version's **parameters** as form fields with labels, help and defaults.
   - **Notes**.
2. While the member types, the form previews the server name, management and proxy hostnames, DNS record and endpoint display names.

   The country picker follows IP-country suggestions until the member edits it. It can replace an untouched empty value or the previous suggestion. It never replaces a picked country, partial name or field the member cleared. The preview omits the server name until it has a valid country. Invalid country text becomes an error only on **Create**.

   For a manually created DNS record, the summary shows the required A record, for example `vpn.example.org` pointing to `203.0.113.10`. Check the record queries DNS unless Skip DNS check is selected. Updating the summary alone never queries that hostname.
3. **Create**. Proxier checks:
   - no non-retired server has this IP, as its IP or as a previous IP it keeps ([IP change](./ip-change.md));
   - the country is a country (an ISO code in use today), and the template version exists and the template isn't archived;
   - parameters pass validation;
   - the hostname pattern in Settings → Servers is set, unless the member typed a hostname;
   - a typed hostname is a full DNS name: two labels or more, letters, digits and hyphens, no wildcard;
   - no non-retired server has the hostname, as its management or proxy hostname. Create checks it again in its transaction, so two servers created at once never share one;
   - for the pattern's name: Cloudflare is configured and a configured zone covers it;
   - for a record made by hand (a typed hostname that no zone covers, or any typed hostname while Cloudflare is not configured): the name points to the IP and to nothing else. Proxier finds the zone from the name's parents (`example.org` for `vpn.example.org`) and asks its own nameservers, which cache nothing, so a check made before the record exists does not delay it later. When the nameservers can't be asked, it asks 1.1.1.1 and 8.8.8.8 instead. **Check the record** runs the same check. With **Skip DNS check** in place, neither runs and Create goes ahead;
   - no routing list covers the hostname (when the routing module is enabled): a list holding `example.com`, or the hostname itself, would send Proxier's checks of the server into the router's tunnel. Any name listed in any list counts, even one the list doesn't install. The live summary says it too: "nl-3.hosts.example.com is covered by example.com (my-sites) in Main: Proxier's checks would go into the tunnel. Take it out of the list first.";
   - every additional address has an IPv4 and a full DNS hostname of its own; neither equals the primary address or another address of any non-retired server. The refusal names the server that holds it. The check runs again in the transaction that stores the server, so two creates at once cannot share one.

   With key access, the SSH account must start with a lower-case letter or `_`, contain only lower-case letters, digits, `_` and `-`, and be at most 32 characters. Proxier ignores the root password field. With password access, Proxier ignores the SSH account field and uses `proxier`. Key access as `root` requires Deploy only. Full setup would disable root login.

   Failures are shown on the form and nothing is created.
4. Proxier creates the server in the `provisioning` state, reserves its name and stores its access, setup and additional addresses. Password access stores the root password encrypted in the job payload. Key access stores no root password. The job payload also stores `skip_dns`, which retries preserve. Proxier queues the job and opens the server page with its steps and live log. The transaction records `server.created{ip, country, hostname, manual_dns, template_version, access, setup}`.

## Steps: the job

Each step is resumable. A retried job starts at the step that didn't finish.

1. **Preflight.**
   - Connect to `SSH connection IP:port` (or `IP:port` when the SSH connection IP is blank): as root with the password, or as the SSH account with Proxier's key. The host key is pinned now ([ADR 0007](../../adr/0007-agentless-ssh-with-pinned-host-keys.md)).
   - With key access, prove root commands work (`sudo -n true`, or nothing for root) before changing anything: a key that logs in without sudo fails here.
   - Read `/etc/os-release` and the architecture, and compare them with the template's `requires`.
   - Check that the manifest's TCP ports (e.g. 80, 443) are free (`ss -ltn`).
2. **Install access.** With key access this step changes nothing: the account already trusts Proxier's key, and every later connection uses it.
   With password access, later SSH connections use the `proxier` account. Commands that need root use `sudo -n`. The remaining actions in this step apply only to password access.
   - Install `sudo` if the image lacks it, then create the user `proxier` if it doesn't exist: a home directory, a login shell and a locked password.
   - Give it passwordless sudo through `/etc/sudoers.d/proxier` (`proxier ALL=(ALL) NOPASSWD:ALL`). Check the file with `visudo -cf` before moving it into place.
   - Append Proxier's public key and your personal keys to `~proxier/.ssh/authorized_keys`, skipping keys already there. The directory gets mode `700` and the file `600`, owned by `proxier`.
   - Open a **new** connection as `proxier` with Proxier's key, and run `sudo -n true`.
   - Once both work, erase the password from the job payload. From here on, nothing can use or show it.
3. **Base bootstrap** runs the template's `base-bootstrap` step as the stored SSH account. Commands that need root use `sudo -n` unless the account is root.
   - Both setup modes install `curl`, `ca-certificates`, `openssl` and `ufw`. Apt waits for its lock. Docker installs through get.docker.com if missing.
   - Full setup first writes an SSH drop-in with `PasswordAuthentication no`, `KbdInteractiveAuthentication no` and `PermitRootLogin no`. Proxier reloads sshd and verifies key login and sudo through a new connection. If verification fails, it removes the drop-in, reloads sshd and fails the step.
   - Full setup writes `.hushlogin` and enables TCP BBR when the kernel supports it. It saves the TCP settings in `/etc/sysctl.d/90-proxier-bbr.conf` before installing Docker. If BBR is unavailable, it logs a warning and continues.
   - Full setup resets and enables ufw. It allows the SSH port and the manifest's ports and denies other incoming connections. This replaces existing rules. Docker's published ports bypass ufw.
   - Deploy only skips SSH hardening, `.hushlogin`, BBR configuration and firewall configuration. Installing prerequisites and Docker can still change installed packages and Docker's network rules.
4. **DNS.** Create the A record(s) for the primary hostname(s): DNS-only, TTL 60, comment `proxier:nl-2`. Additional addresses are skipped: their records are the member's. Then wait for the primary name(s) to be visible ([Cloudflare](../../integrations/cloudflare.md#behaviour)):
   - First ask the zone's own nameservers (`gabe.ns.cloudflare.com` and the rest, port 53, without recursion) every 2 s until every one of them answers with the IP. A resolver asked before then gets "no such name" and may keep that answer for the zone's negative TTL (the SOA's minimum: 30 min at Cloudflare). If the nameservers can't be asked (port 53 dropped, an answer without the authoritative flag, no nameservers found), the log says why and the wait pauses 10 s instead.
   - Then wait until 1.1.1.1 and 8.8.8.8 both answer with the IP (timeout 10 min). A resolver that still answers nothing at 10 min, while the other shows the IP, probably holds a negative answer from before the record existed; the log says so, and it gets until the zone's negative TTL plus a minute has passed since the nameservers had the record.
   - A record made by hand: Proxier writes nothing. The log says the record is made by hand, and the step waits for it the same way, with the zone found from the name's parents. When it doesn't show, the step fails and names the record to create. With **Skip DNS check**, the step logs that it is skipped and goes on.
5. **Generated values.** Create every generated value the version declares (encrypted).
6. **Render and upload.** Render the version for this server, with `.Addresses` including every additional address, and upload the files to the manifest's `dir` (`upload-files`). Record the deployment and its rendered files.
7. **Template install steps**, in order (in the seed template: issue the certificate, `compose up --pull`, wait for the decoy site to answer locally).
8. **Endpoints.** Render and store the endpoints and their display names. An endpoint bound to an address the server lacks is left out; one bound to an additional address gets that address's hostname.
9. **Self-check.** Run the template's checks once. All must pass.
10. **Smoke test.** Run the proxy test from home through every endpoint (up to 3 attempts, 20 s apart). Every endpoint must pass.
11. **Activate.** The server becomes **active**, its health state becomes `unknown`, and the scheduler starts its checks. Subscriptions with "add new servers automatically" append it. → `server.activated{proxy_test}` (notifies "nl-2 is ready")

The job makes one attempt and never retries by itself: a retry may need input, so it is a member's. Any step failing stops the job. The server becomes **failed**, and the page shows the step, the error and the log. → `server.provisioning_failed{step, error}` (notifies). **Cancelling** the job ends the same way with the error `cancelled` and `cancelled: true` in the event, which does not notify; DNS records already made stay until retry or retirement.

## Steps: after a failure

1. **Retry** resumes at the failed step.
   - Password access asks for the root password again after a failure at preflight or install access. The new password replaces any kept one. Later failures need no password. Key access never asks for a root password.
   - If the template's default version moved on in the meantime, it still uses the version the server was created with.
2. **Activate anyway** is offered only when every step up to the self-check passed and only the smoke test failed. A typical case is a VPS whose hosting network is already blocked in Russia, which can still be useful to link holders abroad. The dialog shows the proxy test's error and asks for a confirmation.
   - The server becomes **active** and the scheduler starts its checks. Its health state comes from the first check round, usually `blocked` or `down`, never `healthy` by default.
   - Subscriptions with "add new servers automatically" append it. Those that hide unhealthy servers keep it hidden until it is healthy. → `server.activated{proxy_test, forced: true}` (notifies "nl-2 is active without a passing proxy test")
3. **Retire** cleans up whatever the job had created: the DNS records it made, and the stack if it was uploaded and the server is reachable ([retirement](./server-retirement.md)).
4. Upgrading or changing parameters isn't possible on a failed server. Retire it and create a new one.

## Rules

- The name is reserved at **Create** and never reused, even if provisioning fails and the server is retired.
- Proxier encrypts the root password in the provisioning job payload and erases it after verifying key login and sudo in step 2. It never logs the password or includes it in an event. A failure before step 2 keeps the encrypted password until retry, cancellation or retirement. Proxier does not collect a root password for key access.
- Each server keeps its access and setup choices. Redeploys, health checks, IP changes and retirement use its stored SSH account and Proxier's key. They do not repeat the base setup.
- Proxier verifies key login after installing keys and after changing sshd. It checks DNS before certificate issuance unless the member skips the check for a record they manage. Skipping DNS checks does not skip certificate issuance, self-checks or the smoke test.
- Proxier never writes, changes or deletes a record made by hand. [IP change](./ip-change.md) checks that the member pointed it at the new IP, and [retirement](./server-retirement.md) leaves it and says to delete it at the DNS provider.
- Provisioning never overwrites a DNS record that Proxier didn't create. A conflicting record fails step 4 with the record's current value. **Retry** then offers **Overwrite** as an explicit choice.
- At most two provisioning jobs run at once. Each server's jobs run one at a time.
- Health checks don't run until the server is active.
- The smoke test is the definition of done: a server whose self-check passes but whose proxy test fails from home is **failed**, with the proxy test's error in the log. Only an explicit **Activate anyway** makes it active, and the event records `forced`.
- Password access uses root SSH access to create `proxier` and install Proxier's key and the personal keys from Settings → SSH. Later SSH connections use `proxier`. Root commands use `sudo -n`. Only full setup disables password and root login over SSH.
- Key access uses an existing account and its installed key. Proxier does not add personal keys in this mode. A non-root account must already have passwordless sudo. Full setup still changes sshd and resets the firewall. Deploy only skips those changes.

## Edge cases (each is a test)

- IP already used by active `nl-1` → refused on the form, nothing created.
- An IP `nl-1` keeps as a previous IP → refused, "nl-1 keeps this IP as a previous IP. Forget it there first."
- An IP in the Netherlands, picker empty → "🇳🇱 Netherlands · NL" preselected with "Suggested from the IP (NL)", summary `nl-1`. Another IP in Germany while the suggestion is untouched → Germany. Any country is suggested, not only those with servers; a lookup answer that is no country (or the lookup turned off) suggests nothing.
- Typing "Neth" in the picker, emptying it, or picking Finland while the IP suggests another country → the field is left as it is; the summary shows no name for "Neth" and no complaint while it is typed.
- "Atlantis", a withdrawn code (`UK`) or nothing in the picker → refused on **Create** ("Pick a country from the list." / "Pick a country."), the text kept, nothing created. `nl` or "Нидерланды" typed in full → the Netherlands.
- "Main" holds a custom service with suffix `example.com`; the new server would be `nl-3.hosts.example.com` → refused on the form, naming the list and the service, nothing created.
- Hostname `VPN.example.com.` typed → the summary shows `nl-2` with `vpn.example.com`. The job makes the A record for `vpn.example.com`, the certificate and the connection URIs name it, and `server.created` records it. The next server without a typed hostname gets the pattern's name again.
- Hostname `vpn`, `my vpn.example.com`, `-vpn.example.com`, `*.example.com` or `https://vpn.example.com/` → refused, "Enter a full DNS name such as vpn.example.com.", nothing created.
- Hostname `vpn.example.org` where no configured zone covers `example.org` → the summary shows `A vpn.example.org → 127.0.0.1`, "Create this record at your DNS provider…" and **Check the record**, with no error.
- That record missing, pointing to `198.51.100.1`, or holding a second address → **Check the record** and **Create** say "vpn.example.org does not point to 127.0.0.1 yet (ns1 answered nothing). Create the record A vpn.example.org → 127.0.0.1 at your DNS provider and check again." Nothing is created.
- That record in place → **Check the record** says "The record is in place: ns1 answered 127.0.0.1." **Create** creates the server. The job writes no record of its own, waits for the resolvers to show it, and the server goes active. `server.created` has `manual_dns: true`.
- With Skip DNS check selected, Create accepts a missing or mismatched record. The job skips its DNS wait and leaves the record unchanged. Certificate issuance, self-checks and the smoke test still have to pass.
- The record made by hand is deleted between **Create** and the DNS step → the DNS step fails: "… Make sure the record A vpn.example.org → 127.0.0.1 exists at your DNS provider, then retry".
- The zone's nameservers can't be asked (port 53 dropped) → the check asks 1.1.1.1 and 8.8.8.8 instead. No nameservers found from the name's parents → the same.
- Cloudflare not configured → every typed hostname is a record made by hand, in any zone. A server without a typed hostname is refused: "Cloudflare is not configured…".
- The pattern's name in no configured zone → refused, "nl-2.hosts.example.org is in none of the zones Proxier may use. Allow its zone under Integrations → Cloudflare."
- Hostname `nl-1.hosts.example.com` while `nl-1` is active → refused, "nl-1 already has nl-1.hosts.example.com." Once `nl-1` is retired, the name is free.
- `de-1` was given the hostname `nl-3.hosts.example.com`; the next server in the Netherlands would get that name from the pattern → refused, naming `de-1`. Typing another hostname gets it through.
- Two servers created at once with the same typed hostname → one is created, the other refused.
- A typed hostname covered by a routing list → refused, naming the list and the service, as for a pattern name.
- A new install has no hostname pattern → the form's summary says so before a country is picked, and **Create** refuses it unless the member typed a hostname. Nothing is created. Settings → Servers and the dashboard's first-run list ask for the pattern.
- An install that built servers while the pattern had a default, and never saved one → a migration saves the pattern rebuilt from the newest server's management hostname: `nl-2.hosts.example.com` gives `{country}-{number}.hosts.example.com`. A pattern saved in Settings stays.
- Wrong root password → fails at preflight with "authentication failed". The password stays encrypted for retry. **Retry** asks for it again.
- IP unreachable (timeout) → fails at preflight. The DNS is untouched.
- Port 443 already in use on the VPS → fails at preflight, naming the process from `ss`.
- Ubuntu 20.04 against `requires: ubuntu-22.04+` → fails at preflight with the OS found.
- The seed's `requires.os: [debian-12+, ubuntu-22.04+]` accepts Ubuntu 26.04. The `+` suffix accepts the listed release and later releases of the same distribution.
- The new key login fails after keys were installed (e.g. `authorized_keys` permissions) → install access fails. The password is kept for retry.
- The sshd reload breaks key login → the drop-in is removed, sshd reloaded, the step fails, and Proxier can still log in.
- Docker already installed → that sub-step is skipped and logged as such.
- The kernel has no TCP BBR → "The kernel has no TCP BBR: TCP stays on cubic" in the log, and provisioning goes on.
- A `proxier` user already exists (a VPS image or an earlier attempt) → it is reused: keys appended, sudoers file rewritten, nothing else changed.
- The sudoers file fails `visudo -cf` → install access fails. The original sudoers configuration is untouched, and the password is kept for retry.
- `sudo -n true` fails as `proxier` (for example, `requiretty` set by the image) → install access fails with sudo's message.
- Self-check passes but every smoke-test attempt stalls after 16 KB → **failed**, with **Retry**, **Activate anyway** and **Retire** offered.
- **Activate anyway** → active. The first round gives `blocked`. A subscription that hides `blocked` servers hides it like any other, once it has been blocked for that subscription's grace period.
- A failure at step 7 → **Activate anyway** isn't offered.
- A conflicting A record at `nl-2.hosts.example.com` (not created by Proxier) → DNS step fails, naming its value. **Retry → Overwrite** replaces it.
- DNS still not visible on 8.8.8.8 after 10 min → DNS step fails. **Retry** resumes at DNS and doesn't recreate the record.
- The zone's nameservers don't serve the record yet → no resolver is asked until they all do; they never do within 10 min → DNS step fails, naming what each nameserver answered.
- Port 53 to the zone's nameservers dropped, or answered by something else (no authoritative flag) → the log says so, the wait pauses 10 s, then asks the resolvers.
- 1.1.1.1 still answers nothing at 10 min while 8.8.8.8 shows the IP (a cached negative answer) → the wait goes on up to the zone's negative TTL plus a minute, and ends when 1.1.1.1 shows the IP. Still nothing then → DNS step fails: "Cloudflare answered nothing (probably a cached negative answer …)".
- Let's Encrypt rate limit → install step fails with certbot's message.
- The container restarts during "Install Docker" → the job resumes at base bootstrap, and running it again is harmless.
- Self-check passes but the proxy test fails 3 times (e.g. the hosting network is already blocked in Russia) → server **failed** at smoke test, with the error.
- **Cancel** during DNS → job cancelled, server **failed**, DNS record kept until retirement or retry.
- Two servers created in the same country at the same moment → `nl-2` and `nl-3`, never two `nl-2`.
- A server fails provisioning as `nl-2` and is retired; the next new server in the Netherlands → `nl-3`.
- A server created when servers had locations, `ams-1` in NL → keeps its name; the next new server in the Netherlands is numbered after the highest `nl-` number, not after `ams-1`.
- Success → server active, health `unknown`, first check round within 5 minutes. Subscriptions with "add new servers automatically" include it, and the `server.activated` notification arrives.
- If the SSH account does not accept Proxier's key, preflight fails with "the server refused Proxier's key for <user>" before changing the server. Retry asks for no password.
- If a non-root SSH account lacks passwordless sudo, preflight fails before changing the server.
- With key access and Deploy only, Proxier deploys the stack without creating `proxier`, hardening sshd, configuring ufw or changing TCP settings. Existing firewall and BBR settings remain in place.
- With key access and Full setup, Proxier hardens sshd and resets the firewall. It verifies the stored account's key login after changing sshd.
- The form rejects SSH account names such as `not a user`, `Root` and `a b` without creating a server. It accepts `root` and `_deploy`.
- The form rejects unknown access or setup values without creating a server.
- The form rejects key access as `root` with Full setup because hardening would disable root login.

- A template with no endpoint fails at preflight before connecting. Rendered endpoints are validated before uploading files, so an invalid endpoint cannot leave a newly started stack behind.
