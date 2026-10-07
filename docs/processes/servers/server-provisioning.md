# Server provisioning

**Actors**: Admin; the provisioning job (queue `provisioning`); the new VPS; Cloudflare; Let's Encrypt (through the template's steps); the proxy test client.

Provisioning turns a freshly issued VPS (an IP and a root password) into an **active** server built from a template version. The admin fills in one form and watches the job. There is nothing to type over SSH and nothing to do in DNS. A server only becomes active once a real proxy connection from home works through it.

## Steps — the form

1. Servers → **New server**. Fields:
   - **IP address** (IPv4) and **SSH port** (default 22, under "advanced").
   - **Root password**, never shown again.
   - **Location**, preselected from the IP's country when it matches a known location, with **New location** inline.
   - **Template** and **version** (default version preselected), then the version's **parameters** as form fields with labels, help and defaults.
   - **Notes**.
2. While the admin types, the form shows what will be created: the server name (`nl-2`), the management and proxy hostnames, the DNS record, and the endpoints with their display names.
3. **Create**. Proxier checks:
   - no non-retired server has this IP;
   - the location and template version exist and the template isn't archived;
   - parameters pass validation;
   - Cloudflare is configured and a configured zone covers the hostname.

   Failures are shown on the form and nothing is created.
4. The server is created in **provisioning** with its name reserved, the root password goes encrypted into the job payload, and the job is queued. The browser goes to the server page, which shows the steps and the live log. → `server.created{ip, template_version}`

## Steps — the job

Each step is resumable. A retried job starts at the step that didn't finish.

1. **Preflight.**
   - Connect to `IP:port`.
   - Log in as root with the password. The host key is pinned now ([ADR 0007](../../adr/0007-agentless-ssh-with-pinned-host-keys.md)).
   - Read `/etc/os-release` and the architecture, and compare them with the template's `requires`.
   - Check that the manifest's TCP ports (e.g. 80, 443) are free (`ss -ltn`).
2. **Install access.** Proxier never works as root after this step. It uses a deploy user called **`proxier`**.
   - Install `sudo` if the image lacks it, then create the user `proxier` if it doesn't exist: a home directory, a login shell and a locked password.
   - Give it passwordless sudo through `/etc/sudoers.d/proxier` (`proxier ALL=(ALL) NOPASSWD:ALL`). Check the file with `visudo -cf` before moving it into place.
   - Append Proxier's public key and your personal keys to `~proxier/.ssh/authorized_keys`, skipping keys already there. The directory gets mode `700` and the file `600`, owned by `proxier`.
   - Open a **new** connection as `proxier` with Proxier's key, and run `sudo -n true`.
   - Once both work, erase the password from the job payload. From here on, nothing can use or show it.
3. **Base bootstrap** (the template's `base-bootstrap` step). Commands that need root run through `sudo -n`.
   - SSH hardening: `PasswordAuthentication no`, `KbdInteractiveAuthentication no`, `PermitRootLogin no`, via a drop-in file. sshd is reloaded, then key login as `proxier` and `sudo -n` are verified again. If verification fails, the change is reverted and the step fails.
   - `.hushlogin`, apt prerequisites, and Docker via get.docker.com unless Docker is already installed.
   - Firewall: allow the SSH port and the manifest's ports, then enable it.
4. **DNS.** Create the A record(s) for the hostname(s): DNS-only, TTL 60, comment `proxier:nl-2`. Wait until 1.1.1.1 and 8.8.8.8 both answer with the IP (timeout 10 min).
5. **Generated values.** Create every generated value the version declares (encrypted).
6. **Render and upload.** Render the version for this server and upload the files to the manifest's `dir` (`upload-files`). Record the deployment and its rendered files.
7. **Template install steps**, in order (in the seed template: issue the certificate, `compose up --pull`, wait for the decoy site to answer locally).
8. **Endpoints.** Render and store the endpoints and their display names.
9. **Self-check.** Run the template's checks once. All must pass.
10. **Smoke test.** Run the proxy test from home through every endpoint (up to 3 attempts, 20 s apart). Every endpoint must pass.
11. **Activate.** The server becomes **active**, its health state becomes `unknown`, and the scheduler starts its checks. Subscriptions with "add new servers automatically" append it. → `server.activated{proxy_test}` (notifies "nl-2 is ready")

The job makes one attempt and never retries by itself: a retry may need input, so it is the admin's. Any step failing stops the job. The server becomes **failed**, and the page shows the step, the error and the log. → `server.provisioning_failed{step, error}` (notifies). **Cancelling** the job ends the same way with the error `cancelled` and `cancelled: true` in the event, which does not notify; DNS records already made stay until retry or retirement.

## Steps — after a failure

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

## Edge cases (each is a test)

- IP already used by active `nl-1` → refused on the form, nothing created.
- Wrong root password → fails at preflight with "authentication failed". The password stays encrypted for retry. **Retry** asks for it again.
- IP unreachable (timeout) → fails at preflight. The DNS is untouched.
- Port 443 already in use on the VPS → fails at preflight, naming the process from `ss`.
- Ubuntu 20.04 against `requires: ubuntu-22.04+` → fails at preflight with the OS found.
- The new key login fails after keys were installed (e.g. `authorized_keys` permissions) → install access fails. The password is kept for retry.
- The sshd reload breaks key login → the drop-in is removed, sshd reloaded, the step fails, and Proxier can still log in.
- Docker already installed → that sub-step is skipped and logged as such.
- A `proxier` user already exists (a VPS image or an earlier attempt) → it is reused: keys appended, sudoers file rewritten, nothing else changed.
- The sudoers file fails `visudo -cf` → install access fails. The original sudoers configuration is untouched, and the password is kept for retry.
- `sudo -n true` fails as `proxier` (for example, `requiretty` set by the image) → install access fails with sudo's message.
- Self-check passes but every smoke-test attempt stalls after 16 KB → **failed**, with **Retry**, **Activate anyway** and **Retire** offered.
- **Activate anyway** → active. The first round gives `blocked`. A subscription with "hide unhealthy" doesn't serve it.
- A failure at step 7 → **Activate anyway** isn't offered.
- A conflicting A record at `nl-2.hosts.tikhonnnnn.com` (not created by Proxier) → DNS step fails, naming its value. **Retry → Overwrite** replaces it.
- DNS still not visible on 8.8.8.8 after 10 min → DNS step fails. **Retry** resumes at DNS and doesn't recreate the record.
- Let's Encrypt rate limit → install step fails with certbot's message.
- The container restarts during "Install Docker" → the job resumes at base bootstrap, and running it again is harmless.
- Self-check passes but the proxy test fails 3 times (e.g. the hosting network is already blocked in Russia) → server **failed** at smoke test, with the error.
- **Cancel** during DNS → job cancelled, server **failed**, DNS record kept until retirement or retry.
- Two servers created in the same location at the same moment → `nl-2` and `nl-3`, never two `nl-2`.
- A server fails provisioning as `nl-2` and is retired; the next new server in `nl` → `nl-3`.
- Success → server active, health `unknown`, first check round within 5 minutes. Subscriptions with "add new servers automatically" include it, and the `server.activated` notification arrives.
