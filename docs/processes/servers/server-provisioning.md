# Server provisioning

**Actors**: a member with `servers: manage`; the provisioning job (queue `provisioning`); the new VPS; Cloudflare; Let's Encrypt (through the template's steps); the proxy test client.

Provisioning turns a freshly issued VPS (an IP and a root password) into an **active** server built from a template version. The member fills in one form and watches the job. There is nothing to type over SSH and nothing to do in DNS. A server only becomes active once a real proxy connection from home works through it.

## Steps: the form

1. Servers → **New server**. Fields:
   - **IP address** (IPv4) and **Current SSH port**, where the VPS's SSH listens now (default 22). Provisioning moves SSH to a port of the server's own.
   - **Root password**, never shown again.
   - **Country**, a searchable picker: typing filters the list of every country by name (in the member's language) or code, and the field takes "🇳🇱 Netherlands · NL"; a code (`nl`) or a name in English or Russian typed in full counts too. It is preselected from the IP's country. The country's lower-case code is the first half of the server name ([ADR 0015](../../adr/0015-servers-have-a-country-not-a-location.md)).
   - **Template** and **version** (default version preselected), then the version's **parameters** as form fields with labels, help and defaults.
   - **Notes**.
2. While the member types, the form shows what will be created: the server name (`nl-2`), the management and proxy hostnames, the DNS record, and the endpoints with their display names. A picker the member hasn't chosen in (empty with nothing suggested yet, or showing what the IP suggested last) follows the IP's country as the summary updates; anything else in it (a half-typed name, an emptied field, a country they picked) is theirs, and the summary never replaces it. Until a country is picked the summary shows no name, and text that names no country is refused only on **Create**.
3. **Create**. Proxier checks:
   - no non-retired server has this IP;
   - the country is a country (an ISO code in use today), and the template version exists and the template isn't archived;
   - parameters pass validation;
   - Cloudflare is configured and a configured zone covers the hostname;
   - no routing list covers the hostname (when the routing module is enabled): a list holding `tikhonnnnn.com`, or the hostname itself, would send Proxier's checks of the server into the router's tunnel. Any name listed in any list counts, even one the list doesn't install. The live summary says it too: "nl-3.hosts.tikhonnnnn.com is covered by tikhonnnnn.com (my-sites) in Main: Proxier's checks would go into the tunnel. Take it out of the list first."

   Failures are shown on the form and nothing is created.
4. The server is created in **provisioning** with its name reserved and its **SSH port** drawn: a random port from 10000 to 32767 (below Linux's ephemeral range, so none of the server's outgoing connections holds it), never the current SSH port nor one of the template's ports. It is the server's SSH port for its whole life; the form's port is kept as its **setup port**, for the steps before base bootstrap. The root password goes encrypted into the job payload, and the job is queued. The browser goes to the server page, which shows the steps and the live log. → `server.created{ip, country, template_version}`

## Steps: the job

Each step is resumable. A retried job starts at the step that didn't finish.

1. **Preflight.**
   - Connect to `IP:setup port`.
   - Log in as root with the password. The host key is pinned now ([ADR 0007](../../adr/0007-agentless-ssh-with-pinned-host-keys.md)).
   - Read `/etc/os-release` and the architecture, and compare them with the template's `requires`.
   - Check that the server's SSH port and the manifest's TCP ports (e.g. 80, 443) are free (`ss -ltn`).
2. **Install access**, on the setup port. Proxier never works as root after this step. It uses a deploy user called **`proxier`**.
   - Install `sudo` if the image lacks it, then create the user `proxier` if it doesn't exist: a home directory, a login shell and a locked password.
   - Give it passwordless sudo through `/etc/sudoers.d/proxier` (`proxier ALL=(ALL) NOPASSWD:ALL`). Check the file with `visudo -cf` before moving it into place.
   - Append Proxier's public key and your personal keys to `~proxier/.ssh/authorized_keys`, skipping keys already there. The directory gets mode `700` and the file `600`, owned by `proxier`.
   - Open a **new** connection as `proxier` with Proxier's key, and run `sudo -n true`.
   - Once both work, erase the password from the job payload. From here on, nothing can use or show it.
3. **Base bootstrap** (the template's `base-bootstrap` step). Commands that need root run through `sudo -n`. It connects on the server's SSH port, and on the setup port when nothing answers there, so a restart before or after sshd moved finds it. Before the first connection to `IP:SSH port`, that address gets the key pinned at the setup port: a different key there is a changed key, not a first contact.
   - SSH moves and hardens, through the connection already open:
     1. `proxier`'s password field becomes `*` (no password, not locked): `useradd` and `passwd -l` leave `!`, and sshd with `UsePAM no` refuses a locked account even its key.
     2. If ufw is installed, it allows `<SSH port>/tcp`: an image may come with ufw on.
     3. A drop-in, `/etc/ssh/sshd_config.d/00-proxier.conf`: `Port <SSH port>`, `PasswordAuthentication no`, `KbdInteractiveAuthentication no`, `PermitRootLogin no`, `UsePAM no`, `AllowUsers proxier`.
     4. sshd takes it after `sshd -t`: where it is socket-activated (Ubuntu 22.10 and later), `systemctl daemon-reload` and `systemctl restart ssh.socket`, because a reload keeps the old port; elsewhere `systemctl reload ssh`. Open connections survive both.
     5. A **new** connection on the SSH port verifies key login as `proxier` and `sudo -n`. If it fails, the drop-in is removed through the old connection, sshd takes that the same way (back on the setup port), and the step fails.
   - `.hushlogin` and the apt prerequisites.
   - TCP BBR: `net.ipv4.tcp_congestion_control = bbr` (and the `fq` qdisc, best effort), kept across reboots in `/etc/sysctl.d/90-proxier-bbr.conf`. The server sends what clients download through it, over long lossy paths, where BBR keeps the throughput CUBIC halves on every loss. It comes before Docker, because a container's network namespace starts with the host's default. A kernel without BBR (OpenVZ, LXC) gets a warning, "TCP stays on cubic", no file, and provisioning goes on.
   - Docker via get.docker.com unless Docker is already installed.
   - Firewall: ufw is reset, then denies every incoming connection but the server's SSH port's and the manifest's ports, and is turned on. Rules the image came with go, so the rule set is exactly what Proxier means; no template rule is lost, because the template's own steps come later.
4. **DNS.** Create the A record(s) for the hostname(s): DNS-only, TTL 60, comment `proxier:nl-2`. Wait until 1.1.1.1 and 8.8.8.8 both answer with the IP (timeout 10 min).
5. **Generated values.** Create every generated value the version declares (encrypted).
6. **Render and upload.** Render the version for this server and upload the files to the manifest's `dir` (`upload-files`). Record the deployment and its rendered files.
7. **Template install steps**, in order (in the seed template: issue the certificate, `compose up --pull`, wait for the decoy site to answer locally).
8. **Endpoints.** Render and store the endpoints and their display names.
9. **Self-check.** Run the template's checks once. All must pass.
10. **Smoke test.** Run the proxy test from home through every endpoint (up to 3 attempts, 20 s apart). Every endpoint must pass.
11. **Activate.** The server becomes **active**, its health state becomes `unknown`, and the scheduler starts its checks. Subscriptions with "add new servers automatically" append it. → `server.activated{proxy_test}` (notifies "nl-2 is ready")

The job makes one attempt and never retries by itself: a retry may need input, so it is a member's. Any step failing stops the job. The server becomes **failed**, and the page shows the step, the error and the log. → `server.provisioning_failed{step, error}` (notifies). **Cancelling** the job ends the same way with the error `cancelled` and `cancelled: true` in the event, which does not notify; DNS records already made stay until retry or retirement.

## Steps: after a failure

1. **Retry** resumes at the failed step.
   - If the failed step was preflight or install access (before Proxier's key works), it **always** asks for the root password again; the new one replaces any kept one. Later failures ask for nothing.
   - If the template's default version moved on in the meantime, it still uses the version the server was created with.
2. **Activate anyway** is offered only when every step up to the self-check passed and only the smoke test failed. A typical case is a VPS whose hosting network is already blocked in Russia, which can still be useful to link holders abroad. The dialog shows the proxy test's error and asks for a confirmation.
   - The server becomes **active** and the scheduler starts its checks. Its health state comes from the first check round, usually `blocked` or `down`, never `healthy` by default.
   - Subscriptions with "add new servers automatically" append it. Those that hide unhealthy servers keep it hidden until it is healthy. → `server.activated{proxy_test, forced: true}` (notifies "nl-2 is active without a passing proxy test")
3. **Retire** cleans up whatever the job had created: the DNS records it made, and the stack if it was uploaded and the server is reachable ([retirement](./server-retirement.md)).
4. Upgrading or changing parameters isn't possible on a failed server. Retire it and create a new one.

## Rules

- The name is reserved at **Create** and never reused, even if provisioning fails and the server is retired.
- The root password exists only encrypted in the job payload, and only until Proxier's key login works (step 2). It never appears in logs, events or the UI. A provisioning job that fails before step 2 keeps it, encrypted, until it is retried, cancelled or the server is retired.
- Every remote change is verified before the next step depends on it: key login after installing keys, key login after the sshd change, DNS answers before certificate issuance.
- Provisioning never overwrites a DNS record that Proxier didn't create. A conflicting record fails step 4 with the record's current value. **Retry** then offers **Overwrite** as an explicit choice.
- At most two provisioning jobs run at once. Each server's jobs run one at a time.
- Health checks don't run until the server is active.
- The smoke test is the definition of done: a server whose self-check passes but whose proxy test fails from home is **failed**, with the proxy test's error in the log. Only an explicit **Activate anyway** makes it active, and the event records `forced`.
- Root is used once, with the password, to create the `proxier` user. After that every connection is `proxier` with Proxier's key, root login over SSH is off, and root commands go through `sudo -n`. Your personal keys are installed for `proxier` too.
- From base bootstrap on, every connection (later steps, deploys, checks, stats, retirement) goes to the server's SSH port; the setup port serves only preflight and install access. Port lines in sshd's configuration add up, so on an image whose own `sshd_config` names a port, sshd keeps answering there too, behind ufw's deny.

## Edge cases (each is a test)

- IP already used by active `nl-1` → refused on the form, nothing created.
- An IP in the Netherlands, picker empty → "🇳🇱 Netherlands · NL" preselected with "Suggested from the IP (NL)", summary `nl-1`. Another IP in Germany while the suggestion is untouched → Germany. Any country is suggested, not only those with servers; a lookup answer that is no country (or the lookup turned off) suggests nothing.
- Typing "Neth" in the picker, emptying it, or picking Finland while the IP suggests another country → the field is left as it is; the summary shows no name for "Neth" and no complaint while it is typed.
- "Atlantis", a withdrawn code (`UK`) or nothing in the picker → refused on **Create** ("Pick a country from the list." / "Pick a country."), the text kept, nothing created. `nl` or "Нидерланды" typed in full → the Netherlands.
- "Main" holds a custom service with suffix `tikhonnnnn.com`; the new server would be `nl-3.hosts.tikhonnnnn.com` → refused on the form, naming the list and the service, nothing created.
- Wrong root password → fails at preflight with "authentication failed". The password stays encrypted for retry. **Retry** asks for it again.
- IP unreachable (timeout) → fails at preflight. The DNS is untouched.
- Port 443 already in use on the VPS → fails at preflight, naming the process from `ss`.
- Ubuntu 20.04 against `requires: ubuntu-22.04+` → fails at preflight with the OS found.
- The new key login fails after keys were installed (e.g. `authorized_keys` permissions) → install access fails. The password is kept for retry.
- The sshd reload breaks key login → the drop-in is removed, sshd takes it, the step fails, and Proxier can still log in on the setup port. **Retry** moves SSH again.
- Success → sshd answers only on the server's SSH port, the server's row holds it, ufw allows exactly `<SSH port>/tcp` and the manifest's ports with everything else denied, and later jobs (a check round) connect on it.
- The process restarts after sshd moved and before base bootstrap ends → the step starts again on the SSH port and finishes.
- The drawn SSH port is in use on the VPS → fails at preflight: "port 23817, where Proxier moves SSH, is in use by", the process from `ss`.
- Another host key answers on the new port → the new connection fails with "host key changed", the drop-in is removed, sshd is back on the setup port, and the step fails. The new key waits for a member's decision in Settings → SSH.
- The image comes with ufw on, allowing only the setup port and its own rules → the SSH port is allowed before sshd moves, and afterwards ufw allows exactly Proxier's ports.
- A server created before SSH moved (its SSH port and setup port are one) is retried → SSH stays on that port: the drop-in names it, preflight doesn't count sshd as a taken port, and nothing is pinned again.
- Docker already installed → that sub-step is skipped and logged as such.
- The kernel has no TCP BBR → "The kernel has no TCP BBR: TCP stays on cubic" in the log, and provisioning goes on.
- A `proxier` user already exists (a VPS image or an earlier attempt), its password locked (`!`) → it is reused: keys appended, sudoers file rewritten, and base bootstrap sets its password field to `*`, so key login still works under `UsePAM no`.
- The sudoers file fails `visudo -cf` → install access fails. The original sudoers configuration is untouched, and the password is kept for retry.
- `sudo -n true` fails as `proxier` (for example, `requiretty` set by the image) → install access fails with sudo's message.
- Self-check passes but every smoke-test attempt stalls after 16 KB → **failed**, with **Retry**, **Activate anyway** and **Retire** offered.
- **Activate anyway** → active. The first round gives `blocked`. A subscription that hides `blocked` servers hides it like any other, once it has been blocked for that subscription's grace period.
- A failure at step 7 → **Activate anyway** isn't offered.
- A conflicting A record at `nl-2.hosts.tikhonnnnn.com` (not created by Proxier) → DNS step fails, naming its value. **Retry → Overwrite** replaces it.
- DNS still not visible on 8.8.8.8 after 10 min → DNS step fails. **Retry** resumes at DNS and doesn't recreate the record.
- Let's Encrypt rate limit → install step fails with certbot's message.
- The container restarts during "Install Docker" → the job resumes at base bootstrap on the SSH port, and running it again is harmless.
- Self-check passes but the proxy test fails 3 times (e.g. the hosting network is already blocked in Russia) → server **failed** at smoke test, with the error.
- **Cancel** during DNS → job cancelled, server **failed**, DNS record kept until retirement or retry.
- Two servers created in the same country at the same moment → `nl-2` and `nl-3`, never two `nl-2`.
- A server fails provisioning as `nl-2` and is retired; the next new server in the Netherlands → `nl-3`.
- A server created when servers had locations, `ams-1` in NL → keeps its name; the next new server in the Netherlands is numbered after the highest `nl-` number, not after `ams-1`.
- Success → server active, health `unknown`, first check round within 5 minutes. Subscriptions with "add new servers automatically" include it, and the `server.activated` notification arrives.
